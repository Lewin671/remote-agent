package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/Lewin671/remote-agent/internal/tail"
)

func admissionFixture(t *testing.T, claude bool) (*harness, *schedule.Task) {
	t.Helper()
	h := scheduleTranscriptHarness(t, claude)
	if claude {
		if err := h.s.emit(&pb.Event{Kind: &pb.Event_TurnContext{TurnContext: &pb.TurnContext{PermissionMode: "bypassPermissions"}}}); err != nil {
			t.Fatal(err)
		}
	} else {
		setScopedMode(h, "full")
	}
	spec := taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600, DelaySeconds: 3600})
	raw, err := scopedSchedule(t, h, "create", "", &spec)
	if err != nil {
		t.Fatal(err)
	}
	own := decodeScopedTask(t, raw)
	if _, err = scopedSchedule(t, h, "pause", own.ID, nil); err != nil {
		t.Fatal(err)
	}
	return h, own
}

func admissionRequest(t *testing.T, h *harness, own *schedule.Task, op string) error {
	t.Helper()
	spec := own.Spec
	spec.Title = "Admission request"
	raw, _ := json.Marshal(spec)
	if op == "notify_user" {
		raw = []byte(`{"text":"Useful result"}`)
	}
	req := connect.NewRequest(&pb.ScheduleRequest{Operation: op, SessionId: h.s.meta.ID, TaskId: own.ID, SpecJson: raw})
	req.Header().Set(schedule.MCPScopeHeader, h.s.meta.ID)
	_, err := (&handler{d: h.d}).Schedule(context.Background(), req)
	return err
}

func TestMCPAdmissionReadsTranscriptBeforeEveryMutation(t *testing.T) {
	for _, claude := range []bool{false, true} {
		for _, op := range []string{"create", "update", "resume", "run"} {
			t.Run(map[bool]string{false: "codex", true: "claude"}[claude]+"/"+op, func(t *testing.T) {
				h, own := admissionFixture(t, claude)
				defer h.s.log.Close()
				native := writeScheduledTranscriptInput(t, h, "scheduled", claude)
				if h.s.scheduledTask != "" || h.s.meta.ScheduledTaskID != "" {
					t.Fatal("test must start with an ordinary resumed session and unimported transcript")
				}
				before := visibleSchedules(h.d.tasks)
				err := admissionRequest(t, h, own, op)
				if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "during a scheduled execution") {
					t.Fatalf("pre-poll %s escaped scheduling restriction: %v", op, err)
				}
				if h.s.scheduledTask != native.ID || h.s.scheduledRun != native.Active.ID {
					t.Fatal("admission failed to refresh correct scheduled context")
				}
				if !bytes.Equal(before, visibleSchedules(h.d.tasks)) {
					t.Fatal("rejected MCP operation changed tasks")
				}
			})
		}
	}
}

func TestAllScheduleOperationsRequireCommittedAdmission(t *testing.T) {
	for _, fault := range []string{"read", "commit"} {
		for _, op := range []string{"create", "update", "resume", "run", "list", "get", "pause", "cancel", "notify_user"} {
			t.Run(fault+"/"+op, func(t *testing.T) {
				h, own := admissionFixture(t, false)
				defer func() { h.s.log.Close() }()
				writeScheduledTranscriptInput(t, h, "old", false)
				if !h.s.readRollout() {
					t.Fatal("could not establish cached context")
				}
				writeTranscriptEnd(h, "old", false)
				writeTranscriptInput(h, "human", "Human request", false)
				reader := h.s.reader
				before := visibleSchedules(h.d.tasks)
				if fault == "read" {
					h.s.reader = tail.NewReader(h.dir, 0)
				} else if err := h.s.log.Close(); err != nil {
					t.Fatal(err)
				}
				err := admissionRequest(t, h, own, op)
				if connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "retry "+op) {
					t.Fatalf("%s used stale state after %s failure: %v", op, fault, err)
				}
				if !bytes.Equal(before, visibleSchedules(h.d.tasks)) || h.s.scheduledNotifications != 0 {
					t.Fatal("failed admission changed task state or notified")
				}
				if fault == "read" {
					h.s.reader = reader
				} else {
					h.s.log, err = eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
					if err != nil {
						t.Fatal(err)
					}
				}
				err = admissionRequest(t, h, own, op)
				if op == "notify_user" {
					if err == nil || !strings.Contains(err.Error(), "ordinary turns") {
						t.Fatalf("human context recovery accepted notification: %v", err)
					}
				} else if err != nil {
					t.Fatalf("recovered human context still rejected %s: %v", op, err)
				}
				if h.s.scheduledTask != "" {
					t.Fatal("recovery did not commit human input")
				}
			})
		}
	}
}

