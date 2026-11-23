package screens

import (
	"strconv"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/projection"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	"github.com/Lewin671/remote-agent/clientcore/domain/usage"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	// DefaultSessionWindowLimit bounds initial tail rendering when callers omit limit.
	DefaultSessionWindowLimit = 80
	highContextThresholdPct   = 80
	turnDurationMinMs         = 1000
	minutesHourSec            = 3600
)

// SessionCommandRecord tracks one command slot's submission and settle lifecycle.
type SessionCommandRecord struct {
	_             [0]func()
	SlotKey       string
	RequestID     string
	Status        corev1.SessionCommandStatus
	Busy          bool
	Frozen        bool
	Expired       bool
	ErrorCode     string
	ErrorText     *corev1.Text
	Command       *v1.Command
	SubmittedAtMs int64
}

// SessionOutgoingState is Core's internal pre-projection delivery state for a client send.
type SessionOutgoingState uint8

const (
	OutgoingSending SessionOutgoingState = iota
	OutgoingSent
	OutgoingFailed
	OutgoingUnknown
)

// CancelIntentKind identifies the follow-up action after a CancelInput command settles.
type CancelIntentKind uint8

const (
	CancelIntentCancel CancelIntentKind = iota
	CancelIntentCancelAll
	CancelIntentPromote
	CancelIntentRestore
)

// SessionCancelIntent holds message content across a pending CancelInput command.
type SessionCancelIntent struct {
	Kind            CancelIntentKind
	BatchID         string
	SourceReqID     string
	Text            string
	ImageIDs        []string
	FileIDs         []string
	Mode            v1.SendMode
	HasLocalContent bool
	HasAttachments  bool
}

// LocalSentContent retains local text/attachments for retry, promote, restore, and 5-second undo.
type LocalSentContent struct {
	RequestID string
	Text      string
	ImageIDs  []string
	FileIDs   []string
	Mode      v1.SendMode
}

// SessionUndoItem records one canceled message that can be re-sent within the 5-second undo window.
type SessionUndoItem struct {
	SourceReqID string
	Text        string
	ImageIDs    []string
	FileIDs     []string
	Mode        v1.SendMode
}

// SessionUndoBatch holds the active 5-second undo batch for a session.
type SessionUndoBatch struct {
	BatchID string
	Items   []*SessionUndoItem
}

// SessionOutgoingRecord tracks one outgoing user message from send until projection/dismissal.
type SessionOutgoingRecord struct {
	RequestID      string
	SlotKey        string
	Text           string
	ImageIDs       []string
	FileIDs        []string
	Mode           v1.SendMode
	State          SessionOutgoingState
	Frozen         bool
	Expired        bool
	ErrorCode      string
	ErrorText      *corev1.Text
	Restored       bool
	CreatedMs      int64
	VisibleAfterMs int64
}

// PendingStartPrompt captures the initial prompt passed to CreateSession until recorded in timeline.
type PendingStartPrompt struct {
	RequestID  string
	Text       string
	ImageCount int32
	FileCount  int32
	CreatedMs  int64
}

// SessionUIState holds per-session client UI state owned by Core (slots, outgoing sends, tray actions, dock steps).
type SessionUIState struct {
	Commands               map[string]*SessionCommandRecord
	Outgoing               []*SessionOutgoingRecord
	LocalContents          map[string]*LocalSentContent
	CancelIntents          map[string]*SessionCancelIntent
	PendingResendSources   map[string]string
	DismissedInputIDs      map[string]bool
	SteerVisibleAfterMs    map[string]int64
	SteerGraceNextTargetMs int64
	Undo                   *SessionUndoBatch
	QuestionIndexByID      map[string]int32
	PendingStartPrompt     *PendingStartPrompt
	LastApprovalSeq        map[string]uint64
	LastQuestionSeq        map[string]uint64
	LastPlanSeq            uint64
	LastPlanHash           []byte
	LookupError            string
	timeline               *timelineCache
}

// ClearTimelineCache releases cached timeline items when the underlying event buffer is evicted.
func (ui *SessionUIState) ClearTimelineCache() {
	if ui != nil {
		ui.timeline = nil
	}
}

// HasTimelineCache reports whether ui currently retains cached timeline items.
func (ui *SessionUIState) HasTimelineCache() bool {
	return ui != nil && ui.timeline != nil
}

// NewSessionUIState allocates an initialized SessionUIState.
func NewSessionUIState() *SessionUIState {
	return &SessionUIState{
		Commands:             make(map[string]*SessionCommandRecord),
		LocalContents:        make(map[string]*LocalSentContent),
		CancelIntents:        make(map[string]*SessionCancelIntent),
		PendingResendSources: make(map[string]string),
		DismissedInputIDs:    make(map[string]bool),
		SteerVisibleAfterMs:  make(map[string]int64),
		QuestionIndexByID:    make(map[string]int32),
		LastApprovalSeq:      make(map[string]uint64),
		LastQuestionSeq:      make(map[string]uint64),
	}
}

// BuildSessionView constructs the full SessionViewModel from sync state, world state, and UI state.
func BuildSessionView(
	in world.SessionSnapshot,
	st *State,
	req *corev1.SessionScreenRequest,
	ui *SessionUIState,
) *corev1.SessionViewModel {
	if ui == nil {
		ui = NewSessionUIState()
	}
	var sid string
	var win *corev1.SessionWindowRequest
	if req != nil {
		sid = req.SessionId
		win = req.Window
	}

	ls := findListSession(st, sid, in.Session)
	lm := findListMachine(st, ls, in.Machine)
	agent := sessionEffectiveAgent(ls, in.Session)
	caps := lists.AgentInfo(agent)

	var modelsResp *v1.ListModelsResponse
	var hasModels, modelsLoading bool
	var modelsErr string
	var modelsErrText *corev1.Text
	if ls != nil && ls.MachineID != "" {
		if entry := st.MachineModels[MachineAgentKey{MachineID: ls.MachineID, Agent: agent}]; entry != nil {
			modelsLoading = entry.Loading
			modelsResp = entry.Response
			hasModels = entry.Loaded && entry.Response != nil
			modelsErr = entry.Error
			modelsErrText = entry.ErrorText
		}
	}

	header := buildSessionHeader(in, st, ls, lm, caps, modelsResp, hasModels)
	actions := buildSessionActions(in, st, ls, lm, header, caps, modelsResp, hasModels, modelsLoading, modelsErrText, modelsErr, ui)
	dock := buildSessionDock(in, st, ls, lm, caps, ui)
	composer := buildSessionComposer(in, st, ls, lm, header, caps, dock, ui)
	timeline := buildSessionTimeline(in, st, ls, lm, caps, win, ui)

	return &corev1.SessionViewModel{
		SessionId: sid,
		Header:    header,
		Actions:   actions,
		Dock:      dock,
		Composer:  composer,
		Timeline:  timeline,
	}
}

func findListSession(st *State, sid string, raw *lists.Session) *lists.Session {
	if st != nil && sid != "" {
		for _, s := range st.World.Sessions {
			if s != nil && s.ID == sid {
				return s
			}
		}
	}
	return raw
}

func mapSessionConnState(c lists.ConnectionKind) corev1.ConnState {
	switch c {
	case lists.ConnConnected:
		return corev1.ConnState_CONN_STATE_CONNECTED
	case lists.ConnReconnecting:
		return corev1.ConnState_CONN_STATE_RECONNECTING
	case lists.ConnOffline:
		return corev1.ConnState_CONN_STATE_OFFLINE
	case lists.ConnNoNetwork:
		return corev1.ConnState_CONN_STATE_NO_NETWORK
	default:
		return corev1.ConnState_CONN_STATE_CONNECTING
	}
}

func findListMachine(st *State, ls *lists.Session, raw *lists.Machine) *lists.Machine {
	if st != nil && ls != nil && ls.MachineID != "" {
		if m := findMachine(st, ls.MachineID); m != nil {
			return m
		}
	}
	return raw
}

func sessionEffectiveAgent(ls *lists.Session, raw *lists.Session) v1.Agent {
	if ls != nil {
		return lists.SessionAgent(ls)
	}
	if raw != nil {
		return lists.SessionAgent(raw)
	}
	return lists.DefaultAgent
}

