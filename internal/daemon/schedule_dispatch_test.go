package daemon

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

type blockedScheduleOptions struct {
	agent.Adapter
	entered, release, returned chan struct{}
}

func (a *blockedScheduleOptions) Bin() string { return "/bin/echo" }

func (a *blockedScheduleOptions) Options([]string, string, string, pb.ApprovalMode) ([]string, error) {
	close(a.entered)
	defer close(a.returned)
	<-a.release
	return nil, nil
}

func waitScheduleSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(what)
	}
}

func TestScheduleBlockedLaunchAllowsRPCsAndOtherDispatches(t *testing.T) {
	h := scheduleHarness(t)
	fakeTmux(t, h) // A cancellation regression must still never launch a real CLI.
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
	adapter := &blockedScheduleOptions{Adapter: h.d.agents[pb.Agent_AGENT_CODEX], entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	h.d.agents[pb.Agent_AGENT_CODEX] = adapter
	// Only launch uses Options; creation of a resume task needs no CLI settings.
	h.d.tasks[task.ID].Spec.Target = schedule.Target{Type: "new_session", Cwd: h.dir, Agent: "codex"}
	h.d.tasks[task.ID].SourceSession = ""
	now := time.Now().Add(time.Second)
	jobs := h.d.prepareScheduledDispatches(now)
	if len(jobs) != 1 {
		t.Fatalf("jobs: %d", len(jobs))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); h.d.dispatchScheduled(ctx, jobs[0], now) }()
	defer func() {
		cancel()
		close(adapter.release)
		waitScheduleSignal(t, done, "dispatch did not stop")
		waitScheduleSignal(t, adapter.returned, "options did not return")
	}()
	waitScheduleSignal(t, adapter.entered, "launch did not reach barrier")
	rpcDone := make(chan struct{})
	go func() {
		defer close(rpcDone)
		rpc := &handler{d: h.d}
		for _, op := range []string{"list", "pause", "cancel"} {
			if _, err := rpc.Schedule(context.Background(), connect.NewRequest(&pb.ScheduleRequest{Operation: op, TaskId: task.ID})); err != nil {
				t.Errorf("%s while launch blocked: %v", op, err)
			}
		}
	}()
	waitScheduleSignal(t, rpcDone, "schedule RPC waited for launch")
	if more := h.d.prepareScheduledDispatches(now); len(more) != 0 {
		t.Fatal("duplicated in-flight dispatch")
	}
	other := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
	h.d.tickSchedules(now)
	if got := readTask(t, h.d, other.ID); got.Status != "queued" || got.Dispatches != 1 {
		t.Fatalf("unrelated task blocked: %+v", got)
	}
	cancel()
	waitScheduleSignal(t, done, "loop cancellation did not reach launch")
	got := readTask(t, h.d, task.ID)
	if got.Status != "cancelled" || got.Active == nil || got.Active.Status != "dispatching" || got.Dispatches != 1 || len(got.Runs) != 0 {
		t.Fatalf("cancel overwritten by result: %+v", got)
	}
}

func TestScheduleDispatchPreservesPauseAndCancel(t *testing.T) {
	for _, op := range []string{"pause", "cancel"} {
		t.Run(op, func(t *testing.T) {
			h := scheduleHarness(t)
			task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
			now := time.Now().Add(time.Second)
			jobs := h.d.prepareScheduledDispatches(now)
			if len(jobs) != 1 {
				t.Fatal("missing dispatch")
			}
			if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: op, TaskId: task.ID}, originLocal); err != nil {
				t.Fatal(err)
			}
			h.d.dispatchScheduled(context.Background(), jobs[0], now)
			got := readTask(t, h.d, task.ID)
			status := map[string]string{"pause": "paused", "cancel": "cancelled"}[op]
			if got.Status != status || got.Active == nil || got.Active.Status != "queued" || len(h.s.queue.Queue) != 1 {
				t.Fatalf("management state overwritten: %+v", got)
			}
			if jobs := h.d.prepareScheduledDispatches(now.Add(time.Second)); len(jobs) != 0 {
				t.Fatal("duplicate dispatch")
			}
		})
	}
}

func TestScheduleDispatchReconcilesByExecutionID(t *testing.T) {
	h := scheduleHarness(t)
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
	now := time.Now().Add(time.Second)
	jobs := h.d.prepareScheduledDispatches(now)
	h.d.tasks[task.ID].Active.ID = "replacement"
	h.d.dispatchScheduled(context.Background(), jobs[0], now)
	if len(h.s.queue.Queue) != 0 || readTask(t, h.d, task.ID).Active.ID != "replacement" {
		t.Fatal("stale execution injected or replaced current execution")
	}
}
