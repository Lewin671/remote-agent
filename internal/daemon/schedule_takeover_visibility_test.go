package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func TestMCPTakenOverTasksVisibleButReadOnly(t *testing.T) {
	for _, via := range []string{"client", "local"} {
		t.Run(via, func(t *testing.T) {
			h := scopedHarness(t, "ask")
			spec := taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600, DelaySeconds: 3600})
			raw, err := scopedSchedule(t, h, "create", "", &spec)
			if err != nil {
				t.Fatal(err)
			}
			task := decodeScopedTask(t, raw)
			origin := originLocal
			if via == "client" {
				origin = &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}
			}
			if _, err = h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: task.ID}, origin); err != nil {
				t.Fatal(err)
			}
			// A later machine edit can change SourceSession, but never the creator evidence.
			h.d.tasks[task.ID].SourceSession = "another-source"
			if err = h.d.saveSchedulesLocked(); err != nil {
				t.Fatal(err)
			}
			if err = h.d.loadSchedules(); err != nil {
				t.Fatal(err)
			}
			raw, err = scopedSchedule(t, h, "list", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			var list struct {
				Tasks []schedule.Task `json:"tasks"`
			}
			if err = json.Unmarshal(raw, &list); err != nil {
				t.Fatal(err)
			}
			if len(list.Tasks) != 1 || !list.Tasks[0].MCPReadOnly || list.Tasks[0].Takeover.Via != via {
				t.Fatalf("missing read-only takeover: %s", raw)
			}
			for _, op := range []string{"get", "update", "pause", "resume", "run", "cancel"} {
				_, err = scopedSchedule(t, h, op, task.ID, &spec)
				if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "transferred it to user management") || !strings.Contains(err.Error(), "remote-agent schedule") || !strings.Contains(err.Error(), via) {
					t.Fatalf("%s: %v", op, err)
				}
			}
			// Other sessions must neither discover nor identify the task's takeover.
			raw, err = h.d.scheduleCommandScoped(&pb.ScheduleRequest{Operation: "list", SessionId: "other"}, originLocal, "other")
			if strings.Contains(string(raw), task.ID) {
				t.Fatalf("cross-session list: %s %v", raw, err)
			}
			_, err = h.d.scheduleCommandScoped(&pb.ScheduleRequest{Operation: "get", TaskId: task.ID, SessionId: "other"}, originLocal, "other")
			if err == nil || strings.Contains(err.Error(), "transferred") {
				t.Fatalf("cross-session get: %v", err)
			}
		})
	}
}
