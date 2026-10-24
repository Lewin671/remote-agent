package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/contextio"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"google.golang.org/protobuf/proto"
)

// The schedule lock covers mutations, never session launch or input delivery. A durable
// dispatching record is written before any effect. After a crash there we
// report uncertainty instead of repeating an operation that may have run.
func (d *Daemon) loadSchedules() error {
	d.scheduleMu.Lock()
	defer d.scheduleMu.Unlock()
	d.tasks = map[string]*schedule.Task{}
	b, err := os.ReadFile(filepath.Join(paths.Dir(), "schedules.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state struct {
		Version  int                       `json:"version"`
		Revision uint64                    `json:"revision"`
		Tasks    map[string]*schedule.Task `json:"tasks"`
	}
	if json.Unmarshal(b, &state) != nil || state.Version != 1 {
		return errors.New("cannot load scheduled tasks; refusing to discard them")
	}
	for id, t := range state.Tasks {
		if t == nil || t.ID != id || t.Revision == 0 {
			return errors.New("invalid scheduled task state")
		}
	}
	d.scheduleRevision = state.Revision
	d.scheduleVisible = visibleSchedules(state.Tasks)
	for id, t := range state.Tasks {
		d.tasks[id] = t
		if t.Active != nil && t.Active.Status == "dispatching" {
			now := time.Now().UTC()
			t.Active.Status = "uncertain"
			t.Active.Reason = "daemon_restarted_during_dispatch"
			t.Active.EndedAt = &now
			t.Runs = appendRun(t.Runs, *t.Active)
			t.Active = nil
			t.Status = "uncertain"
			t.Reason = "daemon_restarted_during_dispatch"
			t.NextRunAt = nil
			touchTask(t, now)
		}
	}
	return d.saveSchedulesLocked()
}
func (d *Daemon) saveSchedulesLocked() error {
	visible := visibleSchedules(d.tasks)
	revision := d.scheduleRevision
	if !bytes.Equal(visible, d.scheduleVisible) {
		revision++
	}
	b, err := json.Marshal(struct {
		Version  int                       `json:"version"`
		Revision uint64                    `json:"revision"`
		Tasks    map[string]*schedule.Task `json:"tasks"`
	}{1, revision, d.tasks})
	if err != nil {
		return err
	}
	err = d.writeStateFile(filepath.Join(paths.Dir(), "schedules.json"), b)
	if err == nil || fileReplaced(err) {
		d.scheduleRevision, d.scheduleVisible = revision, visible
	}
	return err
}

const maxNonTerminalTasks = 100

func (d *Daemon) nonTerminalTasksLocked() int {
	count := 0
	for _, t := range d.tasks {
		if !t.Terminal() {
			count++
		}
	}
	return count
}

func scheduleCapacityError() error {
	return connect.NewError(connect.CodeResourceExhausted, errors.New("daemon has reached the 100 non-terminal scheduled task limit"))
}

func touchTask(t *schedule.Task, now time.Time) { t.Revision++; t.UpdatedAt = now }
func appendRun(runs []schedule.Run, run schedule.Run) []schedule.Run {
	runs = append(runs, run)
	if len(runs) > 100 {
		runs = runs[len(runs)-100:]
	}
	return runs
}

func (h *handler) Schedule(ctx context.Context, req *connect.Request[pb.ScheduleRequest]) (*connect.Response[pb.ScheduleResponse], error) {
	origin := originOf(ctx)
	scope := req.Header().Get(schedule.MCPScopeHeader)
	b, err := h.d.scheduleCommandScopedContext(ctx, req.Msg, origin, scope)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.ScheduleResponse{ResultJson: b}), nil
}

func (d *Daemon) scheduleCommand(q *pb.ScheduleRequest, origin *pb.Origin) ([]byte, error) {
	return d.scheduleCommandScoped(q, origin, "")
}

func (d *Daemon) scheduleCommandScoped(q *pb.ScheduleRequest, origin *pb.Origin, scope string) ([]byte, error) {
	return d.scheduleCommandScopedContext(context.Background(), q, origin, scope)
}

