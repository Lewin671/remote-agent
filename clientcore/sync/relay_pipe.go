package sync

import (
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	"github.com/Lewin671/remote-agent/clientcore/gen/litewire"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
)

const (
	// syncRelayOpBase separates sync-owned RelayRpcEffect op_ids from security.Engine op_ids
	// while remaining strictly within JS safe integer (2^53-1) and positive signed int64 ranges.
	syncRelayOpBase uint64 = 1 << 48

	relayRpcDefaultTimeoutMs int64 = 15_000
	relayRpcForwardTimeoutMs int64 = 45_000

	notForwardedTypeName = "remoteagent.v1.NotForwarded"
)

type relayRpcKind uint8

const (
	relayRpcRefreshMachines relayRpcKind = iota + 1
	relayRpcRefreshSessions
	relayRpcRefreshDevices
	relayRpcSessionsPage
	relayRpcSessionByID
	relayRpcDevices
	relayRpcEvents
	relayRpcPrefsGet
	relayRpcPrefsPut
	relayRpcHealthz
	relayRpcForward
	relayRpcPushKey
	relayRpcPushSub
	relayRpcPushTarget
	relayRpcGetImage
)

type pendingRelayRpc struct {
	kind          relayRpcKind
	opID          uint64 // sync-internal logical opID (e.g. refreshOpID, pageOpID, push op_id, image op_id)
	method        corev1.RelayRpcMethod
	timeoutMs     int64
	attemptHeader string
	reqBytes      []byte
	usedToken     string
	retriedAuth   bool
	sid           string
	machineID     string
	imageID       string
	requestID     string
	attemptID     string
	epoch         string
	startedMonoMs int64
	bundle        *pendingRefreshBundle
	push          *pendingPushRpc
}

type pendingAuthRetry struct {
	op     *pendingRelayRpc
	origEv *corev1.RelayRpcResultEvent
}

type pendingRefreshBundle struct {
	refreshOpID  uint64
	refreshRun   uint64
	machinesOpID uint64
	sessionsOpID uint64
	devicesOpID  uint64
	gotMachines  bool
	gotSessions  bool
	gotDevices   bool
	machines     []*v1.Machine
	sessions     []*v1.RemoteSession
	next         *v1.SessionCursor
	devices      []*v1.Device
}

type pendingForwardLeg struct {
	requestID string
	attemptID string
	machineID string
	envelope  *v1.Envelope
}

type pendingPushRpc struct {
	opID             uint64
	getKey           bool
	setWebSub        bool
	setTarget        bool
	subscriptionJSON string
	pushType         v1.PushType
	target           string
	kinds            []v1.NotifyKind
}

func (e *Engine) allocRelayOpID() uint64 {
	e.nextRelayOpSeq++
	return syncRelayOpBase + e.nextRelayOpSeq
}

func (e *Engine) relayServer() string {
	if e.secEngine != nil {
		if s := e.secEngine.Server(); s != "" {
			return s
		}
	}
	return ""
}

func (e *Engine) emitRelayRpc(
	ctx *StepContext,
	method corev1.RelayRpcMethod,
	token string,
	timeoutMs int64,
	attemptHeader string,
	reqBytes []byte,
	meta *pendingRelayRpc,
) uint64 {
	opID := e.allocRelayOpID()
	meta.method = method
	meta.timeoutMs = timeoutMs
	meta.attemptHeader = attemptHeader
	meta.reqBytes = reqBytes
	meta.usedToken = token
	if e.relayRpcOps == nil {
		e.relayRpcOps = make(map[uint64]*pendingRelayRpc)
	}
	e.relayRpcOps[opID] = meta
	if timeoutMs <= 0 {
		timeoutMs = relayRpcDefaultTimeoutMs
	}
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_RelayRpc{
			RelayRpc: &corev1.RelayRpcEffect{
				OpId:          opID,
				Method:        method,
				Server:        e.relayServer(),
				AuthToken:     token,
				TimeoutMs:     timeoutMs,
				AttemptHeader: attemptHeader,
				RequestBytes:  reqBytes,
			},
		},
	})
	return opID
}

func (e *Engine) cancelRelayRpc(ctx *StepContext, opID uint64) {
	if opID == 0 {
		return
	}
	if _, ok := e.relayRpcOps[opID]; !ok {
		return
	}
	delete(e.relayRpcOps, opID)
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_RelayRpcCancel{
			RelayRpcCancel: &corev1.RelayRpcCancelEffect{OpId: opID},
		},
	})
}

func (e *Engine) cancelAllSyncRelayRpcs(ctx *StepContext, forLogout bool) {
	if e.refreshBundle != nil {
		e.cancelRefreshBundle(ctx, e.refreshBundle)
		e.refreshBundle = nil
	}
	keepHealthz := !forLogout && len(e.healthzWaiters) > 0
	var canceled []*pendingRelayRpc
	opIDs := make([]uint64, 0, len(e.relayRpcOps))
	for opID := range e.relayRpcOps {
		opIDs = append(opIDs, opID)
	}
	// Map order is random; cancel in op order so the emitted effects are deterministic.
	sortutil.Uint64s(opIDs)
	for _, opID := range opIDs {
		op := e.relayRpcOps[opID]
		if op == nil {
			delete(e.relayRpcOps, opID)
			continue
		}
		// On normal STOP, never cancel in-flight Forward or prefs PUT: let them finish naturally and
		// deliver their results to Core (matching main). Keep healthz alive if a Forward waits on it.
		// On logout, cancel everything unconditionally so no outgoing-account RPC outlives reset.
		if !forLogout && (op.kind == relayRpcForward || op.kind == relayRpcPrefsPut || (op.kind == relayRpcHealthz && keepHealthz)) {
			continue
		}
		delete(e.relayRpcOps, opID)
		canceled = append(canceled, op)
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_RelayRpcCancel{
				RelayRpcCancel: &corev1.RelayRpcCancelEffect{OpId: opID},
			},
		})
	}
	if forLogout {
		clear(e.forwardRelayOpByAttempt)
	}
	canceledEv := &corev1.RelayRpcResultEvent{
		ConnectCode:       security.ConnectCodeCanceled,
		DiagnosticMessage: "canceled",
	}
	for _, op := range canceled {
		switch op.kind {
		case relayRpcGetImage:
			e.emitSessionImageLoaded(ctx, &corev1.SessionImageLoadedEffect{
				OpId:      op.opID,
				SessionId: op.sid,
				ImageId:   op.imageID,
				Status:    corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_ERROR,
				ErrorCode: "canceled",
			})
		case relayRpcPushKey, relayRpcPushSub, relayRpcPushTarget:
			if op.push != nil {
				e.emitPushRpcResult(ctx, &corev1.PushRpcResultEffect{
					OpId:      op.push.opID,
					Ok:        false,
					Retryable: true,
					Error:     "canceled",
				})
			}
		case relayRpcPrefsGet:
			if len(e.prefsQueue) > 0 && e.prefsQueue[0].Phase == prefsPhaseGet && op.opID == e.prefsQueue[0].ActiveOpID {
				e.prefsQueue[0].ActiveOpID = 0
				e.prefsQueue[0].Phase = prefsPhaseIdle
			}
		default:
			e.dispatchRelayRpcResult(ctx, op, canceledEv)
		}
	}
	if !keepHealthz {
		e.healthzInFlight = false
		e.healthzOpID = 0
		e.healthzWaiters = nil
	}
	queue := e.authRetryQueue
	e.authRetryInFlight = false
	e.authRetryQueue = nil
	for _, item := range queue {
		if item == nil || item.op == nil {
			continue
		}
		ev := item.origEv
		if forLogout || ev == nil {
			ev = canceledEv
		}
		e.dispatchRelayRpcResult(ctx, item.op, ev)
	}
}

