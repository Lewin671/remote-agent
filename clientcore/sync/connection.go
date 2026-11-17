package sync

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func (e *Engine) handleLifecycle(ctx *StepContext, act *corev1.SyncLifecycleAction) {
	if act == nil {
		return
	}
	switch act.Kind {
	case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START:
		if act.HomeScopeToken != "" {
			e.homeScopeToken = act.HomeScopeToken
		}
		if len(act.ScopeHmacKey) > 0 {
			e.scopeHMACKey = append([]byte(nil), act.ScopeHmacKey...)
		}
		e.cancelAllTimers(ctx)
		if e.legID > 0 {
			e.emitCloseStream(ctx, e.legID)
			e.legID = 0
		}
		e.running = true
		e.foreground = true
		e.removed = false
		e.lifecycleGen++
		e.connectGen++
		e.connecting = false
		e.streamOpen = false
		e.wantedConn = false
		e.activeTokenOpID = 0
		e.activeSecOpID = 0
		e.refreshOpID = 0
		e.pageOpID = 0
		e.prefsLoadOpID = 0
		if !e.streamOpen {
			e.conn = lists.ConnConnecting
			e.connStartedAtMs = e.nowMs
			e.connStepText = text.Msg(text.KeyErrorsReadingLocalState)
		}
		if !e.bootstrapped {
			e.activeCacheBootID = e.allocOpID()
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheRead{
					CacheRead: &corev1.CacheReadEffect{
						Kind: &corev1.CacheReadEffect_Bootstrap{
							Bootstrap: &corev1.CacheReadBootstrap{OpId: e.activeCacheBootID},
						},
					},
				},
			})
		}
		if len(e.buffers) > 0 {
			e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_HISTORY_SWEEP, "", HistorySweepIntervalMs)
		}
		e.beginConnect(ctx)
		ctx.Dirty.MarkAll()

	case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP:
		e.running = false
		e.conn = lists.ConnOffline
		e.lifecycleGen++
		e.connectGen++
		if e.legID > 0 {
			e.emitCloseStream(ctx, e.legID)
			e.legID = 0
		}
		e.streamOpen = false
		e.connecting = false
		e.wantedConn = false
		e.activeTokenOpID = 0
		e.activeSecOpID = 0
		e.activeCacheBootID = 0
		e.refreshOpID = 0
		e.pageOpID = 0
		e.devicesOpID = 0
		e.prefsLoadOpID = 0
		clear(e.directReady)
		clear(e.sessionByIDOps)
		e.updateLivePollers(ctx)
		e.cancelAllTimers(ctx)
		ctx.Dirty.MarkAll()

	case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_FOREGROUND:
		e.foreground = true
		e.updateLivePollers(ctx)
		if e.running && !e.removed && e.networkAvailable && !e.streamOpen && !e.connecting {
			e.beginConnect(ctx)
		}

	case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_BACKGROUND:
		e.foreground = false
		if len(e.directReady) > 0 {
			clear(e.directReady)
			ctx.Dirty.MarkAll()
		}
		e.updateLivePollers(ctx)

	case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_RECONNECT_NOW:
		if !e.running || e.removed {
			return
		}
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_RECONNECT, "")
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_STREAM_OPEN, "")
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVENESS, "")
		if e.legID > 0 {
			e.emitCloseStream(ctx, e.legID)
			e.legID = 0
		}
		e.connectGen++
		e.connecting = false
		e.streamOpen = false
		e.activeTokenOpID = 0
		e.recovery.Ready()
		if e.networkAvailable {
			e.beginConnect(ctx)
		}
		ctx.Dirty.MarkAll()

	case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_RETRY_LOGIN:
		if !e.running {
			return
		}
		e.removed = false
		e.forceLogin = true
		if e.cachedToken != "" {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DiscardToken{
					DiscardToken: &corev1.DiscardTokenEffect{Token: e.cachedToken},
				},
			})
			e.cachedToken = ""
			e.tokenExpiresMs = 0
		}
		e.recovery.Ready()
		if e.networkAvailable {
			e.beginConnect(ctx)
		}
		ctx.Dirty.MarkAll()
	}
}

