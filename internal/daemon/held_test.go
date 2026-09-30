package daemon

import (
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// ccBox is Claude Code's input box holding text.
func ccBox(text string) string {
	return ccRule + "\n\x1b[39m❯ " + text + "\n" + ccRule + "\n   Haiku 4.5\n  ⏸ manual mode on · ← for agents\n"
}

// Up pulls the CLI's queue into the input box; the canceled message is
// dropped and the other one written again.
func TestCancelHeldMessage(t *testing.T) {
	h := claudeHarness(t, Config{})
	h.s.mu.Lock()
	h.s.queue.Held = []input{{RequestID: "a", Text: "first"}, {RequestID: "b", Text: "second"}}
	h.s.mu.Unlock()
	box := ""
	h.term.onWrite = func(f *fakeTerm, b string) {
		switch {
		case b == "\x1b[A":
			box = "first second"
		case b == agent.KeyClearLine+"\x7f":
			box = ""
		}
		if box == "" {
			f.screen = ccIdle
		} else {
			f.screen = ccBox(box)
		}
	}
	if err := h.s.cancelInputBy("a", originLocal); err != nil {
		t.Fatal(err)
	}
	w := h.term.written()
	if len(w) != 3 || w[0] != "\x1b[A" || !strings.Contains(w[2], "second") {
		t.Fatalf("writes %q", w)
	}
	h.s.mu.Lock()
	held := h.s.queue.Held
	h.s.mu.Unlock()
	if len(held) != 1 || held[0].RequestID != "b" {
		t.Fatalf("held %v", held)
	}
	st := inputStates(h, "a")
	if len(st) == 0 || st[len(st)-1].Status != pb.InputStatus_INPUT_STATUS_CANCELED {
		t.Fatalf("states %v", st)
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
