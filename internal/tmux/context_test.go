package tmux

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Run as a child by the fake tmux executable. The handshake proves the
// subprocess started and is blocked before its parent cancels it.
func TestTmuxBlockedHelper(t *testing.T) {
	if os.Getenv("RA_TMUX_BLOCKED_HELPER") != "1" {
		return
	}
	c, err := net.Dial("unix", os.Getenv("RA_TMUX_HELPER_SOCKET"))
	if err != nil {
		os.Exit(2)
	}
	_, _ = c.Write([]byte{1})
	_, _ = c.Read(make([]byte, 1))
	os.Exit(0)
}

func TestRunContextStopsBlockedTmux(t *testing.T) {
	for _, manual := range []bool{true, false} {
		name := "default bound"
		if manual {
			name = "caller cancellation"
		}
		t.Run(name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "ra-tmux-ctx-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "ready.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("RA_TMUX_BLOCKED_HELPER", "1")
			t.Setenv("RA_TMUX_HELPER_SOCKET", socket)
			t.Setenv("RA_TMUX_HELPER_EXE", executable)
			bin := filepath.Join(dir, "tmux")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec \"$RA_TMUX_HELPER_EXE\" -test.run='^TestTmuxBlockedHelper$' -- \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			accepted := make(chan net.Conn, 1)
			go func() { c, _ := listener.Accept(); accepted <- c }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := (&Client{Bin: bin}).RunContext(ctx, "capture-pane"); result <- err }()
			var c net.Conn
			select {
			case c = <-accepted:
			case err := <-result:
				t.Fatalf("subprocess failed before handshake: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("no subprocess handshake")
			}
			if c == nil {
				t.Fatal("accept failed")
			}
			defer c.Close()
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.ReadFull(c, make([]byte, 1)); err != nil {
				t.Fatal(err)
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
			if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("child still running: %v", err)
			}
		})
	}
}
