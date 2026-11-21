package sync

import (
	"maps"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/projection"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func pushedSince(current, snapshot map[string]uint64, id string) bool {
	if current == nil || snapshot == nil {
		return false
	}
	return current[id] > snapshot[id]
}

func (e *Engine) handleCacheBootstrapLoaded(ctx *StepContext, ev *corev1.CacheBootstrapLoadedEvent) {
	if ev == nil {
		return
	}
	if ev.OpId != 0 && ev.OpId != e.activeCacheBootID && e.activeCacheBootID != 0 {
		return
	}
	e.activeCacheBootID = 0
	if ev.Error != "" {
		return
	}
	e.bootstrapped = true
	if len(ev.HomeScopeToken) > 0 && e.homeScopeToken == "" {
		e.homeScopeToken = ev.HomeScopeToken
	}
	if len(ev.ScopeHmacKey) > 0 && len(e.scopeHMACKey) == 0 {
		e.scopeHMACKey = append([]byte(nil), ev.ScopeHmacKey...)
	}

	if len(ev.WireMachines) > 0 || len(ev.WireSessions) > 0 || ev.WirePrefs != nil ||
		(e.secEngine != nil && e.secEngine.Locked() && len(ev.Machines) == 0 && len(ev.Sessions) == 0 && ev.Prefs == nil) {
		var prefsSlice []*v1.AccountPrefsObject
		if ev.WirePrefs != nil {
			prefsSlice = []*v1.AccountPrefsObject{ev.WirePrefs}
		}
		e.openWireDirectoryBatch(ctx, ev.WireMachines, ev.WireSessions, nil, prefsSlice, func(outM []decodedMachine, outS []decodedSession, _ []decodedDevice, outP []*decodedPrefs) {
			stepCtx := e.activeStepCtx
			if !e.w.World.MachinesListed {
				for _, m := range outM {
					if m.MachineID == "" {
						continue
					}
					if _, exists := e.machines[m.MachineID]; !exists {
						m.Online = false
						e.storeMachine(decodedToMachineEntry(m))
					}
				}
			}
			if !e.w.World.SessionsListed {
				for _, s := range outS {
					if s.SessionID == "" {
						continue
					}
					if _, exists := e.sessions[s.SessionID]; !exists {
						e.storeSession(decodedToSessionEntry(s, nil))
						e.listed[s.SessionID] = true
					}
				}
			}
			if !e.prefs.Loaded && len(outP) > 0 && outP[0] != nil {
				e.applyAccountPrefsDecoded(outP[0], false)
			}
			e.updateLivePollers(stepCtx)
			stepCtx.Dirty.MarkAll()
		})
		return
	}

	if !e.w.World.MachinesListed {
		for _, m := range ev.Machines {
			if m == nil || m.MachineId == "" {
				continue
			}
			if _, exists := e.machines[m.MachineId]; !exists {
				e.storeMachine(plainToMachineEntry(m))
			}
		}
	}
	if !e.w.World.SessionsListed {
		for _, s := range ev.Sessions {
			if s == nil || s.SessionId == "" {
				continue
			}
			if _, exists := e.sessions[s.SessionId]; !exists {
				entry := plainToSessionEntry(s, nil)
				e.storeSession(entry)
				e.listed[s.SessionId] = true
			}
		}
	}
	if !e.prefs.Loaded && ev.Prefs != nil {
		e.applyAccountPrefs(ev.Prefs, false)
	}
	e.updateLivePollers(ctx)
	ctx.Dirty.MarkAll()
}

func (e *Engine) startRefresh(ctx *StepContext) {
	if !e.running {
		return
	}
	e.refreshRun++
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LIST_RETRY, "")
	e.refreshOpID = e.allocOpID()
	e.refreshSnapshotM = maps.Clone(e.machinePushes)
	e.refreshSnapshotS = maps.Clone(e.sessionPushes)
	e.refreshSnapshotR = maps.Clone(e.removedPushes)
	e.refreshSnapshotD = maps.Clone(e.devicePushes)

	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_Rpc{
			Rpc: &corev1.RpcEffect{
				OpId: e.refreshOpID,
				Kind: &corev1.RpcEffect_FetchLists{
					FetchLists: &corev1.FetchListsRpc{Limit: SessionPageLimit},
				},
			},
		},
	})
}

func (e *Engine) startRefreshDevices(ctx *StepContext, force bool) {
	if !e.running {
		return
	}
	e.devicesOpID = e.allocOpID()
	e.devicesSnapshotR = maps.Clone(e.removedPushes)
	e.devicesSnapshotD = maps.Clone(e.devicePushes)
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_Rpc{
			Rpc: &corev1.RpcEffect{
				OpId: e.devicesOpID,
				Kind: &corev1.RpcEffect_FetchDevices{
					FetchDevices: &corev1.FetchDevicesRpc{Force: force},
				},
			},
		},
	})
}

// RequestMoreSessions asks for the next page of ended sessions (if connected and more remain).
func (e *Engine) RequestMoreSessions(ctx *StepContext) {
	if !e.w.World.SessionsListed {
		if e.running && e.w.LivePushConnected && e.refreshOpID == 0 {
			e.startRefresh(ctx)
		}
		return
	}
	if e.pageOpID != 0 || e.w.World.SessionsNext == nil || !e.running || e.w.World.Connection != lists.ConnConnected {
		if e.w.World.Connection != lists.ConnConnected && e.w.World.SessionsNext != nil {
			e.w.SessionListErrorText = text.Msg(text.KeyConnectionNoServer)
			ctx.Dirty.Add(true, true, false)
		}
		return
	}
	e.w.World.LoadingMoreSessions = true
	e.w.SessionListErrorText = nil
	e.w.World.SessionListError = ""
	e.pageOpID = e.allocOpID()
	e.pageListingGen = e.listingGen
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_Rpc{
			Rpc: &corev1.RpcEffect{
				OpId: e.pageOpID,
				Kind: &corev1.RpcEffect_FetchSessionsPage{
					FetchSessionsPage: &corev1.FetchSessionsPageRpc{
						After: &v1.SessionCursor{
							EndedMs:   e.w.World.SessionsNext.EndedMs,
							SessionId: e.w.World.SessionsNext.SessionID,
						},
						Limit: SessionPageLimit,
					},
				},
			},
		},
	})
	ctx.Dirty.Add(true, true, false)
}

