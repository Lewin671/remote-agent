package transport

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
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
func defaultRouteEntries() []defaultRoute {
	b, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, syscall.AF_UNSPEC)
	if err != nil {
		return nil
	}
	return defaultRouteEntriesFromNetlink(b)
}

func defaultRouteEntriesFromNetlink(b []byte) []defaultRoute {
	msgs, err := syscall.ParseNetlinkMessage(b)
	if err != nil {
		return nil
	}
	var routes []defaultRoute
	for _, m := range msgs {
		// rtmsg: [0]=family, [1]=dst_len, [2]=src_len, [4]=table, [7]=type.
		if m.Header.Type != syscall.RTM_NEWROUTE || len(m.Data) < 12 || m.Data[1] != 0 || m.Data[2] != 0 || m.Data[7] != syscall.RTN_UNICAST {
			continue
		}
		family := ""
		switch m.Data[0] {
		case syscall.AF_INET:
			family = "v4"
		case syscall.AF_INET6:
			family = "v6"
		default:
			continue
		}
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			continue
		}
		r := defaultRoute{
			Family: family,
			Table:  fmt.Sprintf("%d", m.Data[4]),
		}
		for _, a := range attrs {
			switch a.Attr.Type {
			case syscall.RTA_OIF:
				if len(a.Value) == 4 {
					r.IfIndex = int(binary.NativeEndian.Uint32(a.Value))
				}
			case syscall.RTA_GATEWAY:
				if ip := net.IP(a.Value); len(ip) > 0 {
					r.Gateway = ip.String()
				}
			case syscall.RTA_TABLE:
				if len(a.Value) == 4 {
					r.Table = fmt.Sprintf("%d", binary.NativeEndian.Uint32(a.Value))
				}
			}
		}
		if r.IfIndex > 0 {
			routes = append(routes, r)
		}
	}
	return routes
}
