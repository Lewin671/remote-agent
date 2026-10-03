package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// testStream is a stream connection of a logged-in device.
type testStream struct {
	ws *websocket.Conn
}

func (ts *testServer) stream(t *testing.T, token string) *testStream {
	t.Helper()
	ctx := context.Background()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.url, "http")+"/v1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(8 << 20)
	t.Cleanup(func() { ws.CloseNow() })
	hello, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: token}}})
	if err := ws.Write(ctx, websocket.MessageBinary, hello); err != nil {
		t.Fatal(err)
	}
	s := &testStream{ws: ws}
	if f := s.read(t); f.GetReady() == nil {
		t.Fatalf("first frame %v", f)
	}
	return s
}

func (s *testStream) read(t *testing.T) *pb.ServerFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := s.ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := &pb.ServerFrame{}
	if err := proto.Unmarshal(b, f); err != nil {
		t.Fatal(err)
	}
	return f
}

// next returns the next frame ok accepts, skipping others.
func (s *testStream) next(t *testing.T, ok func(*pb.ServerFrame) bool) *pb.ServerFrame {
	t.Helper()
	for {
		if f := s.read(t); ok(f) {
			return f
		}
	}
}

func (s *testStream) write(t *testing.T, f *pb.PeerFrame) {
	t.Helper()
	b, _ := proto.Marshal(f)
	if err := s.ws.Write(context.Background(), websocket.MessageBinary, b); err != nil {
		t.Fatal(err)
	}
}

func newV2Env(t *testing.T) (*testServer, string, string, remoteagentv1connect.MachineServiceClient, remoteagentv1connect.ClientServiceClient, *testStream) {
	t.Helper()
	ts := newTestServer(t)
	mpriv, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	cpriv, cid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if err != nil {
		t.Fatal(err)
	}
	mtok, ctok := ts.login(t, mpriv, mid), ts.login(t, cpriv, cid)
	machine := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(mtok))
	client := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ctok))
	return ts, mid, cid, machine, client, ts.stream(t, mtok)
}

