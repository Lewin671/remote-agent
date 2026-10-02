package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func scopedSchedule(t *testing.T, h *harness, op, id string, spec *schedule.Spec) ([]byte, error) {
	t.Helper()
	q := connect.NewRequest(&pb.ScheduleRequest{Operation: op, TaskId: id, SessionId: h.s.meta.ID})
	if spec != nil {
		q.Msg.SpecJson, _ = json.Marshal(spec)
	}
	q.Header().Set(schedule.MCPScopeHeader, h.s.meta.ID)
	r, err := (&handler{d: h.d}).Schedule(context.Background(), q)
	if err != nil {
		return nil, err
	}
	return r.Msg.ResultJson, nil
}
func scopedHarness(t *testing.T, mode string) *harness {
	h := scheduleHarness(t)
	t.Cleanup(func() { h.s.log.Close() })
	h.d.agents[pb.Agent_AGENT_CODEX] = codex.New("codex", h.dir, h.d.cfg.HistoryPath)
	setScopedMode(h, mode)
	return h
}
func setScopedMode(h *harness, mode string) {
	c := &pb.TurnContext{ApprovalsReviewer: "user", ApprovalPolicy: "on-request", Sandbox: "workspace-write"}
	switch mode {
	case "full":
		c.ApprovalPolicy = "never"
		c.Sandbox = "danger-full-access"
	case "plan":
		c.CollaborationMode = "plan"
	case "custom":
		c.Sandbox = "read-only"
	case "unknown":
		c = &pb.TurnContext{}
	}
	h.s.mu.Lock()
	h.s.context = c
	h.s.mu.Unlock()
}
func scopedNewSpec(h *harness, mode string) schedule.Spec {
	s := taskSpec(schedule.Trigger{Type: "after_turn", DelaySeconds: 60})
	s.Target = schedule.Target{Type: "new_session", Mode: mode, Cwd: h.dir, Agent: "codex"}
	return s
}
func decodeScopedTask(t *testing.T, b []byte) *schedule.Task {
	t.Helper()
	out := &schedule.Task{}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMCPScheduleCannotEscalateOrCrossTargets(t *testing.T) {
	h := scopedHarness(t, "ask")
	for _, tc := range []struct {
		name   string
		target schedule.Target
	}{
		{"new full", schedule.Target{Type: "new_session", Mode: "full"}},
		{"different cwd", schedule.Target{Type: "new_session", Cwd: t.TempDir()}},
		{"different CLI", schedule.Target{Type: "new_session", Agent: "claude"}},
		{"different session", schedule.Target{Type: "resume_session", Session: "other-full-session"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := taskSpec(schedule.Trigger{Type: "after_turn"})
			s.Target = tc.target
			if _, err := scopedSchedule(t, h, "create", "", &s); connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Fatalf("MCP scope bypass: %v", err)
			}
		})
	}
	// The same machine-level user request remains authorized. This is an MCP
	// capability bound, not a claim to isolate processes owned by the user.
	s := scopedNewSpec(h, "full")
	b, _ := json.Marshal(s)
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: b}, originLocal); err != nil {
		t.Fatalf("user request blocked: %v", err)
	}
	// Omitted new-session mode is frozen to the source, never daemon defaults.
	s = scopedNewSpec(h, "")
	out, err := scopedSchedule(t, h, "create", "", &s)
	if err != nil {
		t.Fatal(err)
	}
	task := decodeScopedTask(t, out)
	if task.Spec.Target.Mode != "ask" || task.ScopedSession != h.s.meta.ID {
		t.Fatalf("unscoped defaults: %+v", task)
	}
}

func TestMCPScheduleRejectsPlanUnknownAndCustomModes(t *testing.T) {
	for _, mode := range []string{"plan", "unknown", "custom"} {
		t.Run(mode, func(t *testing.T) {
			h := scopedHarness(t, mode)
			for _, s := range []schedule.Spec{scopedNewSpec(h, "ask"), taskSpec(schedule.Trigger{Type: "after_turn"})} {
				if _, err := scopedSchedule(t, h, "create", "", &s); connect.CodeOf(err) != connect.CodePermissionDenied {
					t.Fatalf("nonstandard mode widened: %v", err)
				}
			}
		})
	}
}

