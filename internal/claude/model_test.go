package claude

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/Lewin671/remote-agent/internal/agent"
)

// testdata/model/model-2.1.300.txt is a real capture of /model on Claude
// Code 2.1.300. Its effort line reads "◐ Medium effort (default) ←/→ to
// adjust", ←/→ step one level along low … max and stop at the ends, and
// the rows come after "Select model" (2026-10-17). Its last row carries
// the "↓" scroll marker, which must not drop that row.
func TestModelMenu2_1_300(t *testing.T) {
	plain := readPlain(t, "testdata/model/model-2.1.300.txt")
	if got := shownEffort(plain); got != "medium" {
		t.Fatalf("effort line read as %q", got)
	}
	if !strings.Contains(normalize(plain), menuHint) {
		t.Fatal("the menu hint was not read")
	}
	rows := rowsAfter(plain, "Select model")
	// Every numbered row is read, including the "↓ 10." row.
	if len(rows) != 10 {
		t.Fatalf("read %d rows, want 10: %+v", len(rows), rows)
	}
	for i, r := range rows {
		if r.num != i+1 {
			t.Errorf("row %d has number %d", i, r.num)
		}
	}
	if rows[9].label != "Opus 4.8" {
		t.Errorf("last row is %q, want Opus 4.8", rows[9].label)
	}
	// The menu lists the catalog's models in their order under "Default
	// (recommended)": each row is found by the name the catalog gives the
	// model, and one the window does not reach is looked up by the place
	// the catalog gives it.
	cat := testCatalog(t)
	for i, m := range cat.Models[:9] {
		got, err := modelRow(cat, rows, m)
		if err != nil || got.num != i+2 || got.label != m.Name {
			t.Errorf("%s: %+v, %v", m.ID, got, err)
		}
	}
	scrolled := cat.Models[9] // Opus 4.7, below the drawn rows
	if got, err := modelRow(cat, rows, scrolled); err != nil || got.num != 11 || got.label != "Opus 4.7" {
		t.Errorf("scrolled row: %+v, %v", got, err)
	}
	if cur := cursorRow(rows); cur < 0 || !strings.HasPrefix(rows[cur].label, "Haiku 5.5") {
		t.Fatalf("cursor row: %+v", rows)
	}
}

// testdata/model/model-2.1.300-narrow.txt is a real capture of the same
// menu in a 90x20 window (2026-10-17). It marks the first and last visible
// rows "↑" and "↓"; all three rows must be read, with the cursor on the
// unmarked middle one.
func TestModelMenuNarrow2_1_300(t *testing.T) {
	plain := readPlain(t, "testdata/model/model-2.1.300-narrow.txt")
	for _, text := range []string{
		"↑ 3.  Fable 5.1              For your toughest challenges",
		"❯ 4.  Sonnet 5.5             Most efficient for simpler tasks",
		"↓ 5.  Haiku 5.5 ✔            Fastest for quick answers",
	} {
		if !strings.Contains(plain, text) {
			t.Fatalf("%q not in the capture", text)
		}
	}
	rows := rowsAfter(plain, "Select model")
	if len(rows) != 3 || rows[0].num != 3 || rows[1].num != 4 || rows[2].num != 5 {
		t.Fatalf("read %+v, want rows 3, 4, 5", rows)
	}
	if cur := cursorRow(rows); cur != 1 || rows[cur].label != "Sonnet 5.5" {
		t.Fatalf("cursor row %d in %+v, want the middle Sonnet 5.5", cur, rows)
	}
}

// A cursor on a row that also carries a scroll marker is still found: the
// mark may sit before or after the cursor. Claude Code 2.1.300 does not
// draw a marker on the cursor row (the narrow capture above shows this),
// so these lines are derived from that capture's shape; a version or
// window size that does would otherwise make SetModel report "cannot find
// the cursor".
func TestMenuRowScrollMarks(t *testing.T) {
	for _, c := range []struct {
		line   string
		num    int
		label  string
		detail string
		cursor bool
	}{
		{"  ↑ 3.  Fable 5.1              For your toughest challenges", 3, "Fable 5.1", "For your toughest challenges", false},
		{"  ❯ 4.  Sonnet 5.5             Most efficient for simpler tasks", 4, "Sonnet 5.5", "Most efficient for simpler tasks", true},
		{"  ↓ 5.  Haiku 5.5 ✔            Fastest for quick answers", 5, "Haiku 5.5", "Fastest for quick answers", false},
		{"  ❯ ↓ 10. Opus 4.8               Best for everyday, complex tasks", 10, "Opus 4.8", "Best for everyday, complex tasks", true},
		{"  ↓ ❯ 10. Opus 4.8               Best for everyday, complex tasks", 10, "Opus 4.8", "Best for everyday, complex tasks", true},
		{"  ↑ ❯ 1. Default (recommended)  Opus 5.5 · Best for everyday, complex tasks", 1, "Default (recommended)", "Opus 5.5 · Best for everyday, complex tasks", true},
	} {
		m := menuRow.FindStringSubmatch(c.line)
		if m == nil {
			t.Errorf("%q did not match", c.line)
			continue
		}
		num, _ := strconv.Atoi(m[2])
		got := row{num: num, label: strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[3]), "✔")), detail: m[4], cursor: strings.Contains(m[1], "❯")}
		want := row{num: c.num, label: c.label, detail: c.detail, cursor: c.cursor}
		if got != want {
			t.Errorf("%q: %+v, want %+v", c.line, got, want)
		}
	}
}

