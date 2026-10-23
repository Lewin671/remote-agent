package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func resumableHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, Config{CodexBin: "/bin/echo", Exe: "remote-agent-test", PollInterval: time.Hour, ScreenInterval: time.Hour})
	fakeTmux(t, h)
	h.s.meta.Shell = true
	h.d.sessions[h.s.meta.ID] = h.s
	return h
}

func TestNaturalExitFinalizesBeforeResume(t *testing.T) {
	h := resumableHarness(t)
	h.term.alive = false
	// The poller publishes Exited, then stops at its final transcript read.
	h.s.readMu.Lock()
	h.s.start(context.Background())
	oldDone, oldStop := h.s.done, h.s.stop
	defer oldStop()
	eventually(t, "exit flag published", func() bool {
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		return h.s.meta.Exited
	})
	if h.s.lifecycleMu.TryLock() {
		h.s.lifecycleMu.Unlock()
		t.Error("old exit finalizer does not own the session lifecycle")
	}
	done := make(chan error, 1)
	go func() { _, err := h.d.resume(h.s.meta.ID); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("resume overtook old exit finalization: %v", err)
	default:
	}
	h.term.mu.Lock()
	h.term.alive = true
	h.term.mu.Unlock()
	h.s.readMu.Unlock()
	if err := awaitLifecycleResult(t, done); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldDone:
	default:
		t.Error("resumed before old workers finished")
	}
	if h.s.done == oldDone {
		t.Fatal("resume reused the old loop generation")
	}
	oldStop()
	select {
	case <-h.s.done:
		t.Error("old exit canceled the resumed generation")
	default:
	}
	var states []pb.SessionStatus
	for _, e := range h.events() {
		if st := e.GetSessionState(); st != nil {
			states = append(states, st.Status)
		}
	}
	if len(states) != 2 || states[0] != pb.SessionStatus_SESSION_STATUS_EXITED || states[1] != pb.SessionStatus_SESSION_STATUS_STARTING {
		t.Fatalf("exit/resume state order: %v", states)
	}
	h.s.stop()
	<-h.s.done
}

func TestResumeDrainsOldLoopsWithoutHoldingWorkerLocks(t *testing.T) {
	h := resumableHarness(t)
	h.s.meta.Exited = true
	ctx, cancel := context.WithCancel(context.Background())
	canceled := make(chan struct{})
	var once sync.Once
	old := &sessionLoops{stop: func() { cancel(); once.Do(func() { close(canceled) }) }, done: make(chan struct{})}
	h.s.sessionLoops = old
	release := make(chan struct{})
	go func() {
		<-ctx.Done()
		<-release
		// A canceled poller may still be waiting to finalize its exit.
		_ = h.s.markExited("")
		h.s.ctlMu.Lock()
		h.s.ctlMu.Unlock()
		close(old.done)
	}()
	done := make(chan error, 1)
	go func() { _, err := h.d.resume(h.s.meta.ID); done <- err }()
	select {
	case <-canceled:
	case <-done:
		close(release)
		t.Fatal("resume did not cancel and await the old generation")
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("resume did not begin draining old workers")
	}
	select {
	case err := <-done:
		t.Fatalf("resume did not await old workers: %v", err)
	default:
	}
	close(release)
	if err := awaitLifecycleResult(t, done); err != nil {
		t.Fatal(err)
	}
	h.s.stop()
	<-h.s.done
}

func awaitLifecycleResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("session lifecycle operation did not finish")
		return nil
	}
}
