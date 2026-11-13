package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

type scheduleStatusTerm struct {
	*fakeTerm
	refresh func(context.Context) error
}

func (t scheduleStatusTerm) RefreshView(ctx context.Context) error { return t.refresh(ctx) }

func TestNativeScheduledDispatchRefreshesEvidenceWithoutBroadeningPermissions(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"unchanged", "changed", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			h := agyHarness(t)
			now := time.Now().UTC()
			report := func(mode string) *agent.StatusLine {
				return &agent.StatusLine{CycleMode: mode, IdleKnown: true, Context: &pb.TurnContext{ApprovalPolicy: "request-review", PermissionMode: mode, Sandbox: "disabled"}}
			}
			h.s.recordStatusLine(report("default"), now)
			task := &schedule.Task{ID: "native-schedule", Status: "scheduled", ScopedSession: h.s.meta.ID, SourceSession: h.s.meta.ID, NextRunAt: &now, Spec: schedule.Spec{Trigger: schedule.Trigger{Type: "interval", IntervalSeconds: 300}, Target: schedule.Target{Type: "new_session"}}}
			if err := h.d.restrictMCPTask(task, true); err != nil {
				t.Fatal(err)
			}
			h.d.tasks = map[string]*schedule.Task{task.ID: task}
			h.s.statusLineAt = now.Add(-time.Minute)
			refreshes := 0
			h.s.term = scheduleStatusTerm{h.term, func(context.Context) error {
				refreshes++
				if !h.d.scheduleMu.TryLock() {
					t.Fatal("native refresh ran under schedule lock")
				}
				h.d.scheduleMu.Unlock()
				if !h.s.readMu.TryLock() {
					t.Fatal("native refresh ran under transcript lock")
				}
				h.s.readMu.Unlock()
				if outcome == "unavailable" {
					return errors.New("no native status")
				}
				mode := "default"
				if outcome == "changed" {
					mode = "auto"
				}
				h.s.recordStatusLine(report(mode), time.Now())
				return nil
			}}
			jobs := h.d.prepareScheduledDispatches(now)
			if refreshes != 1 {
				t.Fatalf("refreshes = %d", refreshes)
			}
			switch outcome {
			case "unchanged":
				if len(jobs) != 1 || task.Status != "dispatching" {
					t.Fatalf("fresh unchanged task was not dispatched: %+v", task)
				}
			case "changed":
				if len(jobs) != 0 || task.Status != "paused" || task.Reason != "agent_schedule_permissions_changed" {
					t.Fatalf("changed permissions were admitted: %+v", task)
				}
			case "unavailable":
				if len(jobs) != 0 || task.Status != "scheduled" || task.Reason != "" || task.Active != nil {
					t.Fatalf("missing evidence paused or dispatched the task: %+v", task)
				}
			}
		})
	}
}

func TestNativeManualRequestWaitsForFreshEvidence(t *testing.T) {
	t.Parallel()
	h := agyHarness(t)
	now := time.Now().UTC()
	report := &agent.StatusLine{CycleMode: "default", IdleKnown: true, Context: &pb.TurnContext{ApprovalPolicy: "request-review", PermissionMode: "default", Sandbox: "disabled"}}
	h.s.recordStatusLine(report, now)
	task := &schedule.Task{ID: "native-manual", Status: "scheduled", ScopedSession: h.s.meta.ID, SourceSession: h.s.meta.ID, Spec: schedule.Spec{Trigger: schedule.Trigger{Type: "interval", IntervalSeconds: 300}, Target: schedule.Target{Type: "new_session"}}}
	if err := h.d.restrictMCPTask(task, true); err != nil {
		t.Fatal(err)
	}
	task.ManualRequest = &schedule.ManualRequest{RequestID: "manual-run", RequestedAt: now}
	h.d.tasks = map[string]*schedule.Task{task.ID: task}
	h.s.statusLineAt = now.Add(-time.Minute)
	ready := false
	h.s.term = scheduleStatusTerm{h.term, func(context.Context) error {
		if !ready {
			return errors.New("temporarily unavailable")
		}
		h.s.recordStatusLine(report, time.Now())
		return nil
	}}
	if jobs := h.d.prepareScheduledDispatches(now); len(jobs) != 0 || task.ManualRequest == nil || task.ManualRequest.RequestID != "manual-run" || len(task.Runs) != 0 {
		t.Fatal("a temporary evidence gap consumed the accepted manual request")
	}
	ready = true
	if jobs := h.d.prepareScheduledDispatches(now); len(jobs) != 1 || task.ManualRequest != nil || task.Active == nil || !task.Active.Manual {
		t.Fatal("the retained manual request did not dispatch after refresh")
	}
}

func TestExitedNativeSourceUsesExactDurableSchedulePermissions(t *testing.T) {
	for _, target := range []string{"new_session", "resume_session"} {
		for _, state := range []string{"unchanged", "changed", "unverified"} {
			t.Run(target+"/"+state, func(t *testing.T) {
				h := agyHarness(t)
				now := time.Now().UTC()
				report := &agent.StatusLine{CycleMode: "default", IdleKnown: true, Context: &pb.TurnContext{ApprovalPolicy: "request-review", PermissionMode: "default", Sandbox: "disabled"}}
				h.s.recordStatusLine(report, now)
				task := &schedule.Task{ID: "exited-native-source", Status: "scheduled", ScopedSession: h.s.meta.ID, SourceSession: h.s.meta.ID, NextRunAt: &now, Spec: schedule.Spec{Trigger: schedule.Trigger{Type: "interval", IntervalSeconds: 300}, Target: schedule.Target{Type: target, Session: "current"}}}
				if err := h.d.restrictMCPTask(task, true); err != nil {
					t.Fatal(err)
				}
				if state == "changed" {
					report.Context.PermissionMode, report.CycleMode = "auto", "auto"
					h.s.recordStatusLine(report, now.Add(time.Second))
				}
				meta := h.s.meta
				meta.Exited = true
				if state == "unverified" {
					meta.ExpectedConversation = "conv"
				}
				if err := h.s.log.Close(); err != nil {
					t.Fatal(err)
				}
				h.open(meta)
				h.d.sessions[meta.ID] = h.s
				h.d.tasks = map[string]*schedule.Task{task.ID: task}
				h.s.term = scheduleStatusTerm{h.term, func(context.Context) error { t.Fatal("exited source requested live status"); return nil }}
				jobs := h.d.prepareScheduledDispatches(now)
				if state == "unchanged" {
					if len(jobs) != 1 || task.Status != "dispatching" {
						t.Fatalf("valid exited delegation was not dispatched: %+v", task)
					}
				} else if len(jobs) != 0 || task.Status != "paused" || task.Reason != "agent_schedule_permissions_changed" {
					t.Fatalf("changed or unverified exited permissions were authorized: %+v", task)
				}
			})
		}
	}
}
