// Package schedule implements the pure client-side rules for scheduled tasks:
// JSON contract decoding/encoding, grouping, sorting, name derivation, timing
// validation, localizable Text descriptions, and unread tracking.
package schedule

import (
	"slices"
	"time"
)

type decodeSentinel string

const (
	errEmptyListResponse decodeSentinel = "empty schedule list response"
	errMissingTasksField decodeSentinel = "missing tasks field in schedule list response"
	errTrailingTaskJSON  decodeSentinel = "trailing data in task JSON"
	errMalformedJSON     decodeSentinel = "malformed schedule JSON"
	errInvalidZone       decodeSentinel = "invalid zone"
)

func (e decodeSentinel) Error() string { return string(e) }

const (
	// MessageMaxBytes is the maximum UTF-8 byte length of a task prompt.
	MessageMaxBytes = 32 * 1024
	// TitleMaxRunes is the maximum task title length in Unicode code points.
	TitleMaxRunes = 60
)

// Spec mirrors internal/schedule.Spec for encrypted machine commands.
type Spec struct {
	Notify      string  `json:"notify,omitempty"`
	Title       string  `json:"title"`
	Message     string  `json:"message"`
	Trigger     Trigger `json:"trigger"`
	Target      Target  `json:"target"`
	LateSeconds int64   `json:"late_seconds,omitempty"`
}

// Trigger mirrors internal/schedule.Trigger.
type Trigger struct {
	Time            string   `json:"time,omitempty"`
	Weekdays        []int    `json:"weekdays,omitempty"`
	Timezone        string   `json:"timezone,omitempty"`
	Until           string   `json:"until,omitempty"`
	Type            string   `json:"type"`
	At              string   `json:"at,omitempty"`
	DelaySeconds    int64    `json:"delay_seconds,omitempty"`
	IntervalSeconds int64    `json:"interval_seconds,omitempty"`
	Repeat          bool     `json:"repeat,omitempty"`
	MaxRuns         int64    `json:"max_runs,omitempty"`
}

// Target mirrors internal/schedule.Target.
type Target struct {
	Type    string `json:"type"`
	Session string `json:"session,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

// Takeover records when a user took over an agent-delegated task.
type Takeover struct {
	Session string `json:"session"`
	Via     string `json:"via"`
}

// ManualRequest records a queued extra execution request.
type ManualRequest struct {
	RequestID   string `json:"request_id"`
	RequestedAt string `json:"requested_at"`
}

// Task mirrors internal/schedule.Task.
type Task struct {
	Takeover      *Takeover      `json:"takeover,omitempty"`
	IntervalPhase string         `json:"interval_phase,omitempty"`
	ManualRequest *ManualRequest `json:"manual_request,omitempty"`
	ID            string         `json:"id"`
	Revision      uint64         `json:"revision"`
	Spec          Spec           `json:"spec"`
	Status        string         `json:"status"`
	Reason        string         `json:"reason,omitempty"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
	NextRunAt     string         `json:"next_run_at,omitempty"`
	ScopedSession string         `json:"scoped_session,omitempty"`
	ScopedMode    string         `json:"scoped_mode,omitempty"`
	Source        string         `json:"source"`
	Dispatches    int64          `json:"dispatches"`
	Runs          []Run          `json:"runs,omitempty"`
	Active        *Run           `json:"active_run,omitempty"`
	CreationKey   string         `json:"creation_key,omitempty"`
}

// Run mirrors internal/schedule.Run.
type Run struct {
	NotifyOnFinish        bool   `json:"notify_on_finish,omitempty"`
	NotificationSessionID string `json:"notification_session_id,omitempty"`
	NotificationSeq       uint64 `json:"notification_seq,omitempty"`
	NotificationSource    string `json:"notification_source,omitempty"`
	TargetType            string `json:"target_type"`
	TaskTitle             string `json:"task_title"`
	StartSeq              uint64 `json:"start_seq"`
	Manual                bool   `json:"manual,omitempty"`
	Notified              bool   `json:"notified"`
	NotificationText      string `json:"notification_text,omitempty"`
	FinalSummary          string `json:"final_summary,omitempty"`
	ID                    string `json:"id"`
	SessionID             string `json:"session_id,omitempty"`
	RequestID             string `json:"request_id"`
	ScheduledAt           string `json:"scheduled_at"`
	StartedAt             string `json:"started_at"`
	EndedAt               string `json:"ended_at,omitempty"`
	Status                string `json:"status"`
	Reason                string `json:"reason,omitempty"`
	AfterSeq              uint64 `json:"after_seq"`
	InputPaused           bool   `json:"input_paused,omitempty"`
}

// FirstSeq returns the first transcript seq belonging to this run.
func (r *Run) FirstSeq() uint64 {
	if r == nil {
		return 1
	}
	if r.StartSeq > 0 {
		return r.StartSeq + 1
	}
	return r.AfterSeq + 1
}

// NotifyOnFinish reports whether completion notifications are enabled on spec.
func (s Spec) NotifyOnFinish() bool {
	return s.Notify != "agent_only"
}

// TaskKey uniquely identifies a task across machines.
type TaskKey struct {
	MachineID string
	TaskID    string
}

// TaskEntry pairs a Task with its owning machine ID.
type TaskEntry struct {
	MachineID string
	Task      *Task
}

// TaskGroup represents the execution-state bucket of a task.
type TaskGroup uint8

const (
	GroupAttention TaskGroup = iota + 1
	GroupActive
	GroupPaused
	GroupEnded
)

// InboxGroup represents the list-level grouping in the Schedules screen.
type InboxGroup uint8

const (
	InboxAttention InboxGroup = iota + 1
	InboxRecent
	InboxEnded
)

// RunGroup groups one run or same-day quiet runs in history.
type RunGroup struct {
	Key   string
	Day   string
	Runs  []*Run
	Quiet bool
}

// QuietSpan identifies a foldable span of consecutive quiet scheduled turns in chat.
type QuietSpan struct {
	Key    string
	TaskID string
	RunID  string
	From   uint64
	Through uint64
	TimesMs []int64
}

// ParseRFC3339Ms parses an RFC3339/RFC3339Nano timestamp into Unix milliseconds, returning 0 on empty/invalid input.
func ParseRFC3339Ms(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// FormatRFC3339Ms formats Unix milliseconds as UTC RFC3339.
func FormatRFC3339Ms(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// CloneTask deep-copies a Task.
func CloneTask(t *Task) *Task {
	if t == nil {
		return nil
	}
	cp := *t
	if t.Takeover != nil {
		tk := *t.Takeover
		cp.Takeover = &tk
	}
	if t.ManualRequest != nil {
		mr := *t.ManualRequest
		cp.ManualRequest = &mr
	}
	cp.Spec.Trigger.Weekdays = slices.Clone(t.Spec.Trigger.Weekdays)
	cp.Runs = slices.Clone(t.Runs)
	if t.Active != nil {
		ar := *t.Active
		cp.Active = &ar
	}
	return &cp
}
