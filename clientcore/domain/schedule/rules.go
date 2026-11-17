package schedule

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/projection"
)

const quietSpanPrefix = "quiet-"

// Visible reports whether t should appear in schedule lists.
// User-cancelled tasks are hidden; system-cancelled tasks with an error reason stay visible for attention.
func Visible(t *Task) bool {
	if t == nil || t.ID == "" {
		return false
	}
	return t.Status != "cancelled" || (t.Reason != "" && t.Reason != "user_cancelled")
}

// GroupOf classifies a task's execution state into Attention, Active, Paused, or Ended.
func GroupOf(t *Task) TaskGroup {
	if t == nil {
		return GroupEnded
	}
	if t.Status == "cancelled" && t.Reason != "" && t.Reason != "user_cancelled" {
		return GroupAttention
	}
	if t.Status == "failed" || t.Status == "uncertain" {
		return GroupAttention
	}
	if t.Status == "paused" {
		if t.Reason != "" && t.Reason != "user_paused" && t.Reason != "completed_while_paused" {
			return GroupAttention
		}
		return GroupPaused
	}
	if t.Status == "completed" || t.Status == "cancelled" {
		return GroupEnded
	}
	return GroupActive
}

// InboxGroupOf classifies a visible task into Attention, Recent, or Ended.
func InboxGroupOf(t *Task, hasNew bool) InboxGroup {
	g := GroupOf(t)
	if g == GroupAttention {
		return InboxAttention
	}
	if g == GroupEnded && !hasNew {
		return InboxEnded
	}
	return InboxRecent
}

// OneShot reports whether t executes only once (type "at", or non-repeating "after_turn").
func OneShot(t *Task) bool {
	if t == nil {
		return false
	}
	tr := t.Spec.Trigger
	return tr.Type == "at" || (tr.Type == "after_turn" && !tr.Repeat)
}

// FailedOnce reports whether t is a failed one-shot task (recovers via explicit retry run rather than resume).
func FailedOnce(t *Task) bool {
	return t != nil && t.Status == "failed" && OneShot(t)
}

// TargetMissing reports whether t is a resume_session task stopped because its destination conversation is gone.
func TargetMissing(t *Task) bool {
	if t == nil || t.Spec.Target.Type != "resume_session" {
		return false
	}
	return t.Reason == "execution_session_missing" ||
		t.Reason == "execution_session_exited" ||
		t.Reason == "source_session_missing"
}

// IsStoppedRun reports whether a run status is terminal-abnormal or skipped.
func IsStoppedRun(status string) bool {
	switch status {
	case "failed", "uncertain", "interrupted", "skipped", "cancelled":
		return true
	}
	return false
}

// IsInProgressStatus reports whether a run status is currently executing or waiting in queue.
func IsInProgressStatus(status string) bool {
	return status == "dispatching" || status == "queued" || status == "running"
}

// InProgress reports whether t has an active dispatching/queued/running execution.
func InProgress(t *Task) bool {
	if t == nil {
		return false
	}
	if t.Active != nil && IsInProgressStatus(t.Active.Status) {
		return true
	}
	for i := range t.Runs {
		if IsInProgressStatus(t.Runs[i].Status) {
			return true
		}
	}
	return false
}

// IsQuietRun reports whether r is a silent completed run eligible for same-day folding.
func IsQuietRun(r *Run) bool {
	return r != nil && r.Status == "completed" && !r.Notified && !r.InputPaused
}

// RunTimeMs returns the run's execution start timestamp (fallback scheduled_at) in Unix ms.
func RunTimeMs(r *Run) int64 {
	if r == nil {
		return 0
	}
	if ms := ParseRFC3339Ms(r.StartedAt); ms > 0 {
		return ms
	}
	return ParseRFC3339Ms(r.ScheduledAt)
}

// NotificationTimeMs returns the run's notification/display timestamp (ended_at, else started_at, else scheduled_at).
func NotificationTimeMs(r *Run) int64 {
	if r == nil {
		return 0
	}
	if ms := ParseRFC3339Ms(r.EndedAt); ms > 0 {
		return ms
	}
	return RunTimeMs(r)
}

