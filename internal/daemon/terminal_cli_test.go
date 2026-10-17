package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
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

// missingCLI checks the error names the command, where it was not found and
// the variable that would name its path instead.
func missingCLI(t *testing.T, err error, where string) {
	t.Helper()
	want := "codex was not found in " + where + "; install it there or set REMOTE_AGENT_CODEX"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error is %v, want it to contain %q", err, want)
	}
}

// noSessionStarted checks that no tmux session was created. The fake tmux
// records each call, so a missing file, or one without new-session, means
// nothing started.
func noSessionStarted(t *testing.T, calls string) {
	t.Helper()
	b, err := os.ReadFile(calls)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ARG: new-session") {
		t.Fatalf("a tmux session was started:\n%s", b)
	}
}

// noSessionDir checks that a session that failed to start left nothing
// behind under the daemon's sessions directory.
func noSessionDir(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) > 0 {
		t.Fatalf("the failed start left %d session directories", len(entries))
	}
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

// When the terminal's PATH has no codex, creation fails instead of
// starting the daemon's CLI: the session runs what the terminal would, or
// nothing at all.
func TestTerminalSessionFailsWithoutTheCLIInItsPATH(t *testing.T) {
	missing := t.TempDir()
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"PATH without codex", map[string]string{"PATH": missing}},
		{"no PATH", map[string]string{"HOME": t.TempDir()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"})
			calls := fakeTmux(t, h)
			_, err := (&handler{d: h.d}).CreateSession(agent.WithOrigin(context.Background(), originLocal), connect.NewRequest(&pb.CreateSessionRequest{
				Cwd: h.dir, Agent: pb.Agent_AGENT_CODEX, Env: tc.env,
			}))
			missingCLI(t, err, "the terminal's PATH")
			if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
				t.Fatalf("create returns %v, want InvalidArgument", code)
			}
			noSessionStarted(t, calls)
			noSessionDir(t)
		})
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

// Resuming or forking starts the CLI the session's saved PATH holds, and
// fails when it holds none rather than running the daemon's.
func TestResumeAndForkFailWithoutTheCLIInTheSavedPATH(t *testing.T) {
	missing := t.TempDir()
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"PATH without codex", map[string]string{"PATH": missing}},
		{"no PATH", map[string]string{"HOME": t.TempDir()}},
	} {
		for _, op := range []string{"resume", "fork"} {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"})
				h.d.sessions[h.s.meta.ID] = h.s
				calls := fakeTmux(t, h)
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				h.d.ctx = ctx
				if err := writeEnvFile(filepath.Join(h.dir, "env.json"), tc.env); err != nil {
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
					_, err = (&handler{d: h.d}).ResumeSession(context.Background(), connect.NewRequest(&pb.ResumeSessionRequest{SessionId: h.s.meta.ID}))
				} else {
					_, err = (&handler{d: h.d}).ForkSession(agent.WithOrigin(context.Background(), originLocal), connect.NewRequest(&pb.ForkSessionRequest{SessionId: h.s.meta.ID}))
				}
				missingCLI(t, err, "the session's saved PATH")
				noSessionStarted(t, calls)
				noSessionDir(t)
			})
		}
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

// A scheduled run derived from a terminal's session fails when that
// session's saved PATH holds no codex, or none is saved, and records why:
// it must not open the daemon's CLI instead.
func TestScheduledSessionFromATerminalFailsWithoutTheCLIInItsPATH(t *testing.T) {
	missing := t.TempDir()
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"PATH without codex", map[string]string{"PATH": missing}},
		{"no PATH", map[string]string{"HOME": t.TempDir()}},
		{"no saved environment", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			if tc.env != nil {
				if err := writeEnvFile(filepath.Join(dir, "env.json"), tc.env); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now()
			task := &schedule.Task{ID: "task1", SourceSession: src, NextRunAt: &now,
				Spec: schedule.Spec{Message: "go", Target: schedule.Target{Type: "new_session", Cwd: h.dir, Agent: "codex"}}}
			h.d.dispatchScheduled(task, now)
			if task.Active != nil {
				t.Fatalf("the failed run is still active: %+v", task.Active)
			}
			if len(task.Runs) != 1 {
				t.Fatalf("the run left %d history entries, want 1", len(task.Runs))
			}
			want := "could_not_start_session: codex was not found in the session's saved PATH; install it there or set REMOTE_AGENT_CODEX"
			if run := task.Runs[0]; run.Status != "failed" || !strings.Contains(run.Reason, want) {
				t.Fatalf("the run is %q (%q), want failed with %q", run.Status, run.Reason, want)
			}
			if task.Status != "failed" || !strings.Contains(task.Reason, want) || task.NextRunAt != nil {
				t.Fatalf("the task is %q (%q) next at %v, want failed with %q and no next run", task.Status, task.Reason, task.NextRunAt, want)
			}
			noSessionStarted(t, calls)
			if entries, err := os.ReadDir(paths.SessionsDir()); err != nil || len(entries) != 1 || entries[0].Name() != src {
				t.Fatalf("the failed run left session directories: %v (%v)", entries, err)
			}
		})
	}
}
