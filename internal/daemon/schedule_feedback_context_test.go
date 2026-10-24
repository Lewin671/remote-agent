package daemon

import (
	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
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
