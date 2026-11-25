package sync

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/shared/linkcore"
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
		e.wasConnectedForPush = false
		e.selfDescribeAttempted = false
		e.selfDescribeActionID = 0
		clear(e.prunedUntrustedMachines)
		e.lifecycleGen++
		if e.security.Loaded || e.everReady {
			e.networkGen++
		}
		if e.secEngine != nil {
			e.secEngine.SetForeground(true)
			e.secEngine.SetNetworkGeneration(e.networkGen)
		}
		e.connectGen++
		e.connecting = false
		e.w.LivePushConnected = false
		e.wantedConn = false
		e.activeTokenOpID = 0
		e.activeSecOpID = 0
		e.refreshOpID = 0
		e.pageOpID = 0
		e.prefsLoadOpID = 0
		if !e.w.LivePushConnected {
			e.w.World.Connection = lists.ConnConnecting
			e.connStartedAtMs = e.nowMs
			e.connStep = world.ConnectStepReadingLocalState
		}
		e.displayGraceUntilMs = 0
		if e.everReady && !e.removed && e.foreground && e.w.World.Connection != lists.ConnConnected {
			e.startDisplayGrace(ctx)
		}
		e.coldStartGraceUntilMs = 0
		e.coldStartGraceExpired = false
		if !e.w.World.MachinesListed && !e.removed && e.foreground && e.networkAvailable {
			e.startColdStartGrace(ctx)
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
		c := e.ensureTransportCoord()
		e.emitCoordEffects(ctx, c.SetLifecycle(e.nowMs, true))
		e.emitCoordEffects(ctx, c.SetMainLeg(e.nowMs, false))
		e.beginConnect(ctx)
		ctx.Dirty.MarkAll()

	case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP:
		e.running = false
		e.wasConnectedForPush = false
		e.selfDescribeActionID = 0
		e.w.World.Connection = lists.ConnOffline
		e.displayGraceUntilMs = 0
		e.coldStartGraceUntilMs = 0
		e.coldStartGraceExpired = false
		e.lifecycleGen++
		e.connectGen++
		if e.legID > 0 {
			e.emitCloseStream(ctx, e.legID)
			e.legID = 0
		}
		e.w.LivePushConnected = false
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
		clear(e.sessionByIDFailed)
		clear(e.sessionByIDErrors)
		e.devicesLoadFailed = false
		e.devicesError = world.Reason{}
		clear(e.lateDirectTickets)
		clear(e.lateDirectDeadline)
		for _, prep := range e.inFlightCmds {
			for _, l := range prep.ActiveLegs {
				if l != nil {
					l.TicketID = 0
				}
			}
		}
		e.updateLivePollers(ctx)
		e.cancelAllTimers(ctx)
		c := e.ensureTransportCoord()
		c.ClearTickets()
		e.emitCoordEffects(ctx, c.SetMainLeg(e.nowMs, false))
		e.emitCoordEffects(ctx, c.SetLifecycle(e.nowMs, false))
		ctx.Dirty.MarkAll()

	case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_FOREGROUND:
		wasForeground := e.foreground
		e.foreground = true
		if e.secEngine != nil {
			e.secEngine.SetForeground(true)
		}
		e.emitCoordEffects(ctx, e.ensureTransportCoord().SetLifecycle(e.nowMs, true))
		e.updateLivePollers(ctx)
		if !wasForeground && e.everReady && !e.removed && e.running && e.w.World.Connection != lists.ConnConnected {
			e.startDisplayGrace(ctx)
			ctx.Dirty.MarkAll()
		}
		if !wasForeground && !e.w.World.MachinesListed && !e.coldStartGraceExpired && !e.removed && e.running && e.networkAvailable {
			e.startColdStartGrace(ctx)
			ctx.Dirty.MarkAll()
		}
		if !wasForeground && e.running && !e.removed {
			if e.security.Loaded || e.everReady {
				e.networkGen++
			}
			if e.secEngine != nil {
				e.secEngine.SetNetworkGeneration(e.networkGen)
			}
			if !e.w.LivePushConnected && !e.connecting {
				if e.networkAvailable {
					e.beginConnect(ctx)
				}
			} else if !e.security.Loaded || e.security.VerifiedNetGen != e.networkGen {
				e.requestSyncSecurity(ctx, true)
			}
		} else if e.running && !e.removed && e.networkAvailable && !e.w.LivePushConnected && !e.connecting {
			e.beginConnect(ctx)
		}

	case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_BACKGROUND:
		e.foreground = false
		if e.secEngine != nil {
			e.secEngine.SetForeground(false)
		}
		e.clearDisplayGrace(ctx)
		e.clearColdStartGrace(ctx)
		if len(e.directReady) > 0 {
			clear(e.directReady)
		}
		e.emitCoordEffects(ctx, e.ensureTransportCoord().SetLifecycle(e.nowMs, false))
		e.updateLivePollers(ctx)
		ctx.Dirty.MarkAll()

	case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_RECONNECT_NOW:
		if !e.running || e.removed {
			return
		}
		clear(e.sessionByIDFailed)
		clear(e.sessionByIDErrors)
		e.devicesLoadFailed = false
		e.devicesError = world.Reason{}
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_RECONNECT, "")
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_STREAM_OPEN, "")
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVENESS, "")
		if e.legID > 0 {
			e.emitCloseStream(ctx, e.legID)
			e.legID = 0
		}
		e.connectGen++
		e.connecting = false
		e.w.LivePushConnected = false
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
		e.clearDisplayGrace(ctx)
		e.clearColdStartGrace(ctx)
		e.forceLogin = true
		if e.cachedToken != "" {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DiscardToken{
					DiscardToken: &corev1.DiscardTokenEffect{Token: e.cachedToken},
				},
			})
			e.cachedToken = ""
			e.tokenExpiresMs = 0
			if e.secEngine != nil {
				e.secEngine.SetAuthToken("", 0)
			}
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
		if e.secEngine != nil {
			e.secEngine.SetNetworkGeneration(e.networkGen)
		}
	}

	if !ev.Available {
		wasConnected := e.w.World.Connection == lists.ConnConnected
		e.connectGen++
		if e.legID > 0 {
			e.emitCloseStream(ctx, e.legID)
			e.legID = 0
		}
		e.w.LivePushConnected = false
		e.connecting = false
		e.activeTokenOpID = 0
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_STREAM_OPEN, "")
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVENESS, "")
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LINK_GRACE, "")
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_RECONNECT, "")
		e.w.World.Connection = lists.ConnNoNetwork
		if e.w.DisconnectedAtMs == 0 && e.nowMs > 0 {
			e.w.DisconnectedAtMs = e.nowMs
		}
		e.w.ConnectionReason = world.Reason{Kind: world.ReasonNoNetwork}
		if wasConnected && e.everReady && !e.removed && e.running && e.foreground {
			e.startDisplayGrace(ctx)
		}
		e.clearColdStartGrace(ctx)
		c := e.ensureTransportCoord()
		e.emitCoordEffects(ctx, c.SetMainLeg(e.nowMs, false))
		e.emitCoordEffects(ctx, c.SetNetworkChanged(e.nowMs))
		e.updateLivePollers(ctx)
		ctx.Dirty.MarkAll()
		return
	}

	if sigChanged || !prevAvail {
		e.emitCoordEffects(ctx, e.ensureTransportCoord().SetNetworkChanged(e.nowMs))
	}

	if e.w.World.Connection == lists.ConnNoNetwork {
		if e.everReady {
			e.w.World.Connection = lists.ConnReconnecting
		} else {
			e.w.World.Connection = lists.ConnConnecting
		}
		e.w.ConnectionReason = world.Reason{}
	}
	if !e.w.World.MachinesListed && !e.coldStartGraceExpired && e.running && !e.removed && e.foreground && e.coldStartGraceUntilMs == 0 {
		e.startColdStartGrace(ctx)
	}
	if sigChanged && e.w.LivePushConnected && e.legID > 0 {
		legID := e.legID
		e.emitCloseStream(ctx, legID)
		e.onLegDropped(ctx, legID, false, false, "", "")
		ctx.Dirty.MarkAll()
		return
	}
	if e.running && !e.removed && !e.w.LivePushConnected && !e.connecting {
		e.beginConnect(ctx)
	}
	ctx.Dirty.MarkAll()
}