func TestContextRequestPathsShareAdmission(t *testing.T) {
	for _, op := range []string{"interrupt", "hook", "settlement"} {
		t.Run(op+"/fresh", func(t *testing.T) {
			h := scheduleTranscriptHarness(t, true)
			defer h.s.log.Close()
			native := writeScheduledTranscriptInput(t, h, "scheduled", true)
			switch op {
			case "interrupt":
				res, err := (&handler{d: h.d}).Interrupt(context.Background(), connect.NewRequest(&pb.InterruptRequest{SessionId: h.s.meta.ID}))
				if err != nil || !res.Msg.Interrupted {
					t.Fatalf("interrupt missed unimported turn: %v", err)
				}
			case "hook":
				_, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "PreToolUse", ToolName: "AskUserQuestion", ToolUseID: "q", PromptID: "scheduled", ToolInput: json.RawMessage(`{"questions":[{"question":"Choose?","options":[{"label":"One"},{"label":"Two"}]}]}`)})
				if err != nil {
					t.Fatal(err)
				}
			case "settlement":
				h.s.meta.Exited = true
				if err := h.s.settleExited(); err != nil {
					t.Fatal(err)
				}
				ends := 0
				for _, e := range h.events() {
					if end := e.GetTurnEnded(); end != nil {
						ends++
						if end.ScheduledTaskId != native.ID || end.ScheduledRunId != native.Active.ID {
							t.Fatalf("settlement used stale turn annotation: %v", end)
						}
					}
				}
				if ends != 1 {
					t.Fatal("settlement missed unimported turn")
				}
			}
			if h.s.scheduledTask != native.ID || h.s.scheduledRun != native.Active.ID {
				t.Fatalf("%s bypassed shared admission", op)
			}
		})
		t.Run(op+"/unavailable", func(t *testing.T) {
			h := scheduleTranscriptHarness(t, true)
			defer h.s.log.Close()
			h.s.reader = tail.NewReader(h.dir, 0)
			var err error
			switch op {
			case "interrupt":
				_, err = h.s.interruptBy(originLocal)
			case "hook":
				_, err = h.s.hook(context.Background(), &agent.Hook{HookEventName: "PreToolUse", ToolName: "ExitPlanMode", ToolUseID: "plan", ToolInput: json.RawMessage(`{}`)})
			case "settlement":
				h.s.meta.Exited = true
				err = h.s.settleExited()
			}
			if op == "interrupt" {
				if err != nil {
					t.Fatalf("cached idle interrupt failed: %v", err)
				}
			} else if connect.CodeOf(err) != connect.CodeUnavailable {
				t.Fatalf("%s ignored transcript failure: %v", op, err)
			}
			if len(h.term.written()) != 0 {
				t.Fatal("failed admission sent terminal control")
			}
		})
	}
}

func TestOwnerScheduleKeepsAuthorityAfterFreshAdmission(t *testing.T) {
	h, _ := admissionFixture(t, false)
	defer h.s.log.Close()
	native := writeScheduledTranscriptInput(t, h, "scheduled", false)
	spec := taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600, DelaySeconds: 3600})
	raw, _ := json.Marshal(spec)
	_, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: raw}, originLocal)
	if err != nil {
		t.Fatal(err)
	}
	if h.s.scheduledTask != native.ID {
		t.Fatal("owner source session was not refreshed")
	}
	h.s.reader = tail.NewReader(h.dir, 0)
	_, err = h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "list", SessionId: h.s.meta.ID}, originLocal)
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("bound local request bypassed admission: %v", err)
	}
	// Machine-wide history queries have no live session context or model restriction.
	if _, err = h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "list"}, originLocal); err != nil {
		t.Fatal(err)
	}
}

func TestScheduledContextKeepsInspectionAndStoppingAvailable(t *testing.T) {
	for _, op := range []string{"list", "get", "pause", "cancel"} {
		t.Run(op, func(t *testing.T) {
			h, own := admissionFixture(t, false)
			defer h.s.log.Close()
			native := writeScheduledTranscriptInput(t, h, "scheduled", false)
			if err := admissionRequest(t, h, own, op); err != nil {
				t.Fatalf("scheduled %s should remain available: %v", op, err)
			}
			if h.s.scheduledTask != native.ID {
				t.Fatal("allowed operation did not refresh context")
			}
		})
	}
}

func TestHookStartAndPermissionRequireAdmission(t *testing.T) {
	for _, event := range []string{"SessionStart", "PermissionRequest"} {
		t.Run(event, func(t *testing.T) {
			h := scheduleTranscriptHarness(t, true)
			defer h.s.log.Close()
			h.s.reader = tail.NewReader(h.dir, 0)
			h.d.cfg.ApprovalTimeout = 50 * time.Millisecond
			_, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: event, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"true"}`)})
			if connect.CodeOf(err) != connect.CodeUnavailable {
				t.Fatalf("%s ignored failed admission: %v", event, err)
			}
		})
	}
}
