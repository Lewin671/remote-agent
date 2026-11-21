package security

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"errors"
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/entropy"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

const (
	// BatchVerifyThreshold is the signature count above which WebCrypto offload is used.
	BatchVerifyThreshold = 8
	// BatchAeadThreshold is the ciphertext/plaintext size above which WebCrypto offload is used.
	BatchAeadThreshold = 16 * 1024
)

var (
	ErrNotInitialized             = errors.New("security: record not initialized")
	ErrNotMember                  = errors.New("security: device is not a trusted member")
	ErrLeaseExpired               = errors.New("security: lease expired")
	ErrClockSkewed                = errors.New("security: clock behind account state")
	ErrRecoveryHistoryUnavailable = errors.New("security: content predates account recovery")
	ErrUnsupportedHistory         = errors.New("security: retired envelope version")
	ErrNeedEntropy                = errors.New("security: waiting for OS entropy")
)

// ErrorCode returns the wire/UI classification code for an opening or sealing error.
func ErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrRecoveryHistoryUnavailable):
		return "recovery_unavailable"
	case errors.Is(err, ErrUnsupportedHistory):
		return "unsupported_history"
	case errors.Is(err, ErrClockSkewed), errors.Is(err, devicesec.ErrClock):
		return "clock"
	case errors.Is(err, ErrLeaseExpired), errors.Is(err, devicesec.ErrStale):
		return "stale"
	case errors.Is(err, devicesec.ErrUntrusted), errors.Is(err, ErrNotMember):
		return "untrusted"
	case errors.Is(err, devicesec.ErrCapacity):
		return "capacity"
	default:
		return "invalid"
	}
}

// Snapshot mirrors the verified security summary needed by sync, screens, and transport.
type Snapshot struct {
	Loaded                 bool
	IsMember               bool
	Removed                bool
	Cleared                bool
	SelfDeviceID           string
	AuthorityDeviceID      string
	TrustedMachines        map[string]bool
	TrustedClients         map[string]bool
	ArchiveMachines        map[string]bool
	HeadEpoch              uint64
	ContentEpoch           uint64
	LeaseEndMs             int64
	ClockSkewed            bool
	RecoveryPending        bool
	RecoveryReadyMs        int64
	RecoveryObjectsPending bool
	NetworkGen             uint64
	VerifiedNetGen         uint64
}

// OpenItem describes one envelope to verify, decrypt, and validate cleanly.
type OpenItem struct {
	Env             *v1.Envelope
	ExpectedPurpose devicesec.Purpose
	MachineID       string
	SessionID       string
	RequestID       string
	Seq             uint64
	History         bool
	OverrideSeed    []byte
	Replacements    []*v1.MachineReplacementProof
}

// OpenResult is the per-item outcome from OpenBatch.
type OpenResult struct {
	Header devicesec.Header
	Plain  []byte
	Body   any
	Err    error
}

// SealResult is returned when asynchronous sealing finishes.
type SealResult struct {
	Envelope       *v1.Envelope
	RequestKeyHash []byte
	PayloadHash    []byte
	Err            error
}

// Engine holds verified client device security state and orchestrates async
// Key* / CryptoBatch effects without touching private keys or OS randomness directly.
type Engine struct {
	entropy *entropy.Pool

	configured     bool
	inlineCrypto   bool
	securityLocked bool
	account        string
	selfDeviceID   string

	loaded                 bool
	isMember               bool
	removed                bool
	cleared                bool
	clockSkewed            bool
	recordErr              string
	netGen                 uint64
	verifiedNetGen         uint64
	lastPushSeq            uint64
	floorMs                int64
	recoveryPending        bool
	recoveryReadyMs        int64
	recoveryObjectsPending bool

	pin          devicesec.Pin
	states       []devicesec.State
	recoveryAt   []bool
	byHash       map[devicesec.Hash]devicesec.State
	hasHead      bool
	head         devicesec.State
	headHash     devicesec.Hash
	hasLease     bool
	lease        devicesec.LeaseCertificate
	epochSecrets map[uint64][32]byte
	minEpochSec  uint64
	sessionSeeds map[string][32]byte
	peerShared   map[[32]byte][32]byte

	lastNowMs    int64
	verifiedAtMs int64
	unlocking    bool
	unlockGen  uint64
	onUnlock   []func(nowMs int64, err error)
	nextOpID   uint64
	signWaiter map[uint64]func(sig []byte, err error)
	agreeWait  map[uint64]func(shared []byte, err error)
	batchWait  map[uint64]func(res []*corev1.SecurityCryptoBatchResult, err error)

	out []*corev1.Effect
}

// NewEngine constructs a security Engine sharing the host's OS entropy pool.
func NewEngine(pool *entropy.Pool) *Engine {
	if pool == nil {
		pool = &entropy.Pool{}
	}
	return &Engine{
		entropy:      pool,
		byHash:       make(map[devicesec.Hash]devicesec.State),
		epochSecrets: make(map[uint64][32]byte),
		sessionSeeds: make(map[string][32]byte),
		peerShared:   make(map[[32]byte][32]byte),
		signWaiter:   make(map[uint64]func([]byte, error)),
		agreeWait:    make(map[uint64]func([]byte, error)),
		batchWait:    make(map[uint64]func([]*corev1.SecurityCryptoBatchResult, error)),
	}
}

// Locked reports whether real security initialization has locked out demo seeding.
func (e *Engine) Locked() bool {
	return e != nil && e.securityLocked
}

// DrainEffects returns and clears effects queued during the current step.
func (e *Engine) DrainEffects() []*corev1.Effect {
	out := e.out
	e.out = nil
	return out
}

func (e *Engine) allocOp() uint64 {
	e.nextOpID++
	return e.nextOpID
}

func (e *Engine) touchNow(nowMs int64) {
	if nowMs > e.lastNowMs {
		e.lastNowMs = nowMs
	}
}

