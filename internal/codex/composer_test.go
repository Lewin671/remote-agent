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