func (d *Daemon) scheduleCommandScopedContext(ctx context.Context, q *pb.ScheduleRequest, origin *pb.Origin, scope string) ([]byte, error) {
	if q.Operation == "notify_user" {
		return d.notifyScheduled(q, scope)
	}
	d.scheduleMu.Lock()
	defer d.scheduleMu.Unlock()
	if d.tasks == nil {
		d.tasks = map[string]*schedule.Task{}
	}
	now := time.Now().UTC()
	if scope != "" {
		if scope != q.SessionId {
			return nil, mcpScheduleDenied()
		}
		if _, err := d.exact(scope); err != nil {
			return nil, mcpScheduleDenied()
		}
	}
	if scope != "" && (q.Operation == "create" || q.Operation == "update" || q.Operation == "resume" || q.Operation == "run") {
		if d.sessionHasScheduledRunLocked(scope) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("MCP cannot create, update, resume or run tasks during a scheduled execution"))
		}
	}
	if q.Operation == "list" {
		tasks := make([]*schedule.Task, 0, len(d.tasks))
		for _, t := range d.tasks {
			if scope != "" && (t.ScopedSession != scope || t.SourceSession != scope) {
				continue
			}
			tasks = append(tasks, t)
		}
		sort.Slice(tasks, func(i, j int) bool {
			if tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
				return tasks[i].ID < tasks[j].ID
			}
			return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
		})
		return json.Marshal(struct {
			Tasks []*schedule.Task `json:"tasks"`
		}{tasks})
	}
	if q.Operation == "create" {
		if len(q.SpecJson) > 64<<10 || len(q.RequestId) > 256 {
			return nil, errors.New("task request is too large")
		}
		spec, err := schedule.DecodeSpec(q.SpecJson)
		if err != nil {
			return nil, err
		}
		// Hash the unnormalized input and its context so retries do not inherit
		// changed session settings. Device origins namespace creation keys.
		b, _ := json.Marshal(spec)
		sum := sha256.Sum256(append(append(b, 0), []byte(q.SessionId)...))
		hash := hex.EncodeToString(sum[:])
		key := ""
		if q.RequestId != "" {
			key = origin.DeviceId + ":" + q.RequestId
			if scope != "" {
				key = "mcp:" + scope + ":" + q.RequestId
			}
		}
		if key != "" {
			for _, t := range d.tasks {
				if t.CreationKey == key {
					if scope != "" && (t.ScopedSession != scope || t.SourceSession != scope) {
						return nil, mcpScheduleDenied()
					}
					if t.CreationHash != hash {
						return nil, errors.New("creation key already belongs to a different task")
					}
					return json.Marshal(t)
				}
			}
		}
		if d.nonTerminalTasksLocked() >= maxNonTerminalTasks {
			return nil, scheduleCapacityError()
		}
		t := &schedule.Task{ID: newID(16), Revision: 1, Spec: spec, CreatedAt: now, UpdatedAt: now, SourceSession: q.SessionId, Source: "local", CreationKey: key, CreationHash: hash}
		if scope != "" {
			t.Source, t.ScopedSession = "agent", scope
			if err := d.restrictMCPTask(t, true); err != nil {
				return nil, err
			}
		}
		if origin.Kind == pb.OriginKind_ORIGIN_KIND_CLIENT {
			t.Source = "client"
		}
		if err := d.prepareSchedule(ctx, t, now); err != nil {
			return nil, err
		}
		d.tasks[t.ID] = t
		if err := d.saveSchedulesLocked(); err != nil {
			if !fileReplaced(err) {
				delete(d.tasks, t.ID)
			}
			return nil, fmt.Errorf("could not persist scheduled task: %w", err)
		}
		return json.Marshal(t)
	}
	t, ok := d.tasks[q.TaskId]
	if !ok {
		return nil, errNotFound
	}
	if scope != "" && (t.ScopedSession != scope || t.SourceSession != scope) {
		return nil, mcpScheduleDenied()
	}
	if q.Operation == "get" {
		return json.Marshal(t)
	}
	if q.ExpectedRevision != 0 && q.ExpectedRevision != t.Revision {
		return nil, connect.NewError(connect.CodeAborted, errors.New("scheduled task changed; reload before editing"))
	}
	// An execution that was confirmed late is taken up first: no operation
	// moves on from an execution that is in fact under way.
	if before := schedule.Clone(t); d.reconfirmScheduledRun(t, now) {
		touchTask(t, now)
		if err := d.saveSchedulesLocked(); err != nil {
			if !fileReplaced(err) {
				d.tasks[t.ID] = before
				d.lateRuns[t.ID] = before.Runs[len(before.Runs)-1].ID
			}
			return nil, fmt.Errorf("could not persist scheduled task: %w", err)
		}
	}
	prev := schedule.Clone(t)
	switch q.Operation {
	case "update":
		if t.Active != nil {
			return nil, errors.New("wait for the active execution before replacing the task definition")
		}
		if len(q.SpecJson) > 64<<10 {
			return nil, errors.New("task request is too large")
		}
		spec, err := schedule.DecodeSpec(q.SpecJson)
		if err != nil {
			return nil, err
		}
		t.Spec = spec
		if q.SessionId != "" {
			t.SourceSession = q.SessionId
		}
		if scope != "" {
			if err := d.restrictMCPTask(t, true); err != nil {
				d.tasks[t.ID] = prev
				return nil, err
			}
		}
		if err := d.prepareSchedule(ctx, t, now); err != nil {
			d.tasks[t.ID] = prev
			return nil, err
		}
	case "pause":
		if t.Status == "cancelled" {
			return nil, errors.New("cancelled tasks cannot be paused")
		}
		t.Status = "paused"
		t.Reason = "user_paused"
	case "cancel":
		t.Status = "cancelled"
		t.Reason = "user_cancelled"
		t.NextRunAt = nil
	case "resume":
		if scope != "" {
			if err := d.restrictMCPTask(t, false); err != nil {
				return nil, err
			}
		}
		if t.Status != "paused" {
			return nil, errors.New("only paused tasks can be resumed")
		}
		if t.Active != nil && t.Active.InputPaused {
			return nil, errors.New("execution input is paused; use resume-input or cancel that input first")
		}
		t.Reason = ""
		if t.Active != nil {
			t.Status = t.Active.Status
		} else if t.Spec.Trigger.Type == "interval" || t.Spec.Trigger.Type == "calendar" {
			d.nextSchedule(t, now)
		} else if t.NextRunAt != nil {
			t.Status = "scheduled"
		} else if t.Spec.Trigger.Type == "after_turn" && prev.Reason != "completed_while_paused" {
			if err := d.prepareSchedule(ctx, t, now); err != nil {
				d.tasks[t.ID] = prev
				return nil, err
			}
		} else {
			t.Status = "completed"
		}
	case "run":
		if until := t.Spec.Trigger.Until; until != nil && now.After(*until) {
			return nil, errors.New("task deadline has passed; update until before running")
		}
		if scope != "" {
			if err := d.restrictMCPTask(t, false); err != nil {
				return nil, err
			}
		}
		if t.Spec.Trigger.Type != "calendar" && t.Spec.Trigger.MaxRuns > 0 && t.Dispatches >= t.Spec.Trigger.MaxRuns {
			return nil, errors.New("task run limit reached; update max_runs before running")
		}
		if t.Active != nil {
			return nil, errors.New("this task already has an active execution")
		}
		if t.Status == "cancelled" {
			return nil, errors.New("cancelled tasks cannot run; create a new task")
		}
		if t.Spec.Trigger.Type == "calendar" && !t.RunNow {
			t.AutomaticStatus, t.AutomaticReason = t.Status, t.Reason
			t.AutomaticNextRunAt = t.NextRunAt
			t.RunNow = true
		}
		t.Status, t.Reason = "scheduled", ""
		t.NextRunAt = &now
	default:
		return nil, errors.New("unknown schedule operation")
	}
	if prev.Terminal() && !t.Terminal() && d.nonTerminalTasksLocked() > maxNonTerminalTasks {
		d.tasks[t.ID] = prev
		return nil, scheduleCapacityError()
	}
	// A machine-level request takes an agent's task over as the user's own.
	if scope == "" && (q.Operation == "resume" || q.Operation == "run" || q.Operation == "update") {
		t.ScopedSession, t.ScopedMode = "", ""
		t.Source = "local"
		if origin.Kind == pb.OriginKind_ORIGIN_KIND_CLIENT {
			t.Source = "client"
		}
	}
	touchTask(t, now)
	if err := d.saveSchedulesLocked(); err != nil {
		if !fileReplaced(err) {
			d.tasks[t.ID] = prev
		}
		return nil, fmt.Errorf("could not persist scheduled task: %w", err)
	}
	// Pausing waits for the uncertain execution's late record; anything
	// else moves on from it.
	if q.Operation != "pause" {
		delete(d.lateRuns, t.ID)
	}
	return json.Marshal(t)
}