// RequestAllSessions ensures all remaining ended-session pages are fetched (e.g. for title filtering).
func (e *Engine) RequestAllSessions(ctx *StepContext) {
	e.wantAllSessions = true
	if !e.w.World.SessionsListed || e.w.World.SessionsNext != nil {
		e.RequestMoreSessions(ctx)
	}
}

// ClearWantAllSessions clears automatic remaining-page drainage when search filter clears.
func (e *Engine) ClearWantAllSessions() {
	e.wantAllSessions = false
}

func (e *Engine) fetchSessionByID(ctx *StepContext, sessionID string) {
	if sessionID == "" || !e.running {
		return
	}
	delete(e.sessionByIDFailed, sessionID)
	opID := e.allocOpID()
	e.sessionByIDOps[opID] = sessionID
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_Rpc{
			Rpc: &corev1.RpcEffect{
				OpId: opID,
				Kind: &corev1.RpcEffect_FetchSessionById{
					FetchSessionById: &corev1.FetchSessionByIdRpc{SessionId: sessionID},
				},
			},
		},
	})
}

func (e *Engine) handleListsFetched(ctx *StepContext, ev *corev1.ListsFetchedEvent) {
	if ev == nil || ev.OpId != e.refreshOpID || !e.running {
		return
	}
	e.refreshOpID = 0
	if ev.Error != "" {
		if ev.UnresolvedHost {
			e.w.SessionListErrorText = text.Msg(text.KeyConnectionNoServer)
		} else {
			e.w.SessionListErrorText = text.Plain(ev.Error)
		}
		e.w.World.SessionListError = ev.Error
		wait := e.listBackoffMs
		if wait <= 0 {
			wait = ListRetryInitialMs
		}
		e.listBackoffMs = min(wait*2, ListRetryMaxMs)
		if e.w.LivePushConnected {
			e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_LIST_RETRY, "", wait)
		}
		ctx.Dirty.MarkAll()
		return
	}

	if len(ev.WireMachines) > 0 || len(ev.WireSessions) > 0 || len(ev.WireDevices) > 0 ||
		(e.secEngine != nil && e.secEngine.Locked() && len(ev.Machines) == 0 && len(ev.Sessions) == 0 && len(ev.Devices) == 0) {
		run := e.refreshRun
		mp, sp, rp, dp := e.cloneRefreshSnapshots()
		evNext := ev.Next
		e.openWireDirectoryBatch(ctx, ev.WireMachines, ev.WireSessions, ev.WireDevices, nil, func(outM []decodedMachine, outS []decodedSession, outD []decodedDevice, _ []*decodedPrefs) {
			stepCtx := e.activeStepCtx
			if !e.running || e.refreshRun != run {
				return
			}
			e.devicesApplyGen++
			e.applyListsDecoded(stepCtx, outM, outS, e.buildAuthoritativeDevices(outD), evNext, mp, sp, rp, dp)
		})
		return
	}

	e.listBackoffMs = ListRetryInitialMs
	e.listingGen++
	e.pageOpID = 0
	e.w.World.LoadingMoreSessions = false
	e.w.World.MachinesListed = true
	e.w.World.SessionsListed = true
	e.w.World.LiveLoaded = true
	e.coldStartGraceExpired = true
	e.clearColdStartGrace(ctx)
	e.w.SessionListErrorText = nil
	e.w.World.SessionListError = ""

	mp := e.refreshSnapshotM
	sp := e.refreshSnapshotS
	rp := e.refreshSnapshotR
	dp := e.refreshSnapshotD

	mids := make(map[string]bool, len(ev.Machines))
	for _, m := range ev.Machines {
		if m != nil && m.MachineId != "" {
			mids[m.MachineId] = true
		}
	}
	sids := make([]string, 0, len(ev.Sessions))
	sidSet := make(map[string]bool, len(ev.Sessions))
	for _, s := range ev.Sessions {
		if s != nil && s.SessionId != "" {
			sids = append(sids, s.SessionId)
			sidSet[s.SessionId] = true
		}
	}

	var pageNext *lists.SessionPlace
	if ev.Next != nil && (ev.Next.EndedMs != 0 || ev.Next.SessionId != "") {
		pageNext = &lists.SessionPlace{
			EndedMs:   ev.Next.EndedMs,
			SessionID: ev.Next.SessionId,
		}
	}

	for id := range e.machines {
		if !mids[id] && !pushedSince(e.machinePushes, mp, id) {
			e.dropMachine(ctx, id)
		}
	}

	knownMachines := make([]string, 0, len(mids)+len(e.machines))
	seenMachines := make(map[string]bool, len(mids)+len(e.machines))
	for id := range mids {
		seenMachines[id] = true
		knownMachines = append(knownMachines, id)
	}
	for id := range e.machines {
		if !seenMachines[id] {
			seenMachines[id] = true
			knownMachines = append(knownMachines, id)
		}
	}

	var reconcile []lists.ReconcileSessionEntry
	var pushedIDs []string
	for id, cur := range e.sessions {
		if cur == nil || sidSet[id] {
			continue
		}
		if pushedSince(e.sessionPushes, sp, id) {
			pushedIDs = append(pushedIDs, id)
			continue
		}
		reconcile = append(reconcile, lists.ReconcileSessionEntry{
			ID:            id,
			MachineID:     cur.MachineID,
			ListedEndedMs: cur.ListedEndedMs,
		})
	}
	removeIDs, unlistIDs := lists.ReconcileEndedSessions(nil, reconcile, sids, pushedIDs, knownMachines, pageNext)

	for _, id := range removeIDs {
		e.removeSession(ctx, id, true)
	}
	for _, id := range unlistIDs {
		delete(e.listed, id)
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_CacheDelete{
				CacheDelete: &corev1.CacheDeleteEffect{
					Kind: &corev1.CacheDeleteEffect_UnlistSessionId{UnlistSessionId: id},
				},
			},
		})
	}
	for id := range e.listed {
		if !sidSet[id] && !pushedSince(e.sessionPushes, sp, id) {
			delete(e.listed, id)
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheDelete{
					CacheDelete: &corev1.CacheDeleteEffect{
						Kind: &corev1.CacheDeleteEffect_UnlistSessionId{UnlistSessionId: id},
					},
				},
			})
		}
	}

	for _, m := range ev.Machines {
		if m == nil || m.MachineId == "" || pushedSince(e.removedPushes, rp, m.MachineId) {
			continue
		}
		e.putMachine(ctx, m, pushedSince(e.machinePushes, mp, m.MachineId), true)
	}

	for _, s := range ev.Sessions {
		if s == nil || s.SessionId == "" {
			continue
		}
		if e.listed[s.SessionId] == false {
			e.listed[s.SessionId] = true
		}
		e.putSession(ctx, s, true)
	}

	e.reconcileDevices(ev.Devices, rp, dp, false)
	e.w.World.SessionsNext = pageNext
	e.w.World.HasMoreSessions = pageNext != nil

	e.updateLivePollers(ctx)
	e.syncDirectWatched(ctx)
	if e.wantAllSessions && e.w.World.SessionsNext != nil {
		e.RequestMoreSessions(ctx)
	}
	ctx.Dirty.MarkAll()
}

