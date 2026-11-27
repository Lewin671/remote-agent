package sync

import (
	"maps"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/projection"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
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

	var wireMachines []*v1.Machine
	for _, raw := range ev.RawMachines {
		if len(raw) == 0 {
			continue
		}
		var m v1.Machine
		if err := m.Unmarshal(raw); err == nil {
			wireMachines = append(wireMachines, &m)
		}
	}
	var wireSessions []*v1.RemoteSession
	for _, raw := range ev.RawSessions {
		if len(raw) == 0 {
			continue
		}
		var s v1.RemoteSession
		if err := s.Unmarshal(raw); err == nil {
			wireSessions = append(wireSessions, &s)
		}
	}
	var prefsSlice []*v1.AccountPrefsObject
	if len(ev.RawPrefs) > 0 {
		var p v1.AccountPrefsObject
		if err := p.Unmarshal(ev.RawPrefs); err == nil {
			prefsSlice = []*v1.AccountPrefsObject{&p}
		}
	}
	e.openWireDirectoryBatchTag(ctx, wireMachines, wireSessions, nil, prefsSlice, dirBatchMeta{kind: dirKindBootstrap})
}

func (e *Engine) startRefresh(ctx *StepContext) {
	if !e.running {
		return
	}
	if e.demoMode {
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LIST_RETRY, "")
		if e.demoListError != "" {
			e.w.SessionListError = world.PlainReason(e.demoListError)
			e.w.World.SessionListError = e.demoListError
		} else {
			e.w.World.MachinesListed = true
			e.w.World.SessionsListed = true
			e.w.World.LiveLoaded = true
			e.w.SessionListError = world.Reason{}
			e.w.World.SessionListError = ""
		}
		ctx.Dirty.Add(true, true, false)
		return
	}
	e.refreshRun++
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LIST_RETRY, "")
	e.refreshOpID = e.allocOpID()
	e.refreshSnapshotM = maps.Clone(e.machinePushes)
	e.refreshSnapshotS = maps.Clone(e.sessionPushes)
	e.refreshSnapshotR = maps.Clone(e.removedPushes)
	e.refreshSnapshotD = maps.Clone(e.devicePushes)

	e.startRefreshViaRelayPipe(ctx)
}

func (e *Engine) startRefreshDevices(ctx *StepContext, force bool) {
	if (!force && !e.running) || e.demoMode {
		return
	}
	e.devicesOpID = e.allocOpID()
	e.devicesSnapshotR = maps.Clone(e.removedPushes)
	e.devicesSnapshotD = maps.Clone(e.devicePushes)
	e.emitRelayRpc(
		ctx,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_DEVICES,
		e.cachedToken,
		relayRpcDefaultTimeoutMs,
		"",
		(&v1.ListDevicesRequest{}).AppendMarshal(nil),
		&pendingRelayRpc{kind: relayRpcDevices, opID: e.devicesOpID},
	)
}

// RequestMoreSessions asks for the next page of ended sessions (if connected and more remain).
func (e *Engine) RequestMoreSessions(ctx *StepContext) {
	if !e.w.World.SessionsListed {
		if e.running && e.w.LivePushConnected && e.refreshOpID == 0 {
			if e.demoMode {
				e.demoListError = ""
			}
			e.startRefresh(ctx)
		}
		return
	}
	if e.pageOpID != 0 || e.w.World.SessionsNext == nil || !e.running || e.w.World.Connection != lists.ConnConnected {
		if e.w.World.Connection != lists.ConnConnected && e.w.World.SessionsNext != nil {
			e.w.SessionListError = world.Reason{Kind: world.ReasonNoServer}
			ctx.Dirty.Add(true, true, false)
		}
		return
	}
	if e.demoMode {
		e.w.World.SessionsNext = nil
		e.w.World.HasMoreSessions = false
		e.w.World.LoadingMoreSessions = false
		ctx.Dirty.Add(true, true, false)
		return
	}
	e.w.World.LoadingMoreSessions = true
	e.w.SessionListError = world.Reason{}
	e.w.World.SessionListError = ""
	e.pageOpID = e.allocOpID()
	e.pageListingGen = e.listingGen
	after := &v1.SessionCursor{
		EndedMs:   e.w.World.SessionsNext.EndedMs,
		SessionId: e.w.World.SessionsNext.SessionID,
	}
	e.emitRelayRpc(
		ctx,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS,
		e.cachedToken,
		relayRpcDefaultTimeoutMs,
		"",
		(&v1.ListRemoteSessionsRequest{After: after, Limit: SessionPageLimit}).AppendMarshal(nil),
		&pendingRelayRpc{kind: relayRpcSessionsPage, opID: e.pageOpID},
	)
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
	if sessionID == "" || !e.running || e.demoMode {
		return
	}
	delete(e.sessionByIDFailed, sessionID)
	delete(e.sessionByIDErrors, sessionID)
	opID := e.allocOpID()
	e.sessionByIDOps[opID] = sessionID
	e.emitRelayRpc(
		ctx,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS,
		e.cachedToken,
		relayRpcDefaultTimeoutMs,
		"",
		(&v1.ListRemoteSessionsRequest{SessionIds: []string{sessionID}, Limit: 1}).AppendMarshal(nil),
		&pendingRelayRpc{kind: relayRpcSessionByID, opID: opID, sid: sessionID},
	)
}

