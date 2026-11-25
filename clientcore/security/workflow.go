package security

import (
	"bytes"
	"errors"
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/entropy"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

const (
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
)

// DeviceOfferInfo carries verified client device metadata from sync directory.
type DeviceOfferInfo struct {
	ID         string
	Kind       v1.DeviceKind
	RecoveryEd []byte
	Meta       *v1.DeviceMeta
	MetaRev    uint64
	LastSeenMs int64
}

type wfError struct {
	reason    WorkflowReason
	raw       string
	code      string
	retryable bool
	uncertain bool
	action    string
}

func (e *wfError) Error() string {
	if e == nil {
		return ""
	}
	if e.raw != "" {
		return e.raw
	}
	if e.code != "" {
		return "security: " + e.code
	}
	return "security error"
}

//go:noinline
func newWfError(code string, reason WorkflowReason) *wfError {
	return &wfError{code: code, reason: reason, retryable: true}
}

//go:noinline
func newRawWfError(code, raw string) *wfError {
	return &wfError{code: code, raw: raw, retryable: true}
}

//go:noinline
func fatalWfError(code string, reason WorkflowReason, action string) *wfError {
	return &wfError{code: code, reason: reason, retryable: false, action: action}
}

type rpcError struct {
	code           int32
	msg            string
	network        bool
	unresolvedHost bool
	detail         *v1.RelayErrorDetail
}

func (e *rpcError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return "rpc failed"
}

func (e *rpcError) relayReason() v1.RelayErrorReason {
	if e == nil || e.detail == nil || !isKnownRelayErrorReason(e.detail.Reason) {
		return v1.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED
	}
	return e.detail.Reason
}

func (e *rpcError) isTokenInvalid() bool {
	if e == nil {
		return false
	}
	if r := e.relayReason(); r != v1.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
		return r == v1.RelayErrorReason_RELAY_ERROR_REASON_TOKEN_INVALID
	}
	return e.code == ConnectCodeUnauthenticated
}

func (e *rpcError) isSignupPowRequired() bool {
	if e == nil {
		return false
	}
	if r := e.relayReason(); r != v1.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
		return r == v1.RelayErrorReason_RELAY_ERROR_REASON_SIGNUP_POW_REQUIRED
	}
	return e.code == ConnectCodeFailedPrecondition
}

func (e *rpcError) isCreateInitLoginMissing() bool {
	if e == nil {
		return false
	}
	if r := e.relayReason(); r != v1.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
		return r == v1.RelayErrorReason_RELAY_ERROR_REASON_LOGIN_FAILED
	}
	return e.code == ConnectCodePermissionDenied || e.code == ConnectCodeNotFound || e.code == ConnectCodeUnauthenticated
}

func (e *rpcError) isRetainedLoginDenied() bool {
	if e == nil {
		return false
	}
	if r := e.relayReason(); r != v1.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
		return r == v1.RelayErrorReason_RELAY_ERROR_REASON_LOGIN_FAILED ||
			r == v1.RelayErrorReason_RELAY_ERROR_REASON_DEVICE_NOT_MEMBER ||
			r == v1.RelayErrorReason_RELAY_ERROR_REASON_ACCOUNT_SUSPENDED
	}
	return e.code == ConnectCodePermissionDenied
}

func (e *rpcError) isPairingGone() bool {
	if e == nil {
		return false
	}
	if r := e.relayReason(); r != v1.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
		return r == v1.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_INVALID_OR_EXPIRED
	}
	return e.code == ConnectCodeNotFound
}

func (e *rpcError) isJoinPairingGone() bool {
	if e == nil {
		return false
	}
	if r := e.relayReason(); r != v1.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
		return r == v1.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_INVALID_OR_EXPIRED ||
			r == v1.RelayErrorReason_RELAY_ERROR_REASON_DEVICE_NOT_MEMBER ||
			r == v1.RelayErrorReason_RELAY_ERROR_REASON_ACCOUNT_SUSPENDED
	}
	return e.code == ConnectCodeNotFound || e.code == ConnectCodePermissionDenied
}

func (e *rpcError) isDeliveryFallback() bool {
	if e == nil {
		return false
	}
	if r := e.relayReason(); r != v1.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
		return r == v1.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_INVALID_OR_EXPIRED
	}
	return e.code == ConnectCodeNotFound || e.code == ConnectCodeFailedPrecondition || e.code == ConnectCodeCanceled
}

func (e *rpcError) isResumeRegFallbackLogin() bool {
	if e == nil {
		return false
	}
	if r := e.relayReason(); r != v1.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
		// Definite non-duplicate rejections should surface directly rather than
		// falling back to a login probe.
		return false
	}
	return e.code == ConnectCodeAlreadyExists || e.code == ConnectCodePermissionDenied
}

type codeDraft struct {
	kind             string // "state" (recovery rotation) or "offer"
	parent           [32]byte
	recoveryEd       []byte
	storagePersisted bool
}

type initiatorPairingSession struct {
	pairingID             string
	code                  string
	url                   string
	server                string
	expiresMs             int64
	ephemSlot             string
	ephemPub              []byte
	trust                 []byte
	replacesDeviceID      string
	joined                bool
	approved              bool
	delivered             bool
	candidateID           string
	candidateEd           []byte
	candidateX            []byte
	candidateKind         v1.DeviceKind
	sasEmoji              []string
	pairKey               []byte
	joinerMetaReceived    bool
	joinerMeta            *v1.DeviceMeta
	replacementCandidates []*corev1.SecurityPairingReplacementCandidate
	removeDeviceIDs       []string
}