func (e *Engine) beginConnect(ctx *StepContext) {
	if !e.running || e.removed || !e.networkAvailable || e.connecting || e.w.LivePushConnected || e.isTimerActive(corev1.TimerKind_TIMER_KIND_RECONNECT, "") {
		return
	}
	wait := e.recovery.DelayMs(e.monoMs, &e.budget)
	if wait > 0 || !e.recovery.Take(e.monoMs, &e.budget) {
		if wait <= 0 {
			wait = 1
		}
		e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_RECONNECT, "", wait)
		return
	}
	e.connectGen++
	e.connecting = true
	e.wantedConn = false
	if !e.w.LivePushConnected {
		if e.everReady && e.w.World.Connection != lists.ConnOffline {
			e.w.World.Connection = lists.ConnReconnecting
		} else if !e.everReady {
			e.w.World.Connection = lists.ConnConnecting
		}
		e.connStartedAtMs = e.nowMs
		e.connStep = world.ConnectStepSigningIn
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
	if e.secEngine != nil && e.secEngine.HasWorkflowBootstrapped() && e.cachedToken != "" &&
		(!e.security.Loaded || e.security.VerifiedNetGen != e.networkGen) {
		e.requestSyncSecurity(ctx, true)
	}
	ctx.Dirty.Add(true, true, true)
}

