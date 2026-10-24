// Package schedule defines the JSON model shared by the local CLI, MCP and
// encrypted machine API. Task contents never need to be visible to the relay.
package schedule

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"time"
	_ "time/tzdata"
)

// MCPScopeHeader marks a scheduling request delegated through a bound MCP.
// It is a capability restriction, not authentication of an OS user.
const MCPScopeHeader = "X-Remote-Agent-Schedule-Scope"

// Spec is a complete task definition; updates replace it atomically.
type Spec struct {
	Title   string  `json:"title" jsonschema:"Short name visible in task lists"`
	Message string  `json:"message" jsonschema:"Message to send to the Agent when triggered"`
	Trigger Trigger `json:"trigger"`
	Target  Target  `json:"target"`
	// LateSeconds bounds catch-up after sleep/offline. Zero means 24 hours.
	LateSeconds int64 `json:"late_seconds,omitempty" jsonschema:"Catch-up window in seconds; zero defaults to 86400. A missed occurrence is skipped."`
}

type Trigger struct {
	Time            string     `json:"time,omitempty" jsonschema:"HH:MM local time for calendar"`
	Weekdays        []int      `json:"weekdays,omitempty" jsonschema:"Calendar weekdays: Monday=1 through Sunday=7; empty means every day"`
	Timezone        string     `json:"timezone,omitempty" jsonschema:"Required IANA timezone for calendar"`
	Until           *time.Time `json:"until,omitempty" jsonschema:"RFC3339 timestamp with timezone; no recurring dispatch after this instant"`
	Type            string     `json:"type" jsonschema:"at (one timestamp), interval (fixed elapsed-time recurrence), calendar (local daily/weekly time), or after_turn (delay from the current turn ending)"`
	At              *time.Time `json:"at,omitempty" jsonschema:"RFC3339 timestamp with timezone, required for at; optional first occurrence for interval"`
	DelaySeconds    int64      `json:"delay_seconds,omitempty" jsonschema:"Nonnegative delay for after_turn, or initial delay for interval without at"`
	IntervalSeconds int64      `json:"interval_seconds,omitempty" jsonschema:"Recurrence interval for interval, minimum 1 second"`
	Repeat          bool       `json:"repeat,omitempty" jsonschema:"For after_turn: repeat the delay after each scheduled execution ends"`
	MaxRuns         int64      `json:"max_runs,omitempty" jsonschema:"Maximum automatic dispatches; zero means no limit"`
}

type Target struct {
	Type    string `json:"type" jsonschema:"resume_session to continue a conversation, or new_session to start a fresh one"`
	Session string `json:"session,omitempty" jsonschema:"Remote Agent session ID or current; required for resume_session"`
	Cwd     string `json:"cwd,omitempty" jsonschema:"Absolute project directory for new_session; defaults to the creating session directory"`
	Agent   string `json:"agent,omitempty" jsonschema:"codex or claude for new_session; defaults to the creating session Agent"`
	Model   string `json:"model,omitempty" jsonschema:"Model for new_session; inherits the creating session model only when using the same Agent"`
	Effort  string `json:"effort,omitempty" jsonschema:"Reasoning effort for new_session"`
	Mode    string `json:"mode,omitempty" jsonschema:"ask, auto or full for new_session; defaults to the creating session mode. New sessions cannot start in plan mode"`
}

// Task is persisted by the daemon. Pausing/canceling stops future dispatches,
// not an execution already handed to a session's input queue.
type Task struct {
	ID            string     `json:"id"`
	Revision      uint64     `json:"revision"`
	Spec          Spec       `json:"spec"`
	Status        string     `json:"status"`
	Reason        string     `json:"reason,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	NextRunAt     *time.Time `json:"next_run_at,omitempty"`
	SourceSession string     `json:"source_session,omitempty"`
	ScopedSession string     `json:"scoped_session,omitempty"` // MCP delegation bound to this session
	ScopedMode    string     `json:"scoped_mode,omitempty"`    // that session's standard mode when the delegation was made
	Source        string     `json:"source"`
	AnchorSession string     `json:"anchor_session,omitempty"`
	AnchorSeq     uint64     `json:"anchor_seq,omitempty"`
	AnchorTurn    string     `json:"anchor_turn,omitempty"`
	Dispatches    int64      `json:"dispatches"`
	Runs          []Run      `json:"runs,omitempty"`
	Active        *Run       `json:"active_run,omitempty"`
	// Creation request identity survives retries and restart.
	CreationKey  string `json:"creation_key,omitempty"`
	CreationHash string `json:"creation_hash,omitempty"`
}

type Run struct {
	ID          string     `json:"id"`
	SessionID   string     `json:"session_id,omitempty"`
	RequestID   string     `json:"request_id"`
	ScheduledAt time.Time  `json:"scheduled_at"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason,omitempty"`
	AfterSeq    uint64     `json:"after_seq"`
	TurnID      string     `json:"turn_id,omitempty"`
	InputPaused bool       `json:"input_paused,omitempty"`
	MessageSeen bool       `json:"message_seen,omitempty"`
}

func DecodeSpec(b []byte) (Spec, error) {
	var s Spec
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, errors.New("invalid task spec JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return s, errors.New("task spec must be one JSON object")
	}
	return s, nil
}

// Terminal tasks no longer dispatch automatically. Active executions are still watched.
func (t *Task) Terminal() bool { return t.Status == "completed" || t.Status == "cancelled" }

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	out := *t
	return &out
}

func Clone(t *Task) *Task {
	out := *t
	out.Spec.Trigger.At = cloneTime(t.Spec.Trigger.At)
	out.Spec.Trigger.Until = cloneTime(t.Spec.Trigger.Until)
	out.Spec.Trigger.Weekdays = slices.Clone(t.Spec.Trigger.Weekdays)
	out.NextRunAt = cloneTime(t.NextRunAt)
	out.Runs = slices.Clone(t.Runs)
	for i := range out.Runs {
		out.Runs[i].EndedAt = cloneTime(t.Runs[i].EndedAt)
	}
	if t.Active != nil {
		run := *t.Active
		run.EndedAt = cloneTime(t.Active.EndedAt)
		out.Active = &run
	}
	return &out
}
