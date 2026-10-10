package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// ccBox is Claude Code's input box holding text.
func ccBox(text string) string {
	return ccRule + "\n\x1b[39m❯ " + text + "\n" + ccRule + "\n   Haiku 4.5\n  ⏸ manual mode on · ← for agents\n"
}

// heldPair is a Claude Code session whose CLI holds two messages, with a
// terminal that pulls them into the input box on Up. clears counts the
// clear keys; with stuck set they do nothing.
func heldPair(t *testing.T) (h *harness, clears *int, stuck *bool) {
	h = claudeHarness(t, Config{})
	h.s.mu.Lock()
	h.s.queue.Held = []input{{RequestID: "a", Text: "first"}, {RequestID: "b", Text: "second"}}
	h.s.recent = append(h.s.recent, h.s.queue.Held...)
	if err := h.s.queue.save(h.dir); err != nil {
		t.Fatal(err)
	}
	h.s.mu.Unlock()
	h.s.emit(&pb.Event{Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
	for _, id := range []string{"a", "b"} {
		h.s.emitInput(id, pb.InputStatus_INPUT_STATUS_HELD_BY_CLI)
	}
	clears, stuck = new(int), new(bool)
	box := ""
	h.term.onWrite = func(f *fakeTerm, b string) {
		switch {
		case b == "\x1b[A":
			box = "first second"
		case b == agent.KeyClearLine+"\x7f":
			*clears++
			if !*stuck {
				box = ""
			}
		case strings.HasPrefix(b, "\x1b[200~"):
			return // submitted: the box is empty again
		}
		if box == "" {
			f.screen = ccIdle
		} else {
			f.screen = ccBox(box)
		}
	}
	return h, clears, stuck
}

// Up pulls the CLI's queue into the input box; the canceled message is
// dropped and the other one goes through the daemon's queue again.
func TestCancelHeldMessage(t *testing.T) {
	h, _, _ := heldPair(t)
	if err := h.s.cancelInputBy("a", originLocal); err != nil {
		t.Fatal(err)
	}
	if w := h.term.written(); len(w) != 2 || w[0] != "\x1b[A" {
		t.Fatalf("writes %q", w)
	}
	h.s.mu.Lock()
	held, queue, recent := h.s.queue.Held, h.s.queue.Queue, len(h.s.recent)
	h.s.mu.Unlock()
	if len(held) != 0 || len(queue) != 1 || queue[0].RequestID != "b" || queue[0].Paused || queue[0].writing() || recent != 1 {
		t.Fatalf("held %v, queue %+v, %d remembered", held, queue, recent)
	}
	st := inputStates(h, "a")
	if len(st) == 0 || st[len(st)-1].Status != pb.InputStatus_INPUT_STATUS_CANCELED {
		t.Fatalf("states %v", st)
	}
	if st := inputStates(h, "b"); st[len(st)-1].Status != pb.InputStatus_INPUT_STATUS_QUEUED || st[len(st)-1].Paused {
		t.Fatalf("states %v", st)
	}
	runInjector(t, h)
	eventually(t, "the other message written again", func() bool {
		w := h.term.written()
		return len(w) == 3 && strings.Contains(w[2], "second")
	})
}

// The other messages are saved back to the daemon's queue before the input
// box, which holds the CLI's queue by then, is emptied: a daemon that
// stops in between comes back with them paused, not gone.
func TestCancelHeldSavesTheOthersBeforeClearing(t *testing.T) {
	h, clears, _ := heldPair(t)
	var atClear []byte
	inner := h.term.onWrite
	h.term.onWrite = func(f *fakeTerm, b string) {
		if b == agent.KeyClearLine+"\x7f" && atClear == nil {
			atClear, _ = os.ReadFile(filepath.Join(h.dir, "queue.json"))
		}
		inner(f, b)
	}
	if err := h.s.cancelInputBy("a", originLocal); err != nil {
		t.Fatal(err)
	}
	if *clears == 0 {
		t.Fatal("the input box was not cleared")
	}
	// The daemon stopped at the first clear key.
	if err := os.WriteFile(filepath.Join(h.dir, "queue.json"), atClear, 0o600); err != nil {
		t.Fatal(err)
	}
	evs := h.events()
	h.s.log.Close()
	os.Remove(filepath.Join(h.dir, "events.jsonl"))
	h.open(h.s.meta)
	var before []*pb.Event
	for _, e := range evs {
		if st := e.GetInputState(); st == nil || st.Status == pb.InputStatus_INPUT_STATUS_HELD_BY_CLI {
			before = append(before, e)
		}
	}
	if err := h.s.log.Append(h.s.meta.ID, before, "", 0); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	h.s.mu.Lock()
	queue := h.s.queue.Queue
	h.s.mu.Unlock()
	if len(queue) != 1 || queue[0].RequestID != "b" || !queue[0].Paused {
		t.Fatalf("queue after restart %+v", queue)
	}
	if st := inputStates(h, "b"); !st[len(st)-1].Paused {
		t.Fatalf("not reported paused: %v", st)
	}
	n := len(h.term.written())
	runInjector(t, h)
	time.Sleep(100 * time.Millisecond)
	if len(h.term.written()) != n {
		t.Fatal("written again without the user's word")
	}
}

// The input box could not be emptied: the other messages wait paused and
// are not written into what the box still holds.
func TestCancelHeldLeavesTheOthersPausedWhenTheBoxStays(t *testing.T) {
	h, _, stuck := heldPair(t)
	*stuck = true
	if err := h.s.cancelInputBy("a", originLocal); err != errHeldChanged {
		t.Fatalf("err %v", err)
	}
	h.s.mu.Lock()
	held, queue := h.s.queue.Held, h.s.queue.Queue
	h.s.mu.Unlock()
	if len(held) != 1 || held[0].RequestID != "a" || len(queue) != 1 || queue[0].RequestID != "b" || !queue[0].Paused {
		t.Fatalf("held %+v, queue %+v", held, queue)
	}
	if st := inputStates(h, "b"); !st[len(st)-1].Paused {
		t.Fatalf("states %v", st)
	}
	for _, w := range h.term.written() {
		if strings.Contains(w, "second") {
			t.Fatalf("wrote %q into a box that was not emptied", w)
		}
	}
}

// Something else in the CLI's queue: nothing is cleared.
func TestCancelHeldMismatch(t *testing.T) {
	h := claudeHarness(t, Config{})
	h.s.mu.Lock()
	h.s.queue.Held = []input{{RequestID: "a", Text: "first"}}
	h.s.mu.Unlock()
	h.term.onWrite = func(f *fakeTerm, b string) {
		if b == "\x1b[A" {
			f.screen = ccBox("first typed locally")
		}
	}
	if err := h.s.cancelInputBy("a", originLocal); err != errHeldChanged {
		t.Fatalf("err %v", err)
	}
	if w := h.term.written(); len(w) != 1 {
		t.Fatalf("writes %q", w)
	}
}

// The model got the message between the check that the CLI still held it
// and the move to the held list: it is not held, or it would be reported
// withdrawn in the terminal once the turn ends.
func TestHoldAfterTheModelGotTheMessage(t *testing.T) {
	h := claudeHarness(t, Config{})
	if _, err := h.s.send("steer", "r1"); err != nil {
		t.Fatal(err)
	}
	h.s.emit(&pb.Event{Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
	if err := h.s.markInflight(0, "r1"); err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	in := h.s.queue.Queue[0] // as settle saw it, still held by the CLI
	h.s.mu.Unlock()
	// The transcript reader logs the CLI handing it to the model.
	msg := &pb.UserMessage{Text: "steer"}
	e := &pb.Event{Kind: &pb.Event_UserMessage{UserMessage: msg}}
	annotateLogged(h.s, e)
	h.s.emit(e)
	h.s.received(msg.RequestId)

	h.s.hold(in)
	h.s.emit(turnEnd(pb.TurnOutcome_TURN_OUTCOME_COMPLETED))
	h.s.sweepHeld()
	h.s.mu.Lock()
	held, queue := len(h.s.queue.Held), len(h.s.queue.Queue)
	h.s.mu.Unlock()
	st := inputStates(h, "r1")
	if held != 0 || queue != 0 || st[len(st)-1].Status != pb.InputStatus_INPUT_STATUS_SUBMITTED {
		t.Fatalf("held %d, queued %d, states %v", held, queue, st)
	}
}
