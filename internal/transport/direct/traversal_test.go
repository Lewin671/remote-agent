package direct

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	a, b := identity(t, 1), identity(t, 2)
	codec := testCodec(t)
	clock := newTestClock()
	connection := make([]byte, 16)
	_, _ = rand.Read(connection)
	slots := [2]*peerSlot{newPeerSlot(), newPeerSlot()}
	ready := make(chan uint64, 4)
	deadline := clock.Ms() + 10000
	// No STUN server and no loopback filter: host candidates only.
	cfg := func(self *Identity, other peerwire.Public, which int) SessionConfig {
		return SessionConfig{Self: self, Peer: other.Member(), Codec: codec, Connection: connection, Deadline: deadline, Pool: NewPool(8), Allow: func() bool { return true }, Now: clock.Now,
			Ready: func(use uint64, _ *Path) { ready <- use },
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
	for n := 0; n < 4; n++ {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatalf("a host-only pair was never handed over: %d of 4 paths ready", n)
		}
	}
}