func (e *Engine) RelayPipeEnabled() bool { return e.relayPipe }

func (e *Engine) cancelRefreshBundle(ctx *StepContext, b *pendingRefreshBundle) {
	if b == nil {
		return
	}
	if !b.gotMachines {
		e.cancelRelayRpc(ctx, b.machinesOpID)
	}
	if !b.gotSessions {
		e.cancelRelayRpc(ctx, b.sessionsOpID)
	}
	if !b.gotDevices {
		e.cancelRelayRpc(ctx, b.devicesOpID)
	}
}

func (e *Engine) handleRelayPipeConfig(ev *corev1.RelayPipeConfigEvent) {
	if ev == nil {
		return
	}
	e.relayPipe = ev.Enabled
	if e.secEngine != nil {
		e.secEngine.SetRelayPipe(ev.Enabled)
	}
}

func (e *Engine) startRefreshViaRelayPipe(ctx *StepContext) {
	if e.refreshBundle != nil {
		e.cancelRefreshBundle(ctx, e.refreshBundle)
	}
	b := &pendingRefreshBundle{
		refreshOpID: e.refreshOpID,
		refreshRun:  e.refreshRun,
	}
	e.refreshBundle = b
	tok := e.cachedToken
	b.machinesOpID = e.emitRelayRpc(
		ctx,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_MACHINES,
		tok,
		relayRpcDefaultTimeoutMs,
		"",
		(&v1.ListMachinesRequest{}).AppendMarshal(nil),
		&pendingRelayRpc{kind: relayRpcRefreshMachines, opID: b.refreshOpID, bundle: b},
	)
	b.sessionsOpID = e.emitRelayRpc(
		ctx,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS,
		tok,
		relayRpcDefaultTimeoutMs,
		"",
		(&v1.ListRemoteSessionsRequest{Limit: SessionPageLimit}).AppendMarshal(nil),
		&pendingRelayRpc{kind: relayRpcRefreshSessions, opID: b.refreshOpID, bundle: b},
	)
	b.devicesOpID = e.emitRelayRpc(
		ctx,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_DEVICES,
		tok,
		relayRpcDefaultTimeoutMs,
		"",
		(&v1.ListDevicesRequest{}).AppendMarshal(nil),
		&pendingRelayRpc{kind: relayRpcRefreshDevices, opID: b.refreshOpID, bundle: b},
	)
}

func (e *Engine) emitCommandRelayLegViaPipe(ctx *StepContext, prep *PreparedCommand, attemptID string) {
	if prep.SealedEnvelope == nil {
		e.handleCommandOutcome(ctx, &corev1.CommandOutcomeEvent{
			RequestId:       prep.RequestID,
			AttemptId:       attemptID,
			Source:          corev1.OutcomeSource_OUTCOME_SOURCE_LOCAL,
			NotForwarded:    true,
			TransportCode:   errCodeFailedPrecond,
			TransportDetail: "missing sealed command",
		})
		return
	}
	leg := &pendingForwardLeg{
		requestID: prep.RequestID,
		attemptID: attemptID,
		machineID: prep.MachineID,
		envelope:  prep.SealedEnvelope,
	}
	if e.cachedToken == "" && e.secEngine != nil {
		tag := e.allocSecOp(&pendingSecOp{kind: secOpForwardAuth, fwdLeg: leg})
		effs := e.secEngine.EnsureAuthTokenTag(e.nowMs, false, relayRpcDefaultTimeoutMs, tag)
		e.emitSecurityEffects(ctx, effs)
		e.maybeRequestEntropy(ctx)
		return
	}
	e.proceedForwardLeg(ctx, leg)
}

func (e *Engine) proceedForwardLeg(ctx *StepContext, leg *pendingForwardLeg) {
	if e.relayEpoch != "" {
		e.sendForwardLegWithEpoch(ctx, leg, e.relayEpoch)
		return
	}
	e.healthzWaiters = append(e.healthzWaiters, leg)
	if !e.healthzInFlight {
		e.healthzInFlight = true
		e.healthzOpID = e.emitRelayRpc(
			ctx,
			corev1.RelayRpcMethod_RELAY_RPC_METHOD_HEALTHZ,
			"",
			relayRpcDefaultTimeoutMs,
			"",
			nil,
			&pendingRelayRpc{kind: relayRpcHealthz},
		)
	}
}

func (e *Engine) finishForwardAuth(ctx *StepContext, leg *pendingForwardLeg, res security.AuthTokenResult) {
	if leg == nil || e.inFlightCmds[leg.requestID] == nil {
		return
	}
	if e.exitingAccount || res.Error != "" || res.Token == "" {
		code := res.ErrorCode
		if code == "" {
			code = errCodeUnauthenticated
		}
		e.handleCommandOutcome(ctx, &corev1.CommandOutcomeEvent{
			RequestId:       leg.requestID,
			AttemptId:       leg.attemptID,
			Source:          corev1.OutcomeSource_OUTCOME_SOURCE_LOCAL,
			NotForwarded:    true,
			HandedOff:       false,
			TransportCode:   code,
			TransportDetail: res.Error,
		})
		return
	}
	e.cachedToken = res.Token
	e.tokenExpiresMs = res.ExpiresMs
	e.proceedForwardLeg(ctx, leg)
}

