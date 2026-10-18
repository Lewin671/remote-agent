package server

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/proto"
)

func TestRegistrationOlderThanFloorIsRefusedForUnknownDevices(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	make := func(created int64) (*pb.RegisterDeviceRequest, ed25519.PrivateKey, string) {
		pub, priv, _ := ed25519.GenerateKey(nil)
		raw, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: pub, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: created})
		return &pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: raw, Signature: ts.keys.SignRegistration(raw), SetupToken: "setup"}, priv, e2e.DeviceID(pub)
	}
	old := time.Now().Add(-time.Hour).UnixMilli()
	known, knownPriv, knownID := make(old)
	if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(known)); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.srv.store.db.Exec(`UPDATE accounts SET registration_floor_ms = ?`, time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	ts.rememberKey(knownID, knownPriv)
	for _, created := range []int64{old, 0} {
		stale, _, _ := make(created)
		ts.admitRegistration(t, stale)
		if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(stale)); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("registration created at %d passed the floor: %v", created, err)
		}
	}
	// A device already registered may retry, and still logs in.
	if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(known)); err != nil {
		t.Fatalf("known registration rejected: %v", err)
	}
	ts.login(t, knownPriv, knownID)
	fresh, freshPriv, freshID := make(time.Now().UnixMilli())
	ts.admitRegistration(t, fresh)
	if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(fresh)); err != nil {
		t.Fatalf("fresh registration rejected: %v", err)
	}
	ts.login(t, freshPriv, freshID)
}
