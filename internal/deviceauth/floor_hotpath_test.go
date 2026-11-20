package deviceauth

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

func TestReadOnlyAcceptAuthenticatedSkipsSecurityWritesAcrossFloorInterval(t *testing.T) {
	c := newCurrentChain(t)
	dbPath := filepath.Join(t.TempDir(), "endpoint.db")
	m := c.machineEndpoint(t, dbPath)
	defer m.Close()
	cl := c.clientEndpoint(t)
	defer cl.Close()

	if _, err := m.db.Exec(`CREATE TABLE security_writes (ms INTEGER); CREATE TRIGGER count_security_writes AFTER UPDATE ON security BEGIN INSERT INTO security_writes VALUES(NEW.floor_ms); END`); err != nil {
		t.Fatal(err)
	}

	now := uint64(time.Now().UnixMilli())
	if err := m.Touch(now); err != nil {
		t.Fatal(err)
	}
	var writes int
	if err := m.db.QueryRow(`SELECT count(*) FROM security_writes`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("baseline touch writes = %d, want 1", writes)
	}

	body, err := proto.Marshal(&pb.Command{Kind: &pb.Command_GetLiveSnapshot{GetLiveSnapshot: &pb.GetLiveSnapshotRequest{SessionId: "session-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i, offset := range []uint64{500, 1000, 1500, 2500, 5000} {
		at := now + offset
		w, err := cl.Prepare(devicesec.Header{
			Purpose:  devicesec.Command,
			Kind:     5,
			Machine:  c.machine.ID,
			Session:  "session-1",
			Request:  fmt.Sprintf("readonly-floor-%04d", i),
			Deadline: at + 60_000,
		}, nil, body)
		if err != nil {
			t.Fatal(err)
		}
		auth, err := m.Authenticate(w, nil, at)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.AcceptAuthenticated(ctx, auth, at, AcceptOptions{}, func(devicesec.Header, []byte) (Completion, error) {
			return Final([]byte("ok")), nil
		}); err != nil {
			t.Fatal(err)
		}
		if got := m.Floor(); got != at {
			t.Fatalf("step %d: in-memory Floor() = %d, want %d", i, got, at)
		}
	}

	if err := m.db.QueryRow(`SELECT count(*) FROM security_writes`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("read-only AcceptAuthenticated wrote security table (%d total writes, want 1)", writes)
	}

	// Background flush persists the advanced floor without holding e.mu across fsync.
	if err := m.flushFloor(); err != nil {
		t.Fatal(err)
	}
	if err := m.db.QueryRow(`SELECT count(*) FROM security_writes`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatalf("after flushFloor writes = %d, want 2", writes)
	}

	// A mutating command past floorWriteInterval coalesces the security update into persistAccepted.
	writeAt := now + 7000
	w, err := cl.Prepare(devicesec.Header{
		Purpose:  devicesec.Command,
		Kind:     5,
		Machine:  c.machine.ID,
		Request:  "write-floor-coalesce-01",
		Deadline: writeAt + 60_000,
	}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	auth, err := m.Authenticate(w, nil, writeAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AcceptAuthenticated(ctx, auth, writeAt, AcceptOptions{}, func(devicesec.Header, []byte) (Completion, error) {
		return Final(nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.db.QueryRow(`SELECT count(*) FROM security_writes`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 3 {
		t.Fatalf("after write command writes = %d, want 3", writes)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenEndpoint(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Floor() != writeAt {
		t.Fatalf("reopened Floor() = %d, want %d", reopened.Floor(), writeAt)
	}
}