func scheduleAgent(name string) (pb.Agent, error) {
	switch name {
	case "codex":
		return pb.Agent_AGENT_CODEX, nil
	case "claude":
		return pb.Agent_AGENT_CLAUDE, nil
	}
	return 0, errors.New("agent must be codex or claude")
}
func scheduleMode(name string) (pb.ApprovalMode, error) {
	switch name {
	case "":
		return 0, nil
	case "ask":
		return pb.ApprovalMode_APPROVAL_MODE_ASK, nil
	case "auto":
		return pb.ApprovalMode_APPROVAL_MODE_AUTO, nil
	case "full":
		return pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS, nil
	case "plan":
		return pb.ApprovalMode_APPROVAL_MODE_PLAN, nil
	}
	return 0, errors.New("mode must be ask, auto, full or plan")
}

func (d *Daemon) prepareSchedule(ctx context.Context, t *schedule.Task, now time.Time) error {
	spec := &t.Spec
	tr := &spec.Trigger
	target := &spec.Target
	if strings.TrimSpace(spec.Title) == "" || len(spec.Title) > 512 {
		return errors.New("title is required and must fit in 512 bytes")
	}
	if strings.TrimSpace(spec.Message) == "" || len(spec.Message) > 32<<10 || strings.ContainsRune(spec.Message, 0) {
		return errors.New("message is required and must fit in 32768 bytes without NUL")
	}
	const maxSeconds = int64(10 * 365 * 24 * 60 * 60)
	if tr.DelaySeconds < 0 || tr.DelaySeconds > maxSeconds || tr.IntervalSeconds < 0 || tr.IntervalSeconds > maxSeconds || spec.LateSeconds < 0 || spec.LateSeconds > maxSeconds || tr.MaxRuns < 0 {
		return errors.New("invalid time interval or run limit")
	}
	if tr.At != nil && (tr.At.Before(now.Add(-time.Duration(maxSeconds)*time.Second)) || tr.At.After(now.Add(time.Duration(maxSeconds)*time.Second))) {
		return errors.New("timestamp must be within ten years of now")
	}
	if err := tr.ValidateCalendar(); err != nil {
		return err
	}
	if tr.Until != nil && tr.Type != "interval" && tr.Type != "calendar" && !(tr.Type == "after_turn" && tr.Repeat) {
		return errors.New("until requires a recurring trigger")
	}
	if tr.Until != nil && now.After(*tr.Until) {
		return errors.New("until must not be in the past")
	}
	var source *session
	if t.SourceSession != "" {
		var err error
		source, err = d.session(t.SourceSession)
		if err != nil {
			return err
		}
		t.SourceSession = source.info().Id
	}
	switch target.Type {
	case "resume_session":
		id := target.Session
		if id == "current" || id == "" {
			id = t.SourceSession
		}
		s, err := d.session(id)
		if err != nil {
			return err
		}
		target.Session = s.info().Id
		if target.Cwd != "" || target.Agent != "" || target.Model != "" || target.Effort != "" || target.Mode != "" {
			return errors.New("resume_session only accepts session; it preserves that conversation's settings")
		}
	case "new_session":
		if target.Session != "" {
			return errors.New("new_session does not accept session")
		}
		if source != nil {
			info := source.info()
			if target.Cwd == "" {
				target.Cwd = info.Cwd
			}
			if target.Agent == "" {
				if info.Agent == pb.Agent_AGENT_CLAUDE {
					target.Agent = "claude"
				} else {
					target.Agent = "codex"
				}
			}
			sourceAgent := "codex"
			if info.Agent == pb.Agent_AGENT_CLAUDE {
				sourceAgent = "claude"
			}
			if target.Agent == sourceAgent {
				if target.Model == "" {
					target.Model = info.Model
				}
				if target.Effort == "" {
					target.Effort = info.Effort
				}
			}
			// The client-facing label is not enough to inherit from: ASK also
			// covers a read-only sandbox, which starting a new session in
			// "ask" would widen. An unknown/nonstandard mode cannot be
			// silently replaced by defaults either.
			if target.Mode == "" {
				target.Mode = exactScheduleMode(info)
			}
			if target.Mode == "" {
				return errors.New("set an explicit mode for a new session when the source mode is unknown or nonstandard")
			}
		}
		if !filepath.IsAbs(target.Cwd) {
			return errors.New("new_session needs an absolute cwd or a creating session")
		}
		if st, err := contextio.Stat(ctx, target.Cwd); err != nil || !st.IsDir() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("new_session cwd is not a directory")
		}
		if target.Agent == "" {
			target.Agent = "codex"
		}
		kind, err := scheduleAgent(target.Agent)
		if err != nil {
			return err
		}
		ag, err := d.knownAgent(kind)
		if err != nil {
			return err
		}
		if ag.Bin() == "" {
			return errors.New("requested Agent is not installed")
		}
		mode, err := scheduleMode(target.Mode)
		if err != nil {
			return err
		}
		if _, err = d.sessionArgsContext(ctx, ag, &pb.CreateSessionRequest{Model: target.Model, Effort: target.Effort, Mode: mode}); err != nil {
			return err
		}
	default:
		return errors.New("target type must be resume_session or new_session")
	}
	t.AnchorSession = ""
	t.AnchorSeq = 0
	t.AnchorTurn = ""
	t.Reason = ""
	t.NextRunAt = nil
	t.RunNow, t.AutomaticNextRunAt = false, nil
	t.AutomaticStatus, t.AutomaticReason = "", ""
	switch tr.Type {
	case "calendar":
		at := nextCalendar(t, now)
		t.NextRunAt, t.Status = &at, "scheduled"
	case "at":
		if tr.At == nil || tr.At.IsZero() || tr.Repeat || tr.IntervalSeconds != 0 || tr.DelaySeconds != 0 {
			return errors.New("at requires a timestamp and no recurrence or delay")
		}
		at := tr.At.UTC()
		t.NextRunAt = &at
		t.Status = "scheduled"
	case "interval":
		if tr.IntervalSeconds < 1 || tr.Repeat {
			return errors.New("interval requires interval_seconds >= 1; repeat is only for after_turn")
		}
		at := now.Add(time.Duration(tr.DelaySeconds) * time.Second)
		if tr.At != nil {
			at = tr.At.UTC()
		}
		t.NextRunAt = &at
		t.Status = "scheduled"
	case "after_turn":
		if tr.At != nil || tr.IntervalSeconds != 0 {
			return errors.New("after_turn only accepts delay_seconds, repeat and max_runs")
		}
		anchor := t.SourceSession
		if target.Type == "resume_session" {
			anchor = target.Session
		}
		s, err := d.session(anchor)
		if err != nil {
			return errors.New("after_turn needs a source session")
		}
		t.AnchorSession = s.info().Id
		s.mu.Lock()
		exited := s.meta.Exited
		s.mu.Unlock()
		running := false
		if err := s.log.Each(func(e *pb.Event) {
			t.AnchorSeq = e.Seq
			if e.GetTurnStarted() != nil {
				t.AnchorTurn = e.TurnId
				running = true
			}
			if e.GetTurnEnded() != nil {
				running = false
			}
		}); err != nil {
			return fmt.Errorf("read source session events: %w", err)
		}
		if running && !exited {
			t.Status = "waiting_turn"
		} else {
			at := now.Add(time.Duration(tr.DelaySeconds) * time.Second)
			t.NextRunAt = &at
			t.Status = "scheduled"
		}
	default:
		return errors.New("trigger type must be at, interval, calendar or after_turn")
	}
	d.expireSchedule(t, now)
	return nil
}

