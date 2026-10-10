package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func scheduleHarness(t *testing.T) *harness {
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	return h
}
func taskSpec(trigger schedule.Trigger) schedule.Spec {
	return schedule.Spec{Title: "Check", Message: "Check deployment", Trigger: trigger, Target: schedule.Target{Type: "resume_session", Session: "current"}}
}
func createTask(t *testing.T, h *harness, spec schedule.Spec, key string) *schedule.Task {
	t.Helper()
	b, _ := json.Marshal(spec)
	res, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: b, RequestId: key}, originLocal)
	if err != nil {
		t.Fatal(err)
	}
	var task schedule.Task
	if err = json.Unmarshal(res, &task); err != nil {
		t.Fatal(err)
	}
	return &task
}
func readTask(t *testing.T, d *Daemon, id string) *schedule.Task {
	t.Helper()
	b, err := d.scheduleCommand(&pb.ScheduleRequest{Operation: "get", TaskId: id}, originLocal)
	if err != nil {
		t.Fatal(err)
	}
	var out schedule.Task
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}
func completeTask(h *harness, run *schedule.Run, at time.Time) {
	h.s.emit(&pb.Event{TurnId: "scheduled-turn", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}},
		&pb.Event{TurnId: "scheduled-turn", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "Check deployment", RequestId: run.RequestID}}},
		&pb.Event{Time: timestamppb.New(at), TurnId: "scheduled-turn", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}})
}

func TestScheduleAfterTurnBindsAndWaits(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	h.s.emit(&pb.Event{TurnId: "first", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn", DelaySeconds: 30}), "")
	if task.Status != "waiting_turn" || task.NextRunAt != nil {
		t.Fatalf("premature timer: %+v", task)
	}
	now := time.Now().UTC()
	h.s.emit(&pb.Event{Time: timestamppb.New(now), TurnId: "other", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}})
	h.d.tickSchedules(now)
	if readTask(t, h.d, task.ID).Status != "waiting_turn" {
		t.Fatal("bound to wrong turn")
	}
	h.s.emit(&pb.Event{Time: timestamppb.New(now), TurnId: "first", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}})
	h.d.tickSchedules(now)
	task = readTask(t, h.d, task.ID)
	if task.NextRunAt == nil || !task.NextRunAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("timer %+v", task)
	}
	h.d.tickSchedules(now.Add(29 * time.Second))
	if len(h.s.queue.Queue) != 0 {
		t.Fatal("sent early")
	}
	h.d.tickSchedules(now.Add(30 * time.Second))
	task = readTask(t, h.d, task.ID)
	if task.Active == nil || task.Active.Status != "queued" || len(h.s.queue.Queue) != 1 || h.s.queue.Queue[0].Mode != pb.SendMode_SEND_MODE_QUEUE {
		t.Fatalf("not queued: %+v", task)
	}
	h.d.tickSchedules(now.Add(time.Hour))
	if len(h.s.queue.Queue) != 1 || readTask(t, h.d, task.ID).Dispatches != 1 {
		t.Fatal("duplicate dispatch")
	}
}

func TestScheduleRestartKeepsQueuedExecution(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "stable-key")
	h.d.tickSchedules(time.Now().Add(time.Second))
	before := readTask(t, h.d, task.ID)
	if before.Active == nil {
		t.Fatal("not dispatched")
	}
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(time.Now().Add(time.Minute))
	after := readTask(t, h.d, task.ID)
	if after.Active.RequestID != before.Active.RequestID || after.Dispatches != 1 || len(h.s.queue.Queue) != 1 {
		t.Fatal("restart duplicated queued message")
	}
}

func TestScheduleDispatchCrashNeverRepeats(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	h.d.scheduleMu.Lock()
	stored := h.d.tasks[task.ID]
	stored.Status = "dispatching"
	stored.Dispatches = 1
	stored.Active = &schedule.Run{ID: "run", RequestID: "id", Status: "dispatching"}
	err := h.d.saveSchedulesLocked()
	h.d.scheduleMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(time.Now().Add(time.Hour))
	after := readTask(t, h.d, task.ID)
	if after.Status != "uncertain" || after.Active != nil || len(after.Runs) != 1 || len(h.s.queue.Queue) != 0 {
		t.Fatalf("unsafe recovery: %+v", after)
	}
}

