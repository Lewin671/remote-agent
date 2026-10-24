package server

import (
	"context"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestScheduledPushDisplayIdentity(t *testing.T) {
	retained := map[string]bool{}
	for _, event := range []string{"scheduled:7", "scheduled:8", "scheduled:7"} {
		retained[pushNotificationTag("session", pb.NotifyKind_NOTIFY_KIND_SCHEDULED, event)] = true
	}
	if len(retained) != 2 {
		t.Fatal("distinct scheduled results replaced one another, or a retry created a duplicate")
	}
	if pushNotificationTag("other", pb.NotifyKind_NOTIFY_KIND_SCHEDULED, "scheduled:7") == pushNotificationTag("session", pb.NotifyKind_NOTIFY_KIND_SCHEDULED, "scheduled:7") {
		t.Fatal("sessions share notification identity")
	}
	if pushNotificationTag("session", pb.NotifyKind_NOTIFY_KIND_ATTENTION, "a") != pushNotificationTag("session", pb.NotifyKind_NOTIFY_KIND_ATTENTION, "b") {
		t.Fatal("ordinary notification replacement changed")
	}
}

func TestScheduledFCMIncludesEventIdentity(t *testing.T) {
	ts := newTestServer(t)
	fake := newFakeFCM(t)
	f, err := loadFCM(fake.account)
	if err != nil {
		t.Fatal(err)
	}
	f.sendURL = fake.srv.URL + "/send"
	ts.srv.push.fcm = f
	_, clientID, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ts.srv.push.setFCM(ctx, ts.tenant(), clientID, "notification-token", []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_SCHEDULED}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"scheduled:7", "scheduled:8", "scheduled:7"} {
		if err := ts.srv.push.notify(ctx, ts.tenant(), "session", pb.NotifyKind_NOTIFY_KIND_SCHEDULED, nil, key); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "two distinct scheduled FCM messages", func() bool { return fake.count() == 2 })
	fake.mu.Lock()
	defer fake.mu.Unlock()
	events := map[string]bool{}
	for _, data := range fake.sent {
		events[data["event"]] = true
	}
	if len(fake.sent) != 2 || !events["scheduled:7"] || !events["scheduled:8"] {
		t.Fatalf("FCM lost stable per-event identity: %v", events)
	}
}
