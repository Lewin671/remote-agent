package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/claude"
	"github.com/Lewin671/remote-agent/internal/paths"
)

// runStatusLine is Claude Code's status line in a remote-agent session
// (`remote-agent statusline claude`). It passes the status line's input,
// which holds the account's usage limits, to the daemon, and runs the
// user's own status line command with the same input, passing its output
// and exit status through; without one it prints nothing. The daemon is
// told in the background and never delays or changes the status line.
func runStatusLine(args []string) int {
	input, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	sessionID := os.Getenv("RA_SESSION")
	done := make(chan struct{})
	go func() {
		defer close(done)
		if sessionID == "" || !json.Valid(input) {
			return
		}
		payload, _ := json.Marshal(map[string]any{"hook_event_name": "StatusLine", "status": json.RawMessage(input)})
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_, _ = dial().Hook(ctx, connect.NewRequest(&pb.HookRequest{SessionId: sessionID, Payload: payload}))
	}()
	code := runUserStatusLine(sessionID, input)
	<-done
	return code
}

// runUserStatusLine runs the user's own status line command saved for the
// session, as Claude Code would have: through the shell, in the current
// folder, with the input on stdin.
func runUserStatusLine(sessionID string, input []byte) int {
	if sessionID == "" {
		return 0
	}
	b, err := os.ReadFile(filepath.Join(paths.SessionDir(sessionID), claude.StatusLineFile))
	if err != nil {
		return 0
	}
	var sl struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(b, &sl) != nil || sl.Command == "" {
		return 0
	}
	cmd := exec.Command("/bin/sh", "-c", sl.Command)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}
