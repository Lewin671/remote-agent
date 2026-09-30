package daemon

import (
	"context"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Codex 0.159.0's approval prompt (codex-notes §4).
const codexApproval = "\x1b[1m\x1b[38;2;128;128;128m•\x1b[0m \x1b[1mRunning\x1b[0m touch ~/x\n" +
	"  \x1b[1mWould you like to run the following command?\x1b[0m\n" +
	"\x1b[1;7m› 1. Yes, proceed (y)\n" +
	"\x1b[0m  2. Yes, and don't ask again for commands that start with `touch` (\x1b[1mp\x1b[0m)\n" +
	"  3. No, and tell Codex what to do differently (\x1b[1mesc\x1b[0m)\n"

func TestAlwaysOption(t *testing.T) {
	for _, c := range []struct{ screen, want string }{
		{codexApproval, "Yes, and don't ask again for commands that start with `touch`"},
		{ccPrompt, "Yes, and always allow access to /w from this project"},
		{"  1. Yes\n  2. No\n", ""},
		// Wrapped at the terminal's width.
		{" ❯ 1. Yes\n   2. Yes, and always allow access to /very/long/path/e2e-cc\n   from this project\n   3. No\n", "Yes, and always allow access to /very/long/path/e2e-cc from this project"},
	} {
		got, yes := alwaysOption(agent.StripANSI(c.screen))
		if got != c.want || !yes {
			t.Errorf("alwaysOption = %q, %v; want %q", got, yes, c.want)
		}
	}
}

func resolvedOf(h *harness, id string) *pb.ApprovalResolved {
	for _, e := range h.events() {
		if r := e.GetApprovalResolved(); r != nil && r.ApprovalId == id {
			return r
		}
	}
	return nil
}

// The hook stays silent and the daemon picks the prompt's second option.
func TestAllowAlwaysAnswersThePrompt(t *testing.T) {
	h := newHarness(t, Config{ApprovalTimeout: time.Minute})
	h.writeRollout(turnContextLine("user"))
	done := make(chan []byte, 1)
	go func() {
		out, _ := h.s.hook(context.Background(), permissionPayload(h))
		done <- out
	}()
	var id string
	eventually(t, "approval request", func() bool {
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		for k := range h.s.approvals {
			id = k
		}
		return id != ""
	})
	if err := h.s.resolveBy(id, true, true, "", originLocal); err != nil {
		t.Fatal(err)
	}
	if out := <-done; out != nil {
		t.Fatalf("hook answered %s; want no output", out)
	}
	// Codex shows its prompt once the hook is done; the key closes it.
	h.term.onWrite = func(f *fakeTerm, b string) {
		if b == "2" {
			f.screen = idleScreen
		}
	}
	h.term.setScreen(codexApproval)
	eventually(t, "resolved", func() bool { return resolvedOf(h, id) != nil })
	r := resolvedOf(h, id)
	if r.Decision != pb.ApprovalDecision_APPROVAL_DECISION_ALLOW_ALWAYS || r.Message != "Yes, and don't ask again for commands that start with `touch`" {
		t.Fatalf("resolved %v", r)
	}
	if w := h.term.written(); len(w) != 1 || w[0] != "2" {
		t.Fatalf("writes %q", w)
	}
}

// A prompt without the option is answered for this request only.
func TestAllowAlwaysFallsBackToYes(t *testing.T) {
	h := newHarness(t, Config{ApprovalTimeout: time.Minute})
	h.writeRollout(turnContextLine("user"))
	a := &approval{id: "a1", key: "touch ~/x"}
	h.term.onWrite = func(f *fakeTerm, b string) { f.screen = idleScreen }
	h.term.setScreen("  \x1b[1mWould you like to run the following command?\x1b[0m touch ~/x\n\x1b[1;7m› 1. Yes, proceed (y)\n\x1b[0m  2. No, and tell Codex what to do differently (esc)\n")
	h.s.answerAlways(a, "t1", originLocal)
	r := resolvedOf(h, "a1")
	if r == nil || r.Decision != pb.ApprovalDecision_APPROVAL_DECISION_ALLOW {
		t.Fatalf("resolved %v", r)
	}
	if w := h.term.written(); len(w) != 1 || w[0] != "1" {
		t.Fatalf("writes %q", w)
	}
}

// Codex 0.159.2 may end a turn while an escalated command still waits for
// approval in the background; the request stays open for the client.
func TestCodexApprovalOutlivesTurn(t *testing.T) {
	h := newHarness(t, Config{ApprovalTimeout: time.Minute})
	h.writeRollout(turnContextLine("user"), taskStarted)
	h.s.bindRollout("codex-1", h.dir+"/rollout.jsonl")
	go h.s.hook(context.Background(), permissionPayload(h))
	var id string
	eventually(t, "approval request", func() bool {
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		for k := range h.s.approvals {
			id = k
		}
		return id != ""
	})
	h.writeRollout(taskComplete)
	h.s.readRollout()
	if r := resolvedOf(h, id); r != nil {
		t.Fatalf("approval settled at the end of the turn: %v", r)
	}
	if err := h.s.resolve(id, true, ""); err != nil {
		t.Fatalf("resolve after the turn: %v", err)
	}
}
