package screens

import (
	"strconv"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/schedule"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	defaultSchedulesEndedLimit = 30
	defaultEarlierRunsLimit    = 5
	paramTime                  = "time"
)

// ScheduleMachineSnapshot holds cached or authoritative tasks and fetch state for one machine.
type ScheduleMachineSnapshot struct {
	Tasks           []*schedule.Task
	Loading         bool
	Live            bool
	Cached          bool
	Error           string
	ErrorText       *corev1.Text
	FetchedRevision uint64
	HasFetchedOnce  bool
	WasReachable    bool
	InFlightReqID   string
	PendingRefresh  bool
	CacheReading    bool
	CacheRestored   bool
	CacheReadOpID   uint64
	SeenLoading     bool
	SeenLoaded      bool
}

// ScheduleMutationState tracks pending or frozen unknown command status for one action slot.
type ScheduleMutationState struct {
	Busy       bool
	Frozen     bool
	Expired    bool
	ErrorText  *corev1.Text
	NoticeText *corev1.Text
}

// ScheduleDetailState tracks the currently visited detail screen's pinned result and expansion state.
type ScheduleDetailState struct {
	MachineID           string
	TaskID              string
	PinnedResultID      string
	ResultCaptured      bool
	ShowAllRuns         bool
	ExpandedQuietGroups map[string]bool
}

// ScheduleFormState holds a create or edit schedule draft.
type ScheduleFormState struct {
	FormKey           string
	Mode              corev1.ScheduleFormMode
	Open              bool
	MachineID         string
	TaskID            string
	SessionID         string
	ProjectKey        string
	Config            lists.StartConfig
	ConfigInitialized bool
	ConfigChosen      bool
	MachineChosen     bool
	AgentOverride     v1.Agent
	Message           string
	Title             string
	NotifyOnFinish    bool
	Trigger           schedule.Trigger
	Timing            schedule.TimingDraft
	TimingError       *corev1.Text
	TimingApplied     bool
	BaseTask          *schedule.Task
	CreationKey       string
	SubmittedSpecJSON string
	FrozenSpec        *schedule.Spec
	Dirty             bool
}

// ScheduleState holds all schedule-domain state managed by Core across screens.
type ScheduleState struct {
	LocalTimezone           string
	TomorrowStartMs         int64
	DayAfterTomorrowStartMs int64

	Machines          map[string]*ScheduleMachineSnapshot
	Seen              *schedule.SeenTracker
	SelectedMachineID string
	SelectedTaskID    string
	EndedExpanded     bool
	Detail            *ScheduleDetailState
	Mutations         map[string]*ScheduleMutationState
	ActiveFormKey     string
	Forms             map[string]*ScheduleFormState

	// SessionEvents provides settled transcript events for final_answer extraction (wired by app).
	SessionEvents func(sessionID string) (events []*v1.Event, settled bool)
}

// NewScheduleState constructs an initialized ScheduleState.
func NewScheduleState() *ScheduleState {
	return &ScheduleState{
		Machines:  make(map[string]*ScheduleMachineSnapshot),
		Seen:      schedule.NewSeenTracker(),
		Mutations: make(map[string]*ScheduleMutationState),
		Forms:     make(map[string]*ScheduleFormState),
	}
}

func scheduleBounds(st *State) schedule.CalendarBounds {
	var b schedule.CalendarBounds
	if st == nil {
		return b
	}
	b.Cal = st.Cal
	if st.Schedules != nil {
		b.LocalTimezone = st.Schedules.LocalTimezone
		b.TomorrowStartMs = st.Schedules.TomorrowStartMs
		b.DayAfterTomorrowStartMs = st.Schedules.DayAfterTomorrowStartMs
	}
	return b
}

func ensureScheduleState(st *State) *ScheduleState {
	if st.Schedules == nil {
		st.Schedules = NewScheduleState()
	}
	return st.Schedules
}

func findSessionEntry(st *State, sessionID string) *lists.Session {
	if st == nil || sessionID == "" {
		return nil
	}
	for _, s := range st.World.Sessions {
		if s != nil && s.ID == sessionID {
			return s
		}
	}
	return nil
}

func sessionDisplayTitle(st *State, sessionID string) *corev1.Text {
	s := findSessionEntry(st, sessionID)
	if s == nil || s.Info == nil {
		return text.Msg(text.KeyScheduleConversation)
	}
	if s.Info.Title != "" {
		return text.Plain(s.Info.Title)
	}
	proj := lists.ProjectName(s.Info.Cwd)
	createdMs := lists.CreatedMs(s)
	if proj != "" && createdMs > 0 {
		return text.JoinLiteral(" · ", text.Plain(proj), text.Time(createdMs, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME))
	}
	if proj != "" {
		return text.Plain(proj)
	}
	if createdMs > 0 {
		return text.Time(createdMs, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME)
	}
	return text.Msg(text.KeyListStateNewSession)
}

func agentByIDString(id string) v1.Agent {
	switch id {
	case "claude":
		return v1.Agent_AGENT_CLAUDE
	case "agy", "antigravity":
		return v1.Agent_AGENT_ANTIGRAVITY
	case "codex":
		return v1.Agent_AGENT_CODEX
	default:
		return lists.DefaultAgent
	}
}

func agentIDString(a v1.Agent) string {
	return lists.AgentInfo(a).ID
}

func approvalModeFromString(s string) v1.ApprovalMode {
	switch s {
	case "ask":
		return v1.ApprovalMode_APPROVAL_MODE_ASK
	case "auto":
		return v1.ApprovalMode_APPROVAL_MODE_AUTO
	case "full":
		return v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS
	default:
		return v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	}
}

// ApprovalModeToScheduleString serializes an ApprovalMode for ScheduleTarget.mode.
func ApprovalModeToScheduleString(m v1.ApprovalMode) string {
	switch m {
	case v1.ApprovalMode_APPROVAL_MODE_ASK:
		return "ask"
	case v1.ApprovalMode_APPROVAL_MODE_AUTO:
		return "auto"
	case v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS:
		return "full"
	default:
		return ""
	}
}

func mapTaskStatus(s string) corev1.ScheduleTaskStatus {
	switch s {
	case "active":
		return corev1.ScheduleTaskStatus_SCHEDULE_TASK_STATUS_ACTIVE
	case "paused":
		return corev1.ScheduleTaskStatus_SCHEDULE_TASK_STATUS_PAUSED
	case "failed":
		return corev1.ScheduleTaskStatus_SCHEDULE_TASK_STATUS_FAILED
	case "uncertain":
		return corev1.ScheduleTaskStatus_SCHEDULE_TASK_STATUS_UNCERTAIN
	case "completed":
		return corev1.ScheduleTaskStatus_SCHEDULE_TASK_STATUS_COMPLETED
	case "cancelled":
		return corev1.ScheduleTaskStatus_SCHEDULE_TASK_STATUS_CANCELLED
	default:
		return corev1.ScheduleTaskStatus_SCHEDULE_TASK_STATUS_UNSPECIFIED
	}
}

func mapRunStatus(s string) corev1.ScheduleRunStatus {
	switch s {
	case "dispatching":
		return corev1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_DISPATCHING
	case "queued":
		return corev1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_QUEUED
	case "running":
		return corev1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_RUNNING
	case "completed":
		return corev1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_COMPLETED
	case "failed":
		return corev1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_FAILED
	case "uncertain":
		return corev1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_UNCERTAIN
	case "interrupted":
		return corev1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_INTERRUPTED
	case "skipped":
		return corev1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_SKIPPED
	case "cancelled":
		return corev1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_CANCELLED
	default:
		return corev1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_UNSPECIFIED
	}
}

func mapScheduleSource(src string) corev1.ScheduleSource {
	switch src {
	case "agent":
		return corev1.ScheduleSource_SCHEDULE_SOURCE_AGENT
	case "local":
		return corev1.ScheduleSource_SCHEDULE_SOURCE_LOCAL
	case "client":
		return corev1.ScheduleSource_SCHEDULE_SOURCE_CLIENT
	default:
		return corev1.ScheduleSource_SCHEDULE_SOURCE_UNSPECIFIED
	}
}

func mapTakeoverView(t *schedule.Task) *corev1.ScheduleTakeoverView {
	if t == nil || t.Takeover == nil {
		return nil
	}
	via := corev1.ScheduleTakeoverVia_SCHEDULE_TAKEOVER_VIA_CLIENT
	if t.Takeover.Via == "local" {
		via = corev1.ScheduleTakeoverVia_SCHEDULE_TAKEOVER_VIA_LOCAL
	}
	return &corev1.ScheduleTakeoverView{
		HasTakeover: true,
		SessionId:   t.Takeover.Session,
		Via:         via,
		Label:       schedule.TakeoverText(t),
	}
}

func mapTimingKindProto(k schedule.TimingKind) corev1.ScheduleTimingKind {
	switch k {
	case schedule.TimingOnce:
		return corev1.ScheduleTimingKind_SCHEDULE_TIMING_KIND_ONCE
	case schedule.TimingDaily:
		return corev1.ScheduleTimingKind_SCHEDULE_TIMING_KIND_DAILY
	case schedule.TimingWeekly:
		return corev1.ScheduleTimingKind_SCHEDULE_TIMING_KIND_WEEKLY
	case schedule.TimingInterval:
		return corev1.ScheduleTimingKind_SCHEDULE_TIMING_KIND_INTERVAL
	case schedule.TimingAfterTurn:
		return corev1.ScheduleTimingKind_SCHEDULE_TIMING_KIND_AFTER_TURN
	default:
		return corev1.ScheduleTimingKind_SCHEDULE_TIMING_KIND_UNSPECIFIED
	}
}

