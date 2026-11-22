package security

import (
	"bytes"
	"errors"
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/entropy"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

const (
	protocolVersion uint32 = 17

	loginContext        = "remote-agent/v1/login\x00"
	registerContext     = "remote-agent/v1/register\x00"
	deleteAccountPrefix = "remote-agent/v1/delete-account\x00"
	recoveryCtxPrefix   = "ra3/recovery-context\x00"
	recoveryDonePrefix  = "ra3/recovery-complete\x00"

	dayMs              int64 = 24 * 60 * 60 * 1000
	halfDayMs          int64 = 12 * 60 * 60 * 1000
	maxPairWindowMs    int64 = 5 * 60 * 1000
	maxPairWindowSlack int64 = 5 * 1000
	pairPollIntervalMs int64 = 1000

	connectCodeCanceled           int32 = 1
	connectCodeUnknown            int32 = 2
	connectCodeInvalidArgument    int32 = 3
	connectCodeDeadlineExceeded   int32 = 4
	connectCodeNotFound           int32 = 5
	connectCodeAlreadyExists      int32 = 6
	connectCodePermissionDenied   int32 = 7
	connectCodeResourceExhausted  int32 = 8
	connectCodeFailedPrecondition int32 = 9
	connectCodeAborted            int32 = 10
	connectCodeUnavailable        int32 = 14
	connectCodeUnauthenticated    int32 = 16
)

// DeviceOfferInfo carries verified client device metadata from sync directory.
type DeviceOfferInfo struct {
	ID         string
	Kind       v1.DeviceKind
	RecoveryEd []byte
	Meta       *v1.DeviceMeta
	MetaRev    uint64
}

type wfError struct {
	msg       string
	code      string
	retryable bool
	uncertain bool
	action    string
}

func (e *wfError) Error() string {
	if e == nil {
		return ""
	}
	return e.msg
}

func newWfError(code, msg string) *wfError {
	return &wfError{code: code, msg: msg, retryable: true}
}

func fatalWfError(code, msg, action string) *wfError {
	return &wfError{code: code, msg: msg, retryable: false, action: action}
}

func uncertainWfError(code, msg string) *wfError {
	return &wfError{code: code, msg: msg, retryable: true, uncertain: true, action: "retry"}
}

type rpcError struct {
	code    int32
	msg     string
	network bool
}

func (e *rpcError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return "rpc failed"
}

type codeDraft struct {
	kind             string // "state" (recovery rotation) or "offer"
	parent           [32]byte
	recoveryEd       []byte
	storagePersisted bool
}

type initiatorPairingSession struct {
	pairingID        string
	code             string
	url              string
	server           string
	expiresMs        int64
	ephemSlot        string
	ephemPub         []byte
	trust            []byte
	replacesDeviceID string
	joined           bool
	approved         bool
	delivered        bool
	candidateID      string
	candidateEd      []byte
	candidateX       []byte
	candidateKind    v1.DeviceKind
	sasEmoji         []string
	pairKey          []byte
}

type joinerPairingSession struct {
	pairingID      string
	server         string
	code           string
	commitHint     []byte
	commit         []byte
	existingRepair bool
	replace        bool
	ephemSlot      string
	ephemPub       []byte
	edSlot         string
	xSlot          string
	edPublic       []byte
	xPublic        []byte
	reusesDeviceEd bool
	candidateToken string
	trust          devicesec.Pin
	approver       string
	sasEmoji       []string
	sasConfirmed   bool
	pairKey        []byte
	registering    bool
}

type storageReply struct {
	found         bool
	revision      string
	data          []byte
	conflict      bool
	entries       []*corev1.SecurityStorageEntry
	hasDeviceEd   bool
	deviceEdPub   []byte
	hasDeviceX    bool
	deviceXPub    []byte
	legacyCleared bool
}

type activePollKind int

const (
	pollNone activePollKind = iota
	pollJoinRevealed
	pollJoinApproved
	pollRecoveryStatus
)

type activePoll struct {
	kind      activePollKind
	pairingID string
	onResult  func(res *corev1.SecurityWorkflowResultEffect, err error)
}

type entropyWaiter struct {
	need int
	fn   func()
}

type workflowState struct {
	server             string
	foreground         bool
	fgGen              uint64
	bootstrapped       bool
	bootstrapInvalid   bool
	bootstrapError     string
	legacyRepairNotice bool

	cachedToken    string
	tokenExpiresMs int64

	record          *PersistedRecord
	recordRev       string
	pendingOp       *PersistedOperation
	pendingOpRev    string
	regIntent       *PersistedRegistrationIntent
	regIntentRev    string
	publicProfiles  map[string]*PersistedPublicProfile // by PublicProfileStorageKey
	deviceEdPresent bool
	deviceEdPub     []byte
	deviceXPresent  bool
	deviceXPub      []byte

	codeDraft *codeDraft
	opBusy    bool
	opQueue   []func()

	lastLeaseRenewAttemptMs int64
	recoveryStatusStale     bool
	syncInFlight            bool
	syncWaiters             []func(error)

	onStateChanged func()
	devicesSnap    []DeviceOfferInfo

	initPairings map[string]*initiatorPairingSession
	joinPairings map[string]*joinerPairingSession
	activePoll   *activePoll

	entropyWait  []entropyWaiter
	keyGenWait   map[uint64]func(pub []byte, err error)
	encapWait    map[uint64]func(pairs []*corev1.SecurityEncapsulatedPair, err error)
	storageWait  map[uint64]func(rep storageReply, err error)
	rpcWait      map[uint64]func(resp []byte, rpcErr *rpcError)
	seqCounter   uint64
}

func (e *Engine) ensureWF() *workflowState {
	if e.wf == nil {
		e.wf = &workflowState{
			foreground:     true,
			publicProfiles: make(map[string]*PersistedPublicProfile),
			initPairings:   make(map[string]*initiatorPairingSession),
			joinPairings:   make(map[string]*joinerPairingSession),
			keyGenWait:     make(map[uint64]func([]byte, error)),
			encapWait:      make(map[uint64]func([]*corev1.SecurityEncapsulatedPair, error)),
			storageWait:    make(map[uint64]func(storageReply, error)),
			rpcWait:        make(map[uint64]func([]byte, *rpcError)),
		}
	}
	return e.wf
}

func (e *Engine) nextWorkflowToken() string {
	wf := e.ensureWF()
	wf.seqCounter++
	var b [16]byte
	if _, err := e.entropy.Read(b[:]); err == nil {
		return hexStr(b[:])
	}
	if id, ok := e.entropy.TakeID(); ok {
		return id
	}
	return "wf-" + u64Str(wf.seqCounter)
}

func u64Str(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// SetNetworkGeneration records the latest network generation from sync lifecycle/network transitions.
func (e *Engine) SetNetworkGeneration(gen uint64) {
	if gen > e.netGen {
		e.netGen = gen
	}
}

// SetForeground updates foreground visibility and invalidates captured foreground checks when hidden.
func (e *Engine) SetForeground(fg bool) {
	wf := e.ensureWF()
	if wf.foreground == fg {
		return
	}
	wf.foreground = fg
	if !fg {
		wf.fgGen++
	}
}

// SetStateChangedCallback registers a hook invoked whenever verified security state installs or clears.
func (e *Engine) SetStateChangedCallback(fn func()) {
	e.ensureWF().onStateChanged = fn
}

// SetDeviceDirectory updates the verified client device directory known from sync.
func (e *Engine) SetDeviceDirectory(devices []DeviceOfferInfo) {
	e.ensureWF().devicesSnap = slices.Clone(devices)
}

// HasWorkflowBootstrapped reports whether the core-driven v4 workflow storage bootstrap has completed.
func (e *Engine) HasWorkflowBootstrapped() bool {
	return e.wf != nil && e.wf.bootstrapped
}

// SetAuthToken seeds an externally obtained bearer token into the workflow state.
func (e *Engine) SetAuthToken(token string, expiresMs int64) {
	wf := e.ensureWF()
	wf.cachedToken = token
	wf.tokenExpiresMs = expiresMs
}

// FlushEntropy delivers buffered OS entropy to any pending workflow entropy waiters.
func (e *Engine) FlushEntropy() []*corev1.Effect {
	wf := e.wf
	if wf == nil || len(wf.entropyWait) == 0 {
		return nil
	}
	var remaining []entropyWaiter
	ready := wf.entropyWait
	wf.entropyWait = nil
	for i, w := range ready {
		if e.entropy.Avail() >= w.need {
			w.fn()
		} else {
			remaining = append(remaining, ready[i:]...)
			e.emitEntropyNeed(w.need)
			break
		}
	}
	if len(remaining) > 0 {
		wf.entropyWait = append(remaining, wf.entropyWait...)
	}
	return e.DrainEffects()
}

func (e *Engine) emitEntropyNeed(minBytes int) {
	need := max(minBytes, entropy.RefillBytes)
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_RequestEntropy{
			RequestEntropy: &corev1.RequestEntropyEffect{
				MinRandomBytes: int32(need),
			},
		},
	})
}