func (e *Engine) sendForwardLegWithEpoch(ctx *StepContext, leg *pendingForwardLeg, epoch string) {
	req := &v1.ForwardRequest{
		MachineId:  leg.machineID,
		Command:    leg.envelope,
		AttemptId:  leg.attemptID,
		RelayEpoch: epoch,
	}
	opID := e.emitRelayRpc(
		ctx,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_FORWARD,
		e.cachedToken,
		relayRpcForwardTimeoutMs,
		leg.attemptID,
		req.AppendMarshal(nil),
		&pendingRelayRpc{
			kind:          relayRpcForward,
			requestID:     leg.requestID,
			attemptID:     leg.attemptID,
			epoch:         epoch,
			startedMonoMs: e.monoMs,
		},
	)
	if e.forwardRelayOpByAttempt == nil {
		e.forwardRelayOpByAttempt = make(map[string]uint64)
	}
	e.forwardRelayOpByAttempt[leg.attemptID] = opID
}

func (e *Engine) cancelRelayForwardAttempt(ctx *StepContext, attemptID string) {
	if attemptID == "" {
		return
	}
	if opID, ok := e.forwardRelayOpByAttempt[attemptID]; ok {
		delete(e.forwardRelayOpByAttempt, attemptID)
		e.cancelRelayRpc(ctx, opID)
	}
	if len(e.healthzWaiters) > 0 {
		out := e.healthzWaiters[:0]
		for _, w := range e.healthzWaiters {
			if w != nil && w.attemptID != attemptID {
				out = append(out, w)
			}
		}
		e.healthzWaiters = out
	}
}

func (e *Engine) handlePushRpcAction(ctx *StepContext, act *corev1.PushRpcAction) {
	if act == nil || act.OpId == 0 {
		return
	}
	if e.removed {
		e.emitPushRpcResult(ctx, &corev1.PushRpcResultEffect{
			OpId:      act.OpId,
			Ok:        false,
			Retryable: false,
			Error:     "removed",
		})
		return
	}
	p := &pendingPushRpc{
		opID:             act.OpId,
		getKey:           act.GetKey,
		setWebSub:        act.SetWebSubscription,
		setTarget:        act.SetTarget,
		subscriptionJSON: act.SubscriptionJson,
		pushType:         v1.PushType(act.PushType),
		target:           act.Target,
		kinds:            append([]v1.NotifyKind(nil), act.Kinds...),
	}
	if !p.getKey && !p.setWebSub && !p.setTarget {
		e.emitPushRpcResult(ctx, &corev1.PushRpcResultEffect{
			OpId:  act.OpId,
			Ok:    false,
			Error: "empty push rpc action",
		})
		return
	}
	if e.secEngine != nil {
		tag := e.allocSecOp(&pendingSecOp{kind: secOpPushAuth, push: p})
		effs := e.secEngine.EnsureAuthTokenTag(e.nowMs, false, relayRpcDefaultTimeoutMs, tag)
		e.emitSecurityEffects(ctx, effs)
		e.maybeRequestEntropy(ctx)
		return
	}
	e.startPushRpcWithToken(ctx, p, e.cachedToken)
}

func (e *Engine) finishPushAuth(ctx *StepContext, p *pendingPushRpc, res security.AuthTokenResult) {
	if p == nil {
		return
	}
	if e.exitingAccount || res.Error != "" || res.Token == "" || res.Removed {
		errText := res.Error
		if errText == "" {
			errText = errCodeUnauthenticated
		}
		e.emitPushRpcResult(ctx, &corev1.PushRpcResultEffect{
			OpId:      p.opID,
			Ok:        false,
			Retryable: !res.Removed,
			Error:     errText,
		})
		return
	}
	e.cachedToken = res.Token
	e.tokenExpiresMs = res.ExpiresMs
	e.startPushRpcWithToken(ctx, p, res.Token)
}

func (e *Engine) startPushRpcWithToken(ctx *StepContext, p *pendingPushRpc, token string) {
	if p.getKey {
		e.emitRelayRpc(
			ctx,
			corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_PUSH_KEY,
			token,
			relayRpcDefaultTimeoutMs,
			"",
			(&v1.GetPushKeyRequest{}).AppendMarshal(nil),
			&pendingRelayRpc{kind: relayRpcPushKey, opID: p.opID, push: p},
		)
		return
	}
	if p.setWebSub {
		if p.subscriptionJSON == "" {
			e.emitRelayRpc(
				ctx,
				corev1.RelayRpcMethod_RELAY_RPC_METHOD_SET_PUSH_SUBSCRIPTION,
				token,
				relayRpcDefaultTimeoutMs,
				"",
				(&v1.SetPushSubscriptionRequest{}).AppendMarshal(nil),
				&pendingRelayRpc{kind: relayRpcPushSub, opID: p.opID, push: p},
			)
			return
		}
		e.emitRelayRpc(
			ctx,
			corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_PUSH_KEY,
			token,
			relayRpcDefaultTimeoutMs,
			"",
			(&v1.GetPushKeyRequest{}).AppendMarshal(nil),
			&pendingRelayRpc{kind: relayRpcPushKey, opID: p.opID, push: p},
		)
		return
	}
	if p.setTarget {
		if p.target == "" {
			e.emitRelayRpc(
				ctx,
				corev1.RelayRpcMethod_RELAY_RPC_METHOD_SET_PUSH_TARGET,
				token,
				relayRpcDefaultTimeoutMs,
				"",
				(&v1.SetPushTargetRequest{Type: p.pushType}).AppendMarshal(nil),
				&pendingRelayRpc{kind: relayRpcPushTarget, opID: p.opID, push: p},
			)
			return
		}
		e.emitRelayRpc(
			ctx,
			corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_PUSH_KEY,
			token,
			relayRpcDefaultTimeoutMs,
			"",
			(&v1.GetPushKeyRequest{}).AppendMarshal(nil),
			&pendingRelayRpc{kind: relayRpcPushKey, opID: p.opID, push: p},
		)
	}
}

func (e *Engine) emitPushRpcResult(ctx *StepContext, res *corev1.PushRpcResultEffect) {
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_PushRpcResult{
			PushRpcResult: res,
		},
	})
}

func (e *Engine) emitSessionImageLoaded(ctx *StepContext, res *corev1.SessionImageLoadedEffect) {
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_SessionImageLoaded{
			SessionImageLoaded: res,
		},
	})
}

