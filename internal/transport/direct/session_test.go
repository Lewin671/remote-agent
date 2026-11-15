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
