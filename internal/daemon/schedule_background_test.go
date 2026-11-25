package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func agyScheduledOwnedRun(t *testing.T) (*harness, *schedule.Task, time.Time) {
	t.Helper()
	h := agyHarness(t)
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	now := time.Now().UTC()
	h.d.tickSchedules(now.Add(time.Second))
	task = readTask(t, h.d, task.ID)
	if task.Active == nil {
		t.Fatal("no active scheduled run")
	}
	h.s.mu.Lock()
	h.s.meta.TaskOwned = true
	h.s.meta.ScheduledTaskID = task.ID
	h.s.meta.ScheduledRunID = task.Active.ID
	h.s.queue.Queue = nil
	h.s.saveSettledLocked()
	h.s.mu.Unlock()
	if err := h.s.emit(
		&pb.Event{TurnId: "conv/step_1", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}},
		withOrigin(&pb.Event{TurnId: "conv/step_1", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "Run script", RequestId: task.Active.RequestID}}}, &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_SYSTEM, ScheduledTaskId: task.ID, ScheduledRunId: task.Active.ID}),
	); err != nil {
		t.Fatal(err)
	}
	return h, task, now.Add(time.Second)
}

func TestScheduledAgyRunWaitsForBackgroundAndUsesFinalTurnSummary(t *testing.T) {
	t.Parallel()
	for _, withAgentNotify := range []bool{false, true} {
		name := "fallback-summary"
		if withAgentNotify {
			name = "agent-notify"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, task, now := agyScheduledOwnedRun(t)
			defer func() { h.s.log.Close() }()

			if err := h.s.emit(
				&pb.Event{TurnId: "conv/step_1", Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{CallId: "conv/step_2", ItemType: "run_command", Background: pb.ToolBackground_TOOL_BACKGROUND_TIMED_OUT, Status: "completed"}}},
				&pb.Event{TurnId: "conv/step_1", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "正在运行…稍后整理报告"}}},
			); err != nil {
				t.Fatal(err)
			}
			fullyFalse := false
			if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "Stop", SessionID: "conv", TurnID: "conv/step_1", TerminationReason: "NO_TOOL_CALL", FullyIdle: &fullyFalse}); err != nil {
				t.Fatal(err)
			}
			h.d.tickSchedules(now.Add(time.Second))

			mid := readTask(t, h.d, task.ID)
			if mid.Active == nil || len(mid.Runs) != 0 {
				t.Fatalf("ended run before background finished: %+v", mid)
			}
			if len(h.d.scheduledClosures()) != 0 {
				t.Fatal("closed session while background work remained")
			}
			for _, e := range h.events() {
				if e.GetScheduledNotification() != nil {
					t.Fatal("sent fallback notification before background work finished")
				}
			}

			// Persistence across daemon restart must keep waiting state and backgroundPossible.
			restartSession(t, h)
			if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "conv"}); err != nil {
				t.Fatal(err)
			}
			if len(h.d.scheduledClosures()) != 0 {
				t.Fatal("restart lost backgroundPossible close guard")
			}

			// Background wakeup starts a new turn with a different TurnId and no UserMessage.
			if err := h.s.emit(&pb.Event{TurnId: "conv/step_5", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}); err != nil {
				t.Fatal(err)
			}
			if withAgentNotify {
				if err := notifyTest(t, h, "脚本完成"); err != nil {
					t.Fatalf("notify_user in wakeup turn failed: %v", err)
				}
			}
			if err := h.s.emit(&pb.Event{TurnId: "conv/step_5", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "## 最终汇报\n\n脚本输出 42"}}}); err != nil {
				t.Fatal(err)
			}
			fullyTrue := true
			if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "Stop", SessionID: "conv", TurnID: "conv/step_5", TerminationReason: "NO_TOOL_CALL", FullyIdle: &fullyTrue}); err != nil {
				t.Fatal(err)
			}
			h.d.tickSchedules(now.Add(45 * time.Second))

			done := readTask(t, h.d, task.ID)
			if done.Active != nil || len(done.Runs) != 1 || done.Runs[0].Status != "completed" {
				t.Fatalf("wakeup turn did not complete run: %+v", done)
			}
			run := done.Runs[0]
			if run.FinalSummary != "最终汇报 脚本输出 42" {
				t.Fatalf("wrong final summary: %q", run.FinalSummary)
			}
			if !run.Notified || run.NotificationCount != 1 {
				t.Fatalf("wrong notification state: %+v", run)
			}
			if withAgentNotify {
				if run.NotificationSource != "agent" || run.NotificationText != "脚本完成" {
					t.Fatalf("unexpected agent notify state: %+v", run)
				}
			} else {
				if run.NotificationSource != "daemon" || run.NotificationText != "最终汇报 脚本输出 42" {
					t.Fatalf("unexpected fallback notify state: %+v", run)
				}
			}
			closures := h.d.scheduledClosures()
			if len(closures) != 1 || closures[0] != h.s {
				t.Fatalf("session not eligible for scheduled close after fullyIdle: %v", closures)
			}
		})
	}
}

