package daemon

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"testing"
	"time"
)

func TestOrderingReplay(t *testing.T) {
	h := newHarness(t, Config{})
	at := func(ms int64) *timestamppb.Timestamp { return timestamppb.New(time.UnixMilli(ms)) }
	events := []*pb.Event{
		{Time: at(1000), Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "q"}}},
		{Time: at(2000), Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "a"}}},
		{Time: at(3000), Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "b"}}},
		{Time: at(4000), Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "background output"}}},
	}
	h.s.emit(events...)
	// Approval priority hides an older question; output does not reset wait time.
	snapshot := &pb.Session{PendingApprovals: 2, PendingQuestions: 1}
	if got := h.s.ordering.attentionSince(snapshot); got.AsTime().UnixMilli() != 2000 {
		t.Fatalf("wait = %v", got)
	}
	h.s.emit(&pb.Event{Time: at(5000), Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{ApprovalId: "a"}}})
	if got := h.s.ordering.attentionSince(snapshot); got.AsTime().UnixMilli() != 3000 {
		t.Fatalf("next wait = %v", got)
	}
	// A fresh tracker reading the log yields the same ordering after restart.
	var replay sessionOrdering
	h.s.log.Each(func(e *pb.Event) { replay.observe(e) })
	if got := replay.attentionSince(snapshot); got.AsTime().UnixMilli() != 3000 {
		t.Fatalf("replayed wait = %v", got)
	}
	replay.observe(&pb.Event{Time: at(6000), Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{ApprovalId: "b"}}})
	snapshot.PendingApprovals = 0
	if got := replay.attentionSince(snapshot); got.AsTime().UnixMilli() != 1000 {
		t.Fatalf("question wait = %v", got)
	}
	replay.observe(&pb.Event{Time: at(7000), Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{}}})
	if replay.attentionSince(snapshot) != nil {
		t.Fatal("finished questions retained")
	}
}

func TestOrderingScreenAndExit(t *testing.T) {
	h := newHarness(t, Config{})
	at := func(ms int64) *timestamppb.Timestamp { return timestamppb.New(time.UnixMilli(ms)) }
	state := func(ms int64, status pb.SessionStatus, plan bool, detail string) {
		h.s.emit(&pb.Event{Time: at(ms), Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Status: status, PlanOpen: plan, Detail: detail}}})
	}
	state(1000, pb.SessionStatus_SESSION_STATUS_IDLE, true, "")
	state(2000, pb.SessionStatus_SESSION_STATUS_IDLE, true, "")
	if got := h.s.info().AttentionSince; got.AsTime().UnixMilli() != 1000 {
		t.Fatalf("plan wait = %v", got)
	}
	state(3000, pb.SessionStatus_SESSION_STATUS_BLOCKED, false, "trust")
	state(4000, pb.SessionStatus_SESSION_STATUS_BLOCKED, false, "trust")
	if got := h.s.info().AttentionSince; got.AsTime().UnixMilli() != 3000 {
		t.Fatalf("popup wait = %v", got)
	}
	state(5000, pb.SessionStatus_SESSION_STATUS_BLOCKED, false, "different popup")
	if got := h.s.info().AttentionSince; got.AsTime().UnixMilli() != 5000 {
		t.Fatalf("new popup wait = %v", got)
	}
	state(6000, pb.SessionStatus_SESSION_STATUS_EXITED, false, "")
	h.s.emit(&pb.Event{Time: at(9000), Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "late sync"}}})
	if got := h.s.info().EndedAt; got.AsTime().UnixMilli() != 6000 {
		t.Fatalf("exit = %v", got)
	}
	var replay sessionOrdering
	h.s.log.Each(func(e *pb.Event) { replay.observe(e) })
	if replay.endedAt.AsTime().UnixMilli() != 6000 {
		t.Fatal("exit lost on replay")
	}
	state(10000, pb.SessionStatus_SESSION_STATUS_IDLE, false, "")
	if h.s.info().EndedAt != nil || h.s.info().AttentionSince != nil {
		t.Fatal("old lifecycle times retained")
	}
}

// An approval can outlive its turn (Codex asks for an escalated command in
// the background): it keeps the time it has waited since, until resolved.
func TestOrderingApprovalOutlivesItsTurn(t *testing.T) {
	at := func(ms int64) *timestamppb.Timestamp { return timestamppb.New(time.UnixMilli(ms)) }
	var o sessionOrdering
	o.observe(&pb.Event{Time: at(1000), Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "a"}}})
	o.observe(&pb.Event{Time: at(1500), Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "q"}}})
	o.observe(&pb.Event{Time: at(2000), Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{}}})
	if got := o.attentionSince(&pb.Session{PendingApprovals: 1}); got == nil || got.AsTime().UnixMilli() != 1000 {
		t.Fatalf("wait after the turn ended = %v", got)
	}
	o.observe(&pb.Event{Time: at(3000), Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{ApprovalId: "a"}}})
	if got := o.attentionSince(&pb.Session{PendingApprovals: 1}); got != nil {
		t.Fatalf("wait after it was resolved = %v", got)
	}
	// A question does end with its turn.
	if got := o.attentionSince(&pb.Session{PendingQuestions: 1}); got != nil {
		t.Fatalf("question wait = %v", got)
	}
}
