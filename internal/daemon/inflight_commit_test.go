package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestInflightCommitFailureRetainsUnwrittenMessageAcrossRestart(t *testing.T) {
	h := newHarness(t, Config{SubmitTimeout: 100 * time.Millisecond})
	if _, err := h.s.send("one", "r1"); err != nil {
		t.Fatal(err)
	}
	h.s.checkScreen()
	h.s.checkScreen()
	h.s.log.Close()
	oldGate, oldGateAt := h.s.gate, h.s.gateAt
	if outcome, err := h.s.inject(h.s.queue.Queue[0]); outcome != notWritten || err == nil {
		t.Fatalf("inject=%v %v", outcome, err)
	}
	if len(h.term.written()) != 0 {
		t.Fatal("bytes went out before failed commit")
	}
	disk, err := loadQueue(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Queue[0].Inflight != nil || disk.Queue[0].Stage == pb.InputStage_INPUT_STAGE_SUBMITTING || h.s.queue.Queue[0].Inflight != nil {
		t.Fatal("untouched message left marked inflight")
	}
	if h.s.gate != oldGate || h.s.gateAt != oldGateAt {
		t.Fatal("failed attempt advanced delivery gate")
	}
	// Reopen the actual durable state, as after repair/restart. It must be
	// submitted once, rather than settled UNCONFIRMED without ever being sent.
	m := h.s.meta
	h.open(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.start(ctx)
	eventually(t, "recovered delivery", func() bool { h.s.mu.Lock(); defer h.s.mu.Unlock(); return len(h.s.queue.Queue) == 0 })
	if len(h.term.written()) != 1 || inputStatus(h, "r1") != pb.InputStatus_INPUT_STATUS_SUBMITTED {
		t.Fatalf("writes=%v status=%v", h.term.written(), inputStatus(h, "r1"))
	}
}

func TestInflightQueueSaveFailureDoesNotAdvanceGate(t *testing.T) {
	h := newHarness(t, Config{})
	if _, err := h.s.send("one", "r1"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.dir, "queue.json")
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	// Refuse the atomic replacement even when tests run as root.
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	gate, at := h.s.gate, h.s.gateAt
	if err := h.s.markInflight(0, "r1"); err == nil {
		t.Fatal("queue save unexpectedly succeeded")
	}
	if h.s.queue.Queue[0].Inflight != nil || h.s.gate != gate || h.s.gateAt != at || len(h.term.written()) != 0 {
		t.Fatal("failed pre-write save advanced submission state")
	}
}
