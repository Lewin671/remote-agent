package daemon

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestNativeUnconfirmedApprovalStaysWithdrawnAcrossHookAndRestart(t *testing.T) {
	h := agyHarness(t)
	h.d.cfg.ApprovalTimeout = time.Hour
	card := nativeCard(t, h)
	hook := *h.s.approvals[card.GetApprovalRequested().ApprovalId].native
	h.term.mu.Lock()
	h.term.failWrites = true
	h.term.mu.Unlock()
	if err := h.s.resolve(card.GetApprovalRequested().ApprovalId, true, ""); !errors.Is(err, agent.ErrDeliveryUnconfirmed) {
		t.Fatal(err)
	}
	assertWithdrawn := func() {
		t.Helper()
		h.s.checkScreen()
		h.s.checkScreen()
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		if len(h.s.approvals) != 0 {
			t.Fatal("unconfirmed request became actionable again")
		}
		candidates := h.s.nativeCandidatesLocked()
		if len(candidates) != 1 || !candidates[0].Uncertain {
			t.Fatal("native candidate lost its uncertainty fence")
		}
	}
	assertWithdrawn()
	h.s.rememberNative(&hook)
	assertWithdrawn()
	m := h.s.meta
	if err := h.s.log.Close(); err != nil {
		t.Fatal(err)
	}
	h.open(m)
	h.s.identityPending = false
	assertWithdrawn()
	h.s.rememberNative(&hook)
	assertWithdrawn()
}

func TestNativeDenialFeedbackReleasesControlBeforeQueueing(t *testing.T) {
	h := agyHarness(t)
	h.d.cfg.ApprovalTimeout = time.Hour
	card := nativeCard(t, h)
	entered, proceed := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(proceed) })
	h.term.onWrite = func(term *fakeTerm, keys string) {
		if keys == "4" {
			close(entered)
			<-proceed
			term.screen = agyScreen(t, "idle-empty-box.txt")
		}
	}
	resolved := make(chan error, 1)
	go func() { resolved <- h.s.resolve(card.GetApprovalRequested().ApprovalId, false, "use another command") }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("denial did not reach native key delivery")
	}
	// A concurrent stop owns lifecycleMu before waiting for terminal control.
	h.s.lifecycleMu.Lock()
	control := make(chan struct{})
	go func() { h.s.ctlMu.Lock(); h.s.ctlMu.Unlock(); close(control) }()
	release.Do(func() { close(proceed) })
	select {
	case <-control:
		h.s.lifecycleMu.Unlock()
	case <-time.After(3 * time.Second):
		h.s.lifecycleMu.Unlock()
		<-control
		<-resolved
		t.Fatal("feedback held terminal control while waiting for lifecycle ownership")
	}
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
	if len(h.s.queue.Queue) != 1 || h.s.queue.Queue[0].Text != "use another command" {
		t.Fatal("denial feedback was not queued after releasing control")
	}
}