func TestScheduleInterruptedAnchorPauses(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	h.s.emit(&pb.Event{TurnId: "first", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	h.s.emit(&pb.Event{TurnId: "first", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_ABORTED}}})
	h.d.tickSchedules(time.Now())
	if readTask(t, h.d, task.ID).Status != "paused" || len(h.s.queue.Queue) != 0 {
		t.Fatal("continued interrupted turn")
	}
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "resume", TaskId: task.ID}, originLocal); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(time.Now().Add(time.Second))
	if readTask(t, h.d, task.ID).Active == nil {
		t.Fatal("explicit resume did not run")
	}
}

func TestScheduleCreationIdempotencyAndConcurrentEdit(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	spec := taskSpec(schedule.Trigger{Type: "after_turn", DelaySeconds: 100})
	a := createTask(t, h, spec, "retry")
	b := createTask(t, h, spec, "retry")
	if a.ID != b.ID {
		t.Fatal("duplicated create")
	}
	spec.Message = "different"
	raw, _ := json.Marshal(spec)
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: raw, RequestId: "retry"}, originLocal); err == nil {
		t.Fatal("key collision accepted")
	}
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "pause", TaskId: a.ID, ExpectedRevision: a.Revision}, originLocal); err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "cancel", TaskId: a.ID, ExpectedRevision: a.Revision}, originLocal); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("missing revision check: %v", err)
	}
}

func TestScheduleRepeatedAfterTurnLimit(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn", DelaySeconds: 5, Repeat: true, MaxRuns: 2}), "")
	at := task.NextRunAt.Add(time.Second)
	h.d.tickSchedules(at)
	task = readTask(t, h.d, task.ID)
	completeTask(h, task.Active, at)
	h.d.tickSchedules(at)
	task = readTask(t, h.d, task.ID)
	if task.Status != "scheduled" || task.NextRunAt == nil || !task.NextRunAt.Equal(at.Add(5*time.Second)) {
		t.Fatalf("wrong recurrence: %+v", task)
	}
	h.d.tickSchedules(at.Add(5 * time.Second))
	task = readTask(t, h.d, task.ID)
	completeTask(h, task.Active, at.Add(5*time.Second))
	h.d.tickSchedules(at.Add(5 * time.Second))
	task = readTask(t, h.d, task.ID)
	if task.Status != "completed" || task.Dispatches != 2 || len(task.Runs) != 2 {
		t.Fatalf("limit %+v", task)
	}
}

func TestScheduleRunCompletionDoesNotMatchEarlierTurn(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	now := time.Now().Add(time.Second)
	h.d.tickSchedules(now)
	task = readTask(t, h.d, task.ID)
	h.s.emit(&pb.Event{Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}}, &pb.Event{TurnId: "ours", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{RequestId: task.Active.RequestID, Text: "Check deployment"}}})
	h.d.tickSchedules(now)
	h.d.tickSchedules(now.Add(time.Second))
	task = readTask(t, h.d, task.ID)
	if task.Active == nil || task.Active.Status != "running" {
		t.Fatalf("matched prior end: %+v", task)
	}
	h.s.emit(&pb.Event{TurnId: "ours", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}})
	h.d.tickSchedules(now.Add(2 * time.Second))
	if readTask(t, h.d, task.ID).Status != "completed" {
		t.Fatal("did not finish")
	}
}

func TestScheduleLateCatchUpAndNoBacklog(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	old := time.Now().Add(-time.Hour)
	spec := taskSpec(schedule.Trigger{Type: "interval", At: &old, IntervalSeconds: 60})
	spec.LateSeconds = 10
	task := createTask(t, h, spec, "")
	now := time.Now()
	h.d.tickSchedules(now)
	task = readTask(t, h.d, task.ID)
	if task.Status != "scheduled" || !task.NextRunAt.After(now) || task.Dispatches != 0 || len(task.Runs) != 1 || task.Runs[0].Status != "skipped" {
		t.Fatalf("catch-up %+v", task)
	}
	h.d.tickSchedules(*task.NextRunAt)
	if len(h.s.queue.Queue) != 1 {
		t.Fatal("next interval did not dispatch once")
	}
}