func mapIntervalUnitProto(u schedule.IntervalUnit) corev1.ScheduleIntervalUnit {
	switch u {
	case schedule.UnitMinutes:
		return corev1.ScheduleIntervalUnit_SCHEDULE_INTERVAL_UNIT_MINUTES
	case schedule.UnitHours:
		return corev1.ScheduleIntervalUnit_SCHEDULE_INTERVAL_UNIT_HOURS
	case schedule.UnitDays:
		return corev1.ScheduleIntervalUnit_SCHEDULE_INTERVAL_UNIT_DAYS
	case schedule.UnitSeconds:
		return corev1.ScheduleIntervalUnit_SCHEDULE_INTERVAL_UNIT_SECONDS
	default:
		return corev1.ScheduleIntervalUnit_SCHEDULE_INTERVAL_UNIT_UNSPECIFIED
	}
}

func mapTimingEndProto(e schedule.TimingEnd) corev1.ScheduleTimingEnd {
	switch e {
	case schedule.EndNever:
		return corev1.ScheduleTimingEnd_SCHEDULE_TIMING_END_NEVER
	case schedule.EndCount:
		return corev1.ScheduleTimingEnd_SCHEDULE_TIMING_END_COUNT
	case schedule.EndDate:
		return corev1.ScheduleTimingEnd_SCHEDULE_TIMING_END_DATE
	default:
		return corev1.ScheduleTimingEnd_SCHEDULE_TIMING_END_UNSPECIFIED
	}
}

func offlineOrUnsupportedReason(st *State, machineID string) *corev1.Text {
	mac := findMachine(st, machineID)
	name := lists.MachineName(mac, machineID)
	if !schedule.Supported(mac, st.World.ClientVersion) {
		return text.Msg(text.KeyScheduleAndroidUpdateRemoteAgentOnThisComputerToManageScheduledTasks)
	}
	if name != "" {
		return text.Msg(text.KeyScheduleAndroidConnectValue1ToEditRunOrDelete, text.Str("value1", name))
	}
	return text.Msg(text.KeyScheduleAndroidComputerOfflineTasksCannotBeEdited)
}

func mutationView(mut *ScheduleMutationState) *corev1.ScheduleMutationView {
	if mut == nil {
		return &corev1.ScheduleMutationView{}
	}
	var frozenHint *corev1.Text
	if mut.Frozen {
		frozenHint = text.Msg(text.KeyScheduleThePreviousOperationSResultIsUnconfirmedVerifyThe)
	}
	return &corev1.ScheduleMutationView{
		Busy:       mut.Busy,
		Frozen:     mut.Frozen,
		Expired:    mut.Expired,
		Error:      mut.ErrorText,
		Notice:     mut.NoticeText,
		CanRetry:   mut.Frozen && !mut.Expired && !mut.Busy,
		CanAbandon: mut.Frozen && !mut.Busy,
		FrozenHint: frozenHint,
	}
}

func buildTaskActions(st *State, machineID string, t *schedule.Task, hasUnread bool, mut *ScheduleMutationState) *corev1.ScheduleTaskActionsView {
	writable := schedule.Writable(&st.World, machineID)
	var busy, frozen bool
	if mut != nil {
		busy = mut.Busy
		frozen = mut.Frozen
	}
	acc := schedule.EditAccess(t, writable, busy, frozen)

	var baseDisabled *corev1.Text
	switch {
	case !writable:
		baseDisabled = offlineOrUnsupportedReason(st, machineID)
	case busy:
		baseDisabled = text.Msg(text.KeyScheduleReadOnlyBusy)
	case frozen:
		baseDisabled = text.Msg(text.KeyScheduleReadOnlyUnconfirmed)
	}

	defDisabled := baseDisabled
	if defDisabled == nil && t != nil && t.Active != nil {
		defDisabled = text.Msg(text.KeyScheduleReadOnlyActiveRun)
	}

	runDisabled := baseDisabled
	if runDisabled == nil && t != nil {
		switch {
		case t.Active != nil || t.ManualRequest != nil:
			runDisabled = text.Msg(text.KeyScheduleRunInProgressCannotRun)
		case schedule.GroupOf(t) == schedule.GroupEnded:
			runDisabled = text.Msg(text.KeyScheduleEndedCannotRun)
		case schedule.GroupOf(t) == schedule.GroupAttention && !acc.FailedOnce:
			runDisabled = text.Msg(text.KeyScheduleStoppedCannotRun)
		}
	}

	var toggleLabel *corev1.Text
	if acc.ShowToggleAction {
		if acc.IsResumeAction {
			toggleLabel = text.Msg(text.KeyScheduleResume)
		} else {
			toggleLabel = text.Msg(text.KeySchedulePause)
		}
	}

	ln := schedule.LatestNotification(t)
	canMarkRead := ln != nil && hasUnread
	canMarkUnread := ln != nil && !hasUnread && schedule.GroupOf(t) != schedule.GroupAttention

	return &corev1.ScheduleTaskActionsView{
		CanRunNow:                    acc.CanRunNow,
		RunNowDisabledReason:         runDisabled,
		ShowToggleAction:             acc.ShowToggleAction,
		IsResumeAction:               acc.IsResumeAction,
		ToggleLabel:                  toggleLabel,
		CanToggle:                    acc.CanToggle,
		ToggleDisabledReason:         baseDisabled,
		CanDelete:                    acc.CanDelete,
		DeleteDisabledReason:         baseDisabled,
		CanMarkRead:                  canMarkRead,
		CanMarkUnread:                canMarkUnread,
		CanRename:                    acc.CanRename,
		RenameDisabledReason:         baseDisabled,
		CanEditDefinition:            acc.CanEditDefinition,
		EditDefinitionDisabledReason: defDisabled,
		CanToggleNotify:              acc.CanToggleNotify,
		NotifyDisabledReason:         baseDisabled,
	}
}

func visibleScheduleEntries(st *State) []schedule.TaskEntry {
	ss := st.Schedules
	if ss == nil {
		return nil
	}
	var out []schedule.TaskEntry
	for _, m := range st.World.Machines {
		if m == nil || !schedule.Supported(m, st.World.ClientVersion) {
			continue
		}
		snap := ss.Machines[m.ID]
		if snap == nil {
			continue
		}
		for _, t := range snap.Tasks {
			if schedule.Visible(t) {
				out = append(out, schedule.TaskEntry{MachineID: m.ID, Task: t})
			}
		}
	}
	return out
}

func taskCwdAndAgent(st *State, e schedule.TaskEntry) (cwd string, agent v1.Agent, agentName string) {
	t := e.Task
	if t == nil {
		return "", v1.Agent_AGENT_UNSPECIFIED, ""
	}
	if t.Spec.Target.Type == "resume_session" {
		if s := findSessionEntry(st, t.Spec.Target.Session); s != nil && s.Info != nil {
			a := lists.SessionAgent(s)
			return s.Info.Cwd, a, lists.AgentInfo(a).Name
		}
		return "", v1.Agent_AGENT_UNSPECIFIED, ""
	}
	a := agentByIDString(t.Spec.Target.Agent)
	return t.Spec.Target.Cwd, a, lists.AgentInfo(a).Name
}

func taskLocationText(st *State, e schedule.TaskEntry) *corev1.Text {
	t := e.Task
	var parts []*corev1.Text
	if t.Spec.Target.Type == "resume_session" {
		if s := findSessionEntry(st, t.Spec.Target.Session); s != nil && s.Info != nil {
			loc := text.Msg(text.KeyScheduleIn, text.ArgText("value", sessionDisplayTitle(st, s.ID)))
			parts = append(parts, loc)
			if s.Info.Agent != v1.Agent_AGENT_UNSPECIFIED {
				parts = append(parts, text.Plain(lists.AgentInfo(s.Info.Agent).Name))
			}
		} else {
			parts = append(parts, text.Msg(text.KeyScheduleInConversation))
		}
	} else {
		pn := lists.ProjectName(t.Spec.Target.Cwd)
		if pn == "" {
			pn = t.Spec.Target.Cwd
		}
		if pn != "" {
			parts = append(parts, text.Plain(pn))
		}
		_, _, aName := taskCwdAndAgent(st, e)
		if aName != "" {
			parts = append(parts, text.Plain(aName))
		}
	}
	if len(st.World.Machines) > 1 {
		if mac := findMachine(st, e.MachineID); mac != nil {
			parts = append(parts, text.Plain(lists.MachineName(mac, e.MachineID)))
		}
	}
	return text.JoinLiteral(" · ", parts...)
}

func taskMenuSubtitle(st *State, e schedule.TaskEntry, bounds schedule.CalendarBounds) *corev1.Text {
	var parts []*corev1.Text
	cwd, _, aName := taskCwdAndAgent(st, e)
	if cwd != "" {
		if pn := lists.ProjectName(cwd); pn != "" {
			parts = append(parts, text.Plain(pn))
		}
	}
	if aName != "" {
		parts = append(parts, text.Plain(aName))
	}
	if e.Task != nil {
		if e.Task.Source == "agent" {
			parts = append(parts, text.Msg(text.KeyScheduleCreatedByTheAgentInAConversation))
		} else {
			parts = append(parts, text.Msg(text.KeyScheduleCreatedByYou))
		}
		if ms := schedule.ParseRFC3339Ms(e.Task.CreatedAt); ms > 0 {
			parts = append(parts, schedule.DayText(ms, bounds))
		}
	}
	return text.JoinLiteral(" · ", parts...)
}

type scopeVariety struct {
	multiProject bool
	multiAgent   bool
	multiMachine bool
}

func computeScopeVariety(st *State, entries []schedule.TaskEntry) scopeVariety {
	projects := make(map[string]bool)
	agents := make(map[v1.Agent]bool)
	machines := make(map[string]bool)
	for _, e := range entries {
		cwd, ag, _ := taskCwdAndAgent(st, e)
		if cwd != "" {
			projects[cwd] = true
		}
		if ag != v1.Agent_AGENT_UNSPECIFIED {
			agents[ag] = true
		}
		if e.MachineID != "" {
			machines[e.MachineID] = true
		}
	}
	return scopeVariety{
		multiProject: len(projects) > 1,
		multiAgent:   len(agents) > 1,
		multiMachine: len(machines) > 1,
	}
}

