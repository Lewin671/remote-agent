package sync

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/projection"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
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
	var q *corev1.SyncPendingQuestion
	if p.Question != nil {
		opts := make([]*corev1.SyncPendingQuestionOption, 0, len(p.Question.Options))
		for _, opt := range p.Question.Options {
			opts = append(opts, &corev1.SyncPendingQuestionOption{
				Label:       opt.Label,
				Description: opt.Description,
			})
		}
		q = &corev1.SyncPendingQuestion{
			QuestionId:       p.Question.QuestionID,
			QuestionKey:      p.Question.QuestionKey,
			Header:           p.Question.Header,
			Text:             p.Question.Text,
			MultiSelect:      p.Question.MultiSelect,
			QuestionCount:    p.Question.QuestionCount,
			Options:          opts,
			CanFreeText:      p.Question.CanFreeText,
			Async:            p.Question.Async,
			ObservedEventSeq: p.Question.ObservedEventSeq,
			AskedAtMs:        p.Question.AskedAtMs,
		}
	}
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
		Question:           q,
	}
}

func (e *Engine) machineTerminalReady(machineID string) bool {
	return e.w.World.Connection == lists.ConnConnected || (machineID != "" && e.directReady[machineID])
}

func (e *Engine) buildSyncMachineView(m *MachineEntry) *corev1.SyncMachineView {
	if m == nil {
		return nil
	}
	return &corev1.SyncMachineView{
		MachineId:           m.ID,
		Online:              m.Online,
		LastSeenMs:          m.LastSeenMs,
		Meta:                m.Meta.Clone(),
		MetaRev:             m.MetaRev,
		MetaBroken:          m.MetaBroken,
		Reachable:           m.reachable,
		Unlinked:            m.unlinked,
		DirectReady:         e.directReady[m.ID],
		ProtocolVersion:     m.ProtocolVersion,
		DaemonOutOfDate:     m.DaemonOutOfDate,
		ClientOutOfDate:     m.ClientOutOfDate,
		ReachabilityUnknown: e.machineReachabilityUnknown(m),
		TerminalReady:       e.machineTerminalReady(m.ID),
		DisplayName:         lists.MachineName(&lists.Machine{ID: m.ID, Meta: m.Meta}, m.ID),
	}
}