func TestForwardChecksSenderAndPassesRegistration(t *testing.T) {
	ts, mid, cid, _, client, ms := newV2Env(t)
	ctx := context.Background()
	seal := func(sender string) *pb.Envelope {
		env, err := e2e.Seal(ts.keys.Command, &pb.Header{
			Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: mid, RequestId: "0123456789abcdef0123456789abcdef",
			DeadlineMs: time.Now().Add(time.Minute).UnixMilli(), Sender: sender,
		}, []byte("cmd"))
		if err != nil {
			t.Fatal(err)
		}
		return env
	}
	// Claiming to be another device is refused before reaching the machine.
	_, err := client.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: mid, Command: seal(mid)}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("forged sender: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := client.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: mid, Command: seal(cid)}))
		done <- err
	}()
	f := ms.next(t, func(f *pb.ServerFrame) bool { return f.GetForwardCall() != nil })
	call := f.GetForwardCall()
	if call.SenderDeviceId != cid {
		t.Fatalf("sender %q, want %q", call.SenderDeviceId, cid)
	}
	reg := &pb.DeviceRegistration{}
	if err := proto.Unmarshal(call.SenderRegistration, reg); err != nil || e2e.DeviceID(reg.DevicePublicKey) != cid ||
		!e2e.VerifyRegistration(ts.keys.AccountPublicKey(), call.SenderRegistration, call.SenderRegistrationSignature) {
		t.Fatalf("registration does not check out: %v", err)
	}
	res, _ := e2e.Seal(ts.keys.Command, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND_RESULT, MachineId: mid}, []byte("ok"))
	ms.write(t, &pb.PeerFrame{Kind: &pb.PeerFrame_ForwardReply{ForwardReply: &pb.ForwardReply{CallId: call.CallId, Result: res}}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func putTestSession(t *testing.T, ts *testServer, machine remoteagentv1connect.MachineServiceClient, mid, sid string) {
	t.Helper()
	key, _ := e2e.Seal(ts.keys.Wrap, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: mid, SessionId: sid}, e2e.NewKey())
	if _, err := machine.PutSession(context.Background(), connect.NewRequest(&pb.PutSessionRequest{SessionId: sid, Key: key})); err != nil {
		t.Fatal(err)
	}
}

func sealImage(t *testing.T, mid, sid, id string, n int) *pb.Envelope {
	t.Helper()
	env, err := e2e.SealMessage(e2e.NewKey(), &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_IMAGE, MachineId: mid, SessionId: sid, RequestId: id, Sender: mid},
		&pb.ImagePayload{Format: pb.ImageFormat_IMAGE_FORMAT_PNG, Data: make([]byte, n)})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestImages(t *testing.T) {
	ts := newTestServer(t)
	ts.srv.cfg.ImageQuota = 3000
	mpriv, mid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	m2priv, m2id, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "")
	cpriv, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	machine := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, mpriv, mid)))
	machine2 := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, m2priv, m2id)))
	client := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cpriv, cid)))
	ctx := context.Background()
	putTestSession(t, ts, machine, mid, "s1")
	const id1, id2, id3 = "00000000000000000000000000000001", "00000000000000000000000000000002", "00000000000000000000000000000003"

	put := func(mc remoteagentv1connect.MachineServiceClient, sid, id string, env *pb.Envelope) error {
		_, err := mc.PutImage(ctx, connect.NewRequest(&pb.PutImageRequest{SessionId: sid, ImageId: id, Image: env}))
		return err
	}
	if err := put(machine, "s1", id1, sealImage(t, mid, "s1", id1, 1000)); err != nil {
		t.Fatal(err)
	}
	// Again is a no-op, and does not count twice.
	if err := put(machine, "s1", id1, sealImage(t, mid, "s1", id1, 1000)); err != nil {
		t.Fatal(err)
	}
	// Another machine cannot store into this machine's session, nor may a
	// client store at all, nor may the envelope be for another place.
	if err := put(machine2, "s1", id2, sealImage(t, m2id, "s1", id2, 10)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("other machine: %v", err)
	}
	if err := put(machine, "s1", id2, sealImage(t, mid, "s1", id3, 10)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("misplaced envelope: %v", err)
	}
	if err := put(machine, "s1", "short", sealImage(t, mid, "s1", "short", 10)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad ID: %v", err)
	}
	// The quota is checked with the insert.
	if err := put(machine, "s1", id2, sealImage(t, mid, "s1", id2, 1000)); err != nil {
		t.Fatal(err)
	}
	if err := put(machine, "s1", id3, sealImage(t, mid, "s1", id3, 1000)); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("over quota: %v", err)
	}

	res, err := client.GetImage(ctx, connect.NewRequest(&pb.GetImageRequest{SessionId: "s1", ImageId: id1}))
	if err != nil || res.Msg.Image == nil || res.Msg.Expired || res.Msg.ExpiresMs <= time.Now().UnixMilli() {
		t.Fatalf("get: %v %v", res, err)
	}
	if _, err := client.GetImage(ctx, connect.NewRequest(&pb.GetImageRequest{SessionId: "s1", ImageId: id3})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("never stored: %v", err)
	}

	// Past its time an image is reported expired and frees its space.
	if _, err := ts.srv.store.db.Exec(`UPDATE images SET expires_ms = 1 WHERE image_id = ?`, id1); err != nil {
		t.Fatal(err)
	}
	res, err = client.GetImage(ctx, connect.NewRequest(&pb.GetImageRequest{SessionId: "s1", ImageId: id1}))
	if err != nil || res.Msg.Image != nil || !res.Msg.Expired {
		t.Fatalf("expired: %v %v", res, err)
	}
	if err := put(machine, "s1", id3, sealImage(t, mid, "s1", id3, 1000)); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	var stored []byte
	_ = ts.srv.store.db.QueryRow(`SELECT envelope FROM images WHERE image_id = ?`, id1).Scan(&stored)
	if stored != nil {
		t.Fatal("expired envelope kept")
	}
	// An expired ID is not stored again.
	if err := put(machine, "s1", id1, sealImage(t, mid, "s1", id1, 10)); err != nil {
		t.Fatal(err)
	}
	if res, _ := client.GetImage(ctx, connect.NewRequest(&pb.GetImageRequest{SessionId: "s1", ImageId: id1})); !res.Msg.Expired {
		t.Fatal("expired image came back")
	}

	// Removing the machine removes its images.
	if _, err := client.RemoveDevice(ctx, connect.NewRequest(&pb.RemoveDeviceRequest{DeviceId: mid})); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = ts.srv.store.db.QueryRow(`SELECT COUNT(*) FROM images`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d images left", n)
	}
}

