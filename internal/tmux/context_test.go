package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunContextStopsBlockedTmux(t *testing.T) {
	// Not parallel for the same ETXTBSY reason as the fake-tmux poll cases.
	// The fake tmux is a shell script, not a re-executed test binary: starting
	// it takes milliseconds even on a loaded machine, so the 5 s bound below is
	// spent blocked, not starting up.
	for _, manual := range []bool{true, false} {
		name := "default bound"
		if manual {
			name = "caller cancellation"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			ready := filepath.Join(dir, "pid")
			bin := filepath.Join(dir, "tmux")
			script := "#!/bin/sh\necho $$ > " + shellLiteral(ready+".tmp") + " && mv " + shellLiteral(ready+".tmp") + " " + shellLiteral(ready) + "\nexec sleep 60\n"
			if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := (&Client{Bin: bin}).RunContext(ctx, "capture-pane"); result <- err }()
			var pid int
			for deadline := time.Now().Add(10 * time.Second); pid == 0; {
				select {
				case err := <-result:
					t.Fatalf("subprocess failed before it was blocked: %v", err)
				default:
				}
				if b, err := os.ReadFile(ready); err == nil {
					pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
				}
				if time.Now().After(deadline) {
					t.Fatal("fake tmux never started")
				}
				time.Sleep(10 * time.Millisecond)
			}
			want := context.DeadlineExceeded
			if manual {
				cancel()
				want = context.Canceled
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatalf("command cancellation: %v", err)
				}
			case <-time.After(CommandTimeout + 2*time.Second):
				t.Fatal("blocked tmux exceeded bound")
			}
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("child still running: %v", err)
			}
		})
	}
}
