package daemon

import (
	"bytes"
	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScheduledBackgroundFollowupKeepsContext(t *testing.T) {
	h := scopedHarness(t, "ask")
	spec := taskSpec(schedule.Trigger{Type: "after_turn", DelaySeconds: 60})
	raw, err := scopedSchedule(t, h, "create", "", &spec)
	if err != nil {
		t.Fatal(err)
	}
	own := decodeScopedTask(t, raw)
	task := startScheduledTurn(t, h)
	endTimed(t, h, pb.TurnOutcome_TURN_OUTCOME_COMPLETED)
	h.d.tickSchedules(time.Now())
	restartSession(t, h)
	setScopedMode(h, "ask")
	// Claude background notification creates a turn without a user message.
	if err := h.s.emit(&pb.Event{TurnId: "followup", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"create", "update", "resume", "run"} {
		if _, err := scopedSchedule(t, h, op, own.ID, &spec); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("followup %s escaped: %v", op, err)
		}
	}
	if err := h.s.emit(&pb.Event{TurnId: "followup", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}}); err != nil {
		t.Fatal(err)
	}
	if end := h.events()[len(h.events())-1].GetTurnEnded(); end.ScheduledTaskId != task.ID || end.ScheduledRunId != task.Active.ID {
		t.Fatalf("lost followup identity: %v", end)
	}
}
func TestHumanInterjectionMakesScheduledTurnOrdinary(t *testing.T) {
	for _, kind := range []pb.OriginKind{pb.OriginKind_ORIGIN_KIND_CLIENT, pb.OriginKind_ORIGIN_KIND_LOCAL} {
		h := scheduleHarness(t)
		startScheduledTurn(t, h)
		if err := h.s.emit(withOrigin(&pb.Event{TurnId: "timed", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "interject", RequestId: "human"}}}, &pb.Origin{Kind: kind})); err != nil {
			t.Fatal(err)
		}
		endTimed(t, h, pb.TurnOutcome_TURN_OUTCOME_COMPLETED)
		if end := h.events()[len(h.events())-1].GetTurnEnded(); end.ScheduledTaskId != "" || end.ScheduledRunId != "" || end.ScheduledNotified {
			t.Fatalf("human interjection remained scheduled: %v", end)
		}
		restartSession(t, h)
		if h.s.scheduledTask != "" {
			t.Fatal("context returned after restart")
		}
		h.s.log.Close()
	}
}

func TestTakeoverNeedsVerifiedHumanOrExplicitResume(t *testing.T) {
	for _, kind := range []pb.OriginKind{pb.OriginKind_ORIGIN_KIND_UNSPECIFIED, pb.OriginKind_ORIGIN_KIND_SYSTEM, pb.OriginKind_ORIGIN_KIND_LOCAL, pb.OriginKind_ORIGIN_KIND_CLIENT} {
		t.Run(kind.String(), func(t *testing.T) {
			h := scheduleHarness(t)
			defer h.s.log.Close()
			task := startScheduledTurn(t, h)
			h.s.meta.TaskOwned = true
			h.s.meta.ScheduledTaskID = task.ID
			h.s.emit(withOrigin(&pb.Event{TurnId: "timed", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "ambiguous input"}}}, &pb.Origin{Kind: kind}))
			human := kind == pb.OriginKind_ORIGIN_KIND_LOCAL || kind == pb.OriginKind_ORIGIN_KIND_CLIENT
			if h.s.info().TaskOwned == human {
				t.Fatal("wrong takeover for origin")
			}
		})
	}
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := startScheduledTurn(t, h)
	h.s.meta.TaskOwned = true
	h.s.meta.ScheduledTaskID = task.ID
	// This is the durable event emitted only by a human-origin ResumeSession.
	h.s.emit(withOrigin(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Status: pb.SessionStatus_SESSION_STATUS_STARTING, Detail: "resumed"}}}, originLocal))
	if h.s.info().TaskOwned || h.s.scheduledTask != "" {
		t.Fatal("explicit resume did not take over")
	}
	restartSession(t, h)
	if h.s.info().TaskOwned || h.s.scheduledTask != "" {
		t.Fatal("resume takeover lost on replay")
	}
}

