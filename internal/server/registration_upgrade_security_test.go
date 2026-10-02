package server

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

func TestLegacyRemovedRegistrationCannotReplayAfterUpgrade(t *testing.T) {
	keys, _ := e2e.Derive(e2e.NewKey())
	makeRegistration := func(created int64) (*pb.RegisterDeviceRequest, ed25519.PrivateKey, string) {
		pub, priv, _ := ed25519.GenerateKey(nil)
		raw, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: keys.AccountPublicKey(), DevicePublicKey: pub, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: created})
		return &pb.RegisterDeviceRequest{Registration: raw, Signature: keys.SignRegistration(raw)}, priv, e2e.DeviceID(pub)
	}
	oldTime := time.Now().Add(-time.Hour).UnixMilli()
	removed, _, _ := makeRegistration(oldTime)
	active, activePriv, activeID := makeRegistration(oldTime)
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Actual pre-tombstone schema. Only the active device remains: old
	// RemoveDevice deleted all evidence of the removed device's ID.
	_, err = db.Exec(`CREATE TABLE account(id INTEGER PRIMARY KEY, public_key BLOB NOT NULL, created_ms INTEGER NOT NULL);
 CREATE TABLE devices(id TEXT PRIMARY KEY, public_key BLOB NOT NULL, kind INTEGER NOT NULL, registration BLOB NOT NULL, signature BLOB NOT NULL, created_ms INTEGER NOT NULL);`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account VALUES (1, ?, ?)`, keys.AccountPublicKey(), oldTime); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO devices VALUES (?, ?, ?, ?, ?, ?)`, activeID, activePriv.Public().(ed25519.PublicKey), int32(pb.DeviceKind_DEVICE_KIND_CLIENT), active.Registration, active.Signature, oldTime); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	srv, err := New(Config{DBPath: path})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer srv.Close()
	defer hs.Close()
	ts := &testServer{srv: srv, url: hs.URL, keys: keys, auth: remoteagentv1connect.NewAuthServiceClient(http.DefaultClient, hs.URL)}
	ctx := context.Background()
	if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(removed)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("legacy removed device resurrected: %v", err)
	}
	// Existing old registration retries and login remain compatible.
	if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(active)); err != nil {
		t.Fatalf("active old registration rejected: %v", err)
	}
	ts.login(t, activePriv, activeID)
	// A current client can pair and create a freshly signed device key.
	fresh, freshPriv, freshID := makeRegistration(time.Now().UnixMilli())
	if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(fresh)); err != nil {
		t.Fatalf("fresh new registration rejected: %v", err)
	}
	ts.login(t, freshPriv, freshID)
	var floor int64
	if err := srv.store.db.QueryRow(`SELECT freshness_floor_ms FROM registration_policy`).Scan(&floor); err != nil {
		t.Fatal(err)
	}
	if floor <= oldTime {
		t.Fatalf("upgrade did not establish freshness floor: %d", floor)
	}
	reopened, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var persisted int64
	if err := reopened.db.QueryRow(`SELECT freshness_floor_ms FROM registration_policy`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != floor {
		t.Fatalf("restart changed freshness floor: %d != %d", persisted, floor)
	}
	// Missing/slow device clocks are refused for unknown keys, while the
	// original active row above still works despite its old timestamp.
	zeroTime, _, _ := makeRegistration(0)
	if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(zeroTime)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("missing timestamp bypassed floor: %v", err)
	}
}

func TestFreshDatabaseDoesNotNeedHistoricalRegistrationFloor(t *testing.T) {
	ts := newTestServer(t)
	var floor int64
	if err := ts.srv.store.db.QueryRow(`SELECT freshness_floor_ms FROM registration_policy`).Scan(&floor); err != nil {
		t.Fatal(err)
	}
	if floor != 0 {
		t.Fatalf("fresh database imposed upgrade floor: %d", floor)
	}
}
