package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// The listing of sessions comes a page at a time: every session that has
// not ended with the first page, the ended ones from the latest to the
// earliest, each exactly once.
func TestSessionPaging(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.db.Exec(`INSERT INTO accounts (id, public_key, plan, created_ms) VALUES ('a', x'00', 'owner', 0);
		INSERT INTO machines (id, account_id) VALUES ('m', 'a')`); err != nil {
		t.Fatal(err)
	}
	ts := st.tenant("a")
	put := func(id string, ended int64) {
		t.Helper()
		if _, err := ts.putSession(ctx, "m", id, []byte("key"), 1, []byte("meta"), 1, ended, nil); err != nil {
			t.Fatal(err)
		}
	}
	put("live-a", 0)
	put("live-b", 0)
	// Two sessions ended in the same millisecond: the ID breaks the tie.
	for i := 1; i <= 7; i++ {
		put(fmt.Sprintf("ended-%d", i), int64(1000*(i/2+1)))
	}
	live, err := ts.liveSessions(ctx, &sessionBudget{})
	if err != nil || len(live) != 2 {
		t.Fatalf("live sessions: %v, %v", live, err)
	}
	var got []string
	var after *pb.SessionCursor
	pages := 0
	for {
		page, next, err := ts.endedSessions(ctx, after, 3, &sessionBudget{})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, s := range page {
			if s.EndedMs == 0 {
				t.Fatalf("%s listed as ended", s.SessionId)
			}
			got = append(got, s.SessionId)
		}
		if next == nil {
			break
		}
		after = next
	}
	want := []string{"ended-6", "ended-7", "ended-4", "ended-5", "ended-2", "ended-3", "ended-1"}
	if fmt.Sprint(got) != fmt.Sprint(want) || pages != 3 {
		t.Fatalf("ended sessions in %d pages: %v, want %v", pages, got, want)
	}
	// A full last page has no cursor after it.
	if page, next, _ := ts.endedSessions(ctx, nil, 7, &sessionBudget{}); len(page) != 7 || next != nil {
		t.Fatalf("a page of exactly the rest: %d sessions, next %v", len(page), next)
	}

	// A session that ends leaves the live ones; resumed, it returns. An
	// older revision changes neither.
	if _, err := ts.putSession(ctx, "m", "live-a", nil, 1, []byte("meta"), 2, 9000, nil); err != nil {
		t.Fatal(err)
	}
	if live, _ := ts.liveSessions(ctx, &sessionBudget{}); len(live) != 1 || live[0].SessionId != "live-b" {
		t.Fatalf("live after one ended: %v", live)
	}
	if page, _, _ := ts.endedSessions(ctx, nil, 1, &sessionBudget{}); page[0].SessionId != "live-a" {
		t.Fatalf("latest ended: %v", page[0].SessionId)
	}
	if _, err := ts.putSession(ctx, "m", "live-a", nil, 1, []byte("meta"), 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if live, _ := ts.liveSessions(ctx, &sessionBudget{}); len(live) != 1 {
		t.Fatal("an older revision brought the session back")
	}
	if _, err := ts.putSession(ctx, "m", "live-a", nil, 1, []byte("meta"), 3, 0, nil); err != nil {
		t.Fatal(err)
	}
	if live, _ := ts.liveSessions(ctx, &sessionBudget{}); len(live) != 2 {
		t.Fatal("a resumed session is not live")
	}

	// By ID: those that exist, whatever their state.
	byID, err := ts.sessionsByID(ctx, []string{"ended-1", "nope", "live-b"}, &sessionBudget{})
	if err != nil || len(byID) != 2 || byID[0].SessionId != "ended-1" || byID[0].EndedMs != 1000 {
		t.Fatalf("by ID: %v, %v", byID, err)
	}
	heads, err := ts.sessionHeads(ctx, "m")
	if err != nil || len(heads) != 9 {
		t.Fatalf("heads: %d, %v", len(heads), err)
	}
	for _, h := range heads {
		if h.SessionId == "ended-7" && h.EndedMs != 4000 {
			t.Fatalf("head of ended-7 has ended_ms %d", h.EndedMs)
		}
	}
}

// A later page seeks to its cursor in the index rather than scanning the
// pages before it.
func TestSessionPageSeeks(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.db.Query(`EXPLAIN QUERY PLAN SELECT session_id FROM sessions
		WHERE account_id = ? AND ended_ms > 0 AND ended_ms <= ? AND (ended_ms < ? OR session_id > ?)
		ORDER BY ended_ms DESC, session_id LIMIT ?`, "a", 5, 5, "x", 3)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "SEARCH sessions USING") || !strings.Contains(plan, "sessions_ended (account_id=? AND ended_ms>? AND ended_ms<?)") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("the page query does not seek, or sorts:\n%s", plan)
	}
}