func buildTaskItemView(
	st *State,
	e schedule.TaskEntry,
	group corev1.ScheduleDisplayGroup,
	hasUnread bool,
	sv scopeVariety,
	bounds schedule.CalendarBounds,
) *corev1.ScheduleTaskItemView {
	ss := st.Schedules
	t := e.Task
	tg := schedule.GroupOf(t)
	writable := schedule.Writable(&st.World, e.MachineID)
	inProgress := schedule.InProgress(t)
	ln := schedule.LatestNotification(t)

	var timeMs int64
	if tg == schedule.GroupAttention {
		timeMs = schedule.StoppedTimeMs(t)
	} else if ln != nil {
		timeMs = schedule.NotificationTimeMs(ln)
	}

	var tone corev1.ScheduleTone
	switch {
	case tg == schedule.GroupAttention:
		tone = corev1.ScheduleTone_SCHEDULE_TONE_WARN
	case inProgress || tg == schedule.GroupActive:
		tone = corev1.ScheduleTone_SCHEDULE_TONE_LIVE
	default:
		tone = corev1.ScheduleTone_SCHEDULE_TONE_NEUTRAL
	}

	var attentionLead, attentionReason, preview *corev1.Text
	var previewPlaceholder bool
	disabledAgentName := disabledNewSessionAgentName(st, e)
	if tg == schedule.GroupAttention {
		attentionLead = schedule.AttentionLeadText(t)
		if disabledAgentName != "" && strings.HasPrefix(t.Reason, "agent_disabled:") {
			mac := findMachine(st, e.MachineID)
			attentionReason = text.Msg(text.KeyScheduleAgentDisabledListNote, text.Str("machine", lists.MachineName(mac, e.MachineID)), text.Str("agent", disabledAgentName))
		} else {
			attentionReason = schedule.ReasonText(t.Reason, true)
		}
		preview = text.JoinLiteral(" · ", attentionLead, attentionReason)
	} else if disabledAgentName != "" {
		mac := findMachine(st, e.MachineID)
		attentionReason = text.Msg(text.KeyScheduleAgentDisabledListNote, text.Str("machine", lists.MachineName(mac, e.MachineID)), text.Str("agent", disabledAgentName))
		preview = attentionReason
	} else if hasUnread && ln != nil {
		if schedule.IsStoppedRun(ln.Status) {
			preview = schedule.ReasonText(ln.Reason, true)
		} else {
			preview = schedule.RunSummaryText(ln)
			if text.IsEmpty(preview) {
				preview = schedule.RunResultText(ln)
			}
		}
	} else {
		preview, previewPlaceholder = schedule.LastResultText(t, bounds)
	}

	timing := schedule.TimingText(t.Spec.Trigger, bounds)
	cwd, _, aName := taskCwdAndAgent(st, e)
	metaParts := []*corev1.Text{timing}
	if sv.multiProject && cwd != "" {
		if pn := lists.ProjectName(cwd); pn != "" {
			metaParts = append(metaParts, text.Plain(pn))
		}
	}
	if sv.multiAgent && aName != "" {
		metaParts = append(metaParts, text.Plain(aName))
	}
	if sv.multiMachine {
		mac := findMachine(st, e.MachineID)
		metaParts = append(metaParts, text.Plain(lists.MachineName(mac, e.MachineID)))
	}
	if t.Spec.Target.Type == "resume_session" {
		if s := findSessionEntry(st, t.Spec.Target.Session); s != nil && s.Info != nil {
			metaParts = append(metaParts, text.Msg(text.KeyScheduleIn, text.ArgText("value", sessionDisplayTitle(st, s.ID))))
		} else {
			metaParts = append(metaParts, text.Msg(text.KeyScheduleInConversation))
		}
	}
	if t.Status == "paused" {
		metaParts = append(metaParts, text.Msg(text.KeyQueuePaused))
	}
	if !writable {
		metaParts = append(metaParts, text.Msg(text.KeyListStateUnlinked))
	}

	var readOnlyReason *corev1.Text
	if !writable {
		readOnlyReason = offlineOrUnsupportedReason(st, e.MachineID)
	}
	mut := ss.Mutations["op:"+e.MachineID+"/"+t.ID]

	return &corev1.ScheduleTaskItemView{
		MachineId:            e.MachineID,
		TaskId:               t.ID,
		Title:                t.Spec.Title,
		Group:                group,
		Status:               mapTaskStatus(t.Status),
		Tone:                 tone,
		HasUnread:            hasUnread,
		InProgress:           inProgress,
		Paused:               t.Status == "paused",
		MachineOffline:       !writable,
		Selected:             ss.SelectedMachineID == e.MachineID && ss.SelectedTaskID == t.ID,
		TimeText:             schedule.RowTimeText(timeMs, bounds),
		AttentionLead:        attentionLead,
		AttentionReason:      attentionReason,
		Preview:              preview,
		PreviewIsPlaceholder: previewPlaceholder,
		Metadata:             text.JoinLiteral(" · ", metaParts...),
		TimingText:           timing,
		NextRunText:          schedule.NextText(t, bounds),
		NextRunMs:            schedule.ParseRFC3339Ms(t.NextRunAt),
		HasNotification:      ln != nil,
		IsAgentCreated:       t.Source == "agent",
		Takeover:             mapTakeoverView(t),
		ReadOnly:             !writable,
		ReadOnlyReason:       readOnlyReason,
		Actions:              buildTaskActions(st, e.MachineID, t, hasUnread, mut),
		MenuSubtitle:         taskMenuSubtitle(st, e, bounds),
	}
}

// BuildSchedules constructs the layout-agnostic SchedulesViewModel.
func BuildSchedules(st *State, endedLimit int) *corev1.SchedulesViewModel {
	if st == nil {
		st = NewState(nil)
	}
	ss := ensureScheduleState(st)
	bounds := scheduleBounds(st)

	if endedLimit == 0 {
		endedLimit = defaultSchedulesEndedLimit
	}

	entries := visibleScheduleEntries(st)

	stopped := schedule.SortInbox(entries, schedule.InboxAttention, ss.Seen)
	recent := schedule.SortInbox(entries, schedule.InboxRecent, ss.Seen)
	endedAll := schedule.SortInbox(entries, schedule.InboxEnded, ss.Seen)

	var fresh, others []schedule.TaskEntry
	for _, e := range recent {
		if ss.Seen.HasNewResult(e.MachineID, e.Task) {
			fresh = append(fresh, e)
		} else {
			others = append(others, e)
		}
	}

	hasAnyNew := false
	for _, e := range entries {
		if ss.Seen.HasNewResult(e.MachineID, e.Task) {
			hasAnyNew = true
			break
		}
	}

	endedShown := endedAll
	if endedLimit > 0 && len(endedAll) > endedLimit {
		endedShown = endedAll[:endedLimit]
	}

	loading := waitingForMachineList(st)
	var machineErrors []*corev1.ScheduleMachineErrorView
	var anySupported bool
	var canCreate bool
	unlinked := st.World.Unlinked != nil && *st.World.Unlinked
	for _, m := range st.World.Machines {
		if m == nil {
			continue
		}
		if m.Unlinked == nil && !unlinked || (m.Unlinked != nil && !*m.Unlinked) {
			canCreate = true
		}
		if !schedule.Supported(m, st.World.ClientVersion) {
			continue
		}
		anySupported = true
		snap := ss.Machines[m.ID]
		if snap == nil || snap.Loading || (snap.CacheReading && !snap.HasFetchedOnce && !snap.Live && len(snap.Tasks) == 0) {
			loading = true
		}
		if snap != nil {
			if !text.IsEmpty(snap.ErrorText) || snap.Error != "" {
				machineErrors = append(machineErrors, &corev1.ScheduleMachineErrorView{
					MachineId:   m.ID,
					MachineName: lists.MachineName(m, m.ID),
					Error: text.Msg(text.KeyScheduleListFailed,
						text.Str("name", lists.MachineName(m, m.ID)),
						reasonArg("reason", snap.ErrorText, snap.Error),
					),
					CanRetry: schedule.Writable(&st.World, m.ID),
				})
			}
		}
	}

	var unsupportedNotice *corev1.Text
	if st.World.MachinesListed && len(st.World.Machines) > 0 && !anySupported {
		unsupportedNotice = text.Msg(text.KeyScheduleUpdateRemoteAgentOnTheComputerToUseScheduled)
	}

	var offlineNotice *corev1.Text
	if st.World.Connection == lists.ConnConnected {
		seenAway := make(map[string]bool)
		var awayNames []*corev1.Text
		for _, e := range entries {
			if seenAway[e.MachineID] {
				continue
			}
			mac := findMachine(st, e.MachineID)
			if !lists.MachineReachable(mac) {
				seenAway[e.MachineID] = true
				awayNames = append(awayNames, text.Plain(lists.MachineName(mac, e.MachineID)))
			}
		}
		if len(awayNames) > 0 {
			joined := text.Join(text.KeyCommonListSeparator, awayNames...)
			offlineNotice = text.Msg(text.KeyScheduleOfflineMachinesNotice, text.ArgText("machines", joined))
		}
	}

	visibleTotal := len(stopped) + len(recent) + len(endedAll)
	headline := &corev1.ScheduleHeadlineView{}
	switch {
	case visibleTotal == 0:
		headline.Kind = corev1.ScheduleHeadlineKind_SCHEDULE_HEADLINE_KIND_EMPTY
		if loading {
			headline.Title = text.Msg(text.KeyScheduleReadingTasks)
		} else {
			headline.Title = text.Msg(text.KeyScheduleNoScheduledTasksYet)
		}
	case len(stopped) > 0:
		headline.Kind = corev1.ScheduleHeadlineKind_SCHEDULE_HEADLINE_KIND_STOPPED
		headline.HighlightCount = int32(len(stopped))
		headline.Title = text.Msg(text.KeyScheduleStoppedCount,
			text.Int("count", int64(len(stopped))),
			text.Str("total", strconv.Itoa(len(stopped))),
		)
		if len(fresh) > 0 {
			headline.Sub = text.Msg(text.KeyScheduleMoreNewResults, text.Int("count", int64(len(fresh))))
		}
	case len(fresh) > 0:
		headline.Kind = corev1.ScheduleHeadlineKind_SCHEDULE_HEADLINE_KIND_NEW_RESULTS
		headline.Title = text.Msg(text.KeyScheduleNewResults, text.Int("count", int64(len(fresh))))
	default:
		headline.Kind = corev1.ScheduleHeadlineKind_SCHEDULE_HEADLINE_KIND_NO_NEW_RESULTS
		headline.Title = text.Msg(text.KeyScheduleNoNewResults)
	}

	sv := computeScopeVariety(st, entries)
	attentionViews := make([]*corev1.ScheduleTaskItemView, 0, len(stopped))
	for _, e := range stopped {
		attentionViews = append(attentionViews, buildTaskItemView(st, e, corev1.ScheduleDisplayGroup_SCHEDULE_DISPLAY_GROUP_ATTENTION, ss.Seen.HasNewResult(e.MachineID, e.Task), sv, bounds))
	}
	freshViews := make([]*corev1.ScheduleTaskItemView, 0, len(fresh))
	for _, e := range fresh {
		freshViews = append(freshViews, buildTaskItemView(st, e, corev1.ScheduleDisplayGroup_SCHEDULE_DISPLAY_GROUP_FRESH, true, sv, bounds))
	}
	otherViews := make([]*corev1.ScheduleTaskItemView, 0, len(others))
	for _, e := range others {
		otherViews = append(otherViews, buildTaskItemView(st, e, corev1.ScheduleDisplayGroup_SCHEDULE_DISPLAY_GROUP_OTHER, false, sv, bounds))
	}
	endedViews := make([]*corev1.ScheduleTaskItemView, 0, len(endedShown))
	for _, e := range endedShown {
		endedViews = append(endedViews, buildTaskItemView(st, e, corev1.ScheduleDisplayGroup_SCHEDULE_DISPLAY_GROUP_ENDED, false, sv, bounds))
	}

	var emptyHint *corev1.Text
	showEmptyHint := visibleTotal == 0 && !loading
	if showEmptyHint {
		emptyHint = text.Msg(text.KeyScheduleTellTheAgentInAConversationCheckForMe)
	}

	var othersTitle *corev1.Text
	if len(stopped) == 0 && len(fresh) == 0 {
		othersTitle = text.Msg(text.KeyScheduleAndroidTask)
	} else {
		othersTitle = text.Msg(text.KeyScheduleOtherTasks)
	}

	var badgeText string
	if len(stopped) > 99 {
		badgeText = "99+"
	} else if len(stopped) > 0 {
		badgeText = strconv.Itoa(len(stopped))
	}

	var endedLabel *corev1.Text
	if len(endedAll) > 0 {
		endedLabel = text.Msg(text.KeyScheduleEndedCount, text.Int("count", int64(len(endedAll))))
	}

	return &corev1.SchedulesViewModel{
		AttentionCount:      int32(len(stopped)),
		HasNewResults:       hasAnyNew,
		AttentionBadgeText:  badgeText,
		CanCreate:           canCreate,
		Connection:          buildConnectionStatus(st),
		Headline:            headline,
		AnyMachineSupported: anySupported,
		UnsupportedNotice:   unsupportedNotice,
		OfflineNotice:       offlineNotice,
		MachineErrors:       machineErrors,
		Loading:             loading,
		ShowEmptyHint:       showEmptyHint,
		EmptyHint:           emptyHint,
		Attention:           attentionViews,
		Fresh:               freshViews,
		Others:              otherViews,
		Ended:               endedViews,
		TotalEnded:          int32(len(endedAll)),
		HasMoreEnded:        len(endedShown) < len(endedAll),
		EndedExpanded:       ss.EndedExpanded,
		EndedLabel:          endedLabel,
		OthersSectionTitle:  othersTitle,
		CanMarkAllRead:      len(fresh) > 1,
		SelectedMachineId:   ss.SelectedMachineID,
		SelectedTaskId:      ss.SelectedTaskID,
		InitialLoaded:       st.World.MachinesListed || st.World.SessionsListed || st.World.PrefsLoaded,
	}
}