func (e *Engine) handleSessionFetchImage(ctx *StepContext, act *corev1.SessionFetchImageAction) {
	if act == nil || act.OpId == 0 {
		return
	}
	machineID := ""
	if sess := e.sessions[act.SessionId]; sess != nil && sess.MachineID != "" {
		machineID = sess.MachineID
	} else {
		machineID = act.MachineId
	}
	if act.SessionId == "" || act.ImageId == "" || machineID == "" || e.secEngine == nil {
		e.emitSessionImageLoaded(ctx, &corev1.SessionImageLoadedEffect{
			OpId:      act.OpId,
			SessionId: act.SessionId,
			ImageId:   act.ImageId,
			Status:    corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_ERROR,
			ErrorCode: "not_found",
		})
		return
	}
	if _, ok := e.secEngine.SessionSeed(act.SessionId); !ok {
		e.emitSessionImageLoaded(ctx, &corev1.SessionImageLoadedEffect{
			OpId:      act.OpId,
			SessionId: act.SessionId,
			ImageId:   act.ImageId,
			Status:    corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_ERROR,
			ErrorCode: "not_found",
		})
		return
	}
	if len(act.CachedEnvelope) > 0 && (!e.security.Loaded || e.CanUseMachineCache(machineID)) {
		var env v1.Envelope
		if err := env.Unmarshal(act.CachedEnvelope); err == nil && !env.HasUnknown() {
			e.openSessionImageEnvelope(ctx, act.OpId, act.SessionId, act.ImageId, machineID, &env, nil, true)
			return
		}
	}
	reqBytes := (&v1.GetImageRequest{
		SessionId: act.SessionId,
		ImageId:   act.ImageId,
	}).AppendMarshal(nil)
	e.emitRelayRpc(
		ctx,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_IMAGE,
		e.cachedToken,
		relayRpcDefaultTimeoutMs,
		"",
		reqBytes,
		&pendingRelayRpc{
			kind:      relayRpcGetImage,
			opID:      act.OpId,
			sid:       act.SessionId,
			machineID: machineID,
			imageID:   act.ImageId,
		},
	)
}

func (e *Engine) handleGetImageRelayResult(ctx *StepContext, op *pendingRelayRpc, ev *corev1.RelayRpcResultEvent) {
	if ev.ConnectCode == security.ConnectCodeNotFound {
		e.emitSessionImageLoaded(ctx, &corev1.SessionImageLoadedEffect{
			OpId:      op.opID,
			SessionId: op.sid,
			ImageId:   op.imageID,
			Status:    corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_MISSING,
		})
		return
	}
	if isRelayRpcError(ev) {
		var errText *corev1.Text
		if ev.DiagnosticMessage != "" {
			errText = &corev1.Text{Literal: ev.DiagnosticMessage}
		}
		e.emitSessionImageLoaded(ctx, &corev1.SessionImageLoadedEffect{
			OpId:      op.opID,
			SessionId: op.sid,
			ImageId:   op.imageID,
			Status:    corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_ERROR,
			ErrorCode: relayRpcTransportCode(ev),
			ErrorText: errText,
		})
		return
	}
	var resp v1.GetImageResponse
	if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
		e.emitSessionImageLoaded(ctx, &corev1.SessionImageLoadedEffect{
			OpId:      op.opID,
			SessionId: op.sid,
			ImageId:   op.imageID,
			Status:    corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_ERROR,
			ErrorCode: errCodeUnknown,
			ErrorText: &corev1.Text{Literal: err.Error()},
		})
		return
	}
	if resp.Expired || resp.Image == nil {
		e.emitSessionImageLoaded(ctx, &corev1.SessionImageLoadedEffect{
			OpId:      op.opID,
			SessionId: op.sid,
			ImageId:   op.imageID,
			Status:    corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_EXPIRED,
		})
		return
	}
	rawEnv := resp.Image.AppendMarshal(nil)
	e.openSessionImageEnvelope(ctx, op.opID, op.sid, op.imageID, op.machineID, resp.Image, rawEnv, false)
}

func machineNotificationKinds(kinds []v1.NotifyKind, advertised bool) []v1.NotifyKind {
	out := make([]v1.NotifyKind, 0, len(kinds)+1)
	for _, k := range kinds {
		if k != v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK {
			out = append(out, k)
		}
	}
	if advertised {
		out = append(out, v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK)
	}
	return out
}

func (e *Engine) startConnectAuthToken(ctx *StepContext, tokenOpID uint64, forceLogin bool, timeoutMs int64) {
	tag := e.allocSecOp(&pendingSecOp{kind: secOpAuthToken, opID: tokenOpID})
	effs := e.secEngine.EnsureAuthTokenTag(e.nowMs, forceLogin, timeoutMs, tag)
	e.emitSecurityEffects(ctx, effs)
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) clearAuthTokenViaPipe(ctx *StepContext) {
	e.cachedToken = ""
	e.tokenExpiresMs = 0
	if e.secEngine != nil {
		effs := e.secEngine.ClearAuthToken()
		e.emitSecurityEffects(ctx, effs)
	}
}

func (e *Engine) handleRelayRpcResult(ctx *StepContext, ev *corev1.RelayRpcResultEvent) {
	if ev == nil {
		return
	}
	if ev.RelayEpoch != "" {
		e.relayEpoch = ev.RelayEpoch
	}
	if ev.OpId < syncRelayOpBase {
		if e.secEngine != nil {
			effs := e.secEngine.HandleRelayRpcResult(e.nowMs, ev)
			e.emitSecurityEffects(ctx, effs)
			e.maybeRequestEntropy(ctx)
		}
		return
	}
	op, ok := e.relayRpcOps[ev.OpId]
	if !ok {
		return
	}
	delete(e.relayRpcOps, ev.OpId)

	// Automatically refresh token once and retry non-Forward unary RPCs on 401 UNAUTHENTICATED.
	// Forward MUST NOT blindly retry without a verified NotForwarded proof (handled in handleForwardRelayResult).
	isPushOp := op.kind == relayRpcPushKey || op.kind == relayRpcPushSub || op.kind == relayRpcPushTarget
	if ev.ConnectCode == security.ConnectCodeUnauthenticated &&
		!op.retriedAuth &&
		op.kind != relayRpcForward &&
		op.kind != relayRpcHealthz &&
		e.secEngine != nil &&
		(e.running || isPushOp || op.kind == relayRpcPrefsPut) &&
		!e.removed &&
		!e.exitingAccount {
		// If another call already refreshed the token since this RPC was sent, retry immediately
		// with the new cached token instead of invalidating it and triggering another login.
		if e.cachedToken != "" && e.cachedToken != op.usedToken {
			e.reemitRelayRpcWithToken(ctx, op, e.cachedToken)
			return
		}
		if op.usedToken != "" && op.usedToken == e.cachedToken {
			e.invalidateCachedToken(ctx, op.usedToken)
		}
		e.authRetryQueue = append(e.authRetryQueue, &pendingAuthRetry{op: op, origEv: ev})
		if !e.authRetryInFlight {
			e.authRetryInFlight = true
			tag := e.allocSecOp(&pendingSecOp{kind: secOpAuthRetry})
			effs := e.secEngine.EnsureAuthTokenTag(e.nowMs, true, relayRpcDefaultTimeoutMs, tag)
			e.emitSecurityEffects(ctx, effs)
			e.maybeRequestEntropy(ctx)
		}
		return
	}
	if ev.ConnectCode == security.ConnectCodeUnauthenticated && op.usedToken != "" && op.usedToken == e.cachedToken {
		e.invalidateCachedToken(ctx, op.usedToken)
	}

	e.dispatchRelayRpcResult(ctx, op, ev)
}

