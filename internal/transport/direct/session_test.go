package direct

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

// The session rules (loss, gaps, restarts, deadlines, receipts) are tested
// in shared/linkcore against a fake executor. These tests run the executor
// with real Pion PCs on a clock that moves only when the test moves it, so a
// loaded machine cannot run a round past its deadline. The context is only a
// guard against a hung run.

func identity(t *testing.T, role uint64) *Identity {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	box, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(public)
	instance, boot := make([]byte, 16), make([]byte, 32)
	_, _ = rand.Read(instance)
	_, _ = rand.Read(boot)
	return &Identity{ID: []byte(hex.EncodeToString(hash[:16])), Role: role, Instance: instance, Boot: boot, Sign: private, Box: box}
}

// testClock is a clock that only the test advances.
type testClock struct{ ms atomic.Int64 }

func newTestClock() *testClock {
	c := &testClock{}
	c.ms.Store(time.Now().UnixMilli())
	return c
}
func (c *testClock) Now() time.Time          { return time.UnixMilli(c.ms.Load()) }
func (c *testClock) Ms() uint64              { return uint64(c.ms.Load()) }
func (c *testClock) Advance(d time.Duration) { c.ms.Add(d.Milliseconds()) }

// peerSlot delivers frames to a session that is published after its peer
// has started: with the clock standing still nothing is retransmitted, so a
// frame must wait for its receiver rather than be lost.
type peerSlot struct {
	s   atomic.Pointer[Session]
	set chan struct{}
}

func newPeerSlot() *peerSlot { return &peerSlot{set: make(chan struct{})} }
func (p *peerSlot) publish(s *Session) {
	p.s.Store(s)
	close(p.set)
}
func (p *peerSlot) deliver(ctx context.Context, raw []byte) bool {
	select {
	case <-p.set:
		return p.s.Load().Receive(raw)
	case <-ctx.Done():
		return false
	}
}

func testCodec(t *testing.T) *peerwire.Codec {
	domain, head := sha256.Sum256([]byte("domain")), sha256.Sum256([]byte("head"))
	codec, err := peerwire.NewCodec("00000000000000000000000000000001", domain, head, 1, protocol.Version)
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func TestAuthenticatedSessionConnectsBothRealPCs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	a, b := identity(t, 1), identity(t, 2)
	codec := testCodec(t)
	clock := newTestClock()
	connection := make([]byte, 16)
	_, _ = rand.Read(connection)
	slots := [2]*peerSlot{newPeerSlot(), newPeerSlot()}
	type ready struct {
		which int
		use   uint64
		path  *Path
	}
	paths := make(chan ready, 4)
	deadline := clock.Ms() + 10000
	cfg := func(self *Identity, other peerwire.Public, which int) SessionConfig {
		return SessionConfig{Self: self, Peer: other.Member(), Codec: codec, Connection: connection, Deadline: deadline, Pool: NewPool(8), Allow: func() bool { return true }, LoopbackOnly: true, Now: clock.Now,
			Ready: func(use uint64, p *Path) { paths <- ready{which, use, p} },
			Send:  func(out Outbound) bool { return slots[1-which].deliver(ctx, out.Raw) }}
	}
	sb, err := NewSession(ctx, cfg(b, a.Public(), 1), false)
	if err != nil {
		t.Fatal(err)
	}
	slots[1].publish(sb)
	defer func() { sb.Close(); sb.Wait() }()
	sa, err := NewSession(ctx, cfg(a, b.Public(), 0), true)
	if err != nil {
		t.Fatal(err)
	}
	slots[0].publish(sa)
	defer func() { sa.Close(); sa.Wait() }()
	var connected [2][2]*Path
	for complete := 0; complete < 4; {
		select {
		case p := <-paths:
			if connected[p.which][p.use-1] == nil {
				complete++
			}
			connected[p.which][p.use-1] = p.path
		case <-ctx.Done():
			t.Fatal("handshake hung")
		}
	}
	for i := 0; i < 2; i++ {
		raw := []byte("authenticated encrypted frame")
		done := make(chan error, 1)
		go func() { done <- connected[0][i].Write(ctx, raw) }()
		got, err := connected[1][i].Read(ctx)
		if err != nil || string(got) != string(raw) {
			t.Fatalf("path %d: %v", i, err)
		}
		if err = <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestEightPeersShareTheDaemonPoolWithRealPCs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	machine := identity(t, 2)
	codec := testCodec(t)
	clock := newTestClock()
	pool := NewPool(8)
	ready := make(chan *Path, 32)
	var sessions []*Session
	defer func() {
		for _, s := range sessions {
			s.Close()
		}
		for _, s := range sessions {
			s.Wait()
		}
	}()
	deadline := clock.Ms() + 10000
	for range 8 {
		client := identity(t, 1)
		conn := make([]byte, 16)
		_, _ = rand.Read(conn)
		daemonSide, clientSide := newPeerSlot(), newPeerSlot()
		common := SessionConfig{Codec: codec, Connection: conn, Deadline: deadline, Allow: func() bool { return true }, LoopbackOnly: true, Now: clock.Now, Ready: func(_ uint64, p *Path) { ready <- p }}
		serverCfg := common
		serverCfg.Self = machine
		serverCfg.Peer = client.Public().Member()
		serverCfg.Pool = pool
		serverCfg.Send = func(out Outbound) bool { return clientSide.deliver(ctx, out.Raw) }
		ds, err := NewSession(ctx, serverCfg, false)
		if err != nil {
			t.Fatal(err)
		}
		daemonSide.publish(ds)
		sessions = append(sessions, ds)
		clientCfg := common
		clientCfg.Self = client
		clientCfg.Peer = machine.Public().Member()
		clientCfg.Pool = NewPool(4)
		clientCfg.Send = func(out Outbound) bool { return daemonSide.deliver(ctx, out.Raw) }
		cs, err := NewSession(ctx, clientCfg, true)
		if err != nil {
			t.Fatal(err)
		}
		clientSide.publish(cs)
		sessions = append(sessions, cs)
	}
	for i := 0; i < 32; i++ {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatalf("only %d of 32 SDK PCs completed the round", i)
		}
	}
	if _, err := New(ctx, Config{Peer: "ninth-fixture", Owner: "round", Use: 1, Generation: 1, Pool: pool, Allow: func() bool { return true }, LoopbackOnly: true}); err == nil {
		t.Fatal("connected peer budget exceeded")
	}
}

