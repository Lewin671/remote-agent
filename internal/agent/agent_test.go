package agent

import "testing"

func TestPermissionDecision(t *testing.T) {
	got := string(PermissionDecision(false, "timed out"))
	want := `{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"timed out"},"hookEventName":"PermissionRequest"}}`
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestRuns(t *testing.T) {
	runs := Runs("\x1b[38;5;246m❯\xa0\x1b[7m\x1b[39mP\x1b[0;2mress up\x1b[0m")
	if len(runs) != 3 || runs[0].FG != 246 || runs[1].Text != "P" || !runs[1].Reverse || runs[1].FG != -1 || !runs[2].Dim || runs[2].Reverse {
		t.Fatalf("runs %+v", runs)
	}
	if got := StripANSI("\x1b]8;id=x;https://e.com\x1b\\link\x1b]8;;\x1b\\ \x1b[1mb\x1b[0m"); got != "link b" {
		t.Fatalf("strip %q", got)
	}
}
