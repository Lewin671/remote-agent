package codex

import (
	"os"
	"testing"
)

// The captures in testdata/composer are the real input box: empty, with
// two pasted images, with an unrecognized path after them, and emptied
// with Ctrl-U (2026-09-29).
func TestComposerText(t *testing.T) {
	for name, want := range map[string]string{
		"empty":   "",
		"two":     "[Image #1] [Image #2]",
		"bad":     "[Image #1] [Image #2] /tmp/ra-img-lab/imgs/fake.png",
		"cleared": "",
	} {
		b, err := os.ReadFile("testdata/composer/" + name + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		empty, _ := os.ReadFile("testdata/composer/empty.txt")
		got, ok := ComposerText(string(b), AnalyzeScreen(string(empty)).Footer)
		if !ok || got != want {
			t.Errorf("%s: %q %v, want %q", name, got, ok, want)
		}
	}
}

// G2: a draft over several lines is read whole, up to the status line
// seen when the input box was empty, so that no line of it can be taken
// for the status line. Without that line the text is not known.
func TestComposerTextMultiline(t *testing.T) {
	b, err := os.ReadFile("testdata/composer/multiline.txt")
	if err != nil {
		t.Fatal(err)
	}
	const footer = "GPT-6-Luna low · /private/tmp/ra-e2e"
	got, ok := ComposerText(string(b), footer)
	if want := "[Image #1]\nmy draft line two\nline three"; !ok || got != want {
		t.Fatalf("%q %v, want %q", got, ok, want)
	}
	if _, ok := ComposerText(string(b), "some other status"); ok {
		t.Fatal("read without finding the status line")
	}
}

func TestComposerTextResizedFooter(t *testing.T) {
	const wide = "GPT-6.1-Sol medium · ~/Code/co-work-space                   ⚠ 1 warning · f2 to view"
	const narrow = "GPT-6.1-Sol medium · ~/Cod…  ⚠ 1 warning · f2 to view"
	screen := func(body, footer string) string { return "\x1b[1m›\x1b[0m " + body + "\n\n  " + footer + "\n" }
	for _, pair := range [][2]string{{wide, narrow}, {narrow, wide}} {
		got, ok := ComposerText(screen("[Image #1]", pair[1]), pair[0])
		if !ok || got != "[Image #1]" {
			t.Errorf("resized: %q %v", got, ok)
		}
		// A footer-like line in a local draft must not hide the rest of it.
		draft := "[Image #1]\n" + pair[0] + "\nforeign text"
		got, ok = ComposerText(screen(draft, pair[1]), pair[0])
		if !ok || got != draft {
			t.Errorf("local draft lost: %q %v", got, ok)
		}
	}
	// Elision must not swallow unrelated status fields or arbitrary trailing text.
	for _, footer := range []string{"GPT-6.1-Sol medium · ~/Cod…  ⚠ 2 warnings · f2 to view", narrow + "\n  foreign text"} {
		if _, ok := ComposerText(screen("[Image #1]", footer), wide); ok {
			t.Errorf("accepted unrelated footer: %q", footer)
		}
	}
}

// Codex 0.165.0 (2026-11-01): a path taken as text wraps over two lines,
// and the warning badge, under the status line while the input box was
// empty, has moved onto it.
func TestComposerTextWrappedPathMovedBadge(t *testing.T) {
	b, err := os.ReadFile("testdata/composer/path-wrapped-0.165.0.txt")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := os.ReadFile("testdata/screen-0.165.0-idle.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := ComposerText(string(b), AnalyzeScreen(string(empty)).Footer)
	if want := "/Users/someone/.remote-agent/uploads/\n0123456789abcdef/0123456789abcdef0123456789abcdef.jpg"; !ok || got != want {
		t.Fatalf("%q %v, want %q", got, ok, want)
	}
	// A badge on both sides still has to be the same.
	if sameFooter("GPT-6.1 Terra high · ~/x ⚠ 1 warning · f2 to view", "GPT-6.1 Terra high · ~/x ⚠ 2 warnings · f2 to view") {
		t.Fatal("accepted a changed warning count")
	}
	if sameFooter("GPT-6.1 Terra high · ~/x ⚠ 1 warning · f2 to view", "GPT-6.1 Terra low · ~/x") {
		t.Fatal("accepted a changed effort")
	}
}

// Codex's live footer changes with composer state: during a task (also
// transiently during startup), a draft replaces the configured status row
// with the native queue shortcut. The boundary is UI, not draft text.
func TestComposerTextNativeFooter(t *testing.T) {
	const status = "GPT-6.1-Sol medium · ~/code/demo"
	for _, hint := range []string{"tab to queue message", "tab to queue message                         100% context left", "tab to queue"} {
		for _, body := range []string{"[Image #1]", "/tmp/uploads/\nexample.jpg", "my draft\n\n" + hint + "\nmore text"} {
			screen := "\x1b[1m›\x1b[0m " + body + "\n\n  \x1b[1m" + hint[:3] + "\x1b[0m" + hint[3:] + "\n"
			for _, footer := range []string{status, ""} {
				got, ok := ComposerText(screen, footer)
				if !ok || got != body {
					t.Errorf("%q (footer %q): got %q %v, want %q", hint, footer, got, ok, body)
				}
			}
			// A plain draft line with the same words proves no boundary.
			plain := "\x1b[1m›\x1b[0m owned\n\n  " + hint + "\n"
			if _, ok := ComposerText(plain, status); ok {
				t.Errorf("accepted unstyled draft as footer: %q", hint)
			}
			if _, ok := ComposerText(screen+"  foreign text\n", status); ok {
				t.Error("silently dropped text below footer")
			}
		}
	}
}
