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

// The session rules are tested in shared/linkcore against a fake executor.
// These tests run the executor with real Pion PCs on a session clock that
// moves only when the test or the pump moves it, so a loaded machine cannot
// run a round past its deadline. Each context is only a guard against a hung
// run.

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

// critical reports whether a frame is a critical round message, as the
// executor marks it for the main connection.
func critical(o Outbound) bool { return o.Critical }

// exchange writes one frame over each confirmed use and reads it back.
func exchange(t *testing.T, ctx context.Context, from, to *pumpEnd) {
	t.Helper()
	for use := 1; use <= 2; use++ {
		raw := []byte("authenticated encrypted frame")
		done := make(chan error, 1)
		go func() { done <- from.ready[use].Write(ctx, raw) }()
		got, err := to.ready[use].Read(ctx)
		if err != nil || string(got) != string(raw) {
			t.Fatalf("use %d: %v", use, err)
		}
		if err = <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// pumpPair starts a responder and an initiator over real Pion PCs.
func pumpPair(t *testing.T, p *pump) (initiator, responder *pumpEnd, a, b *Identity) {
	a, b = identity(t, 1), identity(t, 2)
	codec := testCodec(t)
	connection := make([]byte, 16)
	_, _ = rand.Read(connection)
	deadline := p.clock.Ms() + 10000
	cfg := func(self *Identity, other peerwire.Public) SessionConfig {
		return SessionConfig{Self: self, Peer: other.Member(), Codec: codec, Connection: connection, Deadline: deadline, Pool: NewPool(8), Allow: func() bool { return true }, LoopbackOnly: true}
	}
	responder = p.add(cfg(b, a.Public()), false)
	initiator = p.add(cfg(a, b.Public()), true)
	pair(initiator, responder)
	return initiator, responder, a, b
}

// offererGap drops, for good, sequence 2 of generation 1 on both uses from
// the offerer: the answerer's gap expires and both uses restart in
// generation 2, whose PCs are created while the old ones release the pool.
func offererGap(t *testing.T, clock *testClock, offerer *Identity, other peerwire.Public) func(Outbound) bool {
	codec := testCodec(t)
	return func(o Outbound) bool {
		packet, err := offerer.OpenSent(codec, o.Raw, other, clock.Ms())
		c := packet.Context
		return err == nil && c.Class == 1 && c.Use > 0 && c.Generation == 1 && c.Seq == 2
	}
}

// Real PCs: lost frames are retransmitted and a permanent gap restarts the
// generation, all within the round's 10 s of session time; up to two lost
// frames stay inside the 9.75 s success budget (transport.md section 5).
func TestRealPCsEstablishDespiteLossWithinTheRound(t *testing.T) {
	cases := []struct {
		name string
		loss func(t *testing.T, initiator, responder *pumpEnd, a, b *Identity)
		gap  bool
	}{
		{name: "no loss"},
		{name: "first two initiator frames lost", loss: func(_ *testing.T, initiator, _ *pumpEnd, _, _ *Identity) {
			lost := 0
			initiator.drop = func(Outbound) bool { lost++; return lost <= 2 }
		}},
		{name: "two critical responder frames lost", loss: func(_ *testing.T, _, responder *pumpEnd, _, _ *Identity) {
			lost := 0
			responder.drop = func(o Outbound) bool {
				if critical(o) && lost < 2 {
					lost++
					return true
				}
				return false
			}
		}},
		{name: "permanent gap restarts the generation", gap: true, loss: func(t *testing.T, initiator, responder *pumpEnd, a, b *Identity) {
			if string(a.ID) < string(b.ID) {
				initiator.drop = offererGap(t, initiator.clock, a, b.Public())
			} else {
				responder.drop = offererGap(t, responder.clock, b, a.Public())
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			p := newPump(t, ctx)
			initiator, responder, a, b := pumpPair(t, p)
			if c.loss != nil {
				c.loss(t, initiator, responder, a, b)
			}
			p.run(func() bool {
				return initiator.bothReady() && responder.bothReady() || initiator.s.stopped || responder.s.stopped
			})
			if !initiator.bothReady() || !responder.bothReady() {
				t.Fatalf("round ended after %d ms of session time: %v / %v", p.elapsed(), initiator.s.core.Steps(), responder.s.core.Steps())
			}
			budget := uint64(9750)
			if c.gap {
				budget = 10000
			}
			if p.elapsed() >= budget {
				t.Fatalf("ready after %d ms of session time, budget %d", p.elapsed(), budget)
			}
			for _, e := range []*pumpEnd{initiator, responder} {
				for use := 1; use <= 2; use++ {
					if gen := e.ready[use].cfg.Generation; (gen >= 2) != c.gap {
						t.Fatalf("use %d ready in generation %d", use, gen)
					}
				}
			}
			exchange(t, ctx, initiator, responder)
			t.Logf("ready after %d ms of session time", p.elapsed())
		})
	}
}

// Eight clients connect to one daemon at once, sharing its PC pool; each
// pair loses its first two critical client frames, and all 32 PCs are ready
// within the 9.75 s budget of session time.
func TestEightPeersLoseTwoCriticalFramesWithinTheRound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newPump(t, ctx)
	machine := identity(t, 2)
	codec := testCodec(t)
	pool := NewPool(8)
	deadline := p.clock.Ms() + 10000
	var ends []*pumpEnd
	for range 8 {
		client := identity(t, 1)
		conn := make([]byte, 16)
		_, _ = rand.Read(conn)
		common := SessionConfig{Codec: codec, Connection: conn, Deadline: deadline, Allow: func() bool { return true }, LoopbackOnly: true}
		daemonCfg := common
		daemonCfg.Self, daemonCfg.Peer, daemonCfg.Pool = machine, client.Public().Member(), pool
		clientCfg := common
		clientCfg.Self, clientCfg.Peer, clientCfg.Pool = client, machine.Public().Member(), NewPool(4)
		daemonSide := p.add(daemonCfg, false)
		clientSide := p.add(clientCfg, true)
		pair(clientSide, daemonSide)
		lost := 0
		clientSide.drop = func(o Outbound) bool {
			if critical(o) && lost < 2 {
				lost++
				return true
			}
			return false
		}
		ends = append(ends, daemonSide, clientSide)
	}
	p.run(func() bool {
		for _, e := range ends {
			if !e.bothReady() && !e.s.stopped {
				return false
			}
		}
		return true
	})
	for _, e := range ends {
		if !e.bothReady() {
			t.Fatalf("a round ended unready after %d ms of session time", p.elapsed())
		}
	}
	if p.elapsed() >= 9750 {
		t.Fatalf("32 PCs ready after %d ms of session time, budget 9750", p.elapsed())
	}
	if _, err := New(ctx, Config{Peer: "ninth-fixture", Owner: "round", Use: 1, Generation: 1, Pool: pool, Allow: func() bool { return true }, LoopbackOnly: true}); err == nil {
		t.Fatal("connected peer budget exceeded")
	}
	t.Logf("8 peers, 32 real PCs, 2 lost critical frames per pair: %d ms of session time", p.elapsed())
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
