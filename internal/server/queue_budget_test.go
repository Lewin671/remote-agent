package server

import (
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestAggregateQueueBudgets(t *testing.T) {
	for _, scope := range []string{"account", "relay"} {
		t.Run(scope, func(t *testing.T) {
			h := newHub(nil)
			dropped := false
			p := &peer{dev: &device{id: "phone", account: "a"}, out: make(chan queuedFrame, sendQueue), cancel: func() { dropped = true }}
			q, err := marshalFrame(bigEvents(1024))
			if err != nil {
				t.Fatal(err)
			}
			// Seed reservations belonging to other peers without allocating a
			// production-sized backlog in this unit test.
			if scope == "account" {
				h.accountQueueBytes["a"] = accountSendQueueBytes - q.size
				h.queueBytes = accountSendQueueBytes - q.size
			} else {
				h.accountQueueBytes["other"] = relaySendQueueBytes - q.size
				h.queueBytes = relaySendQueueBytes - q.size
			}
			if !h.sendFrame(p, q) {
				t.Fatal("exactly at budget rejected")
			}
			held := h.queueBytes
			if h.sendFrame(p, q) || !dropped {
				t.Fatal("aggregate overflow did not disconnect peer")
			}
			if h.queueBytes != held || p.queued.Load() != q.size {
				t.Fatal("rejected frame leaked reservation")
			}
			h.releaseFrame(p, (<-p.out).size)
			if !h.sendFrame(p, q) {
				t.Fatal("written frame did not release capacity")
			}
			h.closeQueue(p)
			if p.queued.Load() != 0 || len(p.out) != 0 {
				t.Fatal("disconnect retained backlog")
			}
			before := h.queueBytes
			h.releaseFrame(p, q.size)
			h.closeQueue(p)
			if h.queueBytes != before {
				t.Fatal("disconnect released twice")
			}
			if h.sendFrame(p, q) {
				t.Fatal("disconnected peer accepted frame")
			}
		})
	}
}

func TestQueueReservationReleasedOnFullChannel(t *testing.T) {
	h := newHub(nil)
	p := &peer{dev: &device{account: "a"}, out: make(chan queuedFrame, 1), cancel: func() {}}
	f := &pb.ServerFrame{Kind: &pb.ServerFrame_Heartbeat{Heartbeat: &pb.Heartbeat{}}}
	if !h.send(p, f) {
		t.Fatal("first frame rejected")
	}
	before := h.queueBytes
	if h.send(p, f) {
		t.Fatal("full channel admitted frame")
	}
	if h.queueBytes != before || h.accountQueueBytes["a"] != before || p.queued.Load() != before {
		t.Fatal("full channel leaked budget")
	}
	q := <-p.out
	// Dequeue alone is not completion of a potentially blocked write.
	if h.queueBytes != q.size {
		t.Fatal("write reservation released too early")
	}
	h.releaseFrame(p, q.size)
	if h.queueBytes != 0 || len(h.accountQueueBytes) != 0 {
		t.Fatal("write leaked budget")
	}
}
