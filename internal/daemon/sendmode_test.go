package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"testing/synctest"
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
	synctest.Test(t, testQueueWaitsForTurnEnd)
}

func testQueueWaitsForTurnEnd(t *testing.T) {
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
	time.Sleep(2 * time.Second) // cover the one-second held-input retry
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
	synctest.Test(t, testInterruptPausesWaitingInput)
}

func testInterruptPausesWaitingInput(t *testing.T) {
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
	time.Sleep(2 * time.Second) // cover the one-second held-input retry
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

// reopen stands for a daemon restart: the session is loaded again from its
// directory.
func (h *harness) reopen() {
	h.t.Helper()
	h.s.mu.Lock()
	m := h.s.meta
	h.s.mu.Unlock()
	h.s.log.Close()
	h.open(m)
}

func turnEnd(outcome pb.TurnOutcome) *pb.Event {
	return &pb.Event{Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: outcome}}}
}

// The daemon stopped after the interrupted turn's end was logged and
// before the waiting input was saved as paused: the next daemon pauses it
// before anything is written.
func TestRestartAfterInterruptPausesWaitingInput(t *testing.T) {
	synctest.Test(t, testRestartAfterInterruptPausesWaitingInput)
}

func testRestartAfterInterruptPausesWaitingInput(t *testing.T) {
	h := newHarness(t, Config{})
	h.term.setScreen(draftScreen)
	h.s.emit(&pb.Event{Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
	if _, err := h.s.enqueueMode("after", "q1", nil, originLocal, pb.SendMode_SEND_MODE_QUEUE); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.send("steer", "s1"); err != nil {
		t.Fatal(err)
	}
	h.s.emit(turnEnd(pb.TurnOutcome_TURN_OUTCOME_ABORTED)) // and no afterTurn
	h.reopen()
	for _, id := range []string{"s1", "q1"} {
		st := inputStates(h, id)
		if last := st[len(st)-1]; !last.Paused || last.Status != pb.InputStatus_INPUT_STATUS_QUEUED || last.Reason != "interrupted" {
			t.Fatalf("%s after restart: %v", id, last)
		}
	}
	h.term.setScreen(idleScreen)
	runInjector(t, h)
	time.Sleep(2 * time.Second) // cover the one-second held-input retry
	if w := h.term.written(); len(w) != 0 {
		t.Fatalf("interrupted input written after restart: %q", w)
	}
	// It is saved: a second restart reports nothing new.
	n := len(h.events())
	h.reopen()
	if len(h.events()) != n || !h.s.queue.Queue[0].Paused {
		t.Fatal("pause not saved")
	}
}

// What was accepted or resumed after the interrupted turn is not paused by
// a restart, and an interruption long past does not pause what waits when
// a later turn ends.
func TestRestartKeepsInputFromAfterTheInterrupt(t *testing.T) {
	h := newHarness(t, Config{})
	h.term.setScreen(draftScreen)
	h.s.emit(&pb.Event{Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
	if _, err := h.s.send("resumed", "s1"); err != nil {
		t.Fatal(err)
	}
	h.s.emit(turnEnd(pb.TurnOutcome_TURN_OUTCOME_ABORTED))
	h.s.afterTurn()
	if err := h.s.resumeInputBy("s1", originLocal); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.send("new", "s2"); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	for _, in := range h.s.queue.Queue {
		if in.Paused {
			t.Fatalf("%s paused by the restart", in.RequestID)
		}
	}
	h.s.emit(&pb.Event{Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}, turnEnd(pb.TurnOutcome_TURN_OUTCOME_COMPLETED))
	h.s.afterTurn()
	for _, in := range h.s.queue.Queue {
		if in.Paused {
			t.Fatalf("%s paused by a turn that completed", in.RequestID)
		}
	}
}