func TestScheduledBackgroundTimeoutEndsAndAllowsClose(t *testing.T) {
	t.Parallel()
	h, task, now := agyScheduledOwnedRun(t)
	defer func() { h.s.log.Close() }()

	if err := h.s.emit(&pb.Event{TurnId: "conv/step_1", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "正在运行"}}}); err != nil {
		t.Fatal(err)
	}
	fullyFalse := false
	if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "Stop", SessionID: "conv", TurnID: "conv/step_1", TerminationReason: "NO_TOOL_CALL", FullyIdle: &fullyFalse}); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(now.Add(59 * time.Minute))
	if readTask(t, h.d, task.ID).Active == nil || len(h.d.scheduledClosures()) != 0 {
		t.Fatal("timed out early before background timeout")
	}

	h.d.tickSchedules(now.Add(61 * time.Minute))
	got := readTask(t, h.d, task.ID)
	if got.Active != nil || len(got.Runs) != 1 || got.Status != "failed" || got.Reason != scheduledBackgroundTimeoutReason {
		t.Fatalf("background timeout did not fail task: %+v", got)
	}
	if got.Runs[0].Status != "failed" || got.Runs[0].Reason != scheduledBackgroundTimeoutReason || !got.Runs[0].Notified {
		t.Fatalf("bad run timeout state: %+v", got.Runs[0])
	}
	closures := h.d.scheduledClosures()
	if len(closures) != 1 || closures[0] != h.s {
		t.Fatalf("timed-out session stayed open: %v", closures)
	}
}

func TestScheduledBackgroundWaitCompletesWhenFullyIdleArrivesWithoutNewTurn(t *testing.T) {
	t.Parallel()
	h, task, now := agyScheduledOwnedRun(t)
	defer func() { h.s.log.Close() }()

	if err := h.s.emit(&pb.Event{TurnId: "conv/step_1", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "后台完成"}}}); err != nil {
		t.Fatal(err)
	}
	fullyFalse := false
	if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "Stop", SessionID: "conv", TurnID: "conv/step_1", TerminationReason: "NO_TOOL_CALL", FullyIdle: &fullyFalse}); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(now.Add(time.Second))
	if readTask(t, h.d, task.ID).Active == nil {
		t.Fatal("ended prematurely")
	}

	fullyTrue := true
	if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "Stop", SessionID: "conv", TerminationReason: "NO_TOOL_CALL", FullyIdle: &fullyTrue}); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(now.Add(2 * time.Second))
	got := readTask(t, h.d, task.ID)
	if got.Active != nil || len(got.Runs) != 1 || got.Runs[0].Status != "completed" || got.Runs[0].FinalSummary != "后台完成" {
		t.Fatalf("did not complete on fullyIdle=true: %+v", got)
	}
	if len(h.d.scheduledClosures()) != 1 {
		t.Fatal("session not eligible for close after fullyIdle=true")
	}
}

