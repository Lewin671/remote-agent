package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"sync"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

func TestRevokedDeviceRegistrationCannotBeReplayed(t *testing.T) {
	ts := newTestServer(t)
	ap, aid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	admin := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, ap, aid)))
	ctx := context.Background()
	for _, kind := range []pb.DeviceKind{pb.DeviceKind_DEVICE_KIND_CLIENT, pb.DeviceKind_DEVICE_KIND_MACHINE} {
		pub, priv, _ := ed25519.GenerateKey(nil)
		raw, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: pub, Kind: kind})
		request := &pb.RegisterDeviceRequest{Registration: raw, Signature: ts.keys.SignRegistration(raw)}
		registered, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(request))
		if err != nil {
			t.Fatal(err)
		}
		id := registered.Msg.DeviceId
		oldToken := ts.login(t, priv, id)
		// Legitimate registration retries remain idempotent until revocation.
		if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(request)); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.RemoveDevice(ctx, connect.NewRequest(&pb.RemoveDeviceRequest{DeviceId: id})); err != nil {
			t.Fatal(err)
		}
		if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(request)); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("revoked %v was resurrected by signed registration replay: %v", kind, err)
		}
		if _, err := ts.srv.authenticate(ctx, oldToken); err == nil {
			t.Fatal("revoked login still works")
		}
		challenge, err := ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ts.auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{DeviceId: id, Challenge: challenge.Msg.Challenge, Signature: e2e.SignLogin(priv, challenge.Msg.Challenge)})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("revoked device logged in: %v", err)
		}
	}
}

func TestRevocationSurvivesStoreReopen(t *testing.T) {
	ts := newTestServer(t)
	_, id, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	reg, err := ts.tenant().registration(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.tenant().removeDevice(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// A new connection to the same on-disk database models a server restart.
	st, err := openStore(ts.srv.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	registration := &pb.DeviceRegistration{}
	if err := proto.Unmarshal(reg.raw, registration); err != nil {
		t.Fatal(err)
	}
	if err := st.addDevice(context.Background(), ts.acct(), nil, id, registration, reg.raw, reg.sig, nil); err != errDeviceRevoked {
		t.Fatalf("persistent revocation lost: %v", err)
	}
}

func TestConcurrentRegistrationCannotUndoRemoval(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	for range 20 {
		_, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
		if err != nil {
			t.Fatal(err)
		}
		reg, err := ts.tenant().registration(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		parsed := &pb.DeviceRegistration{}
		if err := proto.Unmarshal(reg.raw, parsed); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 8 {
			wg.Go(func() {
				<-start
				err := ts.srv.store.addDevice(ctx, ts.acct(), nil, id, parsed, reg.raw, reg.sig, nil)
				if err != nil && err != errDeviceRevoked {
					t.Errorf("register: %v", err)
				}
			})
		}
		wg.Go(func() {
			<-start
			if _, err := ts.tenant().removeDevice(ctx, id); err != nil {
				t.Errorf("remove: %v", err)
			}
		})
		close(start)
		wg.Wait()
		if _, err := ts.srv.store.device(ctx, id); err != errNoRows {
			t.Fatalf("registration raced past removal: %v", err)
		}
		if _, err := ts.tenant().machine(ctx, id); err != errNoRows {
			t.Fatalf("registration restored machine routing: %v", err)
		}
	}
}

// A PutSession that passed authentication before its machine was removed
// must not bring the machine's sessions back.
func TestRemovedMachineCannotPutSession(t *testing.T) {
	ts, mid, _, machine, client, _ := newV2Env(t)
	ctx := context.Background()
	putTestSession(t, ts, machine, mid, "s1")
	if _, err := client.RemoveDevice(ctx, connect.NewRequest(&pb.RemoveDeviceRequest{DeviceId: mid})); err != nil {
		t.Fatal(err)
	}
	// The request is past the interceptor; only the store can refuse it.
	if _, err := ts.tenant().putSession(ctx, mid, "s1", []byte("key"), []byte("meta"), 1, 0, nil); !errors.Is(err, errNoMachine) {
		t.Fatalf("put session for a removed machine: %v", err)
	}
	var sessions int
	if err := ts.srv.store.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("%d sessions of a removed machine stored, %v", sessions, err)
	}
	svc := &machineService{s: ts.srv}
	key, _ := e2e.Seal(ts.keys.Wrap, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: mid, SessionId: "s2"}, e2e.NewKey())
	_, err := svc.PutSession(context.WithValue(ctx, ctxKey{}, &device{id: mid, account: ts.acct(), kind: pb.DeviceKind_DEVICE_KIND_MACHINE}), connect.NewRequest(&pb.PutSessionRequest{SessionId: "s2", Key: key}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("put session for a removed machine: %v", err)
	}
}
