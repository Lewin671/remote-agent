package stun

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"net"
	"net/netip"
	"testing"
	"time"
)

func request(fingerprint bool) []byte {
	n := 20
	if fingerprint {
		n = 28
	}
	b := make([]byte, n)
	binary.BigEndian.PutUint16(b, 1)
	binary.BigEndian.PutUint16(b[2:], uint16(n-20))
	binary.BigEndian.PutUint32(b[4:], cookie)
	for i := 8; i < 20; i++ {
		b[i] = byte(i)
	}
	if fingerprint {
		binary.BigEndian.PutUint16(b[20:], 0x8028)
		binary.BigEndian.PutUint16(b[22:], 4)
		binary.BigEndian.PutUint32(b[24:], crc32.ChecksumIEEE(b[:20])^0x5354554e)
	}
	return b
}
func TestMinimalBindingIPv4IPv6(t *testing.T) {
	for _, host := range []string{"192.0.2.1:4321", "[2001:db8::1]:4321"} {
		for _, fp := range []bool{false, true} {
			s := New(DefaultConfig())
			src := netip.MustParseAddrPort(host)
			q := request(fp)
			r := s.Reply(q, src, time.Now())
			want := 32
			if src.Addr().Is6() {
				want = 44
			}
			if fp {
				want += 8
			}
			if len(r) != want {
				t.Fatalf("response bytes=%d want %d", len(r), want)
			}
			if len(r) > 3*len(q) || binary.BigEndian.Uint16(r) != 0x101 || int(binary.BigEndian.Uint16(r[2:])) != len(r)-20 {
				t.Fatal("response budget or header")
			}
			if binary.BigEndian.Uint16(r[26:])^uint16(cookie>>16) != src.Port() {
				t.Fatal("port")
			}
			for i, b := range src.Addr().AsSlice() {
				if r[28+i]^q[4+i] != b {
					t.Fatal("address")
				}
			}
			if fp && binary.BigEndian.Uint32(r[len(r)-4:]) != crc32.ChecksumIEEE(r[:len(r)-8])^0x5354554e {
				t.Fatal("fingerprint")
			}
		}
	}
}
func TestMalformedAndReflectionExtensionsDrop(t *testing.T) {
	s := New(DefaultConfig())
	src := netip.MustParseAddrPort("192.0.2.1:4321")
	variants := [][]byte{nil, make([]byte, 577), request(false)[:19]}
	for _, at := range []int{0, 1, 2, 4} {
		q := request(false)
		q[at] ^= 1
		variants = append(variants, q)
	}
	q := request(true)
	q[27] ^= 1
	variants = append(variants, q)
	q = request(true)
	q[20] = 0
	q[21] = 3
	variants = append(variants, q)
	for _, q := range variants {
		if r := s.Reply(q, src, time.Now()); r != nil {
			t.Fatal("invalid/reflection request answered")
		}
	}
	if len(s.sources) != 0 {
		t.Fatal("invalid packets retained addresses")
	}
}
func TestSourceAndGlobalBudgetsAndExpiry(t *testing.T) {
	c := DefaultConfig()
	c.SourceBurst = 2
	c.SourcePacketsPerSecond = 1
	c.Burst = 3
	c.PacketsPerSecond = 1
	c.MaxSources = 2
	s := New(c)
	now := time.Now()
	a := netip.MustParseAddrPort("192.0.2.1:4321")
	b := netip.MustParseAddrPort("192.0.2.2:4321")
	q := request(false)
	if s.Reply(q, a, now) == nil || s.Reply(q, a, now) == nil || s.Reply(q, a, now) != nil {
		t.Fatal("source burst")
	}
	if s.Reply(q, b, now) == nil || s.Reply(q, b, now) != nil {
		t.Fatal("global burst")
	}
	if s.Reply(q, a, now.Add(time.Second)) == nil {
		t.Fatal("refill")
	}
	third := netip.MustParseAddrPort("192.0.2.3:4321")
	if s.Reply(q, third, now.Add(2*time.Second)) != nil {
		t.Fatal("address memory unbounded")
	}
	if s.Reply(q, third, now.Add(61*time.Second)) == nil || len(s.sources) != 1 {
		t.Fatal("addresses retained past short TTL")
	}
	c = DefaultConfig()
	c.BytesPerSecond = 32
	s = New(c)
	if s.Reply(q, a, now) == nil || s.Reply(q, b, now) != nil {
		t.Fatal("global byte cap")
	}
}
func TestLocalUDPAndCancellation(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(DefaultConfig())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, conn) }()
	client, err := net.DialUDP("udp4", nil, conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	if _, err = client.Write(request(true)); err != nil {
		t.Fatal(err)
	}
	var b [100]byte
	n, err := client.Read(b[:])
	if err != nil || n != 40 {
		t.Fatalf("UDP binding: %d %v", n, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("service did not stop")
	}
	if len(s.sources) != 0 {
		t.Fatal("shutdown retained addresses")
	}
}
func FuzzBindingRequest(f *testing.F) {
	f.Add(request(false))
	f.Add(request(true))
	f.Fuzz(func(t *testing.T, b []byte) {
		s := New(DefaultConfig())
		r := s.Reply(b, netip.MustParseAddrPort("192.0.2.1:4321"), time.Now())
		if len(r) > 52 || len(r) > 3*len(b) {
			t.Fatal("amplification")
		}
	})
}