func (e *Engine) effectiveNow(nowMs int64) uint64 {
	n := max(max(nowMs, e.lastNowMs), e.floorMs)
	if n < 0 {
		return 0
	}
	return uint64(n)
}

// Configure sets client identity and WebCrypto batch policy.
func (e *Engine) Configure(ev *corev1.SecurityConfigEvent) {
	if ev == nil {
		return
	}
	e.securityLocked = true
	e.configured = true
	e.account = ev.Account
	e.selfDeviceID = ev.DeviceId
	e.inlineCrypto = ev.InlineCrypto
}

// HandleKeySigned dispatches a KeySign completion.
func (e *Engine) HandleKeySigned(nowMs int64, ev *corev1.SecurityKeySignedEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	fn := e.signWaiter[ev.OpId]
	delete(e.signWaiter, ev.OpId)
	if fn != nil {
		if ev.Error != "" {
			fn(nil, errors.New(ev.Error))
		} else {
			fn(ev.Signature, nil)
		}
	}
	return e.DrainEffects()
}

// HandleKeyAgreed dispatches a KeyAgree completion.
func (e *Engine) HandleKeyAgreed(nowMs int64, ev *corev1.SecurityKeyAgreedEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	fn := e.agreeWait[ev.OpId]
	delete(e.agreeWait, ev.OpId)
	if fn != nil {
		if ev.Error != "" {
			fn(nil, errors.New(ev.Error))
		} else {
			fn(ev.Shared, nil)
		}
	}
	return e.DrainEffects()
}

// HandleCryptoBatchDone dispatches a CryptoBatch completion.
func (e *Engine) HandleCryptoBatchDone(nowMs int64, ev *corev1.SecurityCryptoBatchDoneEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	fn := e.batchWait[ev.OpId]
	delete(e.batchWait, ev.OpId)
	if fn != nil {
		if ev.Error != "" {
			fn(nil, errors.New(ev.Error))
		} else {
			fn(ev.Results, nil)
		}
	}
	return e.DrainEffects()
}

// RequestKeySign issues an async KeySign effect for DEVICE_ED (shared with transport.Coordinator).
func (e *Engine) RequestKeySign(msg []byte, fn func(sig []byte, err error)) {
	opID := e.allocOp()
	e.signWaiter[opID] = fn
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityKeySign{
			SecurityKeySign: &corev1.SecurityKeySignEffect{
				OpId:    opID,
				KeyKind: corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED,
				Message: slices.Clone(msg),
			},
		},
	})
}

// RequestKeyAgree issues or resolves from cache an X25519 ECDH agreement for DEVICE_X.
func (e *Engine) RequestKeyAgree(peerX [32]byte, cachePeer bool, fn func(shared []byte, err error)) {
	if cachePeer {
		if sec, ok := e.peerShared[peerX]; ok {
			fn(slices.Clone(sec[:]), nil)
			return
		}
	}
	opID := e.allocOp()
	e.agreeWait[opID] = func(shared []byte, err error) {
		if err == nil && cachePeer && len(shared) == 32 {
			var k [32]byte
			copy(k[:], shared)
			e.peerShared[peerX] = k
		}
		fn(shared, err)
	}
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityKeyAgree{
			SecurityKeyAgree: &corev1.SecurityKeyAgreeEffect{
				OpId:       opID,
				KeyKind:    corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_X,
				PeerPublic: slices.Clone(peerX[:]),
			},
		},
	})
}

