package claude

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/internal/agent"
)

// The captures in testdata/composer are the real input box: empty, with
// two pasted images, with an unrecognized path after them, and emptied
// with Ctrl-U (2026-09-29).
func TestComposerText(t *testing.T) {
	for name, want := range map[string]string{
		"empty":   "",
		"two":     "[Image #1] [Image #2]",
		"bad":     "[Image #1] [Image #2]/tmp/ra-img-lab/imgs/fake.png",
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
		text, indexes, ready := New("", "").ImageInput(string(b), "")
		if !ready || text != want || !slices.Equal(indexes, agent.ImagePlaceholders(want)) {
			t.Errorf("%s: ImageInput = %q, %v, %v", name, text, indexes, ready)
		}
	}
}

// Composer-only captures from Claude Code 2.1.322 at 44 columns. The
// fourth placeholder wraps between "Image" and "#11"; trimming screen
// rows loses the space there. No conversation or machine details are kept.
func TestImageInputCurrentCaptures(t *testing.T) {
	for _, tc := range []struct {
		name string
		want []int32
	}{
		{"2.1.322-one", []int32{14}},
		{"2.1.322-wrapped", []int32{8, 9, 10, 11}},
		{"2.1.322-five", []int32{14, 15, 16, 17, 18}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := os.ReadFile("testdata/composer/" + tc.name + ".txt")
			if err != nil {
				t.Fatal(err)
			}
			text, indexes, ok := New("", "").ImageInput(string(b), "")
			rest, all := agent.StripPlaceholders(text, tc.want)
			if !ok || !slices.Equal(indexes, tc.want) || !all || strings.TrimSpace(rest) != "" {
				t.Fatalf("ImageInput = %q, %v, %v", text, indexes, ok)
			}
		})
	}
}

func TestImageInputWrapPositions(t *testing.T) {
	const label = "[Image #123]"
	const rule = "────────────────────"
	a := New("", "")
	for split := 1; split < len(label); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			// ComposerText trims both the end of a screen row and the
			// indentation on the continuation row.
			styled := rule + "\n❯ " + label[:split] + "\n  " + label[split:] + "\n" + rule
			text, indexes, ok := a.ImageInput(styled, "")
			if !ok || text != label || !slices.Equal(indexes, []int32{123}) {
				t.Fatalf("ImageInput = %q, %v, %v", text, indexes, ok)
			}
		})
	}
	for _, draft := range []string{"[Image#123]", "[Image #123] foreign\n  draft"} {
		text, indexes, ok := a.ImageInput(rule+"\n❯ "+draft+"\n"+rule, "")
		rest, _ := agent.StripPlaceholders(text, indexes)
		if !ok || strings.TrimSpace(rest) == "" {
			t.Fatalf("foreign draft was treated as owned images: %q", text)
		}
	}
}
