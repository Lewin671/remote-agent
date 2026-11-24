package termhistory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unicode/utf8"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
)

type sharedSgrFile struct {
	About string          `json:"about"`
	Cases []sharedSgrCase `json:"cases"`
}

type sharedSgrCase struct {
	Name string           `json:"name"`
	Line string           `json:"line"`
	Runs []map[string]any `json:"runs"`
}

func runToMap(r *corev1.TerminalHistoryRun) map[string]any {
	m := map[string]any{"text": r.Text}
	switch r.FgKind {
	case corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_PALETTE:
		m["fg"] = float64(r.FgValue)
	case corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_RGB:
		m["fg"] = fmt.Sprintf("#%06x", r.FgValue)
	}
	switch r.BgKind {
	case corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_PALETTE:
		m["bg"] = float64(r.BgValue)
	case corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_RGB:
		m["bg"] = fmt.Sprintf("#%06x", r.BgValue)
	}
	if r.Bold {
		m["bold"] = true
	}
	if r.Dim {
		m["dim"] = true
	}
	if r.Italic {
		m["italic"] = true
	}
	if r.Underline {
		m["underline"] = true
	}
	if r.Inverse {
		m["inverse"] = true
	}
	return m
}

func mergeSameStyleMaps(runs []*corev1.TerminalHistoryRun) []map[string]any {
	out := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		m := runToMap(r)
		if n := len(out); n > 0 && sameStyleExceptText(out[n-1], m) {
			out[n-1]["text"] = out[n-1]["text"].(string) + m["text"].(string)
			continue
		}
		out = append(out, m)
	}
	return out
}

func sameStyleExceptText(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if k == "text" {
			continue
		}
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestSharedHistorySgrCases(t *testing.T) {
	path := filepath.Join("..", "..", "..", "testdata", "terminal", "history-sgr.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shared vectors %s: %v", path, err)
	}
	var sf sharedSgrFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		t.Fatalf("unmarshal shared vectors: %v", err)
	}
	if len(sf.Cases) == 0 {
		t.Fatal("expected non-empty shared SGR cases")
	}
	for _, tc := range sf.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			gotRuns, _ := ParseSgrLine(tc.Line)
			got := mergeSameStyleMaps(gotRuns)
			want := tc.Runs
			if want == nil {
				want = []map[string]any{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("line %q:\nwant %#v\ngot  %#v", tc.Line, want, got)
			}
		})
	}
}

