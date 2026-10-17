package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/paths"
	"google.golang.org/protobuf/proto"
)

func TestDiagnosticsDoNotChangeClientMetadata(t *testing.T) {
	for _, detail := range []string{"", "trust folder", "stopped"} {
		t.Run(detail, func(t *testing.T) {
			h := newHarness(t, Config{})
			h.s.detail = detail
			if detail == "stopped" {
				h.s.status = pb.SessionStatus_SESSION_STATUS_EXITED
			}
			before := h.s.info()
			h.s.parseHealth = parseHealth{Failures: 4, Unknown: 7, Start: 10, End: 20, Type: "new_type", Category: "json_type"}
			h.s.noteStorageLocked(errors.New("disk full"))
			after := h.s.info()
			if !proto.Equal(before, after) || after.StatusDetail != detail {
				t.Fatalf("metadata changed: before=%v after=%v", before, after)
			}
		})
	}
}

func TestLocalStorageDiagnosticLifecycle(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.log.Close()
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Detail: "stopped"}}}); err == nil {
		t.Fatal("append succeeded")
	}
	detail, err := ReadSessionDiagnostics(paths.Dir(), h.s.meta.ID)
	if err != nil || !strings.Contains(detail, "storage failure") {
		t.Fatalf("local storage failure invisible: %q %v", detail, err)
	}
	if h.s.info().StatusDetail != "" {
		t.Fatal("failed state or diagnostic projected")
	}
	h.s.noteStorageLocked(nil)
	detail, err = ReadSessionDiagnostics(paths.Dir(), h.s.meta.ID)
	if err != nil || detail != "" {
		t.Fatalf("resolved diagnostic not cleared: %q %v", detail, err)
	}
}

type diagnosticLogs struct {
	mu       sync.Mutex
	messages []string
}

func (h *diagnosticLogs) Enabled(context.Context, slog.Level) bool { return true }
func (h *diagnosticLogs) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, r.Message)
	return nil
}
func (h *diagnosticLogs) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *diagnosticLogs) WithGroup(string) slog.Handler      { return h }
func (h *diagnosticLogs) count() int                         { h.mu.Lock(); defer h.mu.Unlock(); return len(h.messages) }

func TestUnknownRecordsAreQuietLocalDiagnostics(t *testing.T) {
	h := newHarness(t, Config{})
	// Use the production layout so the local command's reader sees the file.
	h.s.dir = paths.SessionDir(h.s.meta.ID)
	if err := os.MkdirAll(h.s.dir, 0700); err != nil {
		t.Fatal(err)
	}
	sink := &diagnosticLogs{}
	old := slog.Default()
	slog.SetDefault(slog.New(sink))
	defer slog.SetDefault(old)
	_, changed, err := h.s.log.Since(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.noteParseFailure("rollout", 0, 10, []byte(`{"type":"future_type"}`), agent.ErrUnknownRecord); err != nil {
		t.Fatal(err)
	}
	if h.s.parseHealth.Failures != 0 || h.s.parseHealth.Unknown != 1 || h.s.storageFault != "" || sink.count() != 0 {
		t.Fatal("unknown type treated as a fault")
	}
	select {
	case <-changed:
		t.Fatal("diagnostic poked client synchronization")
	default:
	}
	detail, err := ReadSessionDiagnostics(paths.Dir(), h.s.meta.ID)
	if err != nil || !strings.Contains(detail, "unknown records=1") || strings.Contains(detail, "failure") {
		t.Fatalf("diagnostic %q %v", detail, err)
	}
}

func TestLocalParseFailuresAndLogsAreBounded(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.dir = paths.SessionDir(h.s.meta.ID)
	os.MkdirAll(h.s.dir, 0700)
	sink := &diagnosticLogs{}
	old := slog.Default()
	slog.SetDefault(slog.New(sink))
	defer slog.SetDefault(old)
	for i := int64(1); i <= 3; i++ {
		if err := h.s.noteParseFailure("rollout", i-1, i, []byte(`{"type":"user","text":"PRIVATE_BODY"}`), errors.New("PRIVATE_BODY")); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := ReadSessionDiagnostics(paths.Dir(), h.s.meta.ID)
	if err != nil || !strings.Contains(detail, "parse failures=3") || strings.Contains(detail, "PRIVATE_BODY") || sink.count() != 1 {
		t.Fatalf("diagnostic=%q logs=%d err=%v", detail, sink.count(), err)
	}
}

func TestStorageDiagnosticWriteFailureKeepsRateLimitedLog(t *testing.T) {
	h := newHarness(t, Config{})
	os.WriteFile(filepath.Join(paths.Dir(), "diagnostics"), []byte("blocked"), 0600)
	sink := &diagnosticLogs{}
	old := slog.Default()
	slog.SetDefault(slog.New(sink))
	defer slog.SetDefault(old)
	for i := 0; i < 3; i++ {
		h.s.noteStorageLocked(errors.New("disk full"))
	}
	if h.s.storageFault == "" || sink.count() != 1 {
		t.Fatal("storage fault lost or log flooded")
	}
	if h.s.info().StatusDetail != "" {
		t.Fatal("storage fault leaked to clients")
	}
}

func TestLocalDiagnosticsRejectTraversal(t *testing.T) {
	if _, err := ReadSessionDiagnostics(t.TempDir(), "../other"); err == nil {
		t.Fatal("unsafe session ID accepted")
	}
}

func TestNewSessionClearsPreviousRuntimeStorageFault(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.noteStorageLocked(errors.New("disk full"))
	m := h.s.meta
	h.s.log.Close()
	h.open(m)
	detail, err := ReadSessionDiagnostics(paths.Dir(), m.ID)
	if err != nil || detail != "" {
		t.Fatalf("previous runtime fault survives healthy restore: %q %v", detail, err)
	}
}

func TestLocalDiagnosticsReportUnreadableFile(t *testing.T) {
	root := t.TempDir()
	id := "test"
	dir := filepath.Join(root, "sessions", id)
	os.MkdirAll(dir, 0700)
	b, _ := json.Marshal(parseHealth{Failures: 1, End: 20, Type: "bad", Category: "json_type"})
	os.WriteFile(filepath.Join(dir, "parse-health.json"), b, 0600)
	os.MkdirAll(filepath.Join(root, "diagnostics"), 0700)
	os.WriteFile(storageDiagnosticPath(root, id), []byte("bad json"), 0600)
	detail, err := ReadSessionDiagnostics(root, id)
	if err == nil || !strings.Contains(detail, "parse failures=1") {
		t.Fatalf("partial diagnostic lost: %q %v", detail, err)
	}
}