func buildSessionHeader(
	in world.SessionSnapshot,
	st *State,
	ls *lists.Session,
	lm *lists.Machine,
	caps lists.AgentCapabilities,
	modelsResp *v1.ListModelsResponse,
	hasModels bool,
) *corev1.SessionHeaderView {
	out := &corev1.SessionHeaderView{
		Found:                 in.Found,
		SessionsListed:        in.SessionsListed,
		LiveLoaded:            in.LiveLoaded,
		ConnState:             mapSessionConnState(in.Conn),
		Agent:                 caps.Agent,
		AgentName:             caps.Name,
		ClientProtocolVersion: in.ProtocolVersion,
	}

	if !in.Found {
		out.LookupFailed = in.LookupFailed && !in.LookingUp
		if out.LookupFailed {
			out.LookupErrorText = in.LookupErrorText
		}
		if out.LookupFailed && in.LookupErrorText != nil {
			out.Missing = false
			out.StatusText = in.LookupErrorText
		} else {
			out.Missing = (in.SessionsListed && !in.LookingUp) || out.LookupFailed
			if out.Missing {
				out.MissingText = text.Msg(text.KeyCommonAndroidCannotFindThisSession)
			} else {
				out.StatusText = text.Msg(text.KeyCommonLoading)
			}
		}
		out.Title = text.Msg(text.KeySessionTitle)
		out.Capabilities = &corev1.SessionCapabilitiesView{}
		return out
	}

	var meta *v1.Session
	if ls != nil && ls.Info != nil {
		meta = ls.Info
	} else if in.Session != nil {
		meta = in.Session.Info
	}

	var cwd string
	var machineID string
	var createdMs int64
	if meta != nil {
		cwd = meta.Cwd
	}
	if ls != nil {
		machineID = ls.MachineID
		createdMs = lists.CreatedMs(ls)
	}

	// Title via shared lists helpers so Home/Project/Session match 100%.
	projKey := lists.ProjectKey(ls)
	projName := ""
	if projKey != "" {
		projName = lists.ProjectName(projKey)
	}
	plainTitle, hasCustom, _ := lists.ResolveTitle(ls, st.Cal)
	titleItem := &lists.SessionItem{
		Session:        ls,
		Agent:          caps.Agent,
		Title:          plainTitle,
		HasCustomTitle: hasCustom,
		ProjectKey:     projKey,
		ProjectName:    projName,
		CreatedMs:      createdMs,
	}
	out.Title = sessionTitleText(titleItem)

	if cwd != "" {
		out.Cwd = cwd
		out.ProjectKey = projKey
		out.ShortPath = lists.ShortPath(cwd)
		out.ProjectName = text.Plain(out.ShortPath)
		if out.ProjectName == nil {
			out.ProjectName = text.Plain(cwd)
		}
	} else {
		out.ProjectName = text.Msg(text.KeyProjectsUnknown)
	}
	if st != nil {
		out.MultipleMachines = len(st.World.Machines) > 1
	}

	reachabilityUnknown := (in.ReachabilityUnknown || st.ReachabilityUnknown || (lm != nil && lm.ReachabilityUnknown)) && !in.Removed && !in.AccountControlLost
	displayLm := lm
	if st.DisplayGrace && lm != nil && lm.Online && !lists.MachineReachable(lm) {
		mc := *lm
		reachable := true
		mc.Reachable = &reachable
		displayLm = &mc
	} else if !st.DisplayGrace && reachabilityUnknown && lm != nil && !lists.MachineReachable(lm) && !lm.ReachabilityUnknown {
		mc := *lm
		mc.ReachabilityUnknown = true
		displayLm = &mc
	}
	unlinked := !st.DisplayGrace && !reachabilityUnknown && (in.Unlinked || lists.MachineUnlinked(displayLm, &st.World))
	displayStatus := lists.ShownStatus(ls, displayLm, unlinked)
	out.Status = mapStatus(displayStatus)
	out.Tone = mapTone(lists.ToneOf(displayStatus))
	out.StatusText = statusText(displayStatus)

	mode, rawMode := lists.SessionMode(ls)
	out.ApprovalMode = mode
	out.ApprovalModeRaw = rawMode
	out.ApprovalModeLabel = knownSessionModeText(mode, rawMode)
	if in.Projection != nil && len(in.Projection.TerminalChanges) > 0 {
		out.TerminalModeOverride = rawMode != ""
	}

	var model, effort string
	var stdCtx bool
	if meta != nil {
		model = meta.Model
		effort = meta.Effort
		stdCtx = meta.ContextWindow > 0 && meta.ContextWindow <= lists.StandardContextWindow
	}
	out.Model = model
	out.Effort = effort
	out.StandardContext = stdCtx
	out.ModelUnknown = model == ""
	var modelSlice []*v1.ModelInfo
	if modelsResp != nil {
		modelSlice = modelsResp.Models
	}
	if model != "" {
		out.ModelLabel = lists.ModelLabel(modelSlice, hasModels, model, caps.Agent)
	}
	if effort != "" {
		out.EffortLabel = effortLabelText(effort)
	}

	if in.Projection != nil && in.Projection.Usage != nil {
		u := in.Projection.Usage
		if pct := lists.ContextPercent(ls); pct != nil {
			out.HasContext = true
			out.ContextPercent = *pct
		} else if u.ContextWindow > 0 {
			p := int32((u.LastTotalTokens * 100) / u.ContextWindow)
			if p > 100 {
				p = 100
			}
			out.HasContext = true
			out.ContextPercent = p
		}
		out.ContextTokens = u.LastTotalTokens
		out.ContextWindow = u.ContextWindow
		out.HighContextWarning = out.HasContext && out.ContextPercent >= highContextThresholdPct
		if out.HasContext {
			if out.ContextWindow > 0 {
				out.ContextText = text.Msg(text.KeySessionContextTokens,
					text.Str("used", formatCompactTokenCount(out.ContextTokens)),
					text.Str("total", formatCompactTokenCount(out.ContextWindow)),
				)
			} else {
				out.ContextText = text.Msg(text.KeySessionContextUsed, text.Int("percent", int64(out.ContextPercent)))
			}
		}
	}

	out.MachineId = machineID
	if lm != nil {
		out.ComputerName = text.Plain(lists.MachineName(lm, machineID))
		out.ComputerOnline = lists.MachineReachable(lm) || (st.DisplayGrace && lm.Online)
		if !out.ComputerOnline && !reachabilityUnknown {
			out.OfflineSinceMs = lm.LastSeenMs
		}
		out.DaemonProtocolVersion = lists.MachineProtocolVersion(lm)
	} else {
		out.ComputerName = text.Msg(text.KeyCommonUnknownComputer)
	}
	switch {
	case out.ComputerOnline:
		out.ComputerReachability = corev1.ComputerReachability_COMPUTER_REACHABILITY_ONLINE
	case reachabilityUnknown:
		out.ComputerReachability = corev1.ComputerReachability_COMPUTER_REACHABILITY_UNKNOWN
	default:
		out.ComputerReachability = corev1.ComputerReachability_COMPUTER_REACHABILITY_OFFLINE
	}
	out.ComputerUnlinked = unlinked
	if lm != nil && !out.ComputerOnline && !reachabilityUnknown {
		if lm.LastSeenMs > 0 {
			out.OfflineText = text.Msg(text.KeySessionOfflineNote,
				text.ArgText("lastSeen", formatRelativeTime(lm.LastSeenMs, st.Cal)),
			)
		} else {
			out.OfflineText = text.Msg(text.KeyProjectsComputerOffline)
		}
	}

	mismatch := lists.ProtocolMismatchDirection(lm, in.ProtocolVersion)
	out.ProtocolMismatch = mismatch != 0
	if mismatch < 0 {
		out.ProtocolMismatchText = text.Msg(text.KeySessionUpdateComputer)
	} else if mismatch > 0 {
		out.ProtocolMismatchText = text.Msg(text.KeyCommonAndroidThisAppVersionIsOldUpdateAndRetry)
	}

	out.Exited = lists.Exited(ls)
	out.EndedAtMs = lists.EndedMs(ls)
	if meta != nil {
		out.ForkedFromId = meta.ForkedFrom
		out.ScheduledTaskId = meta.ScheduledTaskId
		out.IsScheduled = meta.ScheduledTaskId != ""
		out.TaskOwned = meta.TaskOwned
	}
	if out.ForkedFromId != "" {
		for _, ev := range in.Events {
			if ev != nil && ev.GetSessionForked() != nil && ev.GetSessionForked().SourceSessionId == out.ForkedFromId {
				out.ForkedFromTitle = text.Plain(ev.GetSessionForked().SourceTitle)
				break
			}
		}
	}
	if ls != nil {
		out.ScheduledTitle = ls.ScheduleName
	}
	if in.Projection != nil {
		out.IdleClosed = in.Projection.IdleClosed
		out.ScheduledClosed = in.Projection.ScheduledClosed
	}
	if out.Exited {
		if out.EndedAtMs > 0 {
			out.EndedText = text.Msg(text.KeySessionEndedAt, text.ArgTimestamp("time", out.EndedAtMs, corev1.TimestampStyle_TIMESTAMP_STYLE_DATE_TIME))
		} else {
			out.EndedText = text.Msg(text.KeySessionEnded)
		}
	}

	// Static + live capabilities on this computer and session.
	var mc *v1.MachineCapabilities
	if lm != nil && lm.Meta != nil {
		mc = lm.Meta.Capabilities
	}
	onlineReady := (in.Conn == lists.ConnConnected || st.DisplayGrace) && out.ComputerOnline && !unlinked && !out.ProtocolMismatch && !in.AccountControlLost && !in.Removed
	out.Capabilities = &corev1.SessionCapabilitiesView{
		CanSend:                  onlineReady && !out.Exited,
		SupportsImages:           mc != nil && mc.ImageUpload,
		SupportsFiles:            mc != nil && mc.FileUpload,
		SupportsSendModes:        caps.SteersDuringTurn,
		SupportsSharedFiles:      mc != nil && mc.SharedFiles,
		SupportsAlwaysAllow:      mc != nil && mc.ApprovalAlways,
		SupportsSteer:            caps.SteersDuringTurn,
		SupportsCompact:          caps.Agent != v1.Agent_AGENT_ANTIGRAVITY,
		SupportsFork:             caps.Forks,
		SupportsModelSwitch:      caps.SwitchesModelInSession,
		SupportsModeSwitch:       true,
		SupportsSchedule:         mc != nil && mc.ScheduledTasks,
		TerminalAvailable:        meta != nil && meta.Status != v1.SessionStatus_SESSION_STATUS_EXITED,
		MaxImages:                maxImagesCount(mc),
		MaxFiles:                 maxFilesCount(mc),
		MaxFileBytes:             maxFileBytesLimit(mc),
		CapabilitiesLoaded:       lm != nil && lm.Meta != nil,
		ImageUploadBlockedReason: imageUploadBlockedReason(lm, out.ComputerOnline, onlineReady),
	}
	if st != nil && st.DirectReportFn != nil && machineID != "" {
		if _, report, ok := st.DirectReportFn(machineID); ok {
			out.DirectChip = BuildSessionDirectChip(report, out.ComputerName, out.ComputerOnline, out.ComputerUnlinked, out.Exited)
		}
	}

	return out
}

const (
	defaultMaxFilesPerMessage = 6
	defaultMaxFileUploadBytes = 100 * 1024 * 1024
)

func maxImagesCount(mc *v1.MachineCapabilities) int32 {
	if mc == nil || mc.MaxImagesPerMessage <= 0 {
		return 6
	}
	return mc.MaxImagesPerMessage
}

func maxFilesCount(mc *v1.MachineCapabilities) int32 {
	if mc == nil || mc.MaxFilesPerMessage <= 0 {
		return defaultMaxFilesPerMessage
	}
	if mc.MaxFilesPerMessage > defaultMaxFilesPerMessage {
		return defaultMaxFilesPerMessage
	}
	return mc.MaxFilesPerMessage
}

func maxFileBytesLimit(mc *v1.MachineCapabilities) int64 {
	if mc == nil || mc.MaxFileUploadBytes <= 0 {
		return defaultMaxFileUploadBytes
	}
	if mc.MaxFileUploadBytes > defaultMaxFileUploadBytes {
		return defaultMaxFileUploadBytes
	}
	return mc.MaxFileUploadBytes
}

func imageUploadBlockedReason(lm *lists.Machine, computerOnline, onlineReady bool) *corev1.Text {
	name := lists.MachineName(lm, "")
	if lm == nil || !computerOnline {
		return text.Msg(text.KeyAttachmentComputerOffline, text.Str("name", name))
	}
	if !onlineReady {
		return text.Msg(text.KeyAttachmentDisconnected)
	}
	if lm.Meta == nil {
		return text.Msg(text.KeyAttachmentComputerUnread, text.Str("name", name))
	}
	if lm.Meta.Capabilities == nil || !lm.Meta.Capabilities.ImageUpload {
		return text.Msg(text.KeyAttachmentPathSpaces, text.Str("name", name))
	}
	return nil
}

func knownSessionModeText(mode v1.ApprovalMode, raw string) *corev1.Text {
	if t := modeNameText(mode); t != nil {
		return t
	}
	if raw != "" {
		return text.Plain(raw)
	}
	return nil
}

func sessionApprovalModeText(mode v1.ApprovalMode, raw, agentName string) *corev1.Text {
	if t := knownSessionModeText(mode, raw); t != nil {
		return t
	}
	return text.Msg(text.KeyModesAgentOwn, text.Str("agent", agentName))
}

