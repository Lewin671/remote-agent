package transport

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

// NetworkChanges retains only the newest observation. Addresses and routes
// stay in memory; neither the fingerprint nor its source is logged. Kernel
// events accelerate the bounded fallback scan, including same-interface DHCP.
func NetworkChanges(ctx context.Context) <-chan uint64 {
	tick := time.NewTicker(time.Second)
	return observeNetworkChanges(ctx, routeHints(ctx), tick.C, networkFingerprint, tick.Stop)
}

func observeNetworkChanges(ctx context.Context, hints <-chan struct{}, ticks <-chan time.Time, scan func() [32]byte, stop func()) <-chan uint64 {
	out := make(chan uint64, 1)
	go func() {
		defer close(out)
		defer stop()
		previous := scan()
		var generation uint64
		for {
			select {
			case <-ctx.Done():
				return
			case <-hints:
			case <-ticks:
			}
			current := scan()
			if current == previous {
				continue
			}
			previous = current
			generation++
			select {
			case <-out:
			default:
			}
			select {
			case out <- generation:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
type defaultRoute struct {
	IfIndex int
	Family  string
	Gateway string
	Table   string
}

type networkSnapshot struct {
	interfaces []net.Interface
	addrs      func(net.Interface) ([]net.Addr, error)
	routes     []defaultRoute
	dnsLines   []string
}

func dnsFingerprintLines(resolvPath string) []string {
	data, err := os.ReadFile(resolvPath)
	if err != nil {
		return nil
	}
	var lines []string
	for _, raw := range strings.Split(string(data), "\n") {
		if i := strings.IndexAny(raw, "#;"); i >= 0 {
			raw = raw[:i]
		}
		fields := strings.Fields(raw)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "nameserver", "search", "domain":
			lines = append(lines, "dns:"+strings.Join(fields, " "))
		}
	}
	return lines
}

func fingerprintSnapshot(s networkSnapshot) [32]byte {
	defaultIfs := make(map[int]bool, len(s.routes))
	var parts []string
	for _, r := range s.routes {
		if r.IfIndex > 0 {
			defaultIfs[r.IfIndex] = true
		}
		parts = append(parts, fmt.Sprintf("route:%s:%d:%s:%s", r.Family, r.IfIndex, r.Gateway, r.Table))
	}
	for _, iface := range s.interfaces {
		if !defaultIfs[iface.Index] || iface.Flags&net.FlagUp == 0 {
			continue
		}
		var addrsStr []string
		if s.addrs != nil {
			if addrs, err := s.addrs(iface); err == nil {
				for _, a := range addrs {
					addrsStr = append(addrsStr, a.String())
				}
			}
		}
		sort.Strings(addrsStr)
		parts = append(parts, fmt.Sprintf("if:%d:%s:%s:%s", iface.Index, iface.Name, iface.Flags.String(), strings.Join(addrsStr, ",")))
	}
	parts = append(parts, s.dnsLines...)
	sort.Strings(parts)
	return sha256.Sum256([]byte(strings.Join(parts, "\n")))
}

// RouteAvailable reports whether a non-loopback local IP still has an UP
// default-route interface matching its address family. Loopback and nil IPs
// are not treated as unavailable by host default-route checks.
func RouteAvailable(localIP net.IP) bool {
	if localIP == nil || localIP.IsLoopback() {
		return true
	}
	ifaces, _ := net.Interfaces()
	return routeAvailableSnapshot(networkSnapshot{
		interfaces: ifaces,
		addrs:      func(i net.Interface) ([]net.Addr, error) { return i.Addrs() },
		routes:     defaultRouteEntries(),
	}, localIP)
}

func routeAvailableSnapshot(s networkSnapshot, localIP net.IP) bool {
	if localIP == nil || localIP.IsLoopback() {
		return true
	}
	wantFamily := "v6"
	if localIP.To4() != nil {
		wantFamily = "v4"
	}
	routeIfs := make(map[int]bool)
	for _, r := range s.routes {
		if r.Family == wantFamily && r.IfIndex > 0 {
			routeIfs[r.IfIndex] = true
		}
	}
	for _, iface := range s.interfaces {
		if iface.Flags&net.FlagUp == 0 || !routeIfs[iface.Index] || s.addrs == nil {
			continue
		}
		addrs, err := s.addrs(iface)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && ip.Equal(localIP) {
				return true
			}
		}
	}
	return false
}

func networkFingerprint() [32]byte {
	interfaces, err := net.Interfaces()
	if err != nil {
		interfaces = nil
	}
	return fingerprintSnapshot(networkSnapshot{
		interfaces: interfaces,
		addrs:      func(i net.Interface) ([]net.Addr, error) { return i.Addrs() },
		routes:     defaultRouteEntries(),
		dnsLines:   dnsFingerprintLines("/etc/resolv.conf"),
	})
}
