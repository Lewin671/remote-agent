package daemon

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/Lewin671/remote-agent/internal/tmux"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResumeAfterMissingTurnEndAllowsScheduledInput(t *testing.T) {
	h := newHarness(t, Config{CodexBin: "/bin/true"})
	h.d.sessions[h.s.meta.ID] = h.s
	bin := filepath.Join(h.dir, "fake-tmux")
	// No real tmux is contacted. The session's terminal is the existing fakeTerm.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nshift 2\ncase \"$1\" in has-session) exit 1;; esac\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	h.d.cfg.Tmux = &tmux.Client{Bin: bin, Socket: "audit-only"}
	if err := writeEnvFile(filepath.Join(h.dir, "env.json"), map[string]string{}); err != nil {
		t.Fatal(err)
	}
	h.writeRollout(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"lost-turn"}}`)
	h.s.bindRollout("original-cli-session", filepath.Join(h.dir, "rollout.jsonl"))
	h.s.markExited("CLI crashed before recording TurnEnded")
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
	h.d.tickSchedules(time.Now().Add(time.Second))
	eventually(t, "resumed empty composer checked", func() bool {
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		return h.s.status == pb.SessionStatus_SESSION_STATUS_IDLE
	})
	time.Sleep(150 * time.Millisecond)
	h.s.mu.Lock()
	running := h.s.running
	queued := len(h.s.queue.Queue)
	blocked := queued > 0 && h.s.holdsLocked(h.s.queue.Queue[0])
	h.s.mu.Unlock()
	got := readTask(t, h.d, task.ID)
	t.Logf("after resume and scheduled send: screen=idle status=%s running=%v queued=%d blocked=%v writes=%d task=%s", h.s.info().Status, running, queued, blocked, len(h.term.written()), got.Status)
	if blocked {
		t.Error("a turn belonging to the dead CLI blocks queued/scheduled input in the resumed idle CLI")
	}
}

func TestExitPreservesRecordedTurnEnd(t *testing.T) {
	h := newHarness(t, Config{})
	h.writeRollout(taskStarted, `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`)
	h.s.bindRollout("cli-session", filepath.Join(h.dir, "rollout.jsonl"))
	h.s.markExited("stopped")
	var ends int
	for _, e := range h.events() {
		if end := e.GetTurnEnded(); end != nil {
			ends++
			if end.Outcome != pb.TurnOutcome_TURN_OUTCOME_COMPLETED {
				t.Fatalf("outcome %v", end)
			}
		}
	}
	if ends != 1 {
		t.Fatalf("turn ends %d", ends)
	}
}

func TestExitEndsMissingTurnForBothAgents(t *testing.T) {
	for _, kind := range []pb.Agent{pb.Agent_AGENT_CODEX, pb.Agent_AGENT_CLAUDE} {
		t.Run(kind.String(), func(t *testing.T) {
			h := newHarness(t, Config{})
			if kind == pb.Agent_AGENT_CLAUDE {
				h = claudeHarness(t, Config{})
			}
			h.s.emit(&pb.Event{TurnId: "lost", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
			h.s.markExited("stopped")
			h.reopen()
			if h.s.running {
				t.Fatal("dead CLI turn still running after replay")
			}
			var ends int
			for _, e := range h.events() {
				if end := e.GetTurnEnded(); end != nil {
					ends++
					if e.TurnId != "lost" || end.Outcome != pb.TurnOutcome_TURN_OUTCOME_ABORTED {
						t.Fatalf("end %v", e)
					}
				}
			}
			if ends != 1 {
				t.Fatalf("turn ends %d", ends)
			}
			if h.s.holdsLocked(input{Mode: pb.SendMode_SEND_MODE_QUEUE}) {
				t.Fatal("old turn still blocks queue")
			}
		})
	}
}
