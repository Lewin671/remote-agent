package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventlog"
)

func repairEventLog(t *testing.T, h *harness) {
	t.Helper()
	log, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h.s.log = log
	t.Cleanup(func() { log.Close() })
}

func TestFirstImageStageCommitFailurePreservesUnwrittenInput(t *testing.T) {
	h := newHarness(t, Config{})
	imgs := imagesFor(t, h.dir, "a.png")
	fakeCodexImages(t, h, map[string]bool{imgs[0].Path: true})
	if _, err := h.s.enqueue("picture", "image-input", imgs, originLocal); err != nil {
		t.Fatal(err)
	}
	h.s.status = pb.SessionStatus_SESSION_STATUS_IDLE
	h.s.log.Close()
	if outcome, err := h.s.inject(h.s.queue.Queue[0]); outcome != notWritten || err == nil {
		t.Fatalf("inject=%v error=%v", outcome, err)
	}
	disk, err := loadQueue(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Queue[0].Stage != pb.InputStage_INPUT_STAGE_WAITING || disk.Queue[0].Inflight != nil || h.s.queue.Queue[0].Stage != pb.InputStage_INPUT_STAGE_WAITING || len(h.term.written()) != 0 {
		t.Fatal("untouched image input recorded as partially pasted")
	}
	repairEventLog(t, h)
	runInjector(t, h)
	eventually(t, "image delivered after storage repair", func() bool { return inputStatus(h, "image-input") == pb.InputStatus_INPUT_STATUS_SUBMITTED })
	if w := h.term.written(); len(w) != 2 {
		t.Fatalf("image/text should each be written once: %v", w)
	}
	for _, st := range inputStates(h, "image-input") {
		if st.Reason == "restarted" || st.Status == pb.InputStatus_INPUT_STATUS_UNCONFIRMED {
			t.Fatalf("untouched input wrongly recovered: %v", st)
		}
	}
}

func TestPersistedReadyTransitionsWakeDespiteEventFailure(t *testing.T) {
	for _, op := range []string{"resume", "release"} {
		t.Run(op, func(t *testing.T) {
			h := newHarness(t, Config{})
			in := input{RequestID: "ready", Text: "one", Paused: true, Stage: pb.InputStage_INPUT_STAGE_WAITING, Mode: pb.SendMode_SEND_MODE_QUEUE}
			h.s.queue.Queue = []input{in}
			if err := h.s.queue.save(h.dir); err != nil {
				t.Fatal(err)
			}
			h.s.log.Close()
			var err error
			if op == "resume" {
				err = h.s.resumeInputBy(in.RequestID, originLocal)
			} else {
				err = h.s.releaseRequeued([]input{in})
			}
			if err == nil {
				t.Fatal("event failure not returned")
			}
			disk, err := loadQueue(h.dir)
			if err != nil || len(disk.Queue) != 1 || disk.Queue[0].Paused || disk.Queue[0].writing() {
				t.Fatalf("ready state lost: %v %v", disk, err)
			}
			select {
			case <-h.s.wake:
			default:
				t.Fatal("durably ready input left asleep after notification failed")
			}
			repairEventLog(t, h)
			runInjector(t, h)
			eventually(t, "released input delivered", func() bool { return inputStatus(h, "ready") == pb.InputStatus_INPUT_STATUS_SUBMITTED })
			if len(h.term.written()) != 1 {
				t.Fatal("released input not submitted exactly once")
			}
		})
	}
}

func TestFailedPauseSaveRetainsSafeStateAndRetriesOnRestore(t *testing.T) {
	for _, path := range []string{"after-turn", "restore"} {
		t.Run(path, func(t *testing.T) {
			h := newHarness(t, Config{})
			h.s.emit(&pb.Event{TurnId: "t", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
			if _, err := h.s.enqueueMode("later", "q", nil, originLocal, pb.SendMode_SEND_MODE_QUEUE); err != nil {
				t.Fatal(err)
			}
			h.s.emit(&pb.Event{TurnId: "t", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_ABORTED}}})
			file := filepath.Join(h.dir, "queue.json")
			if err := os.Rename(file, file+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(file, 0700); err != nil {
				t.Fatal(err)
			}
			if path == "after-turn" {
				h.s.afterTurn()
			} else {
				logged := newLogged(h.s.queue, nil)
				if err := h.s.log.Each(logged.note); err != nil {
					t.Fatal(err)
				}
				h.s.restorePause(logged)
			}
			if !h.s.queue.Queue[0].Paused {
				t.Fatal("save failure unpaused interrupted input")
			}
			for _, st := range inputStates(h, "q") {
				if st.Paused {
					t.Fatal("reported a pause not saved in queue.json")
				}
			}
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(file+".saved", file); err != nil {
				t.Fatal(err)
			}
			h.reopen()
			disk, err := loadQueue(h.dir)
			if err != nil || !disk.Queue[0].Paused || !h.s.queue.Queue[0].Paused {
				t.Fatalf("restart skipped unsaved pause: %v %v", disk, err)
			}
		})
	}
}

func TestCommittedCancellationWakesRemainingInput(t *testing.T) {
	h := newHarness(t, Config{RetryInterval: time.Second})
	for _, id := range []string{"head", "next"} {
		if _, err := h.s.enqueueMode("one", id, nil, originLocal, pb.SendMode_SEND_MODE_QUEUE); err != nil {
			t.Fatal(err)
		}
	}
	for {
		select {
		case <-h.s.wake:
			continue
		default:
			goto drained
		}
	}
drained:
	if err := h.s.cancelInput("head"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.s.wake:
	default:
		t.Fatal("cancellation left remaining input asleep")
	}
}
