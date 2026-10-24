package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/tail"
)

func TestRemoteApprovalSurvivesTranscriptReadFault(t *testing.T) {
	h := claudeHarness(t, Config{ApprovalTimeout: 5 * time.Second})
	defer h.s.log.Close()
	h.s.reader = tail.NewReader(h.dir, 0)
	type reply struct {
		data []byte
		err  error
	}
	done := make(chan reply, 1)
	go func() {
		b, err := h.s.hook(context.Background(), claudePermission("Bash", `{"command":"true"}`))
		done <- reply{b, err}
	}()
	eventually(t, "remote approval despite transcript read failure", func() bool {
		select {
		case r := <-done:
			t.Fatalf("hook returned before remote approval: %s %v", r.data, r.err)
		default:
		}
		return h.s.info().PendingApprovals == 1
	})
	h.s.mu.Lock()
	id := ""
	for key := range h.s.approvals {
		id = key
	}
	h.s.mu.Unlock()
	if err := h.s.resolve(id, true, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || !strings.Contains(string(r.data), `"behavior":"allow"`) {
			t.Fatalf("remote approval lost: %s %v", r.data, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("remote approval did not release hook")
	}
}