func (e *Engine) withEntropy(minBytes int, fn func()) {
	if e.entropy.Avail() >= minBytes {
		fn()
		return
	}
	wf := e.ensureWF()
	wf.entropyWait = append(wf.entropyWait, entropyWaiter{need: minBytes, fn: fn})
	e.emitEntropyNeed(minBytes)
}

func (e *Engine) readRandom(dst []byte) bool {
	_, err := e.entropy.Read(dst)
	return err == nil
}

func (e *Engine) reqKeyGen(kind corev1.SecurityKeyKind, slot string, persist bool, fn func(pub []byte, err error)) {
	opID := e.allocOp()
	e.ensureWF().keyGenWait[opID] = fn
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityKeyGenerate{
			SecurityKeyGenerate: &corev1.SecurityKeyGenerateEffect{
				OpId:    opID,
				KeyKind: kind,
				Slot:    slot,
				Persist: persist,
			},
		},
	})
}

func (e *Engine) reqKeyEncapsulate(recipients []*corev1.SecurityEncapsulateRecipient, fn func(pairs []*corev1.SecurityEncapsulatedPair, err error)) {
	opID := e.allocOp()
	e.ensureWF().encapWait[opID] = fn
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityKeyEncapsulate{
			SecurityKeyEncapsulate: &corev1.SecurityKeyEncapsulateEffect{
				OpId:       opID,
				Recipients: recipients,
			},
		},
	})
}

func (e *Engine) reqKeyDiscard(kind corev1.SecurityKeyKind, slot string) {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityKeyDiscard{
			SecurityKeyDiscard: &corev1.SecurityKeyDiscardEffect{
				KeyKind: kind,
				Slot:    slot,
			},
		},
	})
}

func (e *Engine) reqStorage(eff *corev1.SecurityStorageEffect, fn func(rep storageReply, err error)) {
	opID := e.allocOp()
	eff.OpId = opID
	e.ensureWF().storageWait[opID] = fn
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityStorage{
			SecurityStorage: eff,
		},
	})
}

func (e *Engine) reqRpc(method corev1.SecurityRpcMethod, server, token string, reqBytes []byte, fn func(resp []byte, rpcErr *rpcError)) {
	opID := e.allocOp()
	e.ensureWF().rpcWait[opID] = fn
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityRpc{
			SecurityRpc: &corev1.SecurityRpcEffect{
				OpId:         opID,
				Method:       method,
				Server:       server,
				AuthToken:    token,
				RequestBytes: reqBytes,
			},
		},
	})
}

// HandleKeyGenerated dispatches key generation completion.
func (e *Engine) HandleKeyGenerated(nowMs int64, ev *corev1.SecurityKeyGeneratedEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	wf := e.ensureWF()
	fn := wf.keyGenWait[ev.OpId]
	delete(wf.keyGenWait, ev.OpId)
	if fn != nil {
		if ev.Error != "" {
			fn(nil, errors.New(ev.Error))
		} else {
			fn(ev.PublicKey, nil)
		}
	}
	return e.DrainEffects()
}

// HandleKeyEncapsulated dispatches KEM encapsulation completion.
func (e *Engine) HandleKeyEncapsulated(nowMs int64, ev *corev1.SecurityKeyEncapsulatedEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	wf := e.ensureWF()
	fn := wf.encapWait[ev.OpId]
	delete(wf.encapWait, ev.OpId)
	if fn != nil {
		if ev.Error != "" {
			fn(nil, errors.New(ev.Error))
		} else {
			fn(ev.Pairs, nil)
		}
	}
	return e.DrainEffects()
}