func (e *Engine) handleListsFetched(ctx *StepContext, ev listsFetchedResult) {
	if ev.OpID != e.refreshOpID || !e.running {
		return
	}
	e.refreshOpID = 0
	if ev.Error != "" {
		if ev.UnresolvedHost {
			e.w.SessionListError = world.Reason{Kind: world.ReasonNoServer}
		} else {
			e.w.SessionListError = world.PlainReason(ev.Error)
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

	mp, sp, rp, dp := e.cloneRefreshSnapshots()
	e.openWireDirectoryBatchTag(ctx, ev.WireMachines, ev.WireSessions, ev.WireDevices, nil, dirBatchMeta{
		kind: dirKindListsFetched, refreshRun: e.refreshRun, evNext: ev.Next, mp: mp, sp: sp, rp: rp, dp: dp,
	})
}

func (e *Engine) handleSessionsPageFetched(ctx *StepContext, ev sessionsPageResult) {
	if ev.OpID != e.pageOpID || !e.running {
		return
	}
	e.pageOpID = 0
	e.w.World.LoadingMoreSessions = false
	if e.pageListingGen != e.listingGen {
		return
	}
	if ev.Error != "" {
		e.w.SessionListError = world.PlainReason(ev.Error)
		e.w.World.SessionListError = ev.Error
		ctx.Dirty.Add(true, true, false)
		return
	}
	e.openWireDirectoryBatchTag(ctx, nil, ev.WireSessions, nil, nil, dirBatchMeta{
		kind: dirKindSessionsPage, listingGen: e.listingGen, evNext: ev.Next,
	})
}

func (e *Engine) handleSessionByIDFetched(ctx *StepContext, ev sessionByIDResult) {
	sid, ok := e.sessionByIDOps[ev.OpID]
	if !ok {
		return
	}
	delete(e.sessionByIDOps, ev.OpID)
	if ev.Error != "" || ev.WireSession == nil || ev.WireSession.SessionId == "" {
		if sid != "" {
			e.sessionByIDFailed[sid] = true
			if ev.Error != "" {
				e.sessionByIDErrors[sid] = world.PlainReason(ev.Error)
			} else {
				delete(e.sessionByIDErrors, sid)
			}
		}
		ctx.Dirty.Add(false, true, false)
		ctx.Dirty.MarkSessionData()
		return
	}
	e.openWireDirectoryBatchTag(ctx, nil, []*v1.RemoteSession{ev.WireSession}, nil, nil, dirBatchMeta{
		kind: dirKindSessionByID, sid: sid,
	})
}

func (e *Engine) handleDevicesFetched(ctx *StepContext, ev devicesFetchedResult) {
	if ev.OpID == 0 || ev.OpID != e.devicesOpID {
		return
	}
	e.devicesOpID = 0
	removedSnapshot := e.devicesSnapshotR
	deviceSnapshot := e.devicesSnapshotD
	if ev.Error != "" {
		e.devicesLoadFailed = true
		e.devicesError = world.PlainReason(ev.Error)
		ctx.Dirty.MarkSyncState()
		return
	}
	e.devicesApplyGen++
	e.openWireDirectoryBatchTag(ctx, nil, nil, ev.WireDevices, nil, dirBatchMeta{
		kind: dirKindDevices, devGen: e.devicesApplyGen, mp: removedSnapshot, sp: deviceSnapshot,
	})
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
	if writeCache && dm.Wire != nil {
		if raw, err := dm.Wire.Marshal(); err == nil && len(raw) > 0 {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheWrite{
					CacheWrite: &corev1.CacheWriteEffect{
						Kind: &corev1.CacheWriteEffect_RawWriteMachine{RawWriteMachine: raw},
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

func (e *Engine) putSessionDecoded(ctx *StepContext, ds decodedSession, writeCache bool) {
	if ds.SessionID == "" {
		return
	}
	delete(e.sessionByIDFailed, ds.SessionID)
	delete(e.sessionByIDErrors, ds.SessionID)
	prev := e.sessions[ds.SessionID]
	entry := decodedToSessionEntry(ds, prev)
	e.refreshSessionPending(entry)
	e.storeSession(entry)

	if ds.OpenFailed && ds.MachineID != "" && e.isTrustedMachine(ds.MachineID) {
		if ds.Wire != nil {
			if raw, err := ds.Wire.Marshal(); err == nil && len(raw) > 0 {
				ctx.emit(&corev1.Effect{
					Effect: &corev1.Effect_CacheWrite{
						CacheWrite: &corev1.CacheWriteEffect{
							Kind: &corev1.CacheWriteEffect_RawKeepSessionOwner{RawKeepSessionOwner: raw},
						},
					},
				})
			}
		}
	} else if writeCache && ds.Wire != nil {
		if raw, err := ds.Wire.Marshal(); err == nil && len(raw) > 0 {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheWrite{
					CacheWrite: &corev1.CacheWriteEffect{
						Kind: &corev1.CacheWriteEffect_RawWriteSession{RawWriteSession: raw},
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
	e.putSessionDecoded(ctx, decodedSession{
		SessionID: meta.Id,
		MachineID: machineID,
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
	if e.secEngine != nil {
		e.secEngine.SetDeviceDirectory(e.securityDeviceOffers())
	}
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
	}
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
		Registered: dd.Registered,
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
	var ps *projection.State
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

func pendingFromProjection(ps *projection.State, meta *v1.Session) lists.PendingAction {
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
			p.ApprovalDescription = req.Description
			p.ApprovalTruncated = lists.ApprovalTruncated(req)
			p.ApprovalSkeleton = lists.IsApprovalSkeleton(req)
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
