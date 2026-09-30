package agent

import "testing"

// A message of images only is submitted with Enter alone: Claude Code
// 2.1.285 does not submit after an empty paste (it keeps [Image #N] in the
// input box); Codex takes either.
func TestPasteBytesWithoutText(t *testing.T) {
	if got := string(PasteBytes("")); got != "\r" {
		t.Fatalf("%q", got)
	}
	if got := string(PasteBytes("\x01")); got != "\r" {
		t.Fatalf("control characters only: %q", got)
	}
	if got := string(PasteBytes("hi")); got != "\x1b[200~hi\x1b[201~\r" {
		t.Fatalf("%q", got)
	}
}
