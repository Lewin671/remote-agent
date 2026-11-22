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
	ContentEpoch   uint64
	HeadHash       [32]byte
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
	unlockGen    uint64
	onUnlock     []taskWait
	nextOpID     uint64
	tasks        map[uint64]taskWait

	wf *workflowState

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
		tasks:        make(map[uint64]taskWait),
	}
}

// CurrentSealScope returns the verified content epoch and head hash used to seal outbound envelopes.
func (e *Engine) CurrentSealScope() (uint64, [32]byte, bool) {
	if e == nil || !e.hasHead {
		return 0, [32]byte{}, false
	}
	return e.head.Core.ContentEpoch, e.headHash, true
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
	e.configured = true
	e.inlineCrypto = ev.InlineCrypto
	if ev.Account != "" || ev.DeviceId != "" {
		e.securityLocked = true
		e.account = ev.Account
		e.selfDeviceID = ev.DeviceId
	}
	wf := e.ensureWF()
	if ev.Server != "" {
		if norm, err := CheckServerURL(ev.Server); err == nil {
			wf.server = norm
		} else {
			wf.server = ev.Server
		}
	}
	if ev.LegacyCleared {
		wf.legacyRepairNotice = true
	}
	if ev.Server != "" || ev.LegacyCleared {
		wf.foreground = ev.Foreground
	}
}

// HandleKeySigned dispatches a KeySign completion.
func (e *Engine) HandleKeySigned(nowMs int64, ev *corev1.SecurityKeySignedEvent) []*corev1.Effect {
	if ev == nil {
		return e.finishTaskEv(nowMs, 0, "", secEvent{})
	}
	return e.finishTaskEv(nowMs, ev.OpId, ev.Error, secEvent{kind: evSign, bytes: ev.Signature})
}

// HandleKeyAgreed dispatches a KeyAgree completion.
func (e *Engine) HandleKeyAgreed(nowMs int64, ev *corev1.SecurityKeyAgreedEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	if w, ok := e.takeTask(ev.OpId); ok && w.task != nil {
		se := &secEvent{kind: evAgree, tag: w.tag, bytes: ev.Shared}
		if ev.Error != "" {
			se.err = errors.New(ev.Error)
		} else if w.cachePeer && len(ev.Shared) == 32 {
			var k [32]byte
			copy(k[:], ev.Shared)
			e.peerShared[w.cachePeerX] = k
		}
		e.stepTask(w.task, se)
	}
	return e.DrainEffects()
}

// HandleCryptoBatchDone dispatches a CryptoBatch completion.
func (e *Engine) HandleCryptoBatchDone(nowMs int64, ev *corev1.SecurityCryptoBatchDoneEvent) []*corev1.Effect {
	if ev == nil {
		return e.finishTaskEv(nowMs, 0, "", secEvent{})
	}
	return e.finishTaskEv(nowMs, ev.OpId, ev.Error, secEvent{kind: evBatch, batch: ev.Results})
}

// RequestKeySign issues an async KeySign effect for DEVICE_ED (shared with transport.Coordinator).
func (e *Engine) RequestKeySign(msg []byte, fn func(sig []byte, err error)) {
	e.waitSign(keyDevEd, "", msg, &cbKeyTask{fn: fn}, 0)
}

// RequestKeyAgree issues or resolves from cache an X25519 ECDH agreement for DEVICE_X.
func (e *Engine) RequestKeyAgree(peerX [32]byte, cachePeer bool, fn func(shared []byte, err error)) {
	e.waitAgree(keyDevX, "", peerX, cachePeer, &cbKeyTask{fn: fn}, 0)
}

// RequestKeySignTag issues an async KeySign effect routed to AsyncSink.OnKeySignDone.
func (e *Engine) RequestKeySignTag(msg []byte, tag uint64) {
	e.waitSign(keyDevEd, "", msg, &cbKeyTask{tag: tag}, 0)
}

// RequestKeyAgreeTag issues an async KeyAgree effect routed to AsyncSink.OnKeyAgreeDone.
func (e *Engine) RequestKeyAgreeTag(peerX [32]byte, cachePeer bool, tag uint64) {
	e.waitAgree(keyDevX, "", peerX, cachePeer, &cbKeyTask{tag: tag, agree: true}, 0)
}

