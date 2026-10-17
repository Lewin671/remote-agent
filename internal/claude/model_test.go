package claude

import (
	"os"
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/internal/agent"
)

// testdata/model/model-2.1.300.txt is a real capture of /model on Claude
// Code 2.1.300. Its effort line reads "◐ Medium effort (default) ←/→ to
// adjust", ←/→ step one level along low … max and stop at the ends, and
// the rows come after "Select model" (2026-10-17).
func TestModelMenu2_1_300(t *testing.T) {
	b, err := os.ReadFile("testdata/model/model-2.1.300.txt")
	if err != nil {
		t.Fatal(err)
	}
	plain := plainText(string(b))
	if got := shownEffort(plain); got != "medium" {
		t.Fatalf("effort line read as %q", got)
	}
	rows := rowsAfter(plain, "Select model")
	if !strings.Contains(normalize(plain), menuHint) {
		t.Fatal("the menu hint was not read")
	}
	// The number keys cannot be used: the label carries it twice as
	// "/model" would print it, so only the alias and its version matter.
	for _, c := range []struct {
		name string
		want string
	}{
		{"Sonnet", "Sonnet 5.5"}, // newest of Sonnet 5.5 and Sonnet 5
		{"Haiku", "Haiku 5.5"},   // newest of Haiku 5.5 and Haiku 4.5
	} {
		i, err := modelRow(rows, c.name)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if rows[i].label != c.want {
			t.Errorf("%s: chose %q, want %q", c.name, rows[i].label, c.want)
		}
	}
	cur := -1
	for i, r := range rows {
		if r.cursor {
			cur = i
		}
	}
	if cur < 0 || !strings.HasPrefix(rows[cur].label, "Haiku 5.5") {
		t.Fatalf("cursor row: %+v", rows)
	}
}

// On 2.1.300 the effort levels stop at the ends instead of cycling, so an
// effort below the one showing is reached with ←, not with → around the
// far end. Setting low from medium must press ← once (regression: the
// older code only ever pressed → and timed out).
func TestSetModelEffortBelowMedium(t *testing.T) {
	a := New("claude", "")
	f := &fakeClaude{noWrap: true, models: []string{"Default (recommended)", "Opus 5.5", "Fable 5.1", "Sonnet 5.5", "Haiku 5.5 ✔"}}
	f.cursor, f.effort = 4, 1 // on Haiku 5.5, medium
	if err := a.SetModel(f, "sonnet", "low"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.writes, "|"); got != "/model|\r|"+agent.KeyUp+"|"+agent.KeyLeft+"|s" {
		t.Fatalf("keys %q", f.writes)
	}
}
