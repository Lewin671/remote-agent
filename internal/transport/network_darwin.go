package transport

import (
	"context"
	"fmt"
	"net"
	"os"
	"syscall"

	"golang.org/x/net/route"
)

func routeHints(ctx context.Context) <-chan struct{} {
	out := make(chan struct{}, 1)
	fd, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, syscall.AF_UNSPEC)
	if err != nil {
		return out
	}
	_ = syscall.SetNonblock(fd, true)
	f := os.NewFile(uintptr(fd), "route-events")
	go func() {
		defer f.Close()
		stop := context.AfterFunc(ctx, func() { _ = f.Close() })
		defer stop()
		buf := make([]byte, 64<<10)
		for {
			if _, err := f.Read(buf); err != nil {
				return
			}
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out
}
func defaultRouteEntries() []defaultRoute {
	b, err := route.FetchRIB(syscall.AF_UNSPEC, route.RIBType(syscall.NET_RT_DUMP), 0)
	if err != nil {
		return nil
	}
	return defaultRouteEntriesFromRIB(b)
}

// darwinRTFIfscope mirrors RTF_IFSCOPE from Darwin <net/route.h>. Per-interface
// scoped default routes (e.g., utun VPN/Private Relay interfaces) do not change
// the host primary default route and must not trigger network-change churn.
const darwinRTFIfscope = 0x1000000

// The legacy syscall decoder casts message offsets to aligned Go pointers.
// Darwin route records can begin on a four-byte boundary; decode their bytes.
func defaultRouteEntriesFromRIB(b []byte) []defaultRoute {
	msgs, err := route.ParseRIB(route.RIBType(syscall.NET_RT_DUMP), b)
	if err != nil {
		return nil
	}
	seen := make(map[string]bool, 2)
	var routes []defaultRoute
	for _, msg := range msgs {
		m, ok := msg.(*route.RouteMessage)
		if !ok || m.Flags&syscall.RTF_UP == 0 || m.Flags&darwinRTFIfscope != 0 {
			continue
		}
		addrs := m.Addrs
		if len(addrs) <= syscall.RTAX_GATEWAY {
			continue
		}
		if len(addrs) > syscall.RTAX_NETMASK && addrs[syscall.RTAX_NETMASK] != nil {
			switch mask := addrs[syscall.RTAX_NETMASK].(type) {
			case *route.Inet4Addr:
				if mask.IP != [4]byte{} {
					continue
				}
			case *route.Inet6Addr:
				if mask.IP != [16]byte{} {
					continue
				}
			}
		}
		family := ""
		switch a := addrs[syscall.RTAX_DST].(type) {
		case *route.Inet4Addr:
			if a.IP == [4]byte{} {
				family = "v4"
			}
		case *route.Inet6Addr:
			if a.IP == [16]byte{} {
				family = "v6"
			}
		}
		if family == "" || seen[family] {
			continue
		}
		seen[family] = true
		routes = append(routes, defaultRoute{
			IfIndex: m.Index,
			Family:  family,
			Gateway: formatDarwinAddr(addrs[syscall.RTAX_GATEWAY]),
		})
	}
	return routes
}

func formatDarwinAddr(a route.Addr) string {
	switch v := a.(type) {
	case *route.Inet4Addr:
		return net.IP(v.IP[:]).String()
	case *route.Inet6Addr:
		if v.ZoneID != 0 {
			return fmt.Sprintf("%s%%%d", net.IP(v.IP[:]).String(), v.ZoneID)
		}
		return net.IP(v.IP[:]).String()
	case *route.LinkAddr:
		return fmt.Sprintf("link:%d:%s", v.Index, v.Name)
	default:
		return fmt.Sprintf("%#v", a)
	}
}

func routesFromRIB(b []byte) []string {
	msgs, err := route.ParseRIB(route.RIBType(syscall.NET_RT_DUMP), b)
	if err != nil {
		return nil
	}
	var routes []string
	for _, msg := range msgs {
		m, ok := msg.(*route.RouteMessage)
		if !ok {
			continue
		}
		addrs := m.Addrs
		if len(addrs) <= syscall.RTAX_GATEWAY {
			continue
		}
		zero := false
		switch a := addrs[syscall.RTAX_DST].(type) {
		case *route.Inet4Addr:
			zero = a.IP == [4]byte{}
		case *route.Inet6Addr:
			zero = a.IP == [16]byte{}
		}
		if zero {
			routes = append(routes, fmt.Sprintf("%d/%#v", m.Index, addrs[syscall.RTAX_GATEWAY]))
		}
	}
	return routes
}