// HandleStorageResult dispatches v4 persistence completion.
func (e *Engine) HandleStorageResult(nowMs int64, ev *corev1.SecurityStorageResultEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	wf := e.ensureWF()
	fn := wf.storageWait[ev.OpId]
	delete(wf.storageWait, ev.OpId)
	if fn != nil {
		var err error
		if ev.Error != "" {
			err = errors.New(ev.Error)
		} else if ev.Conflict {
			err = devicesec.ErrStale
		}
		fn(storageReply{
			found:         ev.Found,
			revision:      ev.Revision,
			data:          ev.Data,
			conflict:      ev.Conflict,
			entries:       ev.Entries,
			hasDeviceEd:   ev.HasDeviceEd,
			deviceEdPub:   ev.DeviceEdPub,
			hasDeviceX:    ev.HasDeviceX,
			deviceXPub:    ev.DeviceXPub,
			legacyCleared: ev.LegacyCleared,
		}, err)
	}
	return e.DrainEffects()
}

// HandleRpcResult dispatches security RPC completion.
func (e *Engine) HandleRpcResult(nowMs int64, ev *corev1.SecurityRpcResultEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	wf := e.ensureWF()
	fn := wf.rpcWait[ev.OpId]
	delete(wf.rpcWait, ev.OpId)
	if fn != nil {
		var rErr *rpcError
		if ev.Error != "" || ev.ConnectCode != 0 || ev.NetworkError {
			rErr = &rpcError{
				code:    ev.ConnectCode,
				msg:     ev.Error,
				network: ev.NetworkError,
			}
		}
		fn(ev.ResponseBytes, rErr)
	}
	return e.DrainEffects()
}

func (e *Engine) checkFG(capturedGen uint64) error {
	wf := e.ensureWF()
	if !wf.foreground || wf.fgGen != capturedGen {
		return fatalWfError("foreground", text.KeySecurityThePageIsPausedReturnToTheForegroundTo, "")
	}
	return nil
}

func (e *Engine) runOp(actionID uint64, requireFG bool, work func(fgGen uint64, done func(res *corev1.SecurityWorkflowResultEffect, err error))) {
	wf := e.ensureWF()
	start := func() {
		capturedGen := wf.fgGen
		if requireFG && !wf.foreground {
			wf.opBusy = false
			e.emitActionResult(actionID, nil, fatalWfError("foreground", text.KeySecurityThePageIsPausedReturnToTheForegroundTo, ""))
			e.pumpNextOp()
			return
		}
		work(capturedGen, func(res *corev1.SecurityWorkflowResultEffect, err error) {
			wf.opBusy = false
			e.emitActionResult(actionID, res, err)
			e.emitStatusChanged()
			e.pumpNextOp()
		})
	}
	if wf.opBusy {
		wf.opQueue = append(wf.opQueue, start)
		return
	}
	wf.opBusy = true
	start()
}

func (e *Engine) pumpNextOp() {
	wf := e.ensureWF()
	if wf.opBusy || len(wf.opQueue) == 0 {
		return
	}
	next := wf.opQueue[0]
	wf.opQueue = wf.opQueue[1:]
	wf.opBusy = true
	next()
}

func (e *Engine) emitActionResult(actionID uint64, res *corev1.SecurityWorkflowResultEffect, err error) {
	if actionID == 0 {
		return
	}
	if res == nil {
		res = &corev1.SecurityWorkflowResultEffect{}
	}
	res.ActionId = actionID
	res.Status = e.BuildWorkflowStatus()
	if err == nil {
		res.Ok = true
	} else {
		res.Ok = false
		res.Error = err.Error()
		var we *wfError
		if errors.As(err, &we) {
			res.ErrorCode = we.code
			res.Retryable = we.retryable
			res.Uncertain = we.uncertain
			res.ErrorAction = we.action
		} else {
			res.ErrorCode = ErrorCode(err)
			if re := (&rpcError{}); errors.As(err, &re) {
				if re.code == connectCodeUnavailable || re.network {
					res.ErrorCode = "unavailable"
					res.Retryable = true
					res.Uncertain = true
				} else if re.code == connectCodePermissionDenied {
					res.ErrorCode = "permission_denied"
				} else if re.code == connectCodeNotFound {
					res.ErrorCode = "not_found"
				} else if re.code == connectCodeFailedPrecondition {
					res.ErrorCode = "failed_precondition"
				} else if re.code == connectCodeResourceExhausted {
					res.ErrorCode = "resource_exhausted"
				} else if re.code == connectCodeCanceled {
					res.ErrorCode = "canceled"
				}
			}
		}
	}
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityWorkflowResult{
			SecurityWorkflowResult: res,
		},
	})
}

func (e *Engine) emitStatusChanged() {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityStatusChanged{
			SecurityStatusChanged: &corev1.SecurityStatusChangedEffect{
				Status: e.BuildWorkflowStatus(),
			},
		},
	})
}