// BuildScheduleDetail constructs the layout-agnostic ScheduleDetailViewModel.
func BuildScheduleDetail(st *State, machineID, taskID string, earlierLimit int) *corev1.ScheduleDetailViewModel {
	if st == nil {
		st = NewState(nil)
	}
	ss := ensureScheduleState(st)
	bounds := scheduleBounds(st)
	if earlierLimit == 0 {
		earlierLimit = defaultEarlierRunsLimit
	}

	mac := findMachine(st, machineID)
	supported := schedule.Supported(mac, st.World.ClientVersion)
	writable := schedule.Writable(&st.World, machineID)
	snap := ss.Machines[machineID]

	var task *schedule.Task
	if supported && snap != nil {
		for _, t := range snap.Tasks {
			if t != nil && t.ID == taskID {
				task = t
				break
			}
		}
	}

	if task == nil {
		stillLoading := (mac == nil && waitingForMachineList(st)) ||
			(mac != nil && supported && snap == nil) ||
			(snap != nil && (snap.Loading || (snap.CacheReading && !snap.HasFetchedOnce && !snap.Live)))
		var notFound *corev1.Text
		switch {
		case snap != nil && (!text.IsEmpty(snap.ErrorText) || snap.Error != ""):
			if !text.IsEmpty(snap.ErrorText) {
				notFound = snap.ErrorText
			} else {
				notFound = text.Plain(snap.Error)
			}
		case stillLoading:
			notFound = text.Msg(text.KeyScheduleReadingTasks)
		default:
			notFound = text.Msg(text.KeyScheduleThisTaskCouldNotBeFoundIfTheComputer)
		}
		return &corev1.ScheduleDetailViewModel{
			Found:            false,
			Loading:          stillLoading && (snap == nil || (text.IsEmpty(snap.ErrorText) && snap.Error == "")),
			NotFoundText:     notFound,
			MachineId:        machineID,
			TaskId:           taskID,
			Connection:       buildConnectionStatus(st),
			MachineOnline:    lists.MachineReachable(mac),
			MachineSupported: supported,
			ReadOnly:         true,
			InitialLoaded:    st.World.MachinesListed || st.World.SessionsListed || st.World.PrefsLoaded,
		}
	}

	mut := ss.Mutations["op:"+machineID+"/"+taskID]
	var busy, frozen bool
	if mut != nil {
		busy = mut.Busy
		frozen = mut.Frozen
	}
	hasUnread := ss.Seen.HasNewResult(machineID, task)
	actions := buildTaskActions(st, machineID, task, hasUnread, mut)

	tg := schedule.GroupOf(task)
	timingTxt := schedule.TimingText(task.Spec.Trigger, bounds)
	var subtitle *corev1.Text
	if tg == schedule.GroupAttention {
		subtitle = text.Msg(text.KeyScheduleStopped)
	} else if task.Spec.Target.Type == "resume_session" {
		subtitle = text.Msg(text.KeyScheduleSendTo,
			text.ArgText("detail", sessionDisplayTitle(st, task.Spec.Target.Session)),
			text.ArgText("value", timingTxt),
		)
	} else {
		subtitle = text.JoinLiteral(" · ", timingTxt, schedule.NextText(task, bounds))
	}

	var machineErr *corev1.Text
	if snap != nil {
		if !text.IsEmpty(snap.ErrorText) {
			machineErr = snap.ErrorText
		} else if snap.Error != "" {
			machineErr = text.Plain(snap.Error)
		}
	}

	var readOnlyNotice *corev1.Text
	if !writable {
		readOnlyNotice = offlineOrUnsupportedReason(st, machineID)
	}

	// Attention banner.
	entry := schedule.TaskEntry{MachineID: machineID, Task: task}
	attView := &corev1.ScheduleAttentionView{}
	disabledAgent, disabledAgentName := disabledNewSessionAgent(st, entry)
	if disabledAgent != v1.Agent_AGENT_UNSPECIFIED {
		attView.ShowEnableCliAction = true
		attView.DisabledAgent = disabledAgent
	}
	if tg == schedule.GroupAttention {
		attView.Show = true
		if disabledAgentName != "" && strings.HasPrefix(task.Reason, "agent_disabled:") {
			attView.Message = text.Msg(text.KeyScheduleAgentDisabledPreRun, text.Str("machine", lists.MachineName(mac, machineID)), text.Str("agent", disabledAgentName))
		} else {
			attView.Message = schedule.AttentionBannerText(task, bounds)
		}
		targetMissing := schedule.TargetMissing(task)
		attView.ShowPrimaryAction = !targetMissing
		if !targetMissing {
			failedOnce := schedule.FailedOnce(task)
			sysCancelled := task.Status == "cancelled" && schedule.Visible(task)
			switch {
			case sysCancelled && schedule.OneShot(task):
				attView.PrimaryActionKind = corev1.ScheduleRecoveryActionKind_SCHEDULE_RECOVERY_ACTION_KIND_REARM_TIMING
				attView.PrimaryActionLabel = text.Msg(text.KeyScheduleResume)
			case sysCancelled:
				attView.PrimaryActionKind = corev1.ScheduleRecoveryActionKind_SCHEDULE_RECOVERY_ACTION_KIND_REACTIVATE_UPDATE
				attView.PrimaryActionLabel = text.Msg(text.KeyScheduleResume)
			case failedOnce:
				attView.PrimaryActionKind = corev1.ScheduleRecoveryActionKind_SCHEDULE_RECOVERY_ACTION_KIND_RUN_ONCE
				attView.PrimaryActionLabel = text.Msg(text.KeyScheduleRunOnceNow)
			default:
				attView.PrimaryActionKind = corev1.ScheduleRecoveryActionKind_SCHEDULE_RECOVERY_ACTION_KIND_RESUME
				attView.PrimaryActionLabel = text.Msg(text.KeyScheduleResume)
			}
			canRecover := writable && !busy && !frozen &&
				(task.Active == nil || task.Active.Status == "uncertain") &&
				task.ManualRequest == nil
			attView.PrimaryActionEnabled = canRecover
			if !canRecover {
				attView.PrimaryActionDisabledReason = actions.ToggleDisabledReason
			}
		}
	} else if disabledAgentName != "" {
		// Pre-run notice when target new_session CLI is already disabled before the next run fires.
		attView.Show = false
		attView.Message = text.Msg(text.KeyScheduleAgentDisabledPreRun, text.Str("machine", lists.MachineName(mac, machineID)), text.Str("agent", disabledAgentName))
	}

	// Active run.
	activeView := &corev1.ScheduleActiveRunView{}
	if task.Active != nil && schedule.IsInProgressStatus(task.Active.Status) {
		activeView.HasActive = true
		activeView.RunId = task.Active.ID
		activeView.SessionId = task.Active.SessionID
		activeView.FirstSeq = task.Active.FirstSeq()
		activeView.Status = mapRunStatus(task.Active.Status)
		activeView.StatusText = text.Msg(text.KeyScheduleRunningSince,
			text.ArgTimestamp(paramTime, schedule.RunTimeMs(task.Active), corev1.TimestampStyle_TIMESTAMP_STYLE_TIME),
		)
		activeView.CanOpenSession = task.Active.SessionID != ""
	}

	// Result pinning & resolution.
	allRuns := schedule.AllRuns(task)
	latestNotif := schedule.LatestNotification(task)
	var displayedResult *schedule.Run
	var detailCaptured bool
	var entryUnread map[string]bool
	var expandedQuiet map[string]bool
	var showAllRuns bool

	if ss.Detail != nil && ss.Detail.MachineID == machineID && ss.Detail.TaskID == taskID {
		detailCaptured = ss.Detail.ResultCaptured
		showAllRuns = ss.Detail.ShowAllRuns
		expandedQuiet = ss.Detail.ExpandedQuietGroups
		if detailCaptured {
			if ss.Detail.PinnedResultID != "" {
				for _, r := range allRuns {
					if r.ID == ss.Detail.PinnedResultID {
						displayedResult = r
						break
					}
				}
			}
			if displayedResult == nil && ss.Detail.PinnedResultID == "" {
				displayedResult = latestNotif
			}
		} else {
			displayedResult = latestNotif
		}
	} else {
		displayedResult = latestNotif
	}
	if ss.Seen.DetailCaptured(machineID, taskID) {
		entryUnread = ss.Seen.DetailUnread(machineID, taskID)
	}

	resultCard := &corev1.ScheduleResultCardView{}
	if displayedResult != nil {
		resultCard.HasResult = true
		resultCard.RunId = displayedResult.ID
		resultCard.SessionId = displayedResult.SessionID
		resultCard.FirstSeq = displayedResult.FirstSeq()
		resultCard.UnreadOnEntry = entryUnread[displayedResult.ID]
		resultCard.HasNewerResult = latestNotif != nil && latestNotif.ID != displayedResult.ID
		resultCard.TimeText = schedule.WhenText(schedule.NotificationTimeMs(displayedResult), bounds)
		resultCard.ResultTitle = schedule.RunResultTitle(displayedResult)
		resultCard.ShowConversationAction = displayedResult.SessionID != "" &&
			!(task.Spec.Target.Type == "resume_session" && activeView.HasActive)

		var answer string
		if displayedResult.SessionID != "" && ss.SessionEvents != nil {
			events, settled := ss.SessionEvents(displayedResult.SessionID)
			if !settled {
				resultCard.ResultLoading = true
			} else {
				answer = schedule.FinalAnswer(displayedResult.ID, events)
			}
		}
		switch {
		case answer != "":
			resultCard.MarkdownBody = answer
		case strings.TrimSpace(displayedResult.FinalSummary) != "":
			resultCard.MarkdownBody = displayedResult.FinalSummary
		case displayedResult.Reason != "":
			resultCard.FallbackReasonText = schedule.ReasonText(displayedResult.Reason, false)
		}
	}

	// Earlier runs history.
	var earlier []*schedule.Run
	var displayedMs int64
	if displayedResult != nil {
		displayedMs = schedule.NotificationTimeMs(displayedResult)
	}
	for _, r := range allRuns {
		if displayedResult != nil && r.ID == displayedResult.ID {
			continue
		}
		if task.Active != nil && r.ID == task.Active.ID {
			continue
		}
		if r.Notified && (displayedResult == nil || schedule.NotificationTimeMs(r) > displayedMs) {
			continue
		}
		earlier = append(earlier, r)
	}

	var groups []schedule.RunGroup
	if showAllRuns {
		groups = make([]schedule.RunGroup, 0, len(earlier))
		for _, r := range earlier {
			groups = append(groups, schedule.RunGroup{
				Key:   r.ID,
				Day:   bounds.DayBucketKey(schedule.NotificationTimeMs(r)),
				Runs:  []*schedule.Run{r},
				Quiet: false,
			})
		}
	} else {
		groups = schedule.RunGroups(earlier, bounds)
	}

	shownGroups := groups
	hasMoreEarlier := false
	if !showAllRuns && earlierLimit > 0 && len(groups) > earlierLimit {
		shownGroups = groups[:earlierLimit]
		hasMoreEarlier = true
	}

	groupViews := make([]*corev1.ScheduleHistoryGroupView, 0, len(shownGroups))
	for _, g := range shownGroups {
		gv := &corev1.ScheduleHistoryGroupView{
			GroupKey:    g.Key,
			FoldedQuiet: g.Quiet && len(g.Runs) > 1,
			Expanded:    expandedQuiet[g.Key],
		}
		if gv.FoldedQuiet && len(g.Runs) > 0 {
			latest := g.Runs[0]
			latestMs := schedule.NotificationTimeMs(latest)
			gv.FoldedTitle = text.Msg(text.KeyScheduleQuietRuns,
				text.Int("count", int64(len(g.Runs))),
				text.ArgText("day", schedule.DayText(latestMs, bounds)),
			)
			var foldedSum *corev1.Text
			if strings.TrimSpace(latest.FinalSummary) != "" {
				foldedSum = text.Plain(latest.FinalSummary)
			} else {
				foldedSum = text.Msg(text.KeyScheduleNoNotification)
			}
			gv.FoldedSummary = text.Msg(text.KeyScheduleLatestSummary,
				text.ArgText("summary", foldedSum),
				text.ArgTimestamp(paramTime, latestMs, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME),
			)
		}
		for _, r := range g.Runs {
			gv.Runs = append(gv.Runs, &corev1.ScheduleRunRowView{
				RunId:                  r.ID,
				SessionId:              r.SessionID,
				FirstSeq:               r.FirstSeq(),
				Status:                 mapRunStatus(r.Status),
				UnreadOnEntry:          entryUnread[r.ID],
				Manual:                 r.Manual,
				Notified:               r.Notified,
				NotificationSourceText: schedule.NotificationSourceText(r.NotificationSource),
				TimeText:               schedule.WhenText(schedule.NotificationTimeMs(r), bounds),
				SummaryText:            schedule.RunHistoryRowText(r),
				CanOpenSession:         r.SessionID != "",
			})
		}
		groupViews = append(groupViews, gv)
	}

	var earlierCountTxt, viewAllTxt *corev1.Text
	if len(earlier) > 0 {
		earlierCountTxt = text.Msg(text.KeyScheduleRunsCount, text.Int("count", int64(len(earlier))))
		if hasMoreEarlier {
			viewAllTxt = text.Msg(text.KeyScheduleViewAllRuns, text.Int("count", int64(len(earlier))))
		}
	}

	emptyHistory := !resultCard.HasResult && !activeView.HasActive && len(earlier) == 0
	var emptyHistoryTxt *corev1.Text
	if emptyHistory {
		emptyHistoryTxt = text.Msg(text.KeyScheduleHasNotRunYet)
	}

	limitsTxt := schedule.LimitsText(task.Spec.Trigger, bounds)
	targetType := corev1.ScheduleTargetType_SCHEDULE_TARGET_TYPE_NEW_SESSION
	var targetLbl, targetVal, permTxt *corev1.Text
	var canOpenTarget, permDanger, canEditPerm bool
	if task.Spec.Target.Type == "resume_session" {
		targetType = corev1.ScheduleTargetType_SCHEDULE_TARGET_TYPE_RESUME_SESSION
		targetLbl = text.Msg(text.KeyScheduleSendToSchedules)
		targetVal = sessionDisplayTitle(st, task.Spec.Target.Session)
		canOpenTarget = writable && !schedule.TargetMissing(task) && findSessionEntry(st, task.Spec.Target.Session) != nil
		if s := findSessionEntry(st, task.Spec.Target.Session); s != nil {
			m, _ := lists.SessionMode(s)
			permTxt = modeNameText(m)
			permDanger = m == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS
		}
		if text.IsEmpty(permTxt) {
			permTxt = text.Msg(text.KeyScheduleAndroidFollowConversation)
		}
	} else {
		targetLbl = text.Msg(text.KeyScheduleRunIn)
		targetVal = taskLocationText(st, entry)
		m := approvalModeFromString(task.Spec.Target.Mode)
		permTxt = modeNameText(m)
		if text.IsEmpty(permTxt) {
			permTxt = text.Msg(text.KeyNewSessionFollowCli)
		}
		permDanger = m == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS
		canEditPerm = actions.CanEditDefinition
	}

	return &corev1.ScheduleDetailViewModel{
		Found:                true,
		Loading:              snap != nil && snap.Loading,
		MachineId:            machineID,
		TaskId:               taskID,
		Revision:             task.Revision,
		Title:                task.Spec.Title,
		Subtitle:             subtitle,
		Connection:           buildConnectionStatus(st),
		MachineOnline:        lists.MachineReachable(mac),
		MachineSupported:     supported,
		ReadOnly:             !writable,
		ReadOnlyNotice:       readOnlyNotice,
		MachineError:         machineErr,
		CanRetryMachineList:  writable,
		Mutation:             mutationView(mut),
		Attention:            attView,
		ActiveRun:            activeView,
		ResultCard:           resultCard,
		EarlierGroups:        groupViews,
		TotalEarlierRuns:     int32(len(earlier)),
		HasMoreEarlierRuns:   hasMoreEarlier,
		ShowAllRuns:          showAllRuns,
		EarlierCountText:     earlierCountTxt,
		ViewAllText:          viewAllTxt,
		EmptyHistory:         emptyHistory,
		EmptyHistoryText:     emptyHistoryTxt,
		Message:              task.Spec.Message,
		TimingText:           timingTxt,
		NotifyOnFinish:       task.Spec.NotifyOnFinish(),
		HasLimits:            !text.IsEmpty(limitsTxt),
		LimitsText:           limitsTxt,
		TargetType:           targetType,
		TargetSessionId:      task.Spec.Target.Session,
		TargetLabel:          targetLbl,
		TargetValue:          targetVal,
		CanOpenTargetSession: canOpenTarget,
		PermissionsText:      permTxt,
		PermissionsIsDanger:  permDanger,
		CanEditPermissions:   canEditPerm,
		TaskName:             task.Spec.Title,
		CreationFooter:       schedule.CreationFooterText(task, bounds),
		RunOnceFooter:        text.Msg(text.KeyScheduleAndroidRunOnceNowDoesNotCountTowardAutomaticRunsOr),
		Source:               mapScheduleSource(task.Source),
		Takeover:             mapTakeoverView(task),
		ScopedSessionId:      task.ScopedSession,
		ScopedMode:           task.ScopedMode,
		Actions:              actions,
		MenuSubtitle:         taskMenuSubtitle(st, entry, bounds),
		InitialLoaded:        st.World.MachinesListed || st.World.SessionsListed || st.World.PrefsLoaded,
	}
}

