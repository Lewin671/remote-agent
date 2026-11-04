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
	"github.com/Lewin671/remote-agent/internal/updater"
)

// runDoctor checks the installed CLIs against what remote-agent relies
// on, with a private daemon, state directory and tmux socket, so the
// running daemon and its sessions are not touched. With no argument it
// checks every CLI installed.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	agentFlags := make(map[string]*string)
	for _, spec := range daemon.RegisteredAgents {
		envVar := "REMOTE_AGENT_" + strings.ToUpper(spec.ID)
		agentFlags[spec.ID] = fs.String(spec.ID, os.Getenv(envVar), fmt.Sprintf("%s executable (default: %s in PATH)", spec.ID, spec.ID))
	}
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
	agentBins := make(map[string]string)
	for id, flagVal := range agentFlags {
		agentBins[id] = *flagVal
	}
	cfg, err := daemonConfig(agentBins, tc)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "ra-doctor-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	// Mark the empty throwaway state before New creates upload/session data.
	if err := updater.EnsureState(tmp); err != nil {
		return err
	}
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
		spec, ok := daemon.FindAgent(w)
		if !ok {
			return fmt.Errorf("unknown CLI %q; use %s", w, strings.Join(daemon.AgentIDs(), " or "))
		}
		if cfg.Agents[spec.Kind].Bin == "" {
			return fmt.Errorf("%s not found in PATH; pass --%s", spec.ID, spec.ID)
		}
		kinds = append(kinds, spec.Kind)
	}
	if len(kinds) == 0 {
		for _, spec := range daemon.RegisteredAgents {
			if spec.Required || cfg.Agents[spec.Kind].Bin != "" {
				kinds = append(kinds, spec.Kind)
			}
		}
	}
	failed := false
	var passed []string
	for _, k := range kinds {
		spec, _ := daemon.FindAgentByKind(k)
		name := spec.Name
		how := ""
		if chk, ok := d.Checkable(k); ok {
			how = chk.DoctorDescription()
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
