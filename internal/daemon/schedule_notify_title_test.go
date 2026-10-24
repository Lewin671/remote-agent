package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func TestScheduledNotifyTitleUsesExecutionSource(t *testing.T) {
	for _, target := range []string{"new_session", "resume_session"} {
		for _, manual := range []bool{false, true} {
			for _, phase := range []string{"active", "edited", "restarted"} {
				t.Run(target+map[bool]string{false: "/automatic/", true: "/manual/"}[manual]+phase, func(t *testing.T) {
					h := scheduleHarness(t)
					defer func() { h.s.log.Close() }()
					if err := h.s.rename("对话的自定名称"); err != nil {
						t.Fatal(err)
					}
					spec := taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600, DelaySeconds: 3600})
					// The task name exceeds the push title limit, exercising rune clipping.
					spec.Title = strings.Repeat("任务", 40)
					task := createTask(t, h, spec, "")
					// Launch is simulated; reservation and durable input admission are real.
					if target == "new_session" {
						h.d.tasks[task.ID].Spec.Target = schedule.Target{Type: target, Cwd: h.dir, Agent: "codex", Mode: "auto"}
					}
					now := time.Now()
					if manual {
						requestManual(t, h, task.ID)
					} else {
						h.d.tasks[task.ID].NextRunAt = &now
					}
					pending := h.d.prepareScheduledDispatches(now.Add(time.Second))
					if len(pending) != 1 {
						t.Fatal("no dispatch reservation")
					}
					run := pending[0].Active
					if run.TargetType != target || run.TaskTitle != spec.Title {
						t.Fatalf("dispatch source was not captured: %+v", run)
					}
					h.d.queueScheduledSession(context.Background(), pending[0], now, h.s)
					if err := h.s.emit(&pb.Event{TurnId: "scheduled", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}, &pb.Event{TurnId: "scheduled", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{RequestId: run.RequestID, Text: "A different generated conversation title"}}}); err != nil {
						t.Fatal(err)
					}
					if phase != "active" {
						if err := h.s.emit(&pb.Event{TurnId: "scheduled", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}}); err != nil {
							t.Fatal(err)
						}
						h.d.tickSchedules(now)
						current := readTask(t, h.d, task.ID)
						if current.Active != nil || len(current.Runs) != 1 {
							t.Fatal("run did not move into durable history")
						}
						// Replace both mutable display name and target after this execution.
						updated := current.Spec
						updated.Title = "Edited task name"
						updated.Target = schedule.Target{Type: "resume_session", Session: h.s.meta.ID}
						if target == "resume_session" {
							h.d.agents[pb.Agent_AGENT_CODEX] = &scheduleArgvBarrier{Adapter: h.s.ag}
							updated.Target = schedule.Target{Type: "new_session", Cwd: h.dir, Agent: "codex", Mode: "auto"}
						}
						raw, _ := json.Marshal(updated)
						if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "update", TaskId: task.ID, ExpectedRevision: current.Revision, SpecJson: raw}, originLocal); err != nil {
							t.Fatal(err)
						}
						if phase == "restarted" {
							restartSession(t, h)
						}
						// A native follow-up keeps the old run's context after completion.
						if err := h.s.emit(&pb.Event{TurnId: "follow-up", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}); err != nil {
							t.Fatal(err)
						}
					}
					for _, explicit := range []string{"", "Agent supplied title"} {
						if err := notifyTest(t, h, "A useful result", explicit); err != nil {
							t.Fatal(err)
						}
						want := explicit
						if want == "" {
							want = "对话的自定名称"
							if target == "new_session" {
								want = strings.Repeat("任务", 30)
							}
						}
						events := h.events()
						n := events[len(events)-1].GetScheduledNotification()
						if n == nil || n.RunId != run.ID || n.Title != want {
							t.Fatalf("wrong recorded notification title: got=%v want=%q", n, want)
						}
					}
				})
			}
		}
	}
}
