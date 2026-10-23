package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func newScheduledLaunch(t *testing.T) (*harness, *schedule.Task) {
	t.Helper()
	h := scheduleHarness(t)
	fakeTmux(t, h)
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
	h.d.tasks[task.ID].Spec.Target = schedule.Target{Type: "new_session", Cwd: h.dir, Agent: "codex"}
	h.d.tasks[task.ID].SourceSession = ""
	return h, task
}

func assertAbortedScheduledDispatch(t *testing.T, h *harness, id string) {
	t.Helper()
	before := readTask(t, h.d, id)
	if before.Status != "dispatching" || before.Active == nil || before.Active.Status != "dispatching" || len(before.Runs) != 0 || before.Dispatches != 1 {
		t.Fatalf("shutdown finalized an aborted dispatch: %+v", before)
	}
	data, err := os.ReadFile(filepath.Join(paths.Dir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk struct {
		Tasks map[string]*schedule.Task `json:"tasks"`
	}
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if got := disk.Tasks[id]; got == nil || got.Active == nil || got.Active.ID != before.Active.ID || got.Active.Status != "dispatching" || len(got.Runs) != 0 {
		t.Fatalf("durable dispatch boundary lost: %+v", got)
	}
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	after := readTask(t, h.d, id)
	if after.Status != "uncertain" || after.Active != nil || len(after.Runs) != 1 || after.Runs[0].Status != "uncertain" || after.Runs[0].ID != before.Active.ID || after.Reason != "daemon_restarted_during_dispatch" {
		t.Fatalf("restart did not recover aborted dispatch as uncertain: %+v", after)
	}
}

func TestScheduleLoopCancellationPreservesDispatchForRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, task := newScheduledLaunch(t)
		adapter := &blockedScheduleOptions{Adapter: h.d.agents[pb.Agent_AGENT_CODEX], entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
		h.d.agents[pb.Agent_AGENT_CODEX] = adapter
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		loopDone := make(chan struct{})
		go func() { defer close(loopDone); h.d.scheduleLoop(ctx) }()
		defer func() { close(adapter.release); <-adapter.returned }()
		<-adapter.entered
		cancel()
		<-loopDone
		synctest.Wait()
		assertAbortedScheduledDispatch(t, h, task.ID)
		if len(h.s.queue.Queue) != 0 {
			t.Fatal("shutdown queued a message during blocked launch")
		}
	})
}

type scheduleArgvBarrier struct {
	agent.Adapter
	entered, release chan struct{}
	err              error
}

func (a *scheduleArgvBarrier) Bin() string { return "/bin/echo" }
func (a *scheduleArgvBarrier) Argv(*agent.Launch) ([]string, error) {
	if a.entered != nil {
		close(a.entered)
		<-a.release
	}
	return nil, a.err
}

func TestScheduleLoopWaitsForInFlightDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, task := newScheduledLaunch(t)
		adapter := &scheduleArgvBarrier{Adapter: h.d.agents[pb.Agent_AGENT_CODEX], entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("synthetic launch failure")}
		h.d.agents[pb.Agent_AGENT_CODEX] = adapter
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		loopDone := make(chan struct{})
		go func() { defer close(loopDone); h.d.scheduleLoop(ctx) }()
		<-adapter.entered
		cancel()
		synctest.Wait()
		select {
		case <-loopDone:
			t.Error("scheduler returned with a dispatch still in flight")
		default:
		}
		close(adapter.release)
		<-loopDone
		assertAbortedScheduledDispatch(t, h, task.ID)
	})
}

func TestScheduleShutdownCancelsAndWaitsForDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, task := newScheduledLaunch(t)
		adapter := &scheduleArgvBarrier{Adapter: h.d.agents[pb.Agent_AGENT_CODEX], entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("synthetic launch failure")}
		h.d.agents[pb.Agent_AGENT_CODEX] = adapter
		// This is the cancellation-and-join function deferred by Daemon.Run,
		// including early returns while its caller's context is still live.
		stop := h.d.startScheduleLoop(context.Background())
		<-adapter.entered
		stopped := make(chan struct{})
		go func() { defer close(stopped); stop() }()
		synctest.Wait()
		select {
		case <-stopped:
			t.Error("daemon schedule shutdown did not await dispatch")
		default:
		}
		close(adapter.release)
		<-stopped
		assertAbortedScheduledDispatch(t, h, task.ID)
	})
}

func TestScheduleCancellationAtPersistenceBoundaries(t *testing.T) {
	for _, boundary := range []string{"session", "enqueue"} {
		t.Run(boundary, func(t *testing.T) {
			h := scheduleHarness(t)
			task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
			now := time.Now().Add(time.Second)
			jobs := h.d.prepareScheduledDispatches(now)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			target := paths.Dir()
			if boundary == "enqueue" {
				target = h.dir
			}
			h.d.syncStateDir = func(dir string) error {
				if dir == target {
					cancel()
				}
				return syncDir(dir)
			}
			h.d.dispatchScheduled(ctx, jobs[0], now)
			h.d.syncStateDir = nil
			if ctx.Err() == nil {
				t.Fatal("cancellation boundary not reached")
			}
			assertAbortedScheduledDispatch(t, h, task.ID)
			if boundary == "session" && len(h.s.queue.Queue) != 0 {
				t.Fatal("queued after launch cancellation")
			}
			if boundary == "enqueue" && (len(h.s.queue.Queue) != 1 || h.s.queue.Queue[0].RequestID != jobs[0].Active.RequestID) {
				t.Fatal("lost accepted queue input")
			}
		})
	}
}

func TestScheduleLaunchDeadlineWithoutDaemonCancellationFails(t *testing.T) {
	h, task := newScheduledLaunch(t)
	h.d.agents[pb.Agent_AGENT_CODEX] = &scheduleArgvBarrier{Adapter: h.d.agents[pb.Agent_AGENT_CODEX], err: context.DeadlineExceeded}
	h.d.tickSchedules(time.Now().Add(time.Second))
	got := readTask(t, h.d, task.ID)
	if got.Status != "failed" || got.Active != nil || len(got.Runs) != 1 {
		t.Fatalf("launch-local error treated as daemon shutdown: %+v", got)
	}
}
