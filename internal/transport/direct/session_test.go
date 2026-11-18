package direct

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
	"sync/atomic"
	"testing"
	"time"
)

func identity(t *testing.T, role uint64) peerwire.Identity {
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
	return peerwire.Identity{ID: []byte(hex.EncodeToString(hash[:16])), Role: role, Instance: instance, Boot: boot, Sign: private, Box: box}
}
func TestAuthenticatedSessionBothUsesAndLostBootstrap(t *testing.T) {
	for _, lost := range []int32{0, 2, -1} {
		t.Run(hex.EncodeToString([]byte{byte(lost)}), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			a, b := identity(t, 1), identity(t, 2)
			domain, head := sha256.Sum256([]byte("domain")), sha256.Sum256([]byte("head"))
			codec, err := peerwire.NewCodec("00000000000000000000000000000001", domain, head, 1)
			if err != nil {
				t.Fatal(err)
			}
			connection := make([]byte, 16)
			_, _ = rand.Read(connection)
			var ar, br atomic.Pointer[Session]
			var dropped atomic.Int32
			var frames atomic.Int32
			var highest atomic.Uint64
			type ready struct {
				which int
				use   uint64
				path  *Path
			}
			paths := make(chan ready, 4)
			deadline := uint64(time.Now().Add(10 * time.Second).UnixMilli())
			cfg := func(self peerwire.Identity, other peerwire.Public, which int) SessionConfig {
				return SessionConfig{Self: self, Peer: other.Member(), Codec: codec, Connection: connection, Deadline: deadline, Pool: NewPool(8), Allow: func() bool { return true }, LoopbackOnly: true, Ready: func(use uint64, p *Path) { paths <- ready{which, use, p} }, Send: func(out Outbound) bool {
					raw := out.Raw
					if packet, e := codec.OpenSent(raw, self, other, uint64(time.Now().UnixMilli())); e == nil && packet.Context.Class == 1 {
						if packet.Context.Generation > highest.Load() {
							highest.Store(packet.Context.Generation)
						}
						// Permanently lose sequence 2 of the first generation;
						// later frames witness a real gap, requiring new SDK PCs.
						if lost == -1 && string(self.ID) < string(other.ID) && packet.Context.Generation == 1 && packet.Context.Seq == 2 {
							return true
						}
					}
					frames.Add(1)
					if which == 0 && dropped.Add(1) <= lost {
						return true
					}
					target := ar.Load()
					if which == 0 {
						target = br.Load()
					}
					return target != nil && target.Receive(raw)
				}}
			}
			sa, err := NewSession(ctx, cfg(a, b.Public(), 0), true)
			if err != nil {
				t.Fatal(err)
			}
			ar.Store(sa)
			defer sa.Close()
			sb, err := NewSession(ctx, cfg(b, a.Public(), 1), false)
			if err != nil {
				t.Fatal(err)
			}
			br.Store(sb)
			defer sb.Close()
			var connected [2][2]*Path
			complete := 0
			for complete < 4 {
				select {
				case p := <-paths:
					if lost == -1 && p.path.cfg.Generation < 2 {
						continue
					}
					if connected[p.which][p.use-1] == nil {
						complete++
					}
					connected[p.which][p.use-1] = p.path
				case <-ctx.Done():
					t.Fatalf("handshake timed out after %d frames", frames.Load())
				}
			}
			if lost == -1 && highest.Load() < 2 {
				t.Fatal("missing frame did not restart the PC generation")
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
			t.Logf("both PC uses ready; loss=%d frames=%d", lost, frames.Load())
		})
	}
}

