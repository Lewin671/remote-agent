package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func titleUpdateRequest(t *testing.T, h *harness, before *schedule.Task, spec schedule.Spec, origin *pb.Origin, scope string) ([]byte, error) {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return h.d.scheduleCommandScoped(&pb.ScheduleRequest{Operation: "update", TaskId: before.ID, ExpectedRevision: before.Revision, SessionId: h.s.meta.ID, SpecJson: raw}, origin, scope)
}

func TestTitleOnlyUpdatePreservesExecutionState(t *testing.T) {
	for _, status := range []string{"paused", "failed", "failed-at", "uncertain", "running"} {
		for _, origin := range []*pb.Origin{originLocal, {Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}} {
			t.Run(status+"/"+origin.Kind.String(), func(t *testing.T) {
				var h *harness
				var task *schedule.Task
				var run *schedule.Run
				if status == "failed-at" {
					h = scheduleHarness(t)
					now := time.Now()
					task = createTask(t, h, taskSpec(schedule.Trigger{Type: "at", At: &now}), "")
					h.d.tickSchedules(now.Add(time.Second))
					run = readTask(t, h.d, task.ID).Active
					h.d.endScheduledRun(h.d.tasks[task.ID], now, "failed", "execution_failed")
					requestManual(t, h, task.ID)
				} else {
					h, task, run = queuedManualBehindAutomatic(t)
					switch status {
					case "paused":
						if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "pause", TaskId: task.ID}, originLocal); err != nil {
							t.Fatal(err)
						}
						requestManual(t, h, task.ID)
					case "failed":
						h.d.endScheduledRun(h.d.tasks[task.ID], time.Now(), "failed", "execution_failed")
					case "uncertain":
						markUnconfirmed(t, h, run)
						// A late receipt exists but the task has not polled it yet.
						if err := h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_SUBMITTED}}}); err != nil {
							t.Fatal(err)
						}
					case "running":
						if err := h.s.emit(&pb.Event{TurnId: "live", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}, &pb.Event{TurnId: "live", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "Task execution", RequestId: run.RequestID}}}); err != nil {
							t.Fatal(err)
						}
						h.d.tickSchedules(time.Now())
					}
				}
				defer h.s.log.Close()
				if err := h.d.saveSchedulesLocked(); err != nil {
					t.Fatal(err)
				}
				before := readTask(t, h.d, task.ID)
				expectStatus := status
				if status == "failed-at" {
					expectStatus = "failed"
				}
				if before.Status != expectStatus || before.ManualRequest == nil {
					t.Fatalf("fixture did not reach its intended state: %+v", before)
				}
				late, revision := h.d.lateRuns[task.ID], h.d.scheduleRevision
				want := schedule.Clone(before)
				want.Spec.Title = "改名后仍保持原执行状态"
				want.Revision++
				raw, err := titleUpdateRequest(t, h, before, want.Spec, origin, "")
				if err != nil {
					t.Fatalf("title-only update failed: %v", err)
				}
				expected, _ := json.Marshal(want)
				if !bytes.Equal(raw, expected) || h.d.lateRuns[task.ID] != late {
					t.Fatalf("rename changed execution state or ownership:\nwant %s\n got %s", expected, raw)
				}
				if h.d.scheduleRevision != revision+1 {
					t.Fatal("rename did not publish exactly one visible revision")
				}
				if err := h.d.loadSchedules(); err != nil {
					t.Fatal(err)
				}
				persisted, _ := json.Marshal(readTask(t, h.d, task.ID))
				if !bytes.Equal(persisted, expected) || h.d.lateRuns[task.ID] != late {
					t.Fatal("rename or preserved execution state did not survive reload")
				}
				if status == "uncertain" && !h.d.reconfirmScheduledRun(h.d.tasks[task.ID], time.Now()) {
					t.Fatal("rename lost the existing late-confirmation association")
				}
			})
		}
	}
}

