package transport

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	networkHintDebounce    = 60 * time.Millisecond
	networkHintMaxDebounce = 150 * time.Millisecond
)

// NetworkChanges retains only the newest observation. Addresses and routes
// stay in memory; neither the fingerprint nor its source is logged. Kernel
// events accelerate the bounded fallback scan, including same-interface DHCP.
func NetworkChanges(ctx context.Context) <-chan uint64 {
	tick := time.NewTicker(time.Second)
	return observeNetworkChangesWithDebounce(ctx, routeHints(ctx), tick.C, networkHintDebounce, networkFingerprint, tick.Stop)
}

func observeNetworkChanges(ctx context.Context, hints <-chan struct{}, ticks <-chan time.Time, scan func() [32]byte, stop func()) <-chan uint64 {
	return observeNetworkChangesWithDebounce(ctx, hints, ticks, 0, scan, stop)
}

func observeNetworkChangesWithDebounce(ctx context.Context, hints <-chan struct{}, ticks <-chan time.Time, debounce time.Duration, scan func() [32]byte, stop func()) <-chan uint64 {
	out := make(chan uint64, 1)
	go func() {
		defer close(out)
		defer stop()
		var debounceTimer *time.Timer
		var debounceC <-chan time.Time
		var burstStart time.Time
		stopDebounce := func() {
			if debounceTimer != nil {
				debounceTimer.Stop()
				debounceTimer = nil
				debounceC = nil
			}
		}
		defer stopDebounce()

		previous := scan()
		var generation uint64
		emitIfChanged := func() bool {
			current := scan()
			if current == previous {
				return true
			}
			previous = current
			generation++
			select {
			case <-out:
			default:
			}
			select {
			case out <- generation:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-hints:
				if debounce <= 0 {
					if !emitIfChanged() {
						return
					}
					continue
				}
				now := time.Now()
				if debounceTimer == nil {
					burstStart = now
					debounceTimer = time.NewTimer(debounce)
					debounceC = debounceTimer.C
					continue
				}
				maxWait := max(debounce*2, networkHintMaxDebounce)
				rem := burstStart.Add(maxWait).Sub(now)
				if rem <= 0 {
					stopDebounce()
					if !emitIfChanged() {
						return
					}
					continue
				}
				if !debounceTimer.Stop() {
					select {
					case <-debounceTimer.C:
					default:
					}
				}
				debounceTimer.Reset(min(debounce, rem))
			case <-debounceC:
				debounceTimer = nil
				debounceC = nil
				if !emitIfChanged() {
					return
				}
			case <-ticks:
				stopDebounce()
				if !emitIfChanged() {
					return
				}
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

func fingerprintAddr(a net.Addr) string {
	if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP != nil && ipNet.IP.To4() == nil && ipNet.IP.IsGlobalUnicast() {
		ones, bits := ipNet.Mask.Size()
		if bits == 128 && ones > 0 {
			return ipNet.IP.Mask(ipNet.Mask).String() + "/" + strconv.Itoa(ones)
		}
	}
	return a.String()
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
				seen := make(map[string]struct{}, len(addrs))
				for _, a := range addrs {
					s := fingerprintAddr(a)
					if _, dup := seen[s]; !dup {
						seen[s] = struct{}{}
						addrsStr = append(addrsStr, s)
					}
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
