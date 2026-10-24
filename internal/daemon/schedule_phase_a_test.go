package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func startScheduledTurn(t *testing.T, h *harness) *schedule.Task {
	t.Helper()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	h.d.tickSchedules(time.Now().Add(time.Second))
	task = readTask(t, h.d, task.ID)
	if task.Active == nil {
		t.Fatal("no active run")
	}
	err := h.s.emit(&pb.Event{TurnId: "timed", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}, &pb.Event{TurnId: "timed", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "Check deployment", RequestId: task.Active.RequestID}}})
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func notifyTest(t *testing.T, h *harness, text, title string) error {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"text": text, "title": title})
	_, err := h.d.scheduleCommandScoped(&pb.ScheduleRequest{Operation: "notify_user", SessionId: h.s.meta.ID, SpecJson: b}, originLocal, h.s.meta.ID)
	return err
}
func endTimed(t *testing.T, h *harness, outcome pb.TurnOutcome) {
	t.Helper()
	if err := h.s.emit(&pb.Event{TurnId: "timed", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: outcome}}}); err != nil {
		t.Fatal(err)
	}
}
func restartSession(t *testing.T, h *harness) {
	t.Helper()
	m := h.s.meta
	if err := h.s.log.Close(); err != nil {
		t.Fatal(err)
	}
	h.open(m)
	h.d.sessions[m.ID] = h.s
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
}

func TestScheduledNotifyAndRestart(t *testing.T) {
	h := scheduleHarness(t)
	defer func() { h.s.log.Close() }()
	if notifyTest(t, h, "hello", "") == nil {
		t.Fatal("ordinary idle session notified")
	}
	task := startScheduledTurn(t, h)
	if got := h.events()[len(h.events())-1].Origin; got.GetScheduledTaskId() != task.ID || got.GetScheduledRunId() != task.Active.ID {
		t.Fatalf("unmarked input: %v", got)
	}
	for _, tc := range []struct{ text, title string }{{"", ""}, {strings.Repeat("字", 201), ""}, {"body", strings.Repeat("字", 61)}} {
		if notifyTest(t, h, tc.text, tc.title) == nil {
			t.Fatal("unbounded notice")
		}
	}
	if err := notifyTest(t, h, "结果一", ""); err != nil {
		t.Fatal(err)
	}
	restartSession(t, h)
	for i := 0; i < 2; i++ {
		if err := notifyTest(t, h, "结果二", "自定标题"); err != nil {
			t.Fatal(err)
		}
	}
	if err := notifyTest(t, h, "fourth", ""); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("fourth notice: %v", err)
	}
	// Neither the task's scan cursor nor a restart should lose the last final answer.
	if err := h.s.emit(&pb.Event{TurnId: "timed", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "最后的结果", Phase: "final_answer"}}}); err != nil {
		t.Fatal(err)
	}
	endTimed(t, h, pb.TurnOutcome_TURN_OUTCOME_COMPLETED)
	e := h.events()[len(h.events())-1].GetTurnEnded()
	if e.ScheduledTaskId != task.ID || e.ScheduledRunId != task.Active.ID || !e.ScheduledNotified {
		t.Fatalf("end annotation: %v", e)
	}
	if notifyTest(t, h, "after end", "") == nil {
		t.Fatal("notified outside the scheduled turn")
	}
	restartSession(t, h)
	h.d.tickSchedules(time.Now())
	got := readTask(t, h.d, task.ID)
	if len(got.Runs) != 1 || !got.Runs[0].Notified || got.Runs[0].NotificationCount != 3 || got.Runs[0].NotificationText != "结果二" || got.Runs[0].FinalSummary != "最后的结果" {
		t.Fatalf("run summary: %+v", got)
	}
	notices := 0
	for _, e := range h.events() {
		if n := e.GetScheduledNotification(); n != nil {
			notices++
			if notices == 1 && n.Title != "Check deployment" {
				t.Fatalf("default title: %v", n)
			}
		}
	}
	if notices != 3 {
		t.Fatal(notices)
	}
	// Ordinary following turns retain their normal end semantics.
	h.s.emit(&pb.Event{TurnId: "ordinary", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}, withOrigin(&pb.Event{TurnId: "ordinary", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "hello"}}}, originLocal))
	endTimed(t, h, pb.TurnOutcome_TURN_OUTCOME_COMPLETED)
	if h.events()[len(h.events())-1].GetTurnEnded().ScheduledTaskId != "" {
		t.Fatal("schedule leaked to following turn")
	}
}

