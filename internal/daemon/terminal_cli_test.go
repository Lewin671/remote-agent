package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

// terminalPath builds a PATH the way a terminal may have it, with entries
// a shell passes over before the codex it runs: an empty entry, a relative
// directory holding a codex (the current one), a codex that is not
// executable, and one that is a directory. It returns the PATH and the
// codex a shell finds in it, ahead of a second executable codex.
func terminalPath(t *testing.T) (path, want string) {
	t.Helper()
	root := t.TempDir()
	cli := func(dir string, mode os.FileMode) string {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(root, dir, "codex")
		if err := os.WriteFile(file, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(root, dir)
	}
	relative := cli("relative", 0o700)
	t.Chdir(relative)
	notExecutable := cli("not-executable", 0o600)
	isDir := filepath.Join(root, "is-dir")
	if err := os.MkdirAll(filepath.Join(isDir, "codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	first := cli("first", 0o700)
	second := cli("second", 0o700)
	path = strings.Join([]string{"", ".", notExecutable, isDir, first, second}, string(os.PathListSeparator))
	return path, filepath.Join(first, "codex")
}

func startedCLI(t *testing.T, calls string) string {
	t.Helper()
	got := tmuxCalls(t, calls)
	_, host, _ := strings.Cut(got, "ARG: pty-host\n")
	_, after, ok := strings.Cut(host, "ARG: --\n")
	if !ok {
		t.Fatalf("no CLI in the tmux call:\n%s", got)
	}
	line, _, _ := strings.Cut(after, "\n")
	return strings.TrimPrefix(line, "ARG: ")
}

func startTerminalSession(t *testing.T, cfg Config, env map[string]string) (*harness, *session, string) {
	t.Helper()
	h := newHarness(t, cfg)
	calls := fakeTmux(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.d.ctx = ctx
	s := createSession(t, h, &pb.CreateSessionRequest{Cwd: h.dir, Agent: pb.Agent_AGENT_CODEX, Env: env})
	t.Cleanup(func() { s.stop(); <-s.done; _ = s.log.Close() })
	return h, s, startedCLI(t, calls)
}

// A session started from a terminal runs the codex typing `codex` in that
// terminal would, not the one the daemon found in its own PATH, and
// version checks follow it.
func TestTerminalSessionRunsTheCLIOfItsPATH(t *testing.T) {
	path, want := terminalPath(t)
	h, _, got := startTerminalSession(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"}, map[string]string{"PATH": path})
	if got != want {
		t.Fatalf("the session runs %q, want the terminal's %q", got, want)
	}
	if bin := h.d.adapter(pb.Agent_AGENT_CODEX).Bin(); bin != want {
		t.Fatalf("version checks use %q, want %q", bin, want)
	}
}

// A CLI the user configured by path runs from that path, whatever the
// terminal's PATH holds: it is not a name to look up, not even under a
// PATH entry that happens to contain its path.
func TestTerminalSessionKeepsAConfiguredCLI(t *testing.T) {
	path, _ := terminalPath(t)
	under := t.TempDir()
	if err := os.MkdirAll(filepath.Join(under, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(under, "bin", "echo"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path = under + string(os.PathListSeparator) + path
	_, _, got := startTerminalSession(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo"}, map[string]string{"PATH": path})
	if got != "/bin/echo" {
		t.Fatalf("the session runs %q, want the configured /bin/echo", got)
	}
}

// When the terminal's PATH has no codex, the daemon's still starts it.
func TestTerminalSessionFallsBackToTheDaemonsCLI(t *testing.T) {
	_, _, got := startTerminalSession(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"}, map[string]string{"PATH": t.TempDir()})
	if got != "/bin/echo" {
		t.Fatalf("the session runs %q, want the daemon's /bin/echo", got)
	}
}

// doctor's session carries no caller's environment: it runs the daemon's
// CLI even when agent.env sets a PATH holding another.
func TestDoctorSessionRunsTheDaemonsCLI(t *testing.T) {
	path, _ := terminalPath(t)
	h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex", Env: map[string]string{"PATH": path}})
	calls := fakeTmux(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.d.ctx = ctx
	s, err := h.d.create(&pb.CreateSessionRequest{Cwd: h.dir, Agent: pb.Agent_AGENT_CODEX})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.stop(); <-s.done; _ = s.log.Close() }()
	if got := startedCLI(t, calls); got != "/bin/echo" {
		t.Fatalf("doctor's session runs %q, want the daemon's /bin/echo", got)
	}
}

// Resuming or forking a terminal's session finds the CLI in its saved
// PATH again, as its first start did.
func TestResumeAndForkFindTheCLIInTheSavedPATH(t *testing.T) {
	for _, op := range []string{"resume", "fork"} {
		t.Run(op, func(t *testing.T) {
			path, want := terminalPath(t)
			h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"})
			h.d.sessions[h.s.meta.ID] = h.s
			calls := fakeTmux(t, h)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			h.d.ctx = ctx
			if err := writeEnvFile(filepath.Join(h.dir, "env.json"), map[string]string{"PATH": path}); err != nil {
				t.Fatal(err)
			}
			if op == "fork" {
				rollout := filepath.Join(h.dir, "rollout.jsonl")
				h.writeRollout(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`)
				h.s.bindRollout("cli-src", rollout)
				h.s.readRollout()
				h.s.mu.Lock()
				h.s.meta.RolloutPath = rollout
				h.s.mu.Unlock()
			}
			h.s.markExited("test")
			var err error
			if op == "resume" {
				_, err = h.d.resume(h.s.meta.ID)
			} else {
				_, err = h.d.fork(h.s.meta.ID, originLocal)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := startedCLI(t, calls); got != want {
				t.Fatalf("the %s runs %q, want the saved PATH's %q", op, got, want)
			}
		})
	}
}

// A scheduled run that opens a new session from a terminal's session runs
// the CLI of that session's PATH, as the session itself does.
func TestScheduledSessionFromATerminalFindsTheCLIInItsPATH(t *testing.T) {
	path, want := terminalPath(t)
	h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"})
	calls := fakeTmux(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.d.ctx = ctx
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	src := "fedcba9876543210"
	dir := paths.SessionDir(src)
	if err := paths.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := (&meta{ID: src, Agent: pb.Agent_AGENT_CODEX, Cwd: h.dir}).save(dir); err != nil {
		t.Fatal(err)
	}
	if err := writeEnvFile(filepath.Join(dir, "env.json"), map[string]string{"PATH": path}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	task := &schedule.Task{ID: "task1", SourceSession: src, NextRunAt: &now,
		Spec: schedule.Spec{Message: "go", Target: schedule.Target{Type: "new_session", Cwd: h.dir, Agent: "codex"}}}
	h.d.dispatchScheduled(task, now)
	if task.Active == nil || task.Active.SessionID == "" {
		t.Fatalf("the run did not start a session: %+v", task)
	}
	if s, err := h.d.exact(task.Active.SessionID); err == nil {
		defer func() { s.stop(); <-s.done; _ = s.log.Close() }()
	}
	if got := startedCLI(t, calls); got != want {
		t.Fatalf("the scheduled session runs %q, want the source's PATH's %q", got, want)
	}
}
