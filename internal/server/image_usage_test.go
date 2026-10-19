package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func usageFixture(t *testing.T, s *store, acct string) *tstore {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO accounts (id, public_key, plan, created_ms) VALUES (?1, CAST(?1 AS BLOB), 'owner', 1)`,
		`INSERT INTO devices (id, account_id, public_key, kind, registration, signature, created_ms) VALUES (?1, ?1, x'', 1, x'', x'', 1)`,
		`INSERT INTO machines (id, account_id) VALUES (?1, ?1)`,
		`INSERT INTO sessions (account_id, session_id, machine_id, key, updated_ms) VALUES (?1, 's', ?1, x'', 1)`,
	} {
		if _, err := s.db.Exec(q, acct); err != nil {
			t.Fatal(err)
		}
	}
	return s.tenant(acct)
}

func assertImageUsage(t *testing.T, st *tstore, want int64) {
	t.Helper()
	u, err := st.usage(context.Background())
	if err != nil || u.imageBytes != want {
		t.Fatalf("image usage = %d, want %d: %v", u.imageBytes, want, err)
	}
}

func TestImageUsageLifecycle(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, b := usageFixture(t, s, "a"), usageFixture(t, s, "b")
	ctx := context.Background()
	put := func(st *tstore, id, content string, retention time.Duration, quota int64) error {
		_, err := st.putImage(ctx, st.acct, "s", id, []byte(content), retention, quota, 0)
		return err
	}
	for _, st := range []*tstore{a, b} {
		if err := put(st, "one", "123", time.Hour, 10); err != nil {
			t.Fatal(err)
		}
		assertImageUsage(t, st, 3)
	}
	if err := put(a, "one", "different retry", time.Hour, 10); err != nil {
		t.Fatal(err)
	}
	assertImageUsage(t, a, 3)
	if err := put(a, "too-large", "12345678", time.Hour, 10); !errors.Is(err, errQuota) {
		t.Fatalf("over quota: %v", err)
	}
	assertImageUsage(t, a, 3)
	if err := put(a, "expired", "12345", -time.Hour, 10); err != nil {
		t.Fatal(err)
	}
	// Expiry affects usage immediately, before envelope cleanup.
	assertImageUsage(t, a, 3)
	if n, err := s.expireImages(ctx); err != nil || n != 1 {
		t.Fatalf("expire images: %d, %v", n, err)
	}
	assertImageUsage(t, a, 3)
	if err := put(a, "expired", "12345", time.Hour, 10); err != nil {
		t.Fatal(err)
	}
	assertImageUsage(t, a, 3) // expired IDs cannot be revived
	if _, err := s.db.Exec(`DELETE FROM images WHERE account_id = 'a' AND image_id = 'one'`); err != nil {
		t.Fatal(err)
	}
	assertImageUsage(t, a, 0)
	if err := put(a, "two", "1234567890", time.Hour, 10); err != nil {
		t.Fatal(err)
	}
	assertImageUsage(t, a, 10)
	if _, err := a.removeDevice(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	// Revoking a machine keeps its archive; deleting the records is separate.
	assertImageUsage(t, a, 10)
	if err := a.deleteMachineRecords(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	assertImageUsage(t, a, 0)
	var sessions int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE account_id = 'a'`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("machine's sessions remain: %d, %v", sessions, err)
	}
	assertImageUsage(t, b, 3)
	if err := s.deleteAccount(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	var images int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM images WHERE account_id = 'b'`).Scan(&images); err != nil || images != 0 {
		t.Fatalf("deleted account's images remain: %d, %v", images, err)
	}
	// Reusing the account ID starts with no usage from the deleted account.
	assertImageUsage(t, usageFixture(t, s, "b"), 0)
}

func TestImageUsageIndexOnExistingStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	st := usageFixture(t, s, "a")
	const size = 256 << 10
	if _, err := st.putImage(context.Background(), "a", "s", "image", make([]byte, size), time.Hour, size, 0); err != nil {
		t.Fatal(err)
	}
	// Reproduce the pre-fix layout. Startup must add the index without a
	// data/layout migration, and must tolerate an index created online.
	if _, err := s.db.Exec(`DROP INDEX images_account_usage`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s, err = openStore(path)
		if err != nil {
			t.Fatal(err)
		}
		assertImageUsage(t, s.tenant("a"), size)
		assertImageUsageIndex(t, s)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func assertImageUsageIndex(t *testing.T, s *store) {
	t.Helper()
	var id, parent, unused int
	var detail string
	if err := s.db.QueryRow(`EXPLAIN QUERY PLAN `+imageUsageSQL, "a", nowMs()).Scan(&id, &parent, &unused, &detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "USING COVERING INDEX images_account_usage") {
		t.Fatalf("image aggregate is not index-only: %s", detail)
	}
	// Check bytecode too: a partial/expression index on a WITHOUT ROWID
	// table can still look up every primary-key row, even when called covering.
	var root int
	if err := s.db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name = 'images_account_usage'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`EXPLAIN `+imageUsageSQL, "a", nowMs())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var addr, p1, p2, p3, p5 int
		var op string
		var p4, comment any
		if err := rows.Scan(&addr, &op, &p1, &p2, &p3, &p4, &p5, &comment); err != nil {
			t.Fatal(err)
		}
		if op == "OpenRead" && p2 != root {
			t.Fatalf("aggregate opens a table outside its covering index: root %d", p2)
		}
		if op == "NotFound" || op == "DeferredSeek" {
			t.Fatalf("aggregate still looks up image rows: %s", op)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendEventsDoesNotReadImages(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st := usageFixture(t, s, "a")
	// A real append, including its quota check, must work with no images
	// table at all. This fails if any event-batch statement touches it.
	if _, err := s.db.Exec(`DROP TABLE images`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var used int64
	calls := 0
	limit := func(n int64) error {
		calls++
		if n != used {
			return fmt.Errorf("quota sees %d bytes, want %d", n, used)
		}
		return nil
	}
	for seq := uint64(1); seq <= 2; seq++ {
		e := &pb.StoredEvent{Seq: seq, Envelope: &pb.Envelope{Ciphertext: []byte("event")}}
		used += int64(proto.Size(e.Envelope))
		if _, _, err := st.appendEvents(ctx, "a", "s", []*pb.StoredEvent{e}, limit); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.appendEvents(ctx, "a", "s", []*pb.StoredEvent{e}, limit); err != nil {
			t.Fatalf("retry: %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("quota called %d times including duplicate uploads", calls)
	}
	rejected := errors.New("quota rejected")
	if _, _, err := st.appendEvents(ctx, "a", "s", []*pb.StoredEvent{{Seq: 3, Envelope: &pb.Envelope{Ciphertext: []byte("reject")}}}, func(int64) error { return rejected }); !errors.Is(err, rejected) {
		t.Fatalf("over quota: %v", err)
	}
	var stored int64
	var events, last int
	if err := s.db.QueryRow(`SELECT event_bytes, (SELECT COUNT(*) FROM events), (SELECT last_seq FROM sessions WHERE account_id = 'a') FROM accounts WHERE id = 'a'`).Scan(&stored, &events, &last); err != nil || stored != used || events != 2 || last != 2 {
		t.Fatalf("quota rollback: bytes=%d events=%d last=%d: %v", stored, events, last, err)
	}
}