// BuildWorkflowStatus returns the unified security workflow & identity status for shell UIs.
func (e *Engine) BuildWorkflowStatus() *corev1.SecurityWorkflowStatus {
	wf := e.ensureWF()
	st := &corev1.SecurityWorkflowStatus{
		LegacyRepairNotice: wf.legacyRepairNotice,
		Server:             wf.server,
		DeviceId:           e.selfDeviceID,
		AccountId:          e.account,
		Error:              wf.bootstrapError,
	}
	if !wf.bootstrapped {
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_UNSPECIFIED
	} else if wf.pendingOp != nil && (wf.pendingOp.Kind == "create" || wf.pendingOp.Kind == "recovery") {
		if wf.pendingOp.Kind == "create" {
			st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_CREATE
		} else {
			st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_RECOVERY
		}
	} else if wf.regIntent != nil {
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_REGISTRATION
	} else if wf.bootstrapInvalid {
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_INVALID
	} else if e.isClockSkewed(e.lastNowMs) {
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_CLOCK_SKEWED
	} else if e.loaded && e.hasHead {
		if !e.isMember {
			st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_REMOVED
		} else {
			st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY
		}
	} else {
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_EMPTY
	}

	if wf.record != nil && e.hasHead {
		st.IsMember = e.isMember
		st.IsAuthority = e.isAuthority()
		st.Removed = !e.isMember
		st.AccountId = e.pin.Account
		if wf.record.Server != "" {
			st.Server = wf.record.Server
		}
		st.HeadEpoch = e.head.Core.Epoch
		if e.hasLease && e.lease.Head == e.headHash {
			st.LeaseEndMs = int64(e.lease.NotAfter)
			st.LeaseValid = e.checkLiveLease(e.lastNowMs) == nil
		}
		st.ClockSkewed = e.isClockSkewed(e.lastNowMs)
		st.RecoveryPending = e.recoveryPending
		st.RecoveryReadyMs = e.recoveryReadyMs
		st.RecoveryStatusStale = wf.recoveryStatusStale
		st.RecoveryObjectsPending = wf.record.RecoveryObjectsPending
		st.NeedsRecoveryCodeRotation = wf.record.NeedsRecoveryRotation
		st.HasAccountPublicProfile = len(wf.record.AccountPublicKey) == 32 ||
			wf.publicProfiles[PublicProfileStorageKey(st.Server, e.pin)] != nil
	}

	for _, d := range wf.devicesSnap {
		if d.ID == e.selfDeviceID && d.Meta != nil && len(d.Meta.HandoffRecoveryEd) == 32 {
			st.HasHandoffOffer = true
			if st.IsAuthority && st.LeaseEndMs == 0 {
				st.TakingOver = true
			}
		}
	}

	if op := wf.pendingOp; op != nil {
		sum := &corev1.SecurityPendingOpSummary{
			Kind:           op.Kind,
			Phase:          op.Phase,
			Action:         op.Action,
			CodeSaved:      op.CodeSaved,
			Signed:         len(op.Signed) > 0,
			HasDelivery:    len(op.Delivery) > 0,
			Rejected:       op.OfferRejected,
			TargetDeviceId: op.TargetDeviceID,
			Server:         op.Server,
		}
		if op.Pair != nil {
			sum.DeliveryPairingId = op.Pair.PairingID
			sum.DeliveryCandidateId = op.Pair.CandidateID
		}
		if op.Recovery != nil {
			sum.RecoveryId = op.Recovery.RecoveryID
			sum.RecoveryReadyMs = op.Recovery.ReadyMs
		}
		st.PendingOp = sum
	}
	return st
}

func (e *Engine) isAuthority() bool {
	return e.loaded && e.hasHead && e.isMember && e.selfDeviceID != "" && e.head.Core.AuthorityID == e.selfDeviceID
}

func (e *Engine) effectiveServer() string {
	wf := e.ensureWF()
	if wf.record != nil && wf.record.Server != "" {
		return wf.record.Server
	}
	return wf.server
}

// HandleSecurityPollTick drives pairing and recovery polling timers scheduled by Core.
func (e *Engine) HandleSecurityPollTick(nowMs int64) []*corev1.Effect {
	e.touchNow(nowMs)
	wf := e.ensureWF()
	p := wf.activePoll
	if p == nil {
		return nil
	}
	switch p.kind {
	case pollJoinRevealed:
		e.pollJoinState(p.pairingID, v1.PairingState_PAIRING_STATE_REVEALED)
	case pollJoinApproved:
		e.pollJoinState(p.pairingID, v1.PairingState_PAIRING_STATE_APPROVED)
	}
	return e.DrainEffects()
}