func TestEightPeersBothPCsLoseTwoCriticalFramesWithinRound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	machine := identity(t, 2)
	domain, head := sha256.Sum256([]byte("group-domain")), sha256.Sum256([]byte("group-head"))
	codec, err := peerwire.NewCodec("00000000000000000000000000000001", domain, head, 1)
	if err != nil {
		t.Fatal(err)
	}
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
	started := time.Now()
	deadline := uint64(started.Add(10 * time.Second).UnixMilli())
	for range 8 {
		client := identity(t, 1)
		conn := make([]byte, 16)
		_, _ = rand.Read(conn)
		var daemonSide, clientSide atomic.Pointer[Session]
		var lost atomic.Int32
		common := SessionConfig{Codec: codec, Connection: conn, Deadline: deadline, Allow: func() bool { return true }, LoopbackOnly: true, Ready: func(_ uint64, p *Path) { ready <- p }}
		serverCfg := common
		serverCfg.Self = machine
		serverCfg.Peer = client.Public()
		serverCfg.Pool = pool
		serverCfg.Send = func(out Outbound) bool { target := clientSide.Load(); return target != nil && target.Receive(out.Raw) }
		ds, err := NewSession(ctx, serverCfg, false)
		if err != nil {
			t.Fatal(err)
		}
		daemonSide.Store(ds)
		sessions = append(sessions, ds)
		clientCfg := common
		clientCfg.Self = client
		clientCfg.Peer = machine.Public()
		clientCfg.Pool = NewPool(4)
		clientCfg.Send = func(out Outbound) bool {
			if out.Critical && lost.Add(1) <= 2 {
				return true
			}
			target := daemonSide.Load()
			return target != nil && target.Receive(out.Raw)
		}
		cs, err := NewSession(ctx, clientCfg, true)
		if err != nil {
			t.Fatal(err)
		}
		clientSide.Store(cs)
		sessions = append(sessions, cs)
	}
	for i := 0; i < 32; i++ {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatalf("only %d of 32 SDK PCs completed the original round", i)
		}
	}
	elapsed := time.Since(started)
	if elapsed > 10*time.Second {
		t.Fatal("eight-peer handshake exceeded original deadline", elapsed)
	}
	if _, err = New(ctx, Config{Peer: "ninth-fixture", Owner: "round", Use: 1, Generation: 1, Pool: pool, Allow: func() bool { return true }, LoopbackOnly: true}); err == nil {
		t.Fatal("connected peer budget exceeded")
	}
	t.Logf("8 peers, 32 real SDK PCs, 2 lost critical frames per pair: %s", elapsed)
}

func TestPartialReadyRoundDoneAndSessionDoneWhenPathsClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	a, b := identity(t, 1), identity(t, 2)
	domain, head := sha256.Sum256([]byte("domain")), sha256.Sum256([]byte("head"))
	codec, err := peerwire.NewCodec("00000000000000000000000000000001", domain, head, 1)
	if err != nil {
		t.Fatal(err)
	}
	connection := make([]byte, 16)
	_, _ = rand.Read(connection)
	var offsetMs atomic.Int64
	base := time.Now()
	fakeNow := func() time.Time { return base.Add(time.Duration(offsetMs.Load()) * time.Millisecond) }
	deadline := uint64(base.Add(9 * time.Second).UnixMilli())

	var ar, br atomic.Pointer[Session]
	type readyEvent struct {
		which int
		use   uint64
		path  *Path
	}
	paths := make(chan readyEvent, 4)
	makeCfg := func(self peerwire.Identity, other peerwire.Public, which int) SessionConfig {
		return SessionConfig{
			Self: self, Peer: other.Member(), Codec: codec, Connection: connection,
			Deadline: deadline, Pool: NewPool(4), Allow: func() bool { return true },
			LoopbackOnly: true, Now: fakeNow,
			Ready: func(use uint64, p *Path) { paths <- readyEvent{which, use, p} },
			Send: func(out Outbound) bool {
				if packet, e := codec.OpenSent(out.Raw, self, other, uint64(fakeNow().UnixMilli())); e == nil && packet.Context.Use == 2 {
					return true
				}
				target := ar.Load()
				if which == 0 {
					target = br.Load()
				}
				return target != nil && target.Receive(out.Raw)
			},
		}
	}
	sa, err := NewSession(ctx, makeCfg(a, b.Public(), 0), true)
	if err != nil {
		t.Fatal(err)
	}
	ar.Store(sa)
	defer func() { sa.Close(); sa.Wait() }()
	sb, err := NewSession(ctx, makeCfg(b, a.Public(), 1), false)
	if err != nil {
		t.Fatal(err)
	}
	br.Store(sb)
	defer func() { sb.Close(); sb.Wait() }()

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

	offsetMs.Store(9500)
	select {
	case <-sa.RoundDone():
	case <-ctx.Done():
		t.Fatal("RoundDone did not close after partial round passed deadline")
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
