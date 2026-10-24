package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/Lewin671/remote-agent/internal/tmux"
)

func TestQueueAcceptanceClassification(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.log.Close()
	h.s.mu.Lock()
	saved, err := h.s.saveQueueLocked(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: "saved"}}})
	h.s.mu.Unlock()
	if !saved || !queueAccepted(err) || !errors.Is(err, errQueueAccepted) || !errors.Is(err, errEventCommit) {
		t.Fatalf("accepted recording failure lost its cause: saved=%v error=%v", saved, err)
	}
	for _, rejected := range []error{errEventCommit, errQueueCommit, errQueueUnreadable, errExited} {
		if queueAccepted(fmt.Errorf("operation: %w", rejected)) {
			t.Fatalf("rejection classified as acceptance: %v", rejected)
		}
	}
	if !queueAccepted(nil) {
		t.Fatal("ordinary success not accepted")
	}
	if err := os.Remove(filepath.Join(h.dir, "queue.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(h.dir, "queue.json"), 0700); err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	saved, err = h.s.saveQueueLocked()
	h.s.mu.Unlock()
	if saved || queueAccepted(err) || !errors.Is(err, errQueueCommit) {
		t.Fatalf("failed save accepted: %v %v", saved, err)
	}
}

func TestCancelHeldReportsAlreadyRequeuedMessages(t *testing.T) {
	h, _, _ := heldPair(t)
	// Only the other message's release event fails encoding. Cancellation
	// of a and the canonical queue save both succeed before that failure.
	h.s.queue.Held[1].RequestID = "\xff"
	err := h.s.cancelInputBy("a", originLocal)
	if err == nil || !queueAccepted(err) || !strings.Contains(err.Error(), "already canceled") || !strings.Contains(err.Error(), "already requeued for sending") {
		t.Fatalf("completed queue operations reported as rejection: %v", err)
	}
	disk, err := loadQueue(h.dir)
	if err != nil || len(disk.Queue) != 1 || disk.Queue[0].Paused || len(disk.Held) != 0 {
		t.Fatalf("accepted held release lost: %+v %v", disk, err)
	}
	if inputStatus(h, "a") != pb.InputStatus_INPUT_STATUS_CANCELED {
		t.Fatal("original cancellation not committed")
	}
}

func TestRPCRecordingFailurePreservesAcceptedImagesAndIdentity(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	im := completeUpload(t, h.d.uploads, "local")
	h.s.log.Close()
	rpc := &handler{d: h.d}
	req := &pb.SendRequest{SessionId: h.s.meta.ID, RequestId: "same-input", Text: "picture", ImageIds: []string{im.ImageId}}
	_, err := rpc.Send(context.Background(), connect.NewRequest(req))
	if err == nil || !queueAccepted(err) || !strings.Contains(err.Error(), "same-input queued") || !strings.Contains(err.Error(), "do not resend with a new request ID") {
		t.Fatalf("accepted input reported as rejection: %v", err)
	}
	disk, err := loadQueue(h.dir)
	if err != nil || len(disk.Queue) != 1 || len(disk.Queue[0].Images) != 1 {
		t.Fatalf("lost accepted image queue: %+v %v", disk, err)
	}
	// untake would make the upload pending again, available to another message.
	if err := h.d.uploads.check([]string{im.ImageId}, "local"); err == nil {
		t.Fatal("accepted image returned to the pending pool")
	}
	if _, err := rpc.Send(context.Background(), connect.NewRequest(req)); err != nil {
		t.Fatal(err)
	}
	if len(h.s.queue.Queue) != 1 {
		t.Fatal("retry duplicated accepted message")
	}
}

func TestResumeAndHeldReleaseReturnAcceptedRecordingFailure(t *testing.T) {
	for _, op := range []string{"resume", "held-release"} {
		t.Run(op, func(t *testing.T) {
			h := newHarness(t, Config{})
			in := input{Text: "ready", RequestID: "ready", Paused: true, Mode: pb.SendMode_SEND_MODE_QUEUE}
			h.s.queue.Queue = []input{in}
			h.s.log.Close()
			var err error
			if op == "resume" {
				h.d.sessions[h.s.meta.ID] = h.s
				_, err = (&handler{d: h.d}).ResumeInput(context.Background(), connect.NewRequest(&pb.ResumeInputRequest{SessionId: h.s.meta.ID, RequestId: in.RequestID}))
			} else {
				err = h.s.releaseRequeued([]input{in})
			}
			if err == nil || !queueAccepted(err) || !strings.Contains(err.Error(), "do not resend with a new request ID") {
				t.Fatalf("durable ready state reported as rejection: %v", err)
			}
			disk, err := loadQueue(h.dir)
			if err != nil || len(disk.Queue) != 1 || disk.Queue[0].Paused {
				t.Fatalf("lost accepted ready state: %+v %v", disk, err)
			}
		})
	}
}

func TestCreateKeepsSessionAfterFirstPromptAccepted(t *testing.T) {
	for _, via := range []string{"direct", "rpc"} {
		t.Run(via, func(t *testing.T) {
			h := newHarness(t, Config{CodexBin: "unused", PollInterval: time.Hour, ScreenInterval: time.Hour})
			t.Cleanup(func() {
				for _, s := range h.d.sessions {
					s.stop()
					<-s.done
					_ = s.log.Close()
				}
			})
			bin := filepath.Join(t.TempDir(), "tmux")
			calls := bin + ".calls"
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + calls + "'\n"
			if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			// The RPC launch chooses a login shell on macOS even though tmux
			// below never runs it. This queue-commit test must not wait on the
			// real directory service; shell selection has its own tests.
			if err := os.WriteFile(filepath.Join(filepath.Dir(bin), "dscl"), []byte("#!/bin/sh\nprintf 'UserShell: /bin/sh\\n'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
			h.d.cfg.Tmux = &tmux.Client{Bin: bin, Socket: "test"}
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // no real CLI or background work is needed in this test
			h.d.ctx = ctx
			im := completeUpload(t, h.d.uploads, "local")
			// JSON queue saving accepts this string, protobuf event encoding
			// rejects it. The initial SessionState has no origin and succeeds.
			// This isolates the second commit without a production fault hook.
			origin := &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_LOCAL, RequestId: "\xff"}
			req := &pb.CreateSessionRequest{Cwd: h.dir, Agent: pb.Agent_AGENT_CODEX, Prompt: "first prompt", ImageIds: []string{im.ImageId}}
			var err error
			if via == "rpc" {
				_, err = (&handler{d: h.d}).CreateSession(agent.WithOrigin(context.Background(), origin), connect.NewRequest(req))
			} else {
				_, err = h.d.createBy(req, origin, false, false, "the terminal's PATH")
			}
			if err == nil || !queueAccepted(err) || !strings.Contains(err.Error(), "already created") || connect.CodeOf(err) == connect.CodeInvalidArgument {
				t.Fatalf("accepted creation rejected: %v", err)
			}
			h.d.mu.Lock()
			var created *session
			for _, s := range h.d.sessions {
				created = s
			}
			h.d.mu.Unlock()
			if created == nil {
				t.Fatal("accepted session was removed from daemon supervision")
			}
			created.stop()
			<-created.done
			defer created.log.Close()
			if !strings.Contains(err.Error(), created.meta.ID) {
				t.Fatal("error hid the already-created session identity")
			}
			disk, err := loadQueue(created.dir)
			if err != nil || len(disk.Queue) != 1 || len(disk.Queue[0].Images) != 1 {
				t.Fatalf("accepted first prompt lost: %+v %v", disk, err)
			}
			if err := h.d.uploads.check([]string{im.ImageId}, "local"); err == nil {
				t.Fatal("accepted first prompt's image was released")
			}
			b, err := os.ReadFile(calls)
			if err != nil || strings.Contains(string(b), "kill-session") {
				t.Fatalf("accepted CLI was killed: %s %v", b, err)
			}
		})
	}
}

func TestScheduleRetainsAcceptedRunThroughRestartAndCompletion(t *testing.T) {
	h := scheduleHarness(t)
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
	h.s.log.Close()
	h.d.tickSchedules(time.Now().Add(time.Second))
	task = readTask(t, h.d, task.ID)
	if task.Active == nil || task.Status != "queued" || task.Active.Status != "queued" || task.Dispatches != 1 || len(task.Runs) != 0 || !strings.Contains(task.Reason, "queued, but event recording failed") {
		t.Fatalf("accepted run marked failed or lost: %+v", task)
	}
	run := *task.Active
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: task.ID}, originLocal); err != nil {
		t.Fatal(err)
	}
	if got := readTask(t, h.d, task.ID); got.ManualRequest == nil || got.Active.ID != run.ID || len(h.s.queue.Queue) != 1 {
		t.Fatal("manual request duplicated active input")
	}
	// Canceling the pending request is independent of restoring this accepted run.
	h.d.tasks[task.ID].ManualRequest = nil
	if err := h.d.saveSchedulesLocked(); err != nil {
		t.Fatal(err)
	}
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	task = readTask(t, h.d, task.ID)
	if task.Active == nil || task.Active.RequestID != run.RequestID || task.Active.SessionID != run.SessionID || task.Active.AfterSeq != run.AfterSeq {
		t.Fatalf("restart lost accepted run identity/cursor: %+v", task)
	}
	repairEventLog(t, h)
	runInjector(t, h)
	eventually(t, "accepted scheduled message delivered", func() bool { return inputStatus(h, run.RequestID) == pb.InputStatus_INPUT_STATUS_SUBMITTED })
	now := time.Now().UTC()
	completeTask(h, &run, now)
	h.d.tickSchedules(now)
	task = readTask(t, h.d, task.ID)
	if task.Active != nil || len(task.Runs) != 1 || task.Runs[0].Status != "completed" || task.NextRunAt == nil || task.Status != "scheduled" || len(h.term.written()) != 1 {
		t.Fatalf("accepted execution lost completion/future runs or duplicated delivery: %+v", task)
	}
}
