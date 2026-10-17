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
	"strings"
	"syscall"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/daemon"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/tmux"
)

// runDoctor checks the installed CLIs against what remote-agent relies
// on, with a private daemon, state directory and tmux socket, so the
// running daemon and its sessions are not touched. With no argument it
// checks every CLI installed.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	codexBin := fs.String("codex", os.Getenv("REMOTE_AGENT_CODEX"), "codex executable (default: codex in PATH)")
	claudeBin := fs.String("claude", os.Getenv("REMOTE_AGENT_CLAUDE"), "claude executable (default: claude in PATH)")
	var which []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		which, args = append(which, args[0]), args[1:]
	}
	fs.Parse(args)
	which = append(which, fs.Args()...)

	// Settings come from the real state directory; the session runs in a
	// fixed folder there, so Codex asks to trust it only once.
	realDir := paths.Dir()
	work := filepath.Join(realDir, "doctor-work")
	if err := paths.EnsureDir(work); err != nil {
		return err
	}
	tc := &tmux.Client{Socket: fmt.Sprintf("ra-doctor-%d", os.Getpid())}
	cfg, err := daemonConfig(*codexBin, *claudeBin, tc)
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

	var kinds []pb.Agent
	for _, w := range which {
		switch w {
		case "codex":
			kinds = append(kinds, pb.Agent_AGENT_CODEX)
		case "claude":
			if cfg.ClaudeBin == "" {
				return errors.New("claude not found in PATH; pass --claude")
			}
			kinds = append(kinds, pb.Agent_AGENT_CLAUDE)
		default:
			return fmt.Errorf("unknown CLI %q; use codex or claude", w)
		}
	}
	if len(kinds) == 0 {
		kinds = append(kinds, pb.Agent_AGENT_CODEX)
		if cfg.ClaudeBin != "" {
			kinds = append(kinds, pb.Agent_AGENT_CLAUDE)
		}
	}
	failed := false
	var passed []string
	for _, k := range kinds {
		name, how := "Codex", "GPT-6-Luna, low effort"
		if k == pb.Agent_AGENT_CLAUDE {
			name, how = "Claude Code", "the catalog's models"
		}
		fmt.Printf("Checking the installed %s with a throwaway session (%s)\n", name, how)
		fmt.Printf("in %s. Startup prompts there are answered for you.\n\n", work)
		ok, err := d.Doctor(ctx, k, work, os.Stdout)
		fmt.Println()
		if err != nil {
			return err
		}
		if !ok {
			failed = true
			fmt.Printf("Some checks failed; remote-agent may not work with this %s version. Please report the failing check.\n\n", name)
			continue
		}
		passed = append(passed, fmt.Sprintf("%s %s", name, d.Version(k)))
		if err := daemon.MarkVerified(realDir, k, d.Version(k)); err != nil {
			return err
		}
	}
	if len(passed) > 0 {
		fmt.Printf("All checks passed for %s; marked as verified.\n", strings.Join(passed, ", "))
	}
	if failed {
		return errors.New("some checks failed")
	}
	return nil
}