// startScheduleLoop gives Run ownership of every dispatch until it returns.
func (d *Daemon) startScheduleLoop(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); d.scheduleLoop(ctx) }()
	return func() { cancel(); <-done }
}

func (d *Daemon) scheduleLoop(ctx context.Context) {
	cleaning := map[string]bool{}
	var cleaningMu sync.Mutex
	var dispatches sync.WaitGroup
	defer dispatches.Wait()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			for _, task := range d.prepareScheduledDispatches(now.UTC()) {
				dispatches.Go(func() { d.dispatchScheduled(ctx, task, now.UTC()) })
			}
			// Closing a CLI can block; lifecycle work shares dispatch ownership,
			// never the schedule lock, and at most one closer per session.
			d.stopUnpublishedScheduledRuns()
			for _, s := range d.scheduledClosures() {
				cleaningMu.Lock()
				if !cleaning[s.meta.ID] {
					cleaning[s.meta.ID] = true
					dispatches.Go(func() {
						defer func() { cleaningMu.Lock(); delete(cleaning, s.meta.ID); cleaningMu.Unlock() }()
						if err := s.killIf(ctx, true); err != nil {
							slog.Warn("close scheduled CLI", "err", errClass(err))
						}
					})
				}
				cleaningMu.Unlock()
			}
		}
	}
}
func (d *Daemon) prepareScheduledDispatches(now time.Time) []*schedule.Task {
	var dispatches []*schedule.Task
	d.scheduleMu.Lock()
	defer d.scheduleMu.Unlock()
	for _, t := range d.tasks {
		if t.Terminal() && t.Active == nil && d.lateRuns[t.ID] == "" && (len(t.Runs) == 0 || t.Runs[len(t.Runs)-1].Status != "completed" || t.Runs[len(t.Runs)-1].FollowupsClosed) {
			continue
		}
		prev := schedule.Clone(t)
		changed, dispatch := false, false
		late, hadLate := d.lateRuns[t.ID]
		changed = d.watchScheduledFollowups(t)
		if t.Active != nil {
			changed = d.watchScheduledRun(t, now) || changed
		} else {
			changed = d.reconfirmScheduledRun(t, now) || changed
		}
		if t.Status == "waiting_turn" {
			s, err := d.exact(t.AnchorSession)
			if err != nil {
				t.Status = "failed"
				t.Reason = "source_session_missing"
				changed = true
			} else {
				err := scanEventsSince(s.log, t.AnchorSeq, func(e *pb.Event) bool {
					t.AnchorSeq = e.Seq
					changed = true
					if end := e.GetTurnEnded(); end != nil && (t.AnchorTurn == "" || e.TurnId == "" || e.TurnId == t.AnchorTurn) {
						if end.Outcome == pb.TurnOutcome_TURN_OUTCOME_ABORTED {
							t.Status = "paused"
							t.Reason = "source_turn_interrupted"
						} else {
							at := now
							if e.Time != nil {
								at = e.Time.AsTime()
							}
							at = at.Add(time.Duration(t.Spec.Trigger.DelaySeconds) * time.Second)
							t.NextRunAt = &at
							t.Status = "scheduled"
						}
						return false
					}
					return true
				})
				if err != nil {
					slog.Warn("scan scheduled source events", "session", t.AnchorSession, "err", errClass(err))
				}
				s.mu.Lock()
				exited := s.meta.Exited
				s.mu.Unlock()
				if exited && t.Status == "waiting_turn" {
					t.Status = "paused"
					t.Reason = "source_exited_before_turn_end"
					changed = true
				}
			}
		}
		if t.Active == nil && d.expireSchedule(t, now) {
			changed = true
		}
		if t.Status == "scheduled" && t.Active == nil && t.NextRunAt != nil && !now.Before(*t.NextRunAt) {
			changed = true
			late := t.Spec.LateSeconds
			if late == 0 {
				late = 86400
			}
			// Of an interval's occurrences missed while the machine slept
			// or the daemon was down, only the most recent is caught up,
			// and it is the one the time allowed for that is measured from.
			if tr := t.Spec.Trigger; tr.Type == "interval" {
				interval := time.Duration(tr.IntervalSeconds) * time.Second
				latest := t.NextRunAt.Add(now.Sub(*t.NextRunAt) / interval * interval)
				t.NextRunAt = &latest
			}
			if tr := t.Spec.Trigger; tr.Type == "calendar" && !t.RunNow {
				latest := tr.LatestCalendar(now)
				if latest.After(*t.NextRunAt) {
					t.NextRunAt = &latest
				}
			}
			if now.Sub(*t.NextRunAt) > time.Duration(late)*time.Second {
				run := schedule.Run{ID: newID(8), ScheduledAt: *t.NextRunAt, StartedAt: now, EndedAt: &now, Status: "skipped", Reason: "catch_up_window_expired", Manual: t.RunNow}
				t.Runs = appendRun(t.Runs, run)
				if t.RunNow {
					t.RunNow = false
					d.restoreManualSchedule(t, now)
				} else {
					d.nextSchedule(t, now)
				}
			} else if tr := t.Spec.Trigger; tr.Type == "calendar" && !t.RunNow && t.LastCalendarDate == calendarDate(tr, *t.NextRunAt) {
				d.nextSchedule(t, now)
			} else if !t.RunNow && t.Spec.Trigger.MaxRuns > 0 && t.Dispatches >= t.Spec.Trigger.MaxRuns {
				t.Status = "completed"
				t.NextRunAt = nil
				t.Reason = "run_limit_reached"
			} else {
				if t.ScopedSession != "" && d.restrictMCPTask(t, false) != nil {
					t.Status, t.Reason = "paused", "agent_schedule_permissions_changed"
				} else {
					t.Active = &schedule.Run{ID: newID(8), RequestID: "schedule-" + newID(16), ScheduledAt: *t.NextRunAt, StartedAt: now, Status: "dispatching", Manual: t.RunNow}
					t.Status = "dispatching"
					if t.Spec.Trigger.Type != "calendar" || !t.RunNow {
						t.Dispatches++
					}
					if t.Spec.Trigger.Type == "calendar" && !t.RunNow {
						t.LastCalendarDate = calendarDate(t.Spec.Trigger, t.Active.ScheduledAt)
					}
					t.RunNow = false
					dispatch = true
				}
			}
		}
		if changed {
			if !bytes.Equal(visibleTask(prev), visibleTask(t)) {
				touchTask(t, now)
			}
			if err := d.saveSchedulesLocked(); err != nil {
				if !fileReplaced(err) {
					d.tasks[t.ID] = prev
					if hadLate {
						d.lateRuns[t.ID] = late
					}
				} else {
					t.Status = "uncertain"
					t.Reason = "could_not_persist_dispatch_result"
					t.NextRunAt = nil
					if t.Active != nil {
						t.Active.Status = "uncertain"
					}
				}
				slog.Error("save scheduled tasks", "task", t.ID, "err", errClass(err))
				continue
			}
			if dispatch {
				dispatches = append(dispatches, schedule.Clone(t))
			}
		}
	}
	return dispatches
}

