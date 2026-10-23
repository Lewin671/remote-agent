package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

func TestNotificationDedupeStateIsBounded(t *testing.T) {
	ts := newTestServer(t)
	p := ts.srv.push
	st := ts.srv.store.tenant
	// One account has a share of the state; it cannot take the rest.
	for i := range maxAccountPushRecent {
		if err := p.notify(context.Background(), st("a"), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.notify(context.Background(), st("a"), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "overflow"); !errors.Is(err, errPushBusy) {
		t.Fatalf("one account's notification state is unbounded: %v", err)
	}
	if err := p.notify(context.Background(), st("b"), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "overflow"); err != nil {
		t.Fatalf("one account's notifications used up another's: %v", err)
	}
	// All of them together have a bound too.
	for i := 0; len(p.recent) < maxPushRecent; i++ {
		acct := fmt.Sprintf("acct-%d", i/maxAccountPushRecent)
		if err := p.notify(context.Background(), st(acct), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.notify(context.Background(), st("c"), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "overflow"); !errors.Is(err, errPushBusy) {
		t.Fatalf("unbounded notification state: %v", err)
	}
	if len(p.recent) != maxPushRecent {
		t.Fatalf("dedupe map grew to %d", len(p.recent))
	}
	// Replays remain successful without consuming capacity.
	if err := p.notify(context.Background(), st("a"), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "0"); err != nil {
		t.Fatal(err)
	}
	// Expired state is swept before capacity is checked.
	p.mu.Lock()
	for k, r := range p.recent {
		p.recent[k] = pushRecent{acct: r.acct, at: time.Now().Add(-pushDedupe)}
	}
	p.mu.Unlock()
	if err := p.notify(context.Background(), st("c"), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "overflow"); err != nil {
		t.Fatal(err)
	}
	if len(p.recent) != 1 {
		t.Fatalf("expired state retained: %d", len(p.recent))
	}
}

func TestNotificationDeliveriesAreBoundedAndOverloadIsRetryable(t *testing.T) {
	ts := newTestServer(t)
	p := ts.srv.push
	st := ts.srv.store.tenant
	blocked := make(chan struct{})
	completed := make(chan struct{}, maxPushDeliveries*2)
	p.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		<-blocked
		completed <- struct{}{}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	sub := pushSubscription(t, "https://push.example/endpoint")
	// Each account has two clients subscribed.
	accounts := maxPushDeliveries / maxAccountPushDeliveries
	for a := range accounts + 1 {
		for _, id := range []string{"a", "b"} {
			if err := p.subscribe(context.Background(), st(fmt.Sprint("acct-", a)), fmt.Sprint(a, "-client-", id), sub, []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// One account fills its own share and no more.
	for i := range maxAccountPushDeliveries / 2 {
		if err := p.notify(context.Background(), st("acct-0"), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.notify(context.Background(), st("acct-0"), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "retry"); !errors.Is(err, errPushBusy) {
		t.Fatalf("one account's concurrent pushes are unbounded: %v", err)
	}
	// The others fill the relay's.
	for a := 1; a < accounts; a++ {
		for i := range maxAccountPushDeliveries / 2 {
			if err := p.notify(context.Background(), st(fmt.Sprint("acct-", a)), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, fmt.Sprint(i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(p.deliveries) != maxPushDeliveries {
		t.Fatalf("deliveries=%d", len(p.deliveries))
	}
	last := st(fmt.Sprint("acct-", accounts))
	if err := p.notify(context.Background(), last, "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "retry"); !errors.Is(err, errPushBusy) {
		t.Fatalf("unbounded concurrent pushes: %v", err)
	}
	if _, ok := p.recent[pushRecentKey(last.acct, "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, "retry")]; ok {
		t.Fatal("rejected notification consumed its dedupe key")
	}
	close(blocked)
	deadline := time.Now().Add(10 * time.Second)
	for len(p.deliveries) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("delivery slots leaked")
		}
		time.Sleep(time.Millisecond)
	}
	if err := p.notify(context.Background(), last, "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "retry"); err != nil {
		t.Fatalf("retry after overload: %v", err)
	}
	// Wait for the retry's asynchronous network calls before closing SQLite.
	for len(p.deliveries) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("retry delivery slots leaked")
		}
		time.Sleep(time.Millisecond)
	}
	if len(completed) != maxPushDeliveries+2 {
		t.Fatalf("unexpected delivery count: %d", len(completed))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.busy) != 0 {
		t.Fatalf("deliveries in flight still counted: %v", p.busy)
	}
}

func TestNotificationRetainedKeysHaveBoundedBytes(t *testing.T) {
	ts := newTestServer(t)
	p := ts.srv.push
	sid := strings.Repeat("s", 512<<10)
	for i := range 8 {
		if err := p.notify(context.Background(), ts.tenant(), sid, pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	for key := range p.recent {
		if len(key) > 66 {
			t.Fatalf("retained notification key has %d bytes", len(key))
		}
	}
	// Routing identifiers containing delimiters cannot suppress each other.
	kind := pb.NotifyKind_NOTIFY_KIND_ATTENTION
	if pushRecentKey("acct", "a/"+kind.String()+"/b", kind, "x") == pushRecentKey("acct", "a", kind, "b/"+kind.String()+"/x") ||
		pushRecentKey("acct/a", "s", kind, "x") == pushRecentKey("acct", "a/s", kind, "x") {
		t.Fatal("ambiguous notification field framing")
	}
}

func TestNotificationKeysAreScopedToSession(t *testing.T) {
	ts := newTestServer(t)
	p := ts.srv.push
	if err := p.notify(context.Background(), ts.tenant(), "s-a", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "same"); err != nil {
		t.Fatal(err)
	}
	if err := p.notify(context.Background(), ts.tenant(), "s-b", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "same"); err != nil {
		t.Fatal(err)
	}
	if len(p.recent) != 2 {
		t.Fatal("one session suppressed another session's notification")
	}
	// Nor does one account another's, with the same session ID and key.
	if err := p.notify(context.Background(), ts.srv.store.tenant("other"), "s-a", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "same"); err != nil {
		t.Fatal(err)
	}
	if len(p.recent) != 3 {
		t.Fatal("one account suppressed another account's notification")
	}
}

func TestInvalidNotificationKindsReturnErrorWithoutPanic(t *testing.T) {
	ts := newTestServer(t)
	cp, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	c := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cp, cid)))
	for _, kind := range []pb.NotifyKind{-1, 0, 1000} {
		for _, typ := range []pb.PushType{pb.PushType_PUSH_TYPE_WEB_PUSH, pb.PushType_PUSH_TYPE_FCM} {
			target := "fcm-token"
			if typ == pb.PushType_PUSH_TYPE_WEB_PUSH {
				target = pushSubscription(t, "https://push.example/endpoint")
			}
			_, err := c.SetPushTarget(context.Background(), connect.NewRequest(&pb.SetPushTargetRequest{Type: typ, Target: target, Kinds: []pb.NotifyKind{kind}}))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("invalid kind %d caused unexpected response: %v", kind, err)
			}
		}
	}
}