// HandleWorkflowAction routes UI/sync security workflow requests into Core state machines.
func (e *Engine) HandleWorkflowAction(nowMs int64, ev *corev1.SecurityWorkflowActionEvent, devices []DeviceOfferInfo, onStateChanged func()) []*corev1.Effect {
	e.touchNow(nowMs)
	wf := e.ensureWF()
	if onStateChanged != nil {
		wf.onStateChanged = onStateChanged
	}
	if devices != nil {
		wf.devicesSnap = slices.Clone(devices)
	}
	if ev == nil {
		return nil
	}
	aid := ev.ActionId
	switch a := ev.Action.(type) {
	case *corev1.SecurityWorkflowActionEvent_Bootstrap:
		e.bootstrapFromStorage(aid)
	case *corev1.SecurityWorkflowActionEvent_DismissUpgradeNotice:
		wf.legacyRepairNotice = false
		e.emitActionResult(aid, nil, nil)
		e.emitStatusChanged()
	case *corev1.SecurityWorkflowActionEvent_GetStatus:
		e.emitActionResult(aid, nil, nil)
	case *corev1.SecurityWorkflowActionEvent_SyncNow:
		touch := a.SyncNow != nil && a.SyncNow.Touch
		tok := ""
		if a.SyncNow != nil {
			tok = a.SyncNow.AuthToken
		}
		e.syncStates(touch, tok, func(err error) {
			e.emitActionResult(aid, nil, err)
			e.emitStatusChanged()
		})
	case *corev1.SecurityWorkflowActionEvent_RenewLease:
		force := a.RenewLease != nil && a.RenewLease.Flag
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.renewLeaseOp(fgGen, force, done)
		})
	case *corev1.SecurityWorkflowActionEvent_ResumePendingOp:
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.resumePendingOp(fgGen, done)
		})
	case *corev1.SecurityWorkflowActionEvent_DiscardPendingOp:
		ack := a.DiscardPendingOp != nil && a.DiscardPendingOp.Flag
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.discardPendingOp(fgGen, ack, done)
		})
	case *corev1.SecurityWorkflowActionEvent_PrepareCreation:
		srv, persisted := "", false
		if a.PrepareCreation != nil {
			srv = a.PrepareCreation.Server
			persisted = a.PrepareCreation.StoragePersisted
		}
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.prepareCreationOp(fgGen, srv, persisted, done)
		})
	case *corev1.SecurityWorkflowActionEvent_ConfirmCreationCodeSaved:
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.confirmCreationCodeSavedOp(fgGen, done)
		})
	case *corev1.SecurityWorkflowActionEvent_ContinueCreation:
		setupTok := ""
		if a.ContinueCreation != nil {
			setupTok = a.ContinueCreation.Value
		}
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.continueCreationOp(fgGen, setupTok, done)
		})
	case *corev1.SecurityWorkflowActionEvent_AdoptCreationWithoutCode:
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.adoptCreationWithoutCodeOp(fgGen, done)
		})
	case *corev1.SecurityWorkflowActionEvent_RemoveDevice:
		id, abandon := "", false
		if a.RemoveDevice != nil {
			id = a.RemoveDevice.DeviceId
			abandon = a.RemoveDevice.AbandonPairDelivery
		}
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.removeDeviceOp(fgGen, id, abandon, done)
		})
	case *corev1.SecurityWorkflowActionEvent_DeleteMachineRecords:
		mid := ""
		if a.DeleteMachineRecords != nil {
			mid = a.DeleteMachineRecords.Value
		}
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.deleteMachineRecordsOp(fgGen, mid, done)
		})
	case *corev1.SecurityWorkflowActionEvent_DeleteAccount:
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.deleteAccountOp(fgGen, done)
		})
	case *corev1.SecurityWorkflowActionEvent_PrepareRecoveryRotation:
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.prepareRecoveryRotationOp(fgGen, done)
		})
	case *corev1.SecurityWorkflowActionEvent_ConfirmRecoveryRotation:
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.confirmCodeOp(fgGen, "state", done)
		})
	case *corev1.SecurityWorkflowActionEvent_PrepareHandoffCode:
		persisted := a.PrepareHandoffCode != nil && a.PrepareHandoffCode.Flag
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.prepareHandoffCodeOp(fgGen, persisted, done)
		})
	case *corev1.SecurityWorkflowActionEvent_PublishHandoffOffer:
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.confirmCodeOp(fgGen, "offer", done)
		})
	case *corev1.SecurityWorkflowActionEvent_HandoffAuthority:
		target := ""
		if a.HandoffAuthority != nil {
			target = a.HandoffAuthority.Value
		}
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.handoffAuthorityOp(fgGen, target, done)
		})
	case *corev1.SecurityWorkflowActionEvent_ListHandoffCandidates:
		e.listHandoffCandidatesOp(aid)
	case *corev1.SecurityWorkflowActionEvent_CompleteAccountPublicProfile:
		srv, code := "", ""
		if a.CompleteAccountPublicProfile != nil {
			srv = a.CompleteAccountPublicProfile.Server
			code = a.CompleteAccountPublicProfile.Code
		}
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.completeAccountPublicProfileOp(fgGen, srv, code, done)
		})
	case *corev1.SecurityWorkflowActionEvent_StartPairing:
		origin, rep := "", ""
		if a.StartPairing != nil {
			origin = a.StartPairing.WebOrigin
			rep = a.StartPairing.ReplacesDeviceId
		}
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.startPairingOp(fgGen, origin, rep, done)
		})
	case *corev1.SecurityWorkflowActionEvent_PollPairing:
		pid := ""
		if a.PollPairing != nil {
			pid = a.PollPairing.Value
		}
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.pollPairingInitiatorOp(fgGen, pid, done)
		})
	case *corev1.SecurityWorkflowActionEvent_ApprovePairing:
		pid, sas := "", false
		if a.ApprovePairing != nil {
			pid = a.ApprovePairing.PairingId
			sas = a.ApprovePairing.SasConfirmed
		}
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.approvePairingOp(fgGen, pid, sas, done)
		})
	case *corev1.SecurityWorkflowActionEvent_FinishPairingDelivery:
		pid := ""
		if a.FinishPairingDelivery != nil {
			pid = a.FinishPairingDelivery.Value
		}
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.finishPairingDeliveryOp(fgGen, pid, done)
		})
	case *corev1.SecurityWorkflowActionEvent_CancelPairing:
		pid := ""
		if a.CancelPairing != nil {
			pid = a.CancelPairing.Value
		}
		e.runOp(aid, false, func(_ uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.cancelPairingInitiatorOp(pid, done)
		})
	case *corev1.SecurityWorkflowActionEvent_StartJoinPairing:
		e.startJoinPairingAction(aid, a.StartJoinPairing)
	case *corev1.SecurityWorkflowActionEvent_ConfirmJoinSas:
		pid := ""
		if a.ConfirmJoinSas != nil {
			pid = a.ConfirmJoinSas.Value
		}
		e.confirmJoinSasAction(aid, pid)
	case *corev1.SecurityWorkflowActionEvent_CancelJoinPairing:
		pid := ""
		if a.CancelJoinPairing != nil {
			pid = a.CancelJoinPairing.Value
		}
		e.cancelJoinPairingAction(aid, pid)
	case *corev1.SecurityWorkflowActionEvent_PrepareEmergencyRecovery:
		srv, code := "", ""
		if a.PrepareEmergencyRecovery != nil {
			srv = a.PrepareEmergencyRecovery.Server
			code = a.PrepareEmergencyRecovery.Code
		}
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.prepareEmergencyRecoveryOp(fgGen, srv, code, done)
		})
	case *corev1.SecurityWorkflowActionEvent_ContinueRecoverySigning:
		code := ""
		if a.ContinueRecoverySigning != nil {
			code = a.ContinueRecoverySigning.Value
		}
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.continueRecoverySigningOp(fgGen, code, done)
		})
	case *corev1.SecurityWorkflowActionEvent_ConfirmRecoveryCodeSaved:
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.confirmRecoveryCodeSavedOp(fgGen, done)
		})
	case *corev1.SecurityWorkflowActionEvent_ResumeRecovery:
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.resumeRecoveryOp(fgGen, done)
		})
	case *corev1.SecurityWorkflowActionEvent_CompleteRecovery:
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.completeRecoveryOp(fgGen, done)
		})
	case *corev1.SecurityWorkflowActionEvent_DiscardUnsignedRecovery:
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.discardUnsignedRecoveryOp(fgGen, done)
		})
	case *corev1.SecurityWorkflowActionEvent_DiscardRejectedRecovery:
		e.runOp(aid, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
			e.discardRejectedRecoveryOp(fgGen, done)
		})
	case *corev1.SecurityWorkflowActionEvent_CheckExitAdvice:
		e.checkExitAdviceOp(aid)
	case *corev1.SecurityWorkflowActionEvent_ClearSecurity:
		e.clearSecurityOp(aid)
	case *corev1.SecurityWorkflowActionEvent_UpdateDeviceMeta:
		e.updateDeviceMetaOp(aid, a.UpdateDeviceMeta)
	case *corev1.SecurityWorkflowActionEvent_CheckRetainedDenied:
		e.checkRetainedDeniedOp(aid)
	case *corev1.SecurityWorkflowActionEvent_RepublishRecoveryObjects:
		e.republishRecoveryObjects(func(err error) {
			e.emitActionResult(aid, nil, err)
			e.emitStatusChanged()
		})
	default:
		e.emitActionResult(aid, nil, devicesec.ErrInvalid)
	}
	return e.DrainEffects()
}

