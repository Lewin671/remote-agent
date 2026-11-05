package antigravity

import (
	"fmt"
	"github.com/Lewin671/remote-agent/internal/agent"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readScreenFixture(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("testdata", "screen", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("failed to read screen fixture %s: %v", name, err)
	}
	return string(b)
}

func TestAnalyzeScreens(t *testing.T) {
	a := New("", "")

	t.Run("idle-empty-box", func(t *testing.T) {
		styled := readScreenFixture(t, "idle-empty-box.txt")
		scr := a.Analyze(styled)
		if !scr.ComposerReady {
			t.Errorf("expected ComposerReady=true")
		}
		if !scr.DraftEmpty {
			t.Errorf("expected DraftEmpty=true")
		}
		if scr.Busy {
			t.Errorf("expected Busy=false")
		}
		if scr.Popup != "" {
			t.Errorf("expected Popup='', got %q", scr.Popup)
		}
	})

	t.Run("draft-oneline", func(t *testing.T) {
		styled := readScreenFixture(t, "draft-oneline.txt")
		scr := a.Analyze(styled)
		if !scr.ComposerReady {
			t.Errorf("expected ComposerReady=true")
		}
		if scr.DraftEmpty {
			t.Errorf("expected DraftEmpty=false")
		}
		text, ok := a.ComposerText(styled, "")
		if !ok || !strings.Contains(text, "this is a one line draft") {
			t.Errorf("expected ComposerText to contain draft, got %q (ok=%v)", text, ok)
		}
	})

	t.Run("multiline-draft", func(t *testing.T) {
		styled := readScreenFixture(t, "multiline-draft.txt")
		scr := a.Analyze(styled)
		if !scr.ComposerReady {
			t.Errorf("expected ComposerReady=true")
		}
		if scr.DraftEmpty {
			t.Errorf("expected DraftEmpty=false")
		}
		text, ok := a.ComposerText(styled, "")
		if !ok || !strings.Contains(text, "line 2 of paste") {
			t.Errorf("expected ComposerText to contain lines, got %q (ok=%v)", text, ok)
		}
	})

	t.Run("busy-spinner", func(t *testing.T) {
		styled := readScreenFixture(t, "busy-spinner.txt")
		scr := a.Analyze(styled)
		if !scr.Busy {
			t.Errorf("expected Busy=true")
		}
	})

	t.Run("permission-prompt", func(t *testing.T) {
		styled := readScreenFixture(t, "permission-prompt.txt")
		scr := a.Analyze(styled)
		if !scr.PermissionOpen {
			t.Errorf("expected PermissionOpen=true")
		}
		if scr.Popup != "permission" {
			t.Errorf("expected Popup='permission', got %q", scr.Popup)
		}
	})

	t.Run("ask-question-single", func(t *testing.T) {
		styled := readScreenFixture(t, "ask-question-single.txt")
		scr := a.Analyze(styled)
		if !scr.QuestionOpen {
			t.Errorf("expected QuestionOpen=true")
		}
		if scr.Popup != "question" {
			t.Errorf("expected Popup='question', got %q", scr.Popup)
		}
	})

	t.Run("ask-question-multi", func(t *testing.T) {
		styled := readScreenFixture(t, "ask-question-multi.txt")
		scr := a.Analyze(styled)
		if !scr.QuestionOpen {
			t.Errorf("expected QuestionOpen=true")
		}
		if scr.Popup != "question" {
			t.Errorf("expected Popup='question', got %q", scr.Popup)
		}
	})

	t.Run("artifact-review-screen", func(t *testing.T) {
		styled := readScreenFixture(t, "artifact-review-screen.txt")
		scr := a.Analyze(styled)
		if scr.PlanOpen {
			t.Errorf("empty artifact list must not be a plan")
		}
		if scr.Popup != "artifacts" {
			t.Errorf("expected artifacts popup, got %q", scr.Popup)
		}
	})

	t.Run("trust-dialog", func(t *testing.T) {
		styled := readScreenFixture(t, "trust-dialog.txt")
		scr := a.Analyze(styled)
		if scr.Popup != "trust folder" {
			t.Errorf("expected Popup='trust folder', got %q", scr.Popup)
		}
	})

	t.Run("model-picker", func(t *testing.T) {
		styled := readScreenFixture(t, "model-picker.txt")
		scr := a.Analyze(styled)
		if scr.Popup != "model picker" {
			t.Errorf("expected Popup='model picker', got %q", scr.Popup)
		}
	})

	t.Run("screen-1.4.12-idle", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join("testdata", "screen-1.4.12-idle.txt"))
		if err != nil {
			t.Fatalf("read idle fixture: %v", err)
		}
		scr := a.Analyze(string(b))
		if !scr.ComposerReady {
			t.Errorf("expected ComposerReady=true")
		}
		if !scr.DraftEmpty {
			t.Errorf("expected DraftEmpty=true")
		}
	})
}