// ApplyRecord validates raw chain states, recovery proofs, and lease from the shell
// and kicks off head grant unlocking via KeyAgree if the current ContentEpoch is unopened.
func (e *Engine) ApplyRecord(nowMs int64, ev *corev1.SecurityRecordEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	if ev.OpId == 0 && ev.PushSeq != 0 && ev.PushSeq <= e.lastPushSeq {
		return nil
	}
	if ev.OpId == 0 && ev.PushSeq > e.lastPushSeq {
		e.lastPushSeq = ev.PushSeq
	}
	e.securityLocked = true
	if ev.NetworkGeneration > e.netGen {
		e.netGen = ev.NetworkGeneration
	}
	if ev.FloorMs > e.floorMs {
		e.floorMs = ev.FloorMs
	}
	e.recoveryPending = ev.RecoveryPending
	e.recoveryReadyMs = ev.RecoveryReadyMs
	e.recoveryObjectsPending = ev.RecoveryObjectsPending

	if ev.Removed || ev.Cleared || ev.Error != "" {
		e.loaded = ev.Removed || ev.Error == ""
		if ev.Cleared {
			e.loaded = false
		}
		e.isMember = false
		e.removed = ev.Removed
		e.cleared = ev.Cleared
		e.recordErr = ev.Error
		if ev.Removed || ev.Cleared {
			e.resetChainState()
			e.flushUnlockWaiters(e.lastNowMs, ErrNotMember)
		}
		return e.DrainEffects()
	}

	if len(ev.Domain) != 32 || len(ev.Genesis) != 32 || len(ev.States) == 0 {
		e.recordErr = "invalid security record"
		e.flushUnlockWaiters(e.lastNowMs, devicesec.ErrInvalid)
		return e.DrainEffects()
	}

	acct := ev.Account
	if acct == "" {
		acct = e.account
	}
	var dom, gen devicesec.Hash
	copy(dom[:], ev.Domain)
	copy(gen[:], ev.Genesis)
	pin := devicesec.Pin{Account: acct, Domain: dom, Genesis: gen}

	parsedStates, byHash, proofPresent, err := parseAndVerifyChain(pin, ev.States, ev.RecoveryProofs)
	if err != nil {
		e.recordErr = err.Error()
		e.flushUnlockWaiters(e.lastNowMs, err)
		return e.DrainEffects()
	}

	head := parsedStates[len(parsedStates)-1]
	headHash, _ := head.Hash()

	var lease devicesec.LeaseCertificate
	var hasLease bool
	var clockSkew bool
	effNow := e.effectiveNow(nowMs)
	if head.Core.IssuedAt > effNow+devicesec.MaxClockSkew {
		clockSkew = true
	}
	if len(ev.Lease) > 0 {
		lease, err = devicesec.ParseLease(ev.Lease)
		if err != nil {
			e.recordErr = err.Error()
			e.flushUnlockWaiters(e.lastNowMs, err)
			return e.DrainEffects()
		}
		if err := lease.Verify(head); err != nil {
			e.recordErr = err.Error()
			e.flushUnlockWaiters(e.lastNowMs, err)
			return e.DrainEffects()
		}
		hasLease = true
		if err := lease.Valid(head, effNow); errors.Is(err, devicesec.ErrClock) {
			clockSkew = true
		}
	}

	samePin := e.hasHead && e.pin == pin
	sameHead := samePin && e.headHash == headHash
	extendedFromPrev := samePin && len(parsedStates) >= len(e.states)
	if extendedFromPrev && len(e.states) > 0 {
		prevLastHash, _ := e.states[len(e.states)-1].Hash()
		candHash, _ := parsedStates[len(e.states)-1].Hash()
		extendedFromPrev = prevLastHash == candHash
	}

	e.pin = pin
	e.states = parsedStates
	e.recoveryAt = proofPresent
	e.byHash = byHash
	e.head = head
	e.headHash = headHash
	e.hasHead = true
	e.hasLease = hasLease
	e.lease = lease
	e.clockSkewed = clockSkew
	e.removed = false
	e.cleared = false
	e.recordErr = ""
	e.loaded = true
	if nowMs > 0 {
		e.verifiedAtMs = nowMs
	} else {
		e.verifiedAtMs = e.lastNowMs
	}
	if ev.NetworkGeneration > e.verifiedNetGen {
		e.verifiedNetGen = ev.NetworkGeneration
	}

	if !samePin || !extendedFromPrev {
		clear(e.epochSecrets)
		clear(e.sessionSeeds)
		clear(e.peerShared)
		e.minEpochSec = 0
		e.unlockGen++
		e.unlocking = false
	} else if !sameHead {
		clear(e.peerShared)
	}

	_, e.isMember = head.Core.Member(e.selfDeviceID)
	if !e.isMember {
		clear(e.epochSecrets)
		clear(e.peerShared)
		e.unlocking = false
		e.flushUnlockWaiters(e.lastNowMs, ErrNotMember)
		return e.DrainEffects()
	}

	if _, haveHeadSecret := e.epochSecrets[head.Core.ContentEpoch]; haveHeadSecret {
		if err := e.fillPrevSecrets(); err != nil {
			delete(e.epochSecrets, head.Core.ContentEpoch)
			e.isMember = false
			e.recordErr = err.Error()
			e.flushUnlockWaiters(e.lastNowMs, err)
			return e.DrainEffects()
		}
		e.flushUnlockWaiters(e.lastNowMs, nil)
		return e.DrainEffects()
	}

	if !e.unlocking {
		e.startHeadUnlock()
	}
	return e.DrainEffects()
}

func (e *Engine) resetChainState() {
	e.hasHead = false
	e.states = nil
	e.recoveryAt = nil
	clear(e.byHash)
	e.hasLease = false
	e.lease = devicesec.LeaseCertificate{}
	clear(e.epochSecrets)
	clear(e.sessionSeeds)
	clear(e.peerShared)
	e.minEpochSec = 0
	e.verifiedAtMs = 0
	e.unlocking = false
	e.unlockGen++
}

func parseAndVerifyChain(pin devicesec.Pin, rawStates, rawProofs [][]byte) ([]devicesec.State, map[devicesec.Hash]devicesec.State, []bool, error) {
	proofByState := make(map[devicesec.Hash]devicesec.RecoveryStatement, len(rawProofs))
	for _, raw := range rawProofs {
		if len(raw) == 0 {
			continue
		}
		rs, err := devicesec.ParseRecoveryStatement(raw)
		if err != nil {
			return nil, nil, nil, err
		}
		if _, dup := proofByState[rs.StateHash]; dup {
			return nil, nil, nil, devicesec.ErrInvalid
		}
		proofByState[rs.StateHash] = rs
	}

	states := make([]devicesec.State, len(rawStates))
	byHash := make(map[devicesec.Hash]devicesec.State, len(rawStates))
	proofPresent := make([]bool, len(rawStates))
	for i, raw := range rawStates {
		s, err := devicesec.ParseState(raw)
		if err != nil {
			return nil, nil, nil, err
		}
		h, err := s.Hash()
		if err != nil {
			return nil, nil, nil, err
		}
		if i == 0 {
			if err := pin.VerifyGenesis(s); err != nil {
				return nil, nil, nil, err
			}
		} else {
			if err := pin.Verify(s); err != nil {
				return nil, nil, nil, err
			}
			var rec *devicesec.RecoveryStatement
			if rs, ok := proofByState[h]; ok {
				rec = &rs
				proofPresent[i] = true
			}
			if err := devicesec.VerifyTransition(states[i-1], s, rec); err != nil {
				return nil, nil, nil, err
			}
		}
		states[i] = s
		byHash[h] = s
	}
	return states, byHash, proofPresent, nil
}

