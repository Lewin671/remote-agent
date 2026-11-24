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

func TestPushDeduplicatesWebPushEndpointWithinAccount(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	kinds := []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION}
	endpoint := "https://push.example.com/shared-browser"

	seedPushAccount(t, ts, ts.acct())
	seedPushAccount(t, ts, "other-acct")
	otherTenant := ts.srv.store.tenant("other-acct")
	if err := ts.srv.push.subscribe(ctx, otherTenant, "other-device", pushSubscription(t, endpoint), kinds); err != nil {
		t.Fatal(err)
	}
	if err := ts.srv.push.subscribe(ctx, ts.tenant(), "old-client", pushSubscription(t, endpoint), kinds); err != nil {
		t.Fatal(err)
	}
	if err := ts.srv.push.subscribe(ctx, ts.tenant(), "new-client", pushSubscription(t, endpoint), kinds); err != nil {
		t.Fatal(err)
	}

	var ids []string
	rows, err := ts.srv.store.db.QueryContext(ctx, `SELECT device_id FROM push_subs WHERE account_id = ? ORDER BY device_id`, ts.acct())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 1 || ids[0] != "new-client" {
		t.Fatalf("expected only new-client in push_subs, got %v", ids)
	}

	var otherCount int
	if err := ts.srv.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM push_subs WHERE account_id = 'other-acct' AND device_id = 'other-device'`).Scan(&otherCount); err != nil || otherCount != 1 {
		t.Fatalf("other account subscription should stay untouched: count=%d err=%v", otherCount, err)
	}

	delivered := make(chan string, 4)
	ts.srv.push.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		delivered <- r.URL.Path
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody, Request: r}, nil
	})}
	if err := ts.srv.push.notify(ctx, ts.tenant(), "session", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "dedupe-web"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-delivered:
		if got != "/shared-browser" {
			t.Fatalf("unexpected delivery path %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing web push delivery")
	}
	ts.srv.Close()
	if len(delivered) != 0 {
		t.Fatalf("expected single delivery, got %d extra", len(delivered))
	}
}

func TestPushDeduplicatesFCMTokenWithinAccount(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	kinds := []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION}
	token := "fcm-shared-device-token"

	seedPushAccount(t, ts, ts.acct())
	seedPushAccount(t, ts, "other-acct")
	otherTenant := ts.srv.store.tenant("other-acct")
	if err := ts.srv.push.setFCM(ctx, otherTenant, "other-device", token, kinds); err != nil {
		t.Fatal(err)
	}
	if err := ts.srv.push.setFCM(ctx, ts.tenant(), "old-phone", token, kinds); err != nil {
		t.Fatal(err)
	}
	if err := ts.srv.push.setFCM(ctx, ts.tenant(), "new-phone", token, kinds); err != nil {
		t.Fatal(err)
	}

	var ids []string
	rows, err := ts.srv.store.db.QueryContext(ctx, `SELECT device_id FROM fcm_targets WHERE account_id = ? ORDER BY device_id`, ts.acct())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 1 || ids[0] != "new-phone" {
		t.Fatalf("expected only new-phone in fcm_targets, got %v", ids)
	}

	var otherCount int
	if err := ts.srv.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fcm_targets WHERE account_id = 'other-acct' AND device_id = 'other-device'`).Scan(&otherCount); err != nil || otherCount != 1 {
		t.Fatalf("other account FCM target should stay untouched: count=%d err=%v", otherCount, err)
	}
}

func TestPushNotifyDeduplicatesPreexistingDuplicateEndpoints(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	sub1 := pushSubscription(t, "https://push.example.com/preexisting")
	sub2 := pushSubscription(t, "https://push.example.com/preexisting")
	mask := kindBit(pb.NotifyKind_NOTIFY_KIND_ATTENTION)
	if _, err := ts.srv.store.db.ExecContext(ctx, `INSERT INTO push_subs (device_id, account_id, subscription, kinds) VALUES ('old-client', ?, ?, ?), ('new-client', ?, ?, ?)`,
		ts.acct(), sub1, mask, ts.acct(), sub2, mask); err != nil {
		t.Fatal(err)
	}
	delivered := make(chan string, 4)
	ts.srv.push.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		delivered <- r.URL.Path
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody, Request: r}, nil
	})}
	if err := ts.srv.push.notify(ctx, ts.tenant(), "session", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "dedupe-preexisting"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-delivered:
		if got != "/preexisting" {
			t.Fatalf("unexpected delivery path %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing web push delivery")
	}
	ts.srv.Close()
	if len(delivered) != 0 {
		t.Fatalf("expected single delivery for preexisting duplicates, got %d extra", len(delivered))
	}
}
