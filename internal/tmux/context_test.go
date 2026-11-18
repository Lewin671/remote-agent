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
	t.Run("caller cancellation", func(t *testing.T) {
		dir := t.TempDir()
		ready := filepath.Join(dir, "pid")
		bin := filepath.Join(dir, "tmux")
		script := "#!/bin/sh\necho $$ > " + shellLiteral(ready+".tmp") + " && mv " + shellLiteral(ready+".tmp") + " " + shellLiteral(ready) + "\nexec sleep 600\n"
		if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		// Only the cancellation below may end the command, however slowly the
		// machine starts the fake tmux.
		go func() {
			_, err := (&Client{Bin: bin, Timeout: time.Hour}).RunContext(ctx, "capture-pane")
			result <- err
		}()
		var pid int
		for pid == 0 {
			select {
			case err := <-result:
				t.Fatalf("subprocess failed before it was blocked: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
			if b, err := os.ReadFile(ready); err == nil {
				pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			}
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("command cancellation: %v", err)
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("child still running: %v", err)
		}
	})
	t.Run("default bound", func(t *testing.T) {
		// The bound applies without a caller deadline; whether it fires before or
		// after the subprocess starts, the command reports the deadline.
		_, err := (&Client{Bin: "/bin/sleep", Timeout: time.Millisecond}).RunContext(context.Background(), "600")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("command bound: %v", err)
		}
	})
}