type joinerPairingSession struct {
	pairingID      string
	server         string
	code           string
	commitHint     []byte
	commit         []byte
	existingRepair bool
	replace        bool
	platform       string
	description    string
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

type syncWaiter struct {
	target  taskTarget
	hasSink bool
	sinkTag uint64
}

type activePoll struct {
	kind      activePollKind
	pairingID string
	actionID  uint64
}

type workflowState struct {
	server             string
	foreground         bool
	fgGen              uint64
	bootstrapped       bool
	bootstrapInvalid   bool
	bootstrapErrReason WorkflowReason
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

	codeDraft          *codeDraft
	opBusy             bool
	pumpingOps         bool
	removingOldDevices bool
	opQueue            []queuedOp

	lastLeaseRenewAttemptMs int64
	recoveryStatusStale     bool
	syncInFlight            bool
	syncWaiters             []syncWaiter

	onStateChanged func()
	asyncSink      AsyncSink
	devicesSnap    []DeviceOfferInfo

	initPairings map[string]*initiatorPairingSession
	joinPairings map[string]*joinerPairingSession
	activePoll   *activePoll

	entropyWait []entropyTaskWait
	seqCounter  uint64
}

//go:noinline
func (e *Engine) ensureWF() *workflowState {
	if e.wf == nil {
		e.wf = &workflowState{
			foreground:     true,
			publicProfiles: make(map[string]*PersistedPublicProfile),
			initPairings:   make(map[string]*initiatorPairingSession),
			joinPairings:   make(map[string]*joinerPairingSession),
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
func (e *Engine) SetStateChangedCallback(fn func()) { e.ensureWF().onStateChanged = fn }

// SetAsyncSink registers a closure-free sink for async Engine callbacks.
func (e *Engine) SetAsyncSink(s AsyncSink) { e.ensureWF().asyncSink = s }

func (e *Engine) notifyStateChanged() {
	wf := e.ensureWF()
	if wf.onStateChanged != nil {
		wf.onStateChanged()
	} else if wf.asyncSink != nil {
		wf.asyncSink.OnSecurityStateChanged()
	}
}

// SetDeviceDirectory updates the verified client device directory known from sync.
func (e *Engine) SetDeviceDirectory(devices []DeviceOfferInfo) {
	e.ensureWF().devicesSnap = slices.Clone(devices)
}

// HasWorkflowBootstrapped reports whether the core-driven v4 workflow storage bootstrap has completed.
func (e *Engine) HasWorkflowBootstrapped() bool { return e.wf != nil && e.wf.bootstrapped }

// SetAuthToken seeds an externally obtained bearer token into the workflow state.
func (e *Engine) SetAuthToken(token string, expiresMs int64) {
	wf := e.ensureWF()
	wf.cachedToken, wf.tokenExpiresMs = token, expiresMs
}

// FlushEntropy delivers buffered OS entropy to any pending workflow entropy waiters.
func (e *Engine) FlushEntropy() []*corev1.Effect {
	wf := e.wf
	if wf == nil || len(wf.entropyWait) == 0 {
		return nil
	}
	var remaining []entropyTaskWait
	ready := wf.entropyWait
	wf.entropyWait = nil
	for i, w := range ready {
		if e.entropy.Avail() >= w.need {
			if w.task != nil {
				e.stepTask(w.task, &secEvent{kind: evEntropy, tag: w.tag})
			}
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
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_RequestEntropy{RequestEntropy: &corev1.RequestEntropyEffect{MinRandomBytes: int32(max(minBytes, entropy.RefillBytes))}},
	})
}

func (e *Engine) readRandom(dst []byte) bool {
	_, err := e.entropy.Read(dst)
	return err == nil
}

//go:noinline
func (e *Engine) reqKeyDiscard(kind corev1.SecurityKeyKind, slot string) {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityKeyDiscard{SecurityKeyDiscard: &corev1.SecurityKeyDiscardEffect{KeyKind: kind, Slot: slot}},
	})
}

// HandleKeyGenerated dispatches key generation completion.
func (e *Engine) HandleKeyGenerated(nowMs int64, ev *corev1.SecurityKeyGeneratedEvent) []*corev1.Effect {
	if ev == nil {
		return e.finishTaskEv(nowMs, 0, "", secEvent{})
	}
	return e.finishTaskEv(nowMs, ev.OpId, ev.Error, secEvent{kind: evKeyGen, bytes: ev.PublicKey})
}

// HandleKeyEncapsulated dispatches KEM encapsulation completion.
func (e *Engine) HandleKeyEncapsulated(nowMs int64, ev *corev1.SecurityKeyEncapsulatedEvent) []*corev1.Effect {
	if ev == nil {
		return e.finishTaskEv(nowMs, 0, "", secEvent{})
	}
	return e.finishTaskEv(nowMs, ev.OpId, ev.Error, secEvent{kind: evEncap, encap: ev.Pairs})
}

// HandleStorageResult dispatches v4 persistence completion.
func (e *Engine) HandleStorageResult(nowMs int64, ev *corev1.SecurityStorageResultEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	if w, ok := e.takeTask(ev.OpId); ok && w.task != nil {
		var err error
		if ev.Error != "" {
			err = newRawWfError("storage", ev.Error)
		} else if ev.Conflict {
			err = devicesec.ErrStale
		}
		e.stepTask(w.task, &secEvent{
			kind: evStorage, tag: w.tag, err: err,
			storage: &storageReply{
				found: ev.Found, revision: ev.Revision, data: ev.Data, conflict: ev.Conflict, entries: ev.Entries,
				hasDeviceEd: ev.HasDeviceEd, deviceEdPub: ev.DeviceEdPub, hasDeviceX: ev.HasDeviceX, deviceXPub: ev.DeviceXPub, legacyCleared: ev.LegacyCleared,
			},
		})
	}
	return e.DrainEffects()
}

// HandleRpcResult dispatches security RPC completion.
func (e *Engine) HandleRpcResult(nowMs int64, ev *corev1.SecurityRpcResultEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	if w, ok := e.takeTask(ev.OpId); ok && w.task != nil {
		se := &secEvent{kind: evRPC, tag: w.tag, bytes: ev.ResponseBytes}
		if ev.Error != "" || ev.ConnectCode != 0 || ev.NetworkError || len(ev.ErrorDetails) > 0 {
			rErr := &rpcError{
				code:    ev.ConnectCode,
				msg:     ev.Error,
				network: ev.NetworkError,
				detail:  ParseRelayErrorDetail(ev.ErrorDetails),
			}
			se.rpcErr, se.err = rErr, rErr
		}
		e.stepTask(w.task, se)
	}
	return e.DrainEffects()
}

// HandleRelayRpcResult dispatches unified RelayRpcResultEvent completions belonging to security tasks.
func (e *Engine) HandleRelayRpcResult(nowMs int64, ev *corev1.RelayRpcResultEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	if w, ok := e.takeTask(ev.OpId); ok && w.task != nil {
		se := &secEvent{kind: evRPC, tag: w.tag, bytes: ev.ResponseBytes}
		if ev.DiagnosticMessage != "" || ev.ConnectCode != 0 || ev.NetworkError || ev.UnresolvedHost || len(ev.ErrorDetails) > 0 {
			rErr := &rpcError{
				code:           ev.ConnectCode,
				msg:            ev.DiagnosticMessage,
				network:        ev.NetworkError,
				unresolvedHost: ev.UnresolvedHost,
				detail:         ParseRelayErrorDetail(ev.ErrorDetails),
			}
			se.rpcErr, se.err = rErr, rErr
		}
		e.stepTask(w.task, se)
	}
	return e.DrainEffects()
}

//go:noinline
func (e *Engine) checkFG(capturedGen uint64) error {
	wf := e.ensureWF()
	if !wf.foreground || wf.fgGen != capturedGen {
		return fatalWfError("foreground", WorkflowReasonPagePaused, "")
	}
	return nil
}

//go:noinline
func unpackProtoStates(list []*v1.SecurityState) ([][]byte, [][]byte, error) {
	rawStates, rawProofs := make([][]byte, 0, len(list)), make([][]byte, 0, len(list))
	for _, ss := range list {
		if ss == nil || len(ss.State) == 0 {
			return nil, nil, devicesec.ErrInvalid
		}
		rawStates, rawProofs = append(rawStates, ss.State), append(rawProofs, ss.Recovery)
	}
	return rawStates, rawProofs, nil
}

//go:noinline
func (e *Engine) emitActionResult(actionID uint64, res *corev1.SecurityWorkflowResultEffect, err error) {
	if actionID == 0 {
		return
	}
	if res == nil {
		res = &corev1.SecurityWorkflowResultEffect{}
	}
	res.ActionId, res.Status, res.Ok = actionID, e.BuildWorkflowStatus(), err == nil
	var meta WorkflowMeta
	if res.ExitAdvice != nil {
		switch {
		case res.ExitAdvice.Unresolved:
			meta.ExitReason = ExitAdviceUnresolvedOp
		case res.ExitAdvice.Authority:
			meta.ExitReason = ExitAdviceOnlyManagingClient
		default:
			meta.ExitReason = ExitAdviceDefault
		}
	}
	if err != nil {
		meta.ErrRaw = err.Error()
		if we := asWfError(err); we != nil {
			res.Retryable, res.Uncertain, res.ErrorAction = we.retryable, we.uncertain, we.action
			res.StructuredErrorCode = StructuredErrorCodeFromString(we.code)
			meta.ErrReason = we.reason
		} else {
			res.StructuredErrorCode = StructuredErrorCode(err)
			if re := asRpcError(err); re != nil {
				switch re.code {
				case ConnectCodePermissionDenied:
					res.StructuredErrorCode = corev1.SecurityErrorCode_SECURITY_ERROR_CODE_PERMISSION_DENIED
				case ConnectCodeNotFound:
					res.StructuredErrorCode = corev1.SecurityErrorCode_SECURITY_ERROR_CODE_NOT_FOUND
				case ConnectCodeFailedPrecondition:
					res.StructuredErrorCode = corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FAILED_PRECONDITION
				case ConnectCodeResourceExhausted:
					res.StructuredErrorCode = corev1.SecurityErrorCode_SECURITY_ERROR_CODE_RESOURCE_EXHAUSTED
				case ConnectCodeCanceled:
					res.StructuredErrorCode = corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CANCELED
				default:
					if re.code == ConnectCodeUnavailable || re.network {
						res.Retryable, res.Uncertain = true, true
						res.StructuredErrorCode = corev1.SecurityErrorCode_SECURITY_ERROR_CODE_UNAVAILABLE
					}
				}
			}
		}
	}
	e.recordWorkflowMeta(actionID, meta)
	e.out = append(e.out, &corev1.Effect{Effect: &corev1.Effect_SecurityWorkflowResult{SecurityWorkflowResult: res}})
}

//go:noinline
func (e *Engine) emitStatusChanged() {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityStatusChanged{SecurityStatusChanged: &corev1.SecurityStatusChangedEffect{Status: e.BuildWorkflowStatus()}},
	})
}