func buildTimingDraftView(d schedule.TimingDraft, errTxt *corev1.Text, applied bool) *corev1.ScheduleTimingDraftView {
	var afterHint, intervalHint *corev1.Text
	if d.AfterTurnUnselected && d.Original != nil {
		afterHint = text.Msg(text.KeyScheduleAndroidCurrentlyValue1FinishingDirectlyPreservesTheOriginalRuleChooseA,
			text.ArgText("value1", schedule.FrequencyText(*d.Original, schedule.CalendarBounds{})),
		)
	}
	if d.Kind == schedule.TimingInterval {
		intervalHint = text.Msg(text.KeyScheduleTheFirstRunIsAfterAFullIntervalTo)
	}
	weekdays := make([]int32, 0, len(d.Weekdays))
	for _, wd := range d.Weekdays {
		weekdays = append(weekdays, int32(wd))
	}
	doneEnabled := d.AfterTurnUnselected || d.Kind != schedule.TimingWeekly || len(d.Weekdays) > 0
	showJitter := d.Kind == schedule.TimingDaily || d.Kind == schedule.TimingWeekly
	var jitterLabel, jitterHint *corev1.Text
	var jitterOptions []*corev1.ScheduleJitterOptionView
	if showJitter {
		jitterLabel = text.Msg(text.KeyScheduleJitterLabel)
		jitterHint = text.Msg(text.KeyScheduleJitterHint)
		selected := d.JitterMinutes
		if selected < 0 {
			selected = 0
		}
		mins := schedule.JitterOptionMinutes(selected)
		jitterOptions = make([]*corev1.ScheduleJitterOptionView, 0, len(mins))
		for _, m := range mins {
			var lbl *corev1.Text
			if m == 0 {
				lbl = text.Msg(text.KeyScheduleJitterNone)
			} else {
				lbl = text.Msg(text.KeyScheduleJitterWithinMinutes, text.Int("count", m))
			}
			jitterOptions = append(jitterOptions, &corev1.ScheduleJitterOptionView{
				Minutes:  m,
				Label:    lbl,
				Selected: m == selected,
			})
		}
	}
	return &corev1.ScheduleTimingDraftView{
		Kind:                mapTimingKindProto(d.Kind),
		AfterTurnUnselected: d.AfterTurnUnselected,
		AfterTurnHint:       afterHint,
		Date:                d.Date,
		Time:                d.Time,
		Zone:                d.Zone,
		Workdays:            d.Workdays,
		Weekdays:            weekdays,
		Interval:            d.Interval,
		Unit:                mapIntervalUnitProto(d.Unit),
		End:                 mapTimingEndProto(d.End),
		Count:               d.Count,
		UntilDate:           d.UntilDate,
		UntilMs:             d.UntilMs,
		ShowIntervalHint:    d.Kind == schedule.TimingInterval,
		IntervalHint:        intervalHint,
		TimingError:         errTxt,
		TimingDoneEnabled:   doneEnabled,
		TimingApplied:       applied,
		ShowJitter:          showJitter,
		JitterOptions:       jitterOptions,
		JitterLabel:         jitterLabel,
		JitterHint:          jitterHint,
	}
}