func TestMCPRunAndResumeRecheckMinimumIntervals(t *testing.T) {
	for _, typ := range []string{"interval", "after_turn"} {
		for _, op := range []string{"run", "resume"} {
			t.Run(typ+op, func(t *testing.T) {
				h := scopedHarness(t, "ask")
				tr := schedule.Trigger{Type: typ, IntervalSeconds: 300}
				if typ == "after_turn" {
					tr.IntervalSeconds = 0
					tr.Repeat = true
					tr.DelaySeconds = 60
				}
				spec := taskSpec(tr)
				raw, err := scopedSchedule(t, h, "create", "", &spec)
				if err != nil {
					t.Fatal(err)
				}
				task := decodeScopedTask(t, raw)
				old := h.d.tasks[task.ID]
				old.Spec.Trigger.IntervalSeconds = 1
				if typ == "after_turn" {
					old.Spec.Trigger.IntervalSeconds = 0
					old.Spec.Trigger.DelaySeconds = 1
				}
				old.Status = "paused"
				if _, err := scopedSchedule(t, h, op, task.ID, nil); connect.CodeOf(err) != connect.CodePermissionDenied {
					t.Fatalf("%s allowed short interval: %v", op, err)
				}
			})
		}
	}
}

func TestHumanInterjectionEndsMCPRestrictionAndFollowupNoticesRemainRecorded(t *testing.T) {
	h := scopedHarness(t, "ask")
	task := startScheduledTurn(t, h)
	endTimed(t, h, pb.TurnOutcome_TURN_OUTCOME_COMPLETED)
	h.d.tickSchedules(time.Now())
	h.s.emit(&pb.Event{TurnId: "follow", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
	if err := notifyTest(t, h, "followup result", ""); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(time.Now())
	if got := readTask(t, h.d, task.ID).Runs[0]; !got.Notified || got.NotificationText != "followup result" {
		t.Fatalf("followup notice lost: %+v", got)
	}
	h.s.emit(withOrigin(&pb.Event{TurnId: "follow", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "my request"}}}, originLocal))
	spec := taskSpec(schedule.Trigger{Type: "after_turn", DelaySeconds: 60})
	if _, err := scopedSchedule(t, h, "create", "", &spec); err != nil {
		t.Fatalf("human turn still restricted: %v", err)
	}
}

func TestOrdinaryHumanReplayDoesNotWriteMetadata(t *testing.T) {
	h := scheduleHarness(t)
	defer func() { h.s.log.Close() }()
	for i := 0; i < 300; i++ {
		if err := h.s.emit(withOrigin(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{Status: pb.InputStatus_INPUT_STATUS_QUEUED}}}, originLocal)); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.meta.save(h.s.dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.s.dir, "session.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before = append(before, '\n', ' ')
	if err = os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	restartSession(t, h)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("ordinary replay rewrote session.json")
	}
	h.s.scheduledTask = "stale"
	h.s.takeOverLocked()
	if h.s.scheduledTask != "" {
		t.Fatal("ordinary context was not cleared")
	}
}

func TestMCPRestrictionUsesCurrentTurnNotQueue(t *testing.T) {
	for _, op := range []string{"create", "update", "run"} {
		for _, scheduled := range []bool{false, true} {
			t.Run(op+map[bool]string{false: "human", true: "scheduled"}[scheduled], func(t *testing.T) {
				h := scopedHarness(t, "ask")
				spec := taskSpec(schedule.Trigger{Type: "after_turn", DelaySeconds: 60})
				raw, err := scopedSchedule(t, h, "create", "", &spec)
				if err != nil {
					t.Fatal(err)
				}
				own := decodeScopedTask(t, raw)
				if scheduled {
					startScheduledTurn(t, h)
					if _, err = h.s.enqueueMode("later human", "later-human", nil, originLocal, pb.SendMode_SEND_MODE_QUEUE); err != nil {
						t.Fatal(err)
					}
				} else {
					queued := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
					h.s.emit(withOrigin(&pb.Event{TurnId: "human", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "current human"}}}, originLocal))
					h.s.emit(&pb.Event{TurnId: "human", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
					h.d.tickSchedules(time.Now().Add(time.Second))
					if readTask(t, h.d, queued.ID).Active == nil {
						t.Fatal("no scheduled queue")
					}
				}
				_, err = scopedSchedule(t, h, op, own.ID, &spec)
				if scheduled && connect.CodeOf(err) != connect.CodePermissionDenied {
					t.Fatalf("queued human released restriction: %v", err)
				}
				if !scheduled && err != nil {
					t.Fatalf("queued resume restricted current human: %v", err)
				}
			})
		}
	}
}

