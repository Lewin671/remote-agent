package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventlog"
)

func TestRestoreIgnoresInvalidParseDiagnostics(t *testing.T) {
	for _, fault := range []string{"bad-json", "truncated-json", "wrong-type", "unreadable"} {
		t.Run(fault, func(t *testing.T) {
			r := newRestoreHarness(t)
			// Restore really registers and starts the session, but do not run
			// its background injector while checking synchronous controls.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			r.d.ctx = ctx
			r.d.cfg.PollInterval = time.Hour
			id := "00000000000000bb"
			dir := r.save(meta{ID: id, Agent: pb.Agent_AGENT_CODEX}, true)
			q := &queueState{Queue: []input{{RequestID: "existing", Text: "saved", Paused: true}}}
			if err := q.save(dir); err != nil {
				t.Fatal(err)
			}
			log, err := eventlog.Open(filepath.Join(dir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if err := log.Append(id, []*pb.Event{{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "saved history"}}}}, "", 0); err != nil {
				t.Fatal(err)
			}
			log.Close()
			diagnostic := filepath.Join(dir, "parse-health.json")
			body := map[string]string{
				"bad-json":       "PRIVATE_DIAGNOSTIC_BODY",
				"truncated-json": `{"failures":7,"unknown":`,
				"wrong-type":     `{"failures":7,"end":"PRIVATE_DIAGNOSTIC_BODY"}`,
			}[fault]
			if fault == "unreadable" {
				// ReadFile of a directory fails even when tests run as root.
				err = os.Mkdir(diagnostic, 0700)
			} else {
				err = os.WriteFile(diagnostic, []byte(body), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(old)
			if err := r.d.restore(); err != nil {
				t.Fatal(err)
			}
			s := r.session(id)
			if s == nil {
				t.Fatal("valid live session disappeared because its diagnostic file was invalid")
			}
			<-s.done
			defer s.log.Close()
			if s.parseHealth != (parseHealth{}) || s.info().Status == pb.SessionStatus_SESSION_STATUS_EXITED {
				t.Fatalf("diagnostic corruption changed recovery: %+v", s.parseHealth)
			}
			if !strings.Contains(logs.String(), "cannot load parse diagnostics") || strings.Contains(logs.String(), "PRIVATE_DIAGNOSTIC_BODY") {
				t.Fatalf("missing or unsafe local warning: %s", &logs)
			}
			events, _, err := s.log.Since(0)
			if err != nil || len(events) == 0 || events[0].GetUserMessage().GetText() != "saved history" {
				t.Fatalf("valid history lost: %v %v", events, err)
			}
			rpc := &handler{d: r.d}
			if _, err := rpc.Send(context.Background(), connect.NewRequest(&pb.SendRequest{SessionId: id, RequestId: "new", Text: "next", Mode: pb.SendMode_SEND_MODE_QUEUE})); err != nil {
				t.Fatalf("restored session cannot accept input: %v", err)
			}
			if _, err := rpc.CancelInput(context.Background(), connect.NewRequest(&pb.CancelInputRequest{SessionId: id, RequestId: "new"})); err != nil {
				t.Fatalf("restored session cannot cancel input: %v", err)
			}
			disk, err := loadQueue(dir)
			if err != nil || len(disk.Queue) != 1 || disk.Queue[0].RequestID != "existing" || !disk.Queue[0].Paused {
				t.Fatalf("valid queue lost: %+v %v", disk, err)
			}
			if fault == "unreadable" {
				// Repair filesystem access; no automatic removal of directories.
				if err := os.Remove(diagnostic); err != nil {
					t.Fatal(err)
				}
			}
			var raw any
			parseErr := json.Unmarshal([]byte("{bad"), &raw)
			if err := s.noteParseFailure("rollout", 0, 5, []byte("{bad"), parseErr); err != nil {
				t.Fatalf("new diagnostic did not replace invalid file: %v", err)
			}
			h, err := loadParseHealth(dir)
			if err != nil || h.Failures != 1 || h.End != 5 {
				t.Fatalf("replacement diagnostic invalid: %+v %v", h, err)
			}
		})
	}
}
