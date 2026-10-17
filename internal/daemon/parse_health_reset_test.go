package daemon

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/tail"
)

func parseResetHarness(t *testing.T) (*harness, string) {
	t.Helper()
	h := newHarness(t, Config{})
	path := filepath.Join(h.dir, "rollout.jsonl")
	old := `{"type":"past_schema","padding":"` + strings.Repeat("x", 1024) + `"}` + "\n"
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	h.s.meta.RolloutPath = path
	h.s.reader = tail.NewReader(path, 0)
	if !h.s.readRollout() || h.s.parseHealth.Unknown != 1 {
		t.Fatal("old source diagnostic not recorded")
	}
	t.Cleanup(func() { h.s.log.Close() })
	return h, path
}

func TestParseWatermarkResetsWithTranscript(t *testing.T) {
	for _, kind := range []string{"truncate", "replace-shorter", "replace-equal", "replace-longer"} {
		t.Run(kind, func(t *testing.T) {
			h, path := parseResetHarness(t)
			oldEnd := h.s.parseHealth.End
			bad := `{"type":"event_msg","payload":{"type":"task_complete","duration_ms":"PRIVATE_BODY"}}` + "\n"
			body := bad
			if kind == "replace-equal" || kind == "replace-longer" {
				target := int(oldEnd)
				if kind == "replace-longer" {
					target += 512
				}
				body += strings.Repeat(" ", target-len(bad)-1) + "\n"
			}
			var logs bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(old)
			if kind == "truncate" {
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path+".new", []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+".new", path); err != nil {
					t.Fatal(err)
				}
			}
			if !h.s.readRollout() {
				t.Fatal("new transcript could not be read")
			}
			want := h.s.parseHealth
			if want.Failures != 1 || want.Unknown != 1 || want.Start != 0 || want.End != int64(len(bad)) || want.End >= oldEnd || want.Type != "event_msg/task_complete" || want.Category != "json_type" {
				t.Fatalf("new short failure suppressed by old watermark: %+v", want)
			}
			if strings.Count(logs.String(), "CLI parse failure") != 1 || strings.Contains(logs.String(), "PRIVATE_BODY") {
				t.Fatalf("new failure warning absent or unsafe: %s", &logs)
			}
			disk, err := loadParseHealth(h.dir)
			if err != nil || disk != want {
				t.Fatalf("new diagnostic not durable: %+v %v", disk, err)
			}
			// No event followed the bad line. Reopening loses the event log's
			// in-memory checkpoint and genuinely replays the same bytes.
			m := h.s.meta
			h.s.log.Close()
			h.open(m)
			if !h.s.readRollout() || h.s.parseHealth != want || strings.Count(logs.String(), "CLI parse failure") != 1 {
				t.Fatalf("same new source double counted after replay: %+v", h.s.parseHealth)
			}
		})
	}
}

func TestEmptyTranscriptResetSurvivesRestart(t *testing.T) {
	h, path := parseResetHarness(t)
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if !h.s.readRollout() {
		t.Fatal("empty truncated source blocked")
	}
	disk, err := loadParseHealth(h.dir)
	if err != nil || disk.Path != "" || disk.Unknown != 1 {
		t.Fatalf("watermark invalidation was not saved without a new bad line: %+v %v", disk, err)
	}
	m := h.s.meta
	h.s.log.Close()
	h.open(m)
	if err := os.WriteFile(path, []byte("{bad\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !h.s.readRollout() || h.s.parseHealth.Failures != 1 || h.s.parseHealth.Unknown != 1 || h.s.parseHealth.End != 5 {
		t.Fatalf("restart restored obsolete source watermark: %+v", h.s.parseHealth)
	}
}

func TestResetReplayIgnoresCheckpointFromPreviousSource(t *testing.T) {
	h := newHarness(t, Config{})
	t.Cleanup(func() { h.s.log.Close() })
	path := filepath.Join(h.dir, "rollout.jsonl")
	// Unlike an all-diagnostic source, this leaves a durable event checkpoint
	// beyond the replacement's length. Its later diagnostic has no event.
	old := `{"type":"event_msg","payload":{"type":"task_started","turn_id":"t"}}` + strings.Repeat(" ", 512) + "\n" + `{"type":"past_schema"}` + "\n"
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	h.s.meta.RolloutPath = path
	h.s.reader = tail.NewReader(path, 0)
	if !h.s.readRollout() || h.s.parseHealth.Unknown != 1 {
		t.Fatal("old transcript not committed")
	}
	if err := os.WriteFile(path, []byte("{bad\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !h.s.readRollout() || h.s.parseHealth.Failures != 1 {
		t.Fatal("new short diagnostic not counted")
	}
	want, m := h.s.parseHealth, h.s.meta
	// An unrelated daemon event advances LastSeq, but does not make a
	// checkpoint for the replacement transcript durable.
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Status: pb.SessionStatus_SESSION_STATUS_IDLE}}}); err != nil {
		t.Fatal(err)
	}
	h.s.log.Close()
	h.open(m)
	if !h.s.readRollout() || h.s.parseHealth != want {
		t.Fatalf("obsolete durable checkpoint double counted new source: want=%+v got=%+v", want, h.s.parseHealth)
	}
	// Once a real event commits, its new checkpoint is authoritative again.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"new"}}` + "\n")
	f.Close()
	if err != nil || !h.s.readRollout() {
		t.Fatalf("new event did not commit: %v", err)
	}
	_, off := h.s.log.Checkpoint()
	h.s.log.Close()
	h.open(m)
	if h.s.reader.Offset() != off || !h.s.readRollout() || h.s.parseHealth != want {
		t.Fatal("new source checkpoint not retained or diagnostics double counted")
	}
}

func TestTranscriptResetRetriesDiagnosticSaveBeforeReading(t *testing.T) {
	h, path := parseResetHarness(t)
	want := h.s.parseHealth
	diagnostic := filepath.Join(h.dir, "parse-health.json")
	if err := os.Rename(diagnostic, diagnostic+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(diagnostic, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{bad\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if h.s.readRollout() || h.s.parseHealth != want || !h.s.parseHealthResetPending || h.s.reader.Offset() != 0 {
		t.Fatal("failed watermark reset consumed new source or lost pending invalidation")
	}
	// The file can grow beyond the old watermark before storage is fixed.
	// Retrying must remember the reset rather than rely on size shrinking again.
	body := "{bad\n" + strings.Repeat(" ", int(want.End)+512) + "\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(diagnostic); err != nil {
		t.Fatal(err)
	}
	if !h.s.readRollout() || h.s.parseHealthResetPending || h.s.parseHealth.Failures != 1 || h.s.parseHealth.Unknown != 1 || h.s.parseHealth.End != 5 {
		t.Fatalf("new source lost after diagnostic storage repair: %+v", h.s.parseHealth)
	}
	want = h.s.parseHealth
	h.s.reader = tail.NewReader(path, 0)
	if !h.s.readRollout() || h.s.parseHealth != want {
		t.Fatal("retry/replay counted the new failure twice")
	}
}
