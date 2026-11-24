package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
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

	memberAPriv, memberAID, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	memberBPriv, memberBID, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	auth := ts.authority(t)
	seedPushAccount(t, ts, "other-acct")
	otherTenant := ts.srv.store.tenant("other-acct")
	if err := ts.srv.push.subscribe(ctx, otherTenant, "other-device", pushSubscription(t, endpoint), kinds); err != nil {
		t.Fatal(err)
	}

	client := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url)
	tokA := ts.login(t, memberAPriv, memberAID)
	tokB := ts.login(t, memberBPriv, memberBID)
	subA := pushSubscription(t, endpoint)

	// Original member registers endpoint and can update its own kinds.
	reqA := connect.NewRequest(&pb.SetPushSubscriptionRequest{SubscriptionJson: subA, Kinds: kinds})
	reqA.Header().Set("Authorization", "Bearer "+tokA)
	if _, err := client.SetPushSubscription(ctx, reqA); err != nil {
		t.Fatalf("member A subscribe: %v", err)
	}
	reqA2 := connect.NewRequest(&pb.SetPushSubscriptionRequest{
		SubscriptionJson: subA,
		Kinds:            []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION, pb.NotifyKind_NOTIFY_KIND_TURN_ENDED},
	})
	reqA2.Header().Set("Authorization", "Bearer "+tokA)
	if _, err := client.SetPushSubscription(ctx, reqA2); err != nil {
		t.Fatalf("member A self-update: %v", err)
	}

	// Another non-authority member must be rejected with PermissionDenied, leaving member A intact.
	reqB := connect.NewRequest(&pb.SetPushSubscriptionRequest{SubscriptionJson: pushSubscription(t, endpoint), Kinds: kinds})
	reqB.Header().Set("Authorization", "Bearer "+tokB)
	if _, err := client.SetPushSubscription(ctx, reqB); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("expected PermissionDenied for non-authority takeover, got %v", err)
	}
	var ownerID string
	var ownerMask int
	if err := ts.srv.store.db.QueryRowContext(ctx, `SELECT device_id, kinds FROM push_subs WHERE account_id = ?`, ts.acct()).Scan(&ownerID, &ownerMask); err != nil {
		t.Fatal(err)
	}
	wantMask := kindMask([]pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION, pb.NotifyKind_NOTIFY_KIND_TURN_ENDED})
	if ownerID != memberAID || ownerMask != wantMask {
		t.Fatalf("member A row altered after rejected takeover: owner=%q mask=%d", ownerID, ownerMask)
	}

	// Authority may take over the same endpoint within the account, deduplicating the old row.
	if err := ts.srv.push.subscribe(ctx, ts.tenant(), auth.phoneID, pushSubscription(t, endpoint), kinds); err != nil {
		t.Fatalf("authority takeover: %v", err)
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
	if len(ids) != 1 || ids[0] != auth.phoneID {
		t.Fatalf("expected only authority %q in push_subs, got %v", auth.phoneID, ids)
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

	memberAPriv, memberAID, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	memberBPriv, memberBID, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	auth := ts.authority(t)
	seedPushAccount(t, ts, "other-acct")
	otherTenant := ts.srv.store.tenant("other-acct")
	if err := ts.srv.push.setFCM(ctx, otherTenant, "other-device", token, kinds); err != nil {
		t.Fatal(err)
	}

	client := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url)
	tokA := ts.login(t, memberAPriv, memberAID)
	tokB := ts.login(t, memberBPriv, memberBID)

	// Original member registers FCM token and updates its own row.
	reqA := connect.NewRequest(&pb.SetPushTargetRequest{Type: pb.PushType_PUSH_TYPE_FCM, Target: token, Kinds: kinds})
	reqA.Header().Set("Authorization", "Bearer "+tokA)
	if _, err := client.SetPushTarget(ctx, reqA); err != nil {
		t.Fatalf("member A setFCM: %v", err)
	}
	if err := ts.srv.push.setFCM(ctx, ts.tenant(), memberAID, token, []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION, pb.NotifyKind_NOTIFY_KIND_TURN_ENDED}); err != nil {
		t.Fatalf("member A self-update setFCM: %v", err)
	}

	// Non-authority member B cannot hijack member A's FCM token.
	reqB := connect.NewRequest(&pb.SetPushTargetRequest{Type: pb.PushType_PUSH_TYPE_FCM, Target: token, Kinds: kinds})
	reqB.Header().Set("Authorization", "Bearer "+tokB)
	if _, err := client.SetPushTarget(ctx, reqB); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("expected PermissionDenied for non-authority FCM takeover, got %v", err)
	}
	if err := ts.srv.push.setFCM(ctx, ts.tenant(), memberBID, token, kinds); !errors.Is(err, errPushTargetOwned) {
		t.Fatalf("expected errPushTargetOwned, got %v", err)
	}

	// Authority may take over and deduplicate the FCM token.
	if err := ts.srv.push.setFCM(ctx, ts.tenant(), auth.phoneID, token, kinds); err != nil {
		t.Fatalf("authority setFCM takeover: %v", err)
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
	if len(ids) != 1 || ids[0] != auth.phoneID {
		t.Fatalf("expected only authority %q in fcm_targets, got %v", auth.phoneID, ids)
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