// launchScheduledSession only reads its detached task snapshot.
func (d *Daemon) launchScheduledSession(ctx context.Context, t *schedule.Task) (*session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var s *session
	var err error
	target := t.Spec.Target
	if target.Type == "resume_session" {
		s, err = d.exact(target.Session)
		if err == nil {
			s.mu.Lock()
			exited := s.meta.Exited
			s.mu.Unlock()
			if exited {
				s, err = d.resumeContext(ctx, target.Session)
				if errors.Is(err, errRunning) {
					s, err = d.exact(target.Session)
				}
			}
		}
	} else {
		kind, _ := scheduleAgent(target.Agent)
		mode, _ := scheduleMode(target.Mode)
		// A run continues its source session's way: one that started
		// through the login shell does so again (its environment is
		// computed then), any other keeps the saved environment. A run
		// derived from a terminal is terminal-origin even when its saved
		// environment cannot be read: it must not fall back to the
		// daemon's CLI.
		env := map[string]string{}
		shell := false
		if t.SourceSession != "" {
			if m, err := loadMeta(paths.SessionDir(t.SourceSession)); err == nil {
				shell = m.Shell
			}
			env, _ = readEnvFile(filepath.Join(paths.SessionDir(t.SourceSession), "env.json"))
		}
		terminal := t.SourceSession != "" && !shell
		// No prompt here: the scheduled input uses its own stable request ID.
		s, err = d.createByContext(ctx, &pb.CreateSessionRequest{Cwd: target.Cwd, Agent: kind, Model: target.Model, Effort: target.Effort, Mode: mode, Env: env}, &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_SYSTEM, ScheduledTaskId: t.ID, ScheduledRunId: t.Active.ID}, shell, terminal, "the session's saved PATH")
	}
	return s, err
}