func TestScheduledBackgroundWaitEndsOnHumanTakeover(t *testing.T) {
	t.Parallel()
	h, task, now := agyScheduledOwnedRun(t)
	defer func() { h.s.log.Close() }()

	if err := h.s.emit(&pb.Event{TurnId: "conv/step_1", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "等待脚本"}}}); err != nil {
		t.Fatal(err)
	}
	fullyFalse := false
	if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "Stop", SessionID: "conv", TurnID: "conv/step_1", TerminationReason: "NO_TOOL_CALL", FullyIdle: &fullyFalse}); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(now.Add(time.Second))

	if err := h.s.emit(
		&pb.Event{TurnId: "conv/step_4", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}},
		withOrigin(&pb.Event{TurnId: "conv/step_4", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "human takeover"}}}, originLocal),
		&pb.Event{TurnId: "conv/step_4", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "human reply"}}},
	); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(now.Add(2 * time.Second))

	got := readTask(t, h.d, task.ID)
	if got.Active != nil || len(got.Runs) != 1 || got.Runs[0].Status != "completed" || got.Runs[0].FinalSummary != "等待脚本" {
		t.Fatalf("human takeover did not settle prior scheduled run cleanly: %+v", got)
	}
	if len(h.d.scheduledClosures()) != 0 {
		t.Fatal("human-taken-over session was scheduled for closure")
	}
}

const (
	ccBgShell = "✻ Crunched for 4s · done 3:30 AM · 1 shell still running\n\n" +
		ccRule + "\n\x1b[39m❯\u00a0\x1b[7m \x1b[0m\n" + ccRule + "\n" +
		"   Haiku 5.5 · medium  │  example-project\n" +
		"  ⏸ manual mode on · 1 shell · ← for agents\n"
	ccBgFinished = "✻ Cogitated for 4s · done 3:32 AM\n\n" +
		ccRule + "\n\x1b[39m❯\u00a0\x1b[7m \x1b[0m\n" + ccRule + "\n" +
		"   Haiku 5.5 · medium  │  example-project\n" +
		"  ⏸ manual mode on (shift+tab to cycle) · ← for agents\n"
	ccBusy = "\x1b[38;5;174m·\x1b[39m \x1b[38;5;180mFermenting…\x1b[38;5;174m \x1b[38;5;246m(1s · ↓ 10 tokens)\x1b[39m\n" +
		ccRule + "\n\x1b[38;5;246m❯\u00a0\x1b[7m \x1b[0m\n" + ccRule + "\n" +
		"   Haiku 5.5 · medium  │  example-project\n" +
		"  ⏸ manual mode on · ← for agents\n"
)

func claudeModeScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "claude", "testdata", "mode", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func claudeScheduledOwnedRun(t *testing.T) (*harness, *schedule.Task, time.Time) {
	t.Helper()
	h := claudeHarness(t, Config{ApprovalTimeout: time.Minute})
	h.d.sessions[h.s.meta.ID] = h.s
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	now := time.Now().UTC()
	h.d.tickSchedules(now.Add(time.Second))
	task = readTask(t, h.d, task.ID)
	if task.Active == nil {
		t.Fatal("no active scheduled run")
	}
	h.s.mu.Lock()
	h.s.meta.TaskOwned = true
	h.s.meta.ScheduledTaskID = task.ID
	h.s.meta.ScheduledRunID = task.Active.ID
	h.s.queue.Queue = nil
	h.s.saveSettledLocked()
	h.s.mu.Unlock()
	rolloutPath := filepath.Join(h.dir, "rollout.jsonl")
	h.s.bindRollout("cc-sched-1", rolloutPath)
	if err := h.s.emit(
		&pb.Event{TurnId: "p1", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}},
		withOrigin(&pb.Event{TurnId: "p1", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "Run script", RequestId: task.Active.RequestID}}}, &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_SYSTEM, ScheduledTaskId: task.ID, ScheduledRunId: task.Active.ID}),
	); err != nil {
		t.Fatal(err)
	}
	return h, task, now.Add(time.Second)
}