func (e *Engine) handleSessionsPageFetched(ctx *StepContext, ev *corev1.SessionsPageFetchedEvent) {
	if ev == nil || ev.OpId != e.pageOpID || !e.running {
		return
	}
	e.pageOpID = 0
	e.w.World.LoadingMoreSessions = false
	if e.pageListingGen != e.listingGen {
		return
	}
	if ev.Error != "" {
		e.w.SessionListErrorText = text.Plain(ev.Error)
		e.w.World.SessionListError = ev.Error
		ctx.Dirty.Add(true, true, false)
		return
	}
	if len(ev.WireSessions) > 0 || (e.secEngine != nil && e.secEngine.Locked() && len(ev.Sessions) == 0) {
		gen := e.listingGen
		evNext := ev.Next
		e.openWireDirectoryBatch(ctx, nil, ev.WireSessions, nil, nil, func(_ []decodedMachine, outS []decodedSession, _ []decodedDevice, _ []*decodedPrefs) {
			stepCtx := e.activeStepCtx
			if !e.running || e.listingGen != gen || e.pageListingGen != e.listingGen {
				return
			}
			e.w.SessionListErrorText = nil
			e.w.World.SessionListError = ""
			for _, s := range outS {
				if s.SessionID != "" {
					e.putSessionDecoded(stepCtx, s, false)
				}
			}
			if evNext != nil && (evNext.EndedMs != 0 || evNext.SessionId != "") {
				e.w.World.SessionsNext = &lists.SessionPlace{
					EndedMs:   evNext.EndedMs,
					SessionID: evNext.SessionId,
				}
				e.w.World.HasMoreSessions = true
			} else {
				e.w.World.SessionsNext = nil
				e.w.World.HasMoreSessions = false
			}
			e.updateLivePollers(stepCtx)
			e.syncDirectWatched(stepCtx)
			if e.wantAllSessions && e.w.World.SessionsNext != nil {
				e.RequestMoreSessions(stepCtx)
			}
			stepCtx.Dirty.Add(true, true, false)
		})
		return
	}
	e.w.SessionListErrorText = nil
	e.w.World.SessionListError = ""
	for _, s := range ev.Sessions {
		if s != nil && s.SessionId != "" {
			e.putSession(ctx, s, false)
		}
	}
	if ev.Next != nil && (ev.Next.EndedMs != 0 || ev.Next.SessionId != "") {
		e.w.World.SessionsNext = &lists.SessionPlace{
			EndedMs:   ev.Next.EndedMs,
			SessionID: ev.Next.SessionId,
		}
		e.w.World.HasMoreSessions = true
	} else {
		e.w.World.SessionsNext = nil
		e.w.World.HasMoreSessions = false
	}
	e.updateLivePollers(ctx)
	e.syncDirectWatched(ctx)
	if e.wantAllSessions && e.w.World.SessionsNext != nil {
		e.RequestMoreSessions(ctx)
	}
	ctx.Dirty.Add(true, true, false)
}

func (e *Engine) handleSessionByIDFetched(ctx *StepContext, ev *corev1.SessionByIdFetchedEvent) {
	if ev == nil {
		return
	}
	sid, ok := e.sessionByIDOps[ev.OpId]
	if !ok {
		return
	}
	delete(e.sessionByIDOps, ev.OpId)
	if ev.WireSession != nil || (e.secEngine != nil && e.secEngine.Locked() && ev.Session == nil) {
		if ev.Error != "" || ev.WireSession == nil || ev.WireSession.SessionId == "" {
			if sid != "" {
				e.sessionByIDFailed[sid] = true
			}
			ctx.Dirty.Add(false, true, false)
			return
		}
		e.openWireDirectoryBatch(ctx, nil, []*v1.RemoteSession{ev.WireSession}, nil, nil, func(_ []decodedMachine, outS []decodedSession, _ []decodedDevice, _ []*decodedPrefs) {
			stepCtx := e.activeStepCtx
			if !e.running || len(outS) == 0 {
				return
			}
			delete(e.sessionByIDFailed, sid)
			e.putSessionDecoded(stepCtx, outS[0], false)
			e.updateLivePollers(stepCtx)
			stepCtx.Dirty.MarkAll()
		})
		return
	}
	if ev.Error != "" || ev.Session == nil || ev.Session.SessionId == "" {
		if sid != "" {
			e.sessionByIDFailed[sid] = true
		}
		ctx.Dirty.Add(false, true, false)
		return
	}
	delete(e.sessionByIDFailed, sid)
	e.putSession(ctx, ev.Session, false)
	e.updateLivePollers(ctx)
	ctx.Dirty.MarkAll()
}

