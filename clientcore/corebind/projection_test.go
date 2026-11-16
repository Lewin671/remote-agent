package corebind

import (
	"testing"

	litecore "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	litev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	stdcore "github.com/Lewin671/remote-agent/gen/remoteagent/core/v1"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func TestProjectionRoundTrip(t *testing.T) {
	p := NewProjection(int32(litev1.Agent_AGENT_CLAUDE))

	// Encode with standard protobuf, decode via corebind (lite), and verify output decodes with standard protobuf.
	batch1, err := proto.Marshal(&stdcore.EventBatch{
		Events: []*stdv1.Event{
			{Seq: 1, Kind: &stdv1.Event_TurnStarted{TurnStarted: &stdv1.TurnStarted{}}},
			{Seq: 2, Kind: &stdv1.Event_UserMessage{UserMessage: &stdv1.UserMessage{Text: "hello"}}},
			{Seq: 3, Kind: &stdv1.Event_ToolStarted{ToolStarted: &stdv1.ToolStarted{Title: "ls", CallId: "c1"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out1, err := p.Append(batch1)
	if err != nil {
		t.Fatal(err)
	}
	var s1 stdcore.ProjectionState
	if err := proto.Unmarshal(out1, &s1); err != nil {
		t.Fatal(err)
	}
	if !s1.Running || len(s1.Tools) != 1 || len(s1.TypedLocallySeqs) != 1 {
		t.Fatalf("unexpected state after batch1: %+v", &s1)
	}

	batch2, err := proto.Marshal(&stdcore.EventBatch{
		Events: []*stdv1.Event{
			{Seq: 4, Kind: &stdv1.Event_InputState{InputState: &stdv1.InputState{RequestId: "r1", Status: stdv1.InputStatus_INPUT_STATUS_SUBMITTED}}},
			{Seq: 5, Kind: &stdv1.Event_ToolCall{ToolCall: &stdv1.ToolCall{CallId: "c1", Status: "completed"}}},
			{Seq: 6, Kind: &stdv1.Event_TurnEnded{TurnEnded: &stdv1.TurnEnded{Outcome: stdv1.TurnOutcome_TURN_OUTCOME_COMPLETED}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out2, err := p.Append(batch2)
	if err != nil {
		t.Fatal(err)
	}
	var s2 stdcore.ProjectionState
	if err := proto.Unmarshal(out2, &s2); err != nil {
		t.Fatal(err)
	}
	if s2.Running || len(s2.Tools) != 0 || len(s2.TypedLocallySeqs) != 0 || s2.CompletedAt[3] != 5 {
		t.Fatalf("unexpected state after batch2: %+v", &s2)
	}

	snapBytes, err := p.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var snap stdcore.ProjectionState
	if err := proto.Unmarshal(snapBytes, &snap); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(&s2, &snap) {
		t.Fatal("Snapshot differs from last Append")
	}

	liveBytes, err := proto.Marshal(&stdv1.LiveSnapshot{
		Session: &stdv1.Session{Id: "s", LastSeq: 6},
		Running: true,
		Actions: []*stdv1.Event{
			{Seq: 0, Kind: &stdv1.Event_ApprovalRequested{ApprovalRequested: &stdv1.ApprovalRequested{ApprovalId: "a1"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	actBytes, err := p.CurrentActions(liveBytes)
	if err != nil {
		t.Fatal(err)
	}
	var act stdcore.ProjectionState
	if err := proto.Unmarshal(actBytes, &act); err != nil {
		t.Fatal(err)
	}
	if !act.Running || len(act.Approvals) != 1 || act.Approvals[0].ApprovalId != "a1" {
		t.Fatalf("unexpected CurrentActions state: %+v", &act)
	}

	p.Reset(int32(litev1.Agent_AGENT_CODEX))
	emptySnap, err := p.Append(nil)
	if err != nil {
		t.Fatal(err)
	}
	reducedEmpty, err := Reduce(int32(litev1.Agent_AGENT_CODEX), nil)
	if err != nil {
		t.Fatal(err)
	}
	var sEmpty, sRed litecore.ProjectionState
	if err := sEmpty.Unmarshal(emptySnap); err != nil {
		t.Fatal(err)
	}
	if err := sRed.Unmarshal(reducedEmpty); err != nil {
		t.Fatal(err)
	}
	if len(sEmpty.Inputs) != 0 || len(sRed.Inputs) != 0 || sEmpty.Running != sRed.Running {
		t.Fatal("Reset state differs from empty Reduce")
	}
}