func (d *Daemon) dispatchScheduled(ctx context.Context, snapshot *schedule.Task, now time.Time) {
	s, err := d.launchScheduledSession(ctx, snapshot)
	// Shutdown leaves the durable dispatching boundary for restart recovery,
	// even when launch or delivery concurrently returns another error.
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		d.finishScheduledDispatch(ctx, snapshot, now, "could_not_start_session: "+err.Error(), nil)
		return
	}
	d.queueScheduledSession(ctx, snapshot, now, s)
}

func (d *Daemon) queueScheduledSession(ctx context.Context, snapshot *schedule.Task, now time.Time, s *session) {
	// Publication refusal and admission have one order, including the gap
	// between persisting the session identity and accepting its input.
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	sessionID, after := s.info().Id, s.log.LastSeq()
	d.scheduleMu.Lock()
	t := d.dispatchingTaskLocked(snapshot)
	if t == nil || ctx.Err() != nil {
		d.scheduleMu.Unlock()
		return
	}
	t.Active.SessionID, t.Active.AfterSeq, t.Active.StartSeq = sessionID, after, after
	touchTask(t, now)
	// The session identity must be durable before input is accepted.
	if err := d.saveSchedulesLocked(); err != nil {
		d.uncertainDispatchLocked(t, "could_not_persist_execution")
		d.scheduleMu.Unlock()
		slog.Error("save scheduled execution", "task", snapshot.ID, "err", errClass(err))
		return
	}
	d.scheduleMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	origin := proto.Clone(originSystem).(*pb.Origin)
	origin.RequestId = snapshot.Active.RequestID
	origin.ScheduledTaskId, origin.ScheduledRunId = snapshot.ID, snapshot.Active.ID
	_, err := s.enqueueMode(snapshot.Spec.Message, snapshot.Active.RequestID, nil, origin, pb.SendMode_SEND_MODE_QUEUE)
	reason := ""
	if !queueAccepted(err) {
		reason = "could_not_queue_message"
	}
	d.finishScheduledDispatch(ctx, snapshot, now, reason, err)
}

func (d *Daemon) dispatchingTaskLocked(snapshot *schedule.Task) *schedule.Task {
	t := d.tasks[snapshot.ID]
	if t == nil || t.Active == nil || t.Active.ID != snapshot.Active.ID || t.Active.Status != "dispatching" {
		return nil
	}
	return t
}

func (d *Daemon) uncertainDispatchLocked(t *schedule.Task, reason string) {
	t.Active.Status = "uncertain"
	t.Active.Reason = reason
	t.NextRunAt = nil
	if t.Status != "paused" && t.Status != "cancelled" {
		t.Status, t.Reason = "uncertain", reason
	}
}

func (d *Daemon) finishScheduledDispatch(ctx context.Context, snapshot *schedule.Task, now time.Time, reason string, deliveryErr error) {
	d.scheduleMu.Lock()
	defer d.scheduleMu.Unlock()
	t := d.dispatchingTaskLocked(snapshot)
	if t == nil || ctx.Err() != nil {
		return
	}
	run := t.Active
	if reason != "" {
		d.endScheduledRun(t, now, "failed", reason)
	} else {
		run.Status, run.Reason = "queued", ""
		if deliveryErr != nil {
			run.Reason = "message queued, but event recording failed; do not send another copy"
		}
		if t.Status != "paused" && t.Status != "cancelled" {
			t.Status, t.Reason = run.Status, run.Reason
		}
	}
	if t.Active != nil || !run.Manual {
		t.NextRunAt = nil
	}
	touchTask(t, now)
	if err := d.saveSchedulesLocked(); err != nil {
		// Effects may already have happened. Never restore a dispatchable task.
		if t.Active != nil {
			d.uncertainDispatchLocked(t, "could_not_persist_dispatch_result")
		}
		slog.Error("save scheduled dispatch result", "task", t.ID, "err", errClass(err))
	}
}

