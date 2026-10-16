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