func (e *Engine) handleDevicesFetched(ctx *StepContext, ev *corev1.DevicesFetchedEvent) {
	if ev == nil {
		return
	}
	var removedSnapshot, deviceSnapshot map[string]uint64
	switch {
	case ev.OpId != 0:
		if ev.OpId != e.devicesOpID {
			return
		}
		e.devicesOpID = 0
		removedSnapshot = e.devicesSnapshotR
		deviceSnapshot = e.devicesSnapshotD
	case ev.PushSeq != 0:
		if ev.PushSeq <= e.lastDevicesPushSeq {
			return
		}
		e.lastDevicesPushSeq = ev.PushSeq
		removedSnapshot = nil
		deviceSnapshot = nil
	default:
		return
	}
	if ev.Error != "" {
		return
	}
	if len(ev.WireDevices) > 0 || (e.secEngine != nil && e.secEngine.Locked() && len(ev.Devices) == 0) {
		e.devicesApplyGen++
		devGen := e.devicesApplyGen
		e.openWireDirectoryBatch(ctx, nil, nil, ev.WireDevices, nil, func(_ []decodedMachine, _ []decodedSession, outD []decodedDevice, _ []*decodedPrefs) {
			stepCtx := e.activeStepCtx
			if e.devicesApplyGen != devGen {
				return
			}
			e.reconcileDecodedDevices(e.buildAuthoritativeDevices(outD), removedSnapshot, deviceSnapshot, true)
			stepCtx.Dirty.MarkAll()
		})
		return
	}
	e.reconcileDevices(ev.Devices, removedSnapshot, deviceSnapshot, true)
	ctx.Dirty.MarkAll()
}

