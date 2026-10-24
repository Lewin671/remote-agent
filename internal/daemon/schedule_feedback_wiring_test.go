package daemon

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"testing"
	"time"
)

func TestScheduledCreationWritesOwnership(t *testing.T) {
	h, task := newScheduledLaunch(t)
	defer h.s.log.Close()
	h.d.agents[pb.Agent_AGENT_CODEX] = &scheduleArgvBarrier{Adapter: h.d.agents[pb.Agent_AGENT_CODEX]}
	snapshot := h.d.prepareScheduledDispatches(time.Now().Add(time.Hour))[0]
	created, err := h.d.launchScheduledSession(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := created.info(); got.ScheduledTaskId != task.ID || !got.TaskOwned {
		t.Fatalf("real creation omitted ownership: %v", got)
	}
	m, err := loadMeta(created.dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.ScheduledTaskID != task.ID || m.ScheduledRunID != snapshot.Active.ID || !m.TaskOwned {
		t.Fatal("creation ownership was not durable")
	}
}
func TestRealScheduleLoopClosesCompletedCLI(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	h.d.tasks[task.ID].Status = "completed"
	h.d.tasks[task.ID].Spec.Target.Type = "new_session"
	h.d.tasks[task.ID].Runs = []schedule.Run{{SessionID: h.s.meta.ID, Status: "completed"}}
	h.s.meta.TaskOwned = true
	h.s.meta.ScheduledTaskID = task.ID
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := h.d.startScheduleLoop(ctx)
	defer stop()
	eventually(t, "actual scheduler closed finished CLI", func() bool { return !h.term.Alive() })
}
func TestExplicitResumeRPCClaimsScheduledSession(t *testing.T) {
	h := resumableHarness(t)
	defer h.s.log.Close()
	h.s.meta.TaskOwned = true
	h.s.meta.ScheduledTaskID = "task"
	if err := h.s.kill(); err != nil {
		t.Fatal(err)
	}
	_, err := (&handler{d: h.d}).ResumeSession(context.Background(), connect.NewRequest(&pb.ResumeSessionRequest{SessionId: h.s.meta.ID}))
	if err != nil {
		t.Fatal(err)
	}
	if h.s.info().TaskOwned {
		t.Fatal("ResumeSession RPC did not claim ownership")
	}
	// Drain real background owners before the deferred log close.
	h.s.stop()
	select {
	case <-h.s.done:
	case <-time.After(3 * time.Second):
		t.Fatal("resume loops failed to stop")
	}
}
