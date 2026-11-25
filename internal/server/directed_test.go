package server

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/coder/websocket"
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

type delayedWriteConn struct {
	net.Conn
	delayActive *atomic.Bool
	delay       time.Duration
	writes      *atomic.Int32
}

func (c *delayedWriteConn) Write(p []byte) (int, error) {
	if c.delayActive.CompareAndSwap(true, false) {
		c.writes.Add(1)
		time.Sleep(c.delay)
	}
	return c.Conn.Write(p)
}

type delayedAcceptListener struct {
	net.Listener
	armNext     atomic.Bool
	delayActive atomic.Bool
	delay       time.Duration
	writes      atomic.Int32
}

func (l *delayedAcceptListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if l.armNext.CompareAndSwap(true, false) {
		return &delayedWriteConn{Conn: conn, delayActive: &l.delayActive, delay: l.delay, writes: &l.writes}, nil
	}
	return conn, nil
}

func TestDirectedRemainingResidenceDoesNotCloseSlowHealthyMainWebSocket(t *testing.T) {
	ts := newTestServer(t)
	mpriv, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	cpriv, cid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if err != nil {
		t.Fatal(err)
	}
	mtok, ctok := ts.login(t, mpriv, mid), ts.login(t, cpriv, cid)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dl := &delayedAcceptListener{Listener: ln, delay: 60 * time.Millisecond}
	srv := &http.Server{Handler: ts.srv.Handler()}
	go func() { _ = srv.Serve(dl) }()
	t.Cleanup(func() { _ = srv.Close() })

	// Connect machine stream through the delayed listener so a healthy socket
	// write taking 60 ms completes even when the directed frame has ~15 ms left.
	dl.armNext.Store(true)
	mws, _, err := websocket.Dial(context.Background(), "ws://"+ln.Addr().String()+"/v1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	mws.SetReadLimit(8 << 20)
	t.Cleanup(func() { mws.CloseNow() })
	hello, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: mtok}}})
	if err := mws.Write(context.Background(), websocket.MessageBinary, hello); err != nil {
		t.Fatal(err)
	}
	ms := &testStream{ws: mws}
	if f := ms.read(t); f.GetReady() == nil {
		t.Fatalf("first frame %v", f)
	}
	cs := ts.stream(t, ctok)

	ts.srv.hub.mu.Lock()
	clientPeer := ts.srv.hub.directClients[cid]
	machinePeer := ts.srv.hub.machines[mid]
	ts.srv.hub.mu.Unlock()
	if clientPeer == nil || machinePeer == nil {
		t.Fatal("missing connected peers")
	}

	dl.delayActive.Store(true)
	req := &pb.DirectedPeerFrame{
		ToDevice:   []byte(mid),
		FrameClass: pb.FrameClass_SIGNAL,
		FrameId:    []byte("1234567890123456"),
		Sealed:     []byte("opaque packet"),
	}
	ts.srv.hub.directed(clientPeer, req, time.Now().Add(-directedResidence+15*time.Millisecond))

	delivery := ms.next(t, func(f *pb.ServerFrame) bool { return f.GetPeerFrameDelivery() != nil }).GetPeerFrameDelivery()
	if !bytes.Equal(delivery.FrameId, req.FrameId) {
		t.Fatalf("unexpected delivery %v", delivery)
	}
	if dl.writes.Load() != 1 {
		t.Fatalf("expected delayed socket write to execute, got %d", dl.writes.Load())
	}
	result := cs.next(t, func(f *pb.ServerFrame) bool { return f.GetPeerFrameResult() != nil }).GetPeerFrameResult()
	if result.Status != pb.PeerFrameResult_DELIVERED {
		t.Fatalf("expected DELIVERED, got %v", result.Status)
	}
	if !ts.srv.hub.send(machinePeer, &pb.ServerFrame{Kind: &pb.ServerFrame_Heartbeat{Heartbeat: &pb.Heartbeat{}}}) {
		t.Fatal("main stream closed after delayed directed write")
	}
	if hb := ms.next(t, func(f *pb.ServerFrame) bool { return f.GetHeartbeat() != nil }); hb.GetHeartbeat() == nil {
		t.Fatal("missing subsequent heartbeat on main stream")
	}
}

