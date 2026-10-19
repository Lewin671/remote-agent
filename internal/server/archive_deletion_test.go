package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

func TestArchiveDeletionRequiresRemovedMachine(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	_, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.putSession(ctx, mid, "retained", []byte("key"), []byte("meta"), 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.tenant().putImage(ctx, mid, "retained", "image", []byte("sealed"), time.Hour, 1000, 0); err != nil {
		t.Fatal(err)
	}
	phone := ts.authority(t)
	admin := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, phone.phoneEd, phone.phoneID)))
	remove := func() error {
		_, err := admin.DeleteMachineRecords(ctx, connect.NewRequest(&pb.DeleteMachineRecordsRequest{MachineId: mid}))
		return err
	}
	if err := remove(); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("current member archive deletion = %v, want failed_precondition", err)
	}
	// A rejected request must leave registration and every retained row usable.
	for _, table := range []string{"machines", "sessions", "images"} {
		var count int
		if err := ts.srv.store.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE account_id = ?", ts.acct()).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	if _, err := ts.putSession(ctx, mid, "retained", []byte("key"), []byte("next"), 2, 0, nil); err != nil {
		t.Fatalf("upload after rejected deletion: %v", err)
	}
	// The signed member list remains authoritative even without a registration row.
	if _, err := ts.srv.store.db.Exec("DELETE FROM devices WHERE id = ?", mid); err != nil {
		t.Fatal(err)
	}
	if err := remove(); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("signed current member without registration deleted: %v", err)
	}
	if err := ts.removeViaState(t, mid); err != nil {
		t.Fatal(err)
	}
	if err := remove(); err != nil {
		t.Fatalf("removed archive: %v", err)
	}
	for _, table := range []string{"machines", "sessions", "images"} {
		var count int
		if err := ts.srv.store.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE account_id = ?", ts.acct()).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}