func TestScheduledClaudeRunWaitsForBackgroundAndUsesFinalTurnSummary(t *testing.T) {
	t.Parallel()
	h, task, now := claudeScheduledOwnedRun(t)
	defer func() { h.s.log.Close() }()

	h.term.setScreen(claudeModeScreen(t, "background-shell-2.1.300.txt"))
	if err := h.s.emit(
		&pb.Event{TurnId: "p1", Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{CallId: "t1", ItemType: "Bash", Background: pb.ToolBackground_TOOL_BACKGROUND_TIMED_OUT, Status: "completed"}}},
		&pb.Event{TurnId: "p1", Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{CallId: "t2", ItemType: "Bash", Background: pb.ToolBackground_TOOL_BACKGROUND_REQUESTED, Status: "completed"}}},
		&pb.Event{TurnId: "p1", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "still posting, I'll continue once it finishes"}}},
		&pb.Event{TurnId: "p1", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}},
	); err != nil {
		t.Fatal(err)
	}

	h.d.tickSchedules(now.Add(time.Second))
	h.d.tickSchedules(now.Add(10 * time.Second))
	if mid := readTask(t, h.d, task.ID); mid.Active == nil || len(mid.Runs) != 0 {
		t.Fatalf("ended Claude run while background shell remained: %+v", mid)
	}
	if len(h.d.scheduledClosures()) != 0 {
		t.Fatal("closed Claude session while background shell remained")
	}
	for _, e := range h.events() {
		if e.GetScheduledNotification() != nil {
			t.Fatal("sent fallback notification before background shell finished")
		}
	}

	// Persistence across daemon restart must keep waiting state and backgroundPossible.
	restartSession(t, h)
	h.d.tickSchedules(now.Add(20 * time.Second))
	if mid := readTask(t, h.d, task.ID); mid.Active == nil || len(mid.Runs) != 0 {
		t.Fatalf("restart lost Claude background wait: %+v", mid)
	}
	if len(h.d.scheduledClosures()) != 0 {
		t.Fatal("restart lost Claude backgroundPossible close guard")
	}

	// Claude Code wakes itself with a task-notification turn and writes the final answer.
	h.writeRollout(
		`{"type":"user","promptId":"p2","origin":{"kind":"task-notification","producer":"session-task"},"promptSource":"system","message":{"role":"user","content":"<task-notification>done</task-notification>"}}`,
		`{"type":"assistant","message":{"id":"m2","model":"claude-haiku-5-5","stop_reason":"end_turn","content":[{"type":"text","text":"## 最终汇报\n\n脚本输出 42"}]}}`,
		`{"type":"system","subtype":"turn_duration","durationMs":1200}`,
	)
	if !h.s.readRollout() {
		t.Fatal("readRollout failed")
	}
	h.term.setScreen(claudeModeScreen(t, "finished-shell-2.1.300.txt"))

	// First idle capture starts the grace window; must not complete immediately.
	seqBeforeClear := h.s.log.LastSeq()
	t1 := now.Add(45 * time.Second)
	h.d.tickSchedules(t1)
	if mid := readTask(t, h.d, task.ID); mid.Active == nil || len(mid.Runs) != 0 {
		t.Fatalf("completed on first clear capture without grace: %+v", mid)
	}
	h.d.tickSchedules(t1.Add(backgroundClearGrace))

	done := readTask(t, h.d, task.ID)
	if done.Active != nil || len(done.Runs) != 1 || done.Runs[0].Status != "completed" {
		t.Fatalf("wakeup turn did not complete Claude run: %+v", done)
	}
	run := done.Runs[0]
	if run.FinalSummary != "最终汇报 脚本输出 42" {
		t.Fatalf("wrong final summary: %q", run.FinalSummary)
	}
	if !run.Notified || run.NotificationCount != 1 || run.NotificationSource != "daemon" || run.NotificationText != "最终汇报 脚本输出 42" {
		t.Fatalf("wrong fallback notification state: %+v", run)
	}
	closures := h.d.scheduledClosures()
	if len(closures) != 1 || closures[0] != h.s {
		t.Fatalf("session not eligible for scheduled close after screen clear: %v", closures)
	}
	m, err := loadMeta(h.s.dir)
	if err != nil || m.BackgroundPossible == nil || *m.BackgroundPossible || m.BackgroundIdleSeq != seqBeforeClear {
		t.Fatalf("did not persist cleared background meta: %+v (want idleSeq=%d) err=%v", m, seqBeforeClear, err)
	}
}

