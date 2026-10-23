package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPollUsesOneInvocationAndDistinguishesFailures(t *testing.T) {
	for _, tt := range []struct {
		name, stdout, stderr string
		alive, fail          bool
	}{
		{name: "live", stdout: "ra-pane-alive\n\x1b[31mscreen\n", alive: true},
		{name: "capture failure", stdout: "ra-pane-alive\n", stderr: "capture failed", alive: true, fail: true},
		{name: "gone during capture", stdout: "ra-pane-alive\n", stderr: "can't find session: gone", alive: true, fail: true},
		{name: "missing session", stderr: "can't find session: gone"},
		{name: "no server", stderr: "no server running on test socket"},
		{name: "missing socket", stderr: "error connecting to test socket (No such file or directory)"},
		{name: "permission", stderr: "error connecting to test socket (Permission denied)", alive: true, fail: true},
		{name: "transient", stderr: "server exited unexpectedly", alive: true, fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			bin, calls := filepath.Join(dir, "tmux"), filepath.Join(dir, "calls")
			t.Setenv("RA_POLL_STDOUT", tt.stdout)
			t.Setenv("RA_POLL_STDERR", tt.stderr)
			t.Setenv("RA_POLL_CALLS", calls)
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RA_POLL_CALLS\"\nprintf '%s' \"$RA_POLL_STDOUT\"\nprintf '%s' \"$RA_POLL_STDERR\" >&2\n[ -z \"$RA_POLL_STDERR\" ]\n"
			if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			c := &Client{Bin: bin, Socket: "test"}
			screen, alive, err := c.Poll(context.Background(), "session")
			if alive != tt.alive || (err != nil) != tt.fail {
				t.Fatalf("alive=%v err=%v", alive, err)
			}
			if screen != strings.TrimPrefix(tt.stdout, "ra-pane-alive\n") {
				t.Fatalf("screen=%q", screen)
			}
			invoked, _ := os.ReadFile(calls)
			if strings.Count(string(invoked), "\n") != 1 || !strings.Contains(string(invoked), "has-session -t =session ; display-message -p ra-pane-alive") || !strings.Contains(string(invoked), "; capture-pane -p -e -t =session:") {
				t.Fatalf("poll invocations: %q", invoked)
			}
		})
	}
}

func TestPollRealSessionAndExactMissingTarget(t *testing.T) {
	c := &Client{Socket: "ra-poll-" + randSuffix()}
	if _, err := c.CheckVersion(); err != nil {
		t.Skip(err)
	}
	defer c.Run("kill-server")
	if err := c.NewSession(NewSessionOptions{Name: "prefix-longer", Argv: []string{"cat"}}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		alive bool
	}{{"prefix-longer", true}, {"prefix", false}} {
		_, alive, err := c.Poll(context.Background(), tt.name)
		if err != nil || alive != tt.alive {
			t.Fatalf("poll %s: alive=%v err=%v", tt.name, alive, err)
		}
	}
	if err := c.KillSession("prefix-longer"); err != nil {
		t.Fatal(err)
	}
	if _, alive, err := c.Poll(context.Background(), "prefix-longer"); err != nil || alive {
		t.Fatalf("poll stopped server: %v %v", alive, err)
	}
}

func TestRunContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&Client{Bin: "must-not-start"}).RunContext(ctx, "capture-pane")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
