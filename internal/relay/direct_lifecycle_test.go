package relay

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/internal/transport/direct"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

func TestRetiredDirectChooserCannotReplaceCurrentPeer(t *testing.T) {
	makeIdentity := func(role uint64) peerwire.Identity {
		pub, key, _ := ed25519.GenerateKey(rand.Reader)
		box, _ := ecdh.X25519().GenerateKey(rand.Reader)
		return peerwire.Identity{ID: []byte(hex.EncodeToString(peerwire.Hash(pub)[:16])), Role: role, Sign: key, Box: box, Instance: make([]byte, 16), Boot: make([]byte, 32)}
	}
	self, peer := makeIdentity(2), makeIdentity(1)
	codec, _ := peerwire.NewCodec("00000000000000000000000000000001", [32]byte{1}, [32]byte{2}, 1)
	makePeer := func(value byte) *directPeer {
		connection := make([]byte, 16)
		connection[0] = value
		s, err := direct.NewSession(context.Background(), direct.SessionConfig{Self: self, Peer: peer.Public(), Codec: codec, Connection: connection, Deadline: uint64(time.Now().Add(10 * time.Second).UnixMilli()), Allow: func() bool { return true }, Send: func(direct.Outbound) bool { return true }}, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(); s.Wait() })
		p := &directPeer{session: s, connection: connection}
		p.binding.Peer.ID = string(peer.ID)
		p.processValid.Store(true)
		return p
	}
	old, current := makePeer(1), makePeer(2)
	id := string(peer.ID)
	m := &directManager{active: map[string]*directPeer{id: current}, sessions: map[string]*directPeer{string(old.connection): old, string(current.connection): current}}
	old.session.Retire()
	m.choose(old)
	if m.active[id] != current || !current.session.Active() {
		t.Fatal("retired chooser replaced or retired the current peer")
	}
}

type delayedRelayConn struct {
	net.Conn
	delayActive *atomic.Bool
	delay       time.Duration
	writes      *atomic.Int32
}

func (c *delayedRelayConn) Write(p []byte) (int, error) {
	if c.delayActive.CompareAndSwap(true, false) {
		c.writes.Add(1)
		time.Sleep(c.delay)
	}
	return c.Conn.Write(p)
}

func TestDirectWriteLoopRemainingResidenceDoesNotCloseSlowHealthyMainWebSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	received := make(chan *pb.PeerFrame, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			_, raw, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			f := &pb.PeerFrame{}
			if proto.Unmarshal(raw, f) == nil {
				received <- f
			}
		}
	}))
	defer srv.Close()

	var delayActive atomic.Bool
	var delayedWrites atomic.Int32
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(dctx context.Context, network, addr string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(dctx, network, addr)
				if err != nil {
					return nil, err
				}
				return &delayedRelayConn{Conn: conn, delayActive: &delayActive, delay: 60 * time.Millisecond, writes: &delayedWrites}, nil
			},
		},
	}
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	relayPath := transport.NewRelayPath(ws)

	mctx, mcancel := context.WithCancel(ctx)
	m := &directManager{
		ctx:       mctx,
		cancel:    mcancel,
		queued:    map[string]*directedUsage{},
		writeWake: make(chan struct{}, 1),
	}
	for i := range m.outgoing {
		m.outgoing[i] = transport.FairQueue[*peerOutput]{Entries: 64, Bytes: 1 << 20, PeerEntries: 16, PeerBytes: 256 << 10}
	}
	m.workers.Go(m.writeLoop)
	defer func() {
		mcancel()
		m.workers.Wait()
	}()
	_, detach := m.attach(relayPath)
	defer detach()

	peerID := strings.Repeat("a", 32)
	id := make([]byte, 16)
	raw := peerwire.PB(
		peerwire.B(1, []byte(strings.Repeat("b", 32))),
		peerwire.B(2, []byte(peerID)),
		peerwire.U(3, 1),
		peerwire.B(4, id),
		peerwire.B(5, []byte("sealed-signal")),
	)

	// Remaining residence is ~15 ms, while the healthy socket write takes 60 ms.
	delayActive.Store(true)
	if !m.send(peerID, direct.Outbound{
		Raw:      raw,
		Deadline: uint64(time.Now().Add(15 * time.Millisecond).UnixMilli()),
		Critical: true,
		Valid:    func() bool { return true },
	}) {
		t.Fatal("failed to queue directed frame")
	}

	drainDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(drainDeadline) {
		m.mu.Lock()
		empty := m.queuedBytes == [2]int{} && len(m.queued) == 0
		m.mu.Unlock()
		if empty {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if delayedWrites.Load() != 1 {
		t.Fatalf("expected delayed write to run, got %d", delayedWrites.Load())
	}

	// Main WebSocket must still be open and able to carry normal relay traffic.
	appFrame, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: "healthy"}}})
	if err := relayPath.Write(ctx, appFrame); err != nil {
		t.Fatalf("main WebSocket closed by optional signaling write: %v", err)
	}
	select {
	case f := <-received:
		if f.GetDirectedPeerFrame() == nil || !bytes.Equal(f.GetDirectedPeerFrame().FrameId, id) {
			t.Fatalf("unexpected first frame %v", f)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for directed frame")
	}
	select {
	case f := <-received:
		if f.GetHello() == nil || f.GetHello().Token != "healthy" {
			t.Fatalf("unexpected application frame %v", f)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for application frame on main WebSocket")
	}
}

type blockingWritePath struct {
	transport.Path
	writes atomic.Int32
}

func (p *blockingWritePath) Write(context.Context, []byte) error {
	p.writes.Add(1)
	return nil
}

func TestDirectWriteLoopDropsExpiredFrameBeforeDispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mctx, mcancel := context.WithCancel(ctx)
	m := &directManager{
		ctx:       mctx,
		cancel:    mcancel,
		queued:    map[string]*directedUsage{},
		writeWake: make(chan struct{}, 1),
	}
	for i := range m.outgoing {
		m.outgoing[i] = transport.FairQueue[*peerOutput]{Entries: 64, Bytes: 1 << 20, PeerEntries: 16, PeerBytes: 256 << 10}
	}
	p := &blockingWritePath{}
	_, detach := m.attach(p)
	defer detach()

	peerID := strings.Repeat("c", 32)
	q := &peerOutput{
		peer:  peerID,
		class: 0,
		data:  []byte("expired"),
		out:   direct.Outbound{Valid: func() bool { return true }},
		until: time.Now().Add(-time.Millisecond),
	}
	m.mu.Lock()
	m.outgoing[1].Push(peerID, peerID, len(q.data), q)
	m.queued[peerID] = &directedUsage{frames: [2]int{1, 0}, bytes: [2]int{len(q.data), 0}}
	m.queuedBytes[0] = len(q.data)
	m.mu.Unlock()
	notify(m.writeWake)

	m.workers.Go(m.writeLoop)
	defer func() {
		mcancel()
		m.workers.Wait()
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		empty := m.queuedBytes == [2]int{} && len(m.queued) == 0
		m.mu.Unlock()
		if empty {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if p.writes.Load() != 0 {
		t.Fatalf("expired frame was physically written (%d writes)", p.writes.Load())
	}
	m.mu.Lock()
	leaked := m.queuedBytes != [2]int{} || len(m.queued) != 0
	m.mu.Unlock()
	if leaked {
		t.Fatal("expired frame leaked queue accounting")
	}
}