func TestScheduleCancelAndPauseDoNotDispatch(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	for _, op := range []string{"pause", "cancel"} {
		task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
		if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: op, TaskId: task.ID}, originLocal); err != nil {
			t.Fatal(err)
		}
		h.d.tickSchedules(time.Now().Add(time.Minute))
		if readTask(t, h.d, task.ID).Active != nil || len(h.s.queue.Queue) != 0 {
			t.Fatal("dispatched after " + op)
		}
	}
}

func TestScheduleUnconfirmedDoesNotRetry(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn", Repeat: true}), "")
	now := time.Now().Add(time.Second)
	h.d.tickSchedules(now)
	task = readTask(t, h.d, task.ID)
	h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: task.Active.RequestID, Status: pb.InputStatus_INPUT_STATUS_UNCONFIRMED}}})
	h.d.tickSchedules(now)
	h.d.tickSchedules(now.Add(time.Hour))
	if got := readTask(t, h.d, task.ID); got.Status != "uncertain" || got.Dispatches != 1 || got.Active != nil {
		t.Fatalf("retried uncertain %+v", got)
	}
}

// The CLI's record of the message, coming after it was reported
// unconfirmed, takes the execution up again: it is followed to its end and
// a repeating task goes on, without the message being sent twice.
func TestScheduleLateConfirmationResumesRun(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn", Repeat: true}), "")
	now := time.Now().Add(time.Second)
	h.d.tickSchedules(now)
	run := readTask(t, h.d, task.ID).Active
	h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_UNCONFIRMED}}})
	h.d.tickSchedules(now)
	if got := readTask(t, h.d, task.ID); got.Status != "uncertain" || got.Active != nil {
		t.Fatalf("not uncertain %+v", got)
	}
	// Another message's confirmation is not this one's.
	h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: "other", Status: pb.InputStatus_INPUT_STATUS_SUBMITTED}}})
	h.d.tickSchedules(now)
	if got := readTask(t, h.d, task.ID); got.Status != "uncertain" || got.Active != nil {
		t.Fatalf("taken up by another message %+v", got)
	}
	h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_SUBMITTED}}},
		&pb.Event{TurnId: "scheduled-turn", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "Check deployment", RequestId: run.RequestID}}})
	h.d.tickSchedules(now)
	got := readTask(t, h.d, task.ID)
	if got.Status != "running" || got.Active == nil || got.Active.ID != run.ID || got.Active.EndedAt != nil || len(got.Runs) != 0 {
		t.Fatalf("not taken up again %+v", got)
	}
	h.s.emit(&pb.Event{Time: timestamppb.New(now), TurnId: "scheduled-turn", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}})
	h.d.tickSchedules(now)
	// The execution is completed, and the repeating task goes on to its
	// next one.
	got = readTask(t, h.d, task.ID)
	if len(got.Runs) != 1 || got.Runs[0].ID != run.ID || got.Runs[0].Status != "completed" || got.Status == "uncertain" || got.Reason != "" {
		t.Fatalf("not completed %+v", got)
	}
	if got.Dispatches != 2 || got.Active == nil || got.Active.ID == run.ID {
		t.Fatalf("did not go on %+v", got)
	}
}