func (e *Engine) reemitRelayRpcWithToken(ctx *StepContext, op *pendingRelayRpc, token string) {
	if op.bundle != nil && e.refreshBundle != op.bundle {
		return
	}
	op.retriedAuth = true
	newOpID := e.emitRelayRpc(ctx, op.method, token, op.timeoutMs, op.attemptHeader, op.reqBytes, op)
	if op.bundle != nil {
		switch op.kind {
		case relayRpcRefreshMachines:
			op.bundle.machinesOpID = newOpID
		case relayRpcRefreshSessions:
			op.bundle.sessionsOpID = newOpID
		case relayRpcRefreshDevices:
			op.bundle.devicesOpID = newOpID
		}
	}
}

func (e *Engine) dispatchRelayRpcResult(ctx *StepContext, op *pendingRelayRpc, ev *corev1.RelayRpcResultEvent) {
	switch op.kind {
	case relayRpcRefreshMachines, relayRpcRefreshSessions, relayRpcRefreshDevices:
		e.handleRefreshBundlePart(ctx, op, ev)
	case relayRpcSessionsPage:
		e.handleSessionsPageRelayResult(ctx, op, ev)
	case relayRpcSessionByID:
		e.handleSessionByIDRelayResult(ctx, op, ev)
	case relayRpcDevices:
		e.handleDevicesRelayResult(ctx, op, ev)
	case relayRpcEvents:
		e.handleEventsRelayResult(ctx, op, ev)
	case relayRpcPrefsGet:
		e.handlePrefsGetRelayResult(ctx, op, ev)
	case relayRpcPrefsPut:
		e.handlePrefsPutRelayResult(ctx, op, ev)
	case relayRpcHealthz:
		e.handleHealthzRelayResult(ctx, ev)
	case relayRpcForward:
		e.handleForwardRelayResult(ctx, op, ev)
	case relayRpcPushKey, relayRpcPushSub, relayRpcPushTarget:
		e.handlePushRelayResult(ctx, op, ev)
	case relayRpcGetImage:
		e.handleGetImageRelayResult(ctx, op, ev)
	}
}

func (e *Engine) finishAuthRetry(ctx *StepContext, res security.AuthTokenResult) {
	e.authRetryInFlight = false
	queue := e.authRetryQueue
	e.authRetryQueue = nil
	if len(queue) == 0 {
		return
	}
	if e.exitingAccount || res.Error != "" || res.Token == "" || res.Removed {
		if !e.exitingAccount && res.Removed {
			e.activeTokenOpID = e.allocOpID()
			e.handleTokenResult(ctx, &corev1.TokenResultEvent{
				OpId:      e.activeTokenOpID,
				Removed:   true,
				ErrorCode: res.ErrorCode,
				Error:     res.Error,
			})
		}
		for _, item := range queue {
			e.dispatchRelayRpcResult(ctx, item.op, item.origEv)
		}
		return
	}
	e.cachedToken = res.Token
	e.tokenExpiresMs = res.ExpiresMs
	for _, item := range queue {
		op := item.op
		isPush := op.kind == relayRpcPushKey || op.kind == relayRpcPushSub || op.kind == relayRpcPushTarget
		if !e.running && !isPush && op.kind != relayRpcPrefsPut {
			e.dispatchRelayRpcResult(ctx, op, item.origEv)
			continue
		}
		e.reemitRelayRpcWithToken(ctx, op, res.Token)
	}
}

func (e *Engine) invalidateCachedToken(ctx *StepContext, staleToken string) {
	if staleToken == "" || e.cachedToken != staleToken {
		return
	}
	if e.relayPipe && e.secEngine != nil {
		e.clearAuthTokenViaPipe(ctx)
		return
	}
	e.cachedToken = ""
	e.tokenExpiresMs = 0
	if e.secEngine != nil {
		e.secEngine.SetAuthToken("", 0)
	}
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_DiscardToken{
			DiscardToken: &corev1.DiscardTokenEffect{Token: staleToken},
		},
	})
}

func (e *Engine) handleRefreshBundlePart(ctx *StepContext, op *pendingRelayRpc, ev *corev1.RelayRpcResultEvent) {
	b := op.bundle
	if b == nil || e.refreshBundle != b {
		return
	}
	if isRelayRpcError(ev) {
		e.cancelRefreshBundle(ctx, b)
		e.refreshBundle = nil
		e.handleListsFetched(ctx, &corev1.ListsFetchedEvent{
			OpId:           b.refreshOpID,
			Error:          relayRpcErrorText(ev),
			UnresolvedHost: ev.UnresolvedHost,
		})
		return
	}
	switch op.kind {
	case relayRpcRefreshMachines:
		var resp v1.ListMachinesResponse
		if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
			e.cancelRefreshBundle(ctx, b)
			e.refreshBundle = nil
			e.handleListsFetched(ctx, &corev1.ListsFetchedEvent{OpId: b.refreshOpID, Error: err.Error()})
			return
		}
		b.machines = resp.Machines
		b.gotMachines = true
	case relayRpcRefreshSessions:
		var resp v1.ListRemoteSessionsResponse
		if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
			e.cancelRefreshBundle(ctx, b)
			e.refreshBundle = nil
			e.handleListsFetched(ctx, &corev1.ListsFetchedEvent{OpId: b.refreshOpID, Error: err.Error()})
			return
		}
		b.sessions = resp.Sessions
		b.next = resp.Next
		b.gotSessions = true
	case relayRpcRefreshDevices:
		var resp v1.ListDevicesResponse
		if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
			e.cancelRefreshBundle(ctx, b)
			e.refreshBundle = nil
			e.handleListsFetched(ctx, &corev1.ListsFetchedEvent{OpId: b.refreshOpID, Error: err.Error()})
			return
		}
		b.devices = resp.Devices
		b.gotDevices = true
	}
	if b.gotMachines && b.gotSessions && b.gotDevices {
		e.refreshBundle = nil
		e.handleListsFetched(ctx, &corev1.ListsFetchedEvent{
			OpId:         b.refreshOpID,
			WireMachines: b.machines,
			WireSessions: b.sessions,
			Next:         b.next,
			WireDevices:  b.devices,
		})
	}
}

