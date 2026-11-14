package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/eventview"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/Lewin671/remote-agent/internal/tail"
)

type outcomeAuditBackend struct{ interruptedModelBackend }

func (b outcomeAuditBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return b.Daemon.Local()
}

// Each failure travels through the real server/WS/handler/acceptance log,
// including a same-ID retry. The terminal and its catalogue are isolated fakes.
func TestV072DeterministicFailuresStayFinal(t *testing.T) {
	// This matrix changes process state and executes fresh CLI/tmux scripts; keep it serial.
	for _, name := range []string{"exited-send", "empty-send", "missing-session", "input-capacity", "attachment-capacity", "invalid-create", "invalid-settings", "invalid-model", "invalid-effort", "invalid-mode", "long-title", "invalid-schedule", "missing-upload", "missing-shared-file", "invalid-shared-offset", "invalid-question", "resume-directory", "resume-cli", "resume-env", "auto-directory", "auto-cli", "auto-env", "fork-directory", "fork-cli", "fork-env", "create-directory", "create-cli", "resume-cached-cli", "auto-cached-cli", "fork-cached-cli", "create-cached-cli", "stopped-interrupt", "stopped-compact", "missing-cancel", "missing-stop", "truncated-question", "queue-disk-full", "queue-storage-exhausted", "queue-storage-unverified"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
			h, _ := controlHarness(t)
			h.d.sessions[h.s.meta.ID] = h.s
			sid := h.s.meta.ID
			headerSession := sid
			var cmd *pb.Command
			want := "failed_precondition"
			switch name {
			case "queue-disk-full", "queue-storage-exhausted", "queue-storage-unverified":
				want = "storage_exhausted"
				h.d.diskVolume = func() (*pb.DiskStatus, error) { return &pb.DiskStatus{AvailableBytes: 50e9}, nil }
				if name == "queue-disk-full" {
					want = "disk_full"
					h.d.diskVolume = func() (*pb.DiskStatus, error) { return &pb.DiskStatus{AvailableBytes: 1}, nil }
				}
				if name == "queue-storage-unverified" {
					h.d.diskVolume = func() (*pb.DiskStatus, error) { return nil, os.ErrPermission }
				}
				h.d.uploads.beforeUploadStep = func(step string) error {
					if step == "queue-commit" {
						return syscall.ENOSPC
					}
					return nil
				}
				cmd = &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid, RequestId: "disk-fixture", Text: "fixture"}}}
			case "exited-send":
				h.s.meta.Exited = true
				cmd = &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid, Text: "test"}}}
			case "empty-send":
				want = "invalid_argument"
				cmd = &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid}}}
			case "missing-session":
				want = "invalid_argument"
				cmd = &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "missing", Text: "test"}}}
			case "input-capacity":
				want = "resource_exhausted"
				h.s.queue.Queue = make([]input, maxPendingInputs)
				cmd = &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid, Text: "test"}}}
			case "attachment-capacity":
				headerSession = ""
				want = "resource_exhausted"
				h.d.uploads.limits = func() int64 { return 1 }
				cmd = &pb.Command{Kind: &pb.Command_BeginImageUpload{BeginImageUpload: &pb.BeginImageUploadRequest{TransferId: strings.Repeat("a", 32), FileName: "test.bin", TotalBytes: 8, Sha256: make([]byte, 32)}}}
			case "invalid-create":
				headerSession = ""
				want = "invalid_argument"
				cmd = &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{}}}
			case "invalid-settings":
				headerSession = ""
				want = "invalid_argument"
				cmd = &pb.Command{Kind: &pb.Command_UpdateSettings{UpdateSettings: &pb.UpdateSettingsRequest{Settings: &pb.DaemonSettings{ImageUploadQuotaBytes: 1}}}}
			case "invalid-model":
				want = "not_found"
				cmd = &pb.Command{Kind: &pb.Command_SetModel{SetModel: &pb.SetModelRequest{SessionId: sid, Model: "missing-model", Effort: "high"}}}
			case "invalid-effort":
				want = "unknown"
				cmd = &pb.Command{Kind: &pb.Command_SetModel{SetModel: &pb.SetModelRequest{SessionId: sid, Model: "gpt-6-luna", Effort: "missing-effort"}}}
			case "invalid-mode":
				want = "unknown"
				cmd = &pb.Command{Kind: &pb.Command_SetApprovalMode{SetApprovalMode: &pb.SetApprovalModeRequest{SessionId: sid, Mode: 99}}}
			case "long-title":
				want = "invalid_argument"
				cmd = &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: sid, Title: strings.Repeat("x", renameMax+1)}}}
			case "invalid-schedule":
				headerSession = ""
				want = "unknown"
				cmd = &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "create", SpecJson: []byte("{")}}}
			case "missing-upload":
				headerSession = ""
				want = "not_found"
				cmd = &pb.Command{Kind: &pb.Command_CompleteImageUpload{CompleteImageUpload: &pb.CompleteImageUploadRequest{TransferId: strings.Repeat("b", 32)}}}
			case "missing-shared-file":
				want = "not_found"
				cmd = &pb.Command{Kind: &pb.Command_GetSharedFileChunk{GetSharedFileChunk: &pb.GetSharedFileChunkRequest{SessionId: sid, FileId: strings.Repeat("b", 32)}}}
			case "invalid-shared-offset":
				want = "invalid_argument"
				path := filepath.Join(h.dir, "test.bin")
				writeTestFile(t, path, []byte("content"))
				f, err := h.d.sendFile(context.Background(), sid, sid, path)
				if err != nil {
					t.Fatal(err)
				}
				cmd = &pb.Command{Kind: &pb.Command_GetSharedFileChunk{GetSharedFileChunk: &pb.GetSharedFileChunkRequest{SessionId: sid, FileId: f.FileId, Offset: 1}}}
			case "resume-directory", "resume-cli", "resume-env", "auto-directory", "auto-cli", "auto-env", "fork-directory", "fork-cli", "fork-env", "create-directory", "create-cli", "resume-cached-cli", "auto-cached-cli", "fork-cached-cli", "create-cached-cli":
				want = "unknown"
				if strings.HasPrefix(name, "resume") || strings.HasPrefix(name, "auto") {
					h.s.meta.Exited = true
				}
				h.s.meta.Shell = false
				if strings.HasSuffix(name, "directory") {
					h.s.meta.Cwd = filepath.Join(t.TempDir(), "gone")
				}
				if strings.HasSuffix(name, "cached-cli") {
					h.s.ag.(interface{ SetBin(string) }).SetBin(filepath.Join(t.TempDir(), "removed-cli"))
				} else if strings.HasSuffix(name, "cli") {
					h.s.ag.(interface{ SetBin(string) }).SetBin("")
				} else {
					h.s.ag.(interface{ SetBin(string) }).SetBin("/usr/bin/true")
				}
				switch {
				case strings.HasPrefix(name, "auto"):
					h.s.meta.IdleClosed = true
					cmd = &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid, Text: "test"}}}
				case strings.HasPrefix(name, "resume"):
					cmd = &pb.Command{Kind: &pb.Command_ResumeSession{ResumeSession: &pb.ResumeSessionRequest{SessionId: sid}}}
				case strings.HasPrefix(name, "fork"):
					if strings.HasSuffix(name, "env") {
						h.s.meta.Exited = true
						h.s.meta.RolloutPath = filepath.Join(h.dir, "rollout.jsonl")
						h.s.meta.CodexSessionID = "source"
						h.writeRollout(`{"type":"event_msg","payload":{"type":"user_message","message":"first"}}`)
						h.s.bindRollout("source", filepath.Join(h.dir, "rollout.jsonl"))
						h.s.readRollout()
						info, err := os.Stat(filepath.Join(h.dir, "rollout.jsonl"))
						if err != nil {
							t.Fatal(err)
						}
						h.s.reader = tail.NewReader(filepath.Join(h.dir, "rollout.jsonl"), info.Size())
						h.s.identityPending = false
						h.s.mu.Lock()
						h.s.status = pb.SessionStatus_SESSION_STATUS_IDLE
						h.s.running = false
						h.s.planOpen = false
						h.s.mu.Unlock()
					}
					cmd = &pb.Command{Kind: &pb.Command_ForkSession{ForkSession: &pb.ForkSessionRequest{SessionId: sid}}}
				default:
					headerSession = ""
					want = "invalid_argument"
					cmd = &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: h.s.meta.Cwd}}}
				}
			case "stopped-interrupt":
				h.s.meta.Exited = true
				cmd = &pb.Command{Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: sid}}}
			case "stopped-compact":
				h.s.meta.Exited = true
				cmd = &pb.Command{Kind: &pb.Command_Compact{Compact: &pb.CompactRequest{SessionId: sid}}}
			case "missing-cancel":
				want = "not_found"
				cmd = &pb.Command{Kind: &pb.Command_CancelInput{CancelInput: &pb.CancelInputRequest{SessionId: sid, RequestId: "missing"}}}
			case "missing-stop":
				want = "invalid_argument"
				cmd = &pb.Command{Kind: &pb.Command_StopSession{StopSession: &pb.StopSessionRequest{SessionId: "missing"}}}
			case "truncated-question":
				cmd = &pb.Command{Kind: &pb.Command_AnswerQuestion{AnswerQuestion: &pb.AnswerQuestionRequest{SessionId: sid, QuestionId: "q" + eventview.SkeletonNote, Options: []int32{1}}}}
			case "invalid-question":
				want = "unknown"
				h.s.questions = map[string]*pb.QuestionAsked{"q": {}}
				cmd = &pb.Command{Kind: &pb.Command_AnswerQuestion{AnswerQuestion: &pb.AnswerQuestionRequest{SessionId: sid, QuestionId: "q"}}}
			}
			client, machine := startCancellationTestRelay(t, outcomeAuditBackend{interruptedModelBackend{Daemon: h.d}})
			env, err := client.Seal(machine, headerSession, remoteclient.NewRequestID(), time.Now().Add(time.Minute), cmd)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				_, err = client.Forward(context.Background(), machine, env)
				var ce *remoteclient.CommandError
				if !errors.As(err, &ce) || !ce.FromDaemon || ce.Code != want || remoteclient.Uncertain(err) {
					t.Fatalf("want authenticated final %s, got %v", want, err)
				}
			}
			if keys := h.term.written(); len(keys) != 0 {
				t.Fatalf("preflight injected %d keys", len(keys))
			}
		})
	}
}