func TestAccountPrefs(t *testing.T) {
	ts, mid, cid, machine, client, _ := newV2Env(t)
	_ = machine
	ctx := context.Background()
	cpriv2, cid2, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	client2 := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cpriv2, cid2)))
	cs := ts.stream(t, ts.login(t, cpriv2, cid2))

	seal := func(sender string, rev uint64, text string) *pb.Envelope {
		env, _ := e2e.Seal(ts.keys.Prefs, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_ACCOUNT_PREFS, RequestId: "project-prefs", Seq: rev, Sender: sender}, []byte(text))
		return env
	}
	put := func(c remoteagentv1connect.ClientServiceClient, expected uint64, env *pb.Envelope) (*pb.PutAccountPrefsResponse, error) {
		res, err := c.PutAccountPrefs(ctx, connect.NewRequest(&pb.PutAccountPrefsRequest{ObjectId: "project-prefs", ExpectedRevision: expected, Envelope: env}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	got, err := client.GetAccountPrefs(ctx, connect.NewRequest(&pb.GetAccountPrefsRequest{ObjectId: "project-prefs"}))
	if err != nil || got.Msg.Object.Revision != 0 || got.Msg.Object.Envelope != nil {
		t.Fatalf("empty: %v %v", got, err)
	}
	first := seal(cid, 1, "a")
	if r, err := put(client, 0, first); err != nil || !r.Applied || r.Object.Revision != 1 {
		t.Fatalf("create: %v %v", r, err)
	}
	f := cs.next(t, func(f *pb.ServerFrame) bool { return f.GetAccountPrefsUpdated() != nil })
	if f.GetAccountPrefsUpdated().Object.Revision != 1 {
		t.Fatal("push has the wrong revision")
	}
	// A retried write that went through counts as applied.
	if r, err := put(client, 0, first); err != nil || !r.Applied || r.Object.Revision != 1 {
		t.Fatalf("retry: %v %v", r, err)
	}
	// Another client based on the old revision gets a conflict and the
	// current object.
	if r, err := put(client2, 0, seal(cid2, 1, "b")); err != nil || r.Applied || r.Object.Revision != 1 || !proto.Equal(r.Object.Envelope, first) {
		t.Fatalf("conflict: %v %v", r, err)
	}
	if r, err := put(client2, 1, seal(cid2, 2, "b")); err != nil || !r.Applied || r.Object.Revision != 2 {
		t.Fatalf("redo: %v %v", r, err)
	}
	// The sealed revision must be the new one, the sender the caller, and
	// machines have no say.
	if _, err := put(client, 2, seal(cid, 2, "c")); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("stale sealed revision: %v", err)
	}
	if _, err := put(client, 2, seal(cid2, 3, "c")); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("other sender: %v", err)
	}
	mc := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(machineToken(t, ts, mid)))
	if _, err := put(mc, 2, seal(mid, 3, "c")); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("machine: %v", err)
	}
}

// machineToken logs in a new machine of the account.
func machineToken(t *testing.T, ts *testServer, _ string) string {
	priv, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "")
	if err != nil {
		t.Fatal(err)
	}
	return ts.login(t, priv, id)
}

func TestDeviceDirectory(t *testing.T) {
	ts, mid, cid, _, client, _ := newV2Env(t)
	ctx := context.Background()
	cpriv2, cid2, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	client2 := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cpriv2, cid2)))
	cs := ts.stream(t, ts.login(t, cpriv2, cid2))

	list, err := client.ListDevices(ctx, connect.NewRequest(&pb.ListDevicesRequest{}))
	if err != nil || len(list.Msg.Devices) != 3 || list.Msg.CurrentDeviceId != cid {
		t.Fatalf("list: %v %v", list, err)
	}
	// Only a client that has had a stream open has a last-seen time.
	for _, d := range list.Msg.Devices {
		if seen := d.LastSeenMs > 0; seen != (d.Id == cid2) {
			t.Fatalf("last seen of %s: %d", d.Id, d.LastSeenMs)
		}
	}
	seal := func(sender, target string, rev uint64) *pb.Envelope {
		env, _ := e2e.SealMessage(ts.keys.DeviceMeta, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_DEVICE_META, RequestId: target, Seq: rev, Sender: sender},
			&pb.DeviceMeta{Name: "Pixel", Platform: "android"})
		return env
	}
	put := func(c remoteagentv1connect.ClientServiceClient, target string, expected uint64, env *pb.Envelope) (*pb.PutDeviceMetaResponse, error) {
		res, err := c.PutDeviceMeta(ctx, connect.NewRequest(&pb.PutDeviceMetaRequest{DeviceId: target, ExpectedRevision: expected, Meta: env}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	// Any client may name another client.
	if r, err := put(client, cid2, 0, seal(cid, cid2, 1)); err != nil || !r.Applied || r.Device.MetaRevision != 1 {
		t.Fatalf("name: %v %v", r, err)
	}
	f := cs.next(t, func(f *pb.ServerFrame) bool { return f.GetDeviceUpdated() != nil })
	if d := f.GetDeviceUpdated().Device; d.Id != cid2 || d.MetaRevision != 1 {
		t.Fatalf("push: %v", d)
	}
	if r, err := put(client2, cid2, 0, seal(cid2, cid2, 1)); err != nil || r.Applied || r.Device.MetaRevision != 1 {
		t.Fatalf("conflict: %v %v", r, err)
	}
	if _, err := put(client, mid, 0, seal(cid, mid, 1)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("machine metadata: %v", err)
	}
	if _, err := put(client, cid2, 1, seal(cid, cid, 2)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("envelope for another device: %v", err)
	}
	// Removing a device is pushed.
	if _, err := client.RemoveDevice(ctx, connect.NewRequest(&pb.RemoveDeviceRequest{DeviceId: cid2})); err != nil {
		t.Fatal(err)
	}
	_ = client2
}
