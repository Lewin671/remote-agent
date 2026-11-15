package server

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func directedFixture(t *testing.T) (*hub, *peer, *peer) {
	t.Helper()
	h := newHub(nil)
	makePeer := func(id string, kind pb.DeviceKind) *peer {
		p := &peer{dev: &device{id: id, account: "account", kind: kind}, out: make(chan queuedFrame, sendQueue), cancel: func() { t.Error("directed traffic disconnected main stream") }}
		h.initDirected(p)
		return p
	}
	client := makePeer("11111111111111111111111111111111", pb.DeviceKind_DEVICE_KIND_CLIENT)
	machine := makePeer("22222222222222222222222222222222", pb.DeviceKind_DEVICE_KIND_MACHINE)
	h.machines[machine.dev.id] = machine
	h.directClients[client.dev.id] = client
	return h, client, machine
}
func directedRequest(target *peer, n int, class pb.FrameClass) *pb.DirectedPeerFrame {
	return &pb.DirectedPeerFrame{ToDevice: []byte(target.dev.id), FrameClass: class, FrameId: []byte(fmt.Sprintf("%016d", n)), Sealed: []byte{1, 2, 3}}
}
func popDirected(t *testing.T, h *hub, p *peer, c int) *pb.ServerFrame {
	t.Helper()
	q, ok := h.nextDirected(p, &c)
	if !ok {
		t.Fatal("missing directed frame")
	}
	h.releaseDirected(p, q)
	f := &pb.ServerFrame{}
	if err := proto.Unmarshal(q.data, f); err != nil {
		t.Fatal(err)
	}
	return f
}
func TestDirectedRoutingAndFIFO(t *testing.T) {
	h, c, m := directedFixture(t)
	now := time.Now()
	for i := 0; i < 3; i++ {
		h.directed(c, directedRequest(m, i, pb.FrameClass_SIGNAL), now)
	}
	h.directed(c, directedRequest(m, 9, pb.FrameClass_CONTROL), now)
	// Control may overtake Signal, while each category retains FIFO.
	if f := popDirected(t, h, m, 1).GetPeerFrameDelivery(); f.FrameClass != pb.FrameClass_CONTROL {
		t.Fatal("classes shared FIFO")
	}
	for i := 0; i < 3; i++ {
		f := popDirected(t, h, m, 0).GetPeerFrameDelivery()
		if string(f.FromDevice) != c.dev.id || string(f.ToDevice) != m.dev.id || !bytes.Equal(f.FrameId, directedRequest(m, i, pb.FrameClass_SIGNAL).FrameId) {
			t.Fatal("route/FIFO changed")
		}
	}
	if c.direct.count[2] != 0 {
		t.Fatal("delivery acknowledged before socket write")
	}
	h.directed(m, directedRequest(c, 1, pb.FrameClass_SIGNAL), now)
	if f := popDirected(t, h, c, 0).GetPeerFrameDelivery(); string(f.FromDevice) != m.dev.id {
		t.Fatal("reverse route")
	}
	if h.direct.bytes != [2]int{} {
		t.Fatal("reservation leaked")
	}
}
func TestDirectedExpiryDisconnectAndIsolation(t *testing.T) {
	for _, mode := range []string{"expiry", "disconnect", "other-account", "same-role", "offline"} {
		t.Run(mode, func(t *testing.T) {
			h, c, m := directedFixture(t)
			now := time.Now()
			request := directedRequest(m, 1, pb.FrameClass_SIGNAL)
			want := pb.PeerFrameResult_OFFLINE
			switch mode {
			case "other-account":
				m.dev.account = "other"
			case "same-role":
				request.ToDevice = []byte(c.dev.id)
			case "offline":
				delete(h.machines, m.dev.id)
			}
			h.directed(c, request, now)
			if mode == "expiry" {
				h.expireDirected(m, now.Add(time.Second-time.Nanosecond))
				if m.direct.count[0] != 1 {
					t.Fatal("expired early")
				}
				h.expireDirected(m, now.Add(time.Second))
				want = pb.PeerFrameResult_LIMITED
			}
			if mode == "disconnect" {
				h.closeDirected(m)
				h.closeDirected(m)
			}
			result := popDirected(t, h, c, 2).GetPeerFrameResult()
			if result.Status != want || !bytes.Equal(result.FrameId, request.FrameId) {
				t.Fatalf("got %v", result)
			}
			if h.direct.bytes != [2]int{} {
				t.Fatal("reservation leaked")
			}
		})
	}
}
func TestDirectedFloodKeepsPushAndReceiptsBounded(t *testing.T) {
	h, c, m := directedFixture(t)
	now := time.Now()
	for i := 0; i < 2000; i++ {
		h.directed(c, directedRequest(m, i, pb.FrameClass_SIGNAL), now)
	}
	if m.direct.count[0] != 256 || c.direct.count[2] != 512 {
		t.Fatalf("unbounded or wrong budgets: %v %v", m.direct.count, c.direct.count)
	}
	if m.queued.Load() != 0 || h.queueBytes != 0 {
		t.Fatal("signaling borrowed application queue")
	}
	if !h.send(m, &pb.ServerFrame{Kind: &pb.ServerFrame_Heartbeat{Heartbeat: &pb.Heartbeat{}}}) {
		t.Fatal("push lost to signaling flood")
	}
	q := <-m.out
	h.releaseFrame(m, q.size)
	h.directed(c, directedRequest(m, 2001, pb.FrameClass_CONTROL), now)
	if m.direct.count[1] != 1 {
		t.Fatal("signal flood starved control")
	}
	h.closeDirected(c)
	h.closeDirected(m)
	if h.direct.bytes != [2]int{} {
		t.Fatal("close leaked reservations")
	}
}
func TestDirectedRateAndGlobalBudgets(t *testing.T) {
	h, c, m := directedFixture(t)
	now := time.Now()
	h.direct.mu.Lock()
	for i := 0; i < 256; i++ {
		if !h.direct.allow(c, 0, 100, now) {
			t.Fatal("burst rejected")
		}
	}
	if h.direct.allow(c, 0, 100, now) {
		t.Fatal("device frame rate unenforced")
	}
	if !h.direct.allow(c, 0, 100, now.Add(time.Second/256)) {
		t.Fatal("tokens not refilled")
	}
	// A reconnect cannot reset the per-device bucket.
	copy := *c
	copy.direct = directedQueue{}
	if h.direct.allow(&copy, 0, 100, now.Add(time.Second/256)) {
		t.Fatal("reconnect reset rate")
	}
	h.direct.bytes = [2]int{directedSignalBytes, 0}
	if h.enqueueDirected(m, directedFrame{data: []byte{1}, class: 0}) {
		t.Fatal("signal borrowed reserved control bytes")
	}
	if !h.enqueueDirected(m, directedFrame{data: []byte{1}, class: 1}) {
		t.Fatal("control reserve unavailable")
	}
	h.direct.bytes = [2]int{directedSignalBytes, directedTotalBytes - directedSignalBytes}
	if h.enqueueDirected(m, directedFrame{data: []byte{1}, class: 2}) {
		t.Fatal("receipts bypassed global budget")
	}
	h.direct.mu.Unlock()
}
func TestDirectedAccountLimitsAcrossDevices(t *testing.T) {
	for _, class := range []int{0, 1} {
		h, c, _ := directedFixture(t)
		now := time.Now()
		h.direct.mu.Lock()
		count := 1024
		if class == 1 {
			count = 2048
		}
		for i := 0; i < count; i++ {
			p := &peer{dev: &device{id: fmt.Sprintf("peer-%d", i/128), account: c.dev.account}}
			if !h.direct.allow(p, class, 1, now) {
				t.Fatal("account burst rejected early")
			}
		}
		p := &peer{dev: &device{id: "new-peer", account: c.dev.account}}
		if h.direct.allow(p, class, 1, now) {
			t.Fatal("devices bypassed account frame rate")
		}
		h.direct.mu.Unlock()
	}
}
func TestDirectedWebSocketDeliveredAndLostReceipt(t *testing.T) {
	ts, mid, _, _, _, ms := newV2Env(t)
	private, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if err != nil {
		t.Fatal(err)
	}
	cs := ts.stream(t, ts.login(t, private, id))
	request := &pb.DirectedPeerFrame{ToDevice: []byte(mid), FrameClass: pb.FrameClass_SIGNAL, FrameId: []byte("1234567890123456"), Sealed: []byte("opaque packet")}
	var first []byte
	for i := 0; i < 2; i++ {
		cs.write(t, &pb.PeerFrame{Kind: &pb.PeerFrame_DirectedPeerFrame{DirectedPeerFrame: request}})
		f := ms.next(t, func(f *pb.ServerFrame) bool { return f.GetPeerFrameDelivery() != nil }).GetPeerFrameDelivery()
		if string(f.FromDevice) != id || !bytes.Equal(f.Sealed, request.Sealed) {
			t.Fatal("sender or ciphertext changed")
		}
		raw, _ := proto.Marshal(f)
		if i == 0 {
			first = raw
		} else if !bytes.Equal(first, raw) {
			t.Fatal("transparent retry changed delivery")
		}
		// Consume and discard first receipt as a lost network receipt. The server
		// must still forward the byte-identical second attempt without storing it.
		result := cs.next(t, func(f *pb.ServerFrame) bool { return f.GetPeerFrameResult() != nil }).GetPeerFrameResult()
		if result.Status != pb.PeerFrameResult_DELIVERED {
			t.Fatalf("receipt: %v", result)
		}
	}
}

func TestReplacedMainSocketCannotOriginateDirectedTraffic(t *testing.T) {
	h, old, machine := directedFixture(t)
	current := &peer{dev: old.dev, out: make(chan queuedFrame, sendQueue)}
	h.initDirected(current)
	h.directClients[old.dev.id] = current
	h.directed(old, directedRequest(machine, 1, pb.FrameClass_SIGNAL), time.Now())
	if machine.direct.count[0] != 0 {
		t.Fatal("replaced main socket forwarded peer traffic")
	}
	if popDirected(t, h, old, 2).GetPeerFrameResult().Status != pb.PeerFrameResult_OFFLINE {
		t.Fatal("stale source was acknowledged as delivered")
	}
	h.directed(current, directedRequest(machine, 2, pb.FrameClass_SIGNAL), time.Now())
	if machine.direct.count[0] != 1 {
		t.Fatal("current source stopped working")
	}
}
