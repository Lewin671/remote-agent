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
	if ev.HomeScopeToken != "" && e.homeScopeToken == "" {
		e.homeScopeToken = ev.HomeScopeToken
	}
	if len(ev.ScopeHmacKey) > 0 && len(e.scopeHMACKey) == 0 {
		e.scopeHMACKey = append([]byte(nil), ev.ScopeHmacKey...)
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

	e.reconcileDevices(ev.Devices, rp, false)
	e.w.World.SessionsNext = pageNext
	e.w.World.HasMoreSessions = pageNext != nil

	e.updateLivePollers(ctx)
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
	var removedSnapshot map[string]uint64
	switch {
	case ev.OpId != 0:
		if ev.OpId != e.devicesOpID {
			return
		}
		e.devicesOpID = 0
		removedSnapshot = e.devicesSnapshotR
	case ev.PushSeq != 0:
		if ev.PushSeq <= e.lastDevicesPushSeq {
			return
		}
		e.lastDevicesPushSeq = ev.PushSeq
		removedSnapshot = nil
	default:
		return
	}
	if ev.Error != "" {
		return
	}
	e.reconcileDevices(ev.Devices, removedSnapshot, true)
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
	}
}

func (e *Engine) handleMachineRouteStatus(ctx *StepContext, ev *corev1.MachineRouteStatusEvent) {
	if ev == nil || ev.MachineId == "" {
		return
	}
	if e.directReady[ev.MachineId] == ev.DirectReady {
		return
	}
	e.directReady[ev.MachineId] = ev.DirectReady
	e.updateLivePollers(ctx)
	ctx.Dirty.MarkAll()
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
	cur := e.machines[pm.MachineId]
	entry := plainToMachineEntry(pm)
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
	if writeCache && pm.CacheRef != "" {
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_CacheWrite{
				CacheWrite: &corev1.CacheWriteEffect{
					Kind: &corev1.CacheWriteEffect_WriteMachineRef{WriteMachineRef: pm.CacheRef},
				},
			},
		})
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
	delete(e.sessionByIDFailed, ps.SessionId)
	prev := e.sessions[ps.SessionId]
	entry := plainToSessionEntry(ps, prev)
	e.refreshSessionPending(entry)
	e.storeSession(entry)

	if ps.OpenFailed && ps.MachineId != "" && ps.CacheRef != "" && e.isTrustedMachine(ps.MachineId) {
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_CacheWrite{
				CacheWrite: &corev1.CacheWriteEffect{
					Kind: &corev1.CacheWriteEffect_KeepSessionOwnerRef{KeepSessionOwnerRef: ps.CacheRef},
				},
			},
		})
	} else if writeCache && ps.CacheRef != "" {
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_CacheWrite{
				CacheWrite: &corev1.CacheWriteEffect{
					Kind: &corev1.CacheWriteEffect_WriteSessionRef{WriteSessionRef: ps.CacheRef},
				},
			},
		})
	}
	if ps.RetiredHistory {
		e.markSessionRetired(ps.SessionId)
	}
	if ps.OpenFailed {
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_CacheWrite{
				CacheWrite: &corev1.CacheWriteEffect{
					Kind: &corev1.CacheWriteEffect_SetSessionOpen{
						SetSessionOpen: &corev1.CacheSessionOpen{SessionId: ps.SessionId, Open: false},
					},
				},
			},
		})
	}
	if e.watchedSessions[ps.SessionId] && e.buffers[ps.SessionId] == nil {
		e.startLoadEvents(ctx, ps.SessionId)
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

func (e *Engine) reconcileDevices(items []*corev1.PlainDevice, removedSnapshot map[string]uint64, allowEmpty bool) {
	if len(items) == 0 && !allowEmpty {
		return
	}
	next := make(map[string]*DeviceEntry, len(items))
	for _, d := range items {
		if d == nil || d.DeviceId == "" || pushedSince(e.removedPushes, removedSnapshot, d.DeviceId) {
			continue
		}
		next[d.DeviceId] = plainToDeviceEntry(d)
	}
	e.devices = next
}

func plainToMachineEntry(pm *corev1.PlainMachine) *MachineEntry {
	var meta *v1.MachineMeta
	if pm.Meta != nil {
		meta = pm.Meta.Clone()
	}
	return &MachineEntry{
		Machine: lists.Machine{
			ID:         pm.MachineId,
			Online:     pm.Online,
			LastSeenMs: pm.LastSeenMs,
			Meta:       meta,
		},
		MetaRev:    pm.MetaRev,
		MetaBroken: pm.MetaBroken,
		CacheRef:   pm.CacheRef,
	}
}

func plainToSessionEntry(ps *corev1.PlainSession, prev *SessionEntry) *SessionEntry {
	entry := &SessionEntry{
		Session: lists.Session{
			ID:            ps.SessionId,
			MachineID:     ps.MachineId,
			UpdatedMs:     ps.UpdatedMs,
			ListedEndedMs: ps.ListedEndedMs,
		},
		LastSeq:           ps.LastSeq,
		ServerLastSeq:     ps.LastSeq,
		MetaRev:           ps.MetaRev,
		CheckpointInvalid: ps.CheckpointInvalid,
		RetiredHistory:    ps.RetiredHistory,
		OpenFailed:        ps.OpenFailed,
		CacheRef:          ps.CacheRef,
	}
	if ps.Meta != nil {
		entry.setInfo(ps.Meta.Clone())
	}
	if ps.Checkpoint != nil {
		entry.Checkpoint = ps.Checkpoint.Clone()
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
		if entry.Checkpoint == nil && !ps.CheckpointInvalid {
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
	var meta *v1.DeviceMeta
	if pd.Meta != nil {
		meta = pd.Meta.Clone()
	}
	return &DeviceEntry{
		ID:         pd.DeviceId,
		Kind:       pd.Kind,
		CreatedMs:  pd.CreatedMs,
		LastSeenMs: pd.LastSeenMs,
		Meta:       meta,
		MetaRev:    pd.MetaRev,
		MetaBroken: pd.MetaBroken,
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
	if !e.security.Loaded || d == nil || d.DeviceId == "" {
		return false
	}
	switch d.Kind {
	case v1.DeviceKind_DEVICE_KIND_MACHINE:
		return !e.security.TrustedMachines[d.DeviceId]
	case v1.DeviceKind_DEVICE_KIND_CLIENT:
		return !e.security.TrustedClients[d.DeviceId]
	default:
		return !e.security.TrustedMachines[d.DeviceId] && !e.security.TrustedClients[d.DeviceId]
	}
}