func (e *Engine) startHeadUnlock() {
	enc, _, err := e.pin.GrantChallenge(e.head, e.selfDeviceID)
	if err != nil {
		e.flushUnlockWaiters(e.lastNowMs, err)
		return
	}
	e.unlocking = true
	gen := e.unlockGen
	epoch := e.head.Core.ContentEpoch
	e.RequestKeyAgree(enc, false, func(shared []byte, err error) {
		if gen != e.unlockGen {
			return
		}
		e.unlocking = false
		if err != nil {
			e.flushUnlockWaiters(e.lastNowMs, err)
			return
		}
		sec, err := e.pin.OpenGrantWithDH(e.head, e.selfDeviceID, shared)
		if err != nil || len(sec) != 32 {
			if err == nil {
				err = devicesec.ErrUntrusted
			}
			e.flushUnlockWaiters(e.lastNowMs, err)
			return
		}
		var arr [32]byte
		copy(arr[:], sec)
		e.epochSecrets[epoch] = arr
		if err := e.fillPrevSecrets(); err != nil {
			delete(e.epochSecrets, epoch)
			e.isMember = false
			e.recordErr = err.Error()
			e.flushUnlockWaiters(e.lastNowMs, err)
			return
		}
		e.flushUnlockWaiters(e.lastNowMs, nil)
	})
}

func (e *Engine) fillPrevSecrets() error {
	e.minEpochSec = e.head.Core.ContentEpoch
	for i := len(e.states) - 1; i >= 1; i-- {
		s := e.states[i]
		prev := e.states[i-1]
		curEp := s.Core.ContentEpoch
		prevEp := prev.Core.ContentEpoch
		if prevEp == curEp || len(s.PrevSecret) == 0 {
			if _, ok := e.epochSecrets[prevEp]; ok && (e.minEpochSec == 0 || prevEp < e.minEpochSec) {
				e.minEpochSec = prevEp
			}
			continue
		}
		curSec, ok := e.epochSecrets[curEp]
		if !ok {
			continue
		}
		oldSec, err := devicesec.OpenPrevSecret(curSec[:], s.PrevSecret)
		if err != nil || len(oldSec) != 32 {
			return devicesec.ErrUntrusted
		}
		if prevSec, already := e.epochSecrets[prevEp]; already {
			if !bytes.Equal(oldSec, prevSec[:]) {
				return devicesec.ErrUntrusted
			}
		} else {
			var arr [32]byte
			copy(arr[:], oldSec)
			e.epochSecrets[prevEp] = arr
		}
		if e.minEpochSec == 0 || prevEp < e.minEpochSec {
			e.minEpochSec = prevEp
		}
	}
	return nil
}

func (e *Engine) flushUnlockWaiters(nowMs int64, err error) {
	if len(e.onUnlock) == 0 {
		return
	}
	waiters := e.onUnlock
	e.onUnlock = nil
	for _, fn := range waiters {
		fn(nowMs, err)
	}
}

func (e *Engine) withUnlocked(nowMs int64, fn func(nowMs int64, err error)) {
	if !e.loaded || !e.hasHead {
		if e.unlocking {
			e.onUnlock = append(e.onUnlock, fn)
			return
		}
		fn(nowMs, ErrNotInitialized)
		return
	}
	if !e.isMember {
		fn(nowMs, ErrNotMember)
		return
	}
	if _, ok := e.epochSecrets[e.head.Core.ContentEpoch]; ok {
		fn(nowMs, nil)
		return
	}
	if e.unlocking {
		e.onUnlock = append(e.onUnlock, fn)
		return
	}
	e.onUnlock = append(e.onUnlock, fn)
	e.startHeadUnlock()
}

// Snapshot builds the verified security summary snapshot for sync & views.
func (e *Engine) Snapshot() Snapshot {
	snap := Snapshot{
		Loaded:                 e.loaded,
		IsMember:               e.isMember,
		Removed:                e.removed,
		Cleared:                e.cleared,
		SelfDeviceID:           e.selfDeviceID,
		TrustedMachines:        make(map[string]bool),
		TrustedClients:         make(map[string]bool),
		ArchiveMachines:        make(map[string]bool),
		ClockSkewed:            e.clockSkewed,
		RecoveryPending:        e.recoveryPending,
		RecoveryReadyMs:        e.recoveryReadyMs,
		RecoveryObjectsPending: e.recoveryObjectsPending,
		NetworkGen:             e.netGen,
		VerifiedNetGen:         e.verifiedNetGen,
	}
	if !e.loaded || !e.hasHead {
		return snap
	}
	snap.AuthorityDeviceID = e.head.Core.AuthorityID
	snap.HeadEpoch = e.head.Core.Epoch
	snap.ContentEpoch = e.head.Core.ContentEpoch
	if e.hasLease {
		snap.LeaseEndMs = int64(e.lease.NotAfter)
	}
	for _, s := range e.states {
		for _, m := range s.Core.Members {
			if m.Kind == devicesec.Machine {
				snap.ArchiveMachines[m.ID] = true
			}
		}
	}
	for _, m := range e.head.Core.Members {
		switch m.Kind {
		case devicesec.Machine:
			snap.TrustedMachines[m.ID] = true
			delete(snap.ArchiveMachines, m.ID)
		case devicesec.Client:
			snap.TrustedClients[m.ID] = true
		}
	}
	return snap
}

// CoordMemberInfo describes one trusted member key entry for transport admission.
type CoordMemberInfo struct {
	ID    string
	EdPub [32]byte
	XPub  [32]byte
	Self  bool
	Kind  uint32
}

// CoordInfo carries verified chain identity/member keys for transport admission without importing transport.
type CoordInfo struct {
	Member        bool
	Account       string
	Domain        [32]byte
	Head          string
	HeadHash      [32]byte
	Epoch         uint64
	StartedAt     int64
	LeaseNotAfter int64
	SelfID        string
	SelfEdPub     [32]byte
	SelfXPub      [32]byte
	Members       []CoordMemberInfo
}

