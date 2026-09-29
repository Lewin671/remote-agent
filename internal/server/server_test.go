package server

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

type testServer struct {
	url  string
	keys *e2e.Keys
	auth remoteagentv1connect.AuthServiceClient
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	s, err := New(Config{DBPath: filepath.Join(t.TempDir(), "ra.db"), SetupToken: "setup", AllowedOrigins: []string{"http://localhost:5173"}})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { hs.Close(); s.Close() })
	keys, _ := e2e.Derive(e2e.NewKey())
	return &testServer{url: hs.URL, keys: keys, auth: remoteagentv1connect.NewAuthServiceClient(http.DefaultClient, hs.URL)}
}

func (ts *testServer) register(t *testing.T, keys *e2e.Keys, kind pb.DeviceKind, token string) (ed25519.PrivateKey, string, error) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	reg, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: keys.AccountPublicKey(), DevicePublicKey: pub, Kind: kind})
	res, err := ts.auth.RegisterDevice(context.Background(), connect.NewRequest(&pb.RegisterDeviceRequest{
		Registration: reg, Signature: keys.SignRegistration(reg), SetupToken: token,
	}))
	if err != nil {
		return nil, "", err
	}
	return priv, res.Msg.DeviceId, nil
}

func (ts *testServer) login(t *testing.T, priv ed25519.PrivateKey, id string) string {
	t.Helper()
	ctx := context.Background()
	ch, err := ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id}))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ts.auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{DeviceId: id, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(priv, ch.Msg.Challenge)}))
	if err != nil {
		t.Fatal(err)
	}
	// A challenge works once.
	if _, err := ts.auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{DeviceId: id, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(priv, ch.Msg.Challenge)})); err == nil {
		t.Fatal("challenge reused")
	}
	return res.Msg.Token
}

func withToken(token string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}))
}

func TestRegistration(t *testing.T) {
	ts := newTestServer(t)
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, ""); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("no setup token: %v", err)
	}
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup"); err != nil {
		t.Fatal(err)
	}
	// Later devices need the account key, not the token.
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, ""); err != nil {
		t.Fatal(err)
	}
	other, _ := e2e.Derive(e2e.NewKey())
	if _, _, err := ts.register(t, other, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("second account: %v", err)
	}
	// A registration signed by someone else is refused.
	pub, _, _ := ed25519.GenerateKey(nil)
	reg, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: pub, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT})
	_, err := ts.auth.RegisterDevice(context.Background(), connect.NewRequest(&pb.RegisterDeviceRequest{Registration: reg, Signature: other.SignRegistration(reg)}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("forged registration: %v", err)
	}
}

func TestLoginAndAuthorization(t *testing.T) {
	ts := newTestServer(t)
	mpriv, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	cpriv, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	ctx := context.Background()

	// Wrong key.
	ch, _ := ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: mid}))
	if _, err := ts.auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{DeviceId: mid, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(cpriv, ch.Msg.Challenge)})); err == nil {
		t.Fatal("login with another device's key")
	}

	anon := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url)
	if _, err := anon.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous: %v", err)
	}
	client := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cpriv, cid)))
	if _, err := client.ListSessionHeads(ctx, connect.NewRequest(&pb.ListSessionHeadsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("client calling machine API: %v", err)
	}
	machine := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, mpriv, mid)))
	if _, err := machine.ListSessionHeads(ctx, connect.NewRequest(&pb.ListSessionHeadsRequest{})); err != nil {
		t.Fatal(err)
	}
}

func TestUploadRules(t *testing.T) {
	ts := newTestServer(t)
	priv, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	priv2, mid2, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "")
	m := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, priv, mid)))
	m2 := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, priv2, mid2)))
	ctx := context.Background()
	skey := e2e.NewKey()
	ev := func(machine string, seq uint64) *pb.StoredEvent {
		env, _ := e2e.Seal(skey, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: machine, SessionId: "s", Seq: seq}, []byte("x"))
		return &pb.StoredEvent{Seq: seq, Envelope: env}
	}
	upload := func(c remoteagentv1connect.MachineServiceClient, evs ...*pb.StoredEvent) (uint64, error) {
		res, err := c.UploadEvents(ctx, connect.NewRequest(&pb.UploadEventsRequest{SessionId: "s", Events: evs}))
		if err != nil {
			return 0, err
		}
		return res.Msg.LastSeq, nil
	}
	if _, err := upload(m, ev(mid, 1)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
	keyEnv, _ := e2e.Seal(ts.keys.Wrap, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: mid, SessionId: "s"}, skey)
	if _, err := m.PutSession(ctx, connect.NewRequest(&pb.PutSessionRequest{SessionId: "s"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("session without key: %v", err)
	}
	if _, err := m.PutSession(ctx, connect.NewRequest(&pb.PutSessionRequest{SessionId: "s", Key: keyEnv})); err != nil {
		t.Fatal(err)
	}
	if last, err := upload(m, ev(mid, 1), ev(mid, 2)); err != nil || last != 2 {
		t.Fatalf("upload: %d %v", last, err)
	}
	// Retries are skipped, gaps rejected.
	if last, err := upload(m, ev(mid, 2), ev(mid, 3)); err != nil || last != 3 {
		t.Fatalf("overlapping upload: %d %v", last, err)
	}
	if _, err := upload(m, ev(mid, 5)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("gap: %v", err)
	}
	// A header naming another place is rejected.
	moved := ev(mid, 4)
	moved.Seq = 9
	if _, err := upload(m, moved); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("mismatched seq: %v", err)
	}
	// Another machine cannot write to the session.
	if _, err := upload(m2, ev(mid2, 4)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("other machine: %v", err)
	}
	keyEnv2, _ := e2e.Seal(ts.keys.Wrap, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: mid2, SessionId: "s"}, skey)
	if _, err := m2.PutSession(ctx, connect.NewRequest(&pb.PutSessionRequest{SessionId: "s", Key: keyEnv2})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("other machine put: %v", err)
	}
}

func TestCORS(t *testing.T) {
	ts := newTestServer(t)
	for origin, want := range map[string]string{"http://localhost:5173": "http://localhost:5173", "https://evil.example": ""} {
		req, _ := http.NewRequest(http.MethodOptions, ts.url+"/remoteagent.v1.ClientService/ListMachines", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("origin %s: allow-origin %q", origin, got)
		}
	}
}

func TestChallengesAreBounded(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	for _, id := range []string{"", "short", string(make([]byte, 32)), strings.Repeat("x", 1<<16)} {
		if _, err := ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("device ID %.10q: %v", id, err)
		}
	}
	id := strings.Repeat("ab", 16)
	var err error
	for i := 0; i <= maxChallenges && err == nil; i++ {
		_, err = ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id}))
	}
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("after %d challenges: %v", maxChallenges, err)
	}
}
