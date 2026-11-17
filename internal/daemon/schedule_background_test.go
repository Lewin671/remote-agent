package daemon

import (
	"context"
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
