package daemon

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestDurableAcceptanceWakesInjectorDespiteEventFailure(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.log.Close()
	if _, err := h.s.send("one", "r1"); !errors.Is(err, errQueueAccepted) {
		t.Fatalf("send: %v", err)
	}
	select {
	case <-h.s.wake:
	default:
		t.Fatal("accepted input stranded on sleeping injector")
	}
	// No new entry or event should be needed to retry the same request ID.
	if _, err := h.s.send("one", "r1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.s.wake:
	default:
		t.Fatal("duplicate accepted request did not wake injector")
	}
	q, err := loadQueue(h.dir)
	if err != nil || len(q.Queue) != 1 {
		t.Fatalf("queue duplicated or lost: %v %v", q, err)
	}
}

func TestRPCDuplicateAcceptedInputWakesInjector(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	if _, err := h.s.send("one", "r1"); err != nil {
		t.Fatal(err)
	}
	<-h.s.wake
	handler := &handler{d: h.d}
	if _, err := handler.Send(context.Background(), connect.NewRequest(&pb.SendRequest{SessionId: h.s.meta.ID, RequestId: "r1", Text: "one"})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.s.wake:
	default:
		t.Fatal("RPC dedup bypassed injector wake")
	}
	if len(h.s.queue.Queue) != 1 {
		t.Fatal("duplicate request added another input")
	}
}