func (d *Daemon) watchScheduledRun(t *schedule.Task, now time.Time) (changed bool) {
	run := t.Active
	if run.Status == "uncertain" || run.Status == "dispatching" {
		return false
	}
	s, err := d.exact(run.SessionID)
	if err != nil {
		d.endScheduledRun(t, now, "failed", "execution_session_missing")
		return true
	}
	err = scanEventsSince(s.log, run.AfterSeq, func(e *pb.Event) bool {
		run.AfterSeq = e.Seq
		changed = true
		if in := e.GetInputState(); in != nil && in.RequestId == run.RequestID {
			run.InputPaused = in.Paused
			switch in.Status {
			case pb.InputStatus_INPUT_STATUS_SUBMITTED:
				if run.Status == "queued" {
					run.Status = "submitted"
				}
			case pb.InputStatus_INPUT_STATUS_UNCONFIRMED:
				if d.lateRuns == nil {
					d.lateRuns = map[string]string{}
				}
				d.lateRuns[t.ID] = run.ID
				d.endScheduledRun(t, now, "uncertain", unconfirmedRun)
				return false
			case pb.InputStatus_INPUT_STATUS_CANCELED:
				d.endScheduledRun(t, now, "cancelled", "message_cancelled")
				return false
			case pb.InputStatus_INPUT_STATUS_FAILED, pb.InputStatus_INPUT_STATUS_NEEDS_TERMINAL:
				d.endScheduledRun(t, now, "failed", "message_delivery_failed")
				return false
			}
		}
		if msg := e.GetUserMessage(); msg != nil && msg.RequestId == run.RequestID {
			run.MessageSeen = true
			run.TurnID = e.TurnId
			run.Status = "running"
		}
		if notice := e.GetScheduledNotification(); notice != nil && notice.RunId == run.ID {
			run.Notified, run.NotificationText = true, notice.Text
			run.NotificationCount++
		}
		if msg := e.GetAgentMessage(); msg != nil && run.MessageSeen && (run.TurnID == "" || e.TurnId == "" || e.TurnId == run.TurnID) && msg.Phase == "final_answer" {
			run.FinalSummary = finalRunSummary(msg.Text)
		}
		if end := e.GetTurnEnded(); end != nil && run.MessageSeen && (run.TurnID == "" || e.TurnId == "" || e.TurnId == run.TurnID) {
			status := "completed"
			reason := ""
			if end.Outcome == pb.TurnOutcome_TURN_OUTCOME_ABORTED {
				status = "interrupted"
				reason = "execution_interrupted"
			}
			endedAt := now
			if e.Time != nil {
				endedAt = e.Time.AsTime()
			}
			d.endScheduledRun(t, endedAt, status, reason)
			return false
		}
		return true
	})
	if err != nil {
		slog.Warn("scan scheduled execution events", "session", run.SessionID, "err", errClass(err))
	}
	if t.Active != run {
		return true
	}
	status, reason := t.Status, t.Reason
	// Cursor and message/turn identity are persisted together. Replaying after
	// restart cannot mistake an unrelated earlier turn for this execution.
	if run.InputPaused && t.Status != "cancelled" && (t.Status != "paused" || t.Reason == "execution_input_paused") {
		t.Status = "paused"
		t.Reason = "execution_input_paused"
	} else if !run.InputPaused && t.Status == "paused" && t.Reason == "execution_input_paused" {
		t.Status = run.Status
		t.Reason = ""
	}
	if t.Status != "paused" && t.Status != "cancelled" {
		t.Status = run.Status
	}
	s.mu.Lock()
	exited := s.meta.Exited
	s.mu.Unlock()
	changed = changed || status != t.Status || reason != t.Reason
	if exited {
		d.endScheduledRun(t, now, "failed", "execution_session_exited")
		changed = true
	}
	return changed
}

// unconfirmedRun is why an execution ended uncertain when its message was
// reported UNCONFIRMED.
const unconfirmedRun = "message_submission_unconfirmed"

// reconfirmScheduledRun takes up again the execution of a task that ended
// uncertain because its message was reported UNCONFIRMED, once the CLI's
// record confirms the message after all (inject.go, confirmLate), and
// reports whether any state (including the scan cursor) changed. Until then, and for good if no record comes, the
// task stays uncertain; the message is never sent again. A task paused
// meanwhile stays paused: its execution is followed to the end all the
// same, as one that was running when the task was paused. Any other
// operation on the task moves on from that execution (scheduleCommand).
func (d *Daemon) reconfirmScheduledRun(t *schedule.Task, now time.Time) bool {
	id, ok := d.lateRuns[t.ID]
	paused := t.Status == "paused"
	if !ok || !paused && (t.Status != "uncertain" || t.Reason != unconfirmedRun) || len(t.Runs) == 0 {
		return false
	}
	last := &t.Runs[len(t.Runs)-1]
	if last.ID != id || last.Status != "uncertain" || last.Reason != unconfirmedRun {
		return false
	}
	s, err := d.exact(last.SessionID)
	if err != nil {
		return false
	}
	changed := false
	err = scanEventsSince(s.log, last.AfterSeq, func(e *pb.Event) bool {
		last.AfterSeq = e.Seq
		changed = true
		if in := e.GetInputState(); in != nil && in.RequestId == last.RequestID && in.Status == pb.InputStatus_INPUT_STATUS_SUBMITTED {
			run := *last
			run.Status, run.Reason, run.EndedAt = "submitted", "", nil
			t.Runs = t.Runs[:len(t.Runs)-1]
			t.Active = &run
			if !paused {
				t.Status, t.Reason = run.Status, ""
			}
			delete(d.lateRuns, t.ID)
			d.watchScheduledRun(t, now)
			return false
		}
		return true
	})
	if err != nil {
		slog.Warn("scan uncertain scheduled execution events", "session", last.SessionID, "err", errClass(err))
	}
	return changed
}
func (d *Daemon) endScheduledRun(t *schedule.Task, now time.Time, status, reason string) {
	manual := t.Active.Manual
	t.Active.Status = status
	t.Active.Reason = reason
	t.Active.EndedAt = &now
	t.Runs = appendRun(t.Runs, *t.Active)
	t.Active = nil
	if manual {
		d.restoreManualSchedule(t, now)
		return
	}
	if t.Status == "cancelled" {
		return
	}
	if t.Status == "paused" {
		if status == "completed" {
			pausedReason := t.Reason
			d.nextSchedule(t, now)
			if t.Status == "completed" && (pausedReason == "user_paused" || pausedReason == "execution_input_paused" || pausedReason == "") {
				t.Reason = "completed_while_paused"
			} else {
				t.Reason = pausedReason
			}
			t.Status = "paused"
		}
		return
	}
	if status != "completed" {
		if status == "interrupted" {
			status = "paused"
		}
		t.Status = status
		t.Reason = reason
		t.NextRunAt = nil
		return
	}
	d.nextSchedule(t, now)
}