func effortLabelText(effort string) *corev1.Text {
	if effort == "" {
		return nil
	}
	return text.Plain(effort)
}

func formatCompactTokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		if n%1_000_000 == 0 {
			return strconv.FormatInt(n/1_000_000, 10) + "M"
		}
		return strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64) + "M"
	case n >= 1000:
		return strconv.FormatInt(n/1000, 10) + "K"
	default:
		return strconv.FormatInt(n, 10)
	}
}

func commandSlotState(ui *SessionUIState, slotKey string, expiredText, unknownText *corev1.Text) *corev1.SessionCommandState {
	out := &corev1.SessionCommandState{
		SlotKey: slotKey,
		Status:  corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_IDLE,
	}
	if ui == nil {
		return out
	}
	rec := ui.Commands[slotKey]
	if rec == nil {
		return out
	}
	out.RequestId = rec.RequestID
	out.Status = rec.Status
	out.Busy = rec.Busy
	out.Frozen = rec.Frozen
	out.Expired = rec.Expired
	out.ErrorCode = rec.ErrorCode
	out.ErrorText = rec.ErrorText
	switch rec.Status {
	case corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_FAILED:
		out.CanRetry = true
		out.StatusText = rec.ErrorText
	case corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_UNKNOWN:
		out.CanAbandon = true
		out.CanRetry = !rec.Expired
		if rec.Expired && expiredText != nil {
			out.StatusText = expiredText
		} else if unknownText != nil {
			out.StatusText = unknownText
		} else {
			out.StatusText = rec.ErrorText
		}
	}
	return out
}

func buildSessionActions(
	in world.SessionSnapshot,
	st *State,
	ls *lists.Session,
	lm *lists.Machine,
	header *corev1.SessionHeaderView,
	caps lists.AgentCapabilities,
	modelsResp *v1.ListModelsResponse,
	hasModels, modelsLoading bool,
	modelsErrText *corev1.Text,
	modelsErr string,
	ui *SessionUIState,
) *corev1.SessionActionsView {
	sid := ""
	if ls != nil {
		sid = ls.ID
	}
	out := &corev1.SessionActionsView{
		Interrupt:     commandSlotState(ui, "interrupt:"+sid, nil, nil),
		End:           commandSlotState(ui, "stop:"+sid, nil, nil),
		Resume:        commandSlotState(ui, "resume:"+sid, nil, nil),
		Rename:        commandSlotState(ui, "rename:"+sid, nil, nil),
		Fork:          commandSlotState(ui, "fork:"+sid, text.Msg(text.KeySessionForkExpired), text.Msg(text.KeySessionForkUnknown)),
		Compact:       commandSlotState(ui, "compact:"+sid, nil, nil),
		SetModel:      commandSlotState(ui, "set_model:"+sid, nil, nil),
		SetMode:       commandSlotState(ui, "set_mode:"+sid, nil, nil),
		ModelsLoading: modelsLoading,
		ModelsLoaded:  hasModels,
	}
	if !in.Found {
		return out
	}

	if hasReason(modelsErrText, modelsErr) {
		out.ModelsError = text.Msg(text.KeyModelsLoadFailedReason, text.Str("agent", caps.Name), reasonArg("reason", modelsErrText, modelsErr))
	} else if modelsLoading && lm != nil {
		out.ModelsError = text.Msg(text.KeyModelsReadingMachine, text.Str("name", lists.MachineName(lm, lm.ID)))
	}

	onlineReady := (in.Conn == lists.ConnConnected || st.DisplayGrace) && header.ComputerOnline && !header.ComputerUnlinked && !header.ProtocolMismatch && !in.AccountControlLost && !in.Removed
	runningTurn := in.Projection != nil && in.Projection.Running
	hasPendingInputs := in.Projection != nil && len(in.Projection.PendingInputs) > 0
	exited := header.Exited

	out.CanInterrupt = onlineReady && !exited && runningTurn
	out.CanEnd = onlineReady && !exited
	machineDisplay := lists.MachineName(lm, "")
	if machineDisplay != "" {
		out.EndConfirmNote = text.Msg(text.KeySessionEndNote, text.Str("agent", caps.Name), text.Str("name", machineDisplay))
	}
	out.CanResume = onlineReady && exited
	if exited && !header.ComputerOnline && header.ComputerReachability != corev1.ComputerReachability_COMPUTER_REACHABILITY_UNKNOWN && machineDisplay != "" {
		out.ResumeNote = text.Msg(text.KeySessionOfflineResume, text.Str("name", machineDisplay))
	} else if machineDisplay != "" {
		out.ResumeNote = text.Msg(text.KeySessionResumeNote, text.Str("name", machineDisplay))
	}
	out.CanRename = onlineReady

	// Fork rules: Claude/Codex only, idle, no pending inputs, online.
	out.CanFork = caps.Forks && onlineReady && !runningTurn && !hasPendingInputs
	if caps.Forks {
		switch {
		case !onlineReady:
			out.ForkBlockedReason = text.Msg(text.KeySessionAndroidForkConnected)
		case runningTurn:
			out.ForkBlockedReason = text.Msg(text.KeySessionForkAfterTurn)
		case hasPendingInputs:
			out.ForkBlockedReason = text.Msg(text.KeySessionForkAfterPending)
		}
	}
	out.ForkAction = buildSessionForkAction(caps.Forks, header.ComputerOnline, header.ComputerUnlinked, out.CanFork, out.ForkBlockedReason, out.Fork)

	out.CanCompact = header.Capabilities.SupportsCompact && onlineReady && !exited && !runningTurn
	out.CanSwitchModel = caps.SwitchesModelInSession && onlineReady && !exited && !runningTurn
	if caps.SwitchesModelInSession {
		switch {
		case !onlineReady:
			out.ModelSwitchBlockedReason = text.Msg(text.KeySessionSwitchConnected)
		case runningTurn:
			out.ModelSwitchBlockedReason = text.Msg(text.KeySessionSwitchAfterTurn)
		}
	}

	out.CanSwitchMode = onlineReady && !exited

	// Model & effort choices.
	var modelSlice []*v1.ModelInfo
	if modelsResp != nil {
		modelSlice = modelsResp.Models
	}
	picked := lists.FindModel(modelSlice, header.Model)
	shown := lists.ShownModels(modelSlice, picked, true)
	out.ModelChoices = make([]*corev1.SessionModelChoiceView, 0, len(shown))
	for _, m := range shown {
		if m == nil {
			continue
		}
		var fixedNote *corev1.Text
		if len(m.Efforts) == 1 {
			fixedNote = text.Msg(text.KeyModelsFixedEffort, text.Str("effort", m.Efforts[0]))
		}
		out.ModelChoices = append(out.ModelChoices, &corev1.SessionModelChoiceView{
			Slug:                    m.Slug,
			DisplayName:             m.DisplayName,
			FixedEffortNote:         fixedNote,
			Efforts:                 append([]string(nil), m.Efforts...),
			DefaultEffort:           m.DefaultEffort,
			IsAuto:                  lists.ModelIs(m.Slug, "auto"),
			Selected:                lists.ModelIs(header.Model, m.Slug),
			SupportsStandardContext: caps.StandardContextOption && (m.ContextWindow == 0 || m.ContextWindow > lists.StandardContextWindow),
		})
	}
	efforts, activeEffort := lists.EffortChoices(lists.StartConfig{
		Agent:  caps.Agent,
		Model:  header.Model,
		Effort: header.Effort,
	}, modelsResp, nil)
	for _, eff := range efforts {
		out.EffortChoices = append(out.EffortChoices, &corev1.SessionEffortChoiceView{
			Id:       eff,
			Label:    effortLabelText(eff),
			Selected: eff == activeEffort,
		})
	}

	// Approval mode choices.
	modeLocked := false
	fullAccessUnavailable := false
	if ls != nil && ls.Info != nil {
		modeLocked = ls.Info.ModeLocked
		fullAccessUnavailable = ls.Info.FullAccessUnavailable
	}
	out.ModeLocked = modeLocked
	if caps.Agent == v1.Agent_AGENT_CLAUDE {
		out.ModePermissionNotice = text.Msg(text.KeyModesPermissionNotice)
	}
	for _, m := range []v1.ApprovalMode{
		v1.ApprovalMode_APPROVAL_MODE_ASK,
		v1.ApprovalMode_APPROVAL_MODE_AUTO,
		v1.ApprovalMode_APPROVAL_MODE_PLAN,
		v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS,
	} {
		reason := lists.SessionModeUnavailableReason(m, modeLocked, fullAccessUnavailable, header.Model, modelSlice, hasModels)
		available := reason == lists.ModeAvailable && out.CanSwitchMode
		out.ModeChoices = append(out.ModeChoices, &corev1.SessionModeChoiceView{
			Mode:              m,
			Label:             modeNameText(m),
			Note:              modeNoteText(caps, m),
			Available:         available,
			UnavailableReason: modeUnavailableText(reason, caps.Name),
			Selected:          header.ApprovalMode == m,
			SwitchAfterTurn:   runningTurn,
		})
	}

	out.UsageEntry = buildSessionUsageEntry(st, lm, caps, header.Model)
	return out
}

func buildSessionForkAction(
	supportsFork, computerOnline, computerUnlinked, canFork bool,
	blockedReason *corev1.Text,
	cmd *corev1.SessionCommandState,
) *corev1.SessionForkActionView {
	visible := supportsFork && computerOnline && !computerUnlinked
	out := &corev1.SessionForkActionView{
		Visible: visible,
	}
	if !supportsFork {
		return out
	}
	switch {
	case cmd != nil && cmd.Busy:
		out.Busy = true
		out.Label = text.Msg(text.KeySessionForking)
	case !canFork && blockedReason != nil:
		out.Label = text.Msg(text.KeySessionFork)
		out.Note = blockedReason
	case cmd != nil && cmd.Expired:
		out.Enabled = true
		out.Abandons = true
		out.NoteError = true
		out.Label = text.Msg(text.KeySessionAbandonFork)
		out.Note = text.Msg(text.KeySessionForkExpired)
	case cmd != nil && (cmd.Frozen || cmd.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_UNKNOWN):
		out.Enabled = canFork
		out.CanAbandon = cmd.CanAbandon
		out.NoteError = true
		out.Label = text.Msg(text.KeySessionFork)
		out.Note = text.Msg(text.KeySessionForkUnknown)
	default:
		out.Enabled = canFork
		out.Label = text.Msg(text.KeySessionFork)
		if cmd != nil && cmd.ErrorText != nil {
			out.NoteError = true
			out.Note = text.Msg(text.KeySessionForkFailed, text.ArgText("reason", cmd.ErrorText))
		}
	}
	return out
}

