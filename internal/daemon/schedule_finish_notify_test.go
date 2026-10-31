package daemon

import (
	"encoding/json"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestScheduledFinishNotification(t *testing.T) {
	for _, tc := range []struct {
		name, policy, status, body string
		agent, manual              bool
	}{
		{name: "default", status: "completed", body: strings.Repeat("字", 200)},
		{name: "explicit", policy: "finish", status: "completed", body: strings.Repeat("字", 200)},
		{name: "manual", status: "completed", body: strings.Repeat("字", 200), manual: true},
		{name: "agent", status: "completed", agent: true},
		{name: "disabled", policy: "agent_only", status: "completed"},
		{name: "disabled-agent", policy: "agent_only", status: "completed", agent: true},
		{name: "failed", status: "failed", body: "定时任务运行失败：execution_failed"},
		{name: "interrupted", status: "interrupted", body: "定时任务运行已中断。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := scheduleHarness(t)
			defer func() { h.s.log.Close() }()
			task := startScheduledTurn(t, h)
			run := h.d.tasks[task.ID].Active
			run.NotifyOnFinish = tc.policy != "agent_only"
			run.Manual = tc.manual
			if tc.agent {
				if err := notifyTest(t, h, "Agent result", ""); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.s.emit(&pb.Event{TurnId: "timed", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "# " + strings.Repeat("字", 230)}}}); err != nil {
				t.Fatal(err)
			}
			if tc.status == "failed" {
				h.d.watchScheduledRun(h.d.tasks[task.ID], time.Now())
				h.d.endScheduledRun(h.d.tasks[task.ID], time.Now(), "failed", "execution_failed")
			} else {
				outcome := pb.TurnOutcome_TURN_OUTCOME_COMPLETED
				if tc.status == "interrupted" {
					outcome = pb.TurnOutcome_TURN_OUTCOME_ABORTED
				}
				endTimed(t, h, outcome)
			}
			h.d.tickSchedules(time.Now())
			got := readTask(t, h.d, task.ID).Runs[0]
			count := 0
			for _, e := range h.events() {
				if n := e.GetScheduledNotification(); n != nil {
					count++
					if !tc.agent && (n.Text != tc.body || n.Title != "Check deployment" || e.Origin.GetKind() != pb.OriginKind_ORIGIN_KIND_SYSTEM) {
						t.Fatalf("fallback: %v", e)
					}
				}
			}
			want := 1
			if tc.policy == "agent_only" && !tc.agent {
				want = 0
			}
			if count != want || got.NotificationCount != want || got.Notified != (want > 0) {
				t.Fatalf("event/run mismatch: count=%d run=%+v", count, got)
			}
			source := "daemon"
			if tc.agent {
				source = "agent"
			}
			if want == 0 {
				source = ""
			}
			if got.NotificationSource != source {
				t.Fatalf("wrong source: %+v", got)
			}
			// A restart and repeated ticks must not duplicate the persistent event.
			restartSession(t, h)
			h.d.tickSchedules(time.Now())
			h.d.tickSchedules(time.Now())
			if after := readTask(t, h.d, task.ID).Runs[0]; after.NotificationCount != want {
				t.Fatalf("duplicate after replay: %+v", after)
			}
		})
	}
}

func TestScheduledFinishRetryUsesDurableEventAsAuthority(t *testing.T) {
	h := scheduleHarness(t)
	defer func() { h.s.log.Close() }()
	task := startScheduledTurn(t, h)
	endTimed(t, h, pb.TurnOutcome_TURN_OUTCOME_COMPLETED)
	// Settle the execution while the event log is unavailable for notification.
	h.s.log.Close()
	run := h.d.tasks[task.ID].Active
	run.MessageSeen = true
	h.d.endScheduledRun(h.d.tasks[task.ID], time.Now(), "completed", "")
	if h.d.finishScheduledNotification(task.ID, &h.d.tasks[task.ID].Runs[0]) || h.d.tasks[task.ID].Runs[0].Notified {
		t.Fatal("failed append marked notified")
	}
	repairEventLog(t, h)
	saved := h.d.tasks[task.ID].Runs[0]
	saved.FollowupsClosed = true // force event-authority recovery, not the ordinary follow-up watcher
	h.d.tickSchedules(time.Now())
	if !readTask(t, h.d, task.ID).Runs[0].Notified {
		t.Fatal("failed notification was not retried")
	}
	// Simulate process death after event append, before saving schedules.json.
	h.d.tasks[task.ID].Runs[0] = saved
	if err := h.d.saveSchedulesLocked(); err != nil {
		t.Fatal(err)
	}
	restartSession(t, h)
	h.d.tickSchedules(time.Now())
	count := 0
	for _, e := range h.events() {
		if e.GetScheduledNotification() != nil {
			count++
		}
	}
	got := readTask(t, h.d, task.ID).Runs[0]
	if count != 1 || !got.Notified || got.NotificationSource != "daemon" || got.NotificationCount != 1 {
		t.Fatalf("lost/duplicate event after crash: %d %+v", count, got)
	}
}