func (e *Engine) bootstrapFromStorage(actionID uint64) {
	e.reqStorage(&corev1.SecurityStorageEffect{
		Op: corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP,
	}, func(rep storageReply, err error) {
		wf := e.ensureWF()
		wf.bootstrapped = true
		wf.bootstrapInvalid = false
		wf.bootstrapError = ""
		if rep.legacyCleared {
			wf.legacyRepairNotice = true
		}
		if err != nil {
			wf.bootstrapInvalid = true
			wf.bootstrapError = err.Error()
			e.emitActionResult(actionID, nil, err)
			e.emitStatusChanged()
			return
		}
		wf.deviceEdPresent = rep.hasDeviceEd
		wf.deviceEdPub = slices.Clone(rep.deviceEdPub)
		wf.deviceXPresent = rep.hasDeviceX
		wf.deviceXPub = slices.Clone(rep.deviceXPub)
		wf.record = nil
		wf.recordRev = ""
		wf.pendingOp = nil
		wf.pendingOpRev = ""
		wf.regIntent = nil
		wf.regIntentRev = ""
		clear(wf.publicProfiles)

		var decodeErr error
		for _, ent := range rep.entries {
			if ent == nil || len(ent.Data) == 0 {
				continue
			}
			switch ent.Bucket {
			case corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD:
				rec, err := DecodePersistedRecord(ent.Data)
				if err != nil {
					decodeErr = err
				} else {
					wf.record = rec
					wf.recordRev = ent.Revision
				}
			case corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION:
				op, err := DecodePersistedOperation(ent.Data)
				if err != nil {
					decodeErr = err
				} else {
					wf.pendingOp = op
					wf.pendingOpRev = ent.Revision
				}
			case corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_REGISTRATION_INTENT:
				intent, err := DecodePersistedRegistrationIntent(ent.Data)
				if err != nil {
					decodeErr = err
				} else {
					wf.regIntent = intent
					wf.regIntentRev = ent.Revision
				}
			case corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_PUBLIC_PROFILE:
				prof, err := DecodePersistedPublicProfile(ent.Data)
				if err == nil {
					wf.publicProfiles[PublicProfileStorageKey(prof.Server, prof.Pin)] = prof
				}
			}
		}
		if decodeErr != nil {
			wf.bootstrapInvalid = true
			wf.bootstrapError = decodeErr.Error()
			e.securityLocked = true
			e.emitActionResult(actionID, nil, decodeErr)
			e.emitStatusChanged()
			return
		}
		if wf.pendingOp != nil || wf.regIntent != nil || wf.record != nil {
			e.securityLocked = true
		}
		if wf.record != nil {
			if wf.record.Server != "" {
				wf.server = wf.record.Server
			}
			e.applyPersistedRecordToEngine(wf.record, 0, nil)
		}
		e.emitActionResult(actionID, nil, nil)
		e.emitStatusChanged()
	})
}

func (e *Engine) applyPersistedRecordToEngine(rec *PersistedRecord, opID uint64, onDone func(err error)) {
	if rec == nil {
		if onDone != nil {
			onDone(nil)
		}
		return
	}
	e.securityLocked = true
	e.configured = true
	e.account = rec.Pin.Account
	e.selfDeviceID = rec.MemberID
	ev := &corev1.SecurityRecordEvent{
		OpId:                   opID,
		Account:                rec.Pin.Account,
		Domain:                 slices.Clone(rec.Pin.Domain[:]),
		Genesis:                slices.Clone(rec.Pin.Genesis[:]),
		States:                 rec.States,
		RecoveryProofs:         rec.RecoveryProofs,
		Lease:                  slices.Clone(rec.Lease),
		FloorMs:                rec.FloorMs,
		RecoveryPending:        e.recoveryPending,
		RecoveryReadyMs:        e.recoveryReadyMs,
		RecoveryObjectsPending: rec.RecoveryObjectsPending,
		Removed:                false,
		ContentKeys:            make([]*corev1.SecurityContentKey, 0, len(rec.ContentKeys)),
	}
	for _, ck := range rec.ContentKeys {
		ev.ContentKeys = append(ev.ContentKeys, &corev1.SecurityContentKey{
			ContentEpoch: ck.Epoch,
			Secret:       slices.Clone(ck.Secret[:]),
		})
	}
	e.applyRecordInternal(e.lastNowMs, ev, func(_ int64, unlockErr error) {
		e.syncUnlockedSecretsToRecord(rec)
		if wf := e.ensureWF(); wf.onStateChanged != nil {
			wf.onStateChanged()
		}
		if onDone != nil {
			onDone(unlockErr)
		}
	})
}

func (e *Engine) applyRecordInternal(nowMs int64, ev *corev1.SecurityRecordEvent, onDone func(nowMs int64, err error)) {
	prior := e.out
	e.out = nil
	effs := e.ApplyRecord(nowMs, ev)
	e.out = append(prior, effs...)
	e.withUnlockedFor(nowMs, false, func(curNow int64, err error) {
		if errors.Is(err, ErrNotMember) {
			err = nil
		}
		if onDone != nil {
			onDone(curNow, err)
		}
	})
}

func (e *Engine) openBatchInternal(nowMs int64, items []OpenItem, onDone func([]OpenResult)) {
	prior := e.out
	e.out = nil
	effs := e.OpenBatch(nowMs, items, onDone)
	e.out = append(prior, effs...)
}

func (e *Engine) sealEnvelopeInternal(
	nowMs int64,
	purpose devicesec.Purpose,
	machineID, sessionID, requestID string,
	seq uint64,
	deadlineMs uint64,
	plain []byte,
	useSessionSeed bool,
	overrideSessionSeed []byte,
	onDone func(SealResult),
) {
	prior := e.out
	e.out = nil
	effs := e.SealEnvelope(nowMs, purpose, machineID, sessionID, requestID, seq, deadlineMs, plain, useSessionSeed, overrideSessionSeed, onDone)
	e.out = append(prior, effs...)
}