// BuildWorkflowStatus returns the unified security workflow & identity status for shell UIs.
// BootstrapError returns the structured error metadata from the latest security bootstrap.
func (e *Engine) BootstrapError() BootstrapErrorMeta {
	if e == nil || e.wf == nil {
		return BootstrapErrorMeta{}
	}
	return BootstrapErrorMeta{Reason: e.wf.bootstrapErrReason, Raw: e.wf.bootstrapError}
}

func (e *Engine) BuildWorkflowStatus() *corev1.SecurityWorkflowStatus {
	wf := e.ensureWF()
	st := &corev1.SecurityWorkflowStatus{
		LegacyRepairNotice: wf.legacyRepairNotice, Server: wf.server, DeviceId: e.selfDeviceID, AccountId: e.account,
	}
	switch {
	case !wf.bootstrapped:
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_UNSPECIFIED
	case wf.pendingOp != nil && wf.pendingOp.Kind == "create":
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_CREATE
	case wf.pendingOp != nil && wf.pendingOp.Kind == "recovery":
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_RECOVERY
	case wf.regIntent != nil:
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_REGISTRATION
	case wf.bootstrapInvalid:
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_INVALID
	case e.isClockSkewed(e.lastNowMs):
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_CLOCK_SKEWED
	case e.loaded && e.hasHead && !e.isMember:
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_REMOVED
	case e.loaded && e.hasHead:
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY
	default:
		st.Bootstrap = corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_EMPTY
	}

	if wf.record != nil && e.hasHead {
		st.IsMember, st.IsAuthority, st.Removed, st.AccountId = e.isMember, e.isAuthority(), !e.isMember, e.pin.Account
		if wf.record.Server != "" {
			st.Server = wf.record.Server
		}
		st.HeadEpoch = e.head.Core.Epoch
		if e.hasLease && e.lease.Head == e.headHash {
			st.LeaseEndMs, st.LeaseValid = int64(e.lease.NotAfter), e.checkLiveLease(e.lastNowMs) == nil
		}
		st.ClockSkewed, st.RecoveryPending, st.RecoveryReadyMs = e.isClockSkewed(e.lastNowMs), e.recoveryPending, e.recoveryReadyMs
		st.RecoveryStatusStale, st.RecoveryObjectsPending = wf.recoveryStatusStale, wf.record.RecoveryObjectsPending
		st.NeedsRecoveryCodeRotation = wf.record.NeedsRecoveryRotation
		st.HasAccountPublicProfile = len(wf.record.AccountPublicKey) == 32 || wf.publicProfiles[PublicProfileStorageKey(st.Server, e.pin)] != nil
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
			Kind: op.Kind, Phase: op.Phase, Action: op.Action, CodeSaved: op.CodeSaved,
			Signed: len(op.Signed) > 0, HasDelivery: len(op.Delivery) > 0, Rejected: op.OfferRejected,
			TargetDeviceId: op.TargetDeviceID, Server: op.Server,
		}
		if op.Pair != nil {
			sum.DeliveryPairingId, sum.DeliveryCandidateId = op.Pair.PairingID, op.Pair.CandidateID
		}
		if op.Recovery != nil {
			sum.RecoveryId, sum.RecoveryReadyMs = op.Recovery.RecoveryID, op.Recovery.ReadyMs
		}
		st.PendingOp = sum
	}
	if pendingRem := e.pendingRemoveDeviceIDs(); len(pendingRem) > 0 {
		st.PendingRemoveDeviceIds = pendingRem
	}
	return st
}