func (e *Engine) handleSessionsPageRelayResult(ctx *StepContext, op *pendingRelayRpc, ev *corev1.RelayRpcResultEvent) {
	if isRelayRpcError(ev) {
		e.handleSessionsPageFetched(ctx, &corev1.SessionsPageFetchedEvent{
			OpId:  op.opID,
			Error: relayRpcErrorText(ev),
		})
		return
	}
	var resp v1.ListRemoteSessionsResponse
	if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
		e.handleSessionsPageFetched(ctx, &corev1.SessionsPageFetchedEvent{OpId: op.opID, Error: err.Error()})
		return
	}
	e.handleSessionsPageFetched(ctx, &corev1.SessionsPageFetchedEvent{
		OpId:         op.opID,
		WireSessions: resp.Sessions,
		Next:         resp.Next,
	})
}

func (e *Engine) handleSessionByIDRelayResult(ctx *StepContext, op *pendingRelayRpc, ev *corev1.RelayRpcResultEvent) {
	if isRelayRpcError(ev) {
		e.handleSessionByIDFetched(ctx, &corev1.SessionByIdFetchedEvent{
			OpId:  op.opID,
			Error: relayRpcErrorText(ev),
		})
		return
	}
	var resp v1.ListRemoteSessionsResponse
	if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
		e.handleSessionByIDFetched(ctx, &corev1.SessionByIdFetchedEvent{OpId: op.opID, Error: err.Error()})
		return
	}
	var found *v1.RemoteSession
	for _, s := range resp.Sessions {
		if s != nil && s.SessionId == op.sid {
			found = s
			break
		}
	}
	e.handleSessionByIDFetched(ctx, &corev1.SessionByIdFetchedEvent{
		OpId:        op.opID,
		WireSession: found,
	})
}

func (e *Engine) handleDevicesRelayResult(ctx *StepContext, op *pendingRelayRpc, ev *corev1.RelayRpcResultEvent) {
	if isRelayRpcError(ev) {
		e.handleDevicesFetched(ctx, &corev1.DevicesFetchedEvent{
			OpId:  op.opID,
			Error: relayRpcErrorText(ev),
		})
		return
	}
	var resp v1.ListDevicesResponse
	if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
		e.handleDevicesFetched(ctx, &corev1.DevicesFetchedEvent{OpId: op.opID, Error: err.Error()})
		return
	}
	e.handleDevicesFetched(ctx, &corev1.DevicesFetchedEvent{
		OpId:        op.opID,
		WireDevices: resp.Devices,
	})
}

func (e *Engine) handleEventsRelayResult(ctx *StepContext, op *pendingRelayRpc, ev *corev1.RelayRpcResultEvent) {
	if isRelayRpcError(ev) {
		e.handleEventsPageFetched(ctx, &corev1.EventsPageFetchedEvent{
			OpId:      op.opID,
			SessionId: op.sid,
			Error:     relayRpcErrorText(ev),
		})
		return
	}
	var resp v1.GetEventsResponse
	if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
		e.handleEventsPageFetched(ctx, &corev1.EventsPageFetchedEvent{
			OpId:      op.opID,
			SessionId: op.sid,
			Error:     err.Error(),
		})
		return
	}
	e.handleEventsPageFetched(ctx, &corev1.EventsPageFetchedEvent{
		OpId:       op.opID,
		SessionId:  op.sid,
		WireEvents: resp.Events,
		More:       resp.More,
	})
}

func (e *Engine) handlePrefsGetRelayResult(ctx *StepContext, op *pendingRelayRpc, ev *corev1.RelayRpcResultEvent) {
	if isRelayRpcError(ev) {
		e.handleAccountPrefsFetched(ctx, &corev1.AccountPrefsFetchedEvent{
			OpId:  op.opID,
			Error: relayRpcErrorText(ev),
		})
		return
	}
	var resp v1.GetAccountPrefsResponse
	if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
		e.handleAccountPrefsFetched(ctx, &corev1.AccountPrefsFetchedEvent{OpId: op.opID, Error: err.Error()})
		return
	}
	e.handleAccountPrefsFetched(ctx, &corev1.AccountPrefsFetchedEvent{
		OpId:      op.opID,
		WirePrefs: resp.Object,
	})
}

func (e *Engine) handlePrefsPutRelayResult(ctx *StepContext, op *pendingRelayRpc, ev *corev1.RelayRpcResultEvent) {
	if isRelayRpcError(ev) {
		e.handleAccountPrefsPutResult(ctx, &corev1.AccountPrefsPutResultEvent{
			OpId:  op.opID,
			Error: relayRpcErrorText(ev),
		})
		return
	}
	var resp v1.PutAccountPrefsResponse
	if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
		e.handleAccountPrefsPutResult(ctx, &corev1.AccountPrefsPutResultEvent{OpId: op.opID, Error: err.Error()})
		return
	}
	e.handleAccountPrefsPutResult(ctx, &corev1.AccountPrefsPutResultEvent{
		OpId:        op.opID,
		Applied:     resp.Applied,
		WireCurrent: resp.Object,
	})
}

func (e *Engine) handleHealthzRelayResult(ctx *StepContext, ev *corev1.RelayRpcResultEvent) {
	e.healthzInFlight = false
	e.healthzOpID = 0
	waiters := e.healthzWaiters
	e.healthzWaiters = nil
	if len(waiters) == 0 {
		return
	}
	if !isRelayRpcError(ev) && e.relayEpoch != "" {
		for _, leg := range waiters {
			if leg != nil && e.inFlightCmds[leg.requestID] != nil {
				e.sendForwardLegWithEpoch(ctx, leg, e.relayEpoch)
			}
		}
		return
	}
	code := relayRpcTransportCode(ev)
	detail := ev.DiagnosticMessage
	if detail == "" && e.relayEpoch == "" && !isRelayRpcError(ev) {
		code = errCodeUnavailable
		detail = "missing relay epoch"
	}
	source := corev1.OutcomeSource_OUTCOME_SOURCE_TRANSPORT
	if !ev.NetworkError && !ev.UnresolvedHost && ev.ConnectCode != security.ConnectCodeUnavailable && ev.ConnectCode != security.ConnectCodeDeadlineExceeded {
		source = corev1.OutcomeSource_OUTCOME_SOURCE_LOCAL
	}
	for _, leg := range waiters {
		if leg == nil || e.inFlightCmds[leg.requestID] == nil {
			continue
		}
		e.handleCommandOutcome(ctx, &corev1.CommandOutcomeEvent{
			RequestId:       leg.requestID,
			AttemptId:       leg.attemptID,
			Source:          source,
			NotForwarded:    true,
			HandedOff:       false,
			TransportCode:   code,
			TransportDetail: detail,
		})
	}
}

