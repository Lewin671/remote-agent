package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/codex"
)

const (
	taskStarted  = `{"timestamp":"2026-09-29T10:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`
	taskComplete = `{"timestamp":"2026-09-29T10:00:05Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`
	turnAborted  = `{"timestamp":"2026-09-29T10:00:05Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t1","reason":"interrupted"}}`
)

func queueIDs(h *harness) []string {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	var out []string
	for _, in := range h.s.queue.Queue {
		out = append(out, in.RequestID)
	}
	return out
}

func TestQueueOrderKeepsSteerAhead(t *testing.T) {
	q := &queueState{}
	q.insert(input{RequestID: "q1", Mode: pb.SendMode_SEND_MODE_QUEUE})
	q.insert(input{RequestID: "p1", Mode: pb.SendMode_SEND_MODE_STEER, Paused: true})
	q.insert(input{RequestID: "s1", Mode: pb.SendMode_SEND_MODE_STEER})
	q.insert(input{RequestID: "q2", Mode: pb.SendMode_SEND_MODE_QUEUE})
	q.insert(input{RequestID: "s2", Mode: pb.SendMode_SEND_MODE_STEER})
	var got []string
	for _, in := range q.Queue {
		got = append(got, in.RequestID)
	}
	want := []string{"s1", "s2", "q1", "q2", "p1"}
	if len(got) != len(want) {
		t.Fatalf("order %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
	// A message being written keeps its place at the head.
	q = &queueState{Queue: []input{{RequestID: "w", Mode: pb.SendMode_SEND_MODE_QUEUE, Stage: pb.InputStage_INPUT_STAGE_SUBMITTING}}}
	if pos := q.insert(input{RequestID: "s", Mode: pb.SendMode_SEND_MODE_STEER}); pos != 2 {
		t.Fatalf("steer went before the message being written: %d", pos)
	}
}

// A steer is written during the turn; a queue message only after it ends.
func TestQueueWaitsForTurnEnd(t *testing.T) {
	h := newHarness(t, Config{})
	h.writeRollout(taskStarted)
	h.s.bindRollout("codex-1", filepath.Join(h.dir, "rollout.jsonl"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.start(ctx)
	eventually(t, "running", h.s.isRunningNoRead)

	if _, err := h.s.enqueueMode("later", "q1", nil, originLocal, pb.SendMode_SEND_MODE_QUEUE); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.enqueueMode("now", "s1", nil, originLocal, pb.SendMode_SEND_MODE_STEER); err != nil {
		t.Fatal(err)
	}
	eventually(t, "steer written", func() bool { return len(h.term.written()) == 1 })
	if w := h.term.written(); w[0] != string(codex.PasteBytes("now")) {
		t.Fatalf("writes %q", w)
	}
	time.Sleep(150 * time.Millisecond)
	if w := h.term.written(); len(w) != 1 {
		t.Fatalf("queue message written during the turn: %q", w)
	}
	h.writeRollout(taskComplete)
	eventually(t, "queue written", func() bool { return len(h.term.written()) == 2 })
	if w := h.term.written(); w[1] != string(codex.PasteBytes("later")) {
		t.Fatalf("writes %q", w)
	}
	st := inputStates(h, "q1")
	if st[0].Mode != pb.SendMode_SEND_MODE_QUEUE {
		t.Fatalf("mode not reported: %v", st[0])
	}
}

// An interrupted turn pauses what was not written; ResumeInput sends it.
func TestInterruptPausesWaitingInput(t *testing.T) {
	h := newHarness(t, Config{})
	h.writeRollout(taskStarted)
	h.s.bindRollout("codex-1", filepath.Join(h.dir, "rollout.jsonl"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.start(ctx)
	eventually(t, "running", h.s.isRunningNoRead)
	h.term.setScreen(draftScreen) // nothing can be written yet
	if _, err := h.s.enqueueMode("after", "q1", nil, originLocal, pb.SendMode_SEND_MODE_QUEUE); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.enqueueMode("steer", "s1", nil, originLocal, pb.SendMode_SEND_MODE_STEER); err != nil {
		t.Fatal(err)
	}
	h.writeRollout(turnAborted)
	eventually(t, "paused", func() bool {
		st := inputStates(h, "q1")
		return len(st) > 1 && st[len(st)-1].Paused
	})
	h.term.setScreen(idleScreen)
	time.Sleep(150 * time.Millisecond)
	if w := h.term.written(); len(w) != 0 {
		t.Fatalf("paused input written: %q", w)
	}
	// New input is not held up by paused messages.
	if _, err := h.s.enqueueMode("fresh", "n1", nil, originLocal, pb.SendMode_SEND_MODE_STEER); err != nil {
		t.Fatal(err)
	}
	eventually(t, "fresh written", func() bool { return len(h.term.written()) == 1 })
	if err := h.s.resumeInputBy("s1", originLocal); err != nil {
		t.Fatal(err)
	}
	eventually(t, "resumed written", func() bool { return len(h.term.written()) == 2 })
	if w := h.term.written(); w[1] != string(codex.PasteBytes("steer")) {
		t.Fatalf("writes %q", w)
	}
	// The paused queue message can still be canceled.
	if err := h.s.cancelInput("q1"); err != nil {
		t.Fatal(err)
	}
	if ids := queueIDs(h); len(ids) != 0 {
		eventually(t, "queue empty", func() bool { return len(queueIDs(h)) == 0 })
	}
}

// Without a turn starting, a queue message goes after the fallback.
func TestQueueGateFallback(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.mu.Lock()
	h.s.gate, h.s.gateAt = 1, time.Now().Add(-gateFallback)
	held := h.s.holdsLocked(input{Mode: pb.SendMode_SEND_MODE_QUEUE})
	h.s.gateAt = time.Now()
	heldNow := h.s.holdsLocked(input{Mode: pb.SendMode_SEND_MODE_QUEUE})
	h.s.mu.Unlock()
	if held || !heldNow {
		t.Fatalf("held after fallback %v, before %v", held, heldNow)
	}
}