func (e *Engine) pendingRemoveDeviceIDs() []string {
	wf := e.ensureWF()
	if !e.isAuthority() || wf.record == nil || len(wf.record.PendingRemoveDeviceIDs) == 0 {
		return nil
	}
	out := make([]string, 0, len(wf.record.PendingRemoveDeviceIDs))
	seen := make(map[string]bool, len(wf.record.PendingRemoveDeviceIDs))
	for _, id := range wf.record.PendingRemoveDeviceIDs {
		if id == "" || seen[id] || id == e.selfDeviceID || id == e.head.Core.AuthorityID {
			continue
		}
		if m, live := e.head.Core.Member(id); !live || m.Kind != devicesec.Client {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
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
	p := e.ensureWF().activePoll
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
		e.readBootstrapStorage(&bootstrapTask{actionID: aid})
	case *corev1.SecurityWorkflowActionEvent_DismissUpgradeNotice:
		wf.legacyRepairNotice = false
		e.emitActionResult(aid, nil, nil)
		e.emitStatusChanged()
	case *corev1.SecurityWorkflowActionEvent_GetStatus:
		e.emitActionResult(aid, nil, nil)
	case *corev1.SecurityWorkflowActionEvent_SyncNow:
		e.syncStates(a.SyncNow.GetTouch(), a.SyncNow.GetAuthToken(), actionTarget(aid, targetActionStatus))
	case *corev1.SecurityWorkflowActionEvent_RenewLease:
		e.runOpTask(aid, true, &renewLeaseTask{force: a.RenewLease.GetFlag()})
	case *corev1.SecurityWorkflowActionEvent_ResumePendingOp:
		e.runOpTask(aid, true, &resumePendingTask{includePendingRemovals: true})
	case *corev1.SecurityWorkflowActionEvent_DiscardPendingOp:
		e.runOpTask(aid, true, &discardPendingTask{acknowledgePublished: a.DiscardPendingOp.GetFlag()})
	case *corev1.SecurityWorkflowActionEvent_PrepareCreation:
		e.runOpTask(aid, true, &prepareCreationTask{rawServer: a.PrepareCreation.GetServer(), storagePersisted: a.PrepareCreation.GetStoragePersisted()})
	case *corev1.SecurityWorkflowActionEvent_ConfirmCreationCodeSaved:
		e.runOpTask(aid, true, &confirmOpSavedTask{kind: "create"})
	case *corev1.SecurityWorkflowActionEvent_ContinueCreation:
		e.runOpTask(aid, true, &continueCreationTask{setupToken: a.ContinueCreation.GetValue()})
	case *corev1.SecurityWorkflowActionEvent_AdoptCreationWithoutCode:
		e.runOpTask(aid, true, &adoptCreationWithoutCodeTask{})
	case *corev1.SecurityWorkflowActionEvent_RemoveDevice:
		e.runOpTask(aid, true, &removeDeviceTask{id: a.RemoveDevice.GetDeviceId()})
	case *corev1.SecurityWorkflowActionEvent_DeleteMachineRecords:
		e.runOpTask(aid, true, &deleteMachineRecordsTask{machineID: a.DeleteMachineRecords.GetValue()})
	case *corev1.SecurityWorkflowActionEvent_DeleteAccount:
		e.runOpTask(aid, true, &deleteAccountTask{})
	case *corev1.SecurityWorkflowActionEvent_PrepareRecoveryRotation:
		e.runOpTask(aid, true, &prepareCodeTask{kind: "state"})
	case *corev1.SecurityWorkflowActionEvent_ConfirmRecoveryRotation:
		e.runOpTask(aid, true, &confirmCodeTask{kind: "state"})
	case *corev1.SecurityWorkflowActionEvent_PrepareHandoffCode:
		e.runOpTask(aid, true, &prepareCodeTask{kind: "offer", storagePersisted: a.PrepareHandoffCode.GetFlag()})
	case *corev1.SecurityWorkflowActionEvent_PublishHandoffOffer:
		e.runOpTask(aid, true, &confirmCodeTask{kind: "offer"})
	case *corev1.SecurityWorkflowActionEvent_HandoffAuthority:
		e.runOpTask(aid, true, &handoffAuthorityTask{incomingID: a.HandoffAuthority.GetValue()})
	case *corev1.SecurityWorkflowActionEvent_ListHandoffCandidates:
		e.listHandoffCandidatesOp(aid)
	case *corev1.SecurityWorkflowActionEvent_CompleteAccountPublicProfile:
		e.runOpTask(aid, true, &completeAccountPublicProfileTask{rawServer: a.CompleteAccountPublicProfile.GetServer(), code: a.CompleteAccountPublicProfile.GetCode()})
	case *corev1.SecurityWorkflowActionEvent_StartPairing:
		e.runOpTask(aid, true, &startPairingTask{webOrigin: a.StartPairing.GetWebOrigin(), replacesDeviceID: a.StartPairing.GetReplacesDeviceId()})
	case *corev1.SecurityWorkflowActionEvent_PollPairing:
		e.runOpTask(aid, true, &pollPairingInitiatorTask{pairingID: a.PollPairing.GetValue()})
	case *corev1.SecurityWorkflowActionEvent_ApprovePairing:
		e.runOpTask(aid, true, &approvePairingTask{
			pairingID:       a.ApprovePairing.GetPairingId(),
			sasConfirmed:    a.ApprovePairing.GetSasConfirmed(),
			removeDeviceIDs: slices.Clone(a.ApprovePairing.GetRemoveDeviceIds()),
		})
	case *corev1.SecurityWorkflowActionEvent_FinishPairingDelivery:
		e.runOpTask(aid, true, &finishPairingDeliveryTask{pairingID: a.FinishPairingDelivery.GetValue()})
	case *corev1.SecurityWorkflowActionEvent_CancelPairing:
		e.runOpTask(aid, false, &cancelPairingInitiatorTask{pairingID: a.CancelPairing.GetValue()})
	case *corev1.SecurityWorkflowActionEvent_StartJoinPairing:
		e.startJoinPairingAction(aid, a.StartJoinPairing)
	case *corev1.SecurityWorkflowActionEvent_ConfirmJoinSas:
		e.confirmJoinSasAction(aid, a.ConfirmJoinSas.GetValue())
	case *corev1.SecurityWorkflowActionEvent_CancelJoinPairing:
		e.cancelJoinPairingAction(aid, a.CancelJoinPairing.GetValue())
	case *corev1.SecurityWorkflowActionEvent_PrepareEmergencyRecovery:
		e.runOpTask(aid, true, &prepareEmergencyRecoveryTask{rawServer: a.PrepareEmergencyRecovery.GetServer(), code: a.PrepareEmergencyRecovery.GetCode()})
	case *corev1.SecurityWorkflowActionEvent_ContinueRecoverySigning:
		e.runOpTask(aid, true, &continueRecoverySigningTask{oldCode: a.ContinueRecoverySigning.GetValue()})
	case *corev1.SecurityWorkflowActionEvent_ConfirmRecoveryCodeSaved:
		e.runOpTask(aid, true, &confirmOpSavedTask{kind: "recovery"})
	case *corev1.SecurityWorkflowActionEvent_ResumeRecovery:
		e.runOpTask(aid, true, &resumeRecoveryTask{})
	case *corev1.SecurityWorkflowActionEvent_CompleteRecovery:
		e.runOpTask(aid, true, &completeRecoveryTask{})
	case *corev1.SecurityWorkflowActionEvent_DiscardUnsignedRecovery:
		e.runOpTask(aid, true, &discardUnsignedRecoveryTask{})
	case *corev1.SecurityWorkflowActionEvent_DiscardRejectedRecovery:
		e.runOpTask(aid, true, &discardRejectedRecoveryTask{})
	case *corev1.SecurityWorkflowActionEvent_CheckExitAdvice:
		e.checkExitAdviceOp(aid)
	case *corev1.SecurityWorkflowActionEvent_ClearSecurity:
		e.startClearLocalSecurity(actionTarget(aid, targetAction), true)
	case *corev1.SecurityWorkflowActionEvent_UpdateDeviceMeta:
		e.updateDeviceMetaOp(aid, a.UpdateDeviceMeta)
	case *corev1.SecurityWorkflowActionEvent_CheckRetainedDenied:
		e.checkRetainedDeniedOp(aid)
	case *corev1.SecurityWorkflowActionEvent_RepublishRecoveryObjects:
		e.republishRecoveryObjects(actionTarget(aid, targetActionStatus))
	default:
		e.emitActionResult(aid, nil, devicesec.ErrInvalid)
	}
	return e.DrainEffects()
}

type bootstrapTask struct{ actionID uint64 }

func isConfirmedBootstrapCorruption(err error) bool {
	if err == nil {
		return false
	}
	if we := asWfError(err); we != nil {
		return we.code == "invalid" || we.code == "untrusted"
	}
	return errors.Is(err, errStoreCorrupt) ||
		errors.Is(err, devicesec.ErrInvalid) ||
		errors.Is(err, devicesec.ErrUntrusted)
}

func (t *bootstrapTask) finish(e *Engine, wf *workflowState, err error) error {
	if err != nil {
		if isConfirmedBootstrapCorruption(err) {
			wf.bootstrapInvalid = true
		} else {
			// Transient storage/keystore/keychain unavailability must not be
			// conflated with confirmed record corruption or trigger reset.
			wf.bootstrapped = false
			wf.bootstrapInvalid = false
		}
		wf.bootstrapError = err.Error()
		if we := asWfError(err); we != nil {
			wf.bootstrapErrReason = we.reason
		} else {
			wf.bootstrapErrReason = WorkflowReasonNone
		}
	}
	e.emitActionResult(t.actionID, nil, err)
	e.emitStatusChanged()
	return nil
}

func (t *bootstrapTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	if ev.kind == evChildDone {
		return t.finish(e, wf, ev.err)
	}
	wf.bootstrapped, wf.bootstrapInvalid, wf.bootstrapErrReason, wf.bootstrapError = true, false, WorkflowReasonNone, ""
	if ev.storage != nil && ev.storage.legacyCleared {
		wf.legacyRepairNotice = true
	}
	if ev.err != nil {
		return t.finish(e, wf, ev.err)
	}
	st := ev.storage
	wf.deviceEdPresent, wf.deviceEdPub = st.hasDeviceEd, slices.Clone(st.deviceEdPub)
	wf.deviceXPresent, wf.deviceXPub = st.hasDeviceX, slices.Clone(st.deviceXPub)
	wf.record, wf.recordRev, wf.pendingOp, wf.pendingOpRev, wf.regIntent, wf.regIntentRev = nil, "", nil, "", nil, ""
	clear(wf.publicProfiles)

	var decodeErr error
	for _, ent := range st.entries {
		if ent == nil || len(ent.Data) == 0 {
			continue
		}
		switch ent.Bucket {
		case bktRec:
			if rec, err := DecodePersistedRecord(ent.Data); err != nil {
				decodeErr = err
			} else {
				wf.record, wf.recordRev = rec, ent.Revision
			}
		case bktOp:
			if op, err := DecodePersistedOperation(ent.Data); err != nil {
				decodeErr = err
			} else {
				wf.pendingOp, wf.pendingOpRev = op, ent.Revision
			}
		case bktIntent:
			if intent, err := DecodePersistedRegistrationIntent(ent.Data); err != nil {
				decodeErr = err
			} else {
				wf.regIntent, wf.regIntentRev = intent, ent.Revision
			}
		case bktProf:
			if prof, err := DecodePersistedPublicProfile(ent.Data); err == nil {
				wf.publicProfiles[PublicProfileStorageKey(prof.Server, prof.Pin)] = prof
			}
		}
	}
	if decodeErr != nil {
		e.securityLocked = true
		return t.finish(e, wf, decodeErr)
	}
	if wf.pendingOp != nil || wf.regIntent != nil || wf.record != nil {
		e.securityLocked = true
	}
	if wf.record != nil {
		if wf.record.Server != "" {
			wf.server = wf.record.Server
		}
		if !wf.deviceEdPresent || !wf.deviceXPresent {
			return t.finish(e, wf, devicesec.ErrInvalid)
		}
		e.applyPersistedRecordToEngine(wf.record, parentTarget(t, 0))
		return nil
	}
	return t.finish(e, wf, nil)
}

type applyPersistedRecordTask struct {
	rec    *PersistedRecord
	target taskTarget
}

func (t *applyPersistedRecordTask) step(e *Engine, ev *secEvent) error {
	err := ev.err
	if errors.Is(err, ErrNotMember) {
		err = nil
	}
	e.syncUnlockedSecretsToRecord(t.rec)
	e.notifyStateChanged()
	e.completeTarget(t.target, &secEvent{nowMs: ev.nowMs, err: err})
	return nil
}

//go:noinline
func (e *Engine) applyPersistedRecordToEngine(rec *PersistedRecord, target taskTarget) {
	if rec == nil {
		e.okTarget(target, nil)
		return
	}
	e.securityLocked, e.configured = true, true
	e.account, e.selfDeviceID = rec.Pin.Account, rec.MemberID
	ev := RecordUpdate{
		Direct: true, Account: rec.Pin.Account, Domain: slices.Clone(rec.Pin.Domain[:]), Genesis: slices.Clone(rec.Pin.Genesis[:]),
		States: rec.States, RecoveryProofs: rec.RecoveryProofs, Lease: slices.Clone(rec.Lease), FloorMs: rec.FloorMs,
		RecoveryPending: e.recoveryPending, RecoveryReadyMs: e.recoveryReadyMs, RecoveryObjectsPending: rec.RecoveryObjectsPending,
		ContentKeys: make([]ContentKey, 0, len(rec.ContentKeys)),
	}
	for _, ck := range rec.ContentKeys {
		ev.ContentKeys = append(ev.ContentKeys, ContentKey{ContentEpoch: ck.Epoch, Secret: slices.Clone(ck.Secret[:])})
	}
	prior := e.out
	e.out = nil
	effs := e.ApplyRecord(e.lastNowMs, ev)
	e.out = append(prior, effs...)
	e.waitUnlocked(e.lastNowMs, false, taskWait{task: &applyPersistedRecordTask{rec: rec, target: target}})
}

//go:noinline
func (e *Engine) startOpenBatchInternal(nowMs int64, items []OpenItem, target taskTarget) {
	e.startOpenBatch(nowMs, &openBatchTask{items: items, target: target})
}

//go:noinline
func (e *Engine) startSealEnvelopeInternal(
	nowMs int64, purpose devicesec.Purpose, machineID, sessionID, requestID string,
	seq, deadlineMs uint64, plain []byte, useSessionSeed bool, overrideSessionSeed []byte, target taskTarget,
) {
	e.startSealEnvelope(nowMs, &sealEnvelopeTask{
		purpose: purpose, machineID: machineID, sessionID: sessionID, requestID: requestID,
		seq: seq, deadlineMs: deadlineMs, plain: plain, useSessionSeed: useSessionSeed,
		overrideSeed: overrideSessionSeed, target: target,
	})
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
	raw, err := (&PersistedNotifyRecord{
		Pin:        rec.Pin,
		HeadState:  rec.States[len(rec.States)-1],
		PurposeKey: pkArr,
	}).Encode()
	if err != nil {
		return nil
	}
	return raw
}

type loginTokenTask struct {
	taskBase
	server, slot, deviceID string
	keyKind                corev1.SecurityKeyKind
	fgCheck, cacheResult   bool
	challenge              []byte
}

func (e *Engine) withAuthToken(server, preferred string, fgCheck bool, capturedGen uint64, target taskTarget) {
	if fgCheck {
		if err := e.checkFG(capturedGen); err != nil {
			e.failTarget(target, err)
			return
		}
	}
	if preferred != "" {
		e.completeTarget(target, &secEvent{str: preferred})
		return
	}
	wf := e.ensureWF()
	if wf.cachedToken != "" && (wf.tokenExpiresMs == 0 || e.lastNowMs == 0 || wf.tokenExpiresMs > e.lastNowMs+dayMs) {
		e.completeTarget(target, &secEvent{str: wf.cachedToken})
		return
	}
	e.forceLoginToken(server, keyDevEd, "", e.selfDeviceID, fgCheck, capturedGen, true, target)
}

//go:noinline
func (e *Engine) forceLoginToken(server string, keyKind corev1.SecurityKeyKind, slot, deviceID string, fgCheck bool, capturedGen uint64, cacheResult bool, target taskTarget) {
	if deviceID == "" {
		e.failTarget(target, devicesec.ErrUntrusted)
		return
	}
	e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_CHALLENGE, server, "", &v1.GetChallengeRequest{DeviceId: deviceID}, &loginTokenTask{
		taskBase: taskBase{fgGen: capturedGen, target: target},
		server:   server, keyKind: keyKind, slot: slot, deviceID: deviceID, fgCheck: fgCheck, cacheResult: cacheResult,
	})
}