func (e *Engine) buildSyncSessionView(s *SessionEntry) *corev1.SyncSessionView {
	if s == nil {
		return nil
	}
	var createdMs int64
	if s.Info != nil && s.Info.CreatedAt != nil {
		createdMs = s.Info.CreatedAt.Seconds*1000 + int64(s.Info.CreatedAt.Nanos)/1_000_000
	}
	termReady := e.machineTerminalReady(s.MachineID)
	termWaiting := !termReady && (e.w.ReachabilityUnknown || e.w.DisplayGrace)
	return &corev1.SyncSessionView{
		SessionId:         s.ID,
		MachineId:         s.MachineID,
		Info:              s.Info.Clone(),
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
		Pending:           syncPendingFromLists(s.Pending),
		TerminalReady:     termReady,
		TerminalWaiting:   termWaiting,
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

type syncDevicesByID []*corev1.SyncDeviceView

func (s syncDevicesByID) Len() int           { return len(s) }
func (s syncDevicesByID) Less(i, j int) bool { return s[i].DeviceId < s[j].DeviceId }
func (s syncDevicesByID) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func (e *Engine) buildConnBanner(w *world.State) *corev1.ConnBannerView {
	var kind corev1.ConnBannerKind
	switch {
	case !e.networkAvailable:
		kind = corev1.ConnBannerKind_CONN_BANNER_KIND_NO_NETWORK
	case e.removed || w.World.Connection == lists.ConnOffline || (e.unlinkedFlag && !w.ReachabilityUnknown):
		kind = corev1.ConnBannerKind_CONN_BANNER_KIND_NO_SERVER
	case w.World.Connection == lists.ConnConnected:
		kind = corev1.ConnBannerKind_CONN_BANNER_KIND_NONE
	default:
		kind = corev1.ConnBannerKind_CONN_BANNER_KIND_WAITING
	}
	if kind == corev1.ConnBannerKind_CONN_BANNER_KIND_NONE || w.DisplayGrace {
		return nil
	}
	kept := (w.World.MachinesListed && w.World.SessionsListed) || len(e.sessions) > 0
	out := &corev1.ConnBannerView{
		Kind:     kind,
		Visible:  true,
		KeptData: kept,
	}
	switch kind {
	case corev1.ConnBannerKind_CONN_BANNER_KIND_NO_NETWORK:
		if kept {
			out.Text = text.Msg(text.KeyConnectionNoNetworkPrevious)
		} else {
			out.Text = text.Msg(text.KeyConnectionNoNetworkEmpty)
		}
	case corev1.ConnBannerKind_CONN_BANNER_KIND_NO_SERVER:
		out.ErrorTone = !kept
		out.Text = text.Msg(text.KeyConnectionServerRetrying)
		reason := w.ConnectionReasonText
		if text.IsEmpty(reason) {
			reason = text.Msg(text.KeyConnectionNoResponse)
		}
		out.DetailText = text.Msg(text.KeyConnectionServerDetails, text.ArgText("reason", reason))
	case corev1.ConnBannerKind_CONN_BANNER_KIND_WAITING:
		if w.World.LiveLoaded {
			out.Text = text.Msg(text.KeyConnectionReconnecting)
		} else {
			out.Text = text.Msg(text.KeyConnectionConnecting)
		}
	}
	return out
}

func (e *Engine) buildLeaseRenewalAdvice(w *world.State) *corev1.Text {
	if !e.security.Loaded {
		return nil
	}
	if w.World.IsManager {
		return text.Msg(text.KeySecurityRenewNow)
	}
	if !text.IsEmpty(w.ManagerNameText) {
		return text.Msg(text.KeySecurityOpenToRenew, text.ArgText("value", w.ManagerNameText))
	}
	return nil
}

// BuildSyncStateView constructs the transitional SyncStateView snapshot for unmigrated Step 3-4 screens
// from the engine's world.
func (e *Engine) BuildSyncStateView() *corev1.SyncStateView {
	w := &e.w
	machines := make([]*corev1.SyncMachineView, 0, len(w.World.Machines))
	for _, m := range w.World.Machines {
		if mv := e.buildSyncMachineView(e.machines[m.ID]); mv != nil {
			machines = append(machines, mv)
		}
	}
	sessions := make([]*corev1.SyncSessionView, 0, len(w.World.Sessions))
	for _, s := range w.World.Sessions {
		if sv := e.buildSyncSessionView(e.sessions[s.ID]); sv != nil {
			sessions = append(sessions, sv)
		}
	}

	devices := make([]*corev1.SyncDeviceView, 0, len(e.devices))
	for _, d := range e.devices {
		if dv := e.buildSyncDeviceView(d); dv != nil {
			devices = append(devices, dv)
		}
	}
	sortutil.SortInterface(syncDevicesByID(devices))

	var nextCursor *v1.SessionCursor
	if w.World.SessionsNext != nil {
		nextCursor = &v1.SessionCursor{
			EndedMs:   w.World.SessionsNext.EndedMs,
			SessionId: w.World.SessionsNext.SessionID,
		}
	}

	var prefsCopy *v1.ProjectPrefs
	if w.World.Prefs != nil {
		prefsCopy = w.World.Prefs.Clone()
	}

	var trustedMachineIDs []string
	var archiveMachineIDs []string
	if e.security.Loaded {
		for id, ok := range e.security.TrustedMachines {
			if ok {
				trustedMachineIDs = append(trustedMachineIDs, id)
			}
		}
		sortutil.Strings(trustedMachineIDs)
		for id, ok := range e.security.ArchiveMachines {
			if ok {
				archiveMachineIDs = append(archiveMachineIDs, id)
			}
		}
		sortutil.Strings(archiveMachineIDs)
	}

	return &corev1.SyncStateView{
		Connection:             protoConnState(w.World.Connection),
		ConnectionReason:       w.ConnectionReasonText,
		ConnectionStep:         e.connStepText,
		DisconnectedAtMs:       w.DisconnectedAtMs,
		LivePushConnected:      w.LivePushConnected,
		Unlinked:               e.unlinkedFlag,
		IsManager:              w.World.IsManager,
		SelfDeviceId:           e.security.SelfDeviceID,
		AuthorityDeviceId:      e.security.AuthorityDeviceID,
		ManagerName:            w.ManagerNameText,
		AccountControlLost:     w.World.AccountControlLost,
		AccountClockSkewed:     w.World.AccountClockSkewed,
		Removed:                e.removed,
		SecurityLoaded:         e.security.Loaded,
		IsMember:               e.security.IsMember,
		LeaseEndMs:             e.security.LeaseEndMs,
		RecoveryPending:        e.security.RecoveryPending,
		RecoveryReadyMs:        e.security.RecoveryReadyMs,
		RecoveryObjectsPending: e.security.RecoveryObjectsPending,
		ClientProtocolVersion:  w.World.ClientVersion,
		Machines:               machines,
		MachinesListed:         w.World.MachinesListed,
		Sessions:               sessions,
		SessionsListed:         w.World.SessionsListed,
		LiveLoaded:             w.World.LiveLoaded,
		HasMoreSessions:        w.World.HasMoreSessions,
		NextCursor:             nextCursor,
		LoadingMoreSessions:    w.World.LoadingMoreSessions,
		SessionListFailed:      w.World.SessionListFailed,
		SessionListError:       w.SessionListErrorText,
		PrefsLoaded:            w.World.PrefsLoaded,
		PrefsLoadFailed:        w.World.PrefsLoadFailed,
		PrefsBroken:            e.prefs.Broken,
		PrefsRevision:          e.prefs.Revision,
		PrefsSyncing:           w.World.PrefsSyncing,
		Prefs:                  prefsCopy,
		Devices:                devices,
		DisplayGrace:           w.DisplayGrace,
		ReachabilityUnknown:    w.ReachabilityUnknown,
		TrustedMachineIds:      trustedMachineIDs,
		ArchiveMachineIds:      archiveMachineIDs,
		ConnectionStartedAtMs:  e.connStartedAtMs,
		NetworkAvailable:       e.networkAvailable,
		EverOnline:             e.everReady,
		ConnectFailures:        int32(e.recovery.Failures()),
		RecoveryStatusStale:    e.security.RecoveryStatusStale,
		DevicesLoadFailed:      e.devicesLoadFailed,
		DevicesErrorText:       e.devicesErrorText,
		HasHandoffOffer:        e.security.HasHandoffOffer,
		ConnBanner:             e.buildConnBanner(w),
		ClockSkewBannerText:    clockSkewBannerText(w.World.AccountClockSkewed),
		LeaseRenewalAdvice:     e.buildLeaseRenewalAdvice(w),
	}
}

func clockSkewBannerText(skewed bool) *corev1.Text {
	if !skewed {
		return nil
	}
	return text.Msg(text.KeyErrorsThisDeviceSClockIsBehindTheAccountState)
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
		meta = s.Info
		if s.Info != nil {
			agent = s.Info.Agent
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
			sortutil.Uint64s(seqs)
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