func isAgentSourceUnavailable(snap *v1.UsageStatsResponse, agent v1.Agent) bool {
	if snap == nil {
		return false
	}
	eff := lists.EffectiveAgent(agent)
	for _, src := range snap.Sources {
		if src != nil && lists.EffectiveAgent(src.Agent) == eff && src.State == v1.UsageSourceState_USAGE_SOURCE_STATE_UNAVAILABLE {
			return true
		}
	}
	return false
}

func matchSessionUsageSeries(s *v1.UsageStatsResponse, agent v1.Agent, model string) *v1.UsageSeries {
	if s == nil {
		return nil
	}
	eff := lists.EffectiveAgent(agent)
	for _, item := range s.Series {
		if item == nil || item.Current == nil || item.Current.UsageRequestCount == 0 {
			continue
		}
		if lists.EffectiveAgent(item.Agent) != eff {
			continue
		}
		if model == "" || item.Model == model {
			return item
		}
	}
	return nil
}

func buildSessionUsageEntry(st *State, lm *lists.Machine, caps lists.AgentCapabilities, model string) *corev1.SessionUsageEntryView {
	if lm == nil || lm.Meta == nil || lm.Meta.Capabilities == nil || !lm.Meta.Capabilities.UsageStats {
		return &corev1.SessionUsageEntryView{Visible: false, Supported: false}
	}
	entry := &corev1.SessionUsageEntryView{
		Visible:   true,
		Supported: true,
	}
	if lm.ID == "" {
		return entry
	}
	online := lists.MachineReachable(lm)
	snapKey := UsageCacheKey{MachineID: lm.ID, Preset: v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS}
	var rawEntry *UsageSnapshotEntry
	if st != nil && st.UsageSnapshots != nil {
		rawEntry = st.UsageSnapshots[snapKey]
	}
	snap, cachedOffline, cachedAt, loading := lookupUsageSnapshot(st, lm.ID, v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS, 0, 0, online)
	if !online && snap == nil {
		entry.Subtitle = text.Msg(text.KeySessionUsageEntryOfflineNone)
		return entry
	}
	if isAgentSourceUnavailable(snap, caps.Agent) {
		entry.SourceUnavailable = true
		entry.Subtitle = text.Msg(text.KeySessionUsageEntryUnavailable, text.Str("agent", caps.Name))
		return entry
	}
	matched := matchSessionUsageSeries(snap, caps.Agent, model)
	if matched == nil && !loading {
		snap30, cachedOff30, cachedAt30, loading30 := lookupUsageSnapshot(st, lm.ID, v1.UsageRangePreset_USAGE_RANGE_PRESET_THIRTY_DAYS, 0, 0, online)
		if isAgentSourceUnavailable(snap30, caps.Agent) {
			entry.SourceUnavailable = true
			entry.Subtitle = text.Msg(text.KeySessionUsageEntryUnavailable, text.Str("agent", caps.Name))
			return entry
		}
		if m30 := matchSessionUsageSeries(snap30, caps.Agent, model); m30 != nil {
			matched = m30
			snap = snap30
			entry.FallbackThirtyDays = true
			cachedOffline = cachedOff30
			cachedAt = cachedAt30
		}
		loading = loading || loading30
	}

	if matched == nil {
		if loading {
			entry.Loading = true
			return entry
		}
		if online && rawEntry != nil && (!text.IsEmpty(rawEntry.ErrorText) || rawEntry.Error != "") {
			entry.Subtitle = text.Msg(text.KeySessionUsageEntryReadFailed)
			if !text.IsEmpty(rawEntry.ErrorText) {
				entry.Footnote = rawEntry.ErrorText
			} else {
				entry.Footnote = text.Plain(rawEntry.Error)
			}
			return entry
		}
		entry.ValueText = text.Msg(text.KeySessionUsageEntryNoData)
		entry.Subtitle = text.Msg(text.KeySessionUsageEntryEmptyHint)
		return entry
	}

	entry.HasData = true
	entry.Backfilling = snap != nil && snap.Backfilling
	entry.TotalTokens = usage.TotalTokens(matched.Current)
	entry.TotalTokensText = usage.FormatTokensText(entry.TotalTokens, st.UILocale)
	entry.HasCacheHit = matched.Current.HasCacheHitRate
	entry.CacheHitRate = matched.Current.CacheHitRate
	if entry.HasCacheHit {
		entry.CacheHitText = text.Plain(usage.FormatCacheHit(entry.CacheHitRate))
	}

	if matched.Current.Recent10SpeedCount > 0 {
		entry.HasSpeed = true
		entry.Recent10SpeedP50 = matched.Current.Recent10SpeedP50
		entry.Recent10SpeedCount = matched.Current.Recent10SpeedCount
		entry.FewSpeedSamples = matched.Current.Recent10SpeedCount < usage.MinTrendRequests
	} else if matched.Current.HasSpeed {
		entry.HasSpeed = true
		entry.Recent10SpeedP50 = matched.Current.SpeedP50
		entry.Recent10SpeedCount = matched.Current.SpeedRequestCount
		entry.FewSpeedSamples = matched.Current.SpeedRequestCount < usage.MinTrendRequests
	}
	if entry.HasSpeed {
		entry.SpeedText = text.Plain(usage.FormatSpeedValue(entry.Recent10SpeedP50))
	}

	if cachedOffline && cachedAt > 0 {
		entry.Subtitle = text.Msg(text.KeySessionUsageEntryOfflineCached, text.ArgTimestamp("time", cachedAt, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME))
		entry.FormattedSubtitle = entry.Subtitle
	} else {
		var winLabel *corev1.Text
		if entry.FallbackThirtyDays {
			winLabel = text.Msg(text.KeyUsageEntryLast30D)
		} else {
			winLabel = text.Msg(text.KeyUsageEntryLast7D)
		}
		var detailParts []*corev1.Text
		detailParts = append(detailParts, winLabel)
		if entry.HasCacheHit {
			detailParts = append(detailParts, text.Msg(text.KeyUsageEntryCachePct, text.Str("pct", usage.FormatCacheHit(entry.CacheHitRate))))
		}
		if entry.HasSpeed {
			speedPart := text.JoinLiteral(" ", text.Plain(usage.FormatSpeedValue(entry.Recent10SpeedP50)), text.Msg(text.KeyUsageKpiUnitToks))
			detailParts = append(detailParts, speedPart)
			joined := text.JoinLiteral(" · ", detailParts...)
			if entry.FewSpeedSamples {
				entry.FormattedSubtitle = text.Msg(text.KeyUsageEntryFewSamples, text.ArgText("detail", joined), text.Int("count", int64(entry.Recent10SpeedCount)))
			} else {
				entry.FormattedSubtitle = joined
			}
		} else {
			joined := text.JoinLiteral(" · ", detailParts...)
			entry.FormattedSubtitle = text.Msg(text.KeyUsageEntryNoSpeed, text.ArgText("detail", joined))
		}
	}
	return entry
}

func modeNoteText(caps lists.AgentCapabilities, mode v1.ApprovalMode) *corev1.Text {
	agent := caps.Agent
	switch mode {
	case v1.ApprovalMode_APPROVAL_MODE_ASK:
		if agent == v1.Agent_AGENT_ANTIGRAVITY {
			return text.Msg(text.KeyModesAgyAskDesc)
		}
		return text.Msg(text.KeyModesAskDesc)
	case v1.ApprovalMode_APPROVAL_MODE_AUTO:
		return text.Msg(text.KeyModesAutoDesc, text.Str("agent", caps.Name))
	case v1.ApprovalMode_APPROVAL_MODE_PLAN:
		if agent == v1.Agent_AGENT_ANTIGRAVITY {
			return text.Msg(text.KeyModesAgyPlanDesc)
		}
		return text.Msg(text.KeyModesAskGenericDesc)
	case v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS:
		return text.Msg(text.KeyModesAutoDesc, text.Str("agent", caps.Name))
	default:
		return nil
	}
}

func modeUnavailableText(r lists.ModeUnavailableReason, agentName string) *corev1.Text {
	switch r {
	case lists.ModeAccountNoAuto:
		return text.Msg(text.KeyModesAccountNoAuto)
	case lists.ModeModelNoAuto:
		return text.Msg(text.KeyModesAccountUnsupported)
	case lists.ModeLockedFullAccess, lists.ModeFullAccessRequiresStart:
		return text.Msg(text.KeyModesAgentOwn, text.Str("agent", agentName))
	default:
		return nil
	}
}

