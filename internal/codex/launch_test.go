package codex

import (
	"strings"
	"testing"
)

func TestArgs(t *testing.T) {
	args := Args([]string{"-m", "gpt-6-sol"})
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		`hooks.PermissionRequest=[{matcher="*",hooks=[{type="command",command="\"$RA_EXE\" hook codex",timeout=3600}]}]`,
		`hooks.SessionStart=[{hooks=[{type="command",command="\"$RA_EXE\" hook codex",timeout=30}]}]`,
		"check_for_update_on_startup=false",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in\n%s", want, joined)
		}
	}
	if args[len(args)-2] != "-m" || args[len(args)-1] != "gpt-6-sol" {
		t.Errorf("user args not last: %v", args)
	}
}

func TestPasteBytes(t *testing.T) {
	got := string(PasteBytes("line 1\r\nline\x1b[201~ 2\x03\t好"))
	want := "\x1b[200~line 1\nline[201~ 2\t好\x1b[201~\r"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
