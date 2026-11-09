package daemon

import (
	"context"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// activityTerm is a terminal with a client that typed at a known time.
type activityTerm struct {
	*fakeTerm
	typed time.Time
}

func (a *activityTerm) ClientActivity(context.Context) (time.Time, error) { return a.typed, nil }

func idleHarness(t *testing.T) *harness {
	t.Helper()
	h := scheduleHarness(t)
	t.Cleanup(func() { h.s.log.Close() })
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Status: pb.SessionStatus_SESSION_STATUS_IDLE}}}); err != nil {
		t.Fatal(err)
	}
	return h
}

// age makes the session look idle since d ago.
func age(h *harness, d time.Duration) {
	h.s.mu.Lock()
	h.s.activeAt = time.Now().Add(-d)
	h.s.mu.Unlock()
}

func TestIdleCloseClosesIdleSessionAndRecordsCause(t *testing.T) {
	h := idleHarness(t)
	age(h, 7*time.Hour)
	got := h.d.idleClosures(context.Background())
	if len(got) != 1 || got[0] != h.s {
		t.Fatalf("closures=%v", got)
	}
	if err := h.s.killFor(context.Background(), exitIdle, h.d.idleCloseAfter()); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		events := h.events()
		state := events[len(events)-1].GetSessionState()
		if state.GetStatus() != pb.SessionStatus_SESSION_STATUS_EXITED || state.GetDetail() != "stopped" || !state.GetIdleClosed() || state.GetScheduledClosed() {
			t.Fatalf("exit=%v", state)
		}
	}
	check()
	if m, err := loadMeta(h.s.dir); err != nil || !m.IdleClosed || !m.Exited {
		t.Fatalf("meta=%+v err=%v", m, err)
	}
	h.reopen()
	check()
	if !h.s.idleClosed {
		t.Fatal("replay lost exit cause")
	}
}

func TestIdleCloseRecoversMetadataBeforeExitEvent(t *testing.T) {
	h := idleHarness(t)
	h.s.meta.Exited, h.s.meta.IdleClosed = true, true
	if err := h.s.meta.save(h.s.dir); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	events := h.events()
	if state := events[len(events)-1].GetSessionState(); !state.GetIdleClosed() || state.GetStatus() != pb.SessionStatus_SESSION_STATUS_EXITED {
		t.Fatalf("recovered=%v", state)
	}
}

func TestIdleCloseKeepsSessionsInUse(t *testing.T) {
	for name, set := range map[string]func(h *harness){
		"recent":     func(h *harness) { h.s.activeAt = time.Now().Add(-5 * time.Hour) },
		"running":    func(h *harness) { h.s.running = true },
		"approval":   func(h *harness) { h.s.approvals["a"] = &approval{} },
		"queued":     func(h *harness) { h.s.queue.Queue = append(h.s.queue.Queue, input{RequestID: "r"}) },
		"background": func(h *harness) { h.s.backgroundPossible = true },
		"task owned": func(h *harness) { h.s.meta.TaskOwned = true },
		"typed":      func(h *harness) { h.s.term = &activityTerm{fakeTerm: h.term, typed: time.Now().Add(-time.Hour)} },
	} {
		t.Run(name, func(t *testing.T) {
			h := idleHarness(t)
			age(h, 7*time.Hour)
			h.s.mu.Lock()
			set(h)
			h.s.mu.Unlock()
			if got := h.d.idleClosures(context.Background()); len(got) != 0 {
				t.Fatalf("closed a session in use")
			}
			// The closer checks again: one picked before the session got
			// busy leaves it open.
			if err := h.s.killFor(context.Background(), exitIdle, 6*time.Hour); err != nil {
				t.Fatal(err)
			}
			if h.s.meta.Exited {
				t.Fatal("killed a session in use")
			}
		})
	}
}

func TestIdleCloseActivityRestartsClock(t *testing.T) {
	h := idleHarness(t)
	age(h, 7*time.Hour)
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_TokenUsage{TokenUsage: &pb.TokenUsage{}}}); err != nil {
		t.Fatal(err)
	}
	if len(h.d.idleClosures(context.Background())) != 1 {
		t.Fatal("a usage update counted as activity")
	}
	if err := h.s.emit(&pb.Event{TurnId: "t", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}},
		&pb.Event{TurnId: "t", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}}); err != nil {
		t.Fatal(err)
	}
	if len(h.d.idleClosures(context.Background())) != 0 {
		t.Fatal("a turn did not restart the idle clock")
	}
}

func TestIdleCloseSetting(t *testing.T) {
	h := idleHarness(t)
	if got := h.d.settings().IdleCloseMinutes; got != defaultIdleCloseMinutes {
		t.Fatalf("default=%d", got)
	}
	if _, err := h.d.updateSettings(&pb.DaemonSettings{IdleCloseMinutes: 30}); err == nil {
		t.Fatal("accepted 30 minutes")
	}
	st, err := h.d.updateSettings(&pb.DaemonSettings{IdleCloseMinutes: -5})
	if err != nil || st.IdleCloseMinutes != -1 {
		t.Fatalf("off=%v err=%v", st, err)
	}
	age(h, 30*24*time.Hour)
	if h.d.idleCloseAfter() != 0 || len(h.d.idleClosures(context.Background())) != 0 {
		t.Fatal("closed with the setting off")
	}
	if err := h.s.killFor(context.Background(), exitIdle, h.d.idleCloseAfter()); err != nil || h.s.meta.Exited {
		t.Fatalf("closer ignored the setting: %v", err)
	}
	if st, err = h.d.updateSettings(&pb.DaemonSettings{IdleCloseMinutes: 120}); err != nil || st.IdleCloseMinutes != 120 || h.d.idleCloseAfter() != 2*time.Hour {
		t.Fatalf("2h=%v err=%v", st, err)
	}
}