func (e *Engine) requestSyncSecurity(ctx *StepContext, touch bool) {
	if e.secEngine == nil {
		return
	}
	e.secEngine.SetNetworkGeneration(e.networkGen)
	if !e.secEngine.HasWorkflowBootstrapped() {
		return
	}
	e.activeSecOpID = e.allocOpID()
	tag := e.allocSecOp(&pendingSecOp{kind: secOpSyncStates, opID: e.activeSecOpID})
	effs := e.secEngine.SyncStatesTag(e.nowMs, touch, e.cachedToken, tag)
	e.emitSecurityEffects(ctx, effs)
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) handleTokenResult(ctx *StepContext, ev *corev1.TokenResultEvent) {
	if ev == nil || ev.OpId != e.activeTokenOpID || !e.running {
		return
	}
	e.activeTokenOpID = 0
	if ev.Removed {
		e.removed = true
		e.wasConnectedForPush = false
		e.clearDisplayGrace(ctx)
		e.clearColdStartGrace(ctx)
		e.connecting = false
		e.w.LivePushConnected = false
		e.cachedToken = ""
		e.tokenExpiresMs = 0
		if e.secEngine != nil {
			e.secEngine.SetAuthToken("", 0)
		}
		e.forceLogin = false
		e.w.World.Connection = lists.ConnOffline
		if e.w.DisconnectedAtMs == 0 && e.nowMs > 0 {
			e.w.DisconnectedAtMs = e.nowMs
		}
		e.connStep = world.ConnectStepNone
		e.w.ConnectionReason = world.Reason{Kind: world.ReasonDeviceUnrecognized}
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LINK_GRACE, "")
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_RECONNECT, "")
		e.pruneUntrustedCaches(ctx)
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
	if e.secEngine != nil {
		e.secEngine.SetAuthToken(ev.Token, ev.ExpiresMs)
		if e.secEngine.HasWorkflowBootstrapped() && (!e.security.Loaded || e.security.VerifiedNetGen != e.networkGen) {
			e.requestSyncSecurity(ctx, true)
		}
	}
	e.forceLogin = false
	e.connStep = world.ConnectStepEstablishingPush
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
	e.clearDisplayGrace(ctx)

	e.connecting = false
	e.w.LivePushConnected = true
	e.everReady = true
	e.wantedConn = false
	e.recovery.Ready()
	l := linkcore.NewLiveness(e.monoMs)
	e.liveness = &l
	e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVENESS, "", LivenessTickMs)

	e.serverAhead = ev.ServerTimeMs > 0 && e.nowMs > 0 && (e.nowMs-ev.ServerTimeMs > MaxClockSkewMs)
	e.w.World.Connection = lists.ConnConnected
	e.w.DisconnectedAtMs = 0
	e.connStep = world.ConnectStepNone
	e.w.ConnectionReason = world.Reason{}

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
	e.emitCoordEffects(ctx, e.ensureTransportCoord().SetMainLeg(e.nowMs, true))
	if !e.removed && !e.wasConnectedForPush {
		e.wasConnectedForPush = true
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_SyncPushSubscription{
				SyncPushSubscription: &corev1.SyncPushSubscriptionEffect{},
			},
		})
	}
	e.selfDescribeAttempted = false
	e.maybeDescribeSelf(ctx)
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
	e.w.LivePushConnected = false
	e.connecting = false
	e.liveness = nil
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_STREAM_OPEN, "")
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVENESS, "")
	e.emitCoordEffects(ctx, e.ensureTransportCoord().SetMainLeg(e.nowMs, false))

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
			if e.secEngine != nil {
				e.secEngine.SetAuthToken("", 0)
			}
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
	wasConnected := e.w.World.Connection == lists.ConnConnected
	e.wasConnectedForPush = false
	e.connStep = world.ConnectStepNone
	e.w.ConnectionReason = classifyConnectError(unresolvedHost, errCode, errMsg)
	if !e.networkAvailable {
		e.w.World.Connection = lists.ConnNoNetwork
		if e.w.DisconnectedAtMs == 0 && e.nowMs > 0 {
			e.w.DisconnectedAtMs = e.nowMs
		}
		if wasConnected && e.everReady && !e.removed && e.running && e.foreground {
			e.startDisplayGrace(ctx)
		}
		return
	}
	if e.everReady && !e.isTimerActive(corev1.TimerKind_TIMER_KIND_LINK_GRACE, "") {
		if e.w.World.Connection == lists.ConnConnected || e.w.World.Connection == lists.ConnReconnecting {
			e.w.World.Connection = lists.ConnReconnecting
			e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_LINK_GRACE, "", LinkGraceMs)
		}
	} else if !e.everReady {
		e.w.World.Connection = lists.ConnConnecting
	}
	e.recovery.Failed(e.monoMs, e.nextFloat(ctx))
	e.beginConnect(ctx)
	if wasConnected && e.everReady && !e.removed && e.running && e.foreground {
		e.startDisplayGrace(ctx)
	}
}