func TestTitleOnlyUpdatePreservesDelegation(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "MCP"}[scoped], func(t *testing.T) {
			h, own := admissionFixture(t, false)
			defer h.s.log.Close()
			// Renaming must not freeze a new delegation or transfer ownership.
			setScopedMode(h, "ask")
			before := readTask(t, h.d, own.ID)
			want := schedule.Clone(before)
			want.Spec.Title, want.Revision = "Renamed delegated task", before.Revision+1
			scope := ""
			if scoped {
				scope = h.s.meta.ID
			}
			got, err := titleUpdateRequest(t, h, before, want.Spec, originLocal, scope)
			if err != nil {
				t.Fatal(err)
			}
			expected, _ := json.Marshal(want)
			if !bytes.Equal(got, expected) {
				t.Fatalf("rename changed delegation or another field:\nwant %s\n got %s", expected, got)
			}
		})
	}
}

func TestTitleOnlyUpdateValidatesAndPersists(t *testing.T) {
	for _, fault := range []string{"empty", "oversize", "stale-revision", "write", "directory-sync"} {
		t.Run(fault, func(t *testing.T) {
			h, own := admissionFixture(t, false)
			defer h.s.log.Close()
			before := readTask(t, h.d, own.ID)
			spec := before.Spec
			spec.Title = "A new title"
			switch fault {
			case "empty":
				spec.Title = " \t\n"
			case "oversize":
				spec.Title = strings.Repeat("字", 171)
			case "stale-revision":
				before.Revision--
			case "write":
				file := filepath.Join(paths.Dir(), "schedules.json")
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
			case "directory-sync":
				h.d.syncStateDir = failStateSync()
			}
			original, _ := json.Marshal(h.d.tasks[own.ID])
			_, err := titleUpdateRequest(t, h, before, spec, originLocal, "")
			if err == nil {
				t.Fatal("invalid or failed rename was reported successful")
			}
			if fault == "stale-revision" && connect.CodeOf(err) != connect.CodeAborted {
				t.Fatalf("lost revision conflict: %v", err)
			}
			h.d.syncStateDir = nil
			if fault == "directory-sync" {
				if err := h.d.loadSchedules(); err != nil {
					t.Fatal(err)
				}
				if h.d.tasks[own.ID].Spec.Title != spec.Title || h.d.tasks[own.ID].Revision != before.Revision+1 {
					t.Fatal("replaced file's rename was incorrectly rolled back")
				}
			} else {
				current, _ := json.Marshal(h.d.tasks[own.ID])
				if !bytes.Equal(original, current) {
					t.Fatal("rejected or unpersisted rename changed the task")
				}
			}
		})
	}
}

func TestOtherDefinitionChangesKeepUpdateSemantics(t *testing.T) {
	for _, field := range []string{"message", "trigger", "target", "late_seconds", "active"} {
		t.Run(field, func(t *testing.T) {
			h, own := admissionFixture(t, false)
			defer h.s.log.Close()
			before := readTask(t, h.d, own.ID)
			spec := before.Spec
			spec.Title = "Title plus an execution change"
			switch field {
			case "message", "active":
				spec.Message += " revised"
			case "trigger":
				spec.Trigger.IntervalSeconds *= 2
			case "target":
				h.d.agents[pb.Agent_AGENT_CODEX] = &scheduleArgvBarrier{Adapter: h.s.ag}
				spec.Target = schedule.Target{Type: "new_session", Agent: "codex", Cwd: h.dir, Mode: "full"}
			case "late_seconds":
				spec.LateSeconds = 60
			}
			if field == "active" {
				h.d.tasks[own.ID].Active = &schedule.Run{ID: "live", Status: "running"}
			}
			raw, err := titleUpdateRequest(t, h, before, spec, &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}, "")
			if field == "active" {
				if err == nil || h.d.tasks[own.ID].Spec.Message != before.Spec.Message {
					t.Fatal("active definition replacement was allowed or changed the task")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got schedule.Task
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != "scheduled" || got.Reason != "" || got.NextRunAt == nil || got.ScopedSession != "" || got.Source != "client" {
				t.Fatalf("%s change was incorrectly treated as rename: %+v", field, got)
			}
			actual, _ := json.Marshal(got.Spec)
			expected, _ := json.Marshal(spec)
			if !bytes.Equal(actual, expected) {
				t.Fatal("definition replacement lost a changed field")
			}
		})
	}
}
