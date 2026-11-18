package sync

import (
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/projection"
)

func protoConnState(c lists.ConnectionKind) corev1.ConnState {
	switch c {
	case lists.ConnConnected:
		return corev1.ConnState_CONN_STATE_CONNECTED
	case lists.ConnReconnecting:
		return corev1.ConnState_CONN_STATE_RECONNECTING
	case lists.ConnConnecting:
		return corev1.ConnState_CONN_STATE_CONNECTING
	case lists.ConnOffline:
		return corev1.ConnState_CONN_STATE_OFFLINE
	case lists.ConnNoNetwork:
		return corev1.ConnState_CONN_STATE_NO_NETWORK
	default:
		return corev1.ConnState_CONN_STATE_UNSPECIFIED
	}
}

func syncPendingFromLists(p lists.PendingAction) *corev1.SyncPendingAction {
	return &corev1.SyncPendingAction{
		HasApproval:        p.HasApproval,
		ApprovalId:         p.ApprovalID,
		ApprovalToolName:   p.ApprovalToolName,
		ApprovalCommand:    p.ApprovalCommand,
		ApprovalToolInput:  p.ApprovalToolInput,
		ApprovalTruncated:  p.ApprovalTruncated,
		ApprovalDeadlineMs: p.ApprovalDeadlineMs,
		HasQuestion:        p.HasQuestion,
		HasPlan:            p.HasPlan,
		HasPopup:           p.HasPopup,
		PopupPreview:       p.PopupPreview,
	}
}

func (e *Engine) buildSyncMachineView(m *MachineEntry, unlinked bool) *corev1.SyncMachineView {
	if m == nil {
		return nil
	}
	directReady := e.directReady[m.ID]
	reachable := e.machineReachable(m)
	mUnlinked := unlinked && !directReady
	var pv uint32
	if m.Meta != nil {
		pv = m.Meta.ProtocolVersion
	}
	return &corev1.SyncMachineView{
		MachineId:       m.ID,
		Online:          m.Online,
		LastSeenMs:      m.LastSeenMs,
		Meta:            m.Meta.Clone(),
		MetaRev:         m.MetaRev,
		MetaBroken:      m.MetaBroken,
		Reachable:       reachable,
		Unlinked:        mUnlinked,
		DirectReady:     directReady,
		ProtocolVersion: pv,
		DaemonOutOfDate: pv > 0 && pv < ProtocolVersion,
		ClientOutOfDate: pv > 0 && pv > ProtocolVersion,
	}
}

func (e *Engine) buildSyncSessionView(s *SessionEntry) *corev1.SyncSessionView {
	if s == nil {
		return nil
	}
	var createdMs int64
	if s.Meta != nil && s.Meta.CreatedAt != nil {
		createdMs = s.Meta.CreatedAt.Seconds*1000 + int64(s.Meta.CreatedAt.Nanos)/1_000_000
	}
	return &corev1.SyncSessionView{
		SessionId:         s.ID,
		MachineId:         s.MachineID,
		Info:              s.Meta.Clone(),
		MetaRev:           s.MetaRev,
		LastSeq:           s.LastSeq,
		CreatedMs:         createdMs,
		UpdatedMs:         s.UpdatedMs,
		ListedEndedMs:     s.ListedEndedMs,
		Listed:            e.listed[s.ID],
		Checkpoint:        s.Checkpoint.Clone(),
		CheckpointInvalid: s.CheckpointInvalid,
		RetiredHistory:    s.RetiredHistory,
		OpenFailed:        s.OpenFailed,
		ScheduleName:      s.ScheduleName,
		ScheduleUnread:    s.ScheduleUnread,
		Pending:           syncPendingFromLists(s.LivePending),
	}
}