const (
	loginStageChallenge uint8 = iota
	loginStageSign
	loginStageLogin
)

func (t *loginTokenTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	var fg uint64
	if t.fgCheck {
		fg = t.fgGen
	}
	switch t.stage {
	case loginStageChallenge:
		var chal v1.GetChallengeResponse
		if chal.Unmarshal(ev.bytes) != nil || len(chal.Challenge) == 0 {
			return devicesec.ErrInvalid
		}
		if fg != 0 {
			if err := e.checkFG(fg); err != nil {
				return err
			}
		}
		t.challenge, t.stage = chal.Challenge, loginStageSign
		e.waitSign(t.keyKind, t.slot, append(append([]byte{}, loginContext...), chal.Challenge...), t, 0)
	case loginStageSign:
		if fg != 0 {
			if err := e.checkFG(fg); err != nil {
				return err
			}
		}
		t.stage = loginStageLogin
		e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LOGIN, t.server, "", &v1.LoginRequest{
			DeviceId: t.deviceID, Challenge: t.challenge, Signature: ev.bytes, ProtocolVersion: e.relayVersion,
		}, t)
	case loginStageLogin:
		var lr v1.LoginResponse
		if lr.Unmarshal(ev.bytes) != nil || lr.Token == "" {
			return devicesec.ErrInvalid
		}
		if t.cacheResult {
			wf := e.ensureWF()
			wf.cachedToken, wf.tokenExpiresMs = lr.Token, lr.ExpiresMs
			e.emitTokenUpdated(lr.Token, lr.ExpiresMs, false)
		}
		e.completeTarget(t.target, &secEvent{str: lr.Token})
	}
	return nil
}

