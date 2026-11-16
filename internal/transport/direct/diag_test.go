package direct

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

// A path that loses its gate says which gate, once, with stage times and no
// identifiers.
func TestPathEndNamesTheGateThatRefusedIt(t *testing.T) {
	buf := captureLog(t)
	var allowed atomic.Bool
	allowed.Store(true)
	p, err := New(context.Background(), Config{Use: 2, Owner: "owner-secret", Peer: "peer-secret", Generation: 3, Pool: NewPool(8), Allow: allowed.Load, LoopbackOnly: true,
		Why: func() string {
			if allowed.Load() {
				return ""
			}
			return "round_deadline"
		}})
	if err != nil {
		t.Fatal(err)
	}
	allowed.Store(false)
	select {
	case <-p.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("path did not end")
	}
	p.Close()
	out := buf.String()
	if strings.Count(out, "direct path ended") != 1 || !strings.Contains(out, "reason=round_deadline") || !strings.Contains(out, "use=2") || !strings.Contains(out, "generation=3") {
		t.Fatalf("log = %q", out)
	}
	for _, secret := range []string{"owner-secret", "peer-secret", "candidate", "ufrag"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log leaks %q: %s", secret, out)
		}
	}
}

// The first reason is kept; a later close cannot rewrite it.
func TestPathEndKeepsTheFirstReason(t *testing.T) {
	buf := captureLog(t)
	p, err := New(context.Background(), Config{Use: 1, Owner: "o", Peer: "p", Generation: 1, Pool: NewPool(8), Allow: func() bool { return true }, LoopbackOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	p.closeWhy("policy")
	p.closeWhy("pc_failed")
	p.Close()
	p.Wait()
	if out := buf.String(); !strings.Contains(out, "reason=policy") || strings.Contains(out, "pc_failed") {
		t.Fatalf("log = %q", out)
	}
}

// The round's log shows how far each peer connection got and a bounded trace
// of signal kinds; acknowledgements are left out and the trace stops growing.
func TestRoundTraceIsBoundedAndStepsAreWords(t *testing.T) {
	if got := fmtSteps(true, true, true, false, false, false); got != "blr---" {
		t.Fatalf("steps = %q", got)
	}
	if got := fmtSteps(true, true, true, true, true, true); got != "blrdca" {
		t.Fatalf("steps = %q", got)
	}
	s := &Session{born: time.Now()}
	for i := 0; i < 3*maxTrace; i++ {
		s.traceAdd("rx", 5, 1, true)
	}
	s.traceAdd("tx", 4, 0, true) // an acknowledgement: not recorded
	s.traceMu.Lock()
	n := len(s.trace)
	s.traceMu.Unlock()
	if n != maxTrace {
		t.Fatalf("trace length = %d, want %d", n, maxTrace)
	}
	s2 := &Session{born: time.Now()}
	s2.traceAdd("tx", 4, 0, true)
	s2.traceAdd("tx", 7, 1, false)
	if len(s2.trace) != 1 || !strings.HasPrefix(s2.trace[0], "tx7.1!@") {
		t.Fatalf("trace = %v", s2.trace)
	}
}