// CoordInfo builds the transport Coordinator security info from verified Core state.
func (e *Engine) CoordInfo() (CoordInfo, bool) {
	if e == nil || !e.loaded || !e.hasHead || e.cleared {
		return CoordInfo{}, false
	}
	members := make([]CoordMemberInfo, 0, len(e.head.Core.Members))
	var selfEd, selfX [32]byte
	for _, m := range e.head.Core.Members {
		isSelf := m.ID == e.selfDeviceID
		if isSelf {
			selfEd = m.EdPublic
			selfX = m.XPublic
		}
		members = append(members, CoordMemberInfo{
			ID:    m.ID,
			EdPub: m.EdPublic,
			XPub:  m.XPublic,
			Self:  isSelf,
			Kind:  uint32(m.Kind),
		})
	}
	var leaseNotAfter int64
	if e.hasLease {
		leaseNotAfter = int64(e.lease.NotAfter)
	}
	return CoordInfo{
		Member:        e.isMember,
		Account:       e.pin.Account,
		Domain:        e.pin.Domain,
		Head:          e.headHash.String(),
		HeadHash:      e.headHash,
		Epoch:         e.head.Core.Epoch,
		StartedAt:     e.verifiedAtMs,
		LeaseNotAfter: leaseNotAfter,
		SelfID:        e.selfDeviceID,
		SelfEdPub:     selfEd,
		SelfXPub:      selfX,
		Members:       members,
	}, true
}

// SeedSessionKey caches a verified session seed so subsequent Event openings reuse it.
func (e *Engine) SeedSessionKey(sessionID string, seed []byte) {
	if sessionID != "" && len(seed) == 32 {
		var k [32]byte
		copy(k[:], seed)
		e.sessionSeeds[sessionID] = k
	}
}

// SessionSeed returns a copy of the unlocked 32-byte seed for sessionID, if cached.
func (e *Engine) SessionSeed(sessionID string) ([]byte, bool) {
	k, ok := e.sessionSeeds[sessionID]
	if !ok {
		return nil, false
	}
	return slices.Clone(k[:]), true
}

func (e *Engine) checkEnvelopeVersion(env *v1.Envelope) error {
	if env == nil {
		return devicesec.ErrInvalid
	}
	if len(env.SecurityHeader) == 0 && len(env.Header) > 0 {
		var hdr v1.Header
		if hdr.Unmarshal(env.Header) == nil && hdr.Version != 0 && hdr.Version < devicesec.Version {
			return ErrUnsupportedHistory
		}
	}
	if len(env.SecurityHeader) >= 4 {
		v := uint32(env.SecurityHeader[0])<<24 | uint32(env.SecurityHeader[1])<<16 |
			uint32(env.SecurityHeader[2])<<8 | uint32(env.SecurityHeader[3])
		if v != 0 && v < devicesec.Version {
			return ErrUnsupportedHistory
		}
	}
	return nil
}

func (e *Engine) checkLiveLease(nowMs int64) error {
	if e.clockSkewed {
		return ErrClockSkewed
	}
	if !e.hasLease {
		return ErrLeaseExpired
	}
	if err := e.lease.Valid(e.head, e.effectiveNow(nowMs)); err != nil {
		if errors.Is(err, devicesec.ErrClock) {
			return ErrClockSkewed
		}
		if errors.Is(err, devicesec.ErrStale) {
			return ErrLeaseExpired
		}
		return err
	}
	return nil
}

func (e *Engine) isRecoveryUnavailable(item OpenItem, h devicesec.Header) bool {
	if !item.History || h.Purpose != devicesec.Event {
		return false
	}
	for i, s := range e.states {
		if i < len(e.recoveryAt) && e.recoveryAt[i] &&
			s.Core.Epoch > h.Epoch &&
			s.Core.ContentEpoch > h.ContentEpoch &&
			len(s.PrevSecret) == 0 {
			return true
		}
	}
	return false
}

// CanSeal checks synchronously whether sending a new authenticated object is currently allowed.
func (e *Engine) CanSeal(nowMs int64) error {
	if !e.loaded || !e.hasHead {
		return ErrNotInitialized
	}
	if !e.isMember {
		return ErrNotMember
	}
	if e.clockSkewed {
		return ErrClockSkewed
	}
	if err := e.checkLiveLease(nowMs); err != nil {
		return err
	}
	if _, ok := e.epochSecrets[e.head.Core.ContentEpoch]; !ok {
		return ErrNotInitialized
	}
	return nil
}