const (
	authedRPCWaitToken uint8 = iota
	authedRPCCall
	authedRPCRefreshToken
	authedRPCRetryCall
)

type authedRPCTask struct {
	taskBase
	method   corev1.SecurityRpcMethod
	server   string
	fgCheck  bool
	reqBytes []byte
}

//go:noinline
func (e *Engine) callAuthedRpc(method corev1.SecurityRpcMethod, server, preferred string, fgCheck bool, capturedGen uint64, reqBytes []byte, target taskTarget) {
	t := &authedRPCTask{
		taskBase: taskBase{fgGen: capturedGen, target: target}, method: method, server: server, fgCheck: fgCheck, reqBytes: reqBytes,
	}
	e.withAuthToken(server, preferred, fgCheck, capturedGen, parentTarget(t, 0))
}

func (t *authedRPCTask) failTokenErr(e *Engine, ev *secEvent) {
	re := ev.rpcErr
	if re == nil {
		re = asRpcError(ev.err)
	}
	if re == nil {
		re = &rpcError{msg: ev.err.Error()}
	}
	e.completeTarget(t.target, &secEvent{rpcErr: re, err: re})
}

func (t *authedRPCTask) step(e *Engine, ev *secEvent) error {
	switch t.stage {
	case authedRPCWaitToken, authedRPCRefreshToken:
		if ev.err != nil {
			t.failTokenErr(e, ev)
			return nil
		}
		if t.stage == authedRPCWaitToken {
			t.stage = authedRPCCall
		} else {
			t.stage = authedRPCRetryCall
		}
		e.waitRPC(t.method, t.server, ev.str, t.reqBytes, t, 0)
	case authedRPCCall:
		if ev.rpcErr != nil && ev.rpcErr.isTokenInvalid() {
			e.ensureWF().cachedToken = ""
			t.stage = authedRPCRefreshToken
			e.forceLoginToken(t.server, keyDevEd, "", e.selfDeviceID, t.fgCheck, t.fgGen, true, parentTarget(t, 0))
			return nil
		}
		e.completeTarget(t.target, &secEvent{bytes: ev.bytes, rpcErr: ev.rpcErr, err: ev.err})
	case authedRPCRetryCall:
		e.completeTarget(t.target, &secEvent{bytes: ev.bytes, rpcErr: ev.rpcErr, err: ev.err})
	}
	return nil
}