func TestSgrUnifiedEdgeCases(t *testing.T) {
	t.Run("drops DEL 0x7f and C0 controls except tab", func(t *testing.T) {
		runs, cells := ParseSgrLine("a\x7fb\x00c\td")
		if len(runs) != 1 || runs[0].Text != "abc\td" {
			t.Fatalf("got runs %#v", runs)
		}
		// "abc" = 3 cells, '\t' -> 8, "d" -> 9
		if cells != 9 {
			t.Fatalf("got cells %d, want 9", cells)
		}
	})

	t.Run("rejects private CSI ending in m without resetting style", func(t *testing.T) {
		runs, _ := ParseSgrLine("\x1b[31mred\x1b[?4mstill-red")
		if len(runs) != 1 || runs[0].Text != "redstill-red" ||
			runs[0].FgKind != corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_PALETTE ||
			runs[0].FgValue != 1 {
			t.Fatalf("got runs %#v", runs)
		}
	})

	t.Run("extended color out of range does not corrupt color or subsequent codes", func(t *testing.T) {
		runs, _ := ParseSgrLine("\x1b[38;5;999;1ma\x1b[38;2;300;0;0;4mb")
		if len(runs) != 2 {
			t.Fatalf("got %d runs: %#v", len(runs), runs)
		}
		if runs[0].FgKind != corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_DEFAULT || !runs[0].Bold {
			t.Fatalf("run[0] = %#v", runs[0])
		}
		if runs[1].FgKind != corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_DEFAULT || !runs[1].Bold || !runs[1].Underline {
			t.Fatalf("run[1] = %#v", runs[1])
		}
	})

	t.Run("truncated extended color aborts remaining params", func(t *testing.T) {
		runs, _ := ParseSgrLine("\x1b[38;2;1;2mtext")
		if len(runs) != 1 || runs[0].Bold || runs[0].Dim || runs[0].FgKind != corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_DEFAULT {
			t.Fatalf("truncated 38;2 misapplied attributes: %#v", runs)
		}
	})

	t.Run("new ESC interrupts unfinished CSI and OSC", func(t *testing.T) {
		runs, _ := ParseSgrLine("a\x1b[31\x1b[32mgreen\x1b]0;unterminated\x1b[0mplain")
		if len(runs) != 3 {
			t.Fatalf("got runs %#v", runs)
		}
		if runs[0].Text != "a" || runs[0].FgKind != corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_DEFAULT {
			t.Fatalf("run[0] = %#v", runs[0])
		}
		if runs[1].Text != "green" || runs[1].FgValue != 2 {
			t.Fatalf("run[1] = %#v", runs[1])
		}
		if runs[2].Text != "plain" || runs[2].FgKind != corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_DEFAULT {
			t.Fatalf("run[2] = %#v", runs[2])
		}
	})

	t.Run("drops DCS and APC control strings", func(t *testing.T) {
		runs, _ := ParseSgrLine("hi\x1bPqdata\x1b\\!\x1b_apc\x07end")
		if len(runs) != 1 || runs[0].Text != "hi!end" {
			t.Fatalf("got runs %#v", runs)
		}
	})

	t.Run("ParsePage computes max_cells without per-line cells field", func(t *testing.T) {
		page := ParsePage("p1", []string{"a", "\x1b[31m中文\x1b[0m", "ab中文cd中文ef中文"})
		if page.Id != "p1" || len(page.Lines) != 3 {
			t.Fatalf("unexpected page: %#v", page)
		}
		if page.MaxCells != 18 {
			t.Fatalf("got maxCells %d, want 18", page.MaxCells)
		}
	})
}

func TestWidthAndClusterRuns(t *testing.T) {
	type wantRun struct {
		text  string
		width corev1.TerminalRunWidth
	}
	const (
		narrow        = corev1.TerminalRunWidth_TERMINAL_RUN_WIDTH_NARROW
		wide          = corev1.TerminalRunWidth_TERMINAL_RUN_WIDTH_WIDE
		clusterNarrow = corev1.TerminalRunWidth_TERMINAL_RUN_WIDTH_CLUSTER_NARROW
		clusterWide   = corev1.TerminalRunWidth_TERMINAL_RUN_WIDTH_CLUSTER_WIDE
		zero          = corev1.TerminalRunWidth_TERMINAL_RUN_WIDTH_ZERO
	)
	cases := []struct {
		name      string
		line      string
		wantCells int32
		wantRuns  []wantRun
	}{
		{
			name:      "mixed ASCII and CJK splits narrow and wide runs",
			line:      "中a文b",
			wantCells: 6,
			wantRuns: []wantRun{
				{"中", wide}, {"a", narrow}, {"文", wide}, {"b", narrow},
			},
		},
		{
			name:      "common U+2600-27BF and U+2B50 emojis are 2 cells wide",
			line:      "✅❌⭐⚡✨",
			wantCells: 10,
			wantRuns:  []wantRun{{"✅❌⭐⚡✨", wide}},
		},
		{
			name:      "combining mark stays attached to narrow and wide bases",
			line:      "e\u0301中\u0301x",
			wantCells: 4,
			wantRuns: []wantRun{
				{"e\u0301", clusterNarrow},
				{"中\u0301", clusterWide},
				{"x", narrow},
			},
		},
		{
			name:      "VS16 promotes text symbol to 2-cell wide cluster",
			line:      "☀\ufe0f⚠\ufe0f",
			wantCells: 4,
			wantRuns: []wantRun{
				{"☀\ufe0f", clusterWide},
				{"⚠\ufe0f", clusterWide},
			},
		},
		{
			name:      "ZWJ family and profession emojis are single 2-cell clusters",
			line:      "👨\u200d👩\u200d👧 👩\u200d💻",
			wantCells: 5,
			wantRuns: []wantRun{
				{"👨\u200d👩\u200d👧", clusterWide},
				{" ", narrow},
				{"👩\u200d💻", clusterWide},
			},
		},
		{
			name:      "skin tone modifier, flag pair, and keycap stay 2-cell clusters",
			line:      "👍🏽🇨🇳1\ufe0f\u20e3",
			wantCells: 6,
			wantRuns: []wantRun{
				{"👍🏽", clusterWide},
				{"🇨🇳", clusterWide},
				{"1\ufe0f\u20e3", clusterWide},
			},
		},
		{
			name:      "standalone zero-width characters take 0 cells",
			line:      "a\u200bb",
			wantCells: 2,
			wantRuns: []wantRun{
				{"a", narrow},
				{"\u200b", zero},
				{"b", narrow},
			},
		},
		{
			name:      "fullwidth forms are wide",
			line:      "ＡＢ",
			wantCells: 4,
			wantRuns:  []wantRun{{"ＡＢ", wide}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runs, cells := ParseSgrLine(tc.line)
			if cells != tc.wantCells {
				t.Fatalf("cells = %d, want %d (runs=%#v)", cells, tc.wantCells, runs)
			}
			got := make([]wantRun, len(runs))
			for i, r := range runs {
				got[i] = wantRun{text: r.Text, width: r.Width}
			}
			if !reflect.DeepEqual(got, tc.wantRuns) {
				t.Fatalf("runs:\nwant %#v\ngot  %#v", tc.wantRuns, got)
			}
		})
	}
}