func (e *Engine) syncUnlockedSecretsToRecord(rec *PersistedRecord) {
	if rec == nil {
		return
	}
	seen := make(map[uint64]int, len(rec.ContentKeys))
	for i, ck := range rec.ContentKeys {
		seen[ck.Epoch] = i
	}
	for ep, sec := range e.epochSecrets {
		if idx, ok := seen[ep]; ok {
			rec.ContentKeys[idx].Secret = sec
		} else {
			rec.ContentKeys = append(rec.ContentKeys, ContentKeyEntry{Epoch: ep, Secret: sec})
		}
	}
}

func (e *Engine) buildNotifyRecordBytes(rec *PersistedRecord, head devicesec.State) []byte {
	if rec == nil || !rec.Member || len(rec.States) == 0 || head.Core.ContentEpoch == 0 {
		return nil
	}
	var sec [32]byte
	found := false
	if s, ok := e.epochSecrets[head.Core.ContentEpoch]; ok {
		sec = s
		found = true
	} else {
		for _, ck := range rec.ContentKeys {
			if ck.Epoch == head.Core.ContentEpoch {
				sec = ck.Secret
				found = true
				break
			}
		}
	}
	if !found {
		return nil
	}
	pk, err := devicesec.DerivePurposeKeyRaw(sec[:], rec.Pin.Account, rec.Pin.Domain, head.Core.ContentEpoch, devicesec.Notify)
	if err != nil || len(pk) != 32 {
		return nil
	}
	var pkArr [32]byte
	copy(pkArr[:], pk)
	ntf := &PersistedNotifyRecord{
		Pin:        rec.Pin,
		HeadState:  rec.States[len(rec.States)-1],
		PurposeKey: pkArr,
	}
	raw, err := ntf.Encode()
	if err != nil {
		return nil
	}
	return raw
}

func (e *Engine) withAuthToken(server, preferred string, fgCheck bool, capturedGen uint64, fn func(tok string, err error)) {
	if fgCheck {
		if err := e.checkFG(capturedGen); err != nil {
			fn("", err)
			return
		}
	}
	if preferred != "" {
		fn(preferred, nil)
		return
	}
	wf := e.ensureWF()
	if wf.cachedToken != "" && (wf.tokenExpiresMs == 0 || e.lastNowMs == 0 || wf.tokenExpiresMs > e.lastNowMs+dayMs) {
		fn(wf.cachedToken, nil)
		return
	}
	e.forceLoginToken(server, corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED, "", e.selfDeviceID, fgCheck, capturedGen, true, fn)
}

func (e *Engine) forceLoginToken(server string, keyKind corev1.SecurityKeyKind, slot, deviceID string, fgCheck bool, capturedGen uint64, cacheResult bool, fn func(tok string, err error)) {
	if deviceID == "" {
		fn("", devicesec.ErrUntrusted)
		return
	}
	reqBytes, err := (&v1.GetChallengeRequest{DeviceId: deviceID}).Marshal()
	if err != nil {
		fn("", err)
		return
	}
	e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_CHALLENGE, server, "", reqBytes, func(respBytes []byte, rErr *rpcError) {
		if rErr != nil {
			fn("", rErr)
			return
		}
		var chal v1.GetChallengeResponse
		if err := chal.Unmarshal(respBytes); err != nil || len(chal.Challenge) == 0 {
			fn("", devicesec.ErrInvalid)
			return
		}
		if fgCheck {
			if err := e.checkFG(capturedGen); err != nil {
				fn("", err)
				return
			}
		}
		msg := make([]byte, 0, len(loginContext)+len(chal.Challenge))
		msg = append(msg, loginContext...)
		msg = append(msg, chal.Challenge...)
		e.requestKeySignKind(keyKind, slot, msg, func(sig []byte, signErr error) {
			if signErr != nil {
				fn("", signErr)
				return
			}
			if fgCheck {
				if err := e.checkFG(capturedGen); err != nil {
					fn("", err)
					return
				}
			}
			loginReqBytes, err := (&v1.LoginRequest{
				DeviceId:        deviceID,
				Challenge:       chal.Challenge,
				Signature:       sig,
				ProtocolVersion: protocolVersion,
			}).Marshal()
			if err != nil {
				fn("", err)
				return
			}
			e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LOGIN, server, "", loginReqBytes, func(loginRespBytes []byte, lrErr *rpcError) {
				if lrErr != nil {
					fn("", lrErr)
					return
				}
				var lr v1.LoginResponse
				if err := lr.Unmarshal(loginRespBytes); err != nil || lr.Token == "" {
					fn("", devicesec.ErrInvalid)
					return
				}
				if cacheResult {
					wf := e.ensureWF()
					wf.cachedToken = lr.Token
					wf.tokenExpiresMs = lr.ExpiresMs
					e.out = append(e.out, &corev1.Effect{
						Effect: &corev1.Effect_SecurityTokenUpdated{
							SecurityTokenUpdated: &corev1.SecurityTokenUpdatedEffect{
								Token:     lr.Token,
								ExpiresMs: lr.ExpiresMs,
							},
						},
					})
				}
				fn(lr.Token, nil)
			})
		})
	})
}

func (e *Engine) callAuthedRpc(method corev1.SecurityRpcMethod, server, preferred string, fgCheck bool, capturedGen uint64, reqBytes []byte, fn func(resp []byte, rErr *rpcError)) {
	e.withAuthToken(server, preferred, fgCheck, capturedGen, func(tok string, err error) {
		if err != nil {
			var re *rpcError
			if errors.As(err, &re) {
				fn(nil, re)
			} else {
				fn(nil, &rpcError{msg: err.Error()})
			}
			return
		}
		e.reqRpc(method, server, tok, reqBytes, func(resp []byte, rErr *rpcError) {
			if rErr != nil && rErr.code == connectCodeUnauthenticated {
				e.ensureWF().cachedToken = ""
				e.forceLoginToken(server, corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED, "", e.selfDeviceID, fgCheck, capturedGen, true, func(freshTok string, lErr error) {
					if lErr != nil {
						var lre *rpcError
						if errors.As(lErr, &lre) {
							fn(nil, lre)
						} else {
							fn(nil, &rpcError{msg: lErr.Error()})
						}
						return
					}
					e.reqRpc(method, server, freshTok, reqBytes, fn)
				})
				return
			}
			fn(resp, rErr)
		})
	})
}