// A calendar trial never changes automatic scheduling, even on failure.
func (d *Daemon) restoreManualSchedule(t *schedule.Task, now time.Time) {
	status, reason := t.Status, t.Reason
	t.NextRunAt = t.AutomaticNextRunAt
	if status != "paused" && status != "cancelled" {
		status, reason = t.AutomaticStatus, t.AutomaticReason
	}
	t.Status, t.Reason = status, reason
	t.AutomaticNextRunAt = nil
	t.AutomaticStatus, t.AutomaticReason = "", ""
	d.expireSchedule(t, now)
}
func (d *Daemon) nextSchedule(t *schedule.Task, now time.Time) {
	defer d.expireSchedule(t, now)
	tr := t.Spec.Trigger
	t.Reason = ""
	if tr.MaxRuns > 0 && t.Dispatches >= tr.MaxRuns {
		t.Status = "completed"
		t.NextRunAt = nil
		t.Reason = "run_limit_reached"
		return
	}
	switch {
	case tr.Type == "interval":
		interval := time.Duration(tr.IntervalSeconds) * time.Second
		base := now
		if len(t.Runs) > 0 {
			base = t.Runs[len(t.Runs)-1].ScheduledAt
		}
		next := base.Add(interval)
		if !next.After(now) {
			next = base.Add((now.Sub(base)/interval + 1) * interval)
		}
		t.NextRunAt = &next
		t.Status = "scheduled"
	case tr.Type == "calendar":
		next := nextCalendar(t, now)
		t.NextRunAt, t.Status = &next, "scheduled"
	case tr.Type == "after_turn" && tr.Repeat:
		next := now.Add(time.Duration(tr.DelaySeconds) * time.Second)
		t.NextRunAt = &next
		t.Status = "scheduled"
	default:
		t.Status = "completed"
		t.NextRunAt = nil
	}
}

// expireSchedule also bounds a future cursor. Active executions finish normally.
func (d *Daemon) expireSchedule(t *schedule.Task, now time.Time) bool {
	until := t.Spec.Trigger.Until
	if until == nil || t.Active != nil || t.Terminal() {
		return false
	}
	if now.After(*until) || t.NextRunAt != nil && t.NextRunAt.After(*until) {
		t.Status, t.Reason, t.NextRunAt = "completed", "until_reached", nil
		return true
	}
	return false
}

func calendarDate(tr schedule.Trigger, at time.Time) string {
	loc, err := time.LoadLocation(tr.Timezone)
	if err != nil {
		return ""
	}
	return at.In(loc).Format("2006-01-02")
}

// Only dates durably reserved by actual automatic dispatches are occupied;
// skipped occurrences and their bookkeeping times are irrelevant.
func nextCalendar(t *schedule.Task, now time.Time) time.Time {
	next := t.Spec.Trigger.NextCalendar(now)
	for !next.IsZero() && calendarDate(t.Spec.Trigger, next) <= t.LastCalendarDate {
		next = t.Spec.Trigger.NextCalendar(next)
	}
	return next
}

// Cursors are durable recovery bookkeeping, not client-visible changes.
func visibleTask(t *schedule.Task) []byte {
	t = schedule.Clone(t)
	t.Revision = 0
	t.UpdatedAt = time.Time{}
	t.AnchorSeq = 0
	if t.Active != nil {
		t.Active.AfterSeq = 0
	}
	for i := range t.Runs {
		t.Runs[i].AfterSeq = 0
		t.Runs[i].FollowupsClosed = false
	}
	b, _ := json.Marshal(t)
	return b
}
func visibleSchedules(tasks map[string]*schedule.Task) []byte {
	out := map[string]json.RawMessage{}
	for id, t := range tasks {
		out[id] = visibleTask(t)
	}
	b, _ := json.Marshal(out)
	return b
}

func finalRunSummary(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		n := 0
		for n < len(line) && line[n] == '#' {
			n++
		}
		if n > 0 && n <= 6 && n < len(line) && unicode.IsSpace(rune(line[n])) {
			line = strings.TrimSpace(line[n:])
		}
		lines[i] = line
	}
	text = strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	runes := []rune(text)
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return string(runes)
}

func (d *Daemon) watchScheduledFollowups(t *schedule.Task) bool {
	if len(t.Runs) == 0 {
		return false
	}
	run := &t.Runs[len(t.Runs)-1]
	if run.Status != "completed" || run.FollowupsClosed || run.SessionID == "" {
		return false
	}
	s, err := d.exact(run.SessionID)
	if err != nil {
		return false
	}
	changed := false
	_ = scanEventsSince(s.log, run.AfterSeq, func(e *pb.Event) bool {
		run.AfterSeq = e.Seq
		changed = true
		human := humanOrigin(e.Origin) && (e.GetUserMessage() != nil || e.GetSessionState() != nil && e.GetSessionState().Detail == "resumed")
		if e.GetSessionState() != nil && e.GetSessionState().Status == pb.SessionStatus_SESSION_STATUS_EXITED {
			run.FollowupsClosed = true
			return false
		}
		if human || e.GetUserMessage() != nil && e.Origin.GetScheduledRunId() != "" && e.Origin.GetScheduledRunId() != run.ID {
			run.FollowupsClosed = true
			return false
		}
		if n := e.GetScheduledNotification(); n != nil && n.RunId == run.ID {
			run.Notified, run.NotificationText = true, n.Text
			run.NotificationCount++
		}
		if m := e.GetAgentMessage(); m != nil && m.Phase == "final_answer" {
			run.FinalSummary = finalRunSummary(m.Text)
		}
		return true
	})
	return changed
}
