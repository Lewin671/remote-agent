package direct

import (
	"context"
	"crypto/rand"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/shared/peerwire"
	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
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

// firewalledConn stands for a peer behind a stateful firewall: packets from an
// address are dropped until the peer has sent to it.
type firewalledConn struct {
	*net.UDPConn
	mu      sync.Mutex
	opened  map[string]bool
	drops   int
	dropped chan int
}

func (c *firewalledConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.UDPConn.ReadFrom(b)
		if err != nil {
			return n, addr, err
		}
		c.mu.Lock()
		open := c.opened[addr.String()]
		if !open {
			c.drops++
			select {
			case c.dropped <- c.drops:
			default:
			}
		}
		c.mu.Unlock()
		if open {
			return n, addr, nil
		}
	}
}

func (c *firewalledConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	c.mu.Lock()
	c.opened[addr.String()] = true
	c.mu.Unlock()
	return c.UDPConn.WriteTo(b, addr)
}

// The computer offers, so its Pion is the controlling side and the only one
// that can nominate. Its first checks are dropped by the firewall in front of
// the peer, which starts checking only after Pion's default budget of checks on
// the pair is spent. Pion never re-checks a pair it has given up, so with that
// budget the peer's checks succeed, no nomination follows, and both sides sit
// in checking until the round ends (seen with a phone over IPv6).
func TestComputerNominatesPeerWhoseFirewallOpensLate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	candidates := make(chan *webrtc.ICECandidateInit, 65)
	computer, err := New(ctx, Config{Use: 1, Owner: "test", Peer: "phone", Generation: 1, Pool: NewPool(8), Allow: func() bool { return true }, LoopbackOnly: true, Candidate: func(c *webrtc.ICECandidateInit) { candidates <- c }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { computer.Close(); computer.Wait() })

	raw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	firewall := &firewalledConn{UDPConn: raw, opened: map[string]bool{}, dropped: make(chan int, 1)}
	mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: firewall})
	t.Cleanup(func() { _ = mux.Close() })
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetIncludeLoopbackCandidate(true)
	se.SetICEUDPMux(mux)
	phone, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	connected := make(chan struct{})
	var once sync.Once
	phone.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		if s == webrtc.ICEConnectionStateConnected {
			once.Do(func() { close(connected) })
		}
	})
	phone.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = computer.AddCandidate(c.ToJSON())
		}
	})

	offer, err := computer.Description(true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = phone.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: string(offer)}); err != nil {
		t.Fatal(err)
	}
	answer, err := phone.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = computer.Remote([]byte(answer.SDP), false); err != nil {
		t.Fatal(err)
	}
	if err = phone.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}

	// Pion's default budget is 7 checks after the first; wait until all 8 have
	// been dropped before the phone learns where to check.
	for drops := 0; drops < 8; {
		select {
		case drops = <-firewall.dropped:
		case <-ctx.Done():
			t.Fatal("the computer never checked the phone")
		}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case c := <-candidates:
				if c == nil {
					return
				}
				_ = phone.AddICECandidate(*c)
			}
		}
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("the computer never nominated a pair after the phone's checks got through")
	}
}
