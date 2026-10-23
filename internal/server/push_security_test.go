package server

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// The recovery notice is generic text and must reach only the current
// authority client, not every member.
func TestSecurityPushOnlyToAuthority(t *testing.T) {
	ts := newTestServer(t)
	_, otherID, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	p := ts.srv.push
	var mu sync.Mutex
	var paths []string
	p.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody, Request: r}, nil
	})}
	ctx := context.Background()
	kinds := []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_SECURITY}
	if err := p.subscribe(ctx, ts.tenant(), c.phoneID, pushSubscription(t, "https://push.example/authority"), kinds); err != nil {
		t.Fatal(err)
	}
	if err := p.subscribe(ctx, ts.tenant(), otherID, pushSubscription(t, "https://push.example/other"), kinds); err != nil {
		t.Fatal(err)
	}
	if err := p.notifyDevice(context.Background(), ts.tenant(), c.phoneID, "", pb.NotifyKind_NOTIFY_KIND_SECURITY, nil, "security/test"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(paths)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no security push")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/authority" {
		t.Fatalf("security push paths: %v", paths)
	}
}

// A hidden browser may retain its stream briefly. Subscription delivery must
// not depend on whether that client still has a live stream.
func TestWebPushReachesConnectedClient(t *testing.T) {
	ts := newTestServer(t)
	priv, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	ts.stream(t, ts.login(t, priv, id))
	ts.srv.hub.mu.Lock()
	connected := len(ts.srv.hub.clients[ts.acct()])
	ts.srv.hub.mu.Unlock()
	if connected != 1 {
		t.Fatalf("expected one connected client, got %d", connected)
	}
	delivered := make(chan string, 1)
	p := ts.srv.push
	p.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		delivered <- r.URL.Path
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody, Request: r}, nil
	})}
	if err := p.subscribe(context.Background(), ts.tenant(), id,
		pushSubscription(t, "https://push.example.com/connected"),
		[]pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION}); err != nil {
		t.Fatal(err)
	}
	if err := p.notify(context.Background(), ts.tenant(), "session", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "connected-test"); err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-delivered:
		if path != "/connected" {
			t.Fatalf("unexpected delivery path %q", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connected client did not receive Web Push")
	}
}
