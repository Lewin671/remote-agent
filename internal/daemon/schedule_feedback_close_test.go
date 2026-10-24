package daemon

import (
	"context"
	"github.com/Lewin671/remote-agent/internal/tail"
	"testing"
)

func TestScheduledClosureWaitsForReadableTranscript(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	h.s.meta.TaskOwned = true
	h.s.scheduledClose = true
	h.s.reader = tail.NewReader(h.dir, 0) // A directory cannot be consumed as a transcript.
	if err := h.s.killIf(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !h.term.Alive() {
		t.Fatal("closed with unread user records")
	}
}
