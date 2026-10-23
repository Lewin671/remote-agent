package server

import (
	"context"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestLoginWritesRollbackTogether(t *testing.T) {
	ts := newTestServer(t)
	_, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st := ts.tenant()
	for i := range maxDeviceTokens {
		if err := st.addToken(ctx, []byte{byte(i)}, id, nowMs()+int64(i+1)*10000); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.db.Exec(`CREATE TRIGGER fail_activity BEFORE UPDATE OF last_active_ms ON accounts BEGIN SELECT RAISE(ABORT, 'activity failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.addToken(ctx, []byte("new"), id, nowMs()+3600000); err == nil {
		t.Fatal("login ignored activity write failure")
	}
	var n, old int
	if err := st.db.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE hash = ?) FROM tokens`, []byte{0}).Scan(&n, &old); err != nil {
		t.Fatal(err)
	}
	if n != maxDeviceTokens || old != 1 {
		t.Fatalf("failed login changed tokens: n=%d oldest=%d", n, old)
	}
}

func TestLoginLeavesGlobalExpiryToMaintenance(t *testing.T) {
	ts := newTestServer(t)
	_, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st := ts.tenant()
	expired := []byte("expired-other-device")
	if _, err := st.db.Exec(`INSERT INTO tokens VALUES (?, ?, ?, ?)`, expired, ts.acct(), id, nowMs()-1000); err != nil {
		t.Fatal(err)
	}
	if err := st.addToken(ctx, []byte("valid"), "other-device", nowMs()+time.Hour.Milliseconds()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE hash = ?`, expired).Scan(&n); err != nil || n != 1 {
		t.Fatalf("login did global cleanup: %d, %v", n, err)
	}
	if _, err := ts.srv.store.tokenDevice(ctx, expired); err != errNoRows {
		t.Fatalf("expired token accepted: %v", err)
	}
	if err := ts.srv.store.expireTokens(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE hash = ?`, expired).Scan(&n); err != nil || n != 0 {
		t.Fatalf("maintenance did not remove token: %d, %v", n, err)
	}
}

func TestMachineActivityWritesRollbackTogether(t *testing.T) {
	ts := newTestServer(t)
	_, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.tenant().db.Exec(`CREATE TRIGGER fail_activity BEFORE UPDATE OF last_active_ms ON accounts BEGIN SELECT RAISE(ABORT, 'activity failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.tenant().updateMachine(context.Background(), id, []byte("new metadata"), 1); err == nil {
		t.Fatal("update ignored activity failure")
	}
	var rev, seen int64
	if err := ts.tenant().db.QueryRow(`SELECT meta_revision, last_seen_ms FROM machines WHERE id = ?`, id).Scan(&rev, &seen); err != nil {
		t.Fatal(err)
	}
	if rev != 0 || seen != 0 {
		t.Fatalf("partial machine update: revision=%d seen=%d", rev, seen)
	}
}