func (e *Engine) displayGraceActive() bool {
	if !e.everReady || e.removed || e.w.World.Connection == lists.ConnConnected {
		return false
	}
	if !e.running || !e.foreground {
		return true
	}
	return e.displayGraceUntilMs > 0 && e.monoMs < e.displayGraceUntilMs
}

func (e *Engine) startDisplayGrace(ctx *StepContext) {
	if !e.everReady || e.removed || e.w.World.Connection == lists.ConnConnected || !e.running || !e.foreground {
		return
	}
	until := e.monoMs + DisplayGraceMs
	if until > e.displayGraceUntilMs {
		e.displayGraceUntilMs = until
	}
	e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_LINK_GRACE, displayGraceTimerKey, DisplayGraceMs)
}

func (e *Engine) clearDisplayGrace(ctx *StepContext) {
	e.displayGraceUntilMs = 0
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LINK_GRACE, displayGraceTimerKey)
}

func (e *Engine) expireDisplayGraceOnClock(ctx *StepContext) {
	dirty := false
	if e.displayGraceUntilMs > 0 && e.monoMs >= e.displayGraceUntilMs {
		e.clearDisplayGrace(ctx)
		dirty = true
	}
	if e.coldStartGraceUntilMs > 0 && e.monoMs >= e.coldStartGraceUntilMs {
		e.coldStartGraceExpired = true
		e.clearColdStartGrace(ctx)
		dirty = true
	}
	if dirty {
		ctx.Dirty.MarkAll()
	}
}

func (e *Engine) coldStartGraceActive() bool {
	if e.w.World.MachinesListed || e.removed || !e.running || !e.foreground || !e.networkAvailable {
		return false
	}
	return e.coldStartGraceUntilMs > 0 && e.monoMs < e.coldStartGraceUntilMs
}

func (e *Engine) startColdStartGrace(ctx *StepContext) {
	if e.w.World.MachinesListed || e.coldStartGraceExpired || e.removed || !e.running || !e.foreground || !e.networkAvailable {
		return
	}
	until := e.monoMs + DisplayGraceMs
	if until > e.coldStartGraceUntilMs {
		e.coldStartGraceUntilMs = until
	}
	e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_LINK_GRACE, coldStartGraceTimerKey, DisplayGraceMs)
}

func (e *Engine) clearColdStartGrace(ctx *StepContext) {
	e.coldStartGraceUntilMs = 0
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LINK_GRACE, coldStartGraceTimerKey)
}

func classifyConnectError(unresolvedHost bool, errCode, errMsg string) world.Reason {
	if unresolvedHost {
		return world.Reason{Kind: world.ReasonNoServer}
	}
	switch errCode {
	case "unauthenticated", "permission_denied":
		return world.Reason{Kind: world.ReasonDeviceUnrecognized}
	case "deadline_exceeded", "timeout":
		return world.Reason{Kind: world.ReasonConnectionTimedOutReconnecting}
	}
	if errMsg != "" {
		return world.Reason{Kind: world.ReasonServerReturned, Param: errMsg}
	}
	return world.Reason{Kind: world.ReasonDisconnectedFromServer}
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