// A task paused before or after its message was reported unconfirmed
// stays paused, and the execution is still followed to its end: resuming
// does not run a one-shot task a second time.
func TestScheduleLateConfirmationWhilePaused(t *testing.T) {
	for _, pauseFirst := range []bool{true, false} {
		h := scheduleHarness(t)
		task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
		now := time.Now().Add(time.Second)
		h.d.tickSchedules(now)
		run := readTask(t, h.d, task.ID).Active
		pause := func() {
			if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "pause", TaskId: task.ID}, originLocal); err != nil {
				t.Fatal(err)
			}
		}
		if pauseFirst {
			pause()
		}
		h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_UNCONFIRMED}}})
		h.d.tickSchedules(now)
		if !pauseFirst {
			pause()
		}
		if got := readTask(t, h.d, task.ID); got.Status != "paused" || got.Active != nil || len(got.Runs) != 1 || got.Runs[0].Status != "uncertain" {
			t.Fatalf("pauseFirst=%v: %+v", pauseFirst, got)
		}
		h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_SUBMITTED}}})
		completeTask(h, run, now)
		h.d.tickSchedules(now)
		got := readTask(t, h.d, task.ID)
		if got.Status != "paused" || got.Active != nil || len(got.Runs) != 1 || got.Runs[0].ID != run.ID || got.Runs[0].Status != "completed" {
			t.Fatalf("pauseFirst=%v: not completed while paused %+v", pauseFirst, got)
		}
		if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "resume", TaskId: task.ID}, originLocal); err != nil {
			t.Fatal(err)
		}
		h.d.tickSchedules(now.Add(time.Hour))
		if got = readTask(t, h.d, task.ID); got.Dispatches != 1 || got.Active != nil || got.Status != "completed" {
			t.Fatalf("pauseFirst=%v: ran again after resume %+v", pauseFirst, got)
		}
		h.s.log.Close()
	}
}

// unconfirmedTask is a task whose execution just ended uncertain because
// its message was reported unconfirmed.
func unconfirmedTask(t *testing.T, h *harness, now time.Time) (*schedule.Task, *schedule.Run) {
	t.Helper()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	h.d.tickSchedules(now)
	run := readTask(t, h.d, task.ID).Active
	h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_UNCONFIRMED}}})
	h.d.tickSchedules(now)
	if got := readTask(t, h.d, task.ID); got.Status != "uncertain" || got.Active != nil {
		t.Fatalf("not uncertain %+v", got)
	}
	return task, run
}

func scheduleOp(t *testing.T, h *harness, op, id string, spec *schedule.Spec) {
	t.Helper()
	q := &pb.ScheduleRequest{Operation: op, TaskId: id}
	if spec != nil {
		q.SpecJson, _ = json.Marshal(spec)
	}
	if _, err := h.d.scheduleCommand(q, originLocal); err != nil {
		t.Fatal(err)
	}
}

// Resuming right after the late record was logged, before the scheduler
// looked, takes the execution up instead of sending the message again.
func TestScheduleResumeTakesUpLateConfirmation(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	now := time.Now().Add(time.Second)
	task, run := unconfirmedTask(t, h, now)
	scheduleOp(t, h, "pause", task.ID, nil)
	h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_SUBMITTED}}})
	scheduleOp(t, h, "resume", task.ID, nil)
	got := readTask(t, h.d, task.ID)
	if got.Active == nil || got.Active.ID != run.ID || got.Dispatches != 1 || len(got.Runs) != 0 {
		t.Fatalf("moved on from a confirmed execution %+v", got)
	}
	completeTask(h, run, now)
	h.d.tickSchedules(now.Add(time.Hour))
	if got = readTask(t, h.d, task.ID); got.Status != "completed" || got.Dispatches != 1 || len(h.s.queue.Queue) != 1 {
		t.Fatalf("sent again %+v", got)
	}
}

// Replacing the task's definition, or running or resuming it, moves on
// from the uncertain execution: a late record no longer takes it up, and
// cannot end the replacement in its place.
func TestScheduleLateConfirmationAfterMovingOn(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	now := time.Now().Add(time.Second)
	task, run := unconfirmedTask(t, h, now)
	at := now.Add(24 * time.Hour)
	spec := taskSpec(schedule.Trigger{Type: "at", At: &at})
	scheduleOp(t, h, "update", task.ID, &spec)
	scheduleOp(t, h, "pause", task.ID, nil)
	h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_SUBMITTED}}})
	completeTask(h, run, now)
	h.d.tickSchedules(now)
	got := readTask(t, h.d, task.ID)
	if got.Active != nil || got.Status != "paused" || got.NextRunAt == nil || !got.NextRunAt.Equal(at) || len(got.Runs) != 1 || got.Runs[0].Status != "uncertain" {
		t.Fatalf("the old execution ended the replacement %+v", got)
	}
}