func (e *Engine) handleStreamFrame(ctx *StepContext, ev *corev1.StreamFrameEvent) {
	if ev == nil || ev.LegId != e.legID || !e.running {
		return
	}
	_, isHeartbeat := ev.Frame.(*corev1.StreamFrameEvent_Heartbeat)
	if e.liveness != nil {
		e.liveness.OnFrame(e.monoMs, isHeartbeat)
	}
	if e.w.World.Connection == lists.ConnConnecting {
		e.w.World.Connection = lists.ConnConnected
		e.w.DisconnectedAtMs = 0
		e.connStepText = nil
		e.w.ConnectionReasonText = nil
		ctx.Dirty.MarkAll()
	}

	switch f := ev.Frame.(type) {
	case *corev1.StreamFrameEvent_Heartbeat:
		return
	case *corev1.StreamFrameEvent_MachineUpdated:
		if f.MachineUpdated != nil && f.MachineUpdated.MachineId != "" {
			e.nextPushSeq++
			e.machinePushes[f.MachineUpdated.MachineId] = e.nextPushSeq
			e.putMachine(ctx, f.MachineUpdated, false, true)
			if f.MachineUpdated.Online && e.security.Loaded && !e.isTrustedMachine(f.MachineUpdated.MachineId) {
				e.requestSyncSecurity(ctx, true)
			}
			e.updateLivePollers(ctx)
			ctx.Dirty.MarkAll()
		}
	case *corev1.StreamFrameEvent_MachineRemovedId:
		if f.MachineRemovedId != "" {
			e.nextPushSeq++
			e.removedPushes[f.MachineRemovedId] = e.nextPushSeq
			e.dropMachine(ctx, f.MachineRemovedId)
			e.requestSyncSecurity(ctx, true)
			e.updateLivePollers(ctx)
			ctx.Dirty.MarkAll()
		}
	case *corev1.StreamFrameEvent_SessionUpdated:
		if f.SessionUpdated != nil && f.SessionUpdated.SessionId != "" {
			sid := f.SessionUpdated.SessionId
			e.nextPushSeq++
			e.sessionPushes[sid] = e.nextPushSeq
			prev := e.sessions[sid]
			var prevServerSeq uint64
			if prev != nil {
				prevServerSeq = prev.ServerLastSeq
			}
			e.putSession(ctx, f.SessionUpdated, true)
			updated := e.sessions[sid]
			b := e.buffers[sid]
			needGapFill := b != nil && len(b.pending) > 0 && updated != nil && updated.ServerLastSeq > uint64(len(b.prefix))
			if f.SessionUpdated.LastSeq > prevServerSeq || needGapFill {
				if e.watchedSessions[sid] || b != nil {
					e.startFetchEvents(ctx, sid)
				}
			}
			e.updateLivePollers(ctx)
			ctx.Dirty.MarkAll()
		}
	case *corev1.StreamFrameEvent_EventsAppended:
		if f.EventsAppended != nil && f.EventsAppended.SessionId != "" {
			e.handleEventsAppended(ctx, f.EventsAppended)
		}
	case *corev1.StreamFrameEvent_AccountPrefsUpdated:
		if f.AccountPrefsUpdated != nil {
			e.applyAccountPrefs(f.AccountPrefsUpdated, true)
			ctx.Dirty.MarkAll()
		}
	case *corev1.StreamFrameEvent_DeviceUpdated:
		if f.DeviceUpdated != nil && f.DeviceUpdated.DeviceId != "" {
			e.nextPushSeq++
			e.devicePushes[f.DeviceUpdated.DeviceId] = e.nextPushSeq
			e.devices[f.DeviceUpdated.DeviceId] = plainToDeviceEntry(f.DeviceUpdated)
			if e.deviceNeedsSecuritySync(f.DeviceUpdated) {
				e.requestSyncSecurity(ctx, true)
			}
			ctx.Dirty.Add(true, false, false)
		}
	case *corev1.StreamFrameEvent_DeviceRemovedId:
		if f.DeviceRemovedId != "" {
			e.nextPushSeq++
			e.removedPushes[f.DeviceRemovedId] = e.nextPushSeq
			delete(e.devices, f.DeviceRemovedId)
			e.requestSyncSecurity(ctx, true)
			ctx.Dirty.Add(true, false, false)
		}
	case *corev1.StreamFrameEvent_WireMachineUpdated:
		if f.WireMachineUpdated != nil && f.WireMachineUpdated.Id != "" {
			e.nextPushSeq++
			e.machinePushes[f.WireMachineUpdated.Id] = e.nextPushSeq
			legID := ev.LegId
			wm := f.WireMachineUpdated
			e.openWireDirectoryBatch(ctx, []*v1.Machine{wm}, nil, nil, nil, func(outM []decodedMachine, _ []decodedSession, _ []decodedDevice, _ []*decodedPrefs) {
				stepCtx := e.activeStepCtx
				if !e.running || e.legID != legID || len(outM) == 0 {
					return
				}
				e.putMachineDecoded(stepCtx, outM[0], false, true)
				if wm.Online && e.security.Loaded && !e.isTrustedMachine(wm.Id) {
					e.requestSyncSecurity(stepCtx, true)
				}
				e.updateLivePollers(stepCtx)
				stepCtx.Dirty.MarkAll()
			})
		}
	case *corev1.StreamFrameEvent_WireSessionUpdated:
		if f.WireSessionUpdated != nil && f.WireSessionUpdated.SessionId != "" {
			sid := f.WireSessionUpdated.SessionId
			e.nextPushSeq++
			e.sessionPushes[sid] = e.nextPushSeq
			legID := ev.LegId
			ws := f.WireSessionUpdated
			e.openWireDirectoryBatch(ctx, nil, []*v1.RemoteSession{ws}, nil, nil, func(_ []decodedMachine, outS []decodedSession, _ []decodedDevice, _ []*decodedPrefs) {
				stepCtx := e.activeStepCtx
				if !e.running || e.legID != legID || len(outS) == 0 {
					return
				}
				prev := e.sessions[sid]
				var prevServerSeq uint64
				if prev != nil {
					prevServerSeq = prev.ServerLastSeq
				}
				e.putSessionDecoded(stepCtx, outS[0], true)
				updated := e.sessions[sid]
				b := e.buffers[sid]
				needGapFill := b != nil && len(b.pending) > 0 && updated != nil && updated.ServerLastSeq > uint64(len(b.prefix))
				if ws.LastSeq > prevServerSeq || needGapFill {
					if e.watchedSessions[sid] || b != nil {
						e.startFetchEvents(stepCtx, sid)
					}
				}
				e.updateLivePollers(stepCtx)
				stepCtx.Dirty.MarkAll()
			})
		}
	case *corev1.StreamFrameEvent_WireAccountPrefsUpdated:
		if f.WireAccountPrefsUpdated != nil {
			legID := ev.LegId
			wp := f.WireAccountPrefsUpdated
			e.openWireDirectoryBatch(ctx, nil, nil, nil, []*v1.AccountPrefsObject{wp}, func(_ []decodedMachine, _ []decodedSession, _ []decodedDevice, outP []*decodedPrefs) {
				stepCtx := e.activeStepCtx
				if !e.running || e.legID != legID || len(outP) == 0 || outP[0] == nil {
					return
				}
				e.applyAccountPrefsDecoded(outP[0], true)
				if outP[0].Wire != nil && outP[0].Wire.Revision > 0 {
					stepCtx.emit(&corev1.Effect{
						Effect: &corev1.Effect_CacheWrite{
							CacheWrite: &corev1.CacheWriteEffect{
								Kind: &corev1.CacheWriteEffect_WritePrefs{WritePrefs: outP[0].Wire},
							},
						},
					})
				}
				stepCtx.Dirty.MarkAll()
			})
		}
	case *corev1.StreamFrameEvent_WireDeviceUpdated:
		if f.WireDeviceUpdated != nil && f.WireDeviceUpdated.Id != "" {
			e.nextPushSeq++
			e.devicePushes[f.WireDeviceUpdated.Id] = e.nextPushSeq
			legID := ev.LegId
			wd := f.WireDeviceUpdated
			e.openWireDirectoryBatch(ctx, nil, nil, []*v1.Device{wd}, nil, func(_ []decodedMachine, _ []decodedSession, outD []decodedDevice, _ []*decodedPrefs) {
				stepCtx := e.activeStepCtx
				if !e.running || e.legID != legID || len(outD) == 0 {
					return
				}
				e.devices[wd.Id] = decodedToDeviceEntry(outD[0])
				if e.deviceKindNeedsSecuritySync(wd.Id, wd.Kind) {
					e.requestSyncSecurity(stepCtx, true)
				}
				stepCtx.Dirty.Add(true, false, false)
			})
		}
	}
}

func (e *Engine) handleScheduleHints(ctx *StepContext, ev *corev1.SessionScheduleHintsEvent) {
	if ev == nil {
		return
	}
	for _, h := range ev.Hints {
		if h == nil || h.SessionId == "" {
			continue
		}
		if s := e.sessions[h.SessionId]; s != nil {
			s.ScheduleName = h.ScheduleName
			s.ScheduleUnread = h.Unread
		}
	}
	ctx.Dirty.Add(true, true, false)
}

func (e *Engine) putMachine(ctx *StepContext, pm *corev1.PlainMachine, staleOnline bool, writeCache bool) {
	if pm == nil || pm.MachineId == "" {
		return
	}
	e.putMachineDecoded(ctx, decodedMachine{
		MachineID:  pm.MachineId,
		Online:     pm.Online,
		LastSeenMs: pm.LastSeenMs,
		Meta:       pm.Meta,
		MetaRev:    pm.MetaRev,
		MetaBroken: pm.MetaBroken,
		CacheRef:   pm.CacheRef,
	}, staleOnline, writeCache)
}

