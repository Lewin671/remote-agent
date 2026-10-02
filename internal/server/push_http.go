package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

var errPushEndpoint = errors.New("push endpoint must use HTTPS on a public host")

func validatePushEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return errPushEndpoint
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicPushIP(ip) {
		return errPushEndpoint
	}
	return nil
}

// Exclude special-use ranges that IsGlobalUnicast intentionally includes.
var nonPublicPushRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

// Current IPv6 Internet unicast allocations are under 2000::/3. Go's
// IsGlobalUnicast also accepts deprecated site-local and non-public
// special-use ranges (e.g. fec0::/10, 100::/64 and 5f00::/16).
var globalPushIPv6 = netip.MustParsePrefix("2000::/3")

func publicPushIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Zone() != "" || (ip.Is6() && !globalPushIPv6.Contains(ip)) {
		return false
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range nonPublicPushRanges {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// Resolve and dial the validated address directly. Checking a DNS name only
// when subscribing would allow later DNS rebinding to an internal service.
func dialPublicPush(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, errPushEndpoint
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, errPushEndpoint
	}
	if len(ips) == 0 {
		return nil, errPushEndpoint
	}
	for _, ip := range ips {
		if !publicPushIP(ip) {
			return nil, errPushEndpoint
		}
	}
	var last error
	for _, ip := range ips {
		conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

func newPushHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // environment proxies must not bypass address checks
	transport.DialContext = dialPublicPush
	return &http.Client{Transport: transport, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