// BuildScheduleForm constructs the ScheduleFormViewModel for create or edit drafts.
func BuildScheduleForm(st *State, formKey string) *corev1.ScheduleFormViewModel {
	if st == nil {
		st = NewState(nil)
	}
	ss := ensureScheduleState(st)
	bounds := scheduleBounds(st)

	key := formKey
	if key == "" {
		key = ss.ActiveFormKey
	}
	form := ss.Forms[key]
	if form == nil {
		return &corev1.ScheduleFormViewModel{
			FormKey: key,
			Open:    false,
		}
	}

	isCreate := form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW ||
		form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_FOR_SESSION
	isSessionCreate := form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_FOR_SESSION
	isNewSessionConfig := form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW ||
		form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_TARGET ||
		form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_MODE

	mutKey := "form:" + form.FormKey
	if !isCreate && form.MachineID != "" && form.TaskID != "" {
		mutKey = "op:" + form.MachineID + "/" + form.TaskID
	}
	mut := ss.Mutations[mutKey]
	var busy, frozen bool
	if mut != nil {
		busy = mut.Busy
		frozen = mut.Frozen
	}
	editable := !busy && !frozen

	msgValid := schedule.MessageValid(form.Message)
	msgTooLong := len(form.Message) > schedule.MessageMaxBytes
	var msgErr *corev1.Text
	if msgTooLong {
		msgErr = text.Msg(text.KeyScheduleTooLongMaximum32Kb)
	}
	autoTitle := schedule.ResolveName("", form.Message)
	var titlePH *corev1.Text
	if strings.TrimSpace(form.Message) == "" {
		titlePH = text.Msg(text.KeyScheduleLeaveBlankToUseTheMessageSFirstSentence)
	} else {
		titlePH = text.Msg(text.KeyScheduleIfBlankUses, text.Str("value", autoTitle))
	}

	// Project & Machine & Agent & Model & Mode resolution.
	projRes := lists.BuildProjects(&st.World, "", st.RememberedScope, st.RememberedScopeToken, nil, st.Scope)
	var projChoices []*corev1.ScheduleProjectOptionView
	for _, p := range projRes.Listed {
		if p == nil || p.Hidden {
			continue
		}
		pn := text.Plain(p.Name)
		if p.Name == "" {
			pn = text.Msg(text.KeyProjectsUnknown)
		}
		projChoices = append(projChoices, &corev1.ScheduleProjectOptionView{
			ProjectKey: p.Key,
			Name:       pn,
			ShortPath:  p.ShortPath,
			Selected:   p.Key == form.ProjectKey,
		})
	}

	var projectNameTxt *corev1.Text
	var projectShortPath string
	if form.ProjectKey != "" {
		if pn := lists.ProjectName(form.ProjectKey); pn != "" {
			projectNameTxt = text.Plain(pn)
		} else {
			projectNameTxt = text.Plain(form.ProjectKey)
		}
		projectShortPath = lists.ShortenPath(form.ProjectKey, "", "")
	} else {
		projectNameTxt = text.Msg(text.KeyScheduleAndroidChooseAProject)
	}

	cfg := form.Config
	effMachineID := form.MachineID
	if effMachineID == "" {
		effMachineID = cfg.MachineID
	}
	if isSessionCreate {
		if s := findSessionEntry(st, form.SessionID); s != nil {
			effMachineID = s.MachineID
		}
	}

	var startSettled = true
	if form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW && form.ProjectKey != "" {
		var pickMac string
		if form.MachineChosen || form.ConfigChosen {
			pickMac = cfg.MachineID
		}
		var pickAgent v1.Agent
		if form.AgentOverride != v1.Agent_AGENT_UNSPECIFIED {
			pickAgent = form.AgentOverride
		} else if form.ConfigChosen {
			pickAgent = cfg.Agent
		}
		res := lists.ResolveProjectStart(&st.World, form.ProjectKey, pickMac, pickAgent, nil, false)
		startSettled = res.Settled
		if !form.ConfigInitialized && res.Found {
			cfg = res.Config
			cfg.Agent = lists.EffectiveAgent(cfg.Agent)
			effMachineID = cfg.MachineID
		}
	}
	cfg.Agent = lists.EffectiveAgent(cfg.Agent)

	mac := findMachine(st, effMachineID)
	machineName := lists.MachineName(mac, effMachineID)
	var mMeta *v1.MachineMeta
	if mac != nil {
		mMeta = mac.Meta
	}
	installedAgents := lists.MachineAgents(mMeta)

	// Machine choices.
	lockMachine := !isCreate || isSessionCreate
	machineChoices := make([]*corev1.ScheduleMachineOptionView, 0, len(st.World.Machines))
	for _, m := range st.World.Machines {
		if m == nil {
			continue
		}
		sup := schedule.Supported(m, st.World.ClientVersion)
		onl := lists.MachineReachable(m)
		var note *corev1.Text
		if !sup {
			note = text.Msg(text.KeyScheduleUpdateRemoteAgentOnThisComputerToUseScheduled)
		} else if !onl {
			note = text.Msg(text.KeyScheduleTheComputerIsOfflineConnectToCreateTasks)
		}
		machineChoices = append(machineChoices, &corev1.ScheduleMachineOptionView{
			MachineId: m.ID,
			Name:      lists.MachineName(m, m.ID),
			Online:    onl,
			Supported: sup,
			Selected:  m.ID == effMachineID,
			Note:      note,
		})
	}

	agentOpts := make([]*corev1.AgentOptionView, 0, len(installedAgents))
	for _, a := range installedAgents {
		agentOpts = append(agentOpts, &corev1.AgentOptionView{
			Agent:       a,
			DisplayName: lists.AgentInfo(a).Name,
			Installed:   true,
			Selected:    a == cfg.Agent,
		})
	}

	modelsEntry := st.MachineModels[MachineAgentKey{MachineID: effMachineID, Agent: cfg.Agent}]
	settingsEntry := st.MachineSettings[effMachineID]
	var modelsResp *v1.ListModelsResponse
	var hasModels, modelsLoading bool
	var modelsErrTxt *corev1.Text
	if modelsEntry != nil {
		modelsResp = modelsEntry.Response
		hasModels = modelsEntry.Loaded && modelsResp != nil
		modelsLoading = modelsEntry.Loading
		modelsErrTxt = modelsEntry.ErrorText
		if text.IsEmpty(modelsErrTxt) && modelsEntry.Error != "" {
			modelsErrTxt = text.Plain(modelsEntry.Error)
		}
	}
	var daemonSettings *v1.DaemonSettings
	if settingsEntry != nil && settingsEntry.Loaded {
		daemonSettings = settingsEntry.Settings
	}
	var modelList []*v1.ModelInfo
	if hasModels {
		modelList = modelsResp.Models
	}
	pickedModel := lists.ProjectModel(cfg, modelsResp, daemonSettings)
	efforts, selectedEffort := lists.EffortChoices(cfg, modelsResp, daemonSettings)

	defaultSlug := lists.DefaultsFor(daemonSettings, cfg.Agent).Model
	if defaultSlug == "" && modelsResp != nil {
		defaultSlug = modelsResp.DefaultModel
	}
	defaultModelName := lists.ModelLabel(modelList, hasModels, defaultSlug, cfg.Agent)
	var defaultModelLabel *corev1.Text
	if defaultModelName != "" {
		defaultModelLabel = text.Msg(text.KeyModelsDefaultNamed, text.Str("model", defaultModelName))
	} else {
		defaultModelLabel = text.Msg(text.KeyModelsDefault)
	}
	var modelLabel *corev1.Text
	if cfg.Model == "" {
		modelLabel = text.Msg(text.KeyModelsDefault)
	} else {
		modelLabel = text.Plain(lists.ModelLabel(modelList, hasModels, cfg.Model, cfg.Agent))
	}
	modelOpts := make([]*corev1.ModelOptionView, 0, len(modelList))
	for _, mi := range modelList {
		if mi == nil || mi.Slug == "" {
			continue
		}
		var fixedNote *corev1.Text
		if len(mi.Efforts) == 1 && mi.Efforts[0] != "" {
			fixedNote = text.Msg(text.KeyModelsFixedEffort, text.Str("effort", mi.Efforts[0]))
		}
		dispName := mi.DisplayName
		if dispName == "" {
			dispName = lists.AgentModelLabel(cfg.Agent, mi.Slug)
		}
		modelOpts = append(modelOpts, &corev1.ModelOptionView{
			Slug:            mi.Slug,
			DisplayName:     dispName,
			FixedEffortNote: fixedNote,
			Checked:         cfg.Model != "" && mi.Slug == cfg.Model,
		})
	}
	_ = pickedModel

	panelEnabled := editable && (len(modelList) > 0 || !modelsLoading)
	modelPanel := &corev1.ScheduleModelPanelView{
		ModelsLoading:       modelsLoading,
		ModelsError:         modelsErrTxt,
		DefaultModelLabel:   defaultModelLabel,
		DefaultModelChecked: cfg.Model == "",
		Models:              modelOpts,
		EffortChoices:       efforts,
		SelectedEffort:      selectedEffort,
		PanelEnabled:        panelEnabled,
	}

	dm := lists.DefaultModeOf(mac, cfg.Agent)
	effectiveMode := cfg.Mode
	if effectiveMode == v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		effectiveMode = dm.Mode
	}
	planBlocked := isNewSessionConfig && effectiveMode == v1.ApprovalMode_APPROVAL_MODE_PLAN
	modeMissing := isNewSessionConfig && effectiveMode == v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED

	var modeLabel *corev1.Text
	if effectiveMode != v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		modeLabel = modeNameText(effectiveMode)
	}
	if text.IsEmpty(modeLabel) {
		modeLabel = text.Msg(text.KeyScheduleSelectPermissions)
	}
	modeChoices := []*corev1.ScheduleModeOptionView{
		{
			Mode:     v1.ApprovalMode_APPROVAL_MODE_ASK,
			Label:    text.Msg(text.KeyModesAsk),
			Selected: effectiveMode == v1.ApprovalMode_APPROVAL_MODE_ASK,
			Enabled:  true,
		},
		{
			Mode:     v1.ApprovalMode_APPROVAL_MODE_AUTO,
			Label:    text.Msg(text.KeyModesAuto),
			Selected: effectiveMode == v1.ApprovalMode_APPROVAL_MODE_AUTO,
			Enabled:  true,
		},
		{
			Mode:     v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS,
			Label:    text.Msg(text.KeyModesFullAccess),
			IsDanger: true,
			Selected: effectiveMode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS,
			Enabled:  true,
		},
		{
			Mode:           v1.ApprovalMode_APPROVAL_MODE_PLAN,
			Label:          text.Msg(text.KeyModesPlan),
			Selected:       effectiveMode == v1.ApprovalMode_APPROVAL_MODE_PLAN,
			Enabled:        false,
			DisabledReason: text.Msg(text.KeySchedulePlanModeCannotCreateExecutionTasksChooseOtherPermissions),
		},
	}

	// Notices & submit readiness.
	supported := schedule.Supported(mac, st.World.ClientVersion)
	writable := schedule.Writable(&st.World, effMachineID)

	var notices []*corev1.ScheduleFormNoticeView
	if effMachineID != "" && !supported {
		notices = append(notices, &corev1.ScheduleFormNoticeView{
			Kind:     corev1.ScheduleFormNoticeKind_SCHEDULE_FORM_NOTICE_KIND_UNSUPPORTED_MACHINE,
			Severity: corev1.ScheduleFormNoticeSeverity_SCHEDULE_FORM_NOTICE_SEVERITY_WARN,
			Text:     text.Msg(text.KeyScheduleUpdateRemoteAgentOnThisComputerToUseScheduled),
		})
	} else if effMachineID != "" && !writable {
		notices = append(notices, &corev1.ScheduleFormNoticeView{
			Kind:     corev1.ScheduleFormNoticeKind_SCHEDULE_FORM_NOTICE_KIND_MACHINE_OFFLINE,
			Severity: corev1.ScheduleFormNoticeSeverity_SCHEDULE_FORM_NOTICE_SEVERITY_WARN,
			Text:     text.Msg(text.KeyScheduleTheComputerIsOfflineConnectToCreateTasks),
		})
	}
	if isNewSessionConfig && !startSettled {
		notices = append(notices, &corev1.ScheduleFormNoticeView{
			Kind:     corev1.ScheduleFormNoticeKind_SCHEDULE_FORM_NOTICE_KIND_READING_PREVIOUS,
			Severity: corev1.ScheduleFormNoticeSeverity_SCHEDULE_FORM_NOTICE_SEVERITY_INFO,
			Text:     text.Msg(text.KeyNewSessionReadingPrevious),
		})
	}
	if planBlocked {
		notices = append(notices, &corev1.ScheduleFormNoticeView{
			Kind:     corev1.ScheduleFormNoticeKind_SCHEDULE_FORM_NOTICE_KIND_PLAN_MODE_BLOCKED,
			Severity: corev1.ScheduleFormNoticeSeverity_SCHEDULE_FORM_NOTICE_SEVERITY_WARN,
			Text:     text.Msg(text.KeySchedulePlanModeCannotCreateExecutionTasksChooseOtherPermissions),
		})
	}
	if modeMissing && form.ProjectKey != "" {
		notices = append(notices, &corev1.ScheduleFormNoticeView{
			Kind:     corev1.ScheduleFormNoticeKind_SCHEDULE_FORM_NOTICE_KIND_MODE_REQUIRED,
			Severity: corev1.ScheduleFormNoticeSeverity_SCHEDULE_FORM_NOTICE_SEVERITY_INFO,
			Text:     text.Msg(text.KeyScheduleSelectExplicitPermissionsBeforeCreatingAScheduledTask),
		})
	}
	if isSessionCreate && form.Trigger.Type == "interval" {
		notices = append(notices, &corev1.ScheduleFormNoticeView{
			Kind:     corev1.ScheduleFormNoticeKind_SCHEDULE_FORM_NOTICE_KIND_INTERVAL_HINT,
			Severity: corev1.ScheduleFormNoticeSeverity_SCHEDULE_FORM_NOTICE_SEVERITY_INFO,
			Text:     text.Msg(text.KeyScheduleTheFirstRunIsAfterAFullIntervalTo),
		})
	}
	if isCreate {
		hint := text.Msg(text.KeyScheduleEachRunOpensANewConversationGroupedUnderThisSchedules)
		if isSessionCreate {
			hint = text.Msg(text.KeyScheduleMessagesWillGoToTheCurrentConversationAndWait)
		}
		notices = append(notices, &corev1.ScheduleFormNoticeView{
			Kind:     corev1.ScheduleFormNoticeKind_SCHEDULE_FORM_NOTICE_KIND_TARGET_INFO_HINT,
			Severity: corev1.ScheduleFormNoticeSeverity_SCHEDULE_FORM_NOTICE_SEVERITY_INFO,
			Text:     hint,
		})
	}
	if frozen {
		msg := text.Msg(text.KeyScheduleThePreviousOperationSResultIsUnconfirmedVerifyThe)
		if isCreate {
			msg = text.Msg(text.KeyScheduleThePreviousCreationResultIsUnconfirmedRetryingUsesThe)
		}
		notices = append(notices, &corev1.ScheduleFormNoticeView{
			Kind:     corev1.ScheduleFormNoticeKind_SCHEDULE_FORM_NOTICE_KIND_FROZEN_ATTEMPT,
			Severity: corev1.ScheduleFormNoticeSeverity_SCHEDULE_FORM_NOTICE_SEVERITY_WARN,
			Text:     msg,
		})
	}

	canSubmit := true
	var disabledReason *corev1.Text
	switch {
	case busy:
		canSubmit = false
		disabledReason = text.Msg(text.KeyScheduleReadOnlyBusy)
	case mut != nil && mut.Expired:
		canSubmit = false
		disabledReason = text.Msg(text.KeyErrorsThePreviousOperationSOutcomeIsStillUnknownReload)
	case !writable && !frozen:
		canSubmit = false
		disabledReason = offlineOrUnsupportedReason(st, effMachineID)
	case (form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW ||
		form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_FOR_SESSION ||
		form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_MESSAGE) && !msgValid && form.FrozenSpec == nil:
		canSubmit = false
		if msgTooLong {
			disabledReason = msgErr
		} else {
			disabledReason = text.Msg(text.KeyScheduleMessageRequired)
		}
	case isNewSessionConfig && form.ProjectKey == "":
		canSubmit = false
		disabledReason = text.Msg(text.KeyScheduleProjectRequired)
	case isNewSessionConfig && planBlocked:
		canSubmit = false
		disabledReason = text.Msg(text.KeySchedulePlanModeCannotCreateExecutionTasksChooseOtherPermissions)
	case isNewSessionConfig && modeMissing:
		canSubmit = false
		disabledReason = text.Msg(text.KeyScheduleSelectExplicitPermissionsBeforeCreatingAScheduledTask)
	case form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW && !startSettled && !form.ConfigChosen:
		canSubmit = false
		disabledReason = text.Msg(text.KeyNewSessionReadingPrevious)
	case !isCreate && form.BaseTask != nil && form.BaseTask.Active != nil &&
		form.Mode != corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_RENAME:
		canSubmit = false
		disabledReason = text.Msg(text.KeyScheduleReadOnlyActiveRun)
	}

	var submitLabel *corev1.Text
	if isCreate {
		switch {
		case busy:
			submitLabel = text.Msg(text.KeyScheduleCreating)
		case frozen:
			submitLabel = text.Msg(text.KeyScheduleRetryCreation)
		default:
			submitLabel = text.Msg(text.KeyScheduleCreate)
		}
	} else {
		switch {
		case busy:
			submitLabel = text.Msg(text.KeyScheduleAndroidSaving)
		case frozen:
			submitLabel = text.Msg(text.KeyScheduleRetrySameRequest)
		default:
			submitLabel = text.Msg(text.KeyCommonSave)
		}
	}

	var locationTxt *corev1.Text
	if isNewSessionConfig && form.ProjectKey != "" {
		pn := lists.ProjectName(form.ProjectKey)
		aName := lists.AgentInfo(cfg.Agent).Name
		multiMach := len(st.World.Machines) > 1
		multiCLI := len(installedAgents) > 1
		switch {
		case multiMach && multiCLI:
			locationTxt = text.Msg(text.KeyNewSessionLocationMachineCli, text.Str("machine", machineName), text.Str("project", pn), text.Str("cli", aName))
		case multiMach:
			locationTxt = text.Msg(text.KeyNewSessionLocationMachine, text.Str("machine", machineName), text.Str("project", pn))
		case multiCLI:
			locationTxt = text.Msg(text.KeyNewSessionLocationCli, text.Str("project", pn), text.Str("cli", aName))
		default:
			locationTxt = text.Msg(text.KeyNewSessionLocation, text.Str("project", pn))
		}
	}

	return &corev1.ScheduleFormViewModel{
		FormKey:                   form.FormKey,
		Mode:                      form.Mode,
		Open:                      form.Open,
		MachineId:                 effMachineID,
		TaskId:                    form.TaskID,
		SessionId:                 form.SessionID,
		ProjectKey:                form.ProjectKey,
		LockMachine:               lockMachine,
		Message:                   form.Message,
		MessageValid:              msgValid,
		MessageTooLong:            msgTooLong,
		MessageError:              msgErr,
		Title:                     form.Title,
		TitlePlaceholder:          titlePH,
		AutoTitle:                 autoTitle,
		NotifyOnFinish:            form.NotifyOnFinish,
		TimingSummary:             schedule.TimingText(form.Trigger, bounds),
		Timing:                    buildTimingDraftView(form.Timing, form.TimingError, form.TimingApplied),
		ProjectName:               projectNameTxt,
		ProjectShortPath:          projectShortPath,
		HasProject:                form.ProjectKey != "",
		ProjectChoices:            projChoices,
		MachineName:               machineName,
		MachineSelectable:         !lockMachine && len(st.World.Machines) > 1,
		MachineChoices:            machineChoices,
		Agent:                     cfg.Agent,
		AgentName:                 lists.AgentInfo(cfg.Agent).Name,
		AvailableAgents:           agentOpts,
		Model:                     cfg.Model,
		Effort:                    selectedEffort,
		ModelLabel:                modelLabel,
		EffortLabel:               selectedEffort,
		ModelPanel:                modelPanel,
		ApprovalMode:              effectiveMode,
		ApprovalModeLabel:         modeLabel,
		ApprovalModeIsDanger:      effectiveMode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS,
		ModeChoices:               modeChoices,
		RequiresFullAccessConfirm: isNewSessionConfig && effectiveMode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS && !frozen,
		LocationText:              locationTxt,
		Editable:                  editable,
		CanSubmit:                 canSubmit,
		SubmitLabel:               submitLabel,
		SubmitDisabledReason:      disabledReason,
		HasUnsavedChanges:         form.Dirty,
		Notices:                   notices,
		Mutation:                  mutationView(mut),
	}
}