func (e *Engine) buildSyncDeviceView(d *DeviceEntry) *corev1.SyncDeviceView {
	if d == nil {
		return nil
	}
	var trusted bool
	if e.security.Loaded {
		switch d.Kind {
		case v1.DeviceKind_DEVICE_KIND_MACHINE:
			trusted = e.security.TrustedMachines[d.ID]
		case v1.DeviceKind_DEVICE_KIND_CLIENT:
			trusted = e.security.TrustedClients[d.ID]
		}
	}
	return &corev1.SyncDeviceView{
		DeviceId:   d.ID,
		Kind:       d.Kind,
		CreatedMs:  d.CreatedMs,
		LastSeenMs: d.LastSeenMs,
		Meta:       d.Meta.Clone(),
		MetaRev:    d.MetaRev,
		MetaBroken: d.MetaBroken,
		Trusted:    trusted,
	}
}

// BuildSyncStateView constructs the transitional SyncStateView snapshot for unmigrated Step 3-4 screens.
func (e *Engine) BuildSyncStateView() *corev1.SyncStateView {
	displayGrace := e.displayGraceActive()
	unlinked := e.worldUnlinked()

	machines := make([]*corev1.SyncMachineView, 0, len(e.machines))
	for _, m := range e.machines {
		if mv := e.buildSyncMachineView(m, unlinked); mv != nil {
			machines = append(machines, mv)
		}
	}
	slices.SortFunc(machines, func(a, b *corev1.SyncMachineView) int {
		if a.MachineId < b.MachineId {
			return -1
		}
		if a.MachineId > b.MachineId {
			return 1
		}
		return 0
	})

	sessions := make([]*corev1.SyncSessionView, 0, len(e.sessions))
	for _, s := range e.sessions {
		if sv := e.buildSyncSessionView(s); sv != nil {
			sessions = append(sessions, sv)
		}
	}
	slices.SortFunc(sessions, func(a, b *corev1.SyncSessionView) int {
		if a.SessionId < b.SessionId {
			return -1
		}
		if a.SessionId > b.SessionId {
			return 1
		}
		return 0
	})

	devices := make([]*corev1.SyncDeviceView, 0, len(e.devices))
	for _, d := range e.devices {
		if dv := e.buildSyncDeviceView(d); dv != nil {
			devices = append(devices, dv)
		}
	}
	slices.SortFunc(devices, func(a, b *corev1.SyncDeviceView) int {
		if a.DeviceId < b.DeviceId {
			return -1
		}
		if a.DeviceId > b.DeviceId {
			return 1
		}
		return 0
	})

	var nextCursor *v1.SessionCursor
	if e.sessionsNext != nil {
		nextCursor = &v1.SessionCursor{
			EndedMs:   e.sessionsNext.EndedMs,
			SessionId: e.sessionsNext.SessionID,
		}
	}

	var prefsCopy *v1.ProjectPrefs
	if e.prefs.Prefs != nil {
		prefsCopy = e.prefs.Prefs.Clone()
	}

	return &corev1.SyncStateView{
		Connection:             protoConnState(e.conn),
		ConnectionReason:       e.connReasonText,
		ConnectionStep:         e.connStepText,
		DisconnectedAtMs:       e.offlineSinceMs,
		LivePushConnected:      e.streamOpen,
		Unlinked:               unlinked,
		IsManager:              e.isManager(),
		SelfDeviceId:           e.security.SelfDeviceID,
		AuthorityDeviceId:      e.security.AuthorityDeviceID,
		ManagerName:            e.managerNameText(),
		AccountControlLost:     e.accountControlLost(),
		AccountClockSkewed:     e.accountClockSkewed(),
		Removed:                e.removed,
		SecurityLoaded:         e.security.Loaded,
		IsMember:               e.security.IsMember,
		LeaseEndMs:             e.security.LeaseEndMs,
		RecoveryPending:        e.security.RecoveryPending,
		RecoveryReadyMs:        e.security.RecoveryReadyMs,
		RecoveryObjectsPending: e.security.RecoveryObjectsPending,
		ClientProtocolVersion:  ProtocolVersion,
		Machines:               machines,
		MachinesListed:         e.machinesListed,
		Sessions:               sessions,
		SessionsListed:         e.sessionsListed,
		LiveLoaded:             e.liveLoaded,
		HasMoreSessions:        e.hasMoreSessions,
		NextCursor:             nextCursor,
		LoadingMoreSessions:    e.loadingMore,
		SessionListFailed:      e.sessionListErr != nil,
		SessionListError:       e.sessionListErr,
		PrefsLoaded:            e.prefs.Loaded,
		PrefsLoadFailed:        e.prefs.LoadFailed,
		PrefsBroken:            e.prefs.Broken,
		PrefsRevision:          e.prefs.Revision,
		PrefsSyncing:           len(e.prefsQueue) > 0,
		Prefs:                  prefsCopy,
		Devices:                devices,
		DisplayGrace:           displayGrace,
	}
}