func (e *Engine) putMachineDecoded(ctx *StepContext, dm decodedMachine, staleOnline bool, writeCache bool) {
	if dm.MachineID == "" {
		return
	}
	cur := e.machines[dm.MachineID]
	entry := decodedToMachineEntry(dm)
	if cur != nil {
		if staleOnline {
			entry.Online = cur.Online
		}
		if entry.Meta == nil && !entry.MetaBroken && cur.Meta != nil && cur.MetaRev >= entry.MetaRev {
			entry.Meta = cur.Meta
			entry.MetaRev = cur.MetaRev
		}
		if cur.Meta != nil && entry.Meta != nil && cur.Meta.SettingsRevision > entry.Meta.SettingsRevision {
			entry.Meta = entry.Meta.Clone()
			entry.Meta.DisabledAgents = append([]v1.Agent(nil), cur.Meta.DisabledAgents...)
			entry.Meta.SettingsRevision = cur.Meta.SettingsRevision
		}
	}
	e.storeMachine(entry)
	if writeCache {
		if dm.Wire != nil {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheWrite{
					CacheWrite: &corev1.CacheWriteEffect{
						Kind: &corev1.CacheWriteEffect_WriteMachine{WriteMachine: dm.Wire},
					},
				},
			})
		} else if dm.CacheRef != "" {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheWrite{
					CacheWrite: &corev1.CacheWriteEffect{
						Kind: &corev1.CacheWriteEffect_WriteMachineRef{WriteMachineRef: dm.CacheRef},
					},
				},
			})
		}
	}
}

func (e *Engine) dropMachine(ctx *StepContext, machineID string) {
	if _, ok := e.machines[machineID]; ok {
		delete(e.machines, machineID)
		e.machineOrderStale = true
	}
	delete(e.directReady, machineID)
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_CacheDelete{
			CacheDelete: &corev1.CacheDeleteEffect{
				Kind: &corev1.CacheDeleteEffect_RemoveMachineId{RemoveMachineId: machineID},
			},
		},
	})
	for sid, s := range e.sessions {
		if s != nil && s.MachineID == machineID {
			e.removeSession(ctx, sid, true)
		}
	}
}

func (e *Engine) putSession(ctx *StepContext, ps *corev1.PlainSession, writeCache bool) {
	if ps == nil || ps.SessionId == "" {
		return
	}
	e.putSessionDecoded(ctx, decodedSession{
		SessionID:         ps.SessionId,
		MachineID:         ps.MachineId,
		LastSeq:           ps.LastSeq,
		UpdatedMs:         ps.UpdatedMs,
		ListedEndedMs:     ps.ListedEndedMs,
		Meta:              ps.Meta,
		MetaRev:           ps.MetaRev,
		Checkpoint:        ps.Checkpoint,
		CheckpointInvalid: ps.CheckpointInvalid,
		RetiredHistory:    ps.RetiredHistory,
		OpenFailed:        ps.OpenFailed,
		CacheRef:          ps.CacheRef,
	}, writeCache)
}

func (e *Engine) putSessionDecoded(ctx *StepContext, ds decodedSession, writeCache bool) {
	if ds.SessionID == "" {
		return
	}
	delete(e.sessionByIDFailed, ds.SessionID)
	prev := e.sessions[ds.SessionID]
	entry := decodedToSessionEntry(ds, prev)
	e.refreshSessionPending(entry)
	e.storeSession(entry)

	if ds.OpenFailed && ds.MachineID != "" && e.isTrustedMachine(ds.MachineID) {
		if ds.Wire != nil {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheWrite{
					CacheWrite: &corev1.CacheWriteEffect{
						Kind: &corev1.CacheWriteEffect_KeepSessionOwner{KeepSessionOwner: ds.Wire},
					},
				},
			})
		} else if ds.CacheRef != "" {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheWrite{
					CacheWrite: &corev1.CacheWriteEffect{
						Kind: &corev1.CacheWriteEffect_KeepSessionOwnerRef{KeepSessionOwnerRef: ds.CacheRef},
					},
				},
			})
		}
	} else if writeCache {
		if ds.Wire != nil {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheWrite{
					CacheWrite: &corev1.CacheWriteEffect{
						Kind: &corev1.CacheWriteEffect_WriteSession{WriteSession: ds.Wire},
					},
				},
			})
		} else if ds.CacheRef != "" {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheWrite{
					CacheWrite: &corev1.CacheWriteEffect{
						Kind: &corev1.CacheWriteEffect_WriteSessionRef{WriteSessionRef: ds.CacheRef},
					},
				},
			})
		}
	}
	if ds.RetiredHistory {
		e.markSessionRetired(ds.SessionID)
	}
	if ds.OpenFailed {
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_CacheWrite{
				CacheWrite: &corev1.CacheWriteEffect{
					Kind: &corev1.CacheWriteEffect_SetSessionOpen{
						SetSessionOpen: &corev1.CacheSessionOpen{SessionId: ds.SessionID, Open: false},
					},
				},
			},
		})
	}
	if e.watchedSessions[ds.SessionID] && e.buffers[ds.SessionID] == nil {
		e.startLoadEvents(ctx, ds.SessionID)
	}
	if e.watchedSessions[ds.SessionID] {
		e.syncDirectWatched(ctx)
	}
}