func TestPartialReadyRoundDoneAndSessionDoneWhenPathsClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	a, b := identity(t, 1), identity(t, 2)
	codec := testCodec(t)
	clock := newTestClock()
	connection := make([]byte, 16)
	_, _ = rand.Read(connection)
	deadline := clock.Ms() + 9000

	slots := [2]*peerSlot{newPeerSlot(), newPeerSlot()}
	type readyEvent struct {
		which int
		use   uint64
		path  *Path
	}
	paths := make(chan readyEvent, 4)
	makeCfg := func(self *Identity, other peerwire.Public, which int) SessionConfig {
		return SessionConfig{
			Self: self, Peer: other.Member(), Codec: codec, Connection: connection,
			Deadline: deadline, Pool: NewPool(4), Allow: func() bool { return true },
			LoopbackOnly: true, Now: clock.Now,
			Ready: func(use uint64, p *Path) { paths <- readyEvent{which, use, p} },
			Send: func(out Outbound) bool {
				// Use 2 never gets through: only use 1 can become ready.
				if packet, e := self.OpenSent(codec, out.Raw, other, clock.Ms()); e == nil && packet.Context.Use == 2 {
					return true
				}
				return slots[1-which].deliver(ctx, out.Raw)
			},
		}
	}
	sb, err := NewSession(ctx, makeCfg(b, a.Public(), 1), false)
	if err != nil {
		t.Fatal(err)
	}
	slots[1].publish(sb)
	defer func() { sb.Close(); sb.Wait() }()
	sa, err := NewSession(ctx, makeCfg(a, b.Public(), 0), true)
	if err != nil {
		t.Fatal(err)
	}
	slots[0].publish(sa)
	defer func() { sa.Close(); sa.Wait() }()

	var use1 [2]*Path
	for use1[0] == nil || use1[1] == nil {
		select {
		case ev := <-paths:
			if ev.use != 1 {
				t.Fatalf("unexpected ready use %d", ev.use)
			}
			use1[ev.which] = ev.path
		case <-ctx.Done():
			t.Fatal("use 1 did not become ready")
		}
	}

	clock.Advance(10 * time.Second)
	select {
	case <-sa.RoundDone():
	case <-ctx.Done():
		t.Fatal("RoundDone did not close after partial round passed deadline")
	}
	if !sa.Active() {
		t.Fatal("session became inactive while use 1 path was still alive")
	}
	msg := []byte("use1 still alive after round deadline")
	writeErr := make(chan error, 1)
	go func() { writeErr <- use1[0].Write(ctx, msg) }()
	got, err := use1[1].Read(ctx)
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("use 1 path failed after RoundDone: got=%q err=%v", got, err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("use 1 write failed after RoundDone: %v", err)
	}
	select {
	case <-sa.Done():
		t.Fatal("Session.Done closed while use 1 path was still alive")
	default:
	}

	use1[0].Close()
	use1[1].Close()
	select {
	case <-sa.Done():
	case <-ctx.Done():
		t.Fatal("Session.Done did not close after deadline and all paths closed")
	}
}

func TestRetireClosesRoundDoneImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	a, b := identity(t, 1), identity(t, 2)
	connection := make([]byte, 16)
	_, _ = rand.Read(connection)
	clock := newTestClock()
	s, err := NewSession(ctx, SessionConfig{
		Self: a, Peer: b.Public().Member(), Codec: testCodec(t), Connection: connection,
		Deadline: clock.Ms() + 9000,
		Pool:     NewPool(1), Allow: func() bool { return true },
		Now:  clock.Now,
		Send: func(Outbound) bool { return true },
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close(); s.Wait() }()
	s.Retire()
	select {
	case <-s.RoundDone():
	case <-ctx.Done():
		t.Fatal("Retire did not close RoundDone before Deadline")
	}
	select {
	case <-s.Done():
		t.Fatal("Retire closed Done before replay window ended")
	default:
	}
}