func TestDirectedExpiredBeforeDispatchDropsAsLimitedWithoutClosingMainWS(t *testing.T) {
	ts, mid, cid, _, _, ms := newV2Env(t)
	cpriv, cid2, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = cid
	cs := ts.stream(t, ts.login(t, cpriv, cid2))

	ts.srv.hub.mu.Lock()
	clientPeer := ts.srv.hub.directClients[cid2]
	machinePeer := ts.srv.hub.machines[mid]
	ts.srv.hub.mu.Unlock()
	if clientPeer == nil || machinePeer == nil {
		t.Fatal("missing peers")
	}
	// Enqueue an already-expired frame directly to exercise writeDirected's pre-dispatch check.
	q := directedFrame{
		data:   []byte("expired-frame"),
		source: clientPeer,
		id:     []byte("abcdef0123456789"),
		until:  time.Now().Add(-time.Millisecond),
		class:  0,
	}
	ts.srv.hub.direct.mu.Lock()
	ok := ts.srv.hub.enqueueDirected(machinePeer, q)
	ts.srv.hub.direct.mu.Unlock()
	if !ok {
		t.Fatal("failed to enqueue test frame")
	}
	result := cs.next(t, func(f *pb.ServerFrame) bool { return f.GetPeerFrameResult() != nil }).GetPeerFrameResult()
	if result.Status != pb.PeerFrameResult_LIMITED || !bytes.Equal(result.FrameId, q.id) {
		t.Fatalf("expected LIMITED for expired directed frame, got %v", result)
	}
	if !ts.srv.hub.send(machinePeer, &pb.ServerFrame{Kind: &pb.ServerFrame_Heartbeat{Heartbeat: &pb.Heartbeat{}}}) {
		t.Fatal("main stream closed after expired directed frame")
	}
	if hb := ms.next(t, func(f *pb.ServerFrame) bool { return f.GetHeartbeat() != nil }); hb.GetHeartbeat() == nil {
		t.Fatal("missing heartbeat on main stream")
	}
	if strings.Contains(t.Name(), "skip") {
		t.Fatal("unreachable")
	}
}

