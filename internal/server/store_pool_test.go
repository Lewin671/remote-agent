package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func poolTestStore(t *testing.T) (*store, *tstore, []byte) {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "relay ?#%.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO accounts(id,public_key,plan,created_ms) VALUES('account',X'01','owner',1)`,
		`INSERT INTO devices(id,account_id,public_key,kind,registration,signature,created_ms) VALUES('device','account',X'02',2,X'03',X'04',1)`,
		`INSERT INTO machines(id,account_id) VALUES('machine','account')`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	ts := s.tenant("account")
	if _, err := ts.putSession(ctx, "machine", "session", []byte{}, 1, nil, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	env := &pb.Envelope{Ciphertext: []byte("sealed")}
	if _, _, err := ts.appendEvents(ctx, "machine", "session", []*pb.StoredEvent{{Seq: 1, Envelope: env}}, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.putImage(ctx, "machine", "session", "image", raw, time.Hour, 1<<20, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ts.putPrefs(ctx, "prefs", 0, raw, nil); err != nil {
		t.Fatal(err)
	}
	hash := hashToken("test-token")
	if err := ts.addToken(ctx, hash, "device", nowMs()+time.Hour.Milliseconds()); err != nil {
		t.Fatal(err)
	}
	return s, ts, hash
}

func TestStoreReadPoolConnections(t *testing.T) {
	s, _, _ := poolTestStore(t)
	ctx := context.Background()
	if got := s.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer limit = %d", got)
	}
	if got := s.reader.Stats().MaxOpenConnections; got != 4 {
		t.Fatalf("reader limit = %d", got)
	}
	// Hold all four so that settings are checked on every physical connection,
	// including those lazily opened after startup.
	var conns []*sql.Conn
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for range 4 {
		c, err := s.reader.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		for pragma, want := range map[string]string{"journal_mode": "wal", "query_only": "1", "busy_timeout": "5000", "foreign_keys": "1", "synchronous": "2"} {
			var got string
			if err := c.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
				t.Fatalf("%s = %q, %v; want %q", pragma, got, err, want)
			}
		}
		if _, err := c.ExecContext(ctx, `UPDATE accounts SET plan='trial'`); err == nil {
			t.Fatal("query_only allowed a write")
		}
		if _, err := c.ExecContext(ctx, `PRAGMA query_only=OFF`); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ExecContext(ctx, `UPDATE accounts SET plan='trial'`); err == nil {
			t.Fatal("mode=ro allowed a write")
		}
		if _, err := c.ExecContext(ctx, `PRAGMA query_only=ON`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreReadsProceedDuringWrite(t *testing.T) {
	s, ts, hash := poolTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET status=1 WHERE id='account'`); err != nil {
		t.Fatal(err)
	}
	reads := map[string]func() error{
		"authorization": func() error { _, err := s.tokenDevice(ctx, hash); return err },
		"history": func() error {
			evs, _, err := ts.events(ctx, "session", 0, 10)
			if err == nil && len(evs) != 1 {
				return fmt.Errorf("events = %d", len(evs))
			}
			return err
		},
		"images":         func() error { _, _, err := ts.image(ctx, "session", "image"); return err },
		"accounts":       func() error { _, err := s.accounts(ctx); return err },
		"account count":  func() error { _, err := s.accountCount(ctx); return err },
		"usage":          func() error { _, err := ts.usage(ctx); return err },
		"machines":       func() error { _, err := ts.machines(ctx); return err },
		"devices":        func() error { _, err := ts.devices(ctx); return err },
		"preferences":    func() error { _, err := ts.prefs(ctx, "prefs"); return err },
		"live sessions":  func() error { _, err := ts.liveSessions(ctx, &sessionBudget{}); return err },
		"ended sessions": func() error { _, _, err := ts.endedSessions(ctx, nil, 10, &sessionBudget{}); return err },
		"sessions by id": func() error { _, err := ts.sessionsByID(ctx, []string{"session"}, &sessionBudget{}); return err },
		"session heads":  func() error { _, err := ts.sessionHeads(ctx, "machine"); return err },
	}
	done := make(chan error, len(reads))
	for name, read := range reads {
		go func() {
			if err := read(); err != nil {
				done <- fmt.Errorf("%s: %w", name, err)
			} else {
				done <- nil
			}
		}()
	}
	// Every read must finish before the writer releases its connection or
	// commits the suspension. The context is only a failure watchdog.
	for range reads {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

func TestStoreReadPoolRevocationVisibility(t *testing.T) {
	for _, action := range []string{"token", "device", "suspension"} {
		t.Run(action, func(t *testing.T) {
			s, ts, hash := poolTestStore(t)
			ctx := context.Background()
			var held []*sql.Conn
			defer func() {
				for _, c := range held {
					c.Close()
				}
			}()
			// Prime every pooled connection before the write. Completed statements
			// must release their WAL snapshots even though connections remain open.
			for range 4 {
				c, err := s.reader.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				held = append(held, c)
				var n int
				if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM tokens WHERE hash=?`, hash).Scan(&n); err != nil || n != 1 {
					t.Fatalf("prime: %d, %v", n, err)
				}
			}
			switch action {
			case "token":
				if _, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE hash=?`, hash); err != nil {
					t.Fatal(err)
				}
			case "device":
				if _, err := ts.removeDevice(ctx, "device"); err != nil {
					t.Fatal(err)
				}
			case "suspension":
				if err := s.setAccountStatus(ctx, "account", accountSuspended); err != nil {
					t.Fatal(err)
				}
			}
			for i, c := range held {
				c.Close() // This is the only available reader connection.
				_, err := s.tokenDevice(ctx, hash)
				want := errNoRows
				if action == "suspension" {
					want = errSuspended
				}
				if !errors.Is(err, want) {
					t.Fatalf("connection %d: got %v, want %v", i, err, want)
				}
				next, err := s.reader.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				held[i] = next
			}
		})
	}
}

func TestStoreWriteResultsUseWriterSnapshot(t *testing.T) {
	s, ts, _ := poolTestStore(t)
	ctx := context.Background()
	old, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer old.Rollback()
	var n int
	if err := old.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_prefs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	// Give this tenant an intentionally stale reader. Write results and quota
	// checks must still see the writes in the writer's own transaction.
	ts.reader = old
	env := &pb.Envelope{Ciphertext: []byte("new sealed data")}
	raw, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ts.updateMachine(ctx, "machine", raw, 1)
	if err != nil || !proto.Equal(m.GetMeta(), env) {
		t.Fatalf("machine write result: %v, %v", m, err)
	}
	applied, prefs, err := ts.putPrefs(ctx, "new-prefs", 0, raw, func(u accountUsage) error {
		if u.prefs != 2 {
			return fmt.Errorf("quota saw %d prefs", u.prefs)
		}
		return nil
	})
	if err != nil || !applied || !proto.Equal(prefs.GetEnvelope(), env) {
		t.Fatalf("preferences write result: %v, %v", prefs, err)
	}
	session, err := ts.putSession(ctx, "machine", "session", nil, 1, raw, 1, 0, nil)
	if err != nil || !proto.Equal(session.GetMeta(), env) {
		t.Fatalf("session write result: %v, %v", session, err)
	}
	_, session, err = ts.appendEvents(ctx, "machine", "session", []*pb.StoredEvent{{Seq: 2, Envelope: env}}, nil)
	if err != nil || session.GetLastSeq() != 2 {
		t.Fatalf("append write result: %v, %v", session, err)
	}
	stale, err := ts.machines(ctx)
	if err != nil || len(stale) != 1 || stale[0].Meta != nil {
		t.Fatalf("reader snapshot was not stale: %v, %v", stale, err)
	}
	// A new statement after the snapshot ends must observe the committed write.
	if err := old.Rollback(); err != nil {
		t.Fatal(err)
	}
	ts.reader = s.reader
	current, err := ts.prefs(ctx, "new-prefs")
	if err != nil || !bytes.Equal(current.GetEnvelope().GetCiphertext(), env.Ciphertext) {
		t.Fatalf("committed preferences: %v, %v", current, err)
	}
}
