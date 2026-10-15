package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/updater"
)

func updateDaemon(t *testing.T) (*Daemon, *session) {
	t.Helper()
	s := &session{queue: &queueState{}, status: pb.SessionStatus_SESSION_STATUS_IDLE}
	d := &Daemon{sessions: map[string]*session{"s": s}, uploads: &uploads{dir: t.TempDir()}}
	d.cfg.Exe = "/test/remote-agent"
	return d, s
}

func TestUpdateReadinessIncludesInternalPendingWork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*session)
	}{
		{"turn", func(s *session) { s.running = true }},
		{"starting", func(s *session) { s.status = pb.SessionStatus_SESSION_STATUS_STARTING }},
		{"control", func(s *session) { s.driving = true }},
		{"approval", func(s *session) { s.approvals = map[string]*approval{"a": {}} }},
		{"question", func(s *session) { s.questions = map[string]*pb.QuestionAsked{"q": {}} }},
		{"plan", func(s *session) { s.planOpen = true }},
		{"queued", func(s *session) { s.queue.Queue = []input{{RequestID: "q"}} }},
		{"held", func(s *session) { s.queue.Held = []input{{RequestID: "held"}} }},
		{"unconfirmed", func(s *session) { s.unproven = map[string]bool{"late": true} }},
		{"unmatched", func(s *session) { s.recent = []input{{RequestID: "recent"}} }},
		{"lostQueue", func(s *session) { s.queueLost = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, s := updateDaemon(t)
			if reason := d.updateBusyReason(); reason != "" {
				t.Fatal(reason)
			}
			tc.change(s)
			if reason := d.updateBusyReason(); reason == "" {
				t.Fatal("pending work did not block update")
			}
		})
	}
}

func TestUpdateHealthAndQuietWindow(t *testing.T) {
	d, s := updateDaemon(t)
	_, since := d.observeUpdateIdle()
	if since.IsZero() {
		t.Fatal("idle window not started")
	}
	s.running = true
	_, since = d.observeUpdateIdle()
	if !since.IsZero() {
		t.Fatal("activity did not reset idle window")
	}
	s.running = false
	w := httptest.NewRecorder()
	d.updateHealth(w, httptest.NewRequest(http.MethodGet, "/update/health", nil))
	var h updater.Health
	if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	if h.Format != 1 || h.PID != os.Getpid() || h.Executable != d.cfg.Exe || h.Busy || h.IdleSince.IsZero() {
		t.Fatalf("%+v", h)
	}
	w = httptest.NewRecorder()
	d.updateHealth(w, httptest.NewRequest(http.MethodPost, "/update/health", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal(w.Code)
	}
}

func TestPendingImageBlocksUpdate(t *testing.T) {
	d, _ := updateDaemon(t)
	dir := filepath.Join(d.uploads.dir, "partial")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	tr := transfer{UpdatedMs: time.Now().UnixMilli()}
	b, _ := json.Marshal(tr)
	if err := os.WriteFile(filepath.Join(dir, "image.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if d.updateBusyReason() == "" {
		t.Fatal("upload did not block update")
	}
	tr.UpdatedMs = time.Now().Add(-2 * partialTTL).UnixMilli()
	b, _ = json.Marshal(tr)
	if err := os.WriteFile(filepath.Join(dir, "image.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if reason := d.updateBusyReason(); reason != "" {
		t.Fatal(reason)
	}
}
