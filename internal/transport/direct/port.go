package direct

import (
	"net"
	"net/netip"
	"sync"

	"github.com/pion/ice/v4"
)

// DefaultPort is the one UDP port the daemon's direct paths take host
// candidates on, so a firewall in front of the computer can let in exactly
// this port. Checks towards the computer then reach it even before it has sent
// anything to the other side, which an IPv6 firewall that keeps state would
// otherwise drop (docs/transport.md §2).
const DefaultPort = 41478

// PortMux shares one UDP port among all direct paths: one socket per local
// address, bound to that address so replies leave from the address the check
// was sent to. Addresses are looked up again for every new path, so a changed
// network takes the new ones and drops the ones that went away. Addresses whose
// port is taken get no host candidate; the paths still gather server-reflexive
// ones on ephemeral ports.
type PortMux struct {
	port     int
	loopback bool
	mu       sync.Mutex
	closed   bool
	muxes    map[netip.Addr]*ice.UDPMuxDefault
	addrs    func() []netip.Addr
}

// NewPortMux shares port among paths; loopback is for isolated tests only and
// takes loopback addresses instead of the real ones.
func NewPortMux(port int, loopback bool) *PortMux {
	m := &PortMux{port: port, loopback: loopback, muxes: map[netip.Addr]*ice.UDPMuxDefault{}}
	m.addrs = func() []netip.Addr { return localAddrs(loopback) }
	return m
}

func localAddrs(loopback bool) []netip.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || (iface.Flags&net.FlagLoopback != 0) != loopback {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			prefix, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			ip := prefix.Addr().Unmap()
			if ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsLoopback() != loopback {
				continue
			}
			out = append(out, ip)
		}
	}
	return out
}

// current returns a mux over this moment's addresses, or nil when none could
// be bound and the path should use ephemeral ports instead.
func (m *PortMux) current() ice.UDPMux {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	now := map[netip.Addr]bool{}
	for _, ip := range m.addrs() {
		now[ip] = true
		if m.muxes[ip] != nil {
			continue
		}
		conn, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(m.port))))
		if err != nil {
			continue
		}
		m.muxes[ip] = ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: conn, Logger: silentLogger{}.NewLogger("mux")})
	}
	list := make([]ice.UDPMux, 0, len(m.muxes))
	for ip, mux := range m.muxes {
		if !now[ip] {
			_ = mux.Close()
			delete(m.muxes, ip)
			continue
		}
		list = append(list, mux)
	}
	if len(list) == 0 {
		return nil
	}
	// The wrapper only routes by address; the sockets stay owned here.
	return ice.NewMultiUDPMuxDefault(list...)
}

// Close releases the port; paths made later use ephemeral ports.
func (m *PortMux) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for ip, mux := range m.muxes {
		_ = mux.Close()
		delete(m.muxes, ip)
	}
}
