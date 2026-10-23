package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/eventlog"
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

type blockedLaunchAdapter struct {
	agent.Adapter
	entered, release chan struct{}
	continueLaunch   bool
	calls            atomic.Int32
}

var errBlockedLaunch = errors.New("synthetic launch failure")

func (a *blockedLaunchAdapter) Argv(l *agent.Launch) ([]string, error) {
	if a.calls.Add(1) == 1 {
		close(a.entered)
	}
	<-a.release
	if a.continueLaunch {
		return a.Adapter.Argv(l)
	}
	return nil, errBlockedLaunch
}

func TestSessionLaunchDoesNotBlockUnrelatedResume(t *testing.T) {
	for _, operation := range []string{"resume", "fork"} {
		t.Run(operation, func(t *testing.T) {
			h := resumableHarness(t)
			h.writeRollout(taskStarted, `{"type":"event_msg","payload":{"type":"user_message","message":"hello"}}`)
			h.s.bindRollout("original-cli-session", filepath.Join(h.dir, "rollout.jsonl"))
			h.s.meta.Exited = true
			adapter := &blockedLaunchAdapter{Adapter: h.s.ag, entered: make(chan struct{}), release: make(chan struct{})}
			h.s.ag = adapter
			var release sync.Once
			unblock := func() { release.Do(func() { close(adapter.release) }) }
			defer unblock()
			done := make(chan error, 1)
			go func() {
				var err error
				if operation == "resume" {
					_, err = h.d.resume(h.s.meta.ID)
				} else {
					_, err = h.d.fork(h.s.meta.ID, originLocal)
				}
				done <- err
			}()
			select {
			case <-adapter.entered:
			case err := <-done:
				t.Fatalf("launch did not reach the barrier: %v", err)
			}
			// This is also the same-session serialization boundary. Removing
			// lifecycleMu must fail even if the second goroutine starts late.
			if h.s.lifecycleMu.TryLock() {
				h.s.lifecycleMu.Unlock()
				t.Error("launch does not own its source session lifecycle")
			}
			same := make(chan error, 1)
			go func() { same <- h.s.kill() }()

			dir := t.TempDir()
			log, err := eventlog.Open(filepath.Join(dir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			other, err := newSession(h.d, dir, meta{ID: "fedcba9876543210", Cwd: dir, Tmux: "ra-fedcba98", Exited: true, Shell: true}, &fakeTerm{alive: true, screen: idleScreen}, log)
			if err != nil {
				t.Fatal(err)
			}
			h.d.mu.Lock()
			h.d.sessions[other.meta.ID] = other
			h.d.mu.Unlock()
			resumed := make(chan error, 1)
			go func() { _, err := h.d.resume(other.meta.ID); resumed <- err }()
			finished := false
			select {
			case err := <-resumed:
				finished = true
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("blocked session launch prevented an unrelated session from resuming")
			}
			unblock()
			if err := awaitLifecycleResult(t, done); !errors.Is(err, errBlockedLaunch) {
				t.Errorf("blocked launch result: %v", err)
			}
			if err := awaitLifecycleResult(t, same); err != nil {
				t.Error(err)
			}
			// Join even on a failed independence assertion before closing logs.
			if !finished {
				if err := awaitLifecycleResult(t, resumed); err != nil {
					t.Error(err)
				}
			}
			if other.stop != nil {
				other.stop()
				<-other.done
			}
		})
	}
}

func TestConcurrentResumesStartOneGeneration(t *testing.T) {
	h := resumableHarness(t)
	h.s.meta.Exited = true
	adapter := &blockedLaunchAdapter{Adapter: h.s.ag, entered: make(chan struct{}), release: make(chan struct{}), continueLaunch: true}
	h.s.ag = adapter
	a, b := make(chan error, 1), make(chan error, 1)
	go func() { _, err := h.d.resume(h.s.meta.ID); a <- err }()
	<-adapter.entered
	if h.s.lifecycleMu.TryLock() {
		h.s.lifecycleMu.Unlock()
		t.Error("resume released lifecycle ownership before launching")
	}
	go func() { _, err := h.d.resume(h.s.meta.ID); b <- err }()
	close(adapter.release)
	if err := awaitLifecycleResult(t, a); err != nil {
		t.Fatal(err)
	}
	if err := awaitLifecycleResult(t, b); !errors.Is(err, errRunning) {
		t.Errorf("second resume: %v, want already running", err)
	}
	if got := adapter.calls.Load(); got != 1 {
		t.Errorf("concurrent resumes prepared %d launches", got)
	}
	h.s.stop()
	<-h.s.done
}