func buildSessionDock(
	in world.SessionSnapshot,
	st *State,
	ls *lists.Session,
	lm *lists.Machine,
	caps lists.AgentCapabilities,
	ui *SessionUIState,
) *corev1.SessionDockView {
	out := &corev1.SessionDockView{}
	if !in.Found || in.Projection == nil {
		return out
	}

	p := in.Projection
	sid := ls.ID
	var cwd, home string
	if ls.Info != nil {
		cwd = ls.Info.Cwd
	}
	if lm != nil && lm.Meta != nil {
		home = lm.Meta.Home
	}

	// Count total waiting items across approvals + questions + plan so more_count matches shells.
	hasPlan := p.Plan != nil
	var metaInfo *v1.Session
	if ls != nil && ls.Info != nil {
		metaInfo = ls.Info
	} else if in.Checkpoint != nil {
		metaInfo = in.Checkpoint.Session
	}
	hasPopup := !hasPlan && len(p.Approvals) == 0 && len(p.Questions) == 0 &&
		((metaInfo != nil && metaInfo.Status == v1.SessionStatus_SESSION_STATUS_BLOCKED) || (ls != nil && ls.Pending.HasPopup))

	// Priority 1: Pending Approvals.
	if len(p.Approvals) > 0 {
		pa := p.Approvals[0]
		a := pa.Request
		extra := (len(p.Approvals) - 1) + len(p.Questions)
		if hasPlan {
			extra++
		}
		out.HasItem = true
		out.Kind = corev1.SessionDockKind_SESSION_DOCK_KIND_APPROVAL
		out.ItemId = pa.ApprovalId
		out.MoreCount = int32(extra)
		if seq, ok := findApprovalSeq(in, pa.ApprovalId); ok {
			out.ObservedSeq = seq
		}
		var cmdPreview, desc, toolName string
		var deadlineMs int64
		if a != nil {
			cmdPreview = lists.ShortenPaths(lists.ApprovalDisplay(a.Command), cwd, home)
			desc = lists.ShortenPaths(lists.ApprovalDisplay(a.Description), cwd, home)
			toolName = a.ToolName
			if a.Deadline != nil {
				deadlineMs = a.Deadline.Seconds*1000 + int64(a.Deadline.Nanos)/1_000_000
			}
		}
		summary := cmdPreview
		if summary == "" {
			summary = desc
		}
		if summary == "" {
			summary = toolName
		}
		out.Title = text.Msg(text.KeyApprovalTitle)
		out.Summary = text.Plain(summary)
		out.Meta = text.Plain(toolName)
		out.DeadlineMs = deadlineMs
		if deadlineMs > 0 && st.Cal.NowMs > 0 && deadlineMs > st.Cal.NowMs {
			remSec := (deadlineMs - st.Cal.NowMs + 999) / 1000
			if remSec >= 60 {
				mins := lists.MinutesLeft(deadlineMs, st.Cal.NowMs)
				out.TimeLeftPlain = text.Msg(text.KeyHomeIosApprovalRemaining, text.Int("count", mins))
			} else {
				out.TimeLeftPlain = text.Msg(text.KeyApprovalSecondsLeft, text.Int("count", remSec))
			}
		}

		slotKey := "approval:" + sid + ":" + pa.ApprovalId
		out.Command = commandSlotState(ui, slotKey,
			text.Msg(text.KeyApprovalAndroidReplyExpired, text.Str("decision", approvalDecisionHint(ui, slotKey))),
			text.Msg(text.KeyApprovalAndroidUnknownResult),
		)

		body := ""
		truncated := false
		skeleton := false
		if a != nil {
			body = lists.ShortenPaths(lists.ApprovalBody(a), cwd, home)
			truncated = lists.ApprovalTruncated(a)
			skeleton = lists.IsApprovalSkeleton(a)
		}
		out.IsSkeleton = skeleton
		var canAlways bool
		var alwaysKind corev1.AlwaysAllowKind
		var alwaysTarget string
		var alwaysLabel *corev1.Text
		supportsAlways := lm != nil && lm.Meta != nil && lm.Meta.Capabilities != nil && lm.Meta.Capabilities.ApprovalAlways
		if supportsAlways && a != nil && (a.Command != "" || a.Description != "") {
			ak, target := lists.ParseApprovalAlways(a.Command)
			if ak != lists.AlwaysNone {
				canAlways = true
				alwaysKind = mapAlwaysKind(ak)
				alwaysTarget = target
				if ak == lists.AlwaysDirectory {
					alwaysLabel = text.Msg(text.KeyApprovalAlwaysDirectory, text.Str("path", lists.ShortPath(target)))
				} else if ak == lists.AlwaysPrefix {
					alwaysLabel = text.Msg(text.KeyApprovalAlwaysPrefix, text.Str("command", target))
				}
			}
		}
		out.Detail = &corev1.SessionDockView_Approval{
			Approval: &corev1.SessionApprovalDockView{
				ApprovalId:            pa.ApprovalId,
				ToolName:              toolName,
				CommandPreview:        cmdPreview,
				Body:                  body,
				BodyTruncated:         truncated,
				IsSkeleton:            skeleton,
				Description:           desc,
				CanAlwaysAllow:        canAlways,
				AlwaysKind:            alwaysKind,
				AlwaysTarget:          alwaysTarget,
				AlwaysLabel:           alwaysLabel,
				DenyReasonPlaceholder: text.Msg(text.KeyApprovalReasonPlaceholder),
			},
		}
		return out
	}

	// Priority 2: Pending Questions.
	if len(p.Questions) > 0 {
		pq := p.Questions[0]
		q := pq.Request
		extra := len(p.Questions) - 1
		if hasPlan {
			extra++
		}
		out.HasItem = true
		out.Kind = corev1.SessionDockKind_SESSION_DOCK_KIND_QUESTION
		out.ItemId = pq.QuestionId
		out.IsSkeleton = projection.IsQuestionSkeleton(q)
		out.MoreCount = int32(extra)
		if seq, ok := findQuestionSeq(in, pq.QuestionId); ok {
			out.ObservedSeq = seq
		}
		total := 0
		if q != nil {
			total = len(q.Questions)
		}
		activeIdx := int32(0)
		if idx, ok := ui.QuestionIndexByID[pq.QuestionId]; ok && idx >= 0 && int(idx) < total {
			activeIdx = idx
		}
		var stepView *corev1.SessionQuestionStepView
		var summaryStr string
		if total > 0 {
			stepView = buildQuestionStep(q.Questions[activeIdx], caps.Agent)
			summaryStr = stepView.Header
			if summaryStr == "" {
				summaryStr = stepView.QuestionText
			}
		}
		out.Title = text.Msg(text.KeyDockAnswerTitle)
		out.Summary = text.Plain(summaryStr)
		if total > 1 {
			out.Meta = text.Msg(text.KeyDockQuestionProgress, text.Int("index", int64(activeIdx+1)), text.Int("count", int64(total)))
		}
		slotKey := "question:" + sid + ":" + pq.QuestionId + "#" + strconv.Itoa(int(activeIdx))
		out.Command = commandSlotState(ui, slotKey,
			text.Msg(text.KeyDockAndroidAnswerExpired, text.Str("answer", questionAnswerHint(ui, slotKey))),
			text.Msg(text.KeyDockAndroidAnswerUnknown, text.Str("answer", questionAnswerHint(ui, slotKey))),
		)
		out.Detail = &corev1.SessionDockView_Question{
			Question: &corev1.SessionQuestionDockView{
				QuestionId:     pq.QuestionId,
				ActiveIndex:    activeIdx,
				TotalQuestions: int32(total),
				ProgressText:   out.Meta,
				Question:       stepView,
			},
		}
		return out
	}

	// Priority 3: Plan Proposed.
	if hasPlan {
		out.HasItem = true
		out.Kind = corev1.SessionDockKind_SESSION_DOCK_KIND_PLAN
		out.ItemId = "plan"
		out.IsSkeleton = projection.IsSkeletonText(p.Plan.Text)
		if in.Checkpoint != nil {
			out.ObservedSeq = in.Checkpoint.PlanEventSeq
		}
		out.Title = text.Msg(text.KeyDockPlanTitle, text.Str("title", caps.Name))
		out.Summary = text.Msg(text.KeyDockPlanUnread)
		slotKey := "plan:" + sid
		choiceHint := planChoiceHint(ui, slotKey)
		out.Command = commandSlotState(ui, slotKey,
			text.Msg(text.KeyDockAndroidPlanExpired, text.Str("choice", choiceHint)),
			text.Msg(text.KeyDockAndroidPlanUnknown, text.Str("choice", choiceHint)),
		)
		var note *corev1.Text
		switch caps.Agent {
		case v1.Agent_AGENT_CLAUDE:
			note = text.Msg(text.KeyDockClaudePlanNote)
		case v1.Agent_AGENT_ANTIGRAVITY:
			note = text.Msg(text.KeyDockAgyPlanNote)
		}
		var screenHash []byte
		if in.Checkpoint != nil {
			screenHash = append([]byte(nil), in.Checkpoint.PlanScreenHash...)
		}
		out.Detail = &corev1.SessionDockView_Plan{
			Plan: &corev1.SessionPlanDockView{
				Markdown:              p.Plan.Text,
				ScreenHash:            screenHash,
				Note:                  note,
				ClearContextAvailable: caps.PlanClearContext,
				Choices:               buildPlanChoices(caps),
			},
		}
		return out
	}

	// Priority 4: Terminal Popup.
	if hasPopup {
		out.HasItem = true
		out.Kind = corev1.SessionDockKind_SESSION_DOCK_KIND_TERMINAL_POPUP
		detail := ""
		if metaInfo != nil {
			detail = metaInfo.StatusDetail
		}
		if detail == "" && ls != nil {
			detail = ls.Pending.PopupPreview
		}
		out.ItemId = "terminal-popup"
		if detail != "" {
			out.ItemId = detail
		}
		var popupTitle *corev1.Text
		if detail != "" {
			popupTitle = text.Msg(text.KeyListStatePopup)
		}
		var hint *corev1.Text
		isTrustFolder := caps.Agent == v1.Agent_AGENT_CLAUDE && detail == "trust folder"
		if isTrustFolder {
			hint = text.Msg(text.KeyDockTrustFolder)
		}
		out.Title = text.Msg(text.KeyListStatePopup)
		out.Summary = popupTitle
		out.Meta = text.Msg(text.KeyDockAgentWaiting, text.Str("agent", caps.Name))
		termAvail := metaInfo != nil && metaInfo.Status != v1.SessionStatus_SESSION_STATUS_EXITED
		out.Detail = &corev1.SessionDockView_TerminalPopup{
			TerminalPopup: &corev1.SessionTerminalPopupDockView{
				PopupTitle:    popupTitle,
				Hint:          hint,
				CanGoTerminal: termAvail,
				IsTrustFolder: isTrustFolder,
			},
		}
		return out
	}

	return out
}

func mapAlwaysKind(ak lists.ApprovalAlwaysKind) corev1.AlwaysAllowKind {
	switch ak {
	case lists.AlwaysPrefix:
		return corev1.AlwaysAllowKind_ALWAYS_ALLOW_KIND_COMMAND_PREFIX
	case lists.AlwaysDirectory:
		return corev1.AlwaysAllowKind_ALWAYS_ALLOW_KIND_DIRECTORY
	default:
		return corev1.AlwaysAllowKind_ALWAYS_ALLOW_KIND_UNSPECIFIED
	}
}

func approvalDecisionHint(ui *SessionUIState, slotKey string) string {
	rec := ui.Commands[slotKey]
	if rec == nil || rec.Command == nil || rec.Command.GetResolveApproval() == nil {
		return ""
	}
	r := rec.Command.GetResolveApproval()
	if r.Allow {
		if r.Always {
			return "always"
		}
		return "allow"
	}
	return "deny"
}

func questionAnswerHint(ui *SessionUIState, slotKey string) string {
	rec := ui.Commands[slotKey]
	if rec == nil || rec.Command == nil || rec.Command.GetAnswerQuestion() == nil {
		return ""
	}
	q := rec.Command.GetAnswerQuestion()
	if len(q.Options) > 0 {
		return optionsHint(q.Options)
	}
	if q.Option > 0 {
		return "#" + strconv.FormatInt(int64(q.Option), 10)
	}
	return ""
}

func planChoiceHint(ui *SessionUIState, slotKey string) string {
	rec := ui.Commands[slotKey]
	if rec == nil || rec.Command == nil || rec.Command.GetAnswerPlan() == nil {
		return ""
	}
	switch rec.Command.GetAnswerPlan().Choice {
	case v1.PlanChoice_PLAN_CHOICE_IMPLEMENT:
		return "implement"
	case v1.PlanChoice_PLAN_CHOICE_CLEAR_AND_IMPLEMENT:
		return "clear_and_implement"
	case v1.PlanChoice_PLAN_CHOICE_KEEP_PLANNING:
		return "keep_planning"
	default:
		return ""
	}
}

