package transport

import (
	"bytes"
	"encoding/binary"
	"net"
	"reflect"
	"syscall"
	"testing"

	"golang.org/x/net/route"
)

func TestDarwinRouteSnapshotAcceptsUnalignedRecords(t *testing.T) {
	message := func(index int, destination [4]byte, gateway byte) []byte {
		t.Helper()
		m := &route.RouteMessage{Type: syscall.RTM_GET, Flags: syscall.RTF_UP | syscall.RTF_GATEWAY, Index: index,
			Addrs: []route.Addr{syscall.RTAX_DST: &route.Inet4Addr{IP: destination}, syscall.RTAX_GATEWAY: &route.Inet4Addr{IP: [4]byte{192, 0, 2, gateway}}}}
		b, err := m.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		// The kernel uses four-byte alignment, including when the next route
		// starts at an offset that is not aligned for a Go pointer.
		length := syscall.SizeofRtMsghdr + 2*syscall.SizeofSockaddrInet4
		b = b[:length]
		binary.NativeEndian.PutUint16(b[:2], uint16(length))
		return b
	}
	raw := bytes.Join([][]byte{message(7, [4]byte{}, 1), message(9, [4]byte{}, 2), message(11, [4]byte{192, 0, 2, 7}, 3)}, nil)
	want := []string{"7/&route.Inet4Addr{IP:[4]uint8{0xc0, 0x0, 0x2, 0x1}}", "9/&route.Inet4Addr{IP:[4]uint8{0xc0, 0x0, 0x2, 0x2}}"}
	for offset := 0; offset < 8; offset++ {
		buffer := make([]byte, len(raw)+offset)
		copy(buffer[offset:], raw)
		if got := routesFromRIB(buffer[offset:]); !reflect.DeepEqual(got, want) {
			t.Fatalf("offset %d: default route identities changed: got %v", offset, got)
		}
	}
	if got := routesFromRIB(raw[:len(raw)-1]); got != nil {
		t.Fatal("truncated route table accepted")
	}
}

func TestDarwinDefaultRouteEntriesSkipsIfscope(t *testing.T) {
	marshal := func(flags, index int, dst, gw, mask route.Addr) []byte {
		t.Helper()
		m := &route.RouteMessage{
			Version: syscall.RTM_VERSION,
			Type:    syscall.RTM_GET,
			Flags:   flags,
			Index:   index,
			Addrs: []route.Addr{
				syscall.RTAX_DST:     dst,
				syscall.RTAX_GATEWAY: gw,
				syscall.RTAX_NETMASK: mask,
			},
		}
		b, err := m.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	scopedV6Before := marshal(
		syscall.RTF_UP|syscall.RTF_GATEWAY|darwinRTFIfscope,
		15,
		&route.Inet6Addr{},
		&route.Inet6Addr{IP: [16]byte{0xfe, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, ZoneID: 15},
		&route.Inet6Addr{},
	)
	scopedV4Before := marshal(
		syscall.RTF_UP|syscall.RTF_GATEWAY|darwinRTFIfscope,
		15,
		&route.Inet4Addr{},
		&route.Inet4Addr{IP: [4]byte{10, 8, 0, 1}},
		&route.Inet4Addr{},
	)
	primaryV4 := marshal(
		syscall.RTF_UP|syscall.RTF_GATEWAY,
		4,
		&route.Inet4Addr{},
		&route.Inet4Addr{IP: [4]byte{192, 0, 2, 1}},
		&route.Inet4Addr{},
	)
	primaryV6 := marshal(
		syscall.RTF_UP|syscall.RTF_GATEWAY,
		4,
		&route.Inet6Addr{},
		&route.Inet6Addr{IP: [16]byte{0xfe, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, ZoneID: 4},
		&route.Inet6Addr{},
	)
	scopedV6After := marshal(
		syscall.RTF_UP|syscall.RTF_GATEWAY|darwinRTFIfscope,
		16,
		&route.Inet6Addr{},
		&route.Inet6Addr{IP: [16]byte{0xfd, 0xf4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, ZoneID: 16},
		&route.Inet6Addr{},
	)
	duplicateV4 := marshal(
		syscall.RTF_UP|syscall.RTF_GATEWAY,
		5,
		&route.Inet4Addr{},
		&route.Inet4Addr{IP: [4]byte{198, 51, 100, 1}},
		&route.Inet4Addr{},
	)

	ribBase := bytes.Join([][]byte{primaryV4, primaryV6}, nil)
	ribWithUtun := bytes.Join([][]byte{scopedV6Before, scopedV4Before, primaryV4, primaryV6, duplicateV4, scopedV6After}, nil)

	want := []defaultRoute{
		{IfIndex: 4, Family: "v4", Gateway: "192.0.2.1"},
		{IfIndex: 4, Family: "v6", Gateway: "fe80::1%4"},
	}
	if got := defaultRouteEntriesFromRIB(ribWithUtun); !reflect.DeepEqual(got, want) {
		t.Fatalf("got routes %+v, want %+v", got, want)
	}

	en0 := net.Interface{Index: 4, Name: "en0", Flags: net.FlagUp | net.FlagMulticast}
	utun5 := net.Interface{Index: 15, Name: "utun5", Flags: net.FlagUp | net.FlagPointToPoint}
	utun6 := net.Interface{Index: 16, Name: "utun6", Flags: net.FlagUp | net.FlagPointToPoint}
	addrs := map[int][]net.Addr{
		4:  {&net.IPNet{IP: net.ParseIP("192.0.2.10"), Mask: net.CIDRMask(24, 32)}},
		15: {&net.IPNet{IP: net.ParseIP("10.8.0.2"), Mask: net.CIDRMask(24, 32)}},
		16: {&net.IPNet{IP: net.ParseIP("fdf4::2"), Mask: net.CIDRMask(64, 128)}},
	}
	lookup := func(i net.Interface) ([]net.Addr, error) { return addrs[i.Index], nil }
	base := fingerprintSnapshot(networkSnapshot{
		interfaces: []net.Interface{en0},
		addrs:      lookup,
		routes:     defaultRouteEntriesFromRIB(ribBase),
	})
	withUtun := fingerprintSnapshot(networkSnapshot{
		interfaces: []net.Interface{en0, utun5, utun6},
		addrs:      lookup,
		routes:     defaultRouteEntriesFromRIB(ribWithUtun),
	})
	if withUtun != base {
		t.Fatal("interface-scoped utun default routes changed network fingerprint")
	}
}