func cursorRow(rows []row) int {
	at := -1
	for i, r := range rows {
		if r.cursor {
			at = i
		}
	}
	return at
}

func readPlain(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return plainText(string(b))
}

// On 2.1.300 the effort levels stop at the ends instead of cycling, so an
// effort below the one showing is reached with ←, not with → around the
// far end. Setting low from medium must press ← once (regression: the
// older code only ever pressed → and timed out).
func TestSetModelEffortBelowMedium(t *testing.T) {
	synctest.Test(t, testSetModelEffortBelowMedium)
}

func testSetModelEffortBelowMedium(t *testing.T) {
	a, cat := testAdapter(t)
	f := &fakeClaude{noWrap: true, cat: cat, models: menuOf(cat)}
	f.cursor, f.effort = 4, 1 // on Haiku 5.5, medium
	if err := a.SetModel(f, "claude-sonnet-5-5", "low"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.writes, "|"); got != "/model|\r|"+agent.KeyUp+"|"+agent.KeyLeft+"|s" {
		t.Fatalf("keys %q", f.writes)
	}
}

// The cursor is found when its row carries a marker: SetModel must not
// report "cannot find the cursor". The fake terminal draws a "↓" on the
// cursor row as a version that marks it would.
func TestSetModelCursorOnMarkedRow(t *testing.T) {
	synctest.Test(t, testSetModelCursorOnMarkedRow)
}

func testSetModelCursorOnMarkedRow(t *testing.T) {
	a, cat := testAdapter(t)
	f := &fakeClaude{cursorMark: "↓ ", cat: cat, models: menuOf(cat)}
	f.cursor, f.effort = 4, 1
	if err := a.SetModel(f, "claude-sonnet-5-5", "low"); err != nil {
		t.Fatal(err)
	}
	if f.menu != nil || !strings.Contains(f.note, "Set model to Sonnet 5.5") {
		t.Fatalf("keys %q, note %q", f.writes, f.note)
	}
}

// A narrow window scrolls the list: a row below the cursor that is not
// drawn is still reached, by its number, and the label is checked once the
// cursor has moved (2.1.300 wraps the list at the ends, so the steps are
// counted exactly).
func TestSetModelScrollsToRow(t *testing.T) {
	synctest.Test(t, testSetModelScrollsToRow)
}

func testSetModelScrollsToRow(t *testing.T) {
	a, cat := testAdapter(t)
	f := &fakeClaude{cat: cat, models: menuOf(cat), window: 3}
	f.cursor, f.effort = 4, 1 // Haiku 5.5, medium
	if err := a.SetModel(f, "claude-sonnet-4-6", "high"); err != nil {
		t.Fatal(err)
	}
	if f.cursor != 12 {
		t.Fatalf("cursor on row %d, want the last one", f.cursor+1)
	}
	if f.menu != nil || !strings.Contains(f.note, "Set model to Sonnet 4.6") {
		t.Fatalf("keys %q, note %q", f.writes, f.note)
	}
}

// modelRow finds a model by its menu label; one scrolled out of the window
// is looked up by the place the catalog gives it.
func TestModelRow(t *testing.T) {
	cat := testCatalog(t)
	rows := []row{{num: 3, label: "Fable 5.1"}, {num: 4, label: "Sonnet 5.5", cursor: true}}
	got, err := modelRow(cat, rows, cat.ByID("claude-sonnet-5-5"))
	if err != nil || got.num != 4 || got.label != "Sonnet 5.5" {
		t.Fatalf("visible row: %+v, %v", got, err)
	}
	got, err = modelRow(cat, rows, cat.ByID("claude-sonnet-4-6"))
	if err != nil || got.num != 13 || got.label != "Sonnet 4.6" {
		t.Fatalf("scrolled row: %+v, %v", got, err)
	}
}
