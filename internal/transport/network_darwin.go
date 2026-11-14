package transport

import (
	"context"
	"fmt"
	"os"
	"syscall"
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
	b, err := syscall.RouteRIB(syscall.NET_RT_DUMP, 0)
	if err != nil {
		return nil
	}
	msgs, err := syscall.ParseRoutingMessage(b)
	if err != nil {
		return nil
	}
	var routes []string
	for _, msg := range msgs {
		m, ok := msg.(*syscall.RouteMessage)
		if !ok {
			continue
		}
		addrs, err := syscall.ParseRoutingSockaddr(m)
		if err != nil {
			continue
		}
		if len(addrs) <= syscall.RTAX_GATEWAY {
			continue
		}
		zero := false
		switch a := addrs[syscall.RTAX_DST].(type) {
		case *syscall.SockaddrInet4:
			zero = a.Addr == [4]byte{}
		case *syscall.SockaddrInet6:
			zero = a.Addr == [16]byte{}
		}
		if zero {
			routes = append(routes, fmt.Sprintf("%d/%#v", m.Header.Index, addrs[syscall.RTAX_GATEWAY]))
		}
	}
	return routes
}