func TestMCPScheduleOwnershipCoversEveryOperation(t *testing.T) {
	h := scopedHarness(t, "ask")
	spec := taskSpec(schedule.Trigger{Type: "after_turn", DelaySeconds: 60})
	ownBytes, err := scopedSchedule(t, h, "create", "", &spec)
	if err != nil {
		t.Fatal(err)
	}
	own := decodeScopedTask(t, ownBytes)
	foreign := createTask(t, h, spec, "") // machine-level user task, even in same project
	for _, op := range []string{"get", "update", "pause", "resume", "cancel", "run"} {
		if _, err := scopedSchedule(t, h, op, foreign.ID, &spec); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("%s read/changed user task: %v", op, err)
		}
	}
	out, err := scopedSchedule(t, h, "list", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Tasks []*schedule.Task `json:"tasks"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tasks) != 1 || list.Tasks[0].ID != own.ID {
		t.Fatalf("task disclosure: %+v", list)
	}
	changed := scopedNewSpec(h, "full")
	if _, err := scopedSchedule(t, h, "update", own.ID, &changed); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("update escalated: %v", err)
	}
	if readTask(t, h.d, own.ID).Spec.Target.Type != "resume_session" {
		t.Fatal("rejected update persisted")
	}
}

func TestMCPScheduledDispatchRechecksNarrowedPermissions(t *testing.T) {
	h := scopedHarness(t, "full")
	spec := scopedNewSpec(h, "full")
	out, err := scopedSchedule(t, h, "create", "", &spec)
	if err != nil {
		t.Fatal(err)
	}
	task := decodeScopedTask(t, out)
	setScopedMode(h, "ask")
	if _, err := scopedSchedule(t, h, "run", task.ID, nil); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("run escalated after downgrade: %v", err)
	}
	if _, err := scopedSchedule(t, h, "pause", task.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := scopedSchedule(t, h, "resume", task.ID, nil); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("resume escalated after downgrade: %v", err)
	}
	// Model a due stored task without calling a user-level resume/adoption.
	h.d.scheduleMu.Lock()
	h.d.tasks[task.ID].Status = "scheduled"
	h.d.scheduleMu.Unlock()
	h.d.tickSchedules(task.NextRunAt.Add(time.Second))
	got := readTask(t, h.d, task.ID)
	if got.Status != "paused" || got.Reason != "agent_schedule_permissions_changed" || got.Dispatches != 0 {
		t.Fatalf("unsafe delayed dispatch: %+v", got)
	}
}

func TestLegacyAgentSchedulesRequireMachineConfirmation(t *testing.T) {
	h := scopedHarness(t, "ask")
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn", DelaySeconds: 60}), "")
	h.d.scheduleMu.Lock()
	h.d.tasks[task.ID].Source = "agent"
	if err := h.d.saveSchedulesLocked(); err != nil {
		t.Fatal(err)
	}
	h.d.scheduleMu.Unlock()
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	got := readTask(t, h.d, task.ID)
	if got.Status != "paused" || got.Reason != "legacy_agent_task_requires_confirmation" {
		t.Fatalf("legacy unsafe task not quarantined: %+v", got)
	}
	if _, err := scopedSchedule(t, h, "resume", task.ID, nil); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("MCP adopted legacy task: %v", err)
	}
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "resume", TaskId: task.ID}, originLocal); err != nil {
		t.Fatal(err)
	}
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	if got := readTask(t, h.d, task.ID); got.Status != "scheduled" || got.Source != "local" {
		t.Fatalf("confirmed legacy task requarantined: %+v", got)
	}
}

func TestLegacyMCPRequestsCannotBypassScope(t *testing.T) {
	h := scopedHarness(t, "ask")
	spec := scopedNewSpec(h, "full")
	b, _ := json.Marshal(spec)
	q := connect.NewRequest(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: b})
	_, err := (&handler{d: h.d}).Schedule(context.Background(), q)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("legacy unscoped MCP allowed: %v", err)
	}
	q.Header().Set(schedule.CallerHeader, "cli")
	if _, err := (&handler{d: h.d}).Schedule(context.Background(), q); err != nil {
		t.Fatalf("explicit machine user blocked: %v", err)
	}
	q.Header().Del(schedule.CallerHeader)
	ctx := agent.WithOrigin(context.Background(), &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "user-device"})
	if _, err := (&handler{d: h.d}).Schedule(ctx, q); err != nil {
		t.Fatalf("remote machine user blocked: %v", err)
	}
}

func TestLegacyActiveExecutionCannotClearQuarantine(t *testing.T) {
	h := scopedHarness(t, "ask")
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn", Repeat: true}), "")
	now := time.Now().Add(time.Second)
	h.d.tickSchedules(now)
	task = readTask(t, h.d, task.ID)
	if task.Active == nil {
		t.Fatal("execution not queued")
	}
	h.d.scheduleMu.Lock()
	h.d.tasks[task.ID].Source = "agent"
	if err := h.d.saveSchedulesLocked(); err != nil {
		t.Fatal(err)
	}
	h.d.scheduleMu.Unlock()
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	assertQuarantined := func() {
		t.Helper()
		got := readTask(t, h.d, task.ID)
		if got.Status != "paused" || got.Reason != "legacy_agent_task_requires_confirmation" || got.Dispatches != 1 {
			t.Fatalf("quarantine cleared without user confirmation: %+v", got)
		}
	}
	assertQuarantined()
	for _, paused := range []bool{true, false} {
		h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: task.Active.RequestID, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Paused: paused}}})
		h.d.tickSchedules(now)
		assertQuarantined()
	}
	completeTask(h, task.Active, now)
	h.d.tickSchedules(now)
	assertQuarantined()
	h.d.tickSchedules(now.Add(time.Hour))
	assertQuarantined()
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(now.Add(2 * time.Hour))
	assertQuarantined()
}

func TestLegacyDispatchGateRejectsOverwrittenPause(t *testing.T) {
	h := scopedHarness(t, "ask")
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	h.d.scheduleMu.Lock()
	h.d.tasks[task.ID].Source = "agent"
	h.d.tasks[task.ID].Status = "scheduled"
	h.d.tasks[task.ID].Reason = ""
	h.d.scheduleMu.Unlock()
	h.d.tickSchedules(task.NextRunAt.Add(time.Second))
	got := readTask(t, h.d, task.ID)
	if got.Status != "paused" || got.Reason != "legacy_agent_task_requires_confirmation" || got.Dispatches != 0 || len(h.s.queue.Queue) != 0 {
		t.Fatalf("unscoped legacy definition reached session: %+v", got)
	}
}