// OpenBatch verifies signatures, decrypts, and validates all envelopes in items,
// using CryptoBatch on Web when verify > 8 or any ciphertext > 16 KiB.
func (e *Engine) OpenBatch(nowMs int64, items []OpenItem, onDone func(results []OpenResult)) []*corev1.Effect {
	e.touchNow(nowMs)
	e.withUnlocked(nowMs, func(curNow int64, unlockErr error) {
		results := make([]OpenResult, len(items))
		if unlockErr != nil {
			for i := range results {
				results[i].Err = unlockErr
			}
			onDone(results)
			return
		}

		// Precompute replacement origin maps per (machineID, sessionID) lazily when SessionID is present.
		type replCacheKey struct {
			machineID string
			sessionID string
		}
		type preparedOpen struct {
			idx       int
			item      OpenItem
			ct        devicesec.Ciphertext
			header    devicesec.Header
			edPub     [32]byte
			originMsg []byte
		}
		replCache := make(map[replCacheKey]map[string]bool)
		var toVerify []preparedOpen

		for i, item := range items {
			if verErr := e.checkEnvelopeVersion(item.Env); verErr != nil {
				results[i].Err = verErr
				continue
			}
			ct, err := FromProtoEnvelope(item.Env)
			if err != nil {
				results[i].Err = err
				continue
			}
			h, err := devicesec.ParseHeader(ct.Header)
			if err != nil {
				results[i].Err = err
				continue
			}
			if item.ExpectedPurpose != 0 && h.Purpose != item.ExpectedPurpose {
				results[i].Err = devicesec.ErrInvalid
				continue
			}
			if item.SessionID != "" && h.Session != item.SessionID {
				results[i].Err = devicesec.ErrUntrusted
				continue
			}
			if item.RequestID != "" && h.Request != item.RequestID {
				results[i].Err = devicesec.ErrUntrusted
				continue
			}
			if item.Seq != 0 && h.Seq != item.Seq {
				results[i].Err = devicesec.ErrUntrusted
				continue
			}
			if item.MachineID != "" && h.Machine != item.MachineID {
				if !item.History || len(item.Replacements) == 0 {
					results[i].Err = devicesec.ErrUntrusted
					continue
				}
				var origins map[string]bool
				var cached bool
				ck := replCacheKey{machineID: item.MachineID, sessionID: item.SessionID}
				if item.SessionID != "" {
					origins, cached = replCache[ck]
				}
				if !cached {
					origins, err = MachineReplacementOrigins(e.pin.Account, item.MachineID, e.states, item.Replacements)
					if err != nil {
						results[i].Err = err
						continue
					}
					if item.SessionID != "" {
						replCache[ck] = origins
					}
				}
				if !origins[h.Machine] {
					results[i].Err = devicesec.ErrUntrusted
					continue
				}
			}
			if !item.History {
				if err := e.checkLiveLease(curNow); err != nil {
					results[i].Err = err
					continue
				}
			}
			s, ok := e.byHash[h.State]
			if !ok {
				results[i].Err = devicesec.ErrStale
				continue
			}
			vh, edPub, originMsg, err := ct.OriginVerifyInput(s, e.head, !item.History, e.effectiveNow(curNow))
			if err != nil {
				results[i].Err = err
				continue
			}
			toVerify = append(toVerify, preparedOpen{
				idx:       i,
				item:      item,
				ct:        ct,
				header:    vh,
				edPub:     edPub,
				originMsg: originMsg,
			})
		}

		if len(toVerify) == 0 {
			onDone(results)
			return
		}

		decryptVerified := func(verified []preparedOpen) {
			var batchAEADIdx []int
			var batchAEADItems []*corev1.SecurityCryptoBatchItem
			for vi, p := range verified {
				sec, hasSec := e.epochSecrets[p.header.ContentEpoch]
				if !hasSec {
					if e.isRecoveryUnavailable(p.item, p.header) {
						results[p.idx].Err = ErrRecoveryHistoryUnavailable
					} else {
						results[p.idx].Err = devicesec.ErrStale
					}
					continue
				}
				var seed []byte
				if p.header.Purpose == devicesec.Event || p.header.Purpose == devicesec.Image ||
					p.header.Purpose == devicesec.TerminalOutput || p.header.Purpose == devicesec.TerminalInput ||
					p.header.Purpose == devicesec.TerminalResize || p.header.Purpose == devicesec.TerminalHistory {
					if len(p.item.OverrideSeed) == 32 {
						seed = p.item.OverrideSeed
					} else if cachedSeed, ok := e.sessionSeeds[p.header.Session]; ok {
						sCopy := cachedSeed
						seed = sCopy[:]
					} else {
						results[p.idx].Err = devicesec.ErrInvalid
						continue
					}
				}
				if !e.inlineCrypto && len(p.ct.Cipher) > BatchAeadThreshold {
					_, key, err := devicesec.DeriveCipherKey(sec[:], seed, p.ct)
					if err != nil {
						results[p.idx].Err = err
						continue
					}
					batchAEADIdx = append(batchAEADIdx, vi)
					batchAEADItems = append(batchAEADItems, &corev1.SecurityCryptoBatchItem{
						Kind: &corev1.SecurityCryptoBatchItem_AeadOpen{
							AeadOpen: &corev1.CryptoBatchAeadOpenItem{
								Key:        key,
								Nonce:      slices.Clone(p.ct.Nonce),
								Aad:        slices.Clone(p.ct.Header),
								Ciphertext: slices.Clone(p.ct.Cipher),
							},
						},
					})
				} else {
					_, plain, err := devicesec.OpenCipher(sec[:], seed, p.ct)
					if err != nil {
						results[p.idx].Err = err
						continue
					}
					body, unpacked, err := ValidateAndDecodeBody(p.header, plain)
					if err != nil {
						results[p.idx].Err = err
						continue
					}
					if p.header.Purpose == devicesec.SessionKey {
						if b, ok := body.([]byte); ok {
							e.SeedSessionKey(p.header.Session, b)
						}
					}
					results[p.idx].Plain = unpacked
					results[p.idx].Body = body
				}
			}

			if len(batchAEADItems) == 0 {
				onDone(results)
				return
			}
			opID := e.allocOp()
			e.batchWait[opID] = func(batchRes []*corev1.SecurityCryptoBatchResult, batchErr error) {
				for bi, vi := range batchAEADIdx {
					p := verified[vi]
					if batchErr != nil {
						results[p.idx].Err = batchErr
						continue
					}
					if bi >= len(batchRes) || batchRes[bi] == nil || !batchRes[bi].Ok {
						results[p.idx].Err = devicesec.ErrUntrusted
						continue
					}
					body, unpacked, err := ValidateAndDecodeBody(p.header, batchRes[bi].Data)
					if err != nil {
						results[p.idx].Err = err
						continue
					}
					if p.header.Purpose == devicesec.SessionKey {
						if b, ok := body.([]byte); ok {
							e.SeedSessionKey(p.header.Session, b)
						}
					}
					results[p.idx].Plain = unpacked
					results[p.idx].Body = body
				}
				onDone(results)
			}
			e.out = append(e.out, &corev1.Effect{
				Effect: &corev1.Effect_SecurityCryptoBatch{
					SecurityCryptoBatch: &corev1.SecurityCryptoBatchEffect{
						OpId:  opID,
						Items: batchAEADItems,
					},
				},
			})
		}

		if !e.inlineCrypto && len(toVerify) > BatchVerifyThreshold {
			batchItems := make([]*corev1.SecurityCryptoBatchItem, len(toVerify))
			for vi, p := range toVerify {
				batchItems[vi] = &corev1.SecurityCryptoBatchItem{
					Kind: &corev1.SecurityCryptoBatchItem_Verify{
						Verify: &corev1.CryptoBatchVerifyItem{
							PublicKey: slices.Clone(p.edPub[:]),
							Message:   p.originMsg,
							Signature: slices.Clone(p.ct.Origin),
						},
					},
				}
			}
			opID := e.allocOp()
			e.batchWait[opID] = func(batchRes []*corev1.SecurityCryptoBatchResult, batchErr error) {
				var okItems []preparedOpen
				for vi, p := range toVerify {
					if batchErr != nil {
						results[p.idx].Err = batchErr
						continue
					}
					if vi >= len(batchRes) || batchRes[vi] == nil || !batchRes[vi].Ok {
						results[p.idx].Err = devicesec.ErrUntrusted
						continue
					}
					results[p.idx].Header = p.header
					okItems = append(okItems, p)
				}
				if len(okItems) == 0 {
					onDone(results)
					return
				}
				decryptVerified(okItems)
			}
			e.out = append(e.out, &corev1.Effect{
				Effect: &corev1.Effect_SecurityCryptoBatch{
					SecurityCryptoBatch: &corev1.SecurityCryptoBatchEffect{
						OpId:  opID,
						Items: batchItems,
					},
				},
			})
			return
		}

		var okItems []preparedOpen
		for _, p := range toVerify {
			if !devicesec.VerifyEd25519(p.edPub[:], p.originMsg, p.ct.Origin) {
				results[p.idx].Err = devicesec.ErrUntrusted
				continue
			}
			results[p.idx].Header = p.header
			okItems = append(okItems, p)
		}
		if len(okItems) == 0 {
			onDone(results)
			return
		}
		decryptVerified(okItems)
	})
	return e.DrainEffects()
}

