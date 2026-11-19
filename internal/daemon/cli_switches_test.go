package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func TestCliSwitchesPersistenceConflictAndEnforcement(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, Config{
		Agents: map[pb.Agent]AgentConfig{
			pb.Agent_AGENT_CODEX:  {Bin: "/usr/bin/true"},
			pb.Agent_AGENT_CLAUDE: {Bin: "/usr/bin/true"},
		},
	})
	d := h.d
	rpc := &handler{d: d}
	ctx := context.Background()

	meta0 := d.MachineMeta()
	if !meta0.GetCapabilities().GetCliSwitches() || !meta0.GetCapabilities().GetUsageStats() {
		t.Fatalf("expected cli_switches and usage_stats capabilities enabled: %+v", meta0.GetCapabilities())
	}

	// Disable Claude atomically (wire requests from clientcore omit Settings).
	agClaude := pb.Agent_AGENT_CLAUDE
	enFalse := false
	res, err := rpc.UpdateSettings(ctx, connect.NewRequest(&pb.UpdateSettingsRequest{
		ToggleAgent:   &agClaude,
		ToggleEnabled: &enFalse,
	}))
	if err != nil {
		t.Fatalf("disable claude failed: %v", err)
	}
	if len(res.Msg.Settings.DisabledAgents) != 1 || res.Msg.Settings.DisabledAgents[0] != pb.Agent_AGENT_CLAUDE || res.Msg.Settings.Revision != 1 {
		t.Fatalf("unexpected settings after disable: %+v", res.Msg.Settings)
	}
	if len(d.MachineMeta().DisabledAgents) != 1 || d.MachineMeta().SettingsRevision != 1 {
		t.Fatalf("MachineMeta did not reflect disabled agent / revision: %+v", d.MachineMeta())
	}

	// ToggleAgent without ToggleEnabled is rejected cleanly as InvalidArgument.
	if _, err := rpc.UpdateSettings(ctx, connect.NewRequest(&pb.UpdateSettingsRequest{
		ToggleAgent: &agClaude,
	})); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected InvalidArgument when ToggleEnabled omitted, got %v", err)
	}

	// Non-toggle UpdateSettings preserves disabled_agents instead of resetting it.
	resPreserve, err := rpc.UpdateSettings(ctx, connect.NewRequest(&pb.UpdateSettingsRequest{
		Settings: &pb.DaemonSettings{IdleCloseMinutes: 120},
	}))
	if err != nil {
		t.Fatalf("non-toggle UpdateSettings failed: %v", err)
	}
	if len(resPreserve.Msg.Settings.DisabledAgents) != 1 || resPreserve.Msg.Settings.DisabledAgents[0] != pb.Agent_AGENT_CLAUDE || resPreserve.Msg.Settings.Revision != 2 {
		t.Fatalf("non-toggle UpdateSettings wiped disabled_agents: %+v", resPreserve.Msg.Settings)
	}

	// Attempting to disable the last remaining CLI (Codex) must fail with FailedPrecondition.
	agCodex := pb.Agent_AGENT_CODEX
	_, err = rpc.UpdateSettings(ctx, connect.NewRequest(&pb.UpdateSettingsRequest{
		ToggleAgent:   &agCodex,
		ToggleEnabled: &enFalse,
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition || !errors.Is(err, errCannotDisableLastAgent) {
		t.Fatalf("expected cannot_disable_last_agent FailedPrecondition, got %v", err)
	}

	// Creating a session or new_session schedule for disabled Claude fails with agent_disabled.
	_, err = d.createBy(&pb.CreateSessionRequest{Cwd: dir, Agent: pb.Agent_AGENT_CLAUDE}, originLocal, false, false, "PATH")
	if err == nil || !errors.Is(err, errAgentDisabled) {
		t.Fatalf("expected agent_disabled on CreateSession for disabled Claude, got %v", err)
	}
	_, err = d.scheduleCommand(&pb.ScheduleRequest{
		Operation: "create",
		SpecJson:  []byte(`{"title":"disabled","message":"hi","target":{"type":"new_session","agent":"claude","cwd":"` + filepath.ToSlash(dir) + `","mode":"ask"},"trigger":{"type":"at","at":"2027-01-01T00:00:00Z"}}`),
	}, originLocal)
	if err == nil || !errors.Is(err, errAgentDisabled) {
		t.Fatalf("expected agent_disabled on schedule create for disabled Claude, got %v", err)
	}

	// Concurrent toggle race: two goroutines simultaneously try to disable the only two enabled CLIs.
	enTrue := true
	if _, err := rpc.UpdateSettings(ctx, connect.NewRequest(&pb.UpdateSettingsRequest{
		Settings:      &pb.DaemonSettings{},
		ToggleAgent:   &agClaude,
		ToggleEnabled: &enTrue,
	})); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, ag := range []pb.Agent{pb.Agent_AGENT_CODEX, pb.Agent_AGENT_CLAUDE} {
		wg.Add(1)
		go func(target pb.Agent) {
			defer wg.Done()
			f := false
			_, e := rpc.UpdateSettings(ctx, connect.NewRequest(&pb.UpdateSettingsRequest{
				Settings:      &pb.DaemonSettings{},
				ToggleAgent:   &target,
				ToggleEnabled: &f,
			}))
			errs <- e
		}(ag)
	}
	wg.Wait()
	close(errs)
	var okCnt, errCnt int
	for e := range errs {
		if e == nil {
			okCnt++
		} else {
			errCnt++
		}
	}
	if okCnt != 1 || errCnt != 1 {
		t.Fatalf("concurrent last-2 disable should let 1 succeed and reject 1, got ok=%d err=%d", okCnt, errCnt)
	}

	// Simulate an existing scheduled new_session task whose CLI gets disabled afterward -> dispatch pauses the task cleanly.
	task := &schedule.Task{
		ID:       "t-disabled",
		Revision: 1,
		Status:   "dispatching",
		Spec: schedule.Spec{
			Title:   "will pause",
			Message: "run check",
			Target:  schedule.Target{Type: "new_session", Agent: "claude", Cwd: dir, Mode: "ask"},
			Trigger: schedule.Trigger{Type: "interval", IntervalSeconds: 3600},
		},
		Active: &schedule.Run{ID: "run-1", Status: "dispatching", TargetType: "new_session", StartedAt: time.Now().UTC()},
	}
	// Ensure Claude is currently disabled.
	if !d.isAgentDisabled(pb.Agent_AGENT_CLAUDE) {
		if _, err := rpc.UpdateSettings(ctx, connect.NewRequest(&pb.UpdateSettingsRequest{
			Settings:      &pb.DaemonSettings{},
			ToggleAgent:   &agClaude,
			ToggleEnabled: &enFalse,
		})); err != nil {
			t.Fatal(err)
		}
	}
	d.scheduleMu.Lock()
	if d.tasks == nil {
		d.tasks = map[string]*schedule.Task{}
	}
	d.tasks[task.ID] = task
	d.scheduleMu.Unlock()
	d.dispatchScheduled(ctx, schedule.Clone(task), time.Now().UTC())
	d.scheduleMu.Lock()
	paused := d.tasks[task.ID]
	d.scheduleMu.Unlock()
	if paused.Status != "paused" || paused.Reason != "agent_disabled:claude" {
		t.Fatalf("expected task paused with agent_disabled:claude, got status=%s reason=%s", paused.Status, paused.Reason)
	}
}

func TestReconcileEnabledAgentsWhenLastEnabledUninstalled(t *testing.T) {
	// Only Claude is installed now, while settings.json still disables Claude (Codex was uninstalled).
	h := newHarness(t, Config{
		Agents: map[pb.Agent]AgentConfig{
			pb.Agent_AGENT_CLAUDE: {Bin: "/usr/bin/true"},
		},
	})
	d := h.d
	_, err := d.saveSettings(&pb.DaemonSettings{DisabledAgents: []pb.Agent{pb.Agent_AGENT_CLAUDE}, Revision: 4}, writeFileAtomic)
	if err != nil {
		t.Fatal(err)
	}
	d.reconcileEnabledAgents()
	st := d.settings()
	if len(st.DisabledAgents) != 0 || st.Revision != 5 {
		t.Fatalf("expected reconcileEnabledAgents to re-enable Claude and bump revision, got %+v", st)
	}
}