func findApprovalSeq(in world.SessionSnapshot, id string) (uint64, bool) {
	if in.Checkpoint != nil {
		for _, a := range in.Checkpoint.Actions {
			if a != nil && a.GetApprovalRequested() != nil && a.GetApprovalRequested().ApprovalId == id {
				return a.Seq, true
			}
		}
	}
	for i := len(in.Events) - 1; i >= 0; i-- {
		if e := in.Events[i]; e != nil && e.GetApprovalRequested() != nil && e.GetApprovalRequested().ApprovalId == id {
			return e.Seq, true
		}
	}
	return 0, false
}

func findQuestionSeq(in world.SessionSnapshot, id string) (uint64, bool) {
	if in.Checkpoint != nil {
		for _, a := range in.Checkpoint.Actions {
			if a != nil && a.GetQuestionAsked() != nil && a.GetQuestionAsked().QuestionId == id {
				return a.Seq, true
			}
		}
	}
	for i := len(in.Events) - 1; i >= 0; i-- {
		if e := in.Events[i]; e != nil && e.GetQuestionAsked() != nil && e.GetQuestionAsked().QuestionId == id {
			return e.Seq, true
		}
	}
	return 0, false
}

func buildQuestionStep(q *v1.Question, agent v1.Agent) *corev1.SessionQuestionStepView {
	if q == nil {
		return nil
	}
	opts := make([]*corev1.SessionQuestionOptionView, 0, len(q.Options))
	for i, opt := range q.Options {
		if opt == nil {
			continue
		}
		label, rec := lists.OptionLabel(opt.Label)
		opts = append(opts, &corev1.SessionQuestionOptionView{
			OptionNum:   int32(i + 1),
			Label:       label,
			Description: opt.Description,
			Recommended: rec,
		})
	}
	return &corev1.SessionQuestionStepView{
		Header:       q.Header,
		QuestionText: q.Text,
		MultiSelect:  q.MultiSelect,
		Options:      opts,
		CanFreeText:  agent == v1.Agent_AGENT_CODEX,
	}
}

func buildPlanChoices(caps lists.AgentCapabilities) []*corev1.SessionPlanChoiceOptionView {
	var out []*corev1.SessionPlanChoiceOptionView
	if caps.PlanClearContext {
		out = append(out, &corev1.SessionPlanChoiceOptionView{
			Choice: v1.PlanChoice_PLAN_CHOICE_CLEAR_AND_IMPLEMENT,
			Label:  text.Msg(text.KeyDockPlanClearRecord),
		})
	}
	out = append(out,
		&corev1.SessionPlanChoiceOptionView{
			Choice:      v1.PlanChoice_PLAN_CHOICE_IMPLEMENT,
			Label:       text.Msg(text.KeyDockPlanImplementRecord),
			Recommended: true,
		},
		&corev1.SessionPlanChoiceOptionView{
			Choice: v1.PlanChoice_PLAN_CHOICE_KEEP_PLANNING,
			Label:  text.Msg(text.KeyDockPlanKeep),
		},
	)
	return out
}

func buildSessionComposer(
	in world.SessionSnapshot,
	st *State,
	ls *lists.Session,
	lm *lists.Machine,
	header *corev1.SessionHeaderView,
	caps lists.AgentCapabilities,
	dock *corev1.SessionDockView,
	ui *SessionUIState,
) *corev1.SessionComposerView {
	out := &corev1.SessionComposerView{}
	if !in.Found {
		if header.MissingText != nil {
			out.DisabledReason = header.MissingText
		} else {
			out.DisabledReason = text.Msg(text.KeyCommonLoading)
		}
		return out
	}

	sid := ls.ID
	onlineReady := header.Capabilities.CanSend
	runningTurn := in.Projection != nil && in.Projection.Running
	hasPlan := dock != nil && dock.Kind == corev1.SessionDockKind_SESSION_DOCK_KIND_PLAN && dock.HasItem
	hasQuestion := dock != nil && dock.Kind == corev1.SessionDockKind_SESSION_DOCK_KIND_QUESTION && dock.HasItem
	hasApproval := dock != nil && dock.Kind == corev1.SessionDockKind_SESSION_DOCK_KIND_APPROVAL && dock.HasItem

	out.Enabled = onlineReady
	out.HasActiveTurn = runningTurn
	out.SupportsQueueMode = caps.SteersDuringTurn
	out.DefaultSendMode = lists.SendModeFor(caps.Agent, v1.SendMode_SEND_MODE_STEER, runningTurn)
	out.CanAttachImages = onlineReady && header.Capabilities.SupportsImages
	out.CanAttachFiles = onlineReady && header.Capabilities.SupportsFiles
	out.InterruptVisible = runningTurn && header.ComputerOnline && !header.ComputerUnlinked && (in.Conn == lists.ConnConnected || st.DisplayGrace)
	out.SlashWarningTitle = text.Msg(text.KeyComposerSlashTitle)
	out.SlashWarningText = text.Msg(text.KeyComposerSlashNote)

	switch {
	case header.Exited:
		out.DisabledReason = text.Msg(text.KeySessionEnded)
	case in.AccountControlLost || in.Removed:
		out.DisabledReason = text.Msg(text.KeyErrorsRemoteControlExpiredControlAndNewUploadsArePaused)
	case header.ProtocolMismatch:
		out.DisabledReason = header.ProtocolMismatchText
	case header.ComputerReachability == corev1.ComputerReachability_COMPUTER_REACHABILITY_UNKNOWN:
		out.DisabledReason = text.Msg(text.KeyConnectionConnecting)
	case in.Conn != lists.ConnConnected && !st.DisplayGrace:
		out.DisabledReason = text.Msg(text.KeyComposerIosConnectSend)
	case header.ComputerUnlinked:
		out.DisabledReason = text.Msg(text.KeyErrorsRemoteControlExpiredControlAndNewUploadsArePaused)
	case !header.ComputerOnline:
		out.DisabledReason = text.Msg(text.KeyErrorsTheComputerIsOfflineTheCommandWasNotSent)
	}

	switch {
	case hasPlan:
		out.Placeholder = text.Msg(text.KeyComposerPlanPlaceholder)
	case hasQuestion || hasApproval:
		out.Placeholder = text.Msg(text.KeyComposerAnswerFirstPlaceholder)
	case runningTurn:
		out.Placeholder = text.Msg(text.KeyComposerRunningPlaceholder)
	default:
		out.IsDefaultPlaceholder = true
		out.Placeholder = text.Msg(text.KeyComposerMessage)
	}

	starting := ls != nil && ls.Info != nil && ls.Info.Status == v1.SessionStatus_SESSION_STATUS_STARTING
	nowMs := int64(0)
	if st != nil {
		nowMs = st.Cal.NowMs
	}
	out.PendingTray = buildSessionPendingTray(trayBuildParams{
		sid:           sid,
		in:            in,
		st:            st,
		lm:            lm,
		caps:          caps,
		ui:            ui,
		onlineReady:   onlineReady,
		exited:        header.Exited,
		starting:      starting,
		waitingAnswer: hasApproval || hasQuestion,
		nowMs:         nowMs,
	})
	out.RetainedAttachmentRequestIds = collectRetainedAttachmentIDs(in.Projection, ui)
	return out
}

var trayGroupOrder = []corev1.SessionTrayGroupKind{
	corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_FAILED,
	corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_UNSURE,
	corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_READY,
	corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_ANSWER,
	corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_STEER,
	corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_QUEUE,
	corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_PAUSED,
}

type trayBuildParams struct {
	sid           string
	in            world.SessionSnapshot
	st            *State
	lm            *lists.Machine
	caps          lists.AgentCapabilities
	ui            *SessionUIState
	onlineReady   bool
	exited        bool
	starting      bool
	waitingAnswer bool
	nowMs         int64
}

func buildSessionPendingTray(p trayBuildParams) *corev1.SessionPendingTrayView {
	tray := &corev1.SessionPendingTrayView{
		Offline: !p.onlineReady,
	}
	buckets := make(map[corev1.SessionTrayGroupKind][]*corev1.SessionTrayItemView, len(trayGroupOrder))
	seen := make(map[string]bool)

	var projByID map[string]*corev1.InputProjection
	if p.in.Projection != nil && len(p.in.Projection.Inputs) > 0 {
		projByID = make(map[string]*corev1.InputProjection, len(p.in.Projection.Inputs))
		for _, ip := range p.in.Projection.Inputs {
			if ip != nil && ip.RequestId != "" {
				projByID[ip.RequestId] = ip
			}
		}
	}

	var outgoingByID map[string]*SessionOutgoingRecord
	if p.ui != nil && len(p.ui.Outgoing) > 0 {
		outgoingByID = make(map[string]*SessionOutgoingRecord, len(p.ui.Outgoing))
		for _, og := range p.ui.Outgoing {
			if og != nil && og.RequestID != "" {
				outgoingByID[og.RequestID] = og
			}
		}
	}

	if p.in.Projection != nil {
		for _, pi := range p.in.Projection.PendingInputs {
			if pi == nil || pi.RequestId == "" {
				continue
			}
			if p.ui != nil && p.ui.DismissedInputIDs[pi.RequestId] {
				seen[pi.RequestId] = true
				continue
			}
			seen[pi.RequestId] = true
			item, visible := buildPendingInputTrayItem(p, pi, projByID[pi.RequestId])
			if visible {
				buckets[item.Group] = append(buckets[item.Group], item)
			}
		}
		if !p.exited {
			for _, ip := range p.in.Projection.Inputs {
				if ip == nil || ip.RequestId == "" || seen[ip.RequestId] {
					continue
				}
				if p.ui != nil && p.ui.DismissedInputIDs[ip.RequestId] {
					continue
				}
				og := outgoingByID[ip.RequestId]
				if og != nil && og.Restored {
					seen[ip.RequestId] = true
					continue
				}
				item, ok := buildTerminalInputTrayItem(p, ip, og)
				if ok {
					seen[ip.RequestId] = true
					buckets[item.Group] = append(buckets[item.Group], item)
				}
			}
		}
	}

	if p.ui != nil {
		for _, og := range p.ui.Outgoing {
			if og == nil || og.Restored || seen[og.RequestID] {
				continue
			}
			seen[og.RequestID] = true
			item, visible := buildLocalOutgoingTrayItem(p, og)
			if visible {
				buckets[item.Group] = append(buckets[item.Group], item)
			}
		}
		if startItem := buildStartPromptTrayItem(p); startItem != nil && !seen[startItem.RequestId] {
			buckets[startItem.Group] = append(buckets[startItem.Group], startItem)
		}
	}

	for _, kind := range trayGroupOrder {
		items := buckets[kind]
		if len(items) == 0 {
			continue
		}
		tone := trayGroupTone(kind)
		title := trayGroupTitle(kind, p.caps.Name)
		headerText := title
		if len(items) > 1 {
			headerText = text.Msg(text.KeyQueueGroupCount, text.ArgText("title", title), text.Int("count", int64(len(items))))
		}
		if kind == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_QUEUE {
			for i, it := range items {
				it.QueueNumber = int32(i + 1)
			}
		}
		if kind == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_FAILED ||
			kind == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_UNSURE {
			tray.FailedCount += int32(len(items))
		}
		tray.Groups = append(tray.Groups, &corev1.SessionTrayGroupView{
			Kind:         kind,
			Tone:         tone,
			Title:        title,
			HeaderText:   headerText,
			TotalCount:   int32(len(items)),
			CanCancelAll: kind == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_PAUSED && len(items) > 1,
			Items:        items,
		})
		tray.OrderedItems = append(tray.OrderedItems, items...)
	}
	tray.TotalCount = int32(len(tray.OrderedItems))

	if p.ui != nil && p.ui.Undo != nil && len(p.ui.Undo.Items) > 0 {
		n := int32(len(p.ui.Undo.Items))
		tray.Undo = &corev1.SessionTrayUndoView{
			Visible: true,
			Count:   n,
			Label:   text.Msg(text.KeyQueueCanceledCount, text.Int("count", int64(n))),
			CanUndo: p.onlineReady,
		}
	}
	return tray
}

