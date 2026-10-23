package server

import (
	"context"
	"errors"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestPushQueryCancellationDoesNotHoldAdmissionLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts := newPushAdmissionServer(t)
		conn, err := ts.srv.store.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- ts.srv.push.notify(ctx, ts.tenant(), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "key")
		}()
		synctest.Wait()
		if !ts.srv.push.mu.TryLock() {
			t.Fatal("database wait holds push admission lock")
		}
		ts.srv.push.mu.Unlock()
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("query cancellation: %v", err)
			}
		default:
			t.Fatal("cancellation did not end database wait")
		}
	})
}

func TestPushRechecksAdmissionAfterQuery(t *testing.T) {
	for _, change := range []string{"dedupe", "recent capacity", "delivery capacity"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ts := newPushAdmissionServer(t)
				p := ts.srv.push
				// A target makes delivery-capacity rejection observable; its invalid
				// subscription avoids network calls if a broken implementation admits it.
				if _, err := ts.srv.store.db.Exec(`INSERT INTO push_subs VALUES ('phone', ?, '{}', ?)`, ts.acct(), kindBit(pb.NotifyKind_NOTIFY_KIND_ATTENTION)); err != nil {
					t.Fatal(err)
				}
				conn, err := ts.srv.store.db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				done := make(chan error, 1)
				go func() {
					done <- p.notify(context.Background(), ts.tenant(), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "key")
				}()
				synctest.Wait()
				if !p.mu.TryLock() {
					t.Fatal("query holds mutex")
				}
				key := pushRecentKey(ts.acct(), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, "key")
				switch change {
				case "dedupe":
					p.recent[key] = pushRecent{acct: ts.acct(), at: time.Now().Add(-time.Minute)}
				case "recent capacity":
					for i := range maxAccountPushRecent {
						p.recent[string(rune(i))] = pushRecent{acct: ts.acct(), at: time.Now()}
					}
				case "delivery capacity":
					p.busy[ts.acct()] = maxAccountPushDeliveries
				}
				p.mu.Unlock()
				conn.Close()
				err = <-done
				if change == "dedupe" {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, errPushBusy) {
					t.Fatalf("admission after capacity changed: %v", err)
				}
				synctest.Wait()
				if change == "dedupe" && (len(p.recent) != 1 || time.Since(p.recent[key].at) != time.Minute) {
					t.Fatal("duplicate changed recent keys")
				}
				if change != "dedupe" {
					if _, ok := p.recent[key]; ok {
						t.Fatal("rejected work consumed dedupe key")
					}
				}
				if len(p.deliveries) != 0 {
					t.Fatal("partial reservation")
				}
			})
		})
	}
}

// No HTTP or maintenance goroutines may access the database from outside
// the synctest bubble while it owns database/sql wait channels.
func newPushAdmissionServer(t *testing.T) *testServer {
	st, err := openStore(filepath.Join(t.TempDir(), "push.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := &Server{store: st, ctx: context.Background()}
	srv.push, err = newPusher(srv)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := e2e.Derive(e2e.NewKey())
	return &testServer{srv: srv, keys: keys}
}
