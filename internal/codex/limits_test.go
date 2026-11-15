package codex

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeRollout(t *testing.T, home, name string, mod time.Time, lines ...string) {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "11", "14")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func limitsLine(ts, limitID string, used int) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"` + limitID + `","primary":{"used_percent":` + strconv.Itoa(used) + `,"window_minutes":10080,"resets_at":1763600000},"plan_type":"prolite"}}}`
}

// Runs remote-agent does not manage (codex exec from another agent) write
// the same rollouts; their account limits count too.
func TestAccountLimitsReadsEveryRollout(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	meta := `{"timestamp":"2026-11-14T08:00:00Z","type":"session_meta","payload":{"originator":"codex_exec","source":"exec"}}`
	writeRollout(t, home, "rollout-a.jsonl", now.Add(-2*time.Minute), meta,
		limitsLine("2026-11-14T08:01:00Z", "codex", 40),
		limitsLine("2026-11-14T08:05:00Z", "codex", 42),
		// Another limit with its own windows is not the account's.
		limitsLine("2026-11-14T08:06:00Z", "base_model_inference", 99))
	writeRollout(t, home, "rollout-b.jsonl", now.Add(-time.Minute), meta,
		limitsLine("2026-11-14T08:03:00Z", "codex", 41))
	writeRollout(t, home, "rollout-old.jsonl", now.Add(-48*time.Hour), meta,
		limitsLine("2026-11-14T09:00:00Z", "codex", 90))
	writeRollout(t, home, "notes.txt", now, limitsLine("2026-11-14T10:00:00Z", "codex", 95))

	a := New("codex", home, "")
	u, at, seen := a.AccountLimits(now.Add(-time.Hour))
	if u == nil {
		t.Fatal("no limits found")
	}
	if want := time.Date(2026, 11, 14, 8, 5, 0, 0, time.UTC); !at.Equal(want) || u.PrimaryLimit.GetUsedPercent() != 42 {
		t.Fatalf("got %v at %v, want the newest account record", u.PrimaryLimit, at)
	}
	if u.PlanType != "prolite" || u.PrimaryLimit.ResetsAt != 1763600000 {
		t.Fatalf("lost fields: %v", u)
	}
	if d := now.Add(-time.Minute).Sub(seen); d < 0 || d > time.Second {
		t.Fatalf("seen = %v, want the newest rollout's modification", seen)
	}

	// Nothing changed since the last poll: nothing is read again.
	if u, _, seen2 := a.AccountLimits(seen); u != nil || !seen2.IsZero() {
		t.Fatalf("re-read unchanged rollouts: %v %v", u, seen2)
	}
}

// A long turn can push the record before the tail; a partial first line is
// skipped rather than misread.
func TestAccountLimitsTailStartsMidLine(t *testing.T) {
	home := t.TempDir()
	pad := `{"timestamp":"2026-11-14T08:00:00Z","type":"response_item","payload":{"text":"` + strings.Repeat("x", limitsTail) + `"}}`
	writeRollout(t, home, "rollout.jsonl", time.Now(), limitsLine("2026-11-14T07:00:00Z", "codex", 10), pad,
		limitsLine("2026-11-14T08:10:00Z", "codex", 12))
	u, at, _ := New("codex", home, "").AccountLimits(time.Time{})
	if u.GetPrimaryLimit().GetUsedPercent() != 12 || !at.Equal(time.Date(2026, 11, 14, 8, 10, 0, 0, time.UTC)) {
		t.Fatalf("got %v at %v", u, at)
	}
}

func TestAccountLimitsWithoutSessions(t *testing.T) {
	if u, _, _ := New("codex", t.TempDir(), "").AccountLimits(time.Time{}); u != nil {
		t.Fatal("limits from nowhere")
	}
	if u, _, _ := New("codex", "", "").AccountLimits(time.Time{}); u != nil {
		t.Fatal("limits without a home")
	}
}