func TestSharedFileEvidenceAtEveryDecision(t *testing.T) {
	h := newSendHarness(t)
	for _, raw := range []string{"", "missing.bin", h.outside} {
		_, err := h.send(raw)
		if err == nil || !outcome.IsFinalError(err) {
			t.Fatalf("path decision not final: %v", err)
		}
	}
	req := connect.NewRequest(&pb.SendFileRequest{SessionId: h.id(), Path: "missing.bin"})
	req.Header().Set(schedule.MCPScopeHeader, h.id())
	_, err := (&handler{d: h.d}).SendFile(context.Background(), req)
	if !outcome.IsFinalError(err) || connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
	req.Header().Del(schedule.MCPScopeHeader)
	_, err = (&handler{d: h.d}).SendFile(context.Background(), req)
	if !outcome.IsFinalError(err) || connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(h.cwd, "file.bin"), []byte("bytes"))
	file, err := h.send("file.bin")
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int64{-1, 1, sharedChunkBytes} {
		_, err := h.d.uploads.sharedChunk(h.id(), file.FileId, offset)
		if !outcome.IsFinalError(err) {
			t.Fatal(err)
		}
	}
	// Missing after cleanup remains a known read failure, not an unknown write.
	h.d.uploads.dropSession(h.id())
	_, err = h.d.uploads.sharedChunk(h.id(), file.FileId, 0)
	if !outcome.IsFinalError(err) || connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
	// A filesystem commit failure is not a final registration. Exercise the
	// genuine copy/rename path, failing the directory durability barrier.
	writeTestFile(t, filepath.Join(h.cwd, "next.bin"), []byte("next"))
	h.d.uploads.syncDir = func(dir string) error {
		if dir == h.d.uploads.sharedDir(h.id()) {
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if validHexID(e.Name()) {
					return os.ErrPermission
				}
			}
		}
		return syncDir(dir)
	}
	_, err = h.send("next.bin")
	if err == nil || outcome.IsFinalError(err) || outcome.IsFinalError(sharedErr(err)) {
		t.Fatalf("unconfirmed write became final: %v", err)
	}
}

func TestCompletedSharedCopySurvivesLateCancellation(t *testing.T) {
	h := newSendHarness(t)
	writeTestFile(t, filepath.Join(h.cwd, "file.bin"), []byte("content"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.d.uploads.syncDir = func(dir string) error {
		if dir == h.d.uploads.sharedDir(h.id()) {
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if validHexID(e.Name()) {
					cancel()
				}
			}
		}
		return syncDir(dir)
	}
	file, err := h.d.sendFile(ctx, h.id(), h.id(), "file.bin")
	if err != nil || file == nil || ctx.Err() == nil {
		t.Fatalf("committed copy lost completion: %v", err)
	}
	if _, err := h.d.uploads.loadShared(h.id(), file.FileId); err != nil {
		t.Fatal(err)
	}
}
