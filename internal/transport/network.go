package transport

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// NetworkChanges retains only the newest observation. Addresses and routes
// stay in memory; neither the fingerprint nor its source is logged. Kernel
// events accelerate the bounded fallback scan, including same-interface DHCP.
func NetworkChanges(ctx context.Context) <-chan uint64 {
	out := make(chan uint64, 1)
	go func() {
		defer close(out)
		hints := routeHints(ctx)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		previous := networkFingerprint()
		var generation uint64
		for {
			select {
			case <-ctx.Done():
				return
			case <-hints:
			case <-tick.C:
			}
			current := networkFingerprint()
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
func networkFingerprint() [32]byte {
	var parts []string
	interfaces, err := net.Interfaces()
	if err == nil {
		for _, i := range interfaces {
			if i.Flags&net.FlagLoopback != 0 {
				continue
			}
			parts = append(parts, fmt.Sprint(i.Index, "/", i.Flags))
			addresses, _ := i.Addrs()
			for _, a := range addresses {
				parts = append(parts, fmt.Sprint(i.Index, "/", a.String()))
			}
		}
	}
	parts = append(parts, defaultRoutes()...)
	sort.Strings(parts)
	return sha256.Sum256([]byte(strings.Join(parts, "\n")))
}
