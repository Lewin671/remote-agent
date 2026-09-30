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
		got, ok := ComposerText(string(b))
		if !ok || got != want {
			t.Errorf("%s: %q %v, want %q", name, got, ok, want)
		}
	}
}
