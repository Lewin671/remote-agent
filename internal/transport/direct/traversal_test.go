package direct

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

// A pair that only ever gathers host candidates, as a computer with a public
// address and no NAT does, and as every pair does when STUN cannot be reached:
// no server-reflexive candidate exists, and the session must still hand the
// path over. A connected transport plus the peer's confirmation is the proof.
func TestHostOnlyPairIsHandedOverWithoutAnyServerReflexiveCandidate(t *testing.T) {
	if len(localAddrs(false)) == 0 {
		t.Skip("no routable local address on this machine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	a, b := identity(t, 1), identity(t, 2)
	domain, head := sha256.Sum256([]byte("domain")), sha256.Sum256([]byte("head"))
	codec, err := peerwire.NewCodec("00000000000000000000000000000001", domain, head, 1, protocol.Version)
	if err != nil {
		t.Fatal(err)
	}
	connection := make([]byte, 16)
	_, _ = rand.Read(connection)
	var ar, br atomic.Pointer[Session]
	ready := make(chan uint64, 4)
	deadline := uint64(time.Now().Add(10 * time.Second).UnixMilli())
	// No STUN server and no loopback filter: host candidates only.
	cfg := func(self *Identity, other peerwire.Public, which int) SessionConfig {
		return SessionConfig{Self: self, Peer: other.Member(), Codec: codec, Connection: connection, Deadline: deadline, Pool: NewPool(8), Allow: func() bool { return true },
			Ready: func(use uint64, _ *Path) { ready <- use },
			Send: func(out Outbound) bool {
				target := ar.Load()
				if which == 0 {
					target = br.Load()
				}
				return target != nil && target.Receive(out.Raw)
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
	for n := 0; n < 4; n++ {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatalf("a host-only pair was never handed over: %d of 4 paths ready", n)
		}
	}
}