func TestScheduledNotificationPolicySnapshot(t *testing.T) {
	for _, policy := range []string{"", "finish", "agent_only", "invalid"} {
		t.Run(policy, func(t *testing.T) {
			h := scheduleHarness(t)
			defer h.s.log.Close()
			spec := taskSpec(schedule.Trigger{Type: "after_turn"})
			spec.Notify = policy
			raw, _ := json.Marshal(spec)
			data, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: raw}, originLocal)
			if policy == "invalid" {
				if err == nil {
					t.Fatal("accepted unknown notification policy")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var task schedule.Task
			if err = json.Unmarshal(data, &task); err != nil {
				t.Fatal(err)
			}
			pending := h.d.prepareScheduledDispatches(time.Now().Add(time.Second))
			if len(pending) != 1 || pending[0].Active.NotifyOnFinish != (policy != "agent_only") {
				t.Fatal("policy not captured at dispatch")
			}
			// Change the live definition to the opposite policy, then complete
			// the actual reserved/queued execution. Only event outcomes count.
			current := h.d.tasks[task.ID]
			updated := current.Spec
			updated.Notify = "agent_only"
			if policy == "agent_only" {
				updated.Notify = "finish"
			}
			if _, err := titleUpdateRequest(t, h, schedule.Clone(current), updated, originLocal, ""); err != nil {
				t.Fatal(err)
			}
			h.d.queueScheduledSession(t.Context(), pending[0], time.Now(), h.s)
			completeTask(h, pending[0].Active, time.Now())
			h.d.tickSchedules(time.Now())
			got := readTask(t, h.d, task.ID).Runs[0]
			want := 1
			if policy == "agent_only" {
				want = 0
			}
			count := 0
			for _, e := range h.events() {
				if e.GetScheduledNotification() != nil {
					count++
				}
			}
			if got.Status != "completed" || got.Notified != (want > 0) || got.NotificationCount != want || count != want {
				t.Fatalf("completion followed mutable spec instead of dispatch policy: policy=%q current=%q events=%d run=%+v", policy, current.Spec.Notify, count, got)
			}
		})
	}
}

func TestScheduledFinishLaunchFailureUsesSourceSnapshot(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	spec := taskSpec(schedule.Trigger{Type: "after_turn"})
	task := createTask(t, h, spec, "")
	h.d.tasks[task.ID].Spec.Target.Type = "new_session"
	pending := h.d.prepareScheduledDispatches(time.Now().Add(time.Second))
	if len(pending) != 1 {
		t.Fatal("no reservation")
	}
	active := h.d.tasks[task.ID]
	// No execution session could be created; the source still owns a log.
	active.SourceSession = "different-source"
	active.Spec.Title = "later title"
	h.d.endScheduledRun(active, time.Now(), "failed", "could_not_start_session")
	h.d.tickSchedules(time.Now())
	got := readTask(t, h.d, task.ID).Runs[0]
	if !got.Notified || got.SessionID != "" || got.NotificationSource != "daemon" || got.NotificationText != "定时任务运行失败：could_not_start_session" {
		t.Fatalf("missing launch failure notification: %+v", got)
	}
	events := h.events()
	notice := events[len(events)-1].GetScheduledNotification()
	if notice == nil || notice.Title != spec.Title {
		t.Fatalf("wrong failure event title: %v", notice)
	}
}

func TestScheduledFinishUndeliverableStopsRetrying(t *testing.T) {
	for _, name := range []string{"no-source", "deleted-source"} {
		t.Run(name, func(t *testing.T) {
			h := scheduleHarness(t)
			defer h.s.log.Close()
			source := ""
			if name == "deleted-source" {
				source = h.s.meta.ID
			}
			spec := taskSpec(schedule.Trigger{Type: "after_turn"})
			task := createTask(t, h, spec, "")
			current := h.d.tasks[task.ID]
			current.SourceSession = source
			current.Spec.Target.Type = "new_session"
			pending := h.d.prepareScheduledDispatches(time.Now().Add(time.Second))
			if len(pending) != 1 {
				t.Fatal("no reserved execution")
			}
			if current.Active.NotificationSessionID != source || current.SourceSession != source {
				t.Fatal("source snapshot differs")
			}
			h.d.endScheduledRun(current, time.Now(), "failed", "could_not_start_session")
			if source != "" {
				delete(h.d.sessions, source)
			}
			h.d.tickSchedules(time.Now())
			got := readTask(t, h.d, task.ID)
			run := got.Runs[0]
			if run.NotificationSource != "undeliverable" || run.Notified || run.NotificationCount != 0 || hasPendingFinishNotifications(got) {
				t.Fatalf("not settled as undeliverable: %+v", run)
			}
			rev := got.Revision
			for i := 0; i < 3; i++ {
				h.d.tickSchedules(time.Now())
			}
			if h.d.finishScheduledNotification(task.ID, &h.d.tasks[task.ID].Runs[0]) || readTask(t, h.d, task.ID).Revision != rev {
				t.Fatal("undeliverable notice retried")
			}
			restartSession(t, h)
			h.d.tickSchedules(time.Now())
			if got := readTask(t, h.d, task.ID); got.Runs[0].NotificationSource != "undeliverable" || hasPendingFinishNotifications(got) {
				t.Fatal("restart lost undeliverable state")
			}
			for _, e := range h.events() {
				if e.GetScheduledNotification() != nil {
					t.Fatal("undeliverable run emitted a notification")
				}
			}
		})
	}
}