func TestSgrColonUnderlineColorAndEscUtf8(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []map[string]any
	}{
		{
			name: "colon subparameter preserves sibling bold and red",
			line: "\x1b[1;4:3;31mhi\x1b[0m",
			want: []map[string]any{{"text": "hi", "fg": float64(1), "bold": true}},
		},
		{
			name: "colon underline color does not affect following green",
			line: "\x1b[58:2::0:1:7;32mok",
			want: []map[string]any{{"text": "ok", "fg": float64(2)}},
		},
		{
			name: "58;2;0;1;7 does not reset or enable bold/dim/inverse",
			line: "\x1b[34;58;2;0;1;7mblue\x1b[59m!",
			want: []map[string]any{{"text": "blue!", "fg": float64(4)}},
		},
		{
			name: "58;5;7 does not leak inverse",
			line: "\x1b[33;58;5;7;4myellow",
			want: []map[string]any{{"text": "yellow", "fg": float64(3), "underline": true}},
		},
		{
			name: "truncated 58;2 aborts remaining params without leaking",
			line: "\x1b[31;58;2;1;7mred",
			want: []map[string]any{{"text": "red", "fg": float64(1)}},
		},
		{
			name: "non-rendered single codes 5/8/9/21/53 ignored cleanly",
			line: "\x1b[5;8;9;21;53;36mcyan",
			want: []map[string]any{{"text": "cyan", "fg": float64(6)}},
		},
		{
			name: "ESC followed by multibyte UTF-8 skips whole rune without invalid UTF-8",
			line: "a\x1b中b\x1b😀c",
			want: []map[string]any{{"text": "abc"}},
		},
		{
			name: "double ESC before CSI still parses SGR",
			line: "a\x1b\x1b[31mred",
			want: []map[string]any{{"text": "a"}, {"text": "red", "fg": float64(1)}},
		},
		{
			name: "ESC intermediate charset designation skipped whole",
			line: "x\x1b(By\x1b#8z",
			want: []map[string]any{{"text": "xyz"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runs, _ := ParseSgrLine(tc.line)
			got := make([]map[string]any, len(runs))
			for i, r := range runs {
				if !utf8.ValidString(r.Text) {
					t.Fatalf("run[%d].Text is not valid UTF-8: %x", i, r.Text)
				}
				got[i] = runToMap(r)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("line %q:\nwant %#v\ngot  %#v", tc.line, tc.want, got)
			}
		})
	}
}