func (e *Engine) handleNetworkChanged(ctx *StepContext, ev *corev1.NetworkChangedEvent) {
	if ev == nil {
		return
	}
	prevAvail := e.networkAvailable
	sigChanged := ev.Signature != "" && e.networkSignature != "" && ev.Signature != e.networkSignature
	if ev.Signature != "" {
		e.networkSignature = ev.Signature
	}
	e.networkAvailable = ev.Available
	if sigChanged || (!prevAvail && ev.Available) {
		e.networkGen++
	}

	if !ev.Available {
		e.connectGen++
		if e.legID > 0 {
			e.emitCloseStream(ctx, e.legID)
			e.legID = 0
		}
		e.streamOpen = false
		e.connecting = false
		e.activeTokenOpID = 0
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_STREAM_OPEN, "")
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVENESS, "")
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LINK_GRACE, "")
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_RECONNECT, "")
		e.conn = lists.ConnNoNetwork
		if e.offlineSinceMs == 0 && e.nowMs > 0 {
			e.offlineSinceMs = e.nowMs
		}
		e.connReasonText = text.Msg(text.KeyConnectionNoNetwork)
		e.updateLivePollers(ctx)
		ctx.Dirty.MarkAll()
		return
	}

	if e.conn == lists.ConnNoNetwork {
		if e.everReady {
			e.conn = lists.ConnReconnecting
		} else {
			e.conn = lists.ConnConnecting
		}
		e.connReasonText = nil
	}
	if sigChanged && e.streamOpen && e.legID > 0 {
		legID := e.legID
		e.emitCloseStream(ctx, legID)
		e.onLegDropped(ctx, legID, false, false, "", "")
		ctx.Dirty.MarkAll()
		return
	}
	if e.running && !e.removed && !e.streamOpen && !e.connecting {
		e.beginConnect(ctx)
	}
	ctx.Dirty.MarkAll()
}

func (e *Engine) beginConnect(ctx *StepContext) {
	if !e.running || e.removed || !e.networkAvailable || e.connecting || e.streamOpen || e.isTimerActive(corev1.TimerKind_TIMER_KIND_RECONNECT, "") {
		return
	}
	wait := e.recovery.Delay(e.monoMs)
	if wait > 0 || !e.recovery.Take(e.monoMs) {
		if wait <= 0 {
			wait = 1
		}
		e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_RECONNECT, "", wait)
		return
	}
	e.connectGen++
	e.connecting = true
	e.wantedConn = false
	if !e.streamOpen {
		if e.everReady && e.conn != lists.ConnOffline {
			e.conn = lists.ConnReconnecting
		} else if !e.everReady {
			e.conn = lists.ConnConnecting
		}
		e.connStartedAtMs = e.nowMs
		e.connStepText = text.Msg(text.KeyErrorsSigningIn)
	}
	timeout := StreamOpenTimeoutMs
	if e.everReady || e.networkGen > 0 {
		timeout = min(StreamOpenTimeoutMs, TokenFastTimeoutMs)
	}
	if e.tokenExpiresMs > 0 && e.nowMs > 0 && e.nowMs >= e.tokenExpiresMs {
		e.cachedToken = ""
		e.tokenExpiresMs = 0
		e.forceLogin = true
	}
	e.activeTokenOpID = e.allocOpID()
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_EnsureToken{
			EnsureToken: &corev1.EnsureTokenEffect{
				OpId:       e.activeTokenOpID,
				ForceLogin: e.forceLogin,
				TimeoutMs:  timeout,
			},
		},
	})
	if !e.security.Loaded || e.security.VerifiedNetGen != e.networkGen {
		e.requestSyncSecurity(ctx, true)
	}
	ctx.Dirty.Add(true, true, true)
}

func (e *Engine) requestSyncSecurity(ctx *StepContext, touch bool) {
	e.activeSecOpID = e.allocOpID()
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_SyncSecurity{
			SyncSecurity: &corev1.SyncSecurityEffect{
				OpId:              e.activeSecOpID,
				NetworkGeneration: e.networkGen,
				Touch:             touch,
			},
		},
	})
}

