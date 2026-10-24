package daemon

import (
	"testing"

	"github.com/Lewin671/remote-agent/internal/tail"
)

func TestInterruptSurvivesTranscriptFault(t *testing.T) {
	for _, fault := range []string{"read", "commit"} {
		t.Run(fault, func(t *testing.T) {
			h := scheduleTranscriptHarness(t, true)
			defer h.s.log.Close()
			writeScheduledTranscriptInput(t, h, "running", true)
			if !h.s.readRollout() {
				t.Fatal("could not establish running state")
			}
			writeTranscriptEnd(h, "running", true)
			if fault == "read" {
				h.s.reader = tail.NewReader(h.dir, 0)
			} else if err := h.s.log.Close(); err != nil {
				t.Fatal(err)
			}
			interrupted, err := h.s.interruptBy(originLocal)
			if err != nil || !interrupted {
				t.Fatalf("storage fault disabled interrupt: interrupted=%v err=%v", interrupted, err)
			}
			if len(h.term.written()) != 1 {
				t.Fatal("interrupt did not send Esc exactly once")
			}
		})
	}
}