func TestAuditMenusAndBackgroundInterrupt(t *testing.T) {
	a := New("", "")
	screen := readScreenFixture(t, "mcp-terminal.txt")
	for _, title := range []string{"MCP Servers", "Settings", "Unknown picker"} {
		s := strings.ReplaceAll(screen, "MCP Servers", title)
		scr := a.Analyze(s)
		if scr.ComposerReady || scr.DraftEmpty || scr.Popup != title {
			t.Fatalf("%s accepted: %+v", title, scr)
		}
		if _, ok := a.ComposerText(s, ""); ok {
			t.Fatal("menu exposed composer")
		}
	}
	screen = readScreenFixture(t, "interrupt-background-terminal.txt")
	if scr := a.Analyze(screen); !scr.ComposerReady || !scr.DraftEmpty || scr.Busy || !a.Interrupted(screen) {
		t.Fatalf("foreground cancellation lost: %+v", scr)
	}
	screen = strings.ReplaceAll(screen, "Interrupted · What should Antigravity CLI do instead?", "A later reply")
	if a.Interrupted(screen) {
		t.Fatal("non-cancellation ended turn")
	}
}

func TestEveryScreenSampleClassifiesPanels(t *testing.T) {
	files, err := filepath.Glob("testdata/screen/*.txt")
	if err != nil {
		t.Fatal(err)
	}
	a := New("", "")
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			s := a.Analyze(string(b))
			if s.Popup != "" && (s.ComposerReady || s.DraftEmpty) {
				t.Fatalf("panel permits input: %+v", s)
			}
			if s.ComposerReady {
				if _, ok := a.ComposerText(string(b), ""); !ok {
					t.Fatal("ready composer unreadable")
				}
			}
		})
	}
}

func TestAttachedMediaIsPartOfDraft(t *testing.T) {
	a := New("", "")
	screen := readScreenFixture(t, "image-terminal.txt")
	for _, n := range []int{1, 2} {
		s := strings.Replace(screen, "1 media attached", fmt.Sprintf("%d media attached", n), 1)
		scr := a.Analyze(s)
		text, indexes, ok := a.ImageInput(s, "")
		if !scr.ComposerReady || scr.DraftEmpty || !ok || len(indexes) != n || len(agent.ImagePlaceholders(text)) != n {
			t.Fatalf("attached media ignored: %+v %q %v", scr, text, indexes)
		}
	}
	expanded := readScreenFixture(t, "image-expanded-1.5.0.txt")
	if text, indexes, ok := a.ImageInput(expanded, ""); !ok || text != "[Image #1]" || len(indexes) != 1 || a.Analyze(expanded).DraftEmpty {
		t.Fatal("expanded attachments ignored")
	}
	for _, s := range []string{readScreenFixture(t, "image-cleared-1.5.0.txt"), strings.Replace(screen, "▸ 📎 1 media attached (file, 663 B, image/jpeg)  (ctrl+o to expand)", "", 1)} {
		if text, indexes, ok := a.ImageInput(s, ""); !ok || text != "" || len(indexes) != 0 || !a.Analyze(s).DraftEmpty {
			t.Fatal("empty box not verified")
		}
	}
	if keys := a.ClearImageInput(1, true); len(keys) != 0 {
		t.Fatal("clearing media would interrupt foreground turn")
	}
	if keys := a.ClearImageInput(1, false); string(keys) != "\x05\x15\x1b" {
		t.Fatalf("wrong native media clear %q", keys)
	}
}

func TestChatPaperclipIsNotComposerMedia(t *testing.T) {
	a := New("", "")
	base := readScreenFixture(t, "image-terminal.txt")
	for _, line := range []string{"The file is attached 📎", "There are 2 media attached", "(ctrl+o to collapse)"} {
		s := strings.Replace(base, "▸ 📎 1 media attached (file, 663 B, image/jpeg)  (ctrl+o to expand)", line, 1)
		scr := a.Analyze(s)
		text, indexes, ok := a.ImageInput(s, "")
		if !scr.ComposerReady || !scr.DraftEmpty || !ok || text != "" || len(indexes) != 0 {
			t.Fatalf("chat line %q became composer media: %+v %q %v", line, scr, text, indexes)
		}
	}
}

func TestClippedMediaHeaders(t *testing.T) {
	a := New("", "")
	base := readScreenFixture(t, "image-terminal.txt")
	full := "▸ 📎 1 media attached (file, 663 B, image/jpeg)  (ctrl+o to expand)"
	for _, row := range []string{
		"▸ 📎 1 media attached (file, 103 B, image/png)", // 48 columns, agy 1.5.1
		"▸ 📎 1 media attached (file, 1",                 // 32 columns
		"▸ 📎 1 media attached",                          // summary clipped away
		"▸ 📎 2 media attached  (ctrl+o",                 // 32 columns, two images
		"\x1b[90m▸ 📎 2 media attached  (ctrl+o\x1b[39m",
	} {
		t.Run(row, func(t *testing.T) {
			screen := strings.Replace(base, full, row, 1)
			want := 1
			if strings.Contains(row, "2 media") {
				want = 2
			}
			text, indexes, ok := a.ImageInput(screen, "")
			if !ok || len(indexes) != want || len(agent.ImagePlaceholders(text)) != want || a.Analyze(screen).DraftEmpty {
				t.Fatalf("clipped media ignored: %q %v %v", text, indexes, ok)
			}
			// The same header in history, separated from the input box,
			// must never be borrowed as an attachment of a new message.
			history := row + "\n" + strings.Replace(base, full, "", 1)
			if text, indexes, ok := a.ImageInput(history, ""); !ok || text != "" || len(indexes) != 0 || !a.Analyze(history).DraftEmpty {
				t.Fatal("historical media became current attachments")
			}
		})
	}
}