// Bug 8: Closing a newer socket for a multi-connection client must fall back to the surviving socket.
func TestMultiConnClientClosingNewerSocketRestoresOlderDirectReceiver(t *testing.T) {
	ts, mid, _, _, _, ms := newV2Env(t)
	cpriv, cid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if err != nil {
		t.Fatal(err)
	}
	tok := ts.login(t, cpriv, cid)
	tab1 := ts.stream(t, tok)
	tab2 := ts.stream(t, tok)

	// Close tab2 and wait until hub.remove(tab2) has processed.
	tab2.ws.CloseNow()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ts.srv.hub.mu.Lock()
		n := 0
		for _, peers := range ts.srv.hub.clients {
			for p := range peers {
				if p.dev.id == cid {
					n++
				}
			}
		}
		ts.srv.hub.mu.Unlock()
		if n <= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// tab1 -> machine must deliver (not OFFLINE).
	req := &pb.DirectedPeerFrame{
		ToDevice:   []byte(mid),
		FrameClass: pb.FrameClass_SIGNAL,
		FrameId:    []byte("tab1-to-machine1"),
		Sealed:     []byte("hello-machine"),
	}
	tab1.write(t, &pb.PeerFrame{Kind: &pb.PeerFrame_DirectedPeerFrame{DirectedPeerFrame: req}})
	res := tab1.next(t, func(f *pb.ServerFrame) bool { return f.GetPeerFrameResult() != nil }).GetPeerFrameResult()
	if res.Status != pb.PeerFrameResult_DELIVERED {
		t.Fatalf("tab1 -> machine status = %v, want DELIVERED", res.Status)
	}
	deliv := ms.next(t, func(f *pb.ServerFrame) bool { return f.GetPeerFrameDelivery() != nil }).GetPeerFrameDelivery()
	if !bytes.Equal(deliv.FrameId, req.FrameId) {
		t.Fatalf("machine received wrong frame %q", deliv.FrameId)
	}

	// machine -> client must deliver to tab1 (not OFFLINE).
	req2 := &pb.DirectedPeerFrame{
		ToDevice:   []byte(cid),
		FrameClass: pb.FrameClass_SIGNAL,
		FrameId:    []byte("machine-to-tab11"),
		Sealed:     []byte("hello-tab1"),
	}
	ms.write(t, &pb.PeerFrame{Kind: &pb.PeerFrame_DirectedPeerFrame{DirectedPeerFrame: req2}})
	res2 := ms.next(t, func(f *pb.ServerFrame) bool { return f.GetPeerFrameResult() != nil }).GetPeerFrameResult()
	if res2.Status != pb.PeerFrameResult_DELIVERED {
		t.Fatalf("machine -> tab1 status = %v, want DELIVERED", res2.Status)
	}
	deliv2 := tab1.next(t, func(f *pb.ServerFrame) bool { return f.GetPeerFrameDelivery() != nil }).GetPeerFrameDelivery()
	if !bytes.Equal(deliv2.FrameId, req2.FrameId) {
		t.Fatalf("tab1 received wrong frame %q", deliv2.FrameId)
	}
}

// Opt 4: Draining directed out slices resets them to nil; rate-limit RetryAfterMs reflects actual token deficit.
func TestDirectedQueueDrainResetsSliceAndRateLimitRetryAfterIsShort(t *testing.T) {
	h, c, m := directedFixture(t)
	now := time.Now()
	h.directed(c, directedRequest(m, 1, pb.FrameClass_SIGNAL), now)
	_ = popDirected(t, h, m, 0)
	h.direct.mu.Lock()
	outNil := m.direct.out[0] == nil
	h.direct.mu.Unlock()
	if !outNil {
		t.Fatal("drained directed out slice was not reset to nil")
	}

	// Exhaust the 256-frame Signal burst (1 frame deficit at 256 fps is ~4ms -> clamped to 50ms, not 1000ms).
	h2, c2, m2 := directedFixture(t)
	for i := range 256 {
		h2.directed(c2, directedRequest(m2, i, pb.FrameClass_SIGNAL), now)
	}
	h2.directed(c2, directedRequest(m2, 999, pb.FrameClass_SIGNAL), now)
	res := popDirected(t, h2, c2, 2).GetPeerFrameResult()
	if res.Status != pb.PeerFrameResult_LIMITED {
		t.Fatalf("status = %v, want LIMITED", res.Status)
	}
	if res.RetryAfterMs < 50 || res.RetryAfterMs > 200 {
		t.Fatalf("RetryAfterMs = %d, want in [50, 200] instead of hardcoded 1000", res.RetryAfterMs)
	}

	// On-demand directedExpiryLoop expires queued frame promptly without busy polling.
	h3, c3, m3 := directedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h3.directedExpiryLoop(ctx, m3)
	}()
	h3.directed(c3, directedRequest(m3, 1, pb.FrameClass_SIGNAL), time.Now().Add(-directedResidence+40*time.Millisecond))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		h3.direct.mu.Lock()
		n := c3.direct.count[2]
		h3.direct.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	res3 := popDirected(t, h3, c3, 2).GetPeerFrameResult()
	if res3.Status != pb.PeerFrameResult_LIMITED {
		t.Fatalf("expired frame status = %v, want LIMITED", res3.Status)
	}
	cancel()
	<-done
}
