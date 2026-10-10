package server

import (
	"context"
	"net/http"
	"net/netip"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestPushEndpointRejectsInternalDestinations(t *testing.T) {
	for _, endpoint := range []string{"http://push.example/", "https://127.0.0.1/", "https://[::1]/", "https://[::ffff:127.0.0.1]/", "https://169.254.169.254/", "https://10.0.0.1/", "https://push.example:8443/", "https://user:password@push.example/"} {
		if validatePushEndpoint(endpoint) == nil {
			t.Fatalf("accepted unsafe endpoint %q", endpoint)
		}
	}
	for _, addr := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "169.254.169.254", "10.0.0.1", "100.64.0.1", "198.18.0.1", "192.88.99.1", "64:ff9b::7f00:1", "2002:7f00:1::", "fec0::1", "100::1", "100:0:0:1::1", "5f00::1", "2606:4700:4700::1111%lo0"} {
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

// The push service says a subscription is gone when the delivery returns,
// by which time the device may have subscribed anew. Only the subscription
// that was delivered to is gone.
func TestGoneSubscriptionDoesNotDeleteItsReplacement(t *testing.T) {
	ts := newTestServer(t)
	p := ts.srv.push
	ctx := context.Background()
	kinds := []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION}
	stored := func() string {
		var sub string
		_ = p.s.store.db.QueryRow(`SELECT subscription FROM push_subs WHERE device_id = 'client'`).Scan(&sub)
		return sub
	}
	gone := func(during func()) *http.Client {
		return &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			during()
			return &http.Response{StatusCode: http.StatusGone, Body: http.NoBody, Request: r}, nil
		})}
	}
	old, replacement := pushSubscription(t, "https://push.example/old"), pushSubscription(t, "https://push.example/new")
	if err := p.subscribe(ctx, "client", old, kinds); err != nil {
		t.Fatal(err)
	}
	// Notifications are turned off and on again while a delivery is out.
	p.client = gone(func() {
		if err := p.subscribe(ctx, "client", replacement, kinds); err != nil {
			t.Error(err)
		}
	})
	p.send("client", old, []byte(`{}`))
	if stored() != replacement {
		t.Fatal("a delivery to the old subscription deleted the one that replaced it")
	}
	p.client = gone(func() {})
	p.send("client", replacement, []byte(`{}`))
	if stored() != "" {
		t.Fatal("a subscription the push service dropped is still stored")
	}
}