// SealEnvelope encrypts and signs plain for purpose into a v1.Envelope using
// OS entropy and external KeySign (+ WebCrypto CryptoBatch AEAD when > 16 KiB).
func (e *Engine) SealEnvelope(
	nowMs int64,
	purpose devicesec.Purpose,
	machineID, sessionID, requestID string,
	seq uint64,
	deadlineMs uint64,
	plain []byte,
	useSessionSeed bool,
	onDone func(SealResult),
) []*corev1.Effect {
	e.touchNow(nowMs)
	e.withUnlocked(nowMs, func(curNow int64, unlockErr error) {
		if unlockErr != nil {
			onDone(SealResult{Err: unlockErr})
			return
		}
		if err := e.CanSeal(curNow); err != nil {
			onDone(SealResult{Err: err})
			return
		}
		sec := e.epochSecrets[e.head.Core.ContentEpoch]
		var seed []byte
		if useSessionSeed || purpose == devicesec.Event || purpose == devicesec.Image ||
			purpose == devicesec.TerminalInput || purpose == devicesec.TerminalResize ||
			purpose == devicesec.TerminalHistory || purpose == devicesec.TerminalOutput {
			cachedSeed, ok := e.sessionSeeds[sessionID]
			if !ok {
				onDone(SealResult{Err: devicesec.ErrInvalid})
				return
			}
			sCopy := cachedSeed
			seed = sCopy[:]
		}
		h := devicesec.Header{
			Account:      e.pin.Account,
			Domain:       e.pin.Domain,
			Epoch:        e.head.Core.Epoch,
			State:        e.headHash,
			ContentEpoch: e.head.Core.ContentEpoch,
			Sender:       e.selfDeviceID,
			Kind:         purpose.Kind(),
			Purpose:      purpose,
			Machine:      machineID,
			Session:      sessionID,
			Request:      requestID,
			Seq:          seq,
			Deadline:     deadlineMs,
			Compression:  0,
		}
		headerBytes, key, nonce, err := devicesec.PrepareCiphertext(sec[:], seed, h, len(plain), e.entropy)
		if err != nil {
			if errors.Is(err, entropy.ErrNeedEntropy) {
				err = ErrNeedEntropy
			}
			onDone(SealResult{Err: err})
			return
		}

		reqID := devicesec.RequestIdentity(h)
		payloadHash, err := devicesec.AuthorizedPayloadHash(h, plain)
		if err != nil {
			onDone(SealResult{Err: err})
			return
		}

		signAndFinish := func(cipherBytes []byte) {
			ct, originMsg, err := devicesec.AssembleCiphertext(headerBytes, nonce, cipherBytes)
			if err != nil {
				onDone(SealResult{Err: err})
				return
			}
			e.RequestKeySign(originMsg, func(sig []byte, signErr error) {
				if signErr != nil {
					onDone(SealResult{Err: signErr})
					return
				}
				ct.Origin = sig
				env, err := ToProtoEnvelope(ct)
				if err != nil {
					onDone(SealResult{Err: err})
					return
				}
				onDone(SealResult{
					Envelope:       env,
					RequestKeyHash: slices.Clone(reqID[:]),
					PayloadHash:    slices.Clone(payloadHash[:]),
				})
			})
		}

		if !e.inlineCrypto && len(plain) > BatchAeadThreshold {
			opID := e.allocOp()
			e.batchWait[opID] = func(batchRes []*corev1.SecurityCryptoBatchResult, batchErr error) {
				if batchErr != nil || len(batchRes) == 0 || batchRes[0] == nil || !batchRes[0].Ok {
					if batchErr == nil {
						batchErr = devicesec.ErrInvalid
					}
					onDone(SealResult{Err: batchErr})
					return
				}
				signAndFinish(batchRes[0].Data)
			}
			e.out = append(e.out, &corev1.Effect{
				Effect: &corev1.Effect_SecurityCryptoBatch{
					SecurityCryptoBatch: &corev1.SecurityCryptoBatchEffect{
						OpId: opID,
						Items: []*corev1.SecurityCryptoBatchItem{
							{
								Kind: &corev1.SecurityCryptoBatchItem_AeadSeal{
									AeadSeal: &corev1.CryptoBatchAeadSealItem{
										Key:       key,
										Nonce:     nonce,
										Aad:       headerBytes,
										Plaintext: plain,
									},
								},
							},
						},
					},
				},
			})
			return
		}

		blk, err := aes.NewCipher(key)
		if err != nil {
			onDone(SealResult{Err: err})
			return
		}
		aead, err := cipher.NewGCM(blk)
		if err != nil {
			onDone(SealResult{Err: err})
			return
		}
		signAndFinish(aead.Seal(nil, nonce, plain, headerBytes))
	})
	return e.DrainEffects()
}