func TestScheduledClaudeRunCompletesWhenScreenShowsNoBackground(t *testing.T) {
	t.Parallel()
	h, task, now := claudeScheduledOwnedRun(t)
	defer func() { h.s.log.Close() }()

	h.term.setScreen(ccBgFinished)
	if err := h.s.emit(
		&pb.Event{TurnId: "p1", Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{CallId: "t1", ItemType: "Bash", Background: pb.ToolBackground_TOOL_BACKGROUND_REQUESTED, Status: "completed"}}},
		&pb.Event{TurnId: "p1", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "后台已完成"}}},
		&pb.Event{TurnId: "p1", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}},
	); err != nil {
		t.Fatal(err)
	}

	t0 := now.Add(time.Second)
	h.d.tickSchedules(t0)
	h.d.tickSchedules(t0.Add(backgroundClearGrace - time.Second))
	if mid := readTask(t, h.d, task.ID); mid.Active == nil {
		t.Fatalf("completed before grace period elapsed: %+v", mid)
	}

	// Completes once stable idle screen spans backgroundClearGrace, without waiting 1h.
	h.d.tickSchedules(t0.Add(backgroundClearGrace))
	got := readTask(t, h.d, task.ID)
	if got.Active != nil || len(got.Runs) != 1 || got.Runs[0].Status != "completed" || got.Runs[0].FinalSummary != "后台已完成" {
		t.Fatalf("did not complete promptly on idle screen: %+v", got)
	}
	if len(h.d.scheduledClosures()) != 1 {
		t.Fatal("session not eligible for scheduled close after stable idle screen")
	}

	// Persisted BackgroundIdleSeq keeps older background ToolCall from re-arming after restart.
	m, err := loadMeta(h.s.dir)
	if err != nil || m.BackgroundPossible == nil || *m.BackgroundPossible || m.BackgroundIdleSeq == 0 {
		t.Fatalf("meta=%+v err=%v", m, err)
	}
	restartSession(t, h)
	h.s.mu.Lock()
	bg := h.s.backgroundPossible
	h.s.mu.Unlock()
	if bg {
		t.Fatal("restart re-armed backgroundPossible past BackgroundIdleSeq")
	}
}