func (e *Engine) handleForwardRelayResult(ctx *StepContext, op *pendingRelayRpc, ev *corev1.RelayRpcResultEvent) {
	delete(e.forwardRelayOpByAttempt, op.attemptID)
	elapsedMs := float64(max(int64(0), e.monoMs-op.startedMonoMs))
	if elapsedMs <= 0 {
		elapsedMs = 0.01
	}

	if isRelayRpcError(ev) {
		if nf, ok := parseNotForwardedDetail(ev.ErrorDetails, op.attemptID, op.epoch); ok {
			detail := ev.DiagnosticMessage
			if nf.Reason == "machine_offline" {
				detail = ""
			}
			e.handleCommandOutcome(ctx, &corev1.CommandOutcomeEvent{
				RequestId:       op.requestID,
				AttemptId:       op.attemptID,
				Source:          corev1.OutcomeSource_OUTCOME_SOURCE_SERVER,
				NotForwarded:    true,
				AttemptBound:    true,
				OuterReason:     nf.Reason,
				TransportCode:   security.ConnectCodeName(ev.ConnectCode),
				TransportDetail: detail,
				ElapsedMs:       elapsedMs,
			})
			return
		}
		e.handleCommandOutcome(ctx, &corev1.CommandOutcomeEvent{
			RequestId:       op.requestID,
			AttemptId:       op.attemptID,
			Source:          corev1.OutcomeSource_OUTCOME_SOURCE_TRANSPORT,
			NotForwarded:    false,
			HandedOff:       true,
			AttemptBound:    false,
			TransportCode:   relayRpcTransportCode(ev),
			TransportDetail: ev.DiagnosticMessage,
			ElapsedMs:       elapsedMs,
		})
		return
	}

	var resp v1.ForwardResponse
	if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
		e.handleCommandOutcome(ctx, &corev1.CommandOutcomeEvent{
			RequestId:       op.requestID,
			AttemptId:       op.attemptID,
			Source:          corev1.OutcomeSource_OUTCOME_SOURCE_TRANSPORT,
			NotForwarded:    false,
			HandedOff:       true,
			TransportCode:   errCodeUnknown,
			TransportDetail: err.Error(),
			ElapsedMs:       elapsedMs,
		})
		return
	}

	e.handleCommandOutcome(ctx, &corev1.CommandOutcomeEvent{
		RequestId:    op.requestID,
		AttemptId:    op.attemptID,
		Source:       corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
		AttemptBound: resp.AttemptId == op.attemptID,
		OuterOutcome: resp.Outcome,
		OuterReason:  resp.Reason,
		SealedResult: resp.Result,
		HandedOff:    true,
		ElapsedMs:    elapsedMs,
	})
}

func (e *Engine) handlePushRelayResult(ctx *StepContext, op *pendingRelayRpc, ev *corev1.RelayRpcResultEvent) {
	p := op.push
	if p == nil {
		return
	}
	if isRelayRpcError(ev) {
		retryable := ev.NetworkError || ev.UnresolvedHost ||
			ev.ConnectCode == security.ConnectCodeUnknown ||
			ev.ConnectCode == security.ConnectCodeUnavailable ||
			ev.ConnectCode == security.ConnectCodeDeadlineExceeded
		e.emitPushRpcResult(ctx, &corev1.PushRpcResultEffect{
			OpId:      p.opID,
			Ok:        false,
			Retryable: retryable,
			Error:     relayRpcErrorText(ev),
		})
		return
	}
	switch op.kind {
	case relayRpcPushKey:
		var resp v1.GetPushKeyResponse
		if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
			e.emitPushRpcResult(ctx, &corev1.PushRpcResultEffect{OpId: p.opID, Ok: false, Error: err.Error()})
			return
		}
		if p.getKey {
			e.emitPushRpcResult(ctx, &corev1.PushRpcResultEffect{
				OpId:      p.opID,
				Ok:        true,
				PublicKey: resp.PublicKey,
			})
			return
		}
		kinds := machineNotificationKinds(p.kinds, resp.MachineDiskNotifications)
		if p.setWebSub {
			e.emitRelayRpc(
				ctx,
				corev1.RelayRpcMethod_RELAY_RPC_METHOD_SET_PUSH_SUBSCRIPTION,
				e.cachedToken,
				relayRpcDefaultTimeoutMs,
				"",
				(&v1.SetPushSubscriptionRequest{
					SubscriptionJson: p.subscriptionJSON,
					Kinds:            kinds,
				}).AppendMarshal(nil),
				&pendingRelayRpc{kind: relayRpcPushSub, opID: p.opID, push: p},
			)
			return
		}
		if p.setTarget {
			e.emitRelayRpc(
				ctx,
				corev1.RelayRpcMethod_RELAY_RPC_METHOD_SET_PUSH_TARGET,
				e.cachedToken,
				relayRpcDefaultTimeoutMs,
				"",
				(&v1.SetPushTargetRequest{
					Type:   p.pushType,
					Target: p.target,
					Kinds:  kinds,
				}).AppendMarshal(nil),
				&pendingRelayRpc{kind: relayRpcPushTarget, opID: p.opID, push: p},
			)
			return
		}
	case relayRpcPushSub:
		e.emitPushRpcResult(ctx, &corev1.PushRpcResultEffect{OpId: p.opID, Ok: true, Enabled: true})
	case relayRpcPushTarget:
		var resp v1.SetPushTargetResponse
		if err := resp.Unmarshal(ev.ResponseBytes); err != nil {
			e.emitPushRpcResult(ctx, &corev1.PushRpcResultEffect{OpId: p.opID, Ok: false, Error: err.Error()})
			return
		}
		e.emitPushRpcResult(ctx, &corev1.PushRpcResultEffect{
			OpId:    p.opID,
			Ok:      true,
			Enabled: resp.Enabled,
		})
	}
}