func buildPendingInputTrayItem(p trayBuildParams, pi *corev1.PendingInput, ip *corev1.InputProjection) (*corev1.SessionTrayItemView, bool) {
	cancelSlot := "cancel_input:" + p.sid + ":" + pi.RequestId
	resumeSlot := "resume_input:" + p.sid + ":" + pi.RequestId
	cancelState := commandSlotState(p.ui, cancelSlot, nil, nil)
	resumeState := commandSlotState(p.ui, resumeSlot, nil, nil)

	activity := corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_IDLE
	if cancelState != nil && cancelState.Busy {
		activity = corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_CANCELING
	} else if resumeState != nil && resumeState.Busy {
		activity = corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_SENDING
	}

	mode := pi.Mode
	if mode == v1.SendMode_SEND_MODE_UNSPECIFIED {
		mode = v1.SendMode_SEND_MODE_STEER
	}
	group := trayPendingGroup(pi.Paused, mode, p.starting, p.waitingAnswer)

	if shouldHideCodexSteerGrace(p, pi.RequestId, group, activity) {
		return nil, false
	}

	var local *LocalSentContent
	if p.ui != nil {
		local = p.ui.LocalContents[pi.RequestId]
	}
	imgIDs, fileIDs, imgCount, fileCount := trayAttachments(local, ip, pi.Files)
	itemText := pi.Text
	if itemText == "" && local != nil {
		itemText = local.Text
	}
	foreignDimmed := itemText == "" && imgCount == 0 && fileCount == 0
	hasLocalCopy := !foreignDimmed && ((imgCount == 0 && fileCount == 0) || local != nil)

	origin := pi.Origin
	if origin == nil && ip != nil && ip.Accepted != nil {
		origin = ip.Accepted.Origin
	}
	var acceptedMs int64
	if ip != nil && ip.Accepted != nil {
		acceptedMs = ip.Accepted.TimeMs
	}

	var reasonText *corev1.Text
	if cancelState != nil && (cancelState.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_FAILED || cancelState.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_UNKNOWN) {
		reasonText = text.Msg(text.KeyQueueCancelFailed)
	} else if resumeState != nil && (resumeState.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_FAILED || resumeState.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_UNKNOWN) {
		reasonText = text.Msg(text.KeyQueueResumeFailed)
	}

	idle := activity == corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_IDLE
	return &corev1.SessionTrayItemView{
		RequestId:              pi.RequestId,
		SlotKey:                "send:" + p.sid + ":" + pi.RequestId,
		Group:                  group,
		Tone:                   trayGroupTone(group),
		Activity:               activity,
		ShowSpinner:            group == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_STEER || !idle,
		Text:                   itemText,
		SourceLabel:            traySourceLabel(origin, acceptedMs, p.st, p.in.DeviceNames, p.in.ThisDeviceID, p.lm),
		ForeignDimmed:          foreignDimmed,
		AttachmentCount:        imgCount + fileCount,
		ImageCount:             imgCount,
		FileCount:              fileCount,
		ImageIds:               imgIDs,
		FileIds:                fileIDs,
		Mode:                   mode,
		ReasonText:             reasonText,
		CanCancel:              idle,
		CanResume:              idle && group == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_PAUSED,
		CanPromote:             idle && group == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_QUEUE && hasLocalCopy,
		CanRestore:             idle && hasLocalCopy,
		NetworkActionsDisabled: !p.onlineReady,
		ServerBacked:           true,
		IsSkeleton:             projection.IsSkeletonText(pi.RequestId) || projection.IsSkeletonText(itemText),
	}, true
}

func buildTerminalInputTrayItem(p trayBuildParams, ip *corev1.InputProjection, og *SessionOutgoingRecord) (*corev1.SessionTrayItemView, bool) {
	var group corev1.SessionTrayGroupKind
	switch ip.Status {
	case v1.InputStatus_INPUT_STATUS_FAILED:
		group = corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_FAILED
	case v1.InputStatus_INPUT_STATUS_NEEDS_TERMINAL, v1.InputStatus_INPUT_STATUS_UNCONFIRMED:
		group = corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_UNSURE
	default:
		return nil, false
	}

	var local *LocalSentContent
	if p.ui != nil {
		local = p.ui.LocalContents[ip.RequestId]
	}
	var itemText string
	var mode v1.SendMode
	var origin *v1.Origin
	var acceptedMs int64
	if ip.Accepted != nil {
		itemText = ip.Accepted.Text
		origin = ip.Accepted.Origin
		acceptedMs = ip.Accepted.TimeMs
	}
	if ip.Detail != nil {
		if itemText == "" {
			itemText = ip.Detail.Text
		}
		mode = ip.Detail.Mode
	}
	if og != nil {
		if itemText == "" {
			itemText = og.Text
		}
		if mode == v1.SendMode_SEND_MODE_UNSPECIFIED {
			mode = og.Mode
		}
	}
	if local != nil {
		if itemText == "" {
			itemText = local.Text
		}
		if mode == v1.SendMode_SEND_MODE_UNSPECIFIED {
			mode = local.Mode
		}
	}
	if mode == v1.SendMode_SEND_MODE_UNSPECIFIED {
		mode = v1.SendMode_SEND_MODE_STEER
	}

	var files []*v1.SentFile
	if ip.Accepted != nil && len(ip.Accepted.Files) > 0 {
		files = ip.Accepted.Files
	} else if ip.Detail != nil {
		files = ip.Detail.Files
	}
	imgIDs, fileIDs, imgCount, fileCount := trayAttachments(local, ip, files)
	if og != nil && local == nil {
		if len(imgIDs) == 0 && len(og.ImageIDs) > 0 {
			imgIDs = append([]string(nil), og.ImageIDs...)
			imgCount = int32(len(imgIDs))
		}
		if len(fileIDs) == 0 && len(og.FileIDs) > 0 {
			fileIDs = append([]string(nil), og.FileIDs...)
			fileCount = int32(len(fileIDs))
		}
	}

	foreignDimmed := itemText == "" && imgCount == 0 && fileCount == 0
	hasAllLocalBytes := !foreignDimmed && ((imgCount == 0 && fileCount == 0) || local != nil || og != nil)
	isOwnOrigin := origin == nil || origin.Kind != v1.OriginKind_ORIGIN_KIND_CLIENT || origin.DeviceId == "" || origin.DeviceId == p.in.ThisDeviceID
	canRestore := !foreignDimmed && (hasAllLocalBytes || (isOwnOrigin && itemText != ""))
	expired := og != nil && og.Expired

	var code string
	var errText *corev1.Text
	if ip.Detail != nil && ip.Detail.Reason != "" {
		code = ip.Detail.Reason
	} else if ip.FailureReason != nil && *ip.FailureReason != "" {
		code = *ip.FailureReason
	} else if ip.Status == v1.InputStatus_INPUT_STATUS_NEEDS_TERMINAL {
		code = "input_changed"
	}
	if og != nil {
		if code == "" {
			code = og.ErrorCode
		}
		errText = og.ErrorText
	}

	var reasonText *corev1.Text
	if group == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_UNSURE {
		if code != "" || errText != nil {
			reasonText = trayFailureReasonText(p.caps.Name, code, errText)
		} else if ip.Status == v1.InputStatus_INPUT_STATUS_NEEDS_TERMINAL {
			reasonText = text.Msg(text.KeyComposerInputUnconfirmed)
		} else {
			reasonText = trayUnsureReasonText(og)
		}
	} else {
		reasonText = trayFailureReasonText(p.caps.Name, code, errText)
	}

	return &corev1.SessionTrayItemView{
		RequestId:              ip.RequestId,
		SlotKey:                "send:" + p.sid + ":" + ip.RequestId,
		Group:                  group,
		Tone:                   trayGroupTone(group),
		Activity:               corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_IDLE,
		ShowSpinner:            false,
		Text:                   itemText,
		SourceLabel:            traySourceLabel(origin, acceptedMs, p.st, p.in.DeviceNames, p.in.ThisDeviceID, p.lm),
		ForeignDimmed:          foreignDimmed,
		AttachmentCount:        imgCount + fileCount,
		ImageCount:             imgCount,
		FileCount:              fileCount,
		ImageIds:               imgIDs,
		FileIds:                fileIDs,
		Mode:                   mode,
		ReasonText:             reasonText,
		CanCancel:              group == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_FAILED,
		CanRetry:               group == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_FAILED && hasAllLocalBytes && !expired,
		CanOpenTerminal:        group == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_UNSURE,
		CanIgnore:              group == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_UNSURE,
		CanRestore:             canRestore,
		NetworkActionsDisabled: !p.onlineReady,
		ServerBacked:           false,
		IsSkeleton:             projection.IsSkeletonText(ip.RequestId) || projection.IsSkeletonText(itemText),
	}, true
}

