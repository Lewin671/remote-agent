package server

import (
	"connectrpc.com/connect"
	"context"
	"encoding/json"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"net/http"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestMachineDiskPushKindAndCiphertext(t *testing.T) {
	if !validNotifyKinds([]pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK}) {
		t.Fatal("machine kind refused")
	}
	payload := webPushPayload("", pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK, []byte("sealed-fixture"), "public-event")
	var m map[string]string
	if e := json.Unmarshal(payload, &m); e != nil {
		t.Fatal(e)
	}
	if m["content"] == "" || m["session"] != "" {
		t.Fatal("sessionless ciphertext missing")
	}
	if m["body"] == "sealed-fixture" {
		t.Fatal("plaintext leaked")
	}
}

func TestMachineDiskNotificationScopeValidation(t *testing.T) {
	ts := newTestServer(t)
	priv, mid, e := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if e != nil {
		t.Fatal(e)
	}
	m := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, priv, mid)))
	ctx := context.Background()
	envelope := func(machine, session string) *pb.Envelope {
		env, e := opaqueMessage(&pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: machine, SessionId: session, RequestId: "disk:1:2", Sender: mid}, &pb.NotifyContent{MachineId: machine, Kind: pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK})
		if e != nil {
			t.Fatal(e)
		}
		return env
	}
	for _, tc := range []struct {
		session string
		kind    pb.NotifyKind
		env     *pb.Envelope
		ok      bool
	}{
		{"", pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK, envelope(mid, ""), true},
		{"session", pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK, envelope(mid, "session"), false},
		{"", pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK, nil, false},
		{"", pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK, envelope("other", ""), false},
		{"", pb.NotifyKind_NOTIFY_KIND_ATTENTION, envelope(mid, ""), false},
	} {
		_, e := m.Notify(ctx, connect.NewRequest(&pb.NotifyRequest{SessionId: tc.session, Kind: tc.kind, Content: tc.env}))
		if (e == nil) != tc.ok {
			t.Fatalf("session=%q kind=%v success=%v error=%v", tc.session, tc.kind, tc.ok, e)
		}
	}
}
func TestMachineDiskTagsAreMachineScoped(t *testing.T) {
	var tags []string
	for _, id := range []string{"computer-a", "computer-b"} {
		var payload map[string]string
		if e := json.Unmarshal(webPushPayload("", pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK, []byte("ciphertext"), "event", id), &payload); e != nil {
			t.Fatal(e)
		}
		tags = append(tags, payload["tag"])
		if payload["machine"] != id || payload["tag"] != id+"/NOTIFY_KIND_MACHINE_DISK" {
			t.Fatal(payload)
		}
	}
	if tags[0] == tags[1] {
		t.Fatal("machine notifications replace one another")
	}
}

func TestMachineDiskCoalescingKeepsMachinesSeparate(t *testing.T) {
	ts := newTestServer(t)
	seedPushAccount(t, ts, ts.acct())
	for _, id := range []string{"computer-a", "computer-b"} {
		if e := ts.srv.push.notifyTo(context.Background(), ts.tenant(), "", "", pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK, nil, "", id); e != nil {
			t.Fatal(e)
		}
	}
	if len(ts.srv.push.recent) != 2 {
		t.Fatal("machines coalesced into one pending notification")
	}
}
func TestPushKeyAdvertisesMachineNotificationCapability(t *testing.T) {
	ts := newTestServer(t)
	priv, id, e := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if e != nil {
		t.Fatal(e)
	}
	c := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, priv, id)))
	r, e := c.GetPushKey(context.Background(), connect.NewRequest(&pb.GetPushKeyRequest{}))
	if e != nil || !r.Msg.MachineDiskNotifications {
		t.Fatal(r, e)
	}
}
