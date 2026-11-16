// Package app implements the stateful Elm/Crux client core with a single
// Dispatch(Event) -> EffectBatch and View(Screen) -> ViewModel boundary.
package app

import (
	"slices"
	"strconv"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
)

// Model owns the mutable state held inside Core.
type Model struct {
	state        *screens.State
	seq          uint64
	closed       bool
	sessionIndex map[string]int
}

func newModel() *Model {
	return &Model{
		state:        screens.NewState(),
		sessionIndex: make(map[string]int),
	}
}

func (m *Model) nextID(prefix string) string {
	m.seq++
	return prefix + strconv.FormatUint(m.seq, 10)
}

func sessionFromInput(in *corev1.SessionInput) *lists.Session {
	if in == nil {
		return nil
	}
	id := in.SessionId
	if id == "" && in.Info != nil {
		id = in.Info.Id
	}
	var p lists.PendingAction
	if in.Pending != nil {
		p = lists.PendingAction{
			HasApproval:        in.Pending.HasPendingApproval,
			ApprovalID:         in.Pending.ApprovalId,
			ApprovalToolName:   in.Pending.ApprovalToolName,
			ApprovalCommand:    in.Pending.ApprovalCommand,
			ApprovalToolInput:  in.Pending.ApprovalToolInput,
			ApprovalTruncated:  in.Pending.ApprovalTruncated,
			ApprovalDeadlineMs: in.Pending.ApprovalDeadlineMs,
			HasQuestion:        in.Pending.HasPendingQuestion,
			HasPlan:            in.Pending.HasPendingPlan,
			HasPopup:           in.Pending.HasPopup,
			PopupPreview:       in.Pending.PopupPreview,
		}
	}
	return &lists.Session{
		ID:                id,
		MachineID:         in.MachineId,
		Info:              in.Info,
		UpdatedMs:         in.UpdatedMs,
		ListedEndedMs:     in.ListedEndedMs,
		Pending:           p,
		ScheduleName:      in.ScheduleName,
		ScheduleUnread:    in.ScheduleUnread,
		CreatedLocalHhmm:  in.CreatedLocalHhmm,
		EndedLocalYear:    in.EndedLocalYear,
		EndedLocalMonth:   in.EndedLocalMonth,
		UpdatedLocalYear:  in.UpdatedLocalYear,
		UpdatedLocalMonth: in.UpdatedLocalMonth,
		UpdatedLocalDay:   in.UpdatedLocalDay,
		UpdatedLocalWeek0: in.UpdatedLocalWeekday,
		UpdatedLocalHhmm:  in.UpdatedLocalHhmm,
	}
}

func machineFromInput(in *corev1.MachineInput) *lists.Machine {
	if in == nil {
		return nil
	}
	return &lists.Machine{
		ID:              in.MachineId,
		Online:          in.Online,
		Reachable:       in.Reachable,
		Unlinked:        in.Unlinked,
		LastSeenMs:      in.LastSeenMs,
		ProtocolVersion: in.ProtocolVersion,
		DaemonOutOfDate: in.DaemonOutOfDate,
		ClientOutOfDate: in.ClientOutOfDate,
		Meta:            in.Meta,
	}
}

func (m *Model) upsertSession(s *lists.Session) {
	if s == nil || s.ID == "" {
		return
	}
	if idx, ok := m.sessionIndex[s.ID]; ok {
		m.state.World.Sessions[idx] = s
		return
	}
	m.sessionIndex[s.ID] = len(m.state.World.Sessions)
	m.state.World.Sessions = append(m.state.World.Sessions, s)
}

func (m *Model) removeSession(id string) {
	idx, ok := m.sessionIndex[id]
	if !ok {
		return
	}
	last := len(m.state.World.Sessions) - 1
	if idx != last {
		moved := m.state.World.Sessions[last]
		m.state.World.Sessions[idx] = moved
		m.sessionIndex[moved.ID] = idx
	}
	m.state.World.Sessions = m.state.World.Sessions[:last]
	delete(m.sessionIndex, id)
}

func (m *Model) ensureDraft(key string) *screens.NewSessionDraft {
	if key == "" {
		return nil
	}
	d := m.state.NewSessions[key]
	if d == nil {
		d = &screens.NewSessionDraft{ProjectKey: key}
		m.state.NewSessions[key] = d
	}
	return d
}

func (m *Model) activeNewSessionKey(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if m.state.StandaloneProjectKey != "" {
		return m.state.StandaloneProjectKey
	}
	res := lists.BuildProjects(&m.state.World, "", m.state.RememberedScope, m.state.RememberedScopeToken, nil, m.state.Scope)
	if len(res.Listed) > 0 {
		return res.Listed[0].Key
	}
	return ""
}

