package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func pendingFixture(s *session, queued, held int) {
	s.queue.Queue = make([]input, queued)
	for i := range s.queue.Queue {
		s.queue.Queue[i] = input{Text: "q", RequestID: fmt.Sprintf("q-%d", i)}
	}
	s.queue.Held = make([]input, held)
	for i := range s.queue.Held {
		s.queue.Held[i] = input{Text: "h", RequestID: fmt.Sprintf("h-%d", i)}
	}
	s.trackPendingInput()
}

func TestPendingInputCountAdmissionForAllProducers(t *testing.T) {
	for _, producer := range []string{"local", "remote", "scheduled"} {
		t.Run(producer, func(t *testing.T) {
			h := scheduleHarness(t)
			pendingFixture(h.s, maxPendingInputs-2, 1)
			if _, err := h.s.send("last-slot", "last-slot"); err != nil {
				t.Fatal(err)
			}
			image := completeUpload(t, h.d.uploads, "local")
			beforeBytes := h.d.inputBytes
			switch producer {
			case "local":
				if _, err := h.s.send("extra", "rejected"); connect.CodeOf(err) != connect.CodeResourceExhausted {
					t.Fatalf("local admission: %v", err)
				}
			case "remote":
				_, err := (&handler{d: h.d}).Send(context.Background(), connect.NewRequest(&pb.SendRequest{SessionId: h.s.meta.ID, RequestId: "rejected", Text: "extra", ImageIds: []string{image.ImageId}}))
				if connect.CodeOf(err) != connect.CodeResourceExhausted {
					t.Fatalf("remote admission: %v", err)
				}
			case "scheduled":
				task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
				h.d.tickSchedules(time.Now().Add(time.Second))
				if task = readTask(t, h.d, task.ID); task.Status != "failed" || task.Active != nil || len(task.Runs) != 1 || task.Reason != "could_not_queue_message" {
					t.Fatalf("scheduled admission: %+v", task)
				}
			}
			if count, _ := h.s.queue.pendingUsage(); count != maxPendingInputs || h.s.queue.seen("rejected") || h.d.inputBytes != beforeBytes {
				t.Fatal("rejection retained input or budget")
			}
			if err := h.d.uploads.check([]string{image.ImageId}, "local"); err != nil {
				t.Fatalf("rejection took image: %v", err)
			}
			if _, err := os.Stat(h.d.uploads.sessionDir(h.s.meta.ID)); !os.IsNotExist(err) {
				t.Fatalf("rejection created image directory: %v", err)
			}
		})
	}
}

func TestPendingInputTextAndGlobalReservationBoundaries(t *testing.T) {
	d := New(Config{})
	text := strings.Repeat("x", maxSessionInputBytes)
	q := &queueState{Queue: []input{{Text: text[:len(text)-1]}}}
	reservation, err := d.reserveInput("session", q, "x")
	if err != nil {
		t.Fatal(err)
	}
	if d.inputBytes != maxSessionInputBytes {
		t.Fatal("exact session boundary was rejected or miscounted")
	}
	reservation.release()
	// Use actual pending text for the per-session boundary, including held input.
	d = New(Config{})
	q = &queueState{Held: []input{{Text: text}}}
	if _, err := d.reserveInput("session", q, "x"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("session text limit: %v", err)
	}
	if _, err := d.reserveInput("", nil, text+"x"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("oversized first prompt: %v", err)
	}
	d = New(Config{})
	var reservations []*inputReservation
	for i := 0; i < maxDaemonInputBytes/maxSessionInputBytes; i++ {
		r, err := d.reserveInput("", nil, text)
		if err != nil {
			t.Fatal(err)
		}
		reservations = append(reservations, r)
	}
	if d.inputBytes != maxDaemonInputBytes {
		t.Fatal("global boundary not reached")
	}
	if _, err := d.reserveInput("", nil, "x"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("global limit: %v", err)
	}
	reservations[len(reservations)-1].release()
	extra, err := d.reserveInput("", nil, "x")
	if err != nil {
		t.Fatal("released capacity not available", err)
	}
	extra.release()
	for _, r := range reservations {
		r.release()
	}
	if d.inputBytes != 0 {
		t.Fatal("reservation leak")
	}
}

func TestPendingInputDedupeOutlivesCompletedCache(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	pendingFixture(h.s, 1, 1)
	for i := 0; i <= maxSeen; i++ {
		h.s.queue.remember(fmt.Sprintf("completed-%d", i))
	}
	rpc := &handler{d: h.d}
	for _, id := range []string{"q-0", "h-0"} {
		_, err := rpc.Send(context.Background(), connect.NewRequest(&pb.SendRequest{SessionId: h.s.meta.ID, RequestId: id, Text: "retry", ImageIds: []string{"invalid-image"}}))
		if err != nil {
			t.Fatalf("pending retry took images or duplicated: %v", err)
		}
	}
	if count, _ := h.s.queue.pendingUsage(); count != 2 {
		t.Fatal("pending retry duplicated")
	}
	if err := h.s.queue.save(h.dir); err != nil {
		t.Fatal(err)
	}
	q, err := loadQueue(h.dir)
	if err != nil || !q.seen("q-0") || !q.seen("h-0") {
		t.Fatalf("restart lost pending identity: %v", err)
	}
}

