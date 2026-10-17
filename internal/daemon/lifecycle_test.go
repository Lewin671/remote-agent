package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/Lewin671/remote-agent/internal/tmux"
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
	eventually(t, "scheduled input written", func() bool { return len(h.term.written()) > 0 })
	h.s.mu.Lock()
	running := h.s.running
	queued := len(h.s.queue.Queue)
	blocked := queued > 0 && h.s.holdsLocked(h.s.queue.Queue[0])
	h.s.mu.Unlock()
	got := readTask(t, h.d, task.ID)
	t.Logf("after resume and scheduled send: screen=idle status=%s running=%v queued=%d blocked=%v writes=%d task=%s", h.s.info().Status, running, queued, blocked, len(h.term.written()), got.Status)
	if blocked || running {
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

func TestRestoreExitedCheckpointReadsFinalTranscript(t *testing.T) {
	r := newRestoreHarness(t)
	id := "00000000000000aa"
	rollout := filepath.Join(paths.SessionDir(id), "rollout.jsonl")
	dir := r.save(meta{ID: id, RolloutPath: rollout, Exited: true}, false)
	before := taskStarted + "\n"
	final := `{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"m1","content":[{"text":"last reply"}]}}}` + "\n"
	if err := os.WriteFile(rollout, []byte(before+final), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := eventlog.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Append(id, []*pb.Event{{TurnId: "t1", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}}, rollout, int64(len(before))); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if err = r.d.restore(); err != nil {
		t.Fatal(err)
	}
	s := r.session(id)
	path, off := s.log.Checkpoint()
	evs, _, err := s.log.Since(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("restored EXITED: events=%d checkpoint=%s:%d file_size=%d", len(evs), filepath.Base(path), off, len(before+final))
	for _, e := range evs {
		if e.GetAgentMessage() != nil {
			return
		}
	}
	t.Error("final reply still in transcript but restore skipped it because meta.Exited was already durable")
}

func TestExitRetriesFailedFinalAppendBeforeSettlingTurn(t *testing.T) {
	h := newHarness(t, Config{})
	h.writeRollout(taskStarted)
	h.s.bindRollout("cli", filepath.Join(h.dir, "rollout.jsonl"))
	h.s.log.Close()
	h.writeRollout(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"last","content":[{"text":"final reply"}]}}}`, `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`)
	h.s.markExited("stopped")
	if !h.s.running {
		t.Fatal("settled a turn before its final append succeeded")
	}
	l, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h.s.log = l
	if !h.s.settleExited() {
		t.Fatal("final append not retried")
	}
	var reply, ends int
	for _, e := range h.events() {
		if e.GetAgentMessage().GetText() == "final reply" {
			reply++
		}
		if end := e.GetTurnEnded(); end != nil {
			ends++
			if end.Outcome != pb.TurnOutcome_TURN_OUTCOME_COMPLETED {
				t.Fatalf("premature aborted outcome %v", end)
			}
		}
	}
	if reply != 1 || ends != 1 || h.s.running {
		t.Fatalf("reply=%d ends=%d running=%v", reply, ends, h.s.running)
	}
}

func TestExitSettlesHeldInput(t *testing.T) {
	h, _, _ := heldPair(t)
	if err := h.s.kill(); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	if !h.s.settleExited() {
		t.Fatal("settlement failed")
	}
	for _, id := range []string{"a", "b"} {
		states := inputStates(h, id)
		last := states[len(states)-1]
		if last.Status != pb.InputStatus_INPUT_STATUS_UNCONFIRMED || last.Reason != "exited" {
			t.Fatalf("%s: %v", id, last)
		}
	}
	if len(h.s.queue.Held) > 0 {
		t.Fatal("exited CLI still holds input")
	}
	q, err := loadQueue(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Held) > 0 {
		t.Fatal("held input still persisted")
	}
}

func TestExitSettlesHeldInputOnlyAfterOutcomeIsLogged(t *testing.T) {
	h, _, _ := heldPair(t)
	h.s.log.Close()
	h.s.markExited("stopped")
	if len(h.s.queue.Held) != 2 {
		t.Fatal("held input dropped without outcome")
	}
	l, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h.s.log = l
	// A final user message proves one held input reached the model even
	// when its SUBMITTED outcome has not been written yet.
	h.s.emit(&pb.Event{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "first", RequestId: "a"}}})
	if !h.s.settleExited() {
		t.Fatal("settlement failed")
	}
	if inputStatus(h, "a") != pb.InputStatus_INPUT_STATUS_SUBMITTED || inputStatus(h, "b") != pb.InputStatus_INPUT_STATUS_UNCONFIRMED {
		t.Fatal("wrong final states")
	}
	h.reopen()
	if len(h.s.queue.Held) != 0 {
		t.Fatal("settled input restored")
	}
}

// 2.1.300 writes the user record but no interrupted text or turn_duration
// when Esc wins the race with the first assistant response.
const earlyClaudeUser = `{"type":"user","promptId":"early","origin":{"kind":"human"},"message":{"content":"original question"}}`

func TestClaudeRestoredPromptSettlesEarlyInterruptAfterRestart(t *testing.T) {
	h := claudeHarness(t, Config{ScreenInterval: 10 * time.Millisecond})
	h.writeRollout(earlyClaudeUser)
	h.s.bindRollout("cli", filepath.Join(h.dir, "rollout.jsonl"))
	if _, err := h.s.enqueueMode("next", "next", nil, originLocal, pb.SendMode_SEND_MODE_QUEUE); err != nil {
		t.Fatal(err)
	}
	h.reopen() // no in-memory interrupt intent survives
	h.term.setScreen(ccBox("original question"))
	ctx, cancel := context.WithCancel(context.Background())
	h.s.start(ctx)
	defer func() { cancel(); <-h.s.done }()
	eventually(t, "early interrupt ended", func() bool { h.s.mu.Lock(); defer h.s.mu.Unlock(); return !h.s.running })
	h.s.mu.Lock()
	if len(h.s.queue.Queue) != 1 || !h.s.queue.Queue[0].Paused {
		t.Error("waiting input not paused by interruption")
	}
	h.s.mu.Unlock()
	var ends int
	for _, e := range h.events() {
		if end := e.GetTurnEnded(); end != nil {
			ends++
			if e.TurnId != "early" || end.Outcome != pb.TurnOutcome_TURN_OUTCOME_ABORTED {
				t.Fatalf("end %v", e)
			}
		}
	}
	if ends != 1 {
		t.Fatalf("ends=%d", ends)
	}
	// The live parser learned the inferred end: the next real turn must
	// not abort the old turn again.
	h.writeRollout(`{"type":"user","promptId":"next","origin":{"kind":"human"},"message":{"content":"different question"}}`, `{"type":"system","subtype":"turn_duration","durationMs":1}`)
	h.s.readRollout()
	ends = 0
	for _, e := range h.events() {
		if e.GetTurnEnded() != nil {
			ends++
		}
	}
	if ends != 2 {
		t.Fatalf("parser did not learn inferred end: %d ends", ends)
	}
}

func TestClaudeInterruptKeyAloneDoesNotEndTurn(t *testing.T) {
	h := claudeHarness(t, Config{})
	h.writeRollout(earlyClaudeUser)
	h.s.bindRollout("cli", filepath.Join(h.dir, "rollout.jsonl"))
	if _, err := h.s.interrupt(); err != nil {
		t.Fatal(err)
	}
	h.s.checkScreen()
	h.s.checkScreen()
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if !h.s.running || !h.s.restoredPromptSince.IsZero() {
		t.Fatal("Esc or idle screen alone ended the turn")
	}
}

func TestClaudeRestoredPromptDoesNotOverrideFinalTranscript(t *testing.T) {
	h := claudeHarness(t, Config{})
	h.writeRollout(earlyClaudeUser)
	h.s.bindRollout("cli", filepath.Join(h.dir, "rollout.jsonl"))
	h.term.setScreen(ccBox("original question"))
	h.s.settleRestoredPrompt(ccBox("original question"))
	h.s.restoredPromptSince = time.Now().Add(-2 * time.Second)
	h.writeRollout(`{"type":"system","subtype":"turn_duration","durationMs":1}`)
	h.s.settleRestoredPrompt(ccBox("original question"))
	for _, e := range h.events() {
		if end := e.GetTurnEnded(); end != nil && end.Outcome != pb.TurnOutcome_TURN_OUTCOME_COMPLETED {
			t.Fatal("inferred end overrode final transcript")
		}
	}
}

func TestClaudeRestoredPromptKeepsOutputTurnAndRetriesFailedEnd(t *testing.T) {
	for _, output := range []bool{false, true} {
		t.Run(fmt.Sprint(output), func(t *testing.T) {
			h := claudeHarness(t, Config{})
			h.writeRollout(earlyClaudeUser)
			h.s.bindRollout("cli", filepath.Join(h.dir, "rollout.jsonl"))
			if output {
				h.s.emit(&pb.Event{TurnId: "early", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "partial reply"}}})
			}
			h.term.setScreen(ccBox("original question"))
			h.s.settleRestoredPrompt(ccBox("original question"))
			h.s.restoredPromptSince = time.Now().Add(-2 * time.Second)
			h.s.log.Close()
			h.s.settleRestoredPrompt(ccBox("original question"))
			if !h.s.running {
				t.Fatal("ended without durable outcome")
			}
			l, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			h.s.log = l
			h.s.settleRestoredPrompt(ccBox("original question"))
			if h.s.running != output {
				t.Fatalf("running=%v output=%v", h.s.running, output)
			}
		})
	}
}