func (e *Engine) failRecord(err error) []*corev1.Effect {
	e.recordErr = err.Error()
	e.flushUnlockWaiters(e.lastNowMs, err)
	return e.DrainEffects()
}

// ContentKey provides a pre-decrypted historical content epoch secret.
type ContentKey struct {
	ContentEpoch uint64
	Secret       []byte
}

// RecordUpdate conveys a verified or persisted security state update to Engine.ApplyRecord.
type RecordUpdate struct {
	Direct                 bool
	PushSeq                uint64
	NetworkGeneration      uint64
	Account                string
	Domain                 []byte
	Genesis                []byte
	States                 [][]byte
	RecoveryProofs         [][]byte
	Lease                  []byte
	FloorMs                int64
	RecoveryPending        bool
	RecoveryReadyMs        int64
	RecoveryObjectsPending bool
	Removed                bool
	Cleared                bool
	Error                  string
	ContentKeys            []ContentKey
}

// ApplyRecord validates raw chain states, recovery proofs, and lease
// and kicks off head grant unlocking via KeyAgree if the current ContentEpoch is unopened.
func (e *Engine) ApplyRecord(nowMs int64, ev RecordUpdate) []*corev1.Effect {
	e.touchNow(nowMs)
	if !ev.Direct {
		if ev.PushSeq != 0 {
			if ev.PushSeq <= e.lastPushSeq {
				return nil
			}
			e.lastPushSeq = ev.PushSeq
		} else if e.lastPushSeq != 0 {
			return nil
		}
	}
	e.securityLocked = true
	if ev.NetworkGeneration > e.netGen {
		e.netGen = ev.NetworkGeneration
	}

	if ev.Removed || ev.Cleared {
		e.loaded = ev.Removed
		if ev.Cleared {
			e.loaded = false
		}
		e.isMember = false
		e.removed = ev.Removed
		e.cleared = ev.Cleared
		e.recordErr = ev.Error
		e.recoveryPending = ev.RecoveryPending
		e.recoveryReadyMs = ev.RecoveryReadyMs
		e.recoveryObjectsPending = ev.RecoveryObjectsPending
		e.resetChainState()
		e.flushUnlockWaiters(e.lastNowMs, ErrNotMember)
		return e.DrainEffects()
	}
	if ev.Error != "" {
		e.recordErr = ev.Error
		if !e.hasHead {
			e.loaded = false
			e.isMember = false
			e.removed = false
			e.cleared = false
			e.flushUnlockWaiters(e.lastNowMs, errors.New(ev.Error))
		}
		return e.DrainEffects()
	}

	if ev.FloorMs > e.floorMs {
		e.floorMs = ev.FloorMs
	}
	e.recoveryPending = ev.RecoveryPending
	e.recoveryReadyMs = ev.RecoveryReadyMs
	e.recoveryObjectsPending = ev.RecoveryObjectsPending

	if len(ev.Domain) != 32 || len(ev.Genesis) != 32 || len(ev.States) == 0 {
		return e.failRecord(devicesec.ErrInvalid)
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
		return e.failRecord(err)
	}

	head := parsedStates[len(parsedStates)-1]
	headHash, _ := head.Hash()

	var lease devicesec.LeaseCertificate
	var hasLease bool
	if len(ev.Lease) > 0 {
		lease, err = devicesec.ParseLease(ev.Lease)
		if err != nil {
			return e.failRecord(err)
		}
		if err := lease.Verify(head); err != nil {
			return e.failRecord(err)
		}
		hasLease = true
	}

	samePin := e.hasHead && e.pin == pin
	if samePin && head.Core.Epoch < e.head.Core.Epoch {
		return nil
	}
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
		if e.unlocking {
			e.unlockGen++
			e.unlocking = false
		}
	}

	_, e.isMember = head.Core.Member(e.selfDeviceID)
	if !e.isMember {
		clear(e.peerShared)
	}
	selfIdx := e.latestSelfStateIndex()
	if selfIdx < 0 {
		clear(e.epochSecrets)
		clear(e.peerShared)
		e.unlocking = false
		e.flushUnlockWaiters(e.lastNowMs, ErrNotMember)
		return e.DrainEffects()
	}

	targetState := e.states[selfIdx]
	for _, ck := range ev.ContentKeys {
		if ck.ContentEpoch == 0 || ck.ContentEpoch >= targetState.Core.ContentEpoch || len(ck.Secret) != 32 {
			continue
		}
		if _, exists := e.epochSecrets[ck.ContentEpoch]; !exists {
			var sec [32]byte
			copy(sec[:], ck.Secret)
			e.epochSecrets[ck.ContentEpoch] = sec
		}
	}

	if _, haveTargetSecret := e.epochSecrets[targetState.Core.ContentEpoch]; haveTargetSecret {
		if err := e.fillPrevSecretsFrom(selfIdx); err != nil {
			delete(e.epochSecrets, targetState.Core.ContentEpoch)
			e.isMember = false
			e.recordErr = err.Error()
			e.flushUnlockWaiters(e.lastNowMs, err)
			return e.DrainEffects()
		}
		e.flushUnlockWaiters(e.lastNowMs, nil)
		return e.DrainEffects()
	}

	if !e.unlocking {
		e.startUnlockForState(selfIdx)
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

func (e *Engine) latestSelfStateIndex() int {
	for i := len(e.states) - 1; i >= 0; i-- {
		if _, ok := e.states[i].Core.Member(e.selfDeviceID); ok {
			return i
		}
	}
	return -1
}

type unlockTask struct {
	gen    uint64
	idx    int
	target devicesec.State
	epoch  uint64
}

func (t *unlockTask) step(e *Engine, ev *secEvent) error {
	if t.gen != e.unlockGen {
		return nil
	}
	e.unlocking = false
	if ev.err != nil {
		e.flushUnlockWaiters(e.lastNowMs, ev.err)
		return nil
	}
	sec, err := e.pin.OpenGrantWithDH(t.target, e.selfDeviceID, ev.bytes)
	if err != nil || len(sec) != 32 {
		if err == nil {
			err = devicesec.ErrUntrusted
		}
		e.flushUnlockWaiters(e.lastNowMs, err)
		return nil
	}
	var arr [32]byte
	copy(arr[:], sec)
	e.epochSecrets[t.epoch] = arr
	if err := e.fillPrevSecretsFrom(t.idx); err != nil {
		delete(e.epochSecrets, t.epoch)
		e.isMember = false
		e.recordErr = err.Error()
		e.flushUnlockWaiters(e.lastNowMs, err)
		return nil
	}
	e.flushUnlockWaiters(e.lastNowMs, nil)
	return nil
}

func (e *Engine) startUnlockForState(idx int) {
	if idx < 0 || idx >= len(e.states) {
		e.flushUnlockWaiters(e.lastNowMs, ErrNotMember)
		return
	}
	target := e.states[idx]
	enc, _, err := e.pin.GrantChallenge(target, e.selfDeviceID)
	if err != nil {
		e.flushUnlockWaiters(e.lastNowMs, err)
		return
	}
	e.unlocking = true
	e.waitAgree(corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_X, "", enc, false, &unlockTask{
		gen:    e.unlockGen,
		idx:    idx,
		target: target,
		epoch:  target.Core.ContentEpoch,
	}, 0)
}

func (e *Engine) fillPrevSecretsFrom(idx int) error {
	e.minEpochSec = e.states[idx].Core.ContentEpoch
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
	for _, w := range waiters {
		e.deliverUnlockWaiter(w, nowMs, err)
	}
}

func (e *Engine) deliverUnlockWaiter(w taskWait, nowMs int64, err error) {
	ev := &secEvent{kind: evUnlocked, tag: w.tag, nowMs: nowMs, err: err}
	if w.task != nil {
		e.stepTask(w.task, ev)
		return
	}
	if w.target.kind != targetNone {
		e.completeTarget(w.target, ev)
	}
}

func (e *Engine) waitUnlocked(nowMs int64, requireMember bool, w taskWait) {
	if !e.loaded || !e.hasHead {
		if e.unlocking {
			e.onUnlock = append(e.onUnlock, w)
			return
		}
		e.deliverUnlockWaiter(w, nowMs, ErrNotInitialized)
		return
	}
	if requireMember && !e.isMember {
		e.deliverUnlockWaiter(w, nowMs, ErrNotMember)
		return
	}
	idx := e.latestSelfStateIndex()
	if idx < 0 {
		e.deliverUnlockWaiter(w, nowMs, ErrNotMember)
		return
	}
	if _, ok := e.epochSecrets[e.states[idx].Core.ContentEpoch]; ok {
		e.deliverUnlockWaiter(w, nowMs, nil)
		return
	}
	if e.unlocking {
		e.onUnlock = append(e.onUnlock, w)
		return
	}
	e.onUnlock = append(e.onUnlock, w)
	e.startUnlockForState(idx)
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
		ClockSkewed:            e.isClockSkewed(e.lastNowMs),
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

func (e *Engine) isClockSkewed(nowMs int64) bool {
	if !e.hasHead {
		return false
	}
	effNow := e.effectiveNow(nowMs)
	if e.head.Core.IssuedAt > effNow+devicesec.MaxClockSkew {
		return true
	}
	if e.hasLease && e.lease.IssuedAt > effNow+devicesec.MaxClockSkew {
		return true
	}
	return false
}

func (e *Engine) checkLiveLease(nowMs int64) error {
	if e.isClockSkewed(nowMs) {
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
	if !item.History {
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
	if e.isClockSkewed(nowMs) {
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
type preparedOpen struct {
	idx       int
	item      OpenItem
	ct        devicesec.Ciphertext
	header    devicesec.Header
	edPub     [32]byte
	originMsg []byte
}

const (
	openTagVerify uint8 = 1
	openTagAEAD   uint8 = 2
)

type openBatchTask struct {
	items        []OpenItem
	results      []OpenResult
	toVerify     []preparedOpen
	verified     []preparedOpen
	batchAEADIdx []int
	onDone       func([]OpenResult)
	useSink      bool
	sinkTag      uint64
	target       taskTarget
	secOpID      uint64
	secOpHeader  []byte
}

func (e *Engine) startOpenBatch(nowMs int64, t *openBatchTask) {
	t.results = make([]OpenResult, len(t.items))
	e.waitUnlocked(nowMs, false, taskWait{task: t})
}

//go:noinline
func (e *Engine) emitSecurityOpResult(res *corev1.SecurityOpResultEffect) {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityOpResult{SecurityOpResult: res},
	})
}

func (t *openBatchTask) finish(e *Engine) {
	if t.secOpID != 0 {
		r := t.results[0]
		if r.Err != nil {
			e.emitSecurityOpErr(t.secOpID, r.Err)
			return
		}
		e.emitSecurityOpResult(&corev1.SecurityOpResultEffect{
			OpId: t.secOpID,
			Result: &corev1.SecurityOpResultEffect_Opened{
				Opened: &corev1.SecurityOpenEnvelopeResult{
					Header: slices.Clone(t.secOpHeader), Plaintext: r.Plain,
					Epoch: r.Header.Epoch, ContentEpoch: r.Header.ContentEpoch, Seq: r.Header.Seq,
					Sender: r.Header.Sender, Purpose: uint32(r.Header.Purpose),
					MachineId: r.Header.Machine, SessionId: r.Header.Session, RequestId: r.Header.Request,
					DeadlineMs: int64(r.Header.Deadline),
				},
			},
		})
		return
	}
	if t.useSink {
		if wf := e.wf; wf != nil && wf.asyncSink != nil {
			wf.asyncSink.OnOpenBatchDone(t.sinkTag, t.results)
		}
		return
	}
	if t.onDone != nil {
		t.onDone(t.results)
		return
	}
	e.completeTarget(t.target, &secEvent{opened: t.results})
}

func (t *openBatchTask) step(e *Engine, ev *secEvent) error {
	switch ev.kind {
	case evUnlocked:
		if ev.err != nil {
			for i := range t.results {
				t.results[i].Err = ev.err
			}
			t.finish(e)
			return nil
		}
		t.prepareAndVerify(e, ev.nowMs)
	case evBatch:
		if ev.tag == openTagVerify {
			var okItems []preparedOpen
			for vi, p := range t.toVerify {
				if ev.err != nil {
					t.results[p.idx].Err = ev.err
					continue
				}
				if vi >= len(ev.batch) || ev.batch[vi] == nil || !ev.batch[vi].Ok {
					t.results[p.idx].Err = devicesec.ErrUntrusted
					continue
				}
				t.results[p.idx].Header = p.header
				okItems = append(okItems, p)
			}
			t.toVerify = nil
			if len(okItems) == 0 {
				t.finish(e)
				return nil
			}
			t.decryptVerified(e, okItems)
			return nil
		}
		if ev.tag == openTagAEAD {
			for bi, vi := range t.batchAEADIdx {
				p := t.verified[vi]
				if ev.err != nil {
					t.results[p.idx].Err = ev.err
					continue
				}
				if bi >= len(ev.batch) || ev.batch[bi] == nil || !ev.batch[bi].Ok {
					t.results[p.idx].Err = devicesec.ErrUntrusted
					continue
				}
				body, unpacked, err := ValidateAndDecodeBody(p.header, ev.batch[bi].Data)
				if err != nil {
					t.results[p.idx].Err = err
					continue
				}
				if p.header.Purpose == devicesec.SessionKey {
					if b, ok := body.([]byte); ok {
						e.SeedSessionKey(p.header.Session, b)
					}
				}
				t.results[p.idx].Plain = unpacked
				t.results[p.idx].Body = body
			}
			t.verified = nil
			t.batchAEADIdx = nil
			t.finish(e)
		}
	}
	return nil
}

func (t *openBatchTask) prepareAndVerify(e *Engine, curNow int64) {
	type replCacheKey struct {
		machineID string
		sessionID string
	}
	replCache := make(map[replCacheKey]map[string]bool)
	var toVerify []preparedOpen

	for i, item := range t.items {
		if verErr := e.checkEnvelopeVersion(item.Env); verErr != nil {
			t.results[i].Err = verErr
			continue
		}
		ct, err := FromProtoEnvelope(item.Env)
		if err != nil {
			t.results[i].Err = err
			continue
		}
		h, err := devicesec.ParseHeader(ct.Header)
		if err != nil {
			t.results[i].Err = err
			continue
		}
		if item.ExpectedPurpose != 0 && h.Purpose != item.ExpectedPurpose {
			t.results[i].Err = devicesec.ErrInvalid
			continue
		}
		if item.History && h.Purpose.Control() {
			t.results[i].Err = devicesec.ErrUntrusted
			continue
		}
		if item.SessionID != "" && h.Session != item.SessionID {
			t.results[i].Err = devicesec.ErrUntrusted
			continue
		}
		if item.RequestID != "" && h.Request != item.RequestID {
			t.results[i].Err = devicesec.ErrUntrusted
			continue
		}
		if item.Seq != 0 && h.Seq != item.Seq {
			t.results[i].Err = devicesec.ErrUntrusted
			continue
		}
		if item.MachineID != "" && h.Machine != item.MachineID {
			if !item.History || len(item.Replacements) == 0 {
				t.results[i].Err = devicesec.ErrUntrusted
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
					t.results[i].Err = err
					continue
				}
				if item.SessionID != "" {
					replCache[ck] = origins
				}
			}
			if !origins[h.Machine] {
				t.results[i].Err = devicesec.ErrUntrusted
				continue
			}
		}
		if !item.History {
			if !e.isMember {
				t.results[i].Err = ErrNotMember
				continue
			}
			if err := e.checkLiveLease(curNow); err != nil {
				t.results[i].Err = err
				continue
			}
		}
		s, ok := e.byHash[h.State]
		if !ok {
			t.results[i].Err = devicesec.ErrStale
			continue
		}
		vh, edPub, originMsg, err := ct.OriginVerifyInput(s, e.head, !item.History, e.effectiveNow(curNow))
		if err != nil {
			t.results[i].Err = err
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
		t.finish(e)
		return
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
		t.toVerify = toVerify
		e.waitBatch(batchItems, t, openTagVerify)
		return
	}

	var okItems []preparedOpen
	for _, p := range toVerify {
		if !devicesec.VerifyEd25519(p.edPub[:], p.originMsg, p.ct.Origin) {
			t.results[p.idx].Err = devicesec.ErrUntrusted
			continue
		}
		t.results[p.idx].Header = p.header
		okItems = append(okItems, p)
	}
	if len(okItems) == 0 {
		t.finish(e)
		return
	}
	t.decryptVerified(e, okItems)
}

func (t *openBatchTask) decryptVerified(e *Engine, verified []preparedOpen) {
	var batchAEADIdx []int
	var batchAEADItems []*corev1.SecurityCryptoBatchItem
	for vi, p := range verified {
		sec, hasSec := e.epochSecrets[p.header.ContentEpoch]
		if !hasSec {
			if e.isRecoveryUnavailable(p.item, p.header) {
				t.results[p.idx].Err = ErrRecoveryHistoryUnavailable
			} else {
				t.results[p.idx].Err = devicesec.ErrStale
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
				t.results[p.idx].Err = devicesec.ErrInvalid
				continue
			}
		}
		if !e.inlineCrypto && len(p.ct.Cipher) > BatchAeadThreshold {
			_, key, err := devicesec.DeriveCipherKey(sec[:], seed, p.ct)
			if err != nil {
				t.results[p.idx].Err = err
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
				t.results[p.idx].Err = err
				continue
			}
			body, unpacked, err := ValidateAndDecodeBody(p.header, plain)
			if err != nil {
				t.results[p.idx].Err = err
				continue
			}
			if p.header.Purpose == devicesec.SessionKey {
				if b, ok := body.([]byte); ok {
					e.SeedSessionKey(p.header.Session, b)
				}
			}
			t.results[p.idx].Plain = unpacked
			t.results[p.idx].Body = body
		}
	}
	if len(batchAEADItems) == 0 {
		t.finish(e)
		return
	}
	t.verified = verified
	t.batchAEADIdx = batchAEADIdx
	e.waitBatch(batchAEADItems, t, openTagAEAD)
}

func (e *Engine) OpenBatch(nowMs int64, items []OpenItem, onDone func(results []OpenResult)) []*corev1.Effect {
	return e.openBatch(nowMs, items, onDone, false, 0)
}

// OpenBatchTag is the closure-free AsyncSink variant of OpenBatch.
func (e *Engine) OpenBatchTag(nowMs int64, items []OpenItem, tag uint64) []*corev1.Effect {
	return e.openBatch(nowMs, items, nil, true, tag)
}

func (e *Engine) openBatch(nowMs int64, items []OpenItem, onDone func([]OpenResult), useSink bool, sinkTag uint64) []*corev1.Effect {
	e.touchNow(nowMs)
	e.startOpenBatch(nowMs, &openBatchTask{items: items, onDone: onDone, useSink: useSink, sinkTag: sinkTag})
	return e.DrainEffects()
}

const (
	sealTagAEAD uint8 = 1
	sealTagSign uint8 = 2
)

type sealEnvelopeTask struct {
	purpose        devicesec.Purpose
	machineID      string
	sessionID      string
	requestID      string
	seq            uint64
	deadlineMs     uint64
	plain          []byte
	useSessionSeed bool
	overrideSeed   []byte
	header         devicesec.Header
	headerBytes    []byte
	nonce          []byte
	pendingCT      devicesec.Ciphertext
	reqID          devicesec.Hash
	payloadHash    devicesec.Hash
	onDone         func(SealResult)
	useSink        bool
	sinkTag        uint64
	target         taskTarget
	secOpID        uint64
}

func (e *Engine) startSealEnvelope(nowMs int64, t *sealEnvelopeTask) {
	e.waitUnlocked(nowMs, true, taskWait{task: t})
}

func (t *sealEnvelopeTask) fail(e *Engine, err error) error {
	t.finish(e, SealResult{Err: err})
	return nil
}

func (t *sealEnvelopeTask) finish(e *Engine, sr SealResult) {
	if t.secOpID != 0 {
		if sr.Err != nil {
			e.emitSecurityOpErr(t.secOpID, sr.Err)
			return
		}
		e.emitSecurityOpResult(&corev1.SecurityOpResultEffect{
			OpId: t.secOpID,
			Result: &corev1.SecurityOpResultEffect_Sealed{
				Sealed: &corev1.SecuritySealEnvelopeResult{
					Envelope: sr.Envelope, RequestKeyHash: sr.RequestKeyHash, PayloadHash: sr.PayloadHash,
				},
			},
		})
		return
	}
	if t.useSink {
		if wf := e.wf; wf != nil && wf.asyncSink != nil {
			wf.asyncSink.OnSealEnvelopeDone(t.sinkTag, sr, sr.Err)
		}
		return
	}
	if t.onDone != nil {
		t.onDone(sr)
		return
	}
	e.completeTarget(t.target, &secEvent{seal: &sr, err: sr.Err})
}

func (t *sealEnvelopeTask) signAndFinish(e *Engine, cipherBytes []byte) {
	ct, originMsg, err := devicesec.AssembleCiphertext(t.headerBytes, t.nonce, cipherBytes)
	if err != nil {
		t.finish(e, SealResult{Err: err})
		return
	}
	t.pendingCT = ct
	e.waitSign(keyDevEd, "", originMsg, t, sealTagSign)
}

func (t *sealEnvelopeTask) step(e *Engine, ev *secEvent) error {
	switch ev.kind {
	case evUnlocked:
		if ev.err != nil {
			return t.fail(e, ev.err)
		}
		if err := e.CanSeal(ev.nowMs); err != nil {
			return t.fail(e, err)
		}
		sec := e.epochSecrets[e.head.Core.ContentEpoch]
		var seed []byte
		if len(t.overrideSeed) == 32 {
			seed = slices.Clone(t.overrideSeed)
		} else if len(t.overrideSeed) != 0 {
			return t.fail(e, devicesec.ErrInvalid)
		} else if t.useSessionSeed || t.purpose == devicesec.Event || t.purpose == devicesec.Image ||
			t.purpose == devicesec.TerminalInput || t.purpose == devicesec.TerminalResize ||
			t.purpose == devicesec.TerminalHistory || t.purpose == devicesec.TerminalOutput {
			cachedSeed, ok := e.sessionSeeds[t.sessionID]
			if !ok {
				return t.fail(e, devicesec.ErrInvalid)
			}
			sCopy := cachedSeed
			seed = sCopy[:]
		}
		h := devicesec.Header{
			Account: e.pin.Account, Domain: e.pin.Domain, Epoch: e.head.Core.Epoch, State: e.headHash,
			ContentEpoch: e.head.Core.ContentEpoch, Sender: e.selfDeviceID, Kind: t.purpose.Kind(), Purpose: t.purpose,
			Machine: t.machineID, Session: t.sessionID, Request: t.requestID, Seq: t.seq, Deadline: t.deadlineMs,
		}
		if err := h.Scope(e.head); err != nil {
			return t.fail(e, err)
		}
		headerBytes, key, nonce, err := devicesec.PrepareCiphertext(sec[:], seed, h, len(t.plain), e.entropy)
		if err != nil {
			if errors.Is(err, entropy.ErrNeedEntropy) {
				err = ErrNeedEntropy
			}
			return t.fail(e, err)
		}
		reqID := devicesec.RequestIdentity(h)
		payloadHash, err := devicesec.AuthorizedPayloadHash(h, t.plain)
		if err != nil {
			return t.fail(e, err)
		}
		t.header, t.headerBytes, t.nonce, t.reqID, t.payloadHash = h, headerBytes, nonce, reqID, payloadHash

		if !e.inlineCrypto && len(t.plain) > BatchAeadThreshold {
			e.waitBatch([]*corev1.SecurityCryptoBatchItem{{
				Kind: &corev1.SecurityCryptoBatchItem_AeadSeal{AeadSeal: &corev1.CryptoBatchAeadSealItem{
					Key: key, Nonce: nonce, Aad: headerBytes, Plaintext: t.plain,
				}},
			}}, t, sealTagAEAD)
			return nil
		}
		blk, err := aes.NewCipher(key)
		if err != nil {
			return t.fail(e, err)
		}
		aead, err := cipher.NewGCM(blk)
		if err != nil {
			return t.fail(e, err)
		}
		t.signAndFinish(e, aead.Seal(nil, nonce, t.plain, headerBytes))
	case evBatch:
		if ev.err != nil || len(ev.batch) == 0 || ev.batch[0] == nil || !ev.batch[0].Ok {
			err := ev.err
			if err == nil {
				err = devicesec.ErrInvalid
			}
			return t.fail(e, err)
		}
		t.signAndFinish(e, ev.batch[0].Data)
	case evSign:
		if ev.err != nil {
			return t.fail(e, ev.err)
		}
		ct := t.pendingCT
		ct.Origin = ev.bytes
		env, err := ToProtoEnvelope(ct)
		if err != nil {
			return t.fail(e, err)
		}
		t.finish(e, SealResult{
			Envelope: env, RequestKeyHash: slices.Clone(t.reqID[:]), PayloadHash: slices.Clone(t.payloadHash[:]),
			ContentEpoch: t.header.ContentEpoch, HeadHash: t.header.State,
		})
	}
	return nil
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
	overrideSeed []byte,
	onDone func(SealResult),
) []*corev1.Effect {
	return e.sealEnvelope(nowMs, purpose, machineID, sessionID, requestID, seq, deadlineMs, plain, useSessionSeed, overrideSeed, onDone, false, 0)
}

// SealEnvelopeTag is the closure-free AsyncSink variant of SealEnvelope.
func (e *Engine) SealEnvelopeTag(
	nowMs int64,
	purpose devicesec.Purpose,
	machineID, sessionID, requestID string,
	seq uint64,
	deadlineMs uint64,
	plain []byte,
	useSessionSeed bool,
	overrideSeed []byte,
	tag uint64,
) []*corev1.Effect {
	return e.sealEnvelope(nowMs, purpose, machineID, sessionID, requestID, seq, deadlineMs, plain, useSessionSeed, overrideSeed, nil, true, tag)
}

func (e *Engine) sealEnvelope(
	nowMs int64, purpose devicesec.Purpose, machineID, sessionID, requestID string,
	seq, deadlineMs uint64, plain []byte, useSessionSeed bool, overrideSeed []byte,
	onDone func(SealResult), useSink bool, sinkTag uint64,
) []*corev1.Effect {
	e.touchNow(nowMs)
	e.startSealEnvelope(nowMs, &sealEnvelopeTask{
		purpose: purpose, machineID: machineID, sessionID: sessionID, requestID: requestID,
		seq: seq, deadlineMs: deadlineMs, plain: plain, useSessionSeed: useSessionSeed,
		overrideSeed: overrideSeed, onDone: onDone, useSink: useSink, sinkTag: sinkTag,
	})
	return e.DrainEffects()
}

func (e *Engine) emitSecurityOpErr(opID uint64, err error) {
	e.emitSecurityOpResult(&corev1.SecurityOpResultEffect{OpId: opID, Error: err.Error(), ErrorCode: ErrorCode(err)})
}

// HandleSecurityOp executes stateful shell bridge operations (for remaining S6/file/terminal callers).
func (e *Engine) HandleSecurityOp(nowMs int64, req *corev1.SecurityOpRequestEvent, replacements ...*v1.MachineReplacementProof) []*corev1.Effect {
	e.touchNow(nowMs)
	if req == nil {
		return nil
	}
	switch op := req.Op.(type) {
	case *corev1.SecurityOpRequestEvent_OpenEnvelope:
		if op.OpenEnvelope == nil {
			e.emitSecurityOpErr(req.OpId, devicesec.ErrInvalid)
			return e.DrainEffects()
		}
		o := op.OpenEnvelope
		e.startOpenBatch(nowMs, &openBatchTask{
			items: []OpenItem{{
				Env: o.Envelope, ExpectedPurpose: devicesec.Purpose(o.ExpectedPurpose),
				MachineID: o.MachineId, SessionID: o.SessionId, RequestID: o.RequestId,
				Seq: o.Seq, History: o.History, Replacements: replacements, OverrideSeed: o.OverrideSessionSeed,
			}},
			secOpID: req.OpId, secOpHeader: o.Envelope.GetSecurityHeader(),
		})
	case *corev1.SecurityOpRequestEvent_SealEnvelope:
		if op.SealEnvelope == nil {
			e.emitSecurityOpErr(req.OpId, devicesec.ErrInvalid)
			return e.DrainEffects()
		}
		s := op.SealEnvelope
		e.startSealEnvelope(nowMs, &sealEnvelopeTask{
			purpose: devicesec.Purpose(s.Purpose), machineID: s.MachineId, sessionID: s.SessionId,
			requestID: s.RequestId, seq: s.Seq, deadlineMs: uint64(max(0, s.DeadlineMs)),
			plain: s.Plaintext, useSessionSeed: s.UseSessionSeed, overrideSeed: s.OverrideSessionSeed, secOpID: req.OpId,
		})
	case *corev1.SecurityOpRequestEvent_GetSessionSeed:
		var seed []byte
		var ok bool
		if op.GetSessionSeed != nil {
			seed, ok = e.SessionSeed(op.GetSessionSeed.SessionId)
		}
		if !ok {
			e.emitSecurityOpErr(req.OpId, devicesec.ErrInvalid)
		} else {
			e.emitSecurityOpResult(&corev1.SecurityOpResultEffect{
				OpId: req.OpId, Result: &corev1.SecurityOpResultEffect_SessionSeed{SessionSeed: seed},
			})
		}
	default:
		e.emitSecurityOpErr(req.OpId, devicesec.ErrInvalid)
	}
	return e.DrainEffects()
}

// ComputeSHA256 is a tiny shared digest helper for tests/callers.
func ComputeSHA256(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