type schedSessionStripTasks []schedule.TaskEntry

func (s schedSessionStripTasks) Len() int      { return len(s) }
func (s schedSessionStripTasks) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s schedSessionStripTasks) Less(i, j int) bool {
	na, nb := s[i].Task.NextRunAt, s[j].Task.NextRunAt
	if na == "" {
		na = "~"
	}
	if nb == "" {
		nb = "~"
	}
	if na != nb {
		return na < nb
	}
	if s[i].MachineID != s[j].MachineID {
		return s[i].MachineID < s[j].MachineID
	}
	return s[i].Task.ID < s[j].Task.ID
}

// BuildScheduleSessionStrip constructs the ScheduleSessionStripViewModel for one conversation.
func BuildScheduleSessionStrip(st *State, sessionID string) *corev1.ScheduleSessionStripViewModel {
	if st == nil || sessionID == "" {
		return &corev1.ScheduleSessionStripViewModel{SessionId: sessionID}
	}
	ss := ensureScheduleState(st)
	bounds := scheduleBounds(st)

	var matching []schedule.TaskEntry
	for _, m := range st.World.Machines {
		if m == nil || !schedule.Supported(m, st.World.ClientVersion) {
			continue
		}
		snap := ss.Machines[m.ID]
		if snap == nil {
			continue
		}
		for _, t := range snap.Tasks {
			if !schedule.Visible(t) || schedule.GroupOf(t) == schedule.GroupEnded {
				continue
			}
			if t.Spec.Target.Type == "resume_session" && t.Spec.Target.Session == sessionID {
				matching = append(matching, schedule.TaskEntry{MachineID: m.ID, Task: t})
			}
		}
	}
	if len(matching) == 0 {
		return &corev1.ScheduleSessionStripViewModel{SessionId: sessionID, Visible: false}
	}

	sortutil.SortInterface(schedSessionStripTasks(matching))

	first := matching[0]
	writable := schedule.Writable(&st.World, first.MachineID)
	var stateTxt *corev1.Text
	if !writable {
		stateTxt = text.Msg(text.KeyScheduleNotConnectedShowingThePreviousState)
	} else {
		stateTxt = schedule.NextText(first.Task, bounds)
	}

	var summaryTxt *corev1.Text
	if len(matching) == 1 {
		summaryTxt = text.Msg(text.KeyScheduleStripSingle,
			text.ArgText("frequency", schedule.FrequencyText(first.Task.Spec.Trigger, bounds)),
			text.ArgText("state", stateTxt),
		)
	} else {
		summaryTxt = text.Msg(text.KeyScheduleStripCount,
			text.Int("count", int64(len(matching))),
			text.ArgText("state", stateTxt),
		)
	}

	taskViews := make([]*corev1.ScheduleSessionStripTaskView, 0, len(matching))
	for _, e := range matching {
		taskViews = append(taskViews, &corev1.ScheduleSessionStripTaskView{
			MachineId:   e.MachineID,
			TaskId:      e.Task.ID,
			Title:       e.Task.Spec.Title,
			TimingText:  schedule.TimingText(e.Task.Spec.Trigger, bounds),
			NextRunText: schedule.NextText(e.Task, bounds),
			HasUnread:   ss.Seen.HasNewResult(e.MachineID, e.Task),
			Status:      mapTaskStatus(e.Task.Status),
		})
	}

	return &corev1.ScheduleSessionStripViewModel{
		SessionId:      sessionID,
		Visible:        true,
		Count:          int32(len(matching)),
		SummaryText:    summaryTxt,
		Tasks:          taskViews,
		MachineOffline: !writable,
	}
}

func disabledNewSessionAgent(st *State, e schedule.TaskEntry) (v1.Agent, string) {
	if st == nil || e.Task == nil || e.Task.Spec.Target.Type != "new_session" {
		return v1.Agent_AGENT_UNSPECIFIED, ""
	}
	if e.Task.Status == "completed" || e.Task.Status == "cancelled" {
		return v1.Agent_AGENT_UNSPECIFIED, ""
	}
	mac := findMachine(st, e.MachineID)
	if mac == nil || mac.Meta == nil {
		return v1.Agent_AGENT_UNSPECIFIED, ""
	}
	ag := agentByIDString(e.Task.Spec.Target.Agent)
	if lists.IsAgentDisabled(mac.Meta, ag) {
		return ag, lists.AgentInfo(ag).Name
	}
	return v1.Agent_AGENT_UNSPECIFIED, ""
}

func disabledNewSessionAgentName(st *State, e schedule.TaskEntry) string {
	_, name := disabledNewSessionAgent(st, e)
	return name
}
