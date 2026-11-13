package remoteclient

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestMachinePollingLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		var calls atomic.Int64
		p := NewMachineStatusController(func(context.Context) (*pb.MachineStatus, error) {
			n := calls.Add(1)
			if n >= 2 && n <= 4 {
				return nil, errors.New("offline")
			}
			return &pb.MachineStatus{ObservedMs: n}, nil
		}, 2*time.Second)
		go func() { p.Run(ctx); close(done) }()
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("hidden fetch")
		}
		p.SetActive(true, true, false)
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("unsupported fetch")
		}
		p.SetActive(true, true, true)
		synctest.Wait()
		if p.Snapshot().State != MachineLive || calls.Load() != 1 {
			t.Fatal(p.Snapshot())
		}
		time.Sleep(6 * time.Second)
		synctest.Wait()
		s := p.Snapshot()
		if s.State != MachineStale || s.Last.ObservedMs != 1 || s.Failures != 3 {
			t.Fatal(s)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if p.Snapshot().State != MachineLive || p.Snapshot().CurveSinceMs != 5 {
			t.Fatal(p.Snapshot())
		}
		p.SetActive(false, true, true)
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if calls.Load() != 5 {
			t.Fatal("hidden requested")
		}
		p.SetActive(true, true, true)
		synctest.Wait()
		if p.Snapshot().Last.ObservedMs != 6 || p.Snapshot().CurveSinceMs != 5 {
			t.Fatal("short leave discarded real history")
		}
		cancel()
		<-done
	})
}
func TestMachinePollingNoOverlapOrLatePublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		gate := make(chan struct{})
		calls := 0
		p := NewMachineStatusController(func(context.Context) (*pb.MachineStatus, error) {
			calls++
			<-gate
			return &pb.MachineStatus{ObservedMs: 1}, nil
		}, time.Second)
		done := make(chan struct{})
		p.SetActive(true, true, true)
		go func() { p.Run(ctx); close(done) }()
		synctest.Wait()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if calls != 1 {
			t.Fatal("overlap")
		}
		p.SetActive(false, true, true)
		close(gate)
		synctest.Wait()
		if p.Snapshot().Last != nil {
			t.Fatal("late value published")
		}
		cancel()
		<-done
	})
}
func TestMachineRankingAndReminder(t *testing.T) {
	a := &pb.ResourceUsage{Id: "a", CpuPercent: 10, MemoryBytes: 100e6}
	b := &pb.ResourceUsage{Id: "b", CpuPercent: 12, MemoryBytes: 150e6}
	prev := []string{"a", "b"}
	for _, memory := range []bool{false, true} {
		if StableUsage(prev, []*pb.ResourceUsage{b, a}, memory)[0].Id != "a" {
			t.Fatal("boundary swapped")
		}
	}
	b.CpuPercent = 12.01
	b.MemoryBytes++
	for _, memory := range []bool{false, true} {
		if StableUsage(prev, []*pb.ResourceUsage{a, b}, memory)[0].Id != "b" {
			t.Fatal("challenger not promoted")
		}
	}
	m := &pb.MachineMeta{Capabilities: &pb.MachineCapabilities{MachineStatus: true}, DiskStatus: &pb.DiskStatus{AvailableBytes: 9e9}, Projects: []*pb.ProjectSummary{{Path: "old", LatestCreatedMs: 1}, {Path: "new", LatestEndedMs: 2}}}
	if MachineDiskReminder(false, m) != nil || MachineDiskReminder(true, m) == nil || RecentMachineProject(m) != "new" {
		t.Fatal("machine helpers")
	}
	if !IsDiskFull(&CommandError{Code: "disk_full"}) || IsDiskFull(errors.New("no space left on device")) {
		t.Fatal("string matching")
	}
}

func TestMachineOfflineAndRequestDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		hang := false
		p := NewMachineStatusController(func(ctx context.Context) (*pb.MachineStatus, error) {
			calls++
			if hang {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return &pb.MachineStatus{ObservedMs: int64(calls)}, nil
		}, 2*time.Second)
		done := make(chan struct{})
		go func() { p.Run(ctx); close(done) }()
		p.SetActive(true, true, true)
		synctest.Wait()
		p.SetActive(true, false, true)
		synctest.Wait()
		if p.Snapshot().State != MachineStale || p.Snapshot().Last.ObservedMs != 1 {
			t.Fatal(p.Snapshot())
		}
		time.Sleep(20 * time.Second)
		synctest.Wait()
		if calls != 1 {
			t.Fatal("offline request")
		}
		hang = true
		p.SetActive(true, true, true)
		synctest.Wait()
		for i := 0; i < 3; i++ {
			time.Sleep(MachineStatusRequestTimeout)
			synctest.Wait()
		}
		if p.Snapshot().Failures != 3 || p.Snapshot().State != MachineStale {
			t.Fatal(p.Snapshot())
		}
		cancel()
		<-done
	})
}
func TestStableUsageMultipleMovesAndMembership(t *testing.T) {
	rows := []*pb.ResourceUsage{{Id: "a", CpuPercent: 5}, {Id: "b", CpuPercent: 10}, {Id: "new", CpuPercent: 30}, {Id: "c", CpuPercent: 25}}
	s := StableUsage([]string{"gone", "a", "b", "c"}, rows, false)
	for i, want := range []string{"new", "c", "b", "a"} {
		if s[i].Id != want {
			t.Fatal(s)
		}
	}
}
