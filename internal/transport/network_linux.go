package transport

import (
	"context"
	"fmt"
	"os"
	"syscall"
)

func routeHints(ctx context.Context) <-chan struct{} {
	out := make(chan struct{}, 1)
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW, syscall.NETLINK_ROUTE)
	if err != nil {
		return out
	}
	// Link, IPv4/IPv6 address and route multicast groups.
	if syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK, Groups: 1 | 0x10 | 0x40 | 0x100 | 0x400}) != nil {
		syscall.Close(fd)
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
	b, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, syscall.AF_UNSPEC)
	if err != nil {
		return nil
	}
	msgs, err := syscall.ParseNetlinkMessage(b)
	if err != nil {
		return nil
	}
	var routes []string
	for _, m := range msgs {
		if m.Header.Type != syscall.RTM_NEWROUTE || len(m.Data) < 12 || m.Data[1] != 0 {
			continue
		}
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			continue
		}
		for _, a := range attrs {
			if a.Attr.Type == syscall.RTA_GATEWAY || a.Attr.Type == syscall.RTA_OIF || a.Attr.Type == syscall.RTA_TABLE {
				routes = append(routes, fmt.Sprintf("%d/%d/%x", m.Data[0], a.Attr.Type, a.Value))
			}
		}
	}
	return routes
}