func TestScheduledClaudeBackgroundRaceGapDoesNotClosePrematurely(t *testing.T) {
	t.Parallel()
	t.Run("gap before busy screen", func(t *testing.T) {
		t.Parallel()
		h, task, now := claudeScheduledOwnedRun(t)
		defer func() { h.s.log.Close() }()

		h.term.setScreen(ccBgShell)
		if err := h.s.emit(
			&pb.Event{TurnId: "p1", Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{CallId: "t1", ItemType: "Bash", Background: pb.ToolBackground_TOOL_BACKGROUND_TIMED_OUT, Status: "completed"}}},
			&pb.Event{TurnId: "p1", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "waiting"}}},
			&pb.Event{TurnId: "p1", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}},
		); err != nil {
			t.Fatal(err)
		}
		h.d.tickSchedules(now.Add(time.Second))

		// Background shell finishes; screen briefly shows no background and no turn yet.
		h.term.setScreen(ccBgFinished)
		gap := now.Add(10 * time.Second)
		h.d.tickSchedules(gap)
		h.d.tickSchedules(gap.Add(backgroundClearGrace - time.Second))
		if readTask(t, h.d, task.ID).Active == nil || len(h.d.scheduledClosures()) != 0 {
			t.Fatal("closed run during brief post-background gap")
		}

		// Claude starts working (spinner/dim prompt) before grace expires, resetting stability.
		h.term.setScreen(ccBusy)
		h.d.tickSchedules(gap.Add(backgroundClearGrace))
		if readTask(t, h.d, task.ID).Active == nil || len(h.d.scheduledClosures()) != 0 {
			t.Fatal("closed run when CLI became busy")
		}
	})

	t.Run("unread task-notification in rollout cancels clear", func(t *testing.T) {
		t.Parallel()
		h, task, now := claudeScheduledOwnedRun(t)
		defer func() { h.s.log.Close() }()

		h.term.setScreen(ccBgFinished)
		if err := h.s.emit(
			&pb.Event{TurnId: "p1", Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{CallId: "t1", ItemType: "Bash", Background: pb.ToolBackground_TOOL_BACKGROUND_TIMED_OUT, Status: "completed"}}},
			&pb.Event{TurnId: "p1", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "waiting"}}},
			&pb.Event{TurnId: "p1", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}},
		); err != nil {
			t.Fatal(err)
		}
		t0 := now.Add(time.Second)
		h.d.tickSchedules(t0)

		// Before the final check at t0+grace, Claude writes task-notification to rollout
		// (not yet read by pollLoop). settleBackgroundClear must read rollout first.
		h.writeRollout(`{"type":"user","promptId":"p2","origin":{"kind":"task-notification","producer":"session-task"},"promptSource":"system","message":{"role":"user","content":"<task-notification>done</task-notification>"}}`)
		h.d.tickSchedules(t0.Add(backgroundClearGrace))

		mid := readTask(t, h.d, task.ID)
		if mid.Active == nil || len(mid.Runs) != 0 || mid.Active.TurnID != "p2" {
			t.Fatalf("did not keep run open for unread task-notification turn: %+v", mid)
		}
		if len(h.d.scheduledClosures()) != 0 {
			t.Fatal("closed session despite unread task-notification turn")
		}
	})
}

func TestScheduledClaudeBackgroundTimeoutBackstop(t *testing.T) {
	t.Parallel()
	h, task, now := claudeScheduledOwnedRun(t)
	defer func() { h.s.log.Close() }()

	h.term.setScreen(ccBgShell)
	if err := h.s.emit(
		&pb.Event{TurnId: "p1", Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{CallId: "t1", ItemType: "Bash", Background: pb.ToolBackground_TOOL_BACKGROUND_TIMED_OUT, Status: "completed"}}},
		&pb.Event{TurnId: "p1", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "still running"}}},
		&pb.Event{TurnId: "p1", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}},
	); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(now.Add(59 * time.Minute))
	if readTask(t, h.d, task.ID).Active == nil || len(h.d.scheduledClosures()) != 0 {
		t.Fatal("timed out early while Claude background shell remained")
	}

	h.d.tickSchedules(now.Add(61 * time.Minute))
	got := readTask(t, h.d, task.ID)
	if got.Active != nil || len(got.Runs) != 1 || got.Status != "failed" || got.Reason != scheduledBackgroundTimeoutReason {
		t.Fatalf("Claude background timeout did not fail task: %+v", got)
	}
	closures := h.d.scheduledClosures()
	if len(closures) != 1 || closures[0] != h.s {
		t.Fatalf("timed-out Claude session stayed open: %v", closures)
	}
}
