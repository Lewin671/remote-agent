package daemon

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func TestTitleNotifyOnlyUpdatePreservesPlanAndTakesOver(t *testing.T) {
	for _, status := range []string{"scheduled", "running", "paused", "failed", "uncertain"} {
		for _, withTitle := range []bool{false, true} {
			for _, via := range []string{"local", "client", "MCP"} {
				t.Run(status+"/"+map[bool]string{false: "notify", true: "title-notify"}[withTitle]+"/"+via, func(t *testing.T) {
					h := scopedHarness(t, "ask")
					spec := taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600, DelaySeconds: 3600})
					raw, err := scopedSchedule(t, h, "create", "", &spec)
					if err != nil {
						t.Fatal(err)
					}
					task := decodeScopedTask(t, raw)
					state := h.d.tasks[task.ID]
					state.Status = status
					state.Reason = "preserved-reason"
					state.LastCalendarDate = "2026-10-30"
					phase := time.Now().Add(-time.Hour)
					state.IntervalPhase = &phase
					state.ManualRequest = &schedule.ManualRequest{RequestID: "waiting", RequestedAt: time.Now()}
					state.Runs = []schedule.Run{{ID: "previous", FinalSummary: "preserve summary"}}
					if status == "running" {
						state.Active = &schedule.Run{ID: "running", Status: "running", NotifyOnFinish: true, TaskTitle: state.Spec.Title}
					}
					if status == "uncertain" {
						h.d.lateRuns = map[string]string{task.ID: "late"}
					}
					before := schedule.Clone(state)
					want := schedule.Clone(state)
					want.Spec.Notify = "agent_only"
					if withTitle {
						want.Spec.Title = "Changed title"
					}
					want.Revision++
					scope := ""
					origin := originLocal
					switch via {
					case "client":
						origin = &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}
					case "MCP":
						scope = h.s.meta.ID
					}
					if scope == "" {
						want.Takeover = &schedule.Takeover{Session: h.s.meta.ID, Via: via}
						want.Source = via
						want.ScopedSession = ""
						want.ScopedMode = ""
					}
					raw, err = titleUpdateRequest(t, h, before, want.Spec, origin, scope)
					if err != nil {
						t.Fatal(err)
					}
					expected, _ := json.Marshal(want)
					if !bytes.Equal(raw, expected) {
						t.Fatalf("light update replanned or changed a snapshot:\nwant %s\n got %s", expected, raw)
					}
					if status == "uncertain" && h.d.lateRuns[task.ID] != "late" {
						t.Fatal("light update lost late association")
					}
					if err = h.d.loadSchedules(); err != nil {
						t.Fatal(err)
					}
					persisted, _ := json.Marshal(readTask(t, h.d, task.ID))
					if !bytes.Equal(expected, persisted) {
						t.Fatal("light update state not durable")
					}
				})
			}
		}
	}
}

func TestLightNotifyUpdateRejectsInvalidPolicy(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := startScheduledTurn(t, h)
	before := readTask(t, h.d, task.ID)
	spec := before.Spec
	spec.Notify = "invalid"
	if _, err := titleUpdateRequest(t, h, before, spec, originLocal, ""); err == nil {
		t.Fatal("invalid notification policy accepted")
	}
	got, _ := json.Marshal(readTask(t, h.d, task.ID))
	want, _ := json.Marshal(before)
	if !bytes.Equal(got, want) {
		t.Fatal("invalid policy changed the task")
	}
}