func TestMCPDispatchReportsMinimumIntervalReason(t *testing.T) {
	for _, typ := range []string{"interval", "after_turn"} {
		h := scopedHarness(t, "ask")
		tr := schedule.Trigger{Type: typ, IntervalSeconds: 300}
		if typ == "after_turn" {
			tr.IntervalSeconds = 0
			tr.Repeat = true
			tr.DelaySeconds = 60
		}
		spec := taskSpec(tr)
		raw, err := scopedSchedule(t, h, "create", "", &spec)
		if err != nil {
			t.Fatal(err)
		}
		task := decodeScopedTask(t, raw)
		state := h.d.tasks[task.ID]
		if typ == "interval" {
			state.Spec.Trigger.IntervalSeconds = 1
		} else {
			state.Spec.Trigger.DelaySeconds = 1
		}
		h.d.tickSchedules(task.NextRunAt.Add(time.Second))
		got := readTask(t, h.d, task.ID)
		if got.Status != "paused" || got.Reason != "agent_schedule_interval_below_minimum" || got.Dispatches != 0 {
			t.Fatalf("misleading minimum reason: %+v", got)
		}
	}
}

func TestFollowupSurvivesLaterSkippedRun(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		h := scheduleHarness(t)
		defer func() { h.s.log.Close() }()
		now := time.Now()
		spec := taskSpec(schedule.Trigger{Type: "calendar", Time: "08:00", Timezone: "UTC"})
		spec.LateSeconds = 60
		task := createTask(t, h, spec, "")
		h.d.tasks[task.ID].NextRunAt = &now
		h.d.tickSchedules(now)
		run := readTask(t, h.d, task.ID).Active
		completeTask(h, run, now)
		h.d.tickSchedules(now)
		next := *readTask(t, h.d, task.ID).NextRunAt
		h.d.tickSchedules(next.Add(2 * time.Minute))
		if got := readTask(t, h.d, task.ID); len(got.Runs) != 2 || got.Runs[1].Status != "skipped" {
			t.Fatalf("no real skip: %+v", got)
		}
		if terminal {
			h.d.tasks[task.ID].Status = "completed"
		}
		if err := h.d.saveSchedulesLocked(); err != nil {
			t.Fatal(err)
		}
		if err := h.d.loadSchedules(); err != nil {
			t.Fatal(err)
		}
		h.s.emit(&pb.Event{TurnId: "follow", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
		if err := notifyTest(t, h, "late result", ""); err != nil {
			t.Fatal(err)
		}
		h.s.emit(&pb.Event{TurnId: "follow", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "late final summary"}}})
		h.d.tickSchedules(next.Add(3 * time.Minute))
		got := readTask(t, h.d, task.ID)
		if got.Runs[0].ID != run.ID || got.Runs[0].NotificationCount != 1 || got.Runs[0].NotificationText != "late result" || got.Runs[0].FinalSummary != "late final summary" {
			t.Fatalf("earlier followup lost behind skipped run: %+v", got.Runs[0])
		}
		if got.Runs[1].Notified || got.Runs[1].FinalSummary != "" {
			t.Fatal("followup attributed to skipped run")
		}
		h.d.tickSchedules(next.Add(4 * time.Minute))
		if readTask(t, h.d, task.ID).Runs[0].NotificationCount != 1 {
			t.Fatal("notification replay double counted")
		}
	}
}
