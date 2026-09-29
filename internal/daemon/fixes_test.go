package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// Accepted input and request IDs survive a daemon restart.
func TestQueueSurvivesRestart(t *testing.T) {
	h := newHarness(t, Config{})
	h.term.setScreen(draftScreen)
	if _, err := h.s.send("hello", "r1"); err != nil {
		t.Fatal(err)
	}
	m := h.s.meta
	h.s.log.Close()
	h.open(m)
	if n := h.s.info().QueuedInputs; n != 1 {
		t.Fatalf("queued after restart: %d", n)
	}
	if pos, _ := h.s.send("hello", "r1"); pos != 0 {
		t.Fatal("retried request accepted twice after restart")
	}
	h.term.setScreen(idleScreen)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.start(ctx)
	eventually(t, "delivery after restart", func() bool { return len(h.term.written()) == 1 })
}

// A message that was being written when the daemon died is dropped if
// history shows it was submitted, and sent again otherwise.
func TestInflightRecovery(t *testing.T) {
	for _, submitted := range []bool{true, false} {
		h := newHarness(t, Config{})
		off := int64(0)
		q := &queueState{Queue: []input{{Text: "one", Inflight: &off}, {Text: "two"}}}
		if err := q.save(h.dir); err != nil {
			t.Fatal(err)
		}
		if submitted {
			os.WriteFile(h.d.cfg.HistoryPath, []byte(`{"session_id":"x","text":"one"}`+"\n"), 0o600)
		}
		m := h.s.meta
		h.s.log.Close()
		h.open(m)
		got := h.s.queue.Queue
		if submitted && (len(got) != 1 || got[0].Text != "two") {
			t.Fatalf("submitted inflight not dropped: %+v", got)
		}
		if !submitted && (len(got) != 2 || got[0].Text != "one" || got[0].Inflight != nil) {
			t.Fatalf("unsubmitted inflight not retried: %+v", got)
		}
	}
}

// Identical text submitted by another Codex session does not confirm
// this session's injection.
func TestHistoryMatchesSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	hist := newHistory(path, -1)
	os.WriteFile(path, []byte(`{"session_id":"other","text":"same"}`+"\n"), 0o600)
	if hist.submitted("same", "mine") {
		t.Fatal("another session's line counted")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"session_id":"mine","text":"same"}` + "\n")
	f.Close()
	if !hist.submitted("same", "mine") {
		t.Fatal("own line not counted")
	}
}

// A reply that returns success is the decision Codex gets, even when it
// races the timeout.
func TestApprovalReplyRacingTimeout(t *testing.T) {
	h := newHarness(t, Config{ApprovalTimeout: time.Millisecond})
	h.writeRollout(turnContextLine("user"))
	for i := 0; i < 400; i++ {
		out := make(chan []byte, 1)
		go func() {
			b, _ := h.s.hook(context.Background(), permissionPayload(h))
			out <- b
		}()
		var id string
		for id == "" {
			h.s.mu.Lock()
			for k, a := range h.s.approvals {
				if !a.claimed {
					id = k
				}
			}
			h.s.mu.Unlock()
			if id == "" {
				time.Sleep(50 * time.Microsecond)
			}
		}
		time.Sleep(time.Duration(700+(i%12)*50) * time.Microsecond)
		err := h.s.resolve(id, true, "")
		b := <-out
		allowed := strings.Contains(string(b), `"allow"`)
		if err == nil && !allowed {
			t.Fatalf("round %d: approve succeeded but Codex got %s", i, b)
		}
		if err != nil && allowed {
			t.Fatalf("round %d: approve failed but Codex got allow", i)
		}
	}
}

// Control operations do not interleave: nothing is written between another
// operation's hold and release.
func TestControlOperationsAreSerialized(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.emit(&pb.Event{Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); h.s.inject(context.Background(), "msg") }()
		go func() { defer wg.Done(); h.s.interrupt() }()
	}
	wg.Wait()
	h.term.mu.Lock()
	ops := append([]string(nil), h.term.ops...)
	h.term.mu.Unlock()
	held := false
	writesInHold := 0
	for _, op := range ops {
		switch op {
		case "hold":
			if held {
				t.Fatal("nested hold")
			}
			held, writesInHold = true, 0
		case "release":
			held = false
		case "write":
			if held {
				writesInHold++
				if writesInHold > 1 {
					t.Fatalf("a second write landed inside a hold: %v", ops)
				}
			}
		}
	}
}