// currentHeadBytes returns the serialized head state bytes from the active PersistedRecord or Head.
func (e *Engine) currentHeadBytes() []byte {
	if wf := e.wf; wf != nil && wf.record != nil && len(wf.record.States) > 0 {
		return wf.record.States[len(wf.record.States)-1]
	}
	if e.hasHead {
		if b, err := e.head.MarshalBinary(); err == nil {
			return b
		}
	}
	return nil
}

func (e *Engine) currentSigningTime(nowMs int64) uint64 {
	var floor, leaseIssued uint64
	if e.floorMs > 0 {
		floor = uint64(e.floorMs)
	}
	if wf := e.wf; wf != nil && wf.record != nil && wf.record.FloorMs > 0 && uint64(wf.record.FloorMs) > floor {
		floor = uint64(wf.record.FloorMs)
	}
	if e.hasLease {
		leaseIssued = e.lease.IssuedAt
	}
	eff := max(max(uint64(max(1, nowMs)), floor), leaseIssued)
	if e.hasHead && e.head.Core.IssuedAt > eff {
		eff = e.head.Core.IssuedAt
	}
	return eff
}

func (e *Engine) buildAndSignStateWithKEM(
	keyKind corev1.SecurityKeyKind,
	slot string,
	fgCheck bool,
	capturedGen uint64,
	build func(hpke devicesec.HPKESender) (devicesec.State, error),
	members []devicesec.Member,
	fn func(signed devicesec.State, err error),
) {
	uniq := make(map[string]*corev1.SecurityEncapsulateRecipient, len(members))
	recips := make([]*corev1.SecurityEncapsulateRecipient, 0, len(members))
	for _, m := range members {
		if _, exists := uniq[m.ID]; exists {
			continue
		}
		r := &corev1.SecurityEncapsulateRecipient{
			MemberId: m.ID,
			XPublic:  slices.Clone(m.XPublic[:]),
		}
		uniq[m.ID] = r
		recips = append(recips, r)
	}
	e.reqKeyEncapsulate(recips, func(pairs []*corev1.SecurityEncapsulatedPair, err error) {
		if err != nil {
			fn(devicesec.State{}, err)
			return
		}
		if fgCheck {
			if err := e.checkFG(capturedGen); err != nil {
				fn(devicesec.State{}, err)
				return
			}
		}
		kemMap := make(map[string]struct{ Enc, DH [32]byte }, len(pairs))
		byX := make(map[[32]byte]struct{ enc, dh [32]byte }, len(pairs))
		for _, p := range pairs {
			if p == nil || len(p.Enc) != 32 || len(p.Dh) != 32 {
				fn(devicesec.State{}, devicesec.ErrInvalid)
				return
			}
			var item struct{ enc, dh [32]byte }
			copy(item.enc[:], p.Enc)
			copy(item.dh[:], p.Dh)
			kemMap[p.MemberId] = struct{ Enc, DH [32]byte }{item.enc, item.dh}
			for _, m := range members {
				if m.ID == p.MemberId {
					byX[m.XPublic] = item
				}
			}
		}
		sender := &encapHPKESender{byX: byX}
		unsigned, err := build(sender)
		if err != nil {
			fn(devicesec.State{}, err)
			return
		}
		sb, err := unsigned.SigningBytes()
		if err != nil {
			fn(devicesec.State{}, err)
			return
		}
		e.requestKeySignKind(keyKind, slot, sb, func(sig []byte, signErr error) {
			if signErr != nil {
				fn(devicesec.State{}, signErr)
				return
			}
			if fgCheck {
				if err := e.checkFG(capturedGen); err != nil {
					fn(devicesec.State{}, err)
					return
				}
			}
			if len(sig) != devicesec.Ed25519SignatureSize {
				fn(devicesec.State{}, devicesec.ErrUntrusted)
				return
			}
			unsigned.Signature = slices.Clone(sig)
			if _, err := unsigned.MarshalBinary(); err != nil {
				fn(devicesec.State{}, err)
				return
			}
			fn(unsigned, nil)
		})
	})
}

type encapHPKESender struct {
	byX map[[32]byte]struct{ enc, dh [32]byte }
}

func (s *encapHPKESender) Seal(recipient, info, aad, plaintext []byte) ([]byte, []byte, error) {
	if len(recipient) != 32 {
		return nil, nil, devicesec.ErrInvalid
	}
	var k [32]byte
	copy(k[:], recipient)
	pair, ok := s.byX[k]
	if !ok {
		return nil, nil, devicesec.ErrInvalid
	}
	ct, err := devicesec.HPKESealWithDH(recipient, pair.enc[:], pair.dh[:], info, aad, plaintext)
	if err != nil {
		return nil, nil, err
	}
	return slices.Clone(pair.enc[:]), ct, nil
}

func (e *Engine) buildAndSignLease(head devicesec.State, keyKind corev1.SecurityKeyKind, slot string, nowMs uint64, fn func(l devicesec.LeaseCertificate, err error)) {
	l, err := devicesec.NewLease(head, nowMs)
	if err != nil {
		fn(devicesec.LeaseCertificate{}, err)
		return
	}
	sb, err := l.SigningBytes()
	if err != nil {
		fn(devicesec.LeaseCertificate{}, err)
		return
	}
	e.requestKeySignKind(keyKind, slot, sb, func(sig []byte, signErr error) {
		if signErr != nil {
			fn(devicesec.LeaseCertificate{}, signErr)
			return
		}
		l.Signature = slices.Clone(sig)
		if err := l.Verify(head); err != nil {
			fn(devicesec.LeaseCertificate{}, err)
			return
		}
		fn(l, nil)
	})
}

// needsLeaseRenewal matches docs/architecture.md: renew when remaining < 24h AND elapsed since issue >= 12h.
func needsLeaseRenewal(lease *devicesec.LeaseCertificate, nowMs uint64) bool {
	if lease == nil {
		return true
	}
	if nowMs == 0 {
		nowMs = 1
	}
	if nowMs < lease.IssuedAt {
		return false
	}
	var remaining int64
	if nowMs < lease.NotAfter {
		remaining = int64(lease.NotAfter - nowMs)
	}
	elapsed := int64(nowMs - lease.IssuedAt)
	return remaining < dayMs && elapsed >= halfDayMs
}

func isValidNotifyPrefix(req string) bool {
	return req != "" && !bytes.ContainsRune([]byte(req), 0)
}
