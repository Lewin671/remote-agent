package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"google.golang.org/protobuf/proto"
)

func TestApprovalCommitsProjectAttentionUntilResolved(t *testing.T) {
	h := newHarness(t, Config{ApprovalTimeout: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replies := []chan error{make(chan error, 1), make(chan error, 1)}
	ids := make([]string, 2)
	events := make([]*pb.Event, 2)
	for i := 0; i < 2; i++ {
		ch := replies[i]
		go func() {
			_, err := h.s.permission(ctx, &agent.Hook{ToolName: "shell", ToolInput: []byte(`{"command":"true"}`)})
			ch <- err
		}()
		want := i + 1
		eventually(t, "pending approval", func() bool { return int(h.s.info().PendingApprovals) == want })
		var requested []*pb.Event
		for _, e := range h.events() {
			if e.GetApprovalRequested() != nil {
				requested = append(requested, e)
			}
		}
		events[i] = requested[i]
		ids[i] = events[i].GetApprovalRequested().ApprovalId
		if got := h.s.info().AttentionSince; got == nil || !proto.Equal(got, events[0].Time) {
			t.Fatalf("pending approval attention=%v want=%v", got, events[0].Time)
		}
	}
	if err := h.s.resolve(ids[0], true, ""); err != nil {
		t.Fatal(err)
	}
	if err := <-replies[0]; err != nil {
		t.Fatal(err)
	}
	if got := h.s.info().AttentionSince; got == nil || !proto.Equal(got, events[1].Time) {
		t.Fatalf("next approval attention=%v want=%v", got, events[1].Time)
	}
	if err := h.s.resolve(ids[1], false, "no"); err != nil {
		t.Fatal(err)
	}
	if err := <-replies[1]; err != nil {
		t.Fatal(err)
	}
	if h.s.info().AttentionSince != nil || len(h.s.ordering.approvals) != 0 {
		t.Fatal("resolved approvals retained attention projection")
	}
}

func TestCommittedTurnOutcomeProjectsOnce(t *testing.T) {
	h := newHarness(t, Config{})
	if err := h.s.emit(&pb.Event{TurnId: "t", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}); err != nil {
		t.Fatal(err)
	}
	h.s.readMu.Lock()
	h.s.mu.Lock()
	ok := h.s.abortTurnLocked("test")
	h.s.mu.Unlock()
	h.s.readMu.Unlock()
	if !ok || h.s.running || h.s.turnEnds != 1 {
		t.Fatalf("turn was omitted or projected twice: running=%v ends=%d", h.s.running, h.s.turnEnds)
	}
	h.reopen()
	if h.s.running || h.s.turnEnds != 1 {
		t.Fatal("live and replayed turn projection differ")
	}
}

func TestTranscriptProjectionMatchesReplayOnce(t *testing.T) {
	h := newHarness(t, Config{})
	h.writeRollout(taskStarted, taskComplete)
	h.s.bindRollout("cli", filepath.Join(h.dir, "rollout.jsonl"))
	if h.s.running || h.s.turnEnds != 1 {
		t.Fatalf("transcript projected incorrectly: running=%v ends=%d", h.s.running, h.s.turnEnds)
	}
	h.reopen()
	if h.s.running || h.s.turnEnds != 1 {
		t.Fatal("transcript and replay projection differ")
	}
}

func TestFailedApprovalCommitDoesNotProjectOrdering(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.log.Close()
	if _, err := h.s.permission(context.Background(), &agent.Hook{ToolName: "shell", ToolInput: []byte(`{"command":"true"}`)}); err != nil {
		t.Fatal(err)
	}
	if len(h.s.ordering.approvals) != 0 || h.s.info().AttentionSince != nil {
		t.Fatal("uncommitted approval changed ordering")
	}
}
