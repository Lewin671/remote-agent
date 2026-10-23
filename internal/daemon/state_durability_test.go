package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func failStateSync() func(string) error {
	return func(string) error { return os.ErrPermission }
}

func TestAtomicWriteReportsReplacementUncertainty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	err := writeFileSyncDir(path, []byte("new"), failStateSync())
	if !fileReplaced(err) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("lost sync failure: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("replacement not visible: %q %v", data, err)
	}
	if err := syncDir(filepath.Join(path, "missing")); err == nil {
		t.Fatal("directory open failure ignored")
	}
	err = writeFileSyncDir(filepath.Join(path, "missing"), nil, failStateSync())
	if err == nil || fileReplaced(err) {
		t.Fatalf("pre-rename error classified as replacement: %v", err)
	}
}

func TestQueueKeepsReplacementAfterSyncFailure(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.syncStateDir = failStateSync()
	_, err := h.s.send("keep", "kept")
	if !queueAccepted(err) || !fileReplaced(err) {
		t.Fatalf("lost uncertainty: %v", err)
	}
	disk, err := loadQueue(h.dir)
	if err != nil || len(disk.Queue) != 1 || len(h.s.queue.Queue) != 1 || disk.Queue[0].RequestID != h.s.queue.Queue[0].RequestID {
		t.Fatalf("memory disagrees with replacement: %+v %+v %v", disk, h.s.queue, err)
	}
	if _, err := h.s.send("keep", "kept"); err != nil || len(h.s.queue.Queue) != 1 {
		t.Fatalf("retry duplicated uncertain acceptance: %v", err)
	}
}

func TestQueueStageKeepsReplacementAfterSyncFailure(t *testing.T) {
	for _, mark := range []bool{false, true} {
		t.Run(map[bool]string{false: "stage", true: "inflight"}[mark], func(t *testing.T) {
			h := newHarness(t, Config{})
			if _, err := h.s.send("keep", "kept"); err != nil {
				t.Fatal(err)
			}
			h.d.syncStateDir = failStateSync()
			var err error
			if mark {
				err = h.s.markInflight(0, "kept")
			} else {
				err = h.s.setStage("kept", pb.InputStage_INPUT_STAGE_PASTING_IMAGES, nil)
			}
			if !fileReplaced(err) {
				t.Fatalf("lost sync failure: %v", err)
			}
			disk, err := loadQueue(h.dir)
			if err != nil || disk.Queue[0].Stage != h.s.queue.Queue[0].Stage || (disk.Queue[0].Inflight == nil) != (h.s.queue.Queue[0].Inflight == nil) {
				t.Fatalf("stage reverted over new file: %+v %+v %v", disk, h.s.queue, err)
			}
		})
	}
}

func TestScheduleKeepsReplacementAfterSyncFailure(t *testing.T) {
	h := scheduleHarness(t)
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
	h.d.syncStateDir = failStateSync()
	_, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "pause", TaskId: task.ID}, originLocal)
	if !fileReplaced(err) {
		t.Fatalf("lost sync failure: %v", err)
	}
	if got := readTask(t, h.d, task.ID); got.Status != "paused" {
		t.Fatalf("memory reverted: %+v", got)
	}
	h.d.syncStateDir = nil
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	if got := readTask(t, h.d, task.ID); got.Status != "paused" {
		t.Fatalf("disk disagrees: %+v", got)
	}
}

func TestScheduleDoesNotLaunchAfterDispatchSyncFailure(t *testing.T) {
	h := scheduleHarness(t)
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
	h.d.syncStateDir = failStateSync()
	h.d.tickSchedules(time.Now().Add(time.Second))
	if len(h.s.queue.Queue) != 0 {
		t.Fatal("launched without durable dispatch record")
	}
	got := readTask(t, h.d, task.ID)
	if got.Status != "uncertain" || got.Dispatches != 1 {
		t.Fatalf("lost dispatch uncertainty: %+v", got)
	}
}
