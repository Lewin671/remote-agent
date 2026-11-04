package daemon

import (
	"sync"
	"testing"
	"time"
)

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