// HandleSecurityOp executes stateful shell bridge operations (for remaining S6/file/terminal callers).
func (e *Engine) HandleSecurityOp(nowMs int64, req *corev1.SecurityOpRequestEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if req == nil {
		return nil
	}
	emitErr := func(err error) {
		e.out = append(e.out, &corev1.Effect{
			Effect: &corev1.Effect_SecurityOpResult{
				SecurityOpResult: &corev1.SecurityOpResultEffect{
					OpId:      req.OpId,
					Error:     err.Error(),
					ErrorCode: ErrorCode(err),
				},
			},
		})
	}

	switch op := req.Op.(type) {
	case *corev1.SecurityOpRequestEvent_OpenEnvelope:
		if op.OpenEnvelope == nil {
			emitErr(devicesec.ErrInvalid)
			return e.DrainEffects()
		}
		item := OpenItem{
			Env:             op.OpenEnvelope.Envelope,
			ExpectedPurpose: devicesec.Purpose(op.OpenEnvelope.ExpectedPurpose),
			MachineID:       op.OpenEnvelope.MachineId,
			SessionID:       op.OpenEnvelope.SessionId,
			RequestID:       op.OpenEnvelope.RequestId,
			Seq:             op.OpenEnvelope.Seq,
			History:         op.OpenEnvelope.History,
			OverrideSeed:    op.OpenEnvelope.OverrideSessionSeed,
		}
		return e.OpenBatch(nowMs, []OpenItem{item}, func(res []OpenResult) {
			r := res[0]
			if r.Err != nil {
				emitErr(r.Err)
				return
			}
			e.out = append(e.out, &corev1.Effect{
				Effect: &corev1.Effect_SecurityOpResult{
					SecurityOpResult: &corev1.SecurityOpResultEffect{
						OpId: req.OpId,
						Result: &corev1.SecurityOpResultEffect_Opened{
							Opened: &corev1.SecurityOpenEnvelopeResult{
								Header:       slices.Clone(op.OpenEnvelope.Envelope.GetSecurityHeader()),
								Plaintext:    r.Plain,
								Epoch:        r.Header.Epoch,
								ContentEpoch: r.Header.ContentEpoch,
								Seq:          r.Header.Seq,
								Sender:       r.Header.Sender,
								Purpose:      uint32(r.Header.Purpose),
								MachineId:    r.Header.Machine,
								SessionId:    r.Header.Session,
								RequestId:    r.Header.Request,
								DeadlineMs:   int64(r.Header.Deadline),
							},
						},
					},
				},
			})
		})

	case *corev1.SecurityOpRequestEvent_SealEnvelope:
		if op.SealEnvelope == nil {
			emitErr(devicesec.ErrInvalid)
			return e.DrainEffects()
		}
		return e.SealEnvelope(
			nowMs,
			devicesec.Purpose(op.SealEnvelope.Purpose),
			op.SealEnvelope.MachineId,
			op.SealEnvelope.SessionId,
			op.SealEnvelope.RequestId,
			op.SealEnvelope.Seq,
			uint64(max(0, op.SealEnvelope.DeadlineMs)),
			op.SealEnvelope.Plaintext,
			op.SealEnvelope.UseSessionSeed,
			func(sr SealResult) {
				if sr.Err != nil {
					emitErr(sr.Err)
					return
				}
				e.out = append(e.out, &corev1.Effect{
					Effect: &corev1.Effect_SecurityOpResult{
						SecurityOpResult: &corev1.SecurityOpResultEffect{
							OpId: req.OpId,
							Result: &corev1.SecurityOpResultEffect_Sealed{
								Sealed: &corev1.SecuritySealEnvelopeResult{
									Envelope:       sr.Envelope,
									RequestKeyHash: sr.RequestKeyHash,
									PayloadHash:    sr.PayloadHash,
								},
							},
						},
					},
				})
			},
		)

	case *corev1.SecurityOpRequestEvent_GetSessionSeed:
		if op.GetSessionSeed == nil {
			emitErr(devicesec.ErrInvalid)
			return e.DrainEffects()
		}
		seed, ok := e.SessionSeed(op.GetSessionSeed.SessionId)
		if !ok {
			emitErr(devicesec.ErrInvalid)
			return e.DrainEffects()
		}
		e.out = append(e.out, &corev1.Effect{
			Effect: &corev1.Effect_SecurityOpResult{
				SecurityOpResult: &corev1.SecurityOpResultEffect{
					OpId:   req.OpId,
					Result: &corev1.SecurityOpResultEffect_SessionSeed{SessionSeed: seed},
				},
			},
		})
		return e.DrainEffects()

	default:
		emitErr(devicesec.ErrInvalid)
		return e.DrainEffects()
	}
}

// ComputeSHA256 is a tiny shared digest helper for tests/callers.
func ComputeSHA256(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