func buildLocalOutgoingTrayItem(p trayBuildParams, og *SessionOutgoingRecord) (*corev1.SessionTrayItemView, bool) {
	mode := og.Mode
	if mode == v1.SendMode_SEND_MODE_UNSPECIFIED {
		mode = v1.SendMode_SEND_MODE_STEER
	}
	imgCount := int32(len(og.ImageIDs))
	fileCount := int32(len(og.FileIDs))
	item := &corev1.SessionTrayItemView{
		RequestId:              og.RequestID,
		SlotKey:                og.SlotKey,
		Text:                   og.Text,
		AttachmentCount:        imgCount + fileCount,
		ImageCount:             imgCount,
		FileCount:              fileCount,
		ImageIds:               append([]string(nil), og.ImageIDs...),
		FileIds:                append([]string(nil), og.FileIDs...),
		Mode:                   mode,
		NetworkActionsDisabled: !p.onlineReady,
		ServerBacked:           false,
		IsSkeleton:             projection.IsSkeletonText(og.RequestID) || projection.IsSkeletonText(og.Text),
	}

	switch og.State {
	case OutgoingSending, OutgoingSent:
		group := trayPendingGroup(false, mode, p.starting, p.waitingAnswer)
		if shouldHideCodexSteerGrace(p, og.RequestID, group, corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_SENDING) ||
			(p.caps.Agent == v1.Agent_AGENT_CODEX && group == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_STEER && p.nowMs > 0 && og.VisibleAfterMs > p.nowMs) {
			return nil, false
		}
		item.Group = group
		item.Tone = trayGroupTone(group)
		item.Activity = corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_SENDING
		item.ShowSpinner = true
		return item, true

	case OutgoingFailed:
		item.Group = corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_FAILED
		item.Tone = corev1.SessionTrayTone_SESSION_TRAY_TONE_RED
		item.Activity = corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_IDLE
		item.ReasonText = trayFailureReasonText(p.caps.Name, og.ErrorCode, og.ErrorText)
		item.CanRetry = !og.Expired
		item.CanCancel = true
		item.CanRestore = true
		return item, true

	case OutgoingUnknown:
		item.Group = corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_UNSURE
		item.Tone = corev1.SessionTrayTone_SESSION_TRAY_TONE_ORANGE
		item.Activity = corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_IDLE
		item.ReasonText = trayUnsureReasonText(og)
		item.CanOpenTerminal = true
		item.CanIgnore = true
		item.CanRestore = true
		return item, true
	}
	return nil, false
}

func buildStartPromptTrayItem(p trayBuildParams) *corev1.SessionTrayItemView {
	if p.ui == nil || p.ui.PendingStartPrompt == nil || p.exited || p.ui.PendingStartPrompt.Text == "" {
		return nil
	}
	for _, ev := range p.in.Events {
		if ev != nil && ev.GetUserMessage() != nil {
			return nil
		}
	}
	if FindStartPromptInput(p.in.Projection, p.ui) != nil {
		return nil
	}
	reqID := p.ui.PendingStartPrompt.RequestID
	if reqID == "" {
		reqID = "start-prompt"
	}
	imgCount := p.ui.PendingStartPrompt.ImageCount
	fileCount := p.ui.PendingStartPrompt.FileCount
	return &corev1.SessionTrayItemView{
		RequestId:              reqID,
		SlotKey:                "send:" + p.sid + ":" + reqID,
		Group:                  corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_READY,
		Tone:                   corev1.SessionTrayTone_SESSION_TRAY_TONE_NEUTRAL,
		Activity:               corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_IDLE,
		Text:                   p.ui.PendingStartPrompt.Text,
		AttachmentCount:        imgCount + fileCount,
		ImageCount:             imgCount,
		FileCount:              fileCount,
		Mode:                   v1.SendMode_SEND_MODE_STEER,
		NetworkActionsDisabled: !p.onlineReady,
		ServerBacked:           false,
		IsSkeleton:             projection.IsSkeletonText(reqID) || projection.IsSkeletonText(p.ui.PendingStartPrompt.Text),
	}
}

func shouldHideCodexSteerGrace(
	p trayBuildParams,
	reqID string,
	group corev1.SessionTrayGroupKind,
	activity corev1.SessionTrayRowActivity,
) bool {
	if p.caps.Agent != v1.Agent_AGENT_CODEX ||
		group != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_STEER ||
		activity == corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_CANCELING ||
		p.ui == nil || p.nowMs <= 0 {
		return false
	}
	return p.ui.SteerVisibleAfterMs[reqID] > p.nowMs
}

func trayPendingGroup(paused bool, mode v1.SendMode, starting, waitingAnswer bool) corev1.SessionTrayGroupKind {
	switch {
	case paused:
		return corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_PAUSED
	case starting:
		return corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_READY
	case waitingAnswer && mode != v1.SendMode_SEND_MODE_QUEUE:
		return corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_ANSWER
	case mode == v1.SendMode_SEND_MODE_QUEUE:
		return corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_QUEUE
	default:
		return corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_STEER
	}
}

func trayGroupTone(kind corev1.SessionTrayGroupKind) corev1.SessionTrayTone {
	switch kind {
	case corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_FAILED:
		return corev1.SessionTrayTone_SESSION_TRAY_TONE_RED
	case corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_UNSURE:
		return corev1.SessionTrayTone_SESSION_TRAY_TONE_ORANGE
	default:
		return corev1.SessionTrayTone_SESSION_TRAY_TONE_NEUTRAL
	}
}

func trayGroupTitle(kind corev1.SessionTrayGroupKind, agentName string) *corev1.Text {
	switch kind {
	case corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_FAILED:
		return text.Msg(text.KeyQueueGroupFailed)
	case corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_UNSURE:
		return text.Msg(text.KeyQueueGroupUnsure)
	case corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_READY:
		return text.Msg(text.KeyQueueGroupWaitReady, text.Str("agent", agentName))
	case corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_ANSWER:
		return text.Msg(text.KeyQueueGroupWaitAnswer)
	case corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_STEER:
		return text.Msg(text.KeyQueueGroupSteer)
	case corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_QUEUE:
		return text.Msg(text.KeyQueueGroupQueue)
	case corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_PAUSED:
		return text.Msg(text.KeyQueueGroupPaused)
	default:
		return text.Msg(text.KeyQueueGroupSteer)
	}
}

func trayAttachments(local *LocalSentContent, ip *corev1.InputProjection, piFiles []*v1.SentFile) (imgIDs, fileIDs []string, imgCount, fileCount int32) {
	if local != nil {
		imgIDs = append([]string(nil), local.ImageIDs...)
		fileIDs = append([]string(nil), local.FileIDs...)
		return imgIDs, fileIDs, int32(len(imgIDs)), int32(len(fileIDs))
	}
	if ip != nil {
		if ip.Accepted != nil && len(ip.Accepted.ImageIds) > 0 {
			imgIDs = append([]string(nil), ip.Accepted.ImageIds...)
		} else if ip.Detail != nil && len(ip.Detail.ImageIds) > 0 {
			imgIDs = append([]string(nil), ip.Detail.ImageIds...)
		}
	}
	for _, f := range piFiles {
		if f != nil && f.FileId != "" {
			fileIDs = append(fileIDs, f.FileId)
		}
	}
	fileCount = int32(len(piFiles))
	if int32(len(fileIDs)) > fileCount {
		fileCount = int32(len(fileIDs))
	}
	imgCount = int32(len(imgIDs))
	return imgIDs, fileIDs, imgCount, fileCount
}

func traySourceLabel(origin *v1.Origin, acceptedMs int64, st *State, devices map[string]string, thisDeviceID string, lm *lists.Machine) *corev1.Text {
	if origin != nil && origin.ScheduledTaskId != "" {
		if acceptedMs > 0 {
			return text.Msg(text.KeyQueueScheduledTime, text.ArgTimestamp("time", acceptedMs, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME))
		}
		return text.Msg(text.KeyQueueAndroidScheduled)
	}
	return originText(origin, st, devices, thisDeviceID, lm)
}

func trayFailureReasonText(agentName, rawCode string, errText *corev1.Text) *corev1.Text {
	switch rawCode {
	case "image_not_recognized":
		return text.Msg(text.KeyComposerImagesUnrecognized, text.Str("agent", agentName))
	case "timeout":
		return text.Msg(text.KeyComposerReadyTimeout)
	case "restarted":
		return text.Msg(text.KeyComposerComputerRestarted)
	case "write_failed":
		return text.Msg(text.KeyComposerWriteFailed)
	case "exited":
		return text.Msg(text.KeySessionEndedMessageFailed)
	case "input_changed":
		return text.Msg(text.KeyComposerNotWritten)
	}
	if errText != nil {
		return errText
	}
	if rawCode == "" {
		return text.Msg(text.KeyComposerAndroidUnknownReason)
	}
	return text.Plain(rawCode)
}

func trayUnsureReasonText(og *SessionOutgoingRecord) *corev1.Text {
	if og != nil {
		if og.ErrorText != nil {
			return og.ErrorText
		}
		if og.ErrorCode != "" {
			return trayFailureReasonText("", og.ErrorCode, nil)
		}
	}
	return text.Msg(text.KeyQueueUnconfirmedReason)
}

func collectRetainedAttachmentIDs(p *corev1.ProjectionState, ui *SessionUIState) []string {
	set := make(map[string]bool)
	if ui != nil {
		for reqID, lc := range ui.LocalContents {
			if lc != nil && reqID != "" && (len(lc.ImageIDs) > 0 || len(lc.FileIDs) > 0) {
				set[reqID] = true
			}
		}
		for _, og := range ui.Outgoing {
			if og != nil && og.RequestID != "" && (len(og.ImageIDs) > 0 || len(og.FileIDs) > 0) {
				set[og.RequestID] = true
			}
		}
		if ui.Undo != nil {
			for _, it := range ui.Undo.Items {
				if it != nil && it.SourceReqID != "" && (len(it.ImageIDs) > 0 || len(it.FileIDs) > 0) {
					set[it.SourceReqID] = true
				}
			}
		}
		for _, intent := range ui.CancelIntents {
			if intent != nil && intent.SourceReqID != "" && intent.HasAttachments {
				set[intent.SourceReqID] = true
			}
		}
		for srcReqID := range ui.PendingResendSources {
			if srcReqID != "" {
				set[srcReqID] = true
			}
		}
	}
	if p != nil {
		for _, pi := range p.PendingInputs {
			if pi == nil || pi.RequestId == "" {
				continue
			}
			if len(pi.Files) > 0 {
				set[pi.RequestId] = true
			}
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sortutil.Strings(out)
	return out
}

func originText(o *v1.Origin, st *State, devices map[string]string, thisDeviceID string, lm *lists.Machine) *corev1.Text {
	if o == nil {
		return nil
	}
	if o.ScheduledTaskId != "" {
		return nil
	}
	switch o.Kind {
	case v1.OriginKind_ORIGIN_KIND_CLIENT:
		if o.DeviceId == "" || o.DeviceId == thisDeviceID {
			return nil
		}
		if name := devices[o.DeviceId]; name != "" {
			return text.Plain(name)
		}
		return text.Msg(text.KeySessionOriginOther)
	case v1.OriginKind_ORIGIN_KIND_LOCAL:
		name := ""
		if lm != nil {
			name = lists.MachineName(lm, lm.ID)
		}
		return text.Msg(text.KeySessionOriginTerminal, text.Str("name", name))
	default:
		return nil
	}
}

func optionsHint(options []int32) string {
	parts := make([]string, len(options))
	for i, o := range options {
		parts[i] = "#" + strconv.FormatInt(int64(o), 10)
	}
	return strings.Join(parts, ", ")
}