func (e *Engine) handleTokenResult(ctx *StepContext, ev *corev1.TokenResultEvent) {
	if ev == nil || ev.OpId != e.activeTokenOpID || !e.running {
		return
	}
	e.activeTokenOpID = 0
	if ev.Removed {
		e.removed = true
		e.connecting = false
		e.streamOpen = false
		e.cachedToken = ""
		e.tokenExpiresMs = 0
		e.forceLogin = false
		e.conn = lists.ConnOffline
		if e.offlineSinceMs == 0 && e.nowMs > 0 {
			e.offlineSinceMs = e.nowMs
		}
		e.connStepText = nil
		e.connReasonText = text.Msg(text.KeyErrorsTheServerDoesNotRecognizeThisDeviceItMay)
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LINK_GRACE, "")
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_RECONNECT, "")
		e.updateLivePollers(ctx)
		ctx.Dirty.MarkAll()
		return
	}
	if ev.Error != "" || ev.Token == "" {
		e.connecting = false
		e.onConnectFailure(ctx, ev.UnresolvedHost, ev.ErrorCode, ev.Error)
		ctx.Dirty.MarkAll()
		return
	}
	e.cachedToken = ev.Token
	e.tokenExpiresMs = ev.ExpiresMs
	e.forceLogin = false
	e.connStepText = text.Msg(text.KeyErrorsEstablishingPushConnection)
	legID := e.allocLegID()
	e.legID = legID
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_OpenStream{
			OpenStream: &corev1.OpenStreamEffect{
				LegId:     legID,
				Token:     ev.Token,
				TimeoutMs: StreamOpenTimeoutMs,
			},
		},
	})
	e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_STREAM_OPEN, "", StreamOpenTimeoutMs)
	ctx.Dirty.Add(true, false, false)
}

func (e *Engine) handleStreamReady(ctx *StepContext, ev *corev1.StreamReadyEvent) {
	if ev == nil || ev.LegId != e.legID || !e.running {
		return
	}
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_STREAM_OPEN, "")
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LINK_GRACE, "")
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_RECONNECT, "")

	e.connecting = false
	e.streamOpen = true
	e.everReady = true
	e.wantedConn = false
	e.recovery.Ready()
	e.liveness = NewLegLiveness(e.monoMs)
	e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVENESS, "", LivenessTickMs)

	e.serverAhead = ev.ServerTimeMs > 0 && e.nowMs > 0 && (e.nowMs-ev.ServerTimeMs > MaxClockSkewMs)
	e.conn = lists.ConnConnected
	e.offlineSinceMs = 0
	e.connStepText = nil
	e.connReasonText = nil

	e.startRefresh(ctx)
	if !e.prefs.Loaded {
		e.startLoadPrefs(ctx)
	}
	e.updateLivePollers(ctx)
	for sid, buf := range e.buffers {
		if e.watchedSessions[sid] || !buf.loaded {
			e.startFetchEvents(ctx, sid)
		}
	}
	for sid := range e.watchedSessions {
		if e.buffers[sid] == nil && e.sessions[sid] != nil {
			e.startLoadEvents(ctx, sid)
		}
	}
	ctx.Dirty.MarkAll()
}

func (e *Engine) handleStreamClosed(ctx *StepContext, ev *corev1.StreamClosedEvent) {
	if ev == nil {
		return
	}
	e.onLegDropped(ctx, ev.LegId, ev.PolicyViolation, ev.UnresolvedHost, ev.ErrorCode, ev.Error)
}

func (e *Engine) onLegDropped(ctx *StepContext, legID uint64, policyViolation, unresolvedHost bool, errCode, errMsg string) {
	if legID == 0 || legID != e.legID {
		return
	}
	e.legID = 0
	e.streamOpen = false
	e.connecting = false
	e.liveness = nil
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_STREAM_OPEN, "")
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVENESS, "")

	if policyViolation {
		e.forceLogin = true
		if e.cachedToken != "" {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DiscardToken{
					DiscardToken: &corev1.DiscardTokenEffect{Token: e.cachedToken},
				},
			})
			e.cachedToken = ""
			e.tokenExpiresMs = 0
		}
	}

	if !e.running {
		return
	}
	e.onConnectFailure(ctx, unresolvedHost, errCode, errMsg)
	e.updateLivePollers(ctx)
	ctx.Dirty.MarkAll()
}

