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