func TestInputAdmissionFailureReleasesReservationAndImages(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	image := completeUpload(t, h.d.uploads, "local")
	if err := os.Mkdir(filepath.Join(h.dir, "queue.json"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := (&handler{d: h.d}).Send(context.Background(), connect.NewRequest(&pb.SendRequest{SessionId: h.s.meta.ID, RequestId: "failure", Text: "text", ImageIds: []string{image.ImageId}}))
	if err == nil || queueAccepted(err) || h.d.inputBytes != 0 || len(h.s.queue.Queue) != 0 {
		t.Fatalf("failed save retained admission: %v bytes=%d", err, h.d.inputBytes)
	}
	if err := h.d.uploads.check([]string{image.ImageId}, "local"); err != nil {
		t.Fatalf("failed save retained image: %v", err)
	}
}

func TestInitialPromptRejectsBeforeTakingImagesOrLaunching(t *testing.T) {
	h := newHarness(t, Config{})
	image := completeUpload(t, h.d.uploads, "local")
	h.d.inputBytes = maxDaemonInputBytes
	_, err := h.d.createByContext(context.Background(), &pb.CreateSessionRequest{Cwd: h.dir, Prompt: "extra", ImageIds: []string{image.ImageId}}, originLocal, false, false, "")
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("first prompt admission: %v", err)
	}
	if err := h.d.uploads.check([]string{image.ImageId}, "local"); err != nil {
		t.Fatalf("rejected first prompt took image: %v", err)
	}
	if len(h.d.sessions) != 0 || h.d.inputBytes != maxDaemonInputBytes {
		t.Fatal("rejected creation retained session or budget")
	}
}

func TestPendingInputBudgetReleasedOnSettlementAndRestore(t *testing.T) {
	h := newHarness(t, Config{})
	if _, err := h.s.send("text", "settled"); err != nil {
		t.Fatal(err)
	}
	if h.d.inputBytes != 4 {
		t.Fatal("accepted text not accounted")
	}
	if err := h.s.cancelInputBy("settled", originLocal); err != nil {
		t.Fatal(err)
	}
	if h.d.inputBytes != 0 {
		t.Fatal("cancellation retained budget")
	}
	pendingFixture(h.s, 1, 1)
	if err := h.s.queue.save(h.dir); err != nil {
		t.Fatal(err)
	}
	h.d.inputBytes = 0
	h.d.inputQueues = nil
	if err := h.s.log.Close(); err != nil {
		t.Fatal(err)
	}
	h.open(h.s.meta)
	if h.d.inputBytes != 2 {
		t.Fatalf("restore missed queue and held text: %d", h.d.inputBytes)
	}
}

func TestConcurrentInputReservationsRespectGlobalLimit(t *testing.T) {
	d := New(Config{})
	// Exercise the admission race at the last byte without creating large files.
	d.inputBytes = maxDaemonInputBytes - 1
	start := make(chan struct{})
	results := make(chan *inputReservation, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := d.reserveInput("", nil, "x")
			if err != nil && connect.CodeOf(err) != connect.CodeResourceExhausted {
				t.Error(err)
			}
			results <- r
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	accepted := 0
	for r := range results {
		if r != nil {
			accepted++
			r.release()
		}
	}
	if accepted != 1 || d.inputBytes != maxDaemonInputBytes-1 {
		t.Fatal("concurrent admission overcommitted or leaked budget")
	}
}

func TestPendingInputTextLimitRejectsBeforeInsertion(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.queue.Held = []input{{Text: strings.Repeat("x", maxSessionInputBytes), RequestID: "held"}}
	if _, err := h.s.send("x", "rejected"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("text admission: %v", err)
	}
	if len(h.s.queue.Queue) != 0 || h.s.queue.seen("rejected") || h.d.inputBytes != maxSessionInputBytes {
		t.Fatal("text rejection retained input or bytes")
	}
}

func TestFailedImageTakeReleasesInputReservation(t *testing.T) {
	h := newHarness(t, Config{})
	if _, err := h.s.enqueueUploaded("text", "failed", []string{"invalid"}, originLocal, pb.SendMode_SEND_MODE_STEER); err == nil {
		t.Fatal("invalid image accepted")
	}
	if h.d.inputBytes != 0 || len(h.s.queue.Queue) != 0 || h.s.queue.seen("failed") {
		t.Fatal("image rejection retained reservation or input")
	}
	if _, err := h.d.createByContext(context.Background(), &pb.CreateSessionRequest{Prompt: "text", Cwd: "relative"}, originLocal, false, false, ""); err == nil {
		t.Fatal("invalid creation accepted")
	}
	if h.d.inputBytes != 0 {
		t.Fatal("failed creation retained first-prompt reservation")
	}
}