func (e *Engine) onConnectFailure(ctx *StepContext, unresolvedHost bool, errCode, errMsg string) {
	e.connStepText = nil
	e.connReasonText = classifyConnectError(unresolvedHost, errCode, errMsg)
	if !e.networkAvailable {
		e.conn = lists.ConnNoNetwork
		if e.offlineSinceMs == 0 && e.nowMs > 0 {
			e.offlineSinceMs = e.nowMs
		}
		return
	}
	if e.everReady && !e.isTimerActive(corev1.TimerKind_TIMER_KIND_LINK_GRACE, "") {
		if e.conn == lists.ConnConnected || e.conn == lists.ConnReconnecting {
			e.conn = lists.ConnReconnecting
			e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_LINK_GRACE, "", LinkGraceMs)
		}
	} else if !e.everReady {
		e.conn = lists.ConnConnecting
	}
	e.recovery.Failed(e.monoMs, e.entropy.nextFloat(ctx))
	e.beginConnect(ctx)
}

func classifyConnectError(unresolvedHost bool, errCode, errMsg string) *corev1.Text {
	if unresolvedHost {
		return text.Msg(text.KeyConnectionNoServer)
	}
	switch errCode {
	case "unauthenticated", "permission_denied":
		return text.Msg(text.KeyErrorsTheServerDoesNotRecognizeThisDeviceItMay)
	case "deadline_exceeded", "timeout":
		return text.Msg(text.KeyErrorsConnectionTimedOutReconnecting)
	}
	if errMsg != "" {
		return text.Msg(text.KeyErrorsServerReturned, text.Str("value", errMsg))
	}
	return text.Msg(text.KeyErrorsDisconnectedFromServer)
}

func (e *Engine) handleSecuritySnapshot(ctx *StepContext, ev *corev1.SecuritySnapshotEvent) {
	if ev == nil {
		return
	}
	switch {
	case ev.OpId != 0:
		if ev.OpId != e.activeSecOpID {
			return
		}
		e.activeSecOpID = 0
	case ev.PushSeq != 0:
		if ev.PushSeq <= e.lastSecPushSeq {
			return
		}
		e.lastSecPushSeq = ev.PushSeq
	default:
		return
	}
	if ev.Error != "" {
		if ev.UnresolvedHost {
			e.connReasonText = text.Msg(text.KeyConnectionNoServer)
			ctx.Dirty.Add(true, false, false)
		}
		return
	}
	e.security.Loaded = true
	e.security.IsMember = ev.IsMember
	if ev.SelfDeviceId != "" {
		e.security.SelfDeviceID = ev.SelfDeviceId
	}
	e.security.AuthorityDeviceID = ev.AuthorityDeviceId
	e.security.TrustedMachines = toStringSet(ev.TrustedMachineIds)
	e.security.TrustedClients = toStringSet(ev.TrustedClientIds)
	e.security.ArchiveMachines = toStringSet(ev.ArchiveMachineIds)
	e.security.HeadEpoch = ev.HeadEpoch
	e.security.LeaseEndMs = ev.LeaseEndMs
	e.security.ClockSkewed = ev.ClockSkewed
	e.security.RecoveryPending = ev.RecoveryPending
	e.security.RecoveryReadyMs = ev.RecoveryReadyMs
	e.security.RecoveryObjectsPending = ev.RecoveryObjectsPending
	e.security.NetworkGen = ev.NetworkGeneration
	if ev.NetworkGeneration == e.networkGen || ev.NetworkGeneration >= e.security.VerifiedNetGen {
		e.security.VerifiedNetGen = ev.NetworkGeneration
	}
	e.updateLivePollers(ctx)
	ctx.Dirty.MarkAll()
}

func (e *Engine) checkLeaseClock(ctx *StepContext) {
	if e.security.Loaded && e.security.LeaseEndMs > 0 && e.nowMs >= e.security.LeaseEndMs {
		e.updateLivePollers(ctx)
		ctx.Dirty.MarkAll()
	}
}

func (e *Engine) emitCloseStream(ctx *StepContext, legID uint64) {
	if legID == 0 {
		return
	}
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_CloseStream{
			CloseStream: &corev1.CloseStreamEffect{LegId: legID},
		},
	})
}

func toStringSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	m := make(map[string]bool, len(items))
	for _, s := range items {
		if s != "" {
			m[s] = true
		}
	}
	return m
}
