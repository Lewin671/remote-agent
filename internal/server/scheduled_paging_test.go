package server

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestTaskOwnedPagingExistingLayout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = st.db.Exec(`INSERT INTO accounts(id,public_key,plan,created_ms) VALUES('a',x'00','owner',0); INSERT INTO machines(id,account_id) VALUES('m','a'); DROP TABLE task_owned_sessions;`)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	// A layout-6 database without the optional set starts with no manual migration.
	st, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var version int
	st.db.QueryRow(`PRAGMA user_version`).Scan(&version)
	if version != schemaVersion {
		t.Fatal("layout changed")
	}
	ts := st.tenant("a")
	put := func(id string, rev uint64, ended int64, owned bool) {
		t.Helper()
		if _, err := ts.putSession(ctx, "m", id, []byte("key"), 1, []byte("meta"), rev, ended, owned, nil); err != nil {
			t.Fatal(err)
		}
	}
	put("ordinary-live", 1, 0, false)
	put("ordinary-ended", 1, 1, false)
	for i := 0; i < 120; i++ {
		ended := int64(i)
		if i%2 == 0 {
			ended = 0
		}
		put(fmt.Sprintf("task-%03d", i), 1, ended, true)
	}
	live, err := ts.liveSessions(ctx, &sessionBudget{})
	if err != nil || len(live) != 1 || live[0].SessionId != "ordinary-live" {
		t.Fatalf("live: %v %v", live, err)
	}
	page, cursor, err := ts.endedSessions(ctx, nil, 1, &sessionBudget{})
	if err != nil || len(page) != 1 || page[0].SessionId != "ordinary-ended" || cursor != nil {
		t.Fatalf("ended: %v %v %v", page, cursor, err)
	}
	ids, err := ts.sessionsByID(ctx, []string{"task-001", "task-002"}, &sessionBudget{})
	if err != nil || len(ids) != 2 {
		t.Fatalf("by ID: %v %v", ids, err)
	}
	heads, err := ts.sessionHeads(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	for _, head := range heads {
		if head.SessionId == "task-001" && !head.TaskOwned {
			t.Fatal("head lost ownership")
		}
	}
	put("task-001", 2, 20, false) // user takeover
	put("task-001", 1, 20, true)  // stale metadata cannot hide it again
	page, _, err = ts.endedSessions(ctx, nil, 1, &sessionBudget{})
	if err != nil || len(page) != 1 || page[0].SessionId != "task-001" {
		t.Fatalf("takeover: %v %v", page, err)
	}
	// Missing metadata cannot update routing independently from the encrypted state.
	if _, err := ts.putSession(ctx, "m", "task-001", nil, 1, nil, 3, 20, true, nil); err != nil {
		t.Fatal(err)
	}
	page, _, _ = ts.endedSessions(ctx, nil, 1, &sessionBudget{})
	if len(page) != 1 || page[0].SessionId != "task-001" {
		t.Fatal("routing changed without metadata")
	}
}

func TestScheduledPushKind(t *testing.T) {
	if !validNotifyKinds([]pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_SCHEDULED}) || pushText[pb.NotifyKind_NOTIFY_KIND_SCHEDULED] != "定时任务有新结果" {
		t.Fatal("scheduled push is not accepted or has non-generic text")
	}
}