//go:noinline
func asWfError(err error) *wfError {
	for err != nil {
		if we, ok := err.(*wfError); ok {
			return we
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return nil
		}
		err = u.Unwrap()
	}
	return nil
}

//go:noinline
func asRpcError(err error) *rpcError {
	for err != nil {
		if re, ok := err.(*rpcError); ok {
			return re
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return nil
		}
		err = u.Unwrap()
	}
	return nil
}

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

type kemBuildKind uint8

const (
	kemBuildGenesis kemBuildKind = iota
	kemBuildMembership
	kemBuildRecoveryRot
	kemBuildHandoff
	kemBuildPair
	kemBuildRecovery
)

type kemBuildParams struct {
	kind       kemBuildKind
	accountID  string
	domain     devicesec.Hash
	member     devicesec.Member
	parent     devicesec.State
	members    []devicesec.Member
	removed    []string
	oldSecret  [32]byte
	newSecret  [32]byte
	recoveryEd [32]byte
	targetID   string
	candX      []byte
	now        uint64
}

type kemStateTask struct {
	taskBase
	keyKind  corev1.SecurityKeyKind
	slot     string
	fgCheck  bool
	params   kemBuildParams
	members  []devicesec.Member
	unsigned devicesec.State
}

func (t *kemStateTask) clearSecrets() {
	zeroBytes(t.params.oldSecret[:])
	zeroBytes(t.params.newSecret[:])
}

