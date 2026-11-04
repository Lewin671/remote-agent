package antigravity

import (
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
