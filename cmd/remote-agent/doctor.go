package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Lewin671/remote-agent/internal/daemon"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/tmux"
)

// runDoctor checks the installed Codex against what remote-agent relies
// on, with a private daemon, state directory and tmux socket, so the
// running daemon and its sessions are not touched.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	codexBin := fs.String("codex", os.Getenv("REMOTE_AGENT_CODEX"), "codex executable (default: codex in PATH)")
	fs.Parse(args)

	// Settings come from the real state directory; the session runs in a
	// fixed folder there, so Codex asks to trust it only once.
	realDir := paths.Dir()
	work := filepath.Join(realDir, "doctor-work")
	if err := paths.EnsureDir(work); err != nil {
		return err
	}
	tc := &tmux.Client{Socket: fmt.Sprintf("ra-doctor-%d", os.Getpid())}
	cfg, err := daemonConfig(*codexBin, tc)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "ra-doctor-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	prevHome, hadHome := os.LookupEnv("REMOTE_AGENT_HOME")
	os.Setenv("REMOTE_AGENT_HOME", tmp) // the private daemon and its hooks
	restore := func() {
		if hadHome {
			os.Setenv("REMOTE_AGENT_HOME", prevHome)
		} else {
			os.Unsetenv("REMOTE_AGENT_HOME")
		}
	}
	defer restore()
	defer tc.Run("kill-server")

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	d := daemon.New(cfg)
	errc := make(chan error, 1)
	go func() { errc <- d.Run(ctx) }()
	for i := 0; ; i++ {
		if _, err := os.Stat(paths.Socket()); err == nil {
			break
		}
		select {
		case err := <-errc:
			return fmt.Errorf("start a private daemon: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if i > 100 {
			return errors.New("the private daemon did not start")
		}
	}

	fmt.Println("Checking the installed Codex with a throwaway session (GPT-6-Luna, low effort)")
	fmt.Printf("in %s. Startup prompts there are answered for you.\n\n", work)
	ok, err := d.Doctor(ctx, work, os.Stdout)
	fmt.Println()
	switch {
	case err != nil:
		return err
	case ok:
		restore()
		if err := daemon.MarkVerified(d.CodexVersion()); err != nil {
			return err
		}
		fmt.Printf("All checks passed; Codex %s is marked as verified.\n", d.CodexVersion())
		return nil
	default:
		return errors.New("some checks failed; remote-agent may not work with this Codex version. Please report the failing check")
	}
}
