package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/tmux"
)

// Exercise the private control pipe with every byte, including NUL, newline,
// quotes and tmux command separators. No input may enter a subprocess argv.
func TestTerminalInputUsesPrivateControlPipe(t *testing.T) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	wrapper := filepath.Join(dir, "tmux")
	t.Setenv("RA_TEST_TMUX_CALLS", calls)
	t.Setenv("RA_TEST_TMUX_BIN", bin)
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RA_TEST_TMUX_CALLS\"\nexec \"$RA_TEST_TMUX_BIN\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	tc := &tmux.Client{Bin: wrapper, Socket: fmt.Sprintf("ra-input-%d-%d", os.Getpid(), time.Now().UnixNano())}
	if _, err := tc.CheckVersion(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _, _ = tc.Run("kill-server") })
	output := filepath.Join(dir, "input")
	script := `stty raw -echo; "$1" -L "$2" wait-for -S ready; dd bs=1 count=512 of="$3" 2>/dev/null; "$1" -L "$2" wait-for -S received; exec cat`
	if err := tc.NewSession(tmux.NewSessionOptions{Name: "input", Argv: []string{"/bin/sh", "-c", script, "sh", bin, tc.Socket, output}}); err != nil {
		t.Fatal(err)
	}
	wait := func(channel string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		argv := tc.Command("wait-for", channel)
		if err := exec.CommandContext(ctx, argv[0], argv[1:]...).Run(); err != nil {
			t.Fatal(err)
		}
	}
	wait("ready")
	d := New(Config{Tmux: tc})
	d.sessions["input"] = &session{meta: meta{Tmux: "input"}}
	stream, err := d.OpenTerminal("input")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case <-stream.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("control connection did not attach")
	}
	if err := os.WriteFile(calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 512)
	for i := range want {
		want[i] = byte(i)
	}
	if err := stream.Input(want); err != nil {
		t.Fatal(err)
	}
	invoked, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if len(invoked) != 0 {
		t.Fatalf("input launched subprocesses: %q", invoked)
	}
	wait("received")
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("input bytes changed: got %x, want %x", got, want)
	}
	// A rejected command has its own acknowledgement; a later valid command
	// must not receive that rejection, or leak command text in the error.
	pane := stream.pane
	stream.pane = "%99999999"
	if err := stream.Input([]byte("private-input")); err == nil || strings.Contains(err.Error(), "private-input") {
		t.Fatalf("command rejection: %v", err)
	}
	stream.pane = pane
	if err := stream.Input([]byte("ok")); err != nil {
		t.Fatalf("result misattributed: %v", err)
	}
	stream.Close()
	if err := stream.Input([]byte("after-close")); err == nil {
		t.Fatal("closed stream accepted input")
	}
}