func (m *Model) findMachine(id string) *lists.Machine {
	for _, mach := range m.state.World.Machines {
		if mach != nil && mach.ID == id {
			return mach
		}
	}
	return nil
}

func (m *Model) resolveDraftConfig(key string, d *screens.NewSessionDraft) lists.ProjectStartResult {
	var pickMachine string
	var pickAgent v1.Agent
	if d != nil && d.ConfigInitialized {
		pickMachine = d.Config.MachineID
		pickAgent = d.Config.Agent
	} else if d != nil && d.AgentOverride != v1.Agent_AGENT_UNSPECIFIED {
		pickAgent = d.AgentOverride
	}
	return lists.ResolveProjectStart(&m.state.World, key, pickMachine, pickAgent, nil, false)
}

func (m *Model) maybeFetchMachineConfig(machineID string, agent v1.Agent) []*corev1.Effect {
	if machineID == "" {
		return nil
	}
	mach := m.findMachine(machineID)
	if !lists.MachineReachable(mach) {
		return nil
	}
	var out []*corev1.Effect
	agent = lists.EffectiveAgent(agent)
	mk := screens.MachineAgentKey{MachineID: machineID, Agent: agent}
	mc := m.state.MachineModels[mk]
	if mc == nil || (!mc.Loaded && !mc.Loading && mc.Error == "") {
		m.state.MachineModels[mk] = &screens.ModelsCacheEntry{Loading: true}
		out = append(out, &corev1.Effect{
			Effect: &corev1.Effect_SendCommand{
				SendCommand: &corev1.SendCommandEffect{
					RequestId: m.nextID("cmd-"),
					MachineId: machineID,
					Command: &v1.Command{
						Kind: &v1.Command_ListModels{
							ListModels: &v1.ListModelsRequest{Agent: agent},
						},
					},
				},
			},
		})
	}
	sc := m.state.MachineSettings[machineID]
	if sc == nil || (!sc.Loaded && !sc.Loading && sc.Error == "") {
		m.state.MachineSettings[machineID] = &screens.SettingsCacheEntry{Loading: true}
		out = append(out, &corev1.Effect{
			Effect: &corev1.Effect_SendCommand{
				SendCommand: &corev1.SendCommandEffect{
					RequestId: m.nextID("cmd-"),
					MachineId: machineID,
					Command: &v1.Command{
						Kind: &v1.Command_GetSettings{
							GetSettings: &v1.GetSettingsRequest{},
						},
					},
				},
			},
		})
	}
	return out
}

func renderEffect(targets ...corev1.ScreenTarget) *corev1.Effect {
	return &corev1.Effect{
		Effect: &corev1.Effect_Render{
			Render: &corev1.RenderEffect{
				Screens: slices.Clone(targets),
			},
		},
	}
}

func mapConnState(s corev1.ConnState) lists.ConnectionKind {
	switch s {
	case corev1.ConnState_CONN_STATE_CONNECTED:
		return lists.ConnConnected
	case corev1.ConnState_CONN_STATE_RECONNECTING:
		return lists.ConnReconnecting
	case corev1.ConnState_CONN_STATE_OFFLINE:
		return lists.ConnOffline
	case corev1.ConnState_CONN_STATE_NO_NETWORK:
		return lists.ConnNoNetwork
	default:
		return lists.ConnConnecting
	}
}

func mapPrefsOpKind(k lists.PrefsOpKind) corev1.ProjectPrefsOpKind {
	switch k {
	case lists.PrefsOpPin:
		return corev1.ProjectPrefsOpKind_PROJECT_PREFS_OP_KIND_PIN
	case lists.PrefsOpUnpin:
		return corev1.ProjectPrefsOpKind_PROJECT_PREFS_OP_KIND_UNPIN
	case lists.PrefsOpHide:
		return corev1.ProjectPrefsOpKind_PROJECT_PREFS_OP_KIND_HIDE
	case lists.PrefsOpUnhide:
		return corev1.ProjectPrefsOpKind_PROJECT_PREFS_OP_KIND_UNHIDE
	case lists.PrefsOpAdd:
		return corev1.ProjectPrefsOpKind_PROJECT_PREFS_OP_KIND_ADD
	case lists.PrefsOpSetSettings:
		return corev1.ProjectPrefsOpKind_PROJECT_PREFS_OP_KIND_SET_SETTINGS
	default:
		return corev1.ProjectPrefsOpKind_PROJECT_PREFS_OP_KIND_UNSPECIFIED
	}
}
