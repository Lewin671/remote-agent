package server

import (
	"context"
	"net/http"
	"net/netip"
	"testing"
	"time"
)

func TestPushEndpointRejectsInternalDestinations(t *testing.T) {
	for _, endpoint := range []string{"http://push.example/", "https://127.0.0.1/", "https://[::1]/", "https://[::ffff:127.0.0.1]/", "https://169.254.169.254/", "https://10.0.0.1/", "https://push.example:8443/", "https://user:password@push.example/"} {
		if validatePushEndpoint(endpoint) == nil {
			t.Fatalf("accepted unsafe endpoint %q", endpoint)
		}
	}
	for _, addr := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "169.254.169.254", "10.0.0.1", "100.64.0.1", "198.18.0.1", "64:ff9b::7f00:1", "2002:7f00:1::"} {
		if publicPushIP(netip.MustParseAddr(addr)) {
			t.Fatalf("allowed non-public address %q", addr)
		}
	}
	for _, addr := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicPushIP(netip.MustParseAddr(addr)) {
			t.Fatalf("rejected public address %q", addr)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, err := dialPublicPush(ctx, "tcp", "localhost:443"); err == nil {
		conn.Close()
		t.Fatal("resolved localhost was allowed")
	}
	if newPushHTTPClient().CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("push client follows redirects")
	}
	ts := newTestServer(t)
	if err := ts.srv.push.subscribe(ctx, "client", pushSubscription(t, "https://127.0.0.1/"), nil); err == nil {
		t.Fatal("stored private push endpoint")
	}
}