// SeedSession records a session learned from a daemon command result
// (CreateSession / ForkSession) before the relay's SessionUpdated frame arrives,
// so navigating straight into it never flashes "not found".
func (e *Engine) SeedSession(ctx *StepContext, machineID string, meta *v1.Session) {
	if meta == nil || meta.Id == "" || e.sessions[meta.Id] != nil {
		return
	}
	updated := e.nowMs
	if meta.CreatedAt != nil {
		if ms := meta.CreatedAt.Seconds*1000 + int64(meta.CreatedAt.Nanos)/1_000_000; ms > 0 {
			updated = ms
		}
	}
	e.nextPushSeq++
	e.sessionPushes[meta.Id] = e.nextPushSeq
	e.putSession(ctx, &corev1.PlainSession{
		SessionId: meta.Id,
		MachineId: machineID,
		UpdatedMs: updated,
		Meta:      meta.Clone(),
	}, false)
	e.updateLivePollers(ctx)
	e.refreshWorld()
	ctx.Dirty.MarkAll()
}

func (e *Engine) removeSession(ctx *StepContext, sid string, dropFromCache bool) {
	if _, ok := e.sessions[sid]; ok {
		delete(e.sessions, sid)
		e.sessionOrderStale = true
	}
	delete(e.listed, sid)
	delete(e.buffers, sid)
	delete(e.livePollInFlight, sid)
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVE_POLL, sid)
	if dropFromCache {
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_CacheDelete{
				CacheDelete: &corev1.CacheDeleteEffect{
					Kind: &corev1.CacheDeleteEffect_RemoveSessionId{RemoveSessionId: sid},
				},
			},
		})
	}
}

func (e *Engine) reconcileDevices(items []*corev1.PlainDevice, removedSnapshot, deviceSnapshot map[string]uint64, allowEmpty bool) {
	decoded := make([]decodedDevice, 0, len(items))
	for _, d := range items {
		if d == nil || d.DeviceId == "" {
			continue
		}
		decoded = append(decoded, decodedDevice{
			DeviceID:   d.DeviceId,
			Kind:       d.Kind,
			CreatedMs:  d.CreatedMs,
			LastSeenMs: d.LastSeenMs,
			Meta:       d.Meta,
			MetaRev:    d.MetaRev,
			MetaBroken: d.MetaBroken,
		})
	}
	e.reconcileDecodedDevices(decoded, removedSnapshot, deviceSnapshot, allowEmpty)
}

func (e *Engine) reconcileDecodedDevices(items []decodedDevice, removedSnapshot, deviceSnapshot map[string]uint64, allowEmpty bool) {
	if len(items) == 0 && !allowEmpty {
		return
	}
	next := make(map[string]*DeviceEntry, len(items))
	for _, d := range items {
		if d.DeviceID == "" || pushedSince(e.removedPushes, removedSnapshot, d.DeviceID) {
			continue
		}
		next[d.DeviceID] = decodedToDeviceEntry(d)
	}
	for id, cur := range e.devices {
		if cur != nil && pushedSince(e.devicePushes, deviceSnapshot, id) {
			next[id] = cur
		}
	}
	e.devices = next
}

func plainToMachineEntry(pm *corev1.PlainMachine) *MachineEntry {
	return decodedToMachineEntry(decodedMachine{
		MachineID:  pm.MachineId,
		Online:     pm.Online,
		LastSeenMs: pm.LastSeenMs,
		Meta:       pm.Meta,
		MetaRev:    pm.MetaRev,
		MetaBroken: pm.MetaBroken,
		CacheRef:   pm.CacheRef,
	})
}

func decodedToMachineEntry(dm decodedMachine) *MachineEntry {
	var meta *v1.MachineMeta
	if dm.Meta != nil {
		meta = dm.Meta.Clone()
	}
	return &MachineEntry{
		Machine: lists.Machine{
			ID:         dm.MachineID,
			Online:     dm.Online,
			LastSeenMs: dm.LastSeenMs,
			Meta:       meta,
		},
		MetaRev:    dm.MetaRev,
		MetaBroken: dm.MetaBroken,
		CacheRef:   dm.CacheRef,
	}
}

func plainToSessionEntry(ps *corev1.PlainSession, prev *SessionEntry) *SessionEntry {
	return decodedToSessionEntry(decodedSession{
		SessionID:         ps.SessionId,
		MachineID:         ps.MachineId,
		LastSeq:           ps.LastSeq,
		UpdatedMs:         ps.UpdatedMs,
		ListedEndedMs:     ps.ListedEndedMs,
		Meta:              ps.Meta,
		MetaRev:           ps.MetaRev,
		Checkpoint:        ps.Checkpoint,
		CheckpointInvalid: ps.CheckpointInvalid,
		RetiredHistory:    ps.RetiredHistory,
		OpenFailed:        ps.OpenFailed,
		CacheRef:          ps.CacheRef,
	}, prev)
}

func decodedToSessionEntry(ds decodedSession, prev *SessionEntry) *SessionEntry {
	entry := &SessionEntry{
		Session: lists.Session{
			ID:            ds.SessionID,
			MachineID:     ds.MachineID,
			UpdatedMs:     ds.UpdatedMs,
			ListedEndedMs: ds.ListedEndedMs,
		},
		LastSeq:           ds.LastSeq,
		ServerLastSeq:     ds.LastSeq,
		MetaRev:           ds.MetaRev,
		CheckpointInvalid: ds.CheckpointInvalid,
		RetiredHistory:    ds.RetiredHistory,
		OpenFailed:        ds.OpenFailed,
		CacheRef:          ds.CacheRef,
	}
	if ds.Meta != nil {
		entry.setInfo(ds.Meta.Clone())
	}
	if ds.Checkpoint != nil {
		entry.Checkpoint = ds.Checkpoint.Clone()
	}
	if prev != nil {
		if entry.MachineID == "" {
			entry.MachineID = prev.MachineID
		}
		if prev.ServerLastSeq > entry.ServerLastSeq {
			entry.ServerLastSeq = prev.ServerLastSeq
		}
		if prev.LastSeq > entry.LastSeq {
			entry.LastSeq = prev.LastSeq
		}
		if entry.ServerLastSeq > entry.LastSeq {
			entry.LastSeq = entry.ServerLastSeq
		}
		if entry.Info == nil && prev.Info != nil && prev.MetaRev >= entry.MetaRev {
			entry.setInfo(prev.Info)
			entry.MetaRev = prev.MetaRev
		}
		if entry.Checkpoint == nil && !ds.CheckpointInvalid {
			entry.Checkpoint = prev.Checkpoint
		}
		if entry.ListedEndedMs == 0 {
			entry.ListedEndedMs = prev.ListedEndedMs
		}
		entry.ScheduleName = prev.ScheduleName
		entry.ScheduleUnread = prev.ScheduleUnread
		entry.RetiredHistory = entry.RetiredHistory || prev.RetiredHistory
	}
	return entry
}