func TestAgentNoticeAfterUndeliverableIsNotDaemonNotice(t *testing.T) {
	run := schedule.Run{ID: "run", NotificationSource: "undeliverable"}
	recordRunNotification(&run, &pb.Event{Seq: 1, Kind: &pb.Event_ScheduledNotification{ScheduledNotification: &pb.ScheduledNotification{RunId: "run", Text: "agent result"}}})
	if run.NotificationSource != "agent" || !run.Notified {
		t.Fatalf("wrong notification history: %+v", run)
	}
}

func TestScheduledFinishRelayRefusalUsesOnlySource(t *testing.T) {
	for _, name := range []string{"no-source", "source", "rejected-source"} {
		t.Run(name, func(t *testing.T) {
			sourceAvailable := name == "source"
			h := scheduleHarness(t)
			defer h.s.log.Close()
			spec := taskSpec(schedule.Trigger{Type: "after_turn"})
			task := createTask(t, h, spec, "")
			current := h.d.tasks[task.ID]
			current.Spec.Target.Type = "new_session"
			// A long-lived source log has a different sequence space from
			// the fresh execution log.
			for i := 0; i < 5; i++ {
				if err := h.s.emit(&pb.Event{Kind: &pb.Event_TurnContext{TurnContext: &pb.TurnContext{}}}); err != nil {
					t.Fatal(err)
				}
			}
			pending := h.d.prepareScheduledDispatches(time.Now().Add(time.Second))
			if len(pending) != 1 {
				t.Fatal("no reservation")
			}
			run := current.Active
			run.StartSeq = 100 // sequence from the rejected log, never a source-log cursor
			if !sourceAvailable {
				current.SourceSession = ""
				run.NotificationSessionID = ""
			}
			dir := t.TempDir()
			log, err := eventlog.Open(filepath.Join(dir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			m := h.s.meta
			m.ID = "fedcba9876543210"
			m.TaskOwned = true
			m.ScheduledTaskID = task.ID
			m.ScheduledRunID = run.ID
			rejected, err := newSession(h.d, dir, m, h.term, log)
			if err != nil {
				t.Fatal(err)
			}
			h.d.sessions[m.ID] = rejected
			run.SessionID = m.ID
			if name == "rejected-source" {
				run.NotificationSessionID = m.ID
			}
			if err = h.d.SessionPublicationFailed(m.ID, "relay_session_limit"); err != nil {
				t.Fatal(err)
			}
			saved := current.Runs[0]
			h.d.tickSchedules(time.Now())
			got := readTask(t, h.d, task.ID).Runs[0]
			if got.Notified != sourceAvailable {
				t.Fatalf("wrong delivery outcome: %+v", got)
			}
			wantSource := "undeliverable"
			if sourceAvailable {
				wantSource = "daemon"
			}
			if got.NotificationSource != wantSource {
				t.Fatalf("wrong delivery source: %+v", got)
			}
			if log.LastSeq() != 0 {
				t.Fatal("notice appended to relay-rejected session")
			}
			count := 0
			for _, e := range h.events() {
				if n := e.GetScheduledNotification(); n != nil {
					count++
					if n.Text != "定时任务运行失败：relay_session_limit" || n.RunId != run.ID {
						t.Fatalf("wrong source notice: %v", n)
					}
				}
			}
			want := 0
			if sourceAvailable {
				want = 1
			}
			if count != want {
				t.Fatalf("source notices=%d want=%d", count, want)
			}
			if sourceAvailable {
				folded := got
				recordRunNotification(&folded, &pb.Event{Seq: 1, SessionId: m.ID, Kind: &pb.Event_ScheduledNotification{ScheduledNotification: &pb.ScheduledNotification{TaskId: task.ID, RunId: run.ID, Text: "later agent notice"}}})
				if folded.NotificationCount != 2 || folded.NotificationSource != "agent_and_daemon" {
					t.Fatalf("source-log cursor swallowed a later execution notice: %+v", folded)
				}
				// Replaying after append-before-save must scan the source log from zero.
				current.Runs[0] = saved
				if err = h.d.saveSchedulesLocked(); err != nil {
					t.Fatal(err)
				}
				if err = h.d.loadSchedules(); err != nil {
					t.Fatal(err)
				}
				h.d.tickSchedules(time.Now())
				count = 0
				for _, e := range h.events() {
					if e.GetScheduledNotification() != nil {
						count++
					}
				}
				if count != 1 || readTask(t, h.d, task.ID).Runs[0].NotificationCount != 1 {
					t.Fatal("source fallback duplicated on replay")
				}
			}
		})
	}
}