// BuildSessionDataView constructs the transitional SessionDataView for sessionID.
func (e *Engine) BuildSessionDataView(sessionID string) *corev1.SessionDataView {
	s := e.sessions[sessionID]
	buf := e.buffers[sessionID]
	if s == nil && buf == nil {
		return &corev1.SessionDataView{
			SessionId: sessionID,
			Found:     false,
		}
	}

	out := &corev1.SessionDataView{
		SessionId: sessionID,
		Found:     true,
		Session:   e.buildSyncSessionView(s),
	}

	var meta *v1.Session
	var liveSnap *v1.LiveSnapshot
	var agent v1.Agent
	if s != nil {
		out.LastSeq = s.LastSeq
		out.Retired = s.RetiredHistory
		meta = s.Meta
		if s.Meta != nil {
			agent = s.Meta.Agent
		}
		if s.Checkpoint != nil {
			liveSnap = s.Checkpoint.Clone()
			out.LiveSnapshot = liveSnap
		}
	}

	var ps *corev1.ProjectionState
	if buf != nil {
		out.Loading = buf.awaitingCache || buf.fetching
		out.Loaded = buf.loaded
		out.AwaitingCache = buf.awaitingCache
		out.Backfilling = buf.backfilling
		out.Fetching = buf.fetching
		out.HasGap = (s != nil && buf.hasServerGap(s.ServerLastSeq)) || (s == nil && len(buf.pending) > 0)
		out.TailRequested = buf.tailRequested
		out.Retired = out.Retired || buf.retired
		out.RetryingCorruptCache = buf.retryingCorruptCache
		out.Error = buf.errorText
		out.FetchError = buf.fetchErrorText
		out.PrefixLen = uint64(len(buf.prefix))
		out.PendingCount = int32(len(buf.pending))

		if len(buf.unavailableSeqs) > 0 {
			seqs := make([]uint64, 0, len(buf.unavailableSeqs))
			for seq := range buf.unavailableSeqs {
				seqs = append(seqs, seq)
			}
			slices.Sort(seqs)
			out.UnavailableSeqs = seqs
			out.UnavailableCount = uint32(len(seqs))
		}

		if len(buf.prefix) > 0 {
			if buf.reducer == nil {
				buf.rebuildReducer(agent)
			}
			if liveSnap != nil {
				ps = buf.reducer.CurrentActions(liveSnap)
			} else {
				ps = buf.reducer.Snapshot()
			}
		} else if liveSnap != nil {
			ps = projection.CurrentActions(nil, liveSnap, nil)
		}

		shown := buf.ShownEvents()
		if len(shown) > 0 {
			events := make([]*v1.Event, 0, len(shown))
			for _, ev := range shown {
				if ev != nil {
					events = append(events, ev.Clone())
				}
			}
			out.Events = events
		}
	} else if liveSnap != nil {
		ps = projection.CurrentActions(nil, liveSnap, nil)
	}

	out.Projection = ps
	out.Pending = syncPendingFromLists(pendingFromProjection(ps, meta))
	return out
}
