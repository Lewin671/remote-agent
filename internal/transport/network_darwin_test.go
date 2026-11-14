package transport

import (
	"bytes"
	"encoding/binary"
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