func (t *kemStateTask) onError(e *Engine, err error) { t.clearSecrets(); e.failTarget(t.target, err) }

func (t *kemStateTask) buildUnsigned(e *Engine, hpke devicesec.HPKESender) (devicesec.State, error) {
	p := &t.params
	switch p.kind {
	case kemBuildGenesis:
		return devicesec.NewGenesis(p.accountID, p.domain, p.member, p.recoveryEd, p.newSecret[:], p.now, hpke)
	case kemBuildMembership:
		return devicesec.NewMembershipState(p.parent, p.members, p.removed, p.oldSecret[:], p.newSecret[:], p.now, hpke, e.entropy)
	case kemBuildRecoveryRot:
		return devicesec.NewRecoveryKeyRotation(p.parent, p.oldSecret[:], p.recoveryEd, p.now, hpke)
	case kemBuildHandoff:
		return devicesec.NewHandoff(p.parent, p.targetID, p.recoveryEd, p.oldSecret[:], p.newSecret[:], p.now, hpke, e.entropy)
	case kemBuildPair:
		return devicesec.NewPairState(p.parent, p.member, p.candX, p.oldSecret[:], p.newSecret[:], p.now, hpke, e.entropy)
	case kemBuildRecovery:
		return devicesec.NewRecovery(p.parent, p.member, p.members, p.newSecret[:], p.recoveryEd, p.now, hpke)
	default:
		return devicesec.State{}, devicesec.ErrInvalid
	}
}

//go:noinline
func (e *Engine) buildAndSignStateWithKEM(
	keyKind corev1.SecurityKeyKind, slot string, fgCheck bool, capturedGen uint64,
	params kemBuildParams, members []devicesec.Member, target taskTarget,
) {
	uniq := make(map[string]struct{}, len(members))
	recips := make([]*corev1.SecurityEncapsulateRecipient, 0, len(members))
	for _, m := range members {
		if _, exists := uniq[m.ID]; exists {
			continue
		}
		uniq[m.ID] = struct{}{}
		recips = append(recips, &corev1.SecurityEncapsulateRecipient{MemberId: m.ID, XPublic: slices.Clone(m.XPublic[:])})
	}
	e.waitEncap(recips, &kemStateTask{
		taskBase: taskBase{fgGen: capturedGen, target: target}, keyKind: keyKind, slot: slot, fgCheck: fgCheck, params: params, members: members,
	}, 0)
}

const (
	kemStageEncap uint8 = iota
	kemStageSign
)

func (t *kemStateTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	if t.fgCheck {
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
	}
	switch t.stage {
	case kemStageEncap:
		byX := make(map[[32]byte]struct{ enc, dh [32]byte }, len(ev.encap))
		for _, p := range ev.encap {
			if p == nil || len(p.Enc) != 32 || len(p.Dh) != 32 {
				return devicesec.ErrInvalid
			}
			var item struct{ enc, dh [32]byte }
			copy(item.enc[:], p.Enc)
			copy(item.dh[:], p.Dh)
			for _, m := range t.members {
				if m.ID == p.MemberId {
					byX[m.XPublic] = item
				}
			}
		}
		unsigned, err := t.buildUnsigned(e, &encapHPKESender{byX: byX})
		t.clearSecrets()
		if err != nil {
			return err
		}
		sb, err := unsigned.SigningBytes()
		if err != nil {
			return err
		}
		t.unsigned, t.stage = unsigned, kemStageSign
		e.waitSign(t.keyKind, t.slot, sb, t, 0)
	case kemStageSign:
		if len(ev.bytes) != devicesec.Ed25519SignatureSize {
			return devicesec.ErrUntrusted
		}
		t.unsigned.Signature = slices.Clone(ev.bytes)
		if _, err := t.unsigned.MarshalBinary(); err != nil {
			return err
		}
		st := t.unsigned
		e.completeTarget(t.target, &secEvent{state: &st})
	}
	return nil
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

type leaseTask struct {
	taskBase
	head  devicesec.State
	lease devicesec.LeaseCertificate
}

//go:noinline
func (e *Engine) buildAndSignLease(head devicesec.State, keyKind corev1.SecurityKeyKind, slot string, nowMs uint64, target taskTarget) {
	l, err := devicesec.NewLease(head, nowMs)
	if err != nil {
		e.failTarget(target, err)
		return
	}
	sb, err := l.SigningBytes()
	if err != nil {
		e.failTarget(target, err)
		return
	}
	e.waitSign(keyKind, slot, sb, &leaseTask{taskBase: taskBase{target: target}, head: head, lease: l}, 0)
}

func (t *leaseTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	t.lease.Signature = slices.Clone(ev.bytes)
	if err := t.lease.Verify(t.head); err != nil {
		return err
	}
	l := t.lease
	e.completeTarget(t.target, &secEvent{lease: &l})
	return nil
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