func (e *Engine) handleStreamRawFrame(ctx *StepContext, ev *corev1.StreamRawFrameEvent) {
	if ev == nil || ev.LegId != e.legID || !e.running || len(ev.RawServerFrame) == 0 {
		return
	}
	// Preserve exact signed PeerFrameDelivery bytes (field 11) without re-encoding.
	if rawDelivery, rawResult := extractRawPeerFrameFields(ev.RawServerFrame); len(rawDelivery) > 0 {
		if e.liveness != nil {
			e.liveness.OnFrame(e.monoMs, false)
		}
		e.handleDirectSignalReceived(ctx, &corev1.DirectSignalReceivedEvent{Raw: rawDelivery})
		return
	} else if len(rawResult) > 0 {
		if e.liveness != nil {
			e.liveness.OnFrame(e.monoMs, false)
		}
		var res v1.PeerFrameResult
		if res.Unmarshal(rawResult) == nil && len(res.FrameId) > 0 {
			e.handleDirectSignalDelivery(ctx, &corev1.DirectSignalDeliveryEvent{
				FrameId:      res.FrameId,
				Status:       uint32(res.Status),
				RetryAfterMs: uint64(res.RetryAfterMs),
			})
		}
		return
	}

	var sf v1.ServerFrame
	if sf.Unmarshal(ev.RawServerFrame) != nil {
		return
	}
	switch k := sf.Kind.(type) {
	case *v1.ServerFrame_Ready:
		var serverTimeMs int64
		if k.Ready != nil {
			serverTimeMs = k.Ready.ServerTimeMs
		}
		e.handleStreamReady(ctx, &corev1.StreamReadyEvent{
			LegId:        ev.LegId,
			ServerTimeMs: serverTimeMs,
		})
	case *v1.ServerFrame_Heartbeat:
		e.handleStreamFrame(ctx, &corev1.StreamFrameEvent{
			LegId: ev.LegId,
			Frame: &corev1.StreamFrameEvent_Heartbeat{Heartbeat: true},
		})
	case *v1.ServerFrame_MachineUpdated:
		if k.MachineUpdated != nil && k.MachineUpdated.Machine != nil {
			e.handleStreamFrame(ctx, &corev1.StreamFrameEvent{
				LegId: ev.LegId,
				Frame: &corev1.StreamFrameEvent_WireMachineUpdated{WireMachineUpdated: k.MachineUpdated.Machine},
			})
		}
	case *v1.ServerFrame_MachineRemoved:
		if k.MachineRemoved != nil && k.MachineRemoved.MachineId != "" {
			e.handleStreamFrame(ctx, &corev1.StreamFrameEvent{
				LegId: ev.LegId,
				Frame: &corev1.StreamFrameEvent_MachineRemovedId{MachineRemovedId: k.MachineRemoved.MachineId},
			})
		}
	case *v1.ServerFrame_SessionUpdated:
		if k.SessionUpdated != nil && k.SessionUpdated.Session != nil {
			e.handleStreamFrame(ctx, &corev1.StreamFrameEvent{
				LegId: ev.LegId,
				Frame: &corev1.StreamFrameEvent_WireSessionUpdated{WireSessionUpdated: k.SessionUpdated.Session},
			})
		}
	case *v1.ServerFrame_EventsAppended:
		if k.EventsAppended != nil && k.EventsAppended.SessionId != "" {
			e.handleStreamFrame(ctx, &corev1.StreamFrameEvent{
				LegId: ev.LegId,
				Frame: &corev1.StreamFrameEvent_EventsAppended{
					EventsAppended: &corev1.EventsAppendedFrame{
						SessionId:  k.EventsAppended.SessionId,
						WireEvents: k.EventsAppended.Events,
					},
				},
			})
		}
	case *v1.ServerFrame_AccountPrefsUpdated:
		if k.AccountPrefsUpdated != nil && k.AccountPrefsUpdated.Object != nil &&
			k.AccountPrefsUpdated.Object.ObjectId == ProjectPrefsObjectID {
			e.handleStreamFrame(ctx, &corev1.StreamFrameEvent{
				LegId: ev.LegId,
				Frame: &corev1.StreamFrameEvent_WireAccountPrefsUpdated{WireAccountPrefsUpdated: k.AccountPrefsUpdated.Object},
			})
		}
	case *v1.ServerFrame_DeviceUpdated:
		if k.DeviceUpdated != nil && k.DeviceUpdated.Device != nil {
			e.handleStreamFrame(ctx, &corev1.StreamFrameEvent{
				LegId: ev.LegId,
				Frame: &corev1.StreamFrameEvent_WireDeviceUpdated{WireDeviceUpdated: k.DeviceUpdated.Device},
			})
		}
	case *v1.ServerFrame_DeviceRemoved:
		if k.DeviceRemoved != nil && k.DeviceRemoved.DeviceId != "" {
			e.handleStreamFrame(ctx, &corev1.StreamFrameEvent{
				LegId: ev.LegId,
				Frame: &corev1.StreamFrameEvent_DeviceRemovedId{DeviceRemovedId: k.DeviceRemoved.DeviceId},
			})
		}
	}
}

// extractRawPeerFrameFields scans top-level protobuf fields of ServerFrame for
// field 11 (PeerFrameDelivery) and field 12 (PeerFrameResult) without decoding
// or re-encoding the signed peer packet bytes.
func extractRawPeerFrameFields(b []byte) (delivery []byte, result []byte) {
	for len(b) > 0 {
		num, wtyp, n := litewire.ConsumeTag(b)
		if n < 0 {
			return nil, nil
		}
		b = b[n:]
		if (num == 11 || num == 12) && wtyp == litewire.BytesType {
			val, m := litewire.ConsumeBytes(b)
			if m < 0 {
				return nil, nil
			}
			if num == 11 {
				delivery = val
			} else {
				result = val
			}
			b = b[m:]
			continue
		}
		m := litewire.ConsumeFieldValue(num, wtyp, b)
		if m < 0 {
			return nil, nil
		}
		b = b[m:]
	}
	return delivery, result
}

func isRelayRpcError(ev *corev1.RelayRpcResultEvent) bool {
	return ev.ConnectCode != 0 || ev.NetworkError || ev.UnresolvedHost || ev.DiagnosticMessage != ""
}

func relayRpcErrorText(ev *corev1.RelayRpcResultEvent) string {
	if ev.DiagnosticMessage != "" {
		return ev.DiagnosticMessage
	}
	if ev.ConnectCode != 0 {
		return security.ConnectCodeName(ev.ConnectCode)
	}
	if ev.UnresolvedHost {
		return errCodeUnresolvedHost
	}
	return "network error"
}

func relayRpcTransportCode(ev *corev1.RelayRpcResultEvent) string {
	if ev.UnresolvedHost {
		return errCodeUnresolvedHost
	}
	if ev.NetworkError {
		if ev.ConnectCode == security.ConnectCodeDeadlineExceeded {
			return "deadline_exceeded"
		}
		return errCodeNetwork
	}
	if ev.ConnectCode != 0 {
		return security.ConnectCodeName(ev.ConnectCode)
	}
	return errCodeUnknown
}

func parseNotForwardedDetail(details []*corev1.RpcErrorDetailBytes, attemptID, epoch string) (*v1.NotForwarded, bool) {
	if attemptID == "" || epoch == "" {
		return nil, false
	}
	for _, d := range details {
		if d == nil || len(d.Value) == 0 {
			continue
		}
		if strings.TrimPrefix(d.TypeName, "type.googleapis.com/") != notForwardedTypeName {
			continue
		}
		var nf v1.NotForwarded
		if nf.Unmarshal(d.Value) != nil {
			continue
		}
		if nf.AttemptId == attemptID && nf.RelayEpoch == epoch {
			return &nf, true
		}
	}
	return nil, false
}