func TestScheduledOwnershipClosureAndTakeover(t *testing.T) {
	for _, tc := range []struct {
		name          string
		end, takeover bool
		outcome       pb.TurnOutcome
	}{
		{"completed", true, false, pb.TurnOutcome_TURN_OUTCOME_COMPLETED},
		{"interrupted", true, false, pb.TurnOutcome_TURN_OUTCOME_ABORTED},
		{"approval still running", false, false, 0},
		{"local takeover", true, true, pb.TurnOutcome_TURN_OUTCOME_COMPLETED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := scheduleHarness(t)
			defer func() { h.s.log.Close() }()
			task := startScheduledTurn(t, h)
			h.s.mu.Lock()
			h.s.meta.ScheduledTaskID = task.ID
			h.s.meta.TaskOwned = true
			h.s.mu.Unlock()
			if len(projectSummaries([]*session{h.s})) != 0 {
				t.Fatal("task included in project summary")
			}
			// Queue delivery has been confirmed by the synthetic CLI message.
			h.s.mu.Lock()
			h.s.queue.Queue = nil
			if err := h.s.queue.save(h.s.dir); err != nil {
				t.Fatal(err)
			}
			h.s.mu.Unlock()
			if tc.takeover {
				h.s.emit(withOrigin(&pb.Event{TurnId: "timed", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "let me continue"}}}, originLocal))
			}
			if tc.end {
				endTimed(t, h, tc.outcome)
			} else {
				h.s.emit(&pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "waiting"}}})
			}
			restartSession(t, h)
			if got := h.s.info(); got.ScheduledTaskId != task.ID || got.TaskOwned == tc.takeover {
				t.Fatalf("metadata: %v", got)
			}
			if err := h.s.killIf(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if got := !h.term.Alive(); got != (tc.end && !tc.takeover) {
				t.Fatalf("closed=%v", got)
			}
			if tc.end && !tc.takeover {
				// The user explicitly resumes before their reply is recorded.
				h.s.mu.Lock()
				h.s.meta.Exited = false
				h.s.status = pb.SessionStatus_SESSION_STATUS_IDLE
				h.s.mu.Unlock()
				h.term.mu.Lock()
				h.term.alive = true
				h.term.mu.Unlock()
				restartSession(t, h)
				if err := h.s.killIf(context.Background(), true); err != nil {
					t.Fatal(err)
				}
				if !h.term.Alive() {
					t.Fatal("original run closed the user's resumed CLI")
				}
			}
			if tc.takeover && len(projectSummaries([]*session{h.s})) != 1 {
				t.Fatal("takeover did not restore project")
			}
		})
	}
}

func TestScheduledFailedDeliveryClosesCLI(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	h.s.meta.TaskOwned = true
	h.s.meta.ScheduledTaskID, h.s.meta.ScheduledRunID = task.ID, "failed"
	h.d.tasks[task.ID].Spec.Target.Type = "new_session"
	h.d.tasks[task.ID].Runs = []schedule.Run{{ID: "failed", SessionID: h.s.meta.ID, Status: "failed"}}
	candidates := h.d.scheduledClosures()
	if len(candidates) != 1 {
		t.Fatal("failed run leaked CLI")
	}
	if err := candidates[0].killIf(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if h.term.Alive() {
		t.Fatal("CLI not closed")
	}
}

func TestMCPScheduledRunCannotExtendDelegation(t *testing.T) {
	h := scopedHarness(t, "ask")
	spec := taskSpec(schedule.Trigger{Type: "after_turn", DelaySeconds: 60})
	raw, err := scopedSchedule(t, h, "create", "", &spec)
	if err != nil {
		t.Fatal(err)
	}
	owned := decodeScopedTask(t, raw)
	startScheduledTurn(t, h)
	for _, op := range []string{"create", "update", "resume", "run"} {
		if _, err := scopedSchedule(t, h, op, owned.ID, &spec); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("%s: %v", op, err)
		}
	}
	for _, op := range []string{"list", "get", "pause", "cancel"} {
		if _, err := scopedSchedule(t, h, op, owned.ID, nil); err != nil {
			t.Fatalf("%s: %v", op, err)
		}
	}
}

func TestMCPRecurrenceFloors(t *testing.T) {
	for _, tr := range []schedule.Trigger{{Type: "interval", IntervalSeconds: 299}, {Type: "after_turn", Repeat: true, DelaySeconds: 59}} {
		h := scopedHarness(t, "ask")
		spec := taskSpec(tr)
		if _, err := scopedSchedule(t, h, "create", "", &spec); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("MCP recurrence: %v", err)
		}
		createTask(t, h, spec, "") // explicit user commands retain short recurrences
		if tr.Type == "interval" {
			spec.Trigger.IntervalSeconds = 300
		} else {
			spec.Trigger.DelaySeconds = 60
		}
		if _, err := scopedSchedule(t, h, "create", "", &spec); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScheduleDiscoveryRevisionCoalesces(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	if !h.d.capabilities().ScheduledTasks {
		t.Fatal("missing capability")
	}
	createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	before := h.d.publishedScheduleRevision()
	createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	if h.d.publishedScheduleRevision() != before {
		t.Fatal("changes not coalesced")
	}
	h.d.schedulePublishAt = time.Time{}
	after := h.d.publishedScheduleRevision()
	if after <= before {
		t.Fatal("revision not published")
	}
	restartSession(t, h)
	h.d.schedulePublishAt = time.Time{}
	if h.d.publishedScheduleRevision() < after {
		t.Fatal("revision regressed on restart")
	}
}
