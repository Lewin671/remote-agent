package direct

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func freePort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// Both ends take their host candidates on their fixed port, and the data
// channel works through the shared sockets.
func TestFixedPortHostCandidatesConnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ports := []int{freePort(t), freePort(t)}
	muxes := []*PortMux{NewPortMux(ports[0], true), NewPortMux(ports[1], true)}
	defer muxes[0].Close()
	defer muxes[1].Close()
	chans := []chan *webrtc.ICECandidateInit{make(chan *webrtc.ICECandidateInit, 65), make(chan *webrtc.ICECandidateInit, 65)}
	paths := make([]*Path, 2)
	for i := range paths {
		ch := chans[i]
		p, err := New(ctx, Config{Use: 1, Owner: "test", Peer: []string{"peer-b", "peer-a"}[i], Generation: 1, Pool: NewPool(8), Allow: func() bool { return true }, LoopbackOnly: true, Port: muxes[i], Candidate: func(c *webrtc.ICECandidateInit) { ch <- c }})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { p.Close(); p.Wait() }()
		paths[i] = p
	}
	offer, err := paths[0].Description(true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = paths[1].Remote(offer, true); err != nil {
		t.Fatal(err)
	}
	answer, err := paths[1].Description(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = paths[0].Remote(answer, false); err != nil {
		t.Fatal(err)
	}
	for i := range paths {
		var hosts int
		for c := range chans[i] {
			if c == nil {
				break
			}
			if strings.Contains(c.Candidate, " typ host") {
				hosts++
				if f := strings.Fields(c.Candidate); f[5] != strconv.Itoa(ports[i]) {
					t.Fatalf("host candidate %d is not on the fixed port %d", i, ports[i])
				}
			}
			_ = paths[1-i].AddCandidate(*c)
		}
		if hosts == 0 {
			t.Fatalf("end %d gathered no host candidate", i)
		}
	}
	paths[0].Confirm()
	paths[1].Confirm()
	raw := bytes.Repeat([]byte{7}, 4096)
	done := make(chan error, 1)
	go func() { done <- paths[0].Write(ctx, raw) }()
	got, err := paths[1].Read(ctx)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("read %d bytes, %v", len(got), err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

// A new network: the addresses that went away release the port, new ones bind it.
func TestFixedPortFollowsAddressChanges(t *testing.T) {
	port := freePort(t)
	m := NewPortMux(port, true)
	defer m.Close()
	lo := netip.MustParseAddr("127.0.0.1")
	m.addrs = func() []netip.Addr { return []netip.Addr{lo} }
	if m.current() == nil || len(m.muxes) != 1 {
		t.Fatal("did not bind the loopback address")
	}
	m.addrs = func() []netip.Addr { return nil }
	if m.current() != nil || len(m.muxes) != 0 {
		t.Fatal("kept a socket for an address that went away")
	}
	c, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(lo, uint16(port))))
	if err != nil {
		t.Fatalf("port not released: %v", err)
	}
	// The port taken by someone else: no mux, the path falls back to ephemeral ports.
	m.addrs = func() []netip.Addr { return []netip.Addr{lo} }
	if m.current() != nil {
		t.Fatal("claimed a port another socket holds")
	}
	c.Close()
	if m.current() == nil {
		t.Fatal("did not take the port once it was free")
	}
	m.Close()
	if m.current() != nil {
		t.Fatal("bound again after Close")
	}
}