// AllRuns returns active + historical runs deduplicated by ID (active wins) and sorted newest first.
func AllRuns(t *Task) []*Run {
	if t == nil {
		return nil
	}
	byID := make(map[string]*Run, len(t.Runs)+1)
	for i := range t.Runs {
		r := t.Runs[i]
		if r.ID != "" {
			cp := r
			byID[r.ID] = &cp
		}
	}
	if t.Active != nil && t.Active.ID != "" {
		cp := *t.Active
		byID[cp.ID] = &cp
	}
	out := make([]*Run, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b *Run) int {
		ta, tb := RunTimeMs(a), RunTimeMs(b)
		if ta != tb {
			if ta > tb {
				return -1
			}
			return 1
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out
}

// LatestNotification returns the newest notified run by NotificationTimeMs (tie-break ID desc).
func LatestNotification(t *Task) *Run {
	var best *Run
	var bestMs int64
	for _, r := range AllRuns(t) {
		if !r.Notified {
			continue
		}
		ms := NotificationTimeMs(r)
		if best == nil || ms > bestMs || (ms == bestMs && r.ID > best.ID) {
			best = r
			bestMs = ms
		}
	}
	return best
}

// StoppedTimeMs returns the display timestamp for an attention-group task.
func StoppedTimeMs(t *Task) int64 {
	if t == nil {
		return 0
	}
	runs := AllRuns(t)
	if len(runs) > 0 {
		if ms := NotificationTimeMs(runs[0]); ms > 0 {
			return ms
		}
	}
	if ms := ParseRFC3339Ms(t.UpdatedAt); ms > 0 {
		return ms
	}
	return ParseRFC3339Ms(t.CreatedAt)
}

// SortInbox filters and sorts visible tasks belonging to group.
func SortInbox(entries []TaskEntry, group InboxGroup, hasNew func(TaskEntry) bool) []TaskEntry {
	if hasNew == nil {
		hasNew = func(TaskEntry) bool { return false }
	}
	out := make([]TaskEntry, 0, len(entries))
	for _, e := range entries {
		if e.Task == nil || !Visible(e.Task) {
			continue
		}
		if InboxGroupOf(e.Task, hasNew(e)) == group {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b TaskEntry) int {
		switch group {
		case InboxAttention:
			sa, sb := StoppedTimeMs(a.Task), StoppedTimeMs(b.Task)
			if sa != sb {
				if sa > sb {
					return -1
				}
				return 1
			}
		case InboxEnded:
			na, nb := NotificationTimeMs(LatestNotification(a.Task)), NotificationTimeMs(LatestNotification(b.Task))
			if na != nb {
				if na > nb {
					return -1
				}
				return 1
			}
			ua, ub := ParseRFC3339Ms(a.Task.UpdatedAt), ParseRFC3339Ms(b.Task.UpdatedAt)
			if ua == 0 {
				ua = ParseRFC3339Ms(a.Task.CreatedAt)
			}
			if ub == 0 {
				ub = ParseRFC3339Ms(b.Task.CreatedAt)
			}
			if ua != ub {
				if ua > ub {
					return -1
				}
				return 1
			}
		case InboxRecent:
			na, nb := NotificationTimeMs(LatestNotification(a.Task)), NotificationTimeMs(LatestNotification(b.Task))
			if na != nb {
				if na > nb {
					return -1
				}
				return 1
			}
			xa, xb := ParseRFC3339Ms(a.Task.NextRunAt), ParseRFC3339Ms(b.Task.NextRunAt)
			if xa == 0 {
				xa = math.MaxInt64
			}
			if xb == 0 {
				xb = math.MaxInt64
			}
			if xa != xb {
				if xa < xb {
					return -1
				}
				return 1
			}
		}
		if a.MachineID != b.MachineID {
			return strings.Compare(a.MachineID, b.MachineID)
		}
		return strings.Compare(a.Task.ID, b.Task.ID)
	})
	return out
}

// MessageValid reports whether message is non-blank and within the 32 KiB UTF-8 limit.
func MessageValid(msg string) bool {
	return strings.TrimSpace(msg) != "" && len(msg) <= MessageMaxBytes
}

func isScheduleSpace(r rune) bool {
	return unicode.IsSpace(r) || r == '\uFEFF'
}

func collapseSpaces(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range s {
		if isScheduleSpace(r) {
			if b.Len() > 0 && !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// ClipName truncates s to at most TitleMaxRunes Unicode code points.
func ClipName(s string) string {
	count := 0
	for i := range s {
		if count == TitleMaxRunes {
			return s[:i]
		}
		count++
	}
	return s
}

// NameInput sanitizes interactive title typing: collapses whitespace, trims leading space, clips to 60 runes.
func NameInput(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range s {
		if isScheduleSpace(r) {
			if b.Len() > 0 && !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return ClipName(b.String())
}

// ResolveName returns the clipped explicit name, or derives a 60-rune title from message's first sentence.
func ResolveName(name, message string) string {
	explicit := collapseSpaces(name)
	if explicit != "" {
		return ClipName(explicit)
	}
	trimmed := strings.TrimFunc(message, isScheduleSpace)
	if trimmed == "" {
		return ""
	}
	var line string
	for _, l := range strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n") {
		if strings.TrimFunc(l, isScheduleSpace) != "" {
			line = l
			break
		}
	}
	if line == "" {
		line = trimmed
	}
	// Cut at the first sentence terminator (Chinese punctuation, !, ?, or '.' followed by space/EOL).
	end := len(line)
	for i, r := range line {
		if r == '。' || r == '！' || r == '？' || r == '!' || r == '?' {
			end = i
			break
		}
		if r == '.' {
			nextIdx := i + 1
			if nextIdx >= len(line) {
				end = i
				break
			}
			nr, _ := utf8.DecodeRuneInString(line[nextIdx:])
			if unicode.IsSpace(nr) {
				end = i
				break
			}
		}
	}
	first := line[:end]
	if collapseSpaces(first) == "" {
		first = line
	}
	cleaned := collapseSpaces(first)
	cleaned = strings.TrimRightFunc(cleaned, func(r rune) bool {
		return r == ',' || r == '，' || r == ':' || r == '：' || r == ';' || r == '；' || isScheduleSpace(r)
	})
	return ClipName(cleaned)
}

// RenamedSpec returns an updated Spec if the resolved title differs from task.Spec.Title.
func RenamedSpec(t *Task, name string) (Spec, bool) {
	if t == nil {
		return Spec{}, false
	}
	resolved := ResolveName(name, t.Spec.Message)
	if resolved == t.Spec.Title {
		return t.Spec, false
	}
	next := t.Spec
	next.Title = resolved
	return next, true
}

// Supported reports whether m advertises scheduled_tasks and has matching protocol version.
func Supported(m *lists.Machine, clientVersion uint32) bool {
	if m == nil || m.Meta == nil || m.Meta.Capabilities == nil {
		return false
	}
	if !m.Meta.Capabilities.ScheduledTasks {
		return false
	}
	return lists.ProtocolMismatchDirection(m, clientVersion) == 0
}

// Writable reports whether machineID is reachable, schedule-supported, and account control is valid.
func Writable(w *lists.World, machineID string) bool {
	if w == nil || machineID == "" || w.AccountControlLost || w.AccountClockSkewed {
		return false
	}
	for _, m := range w.Machines {
		if m != nil && m.ID == machineID {
			return lists.MachineReachable(m) && Supported(m, w.ClientVersion)
		}
	}
	return false
}

// Access describes allowed actions for one task given connectivity and mutation state.
type Access struct {
	Writable          bool
	CanRename         bool
	CanToggleNotify   bool
	CanEditDefinition bool
	CanRunNow         bool
	ShowToggleAction  bool
	IsResumeAction    bool
	CanToggle         bool
	CanDelete         bool
	ShowAttention     bool
	TargetMissing     bool
	FailedOnce        bool
	SystemCancelled   bool
}

// EditAccess computes task action permissions matching Android/iOS ScheduleEditAccess.
func EditAccess(t *Task, writable, busy, frozen bool) Access {
	if t == nil {
		return Access{}
	}
	g := GroupOf(t)
	failedOnce := FailedOnce(t)
	targetMissing := TargetMissing(t)
	sysCancelled := t.Status == "cancelled" && Visible(t)
	base := writable && !busy && !frozen
	def := base && t.Active == nil
	showToggle := g != GroupEnded && !failedOnce
	isResume := g == GroupPaused || g == GroupAttention
	canRun := def && t.ManualRequest == nil && ((g != GroupEnded && g != GroupAttention) || failedOnce)
	return Access{
		Writable:          writable,
		CanRename:         base,
		CanToggleNotify:   base,
		CanEditDefinition: def,
		CanRunNow:         canRun,
		ShowToggleAction:  showToggle,
		IsResumeAction:    isResume,
		CanToggle:         base && showToggle,
		CanDelete:         base,
		ShowAttention:     g == GroupAttention,
		TargetMissing:     targetMissing,
		FailedOnce:        failedOnce,
		SystemCancelled:   sysCancelled,
	}
}

// RunGroups groups runs ordered by notificationTime desc, folding same-local-day quiet runs.
func RunGroups(runs []*Run, bounds CalendarBounds) []RunGroup {
	ordered := slices.Clone(runs)
	slices.SortFunc(ordered, func(a, b *Run) int {
		ta, tb := NotificationTimeMs(a), NotificationTimeMs(b)
		if ta != tb {
			if ta > tb {
				return -1
			}
			return 1
		}
		return strings.Compare(b.ID, a.ID)
	})

	groups := make([]RunGroup, 0, len(ordered))
	quietByDay := make(map[string]*RunGroup)
	for _, r := range ordered {
		if r == nil {
			continue
		}
		ms := NotificationTimeMs(r)
		day := bounds.DayBucketKey(ms)
		if IsQuietRun(r) {
			if existing, ok := quietByDay[day]; ok {
				existing.Runs = append(existing.Runs, r)
				continue
			}
			g := RunGroup{
				Key:   "quiet-" + day,
				Day:   day,
				Runs:  []*Run{r},
				Quiet: true,
			}
			groups = append(groups, g)
			quietByDay[day] = &groups[len(groups)-1]
			continue
		}
		groups = append(groups, RunGroup{
			Key:   r.ID,
			Day:   day,
			Runs:  []*Run{r},
			Quiet: false,
		})
	}
	// If a quiet group only ended up with 1 run, preserve its run ID key for stable expansion keys.
	for i := range groups {
		if groups[i].Quiet && len(groups[i].Runs) == 1 {
			groups[i].Key = groups[i].Runs[0].ID
			groups[i].Quiet = false
		}
	}
	return groups
}

// FinalAnswer extracts the last final_answer agent message belonging to runID from a contiguous event prefix.
// Matches the daemon's scheduled follow-up boundary and returns "" on skeleton/gap/missing run.
func FinalAnswer(runID string, events []*v1.Event) string {
	if runID == "" || len(events) == 0 {
		return ""
	}
	ordered := slices.Clone(events)
	slices.SortFunc(ordered, func(a, b *v1.Event) int {
		if a.GetSeq() < b.GetSeq() {
			return -1
		}
		if a.GetSeq() > b.GetSeq() {
			return 1
		}
		return 0
	})
	start := -1
	for i, e := range ordered {
		if e != nil && e.GetUserMessage() != nil && e.GetOrigin().GetScheduledRunId() == runID {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	prevSeq := ordered[start].GetSeq()
	answer := ""
	for _, e := range ordered[start+1:] {
		if e == nil || e.GetSeq() != prevSeq+1 {
			return ""
		}
		prevSeq = e.GetSeq()
		if e.Kind == nil || projection.IsSkeleton(e) {
			return ""
		}
		orig := e.GetOrigin()
		human := orig != nil && (orig.Kind == v1.OriginKind_ORIGIN_KIND_CLIENT || orig.Kind == v1.OriginKind_ORIGIN_KIND_LOCAL)
		if e.GetUserMessage() != nil {
			if human || (orig != nil && orig.ScheduledRunId != "" && orig.ScheduledRunId != runID) {
				break
			}
		}
		if ss := e.GetSessionState(); ss != nil {
			if ss.Status == v1.SessionStatus_SESSION_STATUS_EXITED || (human && ss.Detail == "resumed") {
				break
			}
		}
		if am := e.GetAgentMessage(); am != nil && am.Phase == "final_answer" {
			if strings.TrimSpace(am.Text) != "" {
				answer = am.Text
			}
		}
	}
	return answer
}

// QuietSpans computes consecutive same-day silent completed scheduled turn spans in a transcript.
func QuietSpans(events []*v1.Event, bounds CalendarBounds) []QuietSpan {
	if len(events) == 0 {
		return nil
	}
	hasScheduled := false
	notifiedRuns := make(map[string]bool)
	identity := func(task, run string) string { return task + "/" + run }
	for _, e := range events {
		if e == nil {
			continue
		}
		if orig := e.GetOrigin(); orig != nil && orig.ScheduledTaskId != "" && orig.ScheduledRunId != "" {
			hasScheduled = true
		}
		if sn := e.GetScheduledNotification(); sn != nil {
			notifiedRuns[identity(sn.TaskId, sn.RunId)] = true
		}
		if te := e.GetTurnEnded(); te != nil && te.ScheduledNotified {
			notifiedRuns[identity(te.ScheduledTaskId, te.ScheduledRunId)] = true
		}
	}
	if !hasScheduled {
		return nil
	}

	derived := projection.Derive(events, v1.Agent_AGENT_UNSPECIFIED)
	toolEnds := make(map[uint64]uint64)
	// Derive started->completed tool spans directly so we don't depend on unexported reducer maps.
	openTools := make(map[string]uint64)
	for _, e := range events {
		if e == nil {
			continue
		}
		if ts := e.GetToolStarted(); ts != nil && ts.CallId != "" {
			openTools[ts.CallId] = e.Seq
		}
		if tc := e.GetToolCall(); tc != nil && tc.CallId != "" {
			if startSeq, ok := openTools[tc.CallId]; ok {
				toolEnds[startSeq] = e.Seq
				delete(openTools, tc.CallId)
			}
		}
	}

	toolOK := func(tc *v1.ToolCall) bool {
		return tc != nil && tc.Status != "failed" && (tc.ExitCode == nil || *tc.ExitCode == 0)
	}

	var spans []QuietSpan
	var start *v1.Event
	var taskID, runID string
	var notified bool
	safe := true
	var toolsThrough uint64
	boundary := true
	lastDay := ""
	tailMayContinue := false

	for _, e := range events {
		if e == nil {
			continue
		}
		oversized := projection.IsSkeleton(e)
		if e.GetUserMessage() != nil {
			tailMayContinue = false
			orig := e.GetOrigin()
			if orig != nil && orig.ScheduledRunId != "" && orig.ScheduledTaskId != "" {
				start = e
				runID = orig.ScheduledRunId
				taskID = orig.ScheduledTaskId
				notified = false
				safe = !oversized && e.Time != nil
				toolsThrough = 0
			} else {
				start = nil
				runID = ""
				taskID = ""
				boundary = true
			}
			continue
		}

		if start != nil {
			if e.GetScheduledNotification() != nil {
				notified = true
			} else if oversized || e.Kind == nil {
				safe = false
			} else if tc := e.GetToolCall(); tc != nil {
				if !toolOK(tc) || projection.WentToBackground(tc) {
					safe = false
				}
			} else if e.GetToolStarted() != nil {
				endSeq, ok := toolEnds[e.Seq]
				if !ok {
					safe = false
				} else if endSeq > toolsThrough {
					toolsThrough = endSeq
				}
			} else if e.GetTurnContext() != nil {
				if derived != nil && derived.TerminalChanges[e.Seq] != nil {
					safe = false
				}
			} else if is := e.GetInputState(); is != nil {
				if is.Paused ||
					is.Status == v1.InputStatus_INPUT_STATUS_FAILED ||
					is.Status == v1.InputStatus_INPUT_STATUS_UNCONFIRMED ||
					is.Status == v1.InputStatus_INPUT_STATUS_CANCELED ||
					is.Status == v1.InputStatus_INPUT_STATUS_NEEDS_TERMINAL ||
					is.Status == v1.InputStatus_INPUT_STATUS_HELD_BY_CLI {
					safe = false
				}
			} else if e.GetApprovalRequested() != nil ||
				e.GetQuestionAsked() != nil ||
				e.GetQuestionAnswered() != nil ||
				e.GetSettingsChanged() != nil ||
				e.GetImageGenerated() != nil ||
				e.GetPlanProposed() != nil ||
				e.GetSessionState() != nil ||
				e.GetSessionForked() != nil {
				safe = false
			}
		} else {
			isFence := oversized || e.Kind == nil ||
				e.GetScheduledNotification() != nil ||
				e.GetAgentMessage() != nil ||
				e.GetReasoning() != nil ||
				e.GetToolCall() != nil ||
				e.GetToolStarted() != nil ||
				e.GetApprovalRequested() != nil ||
				e.GetQuestionAsked() != nil ||
				e.GetPlanProposed() != nil ||
				e.GetSettingsChanged() != nil ||
				e.GetQuestionAnswered() != nil ||
				e.GetTurnContext() != nil ||
				e.GetTurnEnded() != nil ||
				e.GetImageGenerated() != nil ||
				e.GetSessionForked() != nil
			if !isFence {
				if is := e.GetInputState(); is != nil {
					isFence = is.Status != v1.InputStatus_INPUT_STATUS_QUEUED &&
						is.Status != v1.InputStatus_INPUT_STATUS_SUBMITTED &&
						is.Status != v1.InputStatus_INPUT_STATUS_HELD_BY_CLI
				} else if ss := e.GetSessionState(); ss != nil && ss.Status == v1.SessionStatus_SESSION_STATUS_EXITED {
					isFence = true
				}
			}
			if isFence {
				boundary = true
				if tailMayContinue && len(spans) > 0 {
					spans = spans[:len(spans)-1]
					tailMayContinue = false
				}
			}
		}

		te := e.GetTurnEnded()
		if te == nil {
			continue
		}
		if start != nil &&
			safe &&
			toolsThrough <= e.Seq &&
			!notified &&
			!notifiedRuns[identity(taskID, runID)] &&
			!te.ScheduledNotified &&
			te.ScheduledTaskId == taskID &&
			te.ScheduledRunId == runID &&
			te.Outcome == v1.TurnOutcome_TURN_OUTCOME_COMPLETED {
			atMs := start.Time.Seconds*1000 + int64(start.Time.Nanos)/1_000_000
			day := bounds.DayBucketKey(atMs)
			if !boundary && len(spans) > 0 && day == lastDay && spans[len(spans)-1].TaskID == taskID {
				spans[len(spans)-1].Through = e.Seq
				spans[len(spans)-1].TimesMs = append(spans[len(spans)-1].TimesMs, atMs)
			} else {
				spans = append(spans, QuietSpan{
					Key:     quietSpanPrefix + strconv.FormatUint(start.Seq, 10),
					TaskID:  taskID,
					RunID:   runID,
					From:    start.Seq,
					Through: e.Seq,
					TimesMs: []int64{atMs},
				})
			}
			lastDay = day
			boundary = false
			tailMayContinue = true
		} else {
			boundary = true
			tailMayContinue = false
		}
		start = nil
		taskID = ""
		runID = ""
	}
	return spans
}
