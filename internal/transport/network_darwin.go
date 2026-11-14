package transport

import (
	"context"
	"fmt"
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
func defaultRoutes() []string {
	b, err := route.FetchRIB(syscall.AF_UNSPEC, route.RIBType(syscall.NET_RT_DUMP), 0)
	if err != nil {
		return nil
	}
	return routesFromRIB(b)
}

// The legacy syscall decoder casts message offsets to aligned Go pointers.
// Darwin route records can begin on a four-byte boundary; decode their bytes.
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