func plainToDeviceEntry(pd *corev1.PlainDevice) *DeviceEntry {
	return decodedToDeviceEntry(decodedDevice{
		DeviceID:   pd.DeviceId,
		Kind:       pd.Kind,
		CreatedMs:  pd.CreatedMs,
		LastSeenMs: pd.LastSeenMs,
		Meta:       pd.Meta,
		MetaRev:    pd.MetaRev,
		MetaBroken: pd.MetaBroken,
	})
}

func decodedToDeviceEntry(dd decodedDevice) *DeviceEntry {
	var meta *v1.DeviceMeta
	if dd.Meta != nil {
		meta = dd.Meta.Clone()
	}
	return &DeviceEntry{
		ID:         dd.DeviceID,
		Kind:       dd.Kind,
		CreatedMs:  dd.CreatedMs,
		LastSeenMs: dd.LastSeenMs,
		Meta:       meta,
		MetaRev:    dd.MetaRev,
		MetaBroken: dd.MetaBroken,
	}
}

func (e *Engine) refreshSessionPending(s *SessionEntry) {
	if s == nil {
		return
	}
	var agent v1.Agent
	if s.Info != nil {
		agent = s.Info.Agent
	}
	var ps *corev1.ProjectionState
	if buf := e.buffers[s.ID]; buf != nil && len(buf.prefix) > 0 {
		if buf.reducer == nil {
			buf.rebuildReducer(agent)
		}
		if buf.cachedSnap == nil {
			buf.cachedSnap = buf.reducer.Snapshot()
		}
		if s.Checkpoint != nil {
			ps = projection.CurrentActions(buf.cachedSnap, s.Checkpoint, buf.prefix)
		} else {
			ps = buf.cachedSnap
		}
	} else if s.Checkpoint != nil {
		ps = projection.CurrentActions(nil, s.Checkpoint, nil)
	}
	s.Pending = pendingFromProjection(ps, s.Info)
}

func pendingFromProjection(ps *corev1.ProjectionState, meta *v1.Session) lists.PendingAction {
	var p lists.PendingAction
	if meta != nil && meta.Status == v1.SessionStatus_SESSION_STATUS_BLOCKED {
		p.HasPopup = true
		p.PopupPreview = meta.StatusDetail
	}
	if ps == nil {
		return p
	}
	if len(ps.Approvals) > 0 && ps.Approvals[0] != nil {
		pa := ps.Approvals[0]
		p.HasApproval = true
		p.ApprovalID = pa.ApprovalId
		if req := pa.Request; req != nil {
			p.ApprovalToolName = req.ToolName
			p.ApprovalCommand = req.Command
			p.ApprovalToolInput = req.ToolInputJson
			p.ApprovalTruncated = lists.ApprovalTruncated(req)
			if req.Deadline != nil {
				p.ApprovalDeadlineMs = req.Deadline.Seconds*1000 + int64(req.Deadline.Nanos)/1_000_000
			}
		}
	}
	if len(ps.Questions) > 0 {
		p.HasQuestion = true
		if pq := ps.Questions[0]; pq != nil {
			qData := &lists.PendingQuestionData{
				QuestionID:       pq.QuestionId,
				CanFreeText:      meta != nil && meta.Agent == v1.Agent_AGENT_CODEX,
				ObservedEventSeq: pq.Seq,
				AskedAtMs:        pq.AskedAtMs,
			}
			if req := pq.Request; req != nil {
				qData.Async = req.Async
				qData.QuestionCount = int32(len(req.Questions))
				if len(req.Questions) > 0 && req.Questions[0] != nil {
					q0 := req.Questions[0]
					key := q0.Id
					if key == "" {
						key = q0.Text
					}
					qData.QuestionKey = key
					qData.Header = q0.Header
					qData.Text = q0.Text
					qData.MultiSelect = q0.MultiSelect
					if len(q0.Options) > 0 {
						qData.Options = make([]lists.PendingQuestionOption, 0, len(q0.Options))
						for _, opt := range q0.Options {
							if opt == nil {
								continue
							}
							qData.Options = append(qData.Options, lists.PendingQuestionOption{
								Label:       opt.Label,
								Description: opt.Description,
							})
						}
					}
				}
			}
			p.Question = qData
		}
	}
	if ps.Plan != nil {
		p.HasPlan = true
	}
	return p
}

func (e *Engine) isTrustedMachine(machineID string) bool {
	if !e.security.Loaded {
		return true
	}
	return e.security.TrustedMachines[machineID] || e.security.ArchiveMachines[machineID]
}

func (e *Engine) deviceNeedsSecuritySync(d *corev1.PlainDevice) bool {
	if d == nil {
		return false
	}
	return e.deviceKindNeedsSecuritySync(d.DeviceId, d.Kind)
}

func (e *Engine) deviceKindNeedsSecuritySync(deviceID string, kind v1.DeviceKind) bool {
	if !e.security.Loaded || deviceID == "" {
		return false
	}
	switch kind {
	case v1.DeviceKind_DEVICE_KIND_MACHINE:
		return !e.security.TrustedMachines[deviceID]
	case v1.DeviceKind_DEVICE_KIND_CLIENT:
		return !e.security.TrustedClients[deviceID]
	default:
		return !e.security.TrustedMachines[deviceID] && !e.security.TrustedClients[deviceID]
	}
}
