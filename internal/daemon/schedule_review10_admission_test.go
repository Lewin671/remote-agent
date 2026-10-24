package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/tail"
)

func TestScheduleSafetyOperationsBypassTranscript(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		for _, op := range []string{"list", "get", "pause", "cancel"} {
			t.Run(map[bool]string{false: "local", true: "mcp"}[scoped]+"/"+op, func(t *testing.T) {
				h, own := admissionFixture(t, false)
				defer h.s.log.Close()
				h.s.reader = tail.NewReader(h.dir, 0)
				h.s.readMu.Lock()
				var once sync.Once
				release := func() { once.Do(h.s.readMu.Unlock) }
				defer release()
				done := make(chan error, 1)
				go func() {
					if scoped {
						done <- admissionRequest(t, h, own, op)
					} else {
						_, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: op, SessionId: h.s.meta.ID, TaskId: own.ID}, originLocal)
						done <- err
					}
				}()
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("safety operation was gated by transcript: %v", err)
					}
				case <-time.After(time.Second):
					release()
					<-done
					t.Fatal("safety operation waited for transcript lock")
				}
			})
		}
	}
}

func TestScheduleSaveReleasesTranscriptLock(t *testing.T) {
	h, own := admissionFixture(t, false)
	defer h.s.log.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.d.syncStateDir = func(dir string) error {
		if dir == paths.Dir() {
			once.Do(func() { close(entered) })
			<-release
		}
		return syncDir(dir)
	}
	done := make(chan error, 1)
	go func() { done <- admissionRequest(t, h, own, "create") }()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("save did not start")
	}
	free := h.s.readMu.TryLock()
	if free {
		h.s.readMu.Unlock()
	}
	// Task controls must stay ordered with the already-admitted execution.
	control := make(chan error, 1)
	go func() {
		_, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "pause", TaskId: own.ID}, originLocal)
		control <- err
	}()
	controlDone := false
	select {
	case <-control:
		controlDone = true
		t.Error("task control overtook admitted save")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !controlDone {
		if err := <-control; err != nil {
			t.Fatal(err)
		}
	}
	if !free {
		t.Fatal("schedules.json save retained transcript read lock")
	}
}

func TestLocalContextDependentOperationsStillRejectReadFaults(t *testing.T) {
	for _, op := range []string{"create", "update", "resume", "run"} {
		h, own := admissionFixture(t, false)
		raw, _ := json.Marshal(own.Spec)
		h.s.reader = tail.NewReader(h.dir, 0)
		_, err := h.d.scheduleCommandScopedContext(context.Background(), &pb.ScheduleRequest{Operation: op, SessionId: h.s.meta.ID, TaskId: own.ID, SpecJson: raw}, originLocal, "")
		if err == nil {
			t.Fatalf("%s ignored required read", op)
		}
		h.s.log.Close()
	}
}