func TestSchedulePersistenceIsPrivateAndCorruptionStopsLoad(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn", DelaySeconds: 10}), "")
	path := filepath.Join(paths.Dir(), "schedules.json")
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("permissions: %v %v", st, err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.d.loadSchedules(); err == nil {
		t.Fatal("silently discarded corrupted schedules")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "broken" {
		t.Fatal("overwrote damaged state")
	}
}

func TestScheduleRejectsInvalidSpecs(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	for _, raw := range []string{`null`, `{} {}`, `{"extra":1}`, `{"title":"x","message":"y","trigger":{"type":"interval","interval_seconds":0},"target":{"type":"resume_session","session":"current"}}`} {
		if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: []byte(raw)}, originLocal); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	b, _ := json.Marshal(taskSpec(schedule.Trigger{Type: "after_turn"}))
	req := connect.NewRequest(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: b})
	_, err := (&handler{d: h.d}).Schedule(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, t := range h.d.tasks {
		if strings.Contains(t.ID, "/") {
			panic("invalid ID")
		}
	}
}

func TestSchedulePausedInputDoesNotResurrectCancelledTask(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn", Repeat: true}), "")
	now := time.Now().Add(time.Second)
	h.d.tickSchedules(now)
	task = readTask(t, h.d, task.ID)
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "cancel", TaskId: task.ID}, originLocal); err != nil {
		t.Fatal(err)
	}
	h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: task.Active.RequestID, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Paused: true}}})
	h.d.tickSchedules(now)
	if got := readTask(t, h.d, task.ID); got.Status != "cancelled" || !got.Active.InputPaused {
		t.Fatalf("cancel overwritten %+v", got)
	}
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "pause", TaskId: task.ID}, originLocal); err == nil {
		t.Fatal("cancelled task resurrected through pause")
	}
}

func TestScheduleInputPauseAndResumeAreTracked(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	now := time.Now().Add(time.Second)
	h.d.tickSchedules(now)
	task = readTask(t, h.d, task.ID)
	h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: task.Active.RequestID, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Paused: true}}})
	h.d.tickSchedules(now)
	if got := readTask(t, h.d, task.ID); got.Status != "paused" || !got.Active.InputPaused {
		t.Fatalf("not paused %+v", got)
	}
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "resume", TaskId: task.ID}, originLocal); err == nil {
		t.Fatal("reported resume while execution input remained paused")
	}
	h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: task.Active.RequestID, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Paused: false}}})
	h.d.tickSchedules(now)
	if got := readTask(t, h.d, task.ID); got.Status != "queued" || got.Active.InputPaused {
		t.Fatalf("did not track input resume %+v", got)
	}
}

// A new session inherits the source's mode only when the source's whole
// configuration is a standard mode: the ASK label also covers a read-only
// sandbox, which a new "ask" session would widen to workspace-write.
func TestScheduleNewSessionInheritsOnlyExactModes(t *testing.T) {
	h := scopedHarness(t, "custom")
	create := func(mode string) (*schedule.Task, error) {
		spec := taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600})
		spec.Target = schedule.Target{Type: "new_session", Mode: mode}
		b, _ := json.Marshal(spec)
		res, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: b}, originLocal)
		if err != nil {
			return nil, err
		}
		task := &schedule.Task{}
		return task, json.Unmarshal(res, task)
	}
	if h.s.info().Mode != pb.ApprovalMode_APPROVAL_MODE_ASK {
		t.Fatal("the read-only source should show as ASK")
	}
	if task, err := create(""); err == nil {
		t.Fatalf("read-only source inherited as %q", task.Spec.Target.Mode)
	}
	if task, err := create("ask"); err != nil || task.Spec.Target.Mode != "ask" {
		t.Fatalf("explicit mode: %v", err)
	}
	for _, mode := range []string{"ask", "full"} {
		setScopedMode(h, mode)
		if task, err := create(""); err != nil || task.Spec.Target.Mode != mode {
			t.Fatalf("%s source: %v", mode, err)
		}
	}
	for _, mode := range []string{"plan", "unknown"} {
		setScopedMode(h, mode)
		if _, err := create(""); err == nil {
			t.Fatalf("%s source inherited", mode)
		}
	}
}
