package daemon

import (
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHumanAdmissionBoundsRunningResumeRetry(t *testing.T) {
	h := resumableHarness(t)
	defer h.s.log.Close()
	calls := fakeTmux(t, h)
	bin := h.d.cfg.Tmux.Bin
	script, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(bin, []byte(strings.ReplaceAll(string(script), "has-session) exit 1", "has-session) exit 0")), 0700); err != nil {
		t.Fatal(err)
	}
	h.s.meta.Exited = true
	h.s.meta.ScheduledTaskID = "task"
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = h.s.enqueueInputContext(ctx, "human", "human", nil, originLocal, pb.SendMode_SEND_MODE_QUEUE, nil, nil)
	if !errors.Is(err, errRunning) {
		t.Fatalf("unbounded admission retry: %v", err)
	}
	if got := strings.Count(tmuxCalls(t, calls), "has-session"); got != 2 {
		t.Fatalf("resume attempts = %d, want 2", got)
	}
}
