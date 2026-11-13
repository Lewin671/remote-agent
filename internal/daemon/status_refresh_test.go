package daemon

import (
	"context"
	"fmt"
	"github.com/Lewin671/remote-agent/internal/tmux"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStatusRefreshRestoresViewportAndSizingPolicy(t *testing.T) {
	t.Parallel()
	tc := &tmux.Client{Socket: fmt.Sprintf("ra-refresh-%d-%d", os.Getpid(), time.Now().UnixNano())}
	if _, err := tc.CheckVersion(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _, _ = tc.Run("kill-server") })
	if err := tc.NewSession(tmux.NewSessionOptions{Name: "refresh", Width: 91, Height: 24, Argv: []string{"/bin/sh", "-c", "exec cat"}}); err != nil {
		t.Fatal(err)
	}
	before, err := tc.Run("display-message", "-p", "-t", "=refresh:", "#{window_width} #{window_height}")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := tc.Run("show-window-options", "-v", "-t", "=refresh:", "window-size")
	if err != nil {
		t.Fatal(err)
	}
	term := &tmuxTerminal{tmux: tc, name: "refresh"}
	if err := term.RefreshView(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := tc.Run("display-message", "-p", "-t", "=refresh:", "#{window_width} #{window_height}")
	if err != nil {
		t.Fatal(err)
	}
	current, err := tc.Run("show-window-options", "-v", "-t", "=refresh:", "window-size")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(before) != strings.TrimSpace(after) || strings.TrimSpace(policy) != strings.TrimSpace(current) {
		t.Fatalf("viewport was not restored: %q/%q, policy %q/%q", before, after, policy, current)
	}
}
