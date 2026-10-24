package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/Lewin671/remote-agent/internal/tmux"
)

func TestScheduledClientEnvironmentSurvivesDaemonRestart(t *testing.T) {
	for _, cli := range []string{"codex", "claude"} {
		for _, source := range []string{"", "client-source"} {
			for _, saved := range []string{"stale", "missing"} {
				t.Run(cli+"/"+source+"/"+saved, func(t *testing.T) {
					t.Setenv("PATH", "/stale/daemon/bin:/usr/bin:/bin")
					h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex", ClaudeBin: "/bin/echo", ClaudeCmd: "claude", Env: map[string]string{"RA_TEST_EXTRA": "agent-env"}})
					defer h.s.log.Close()
					calls := fakeTmux(t, h)
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					h.d.ctx = ctx // Fake tmux: do not start background CLI readers.
					if source != "" {
						dir := paths.SessionDir(source)
						if err := paths.EnsureDir(dir); err != nil {
							t.Fatal(err)
						}
						if err := (&meta{ID: source, Shell: true, Exited: true}).save(dir); err != nil {
							t.Fatal(err)
						}
						if err := writeEnvFile(filepath.Join(dir, "env.json"), map[string]string{"PATH": "/old/source/path", "RA_OLD_SOURCE": "discard"}); err != nil {
							t.Fatal(err)
						}
					}
					task := &schedule.Task{ID: "task", SourceSession: source, Active: &schedule.Run{ID: "run"}, Spec: schedule.Spec{Target: schedule.Target{Type: "new_session", Cwd: h.dir, Agent: cli}}}
					s, err := h.d.launchScheduledSession(context.Background(), task)
					if err != nil {
						t.Fatal(err)
					}
					s.stop()
					<-s.done
					assertShell := func(s *session) {
						t.Helper()
						m, err := loadMeta(s.dir)
						if err != nil || !m.Shell {
							t.Fatalf("scheduled client launch style was not durable: meta=%+v err=%v", m, err)
						}
						env := sessionEnvFile(t, s.dir)
						if env["PATH"] != systemPath() || env["RA_TEST_EXTRA"] != "agent-env" || env["RA_OLD_SOURCE"] != "" {
							t.Fatalf("not a fresh client shell environment: PATH=%q", env["PATH"])
						}
						got := tmuxCalls(t, calls)
						for _, arg := range []string{"--clean-env", "-l", "-i", "-c", cli} {
							if !strings.Contains(got, "ARG: "+arg+"\n") {
								t.Fatalf("missing client shell launch argument %q", arg)
							}
						}
						if strings.Contains(got, "ARG: /bin/echo\n") {
							t.Fatal("used daemon's cached CLI path instead of login shell lookup")
						}
					}
					assertShell(s)
					s.mu.Lock()
					s.meta.Starting = false
					s.mu.Unlock()
					if err := s.markExited("test completed"); err != nil {
						t.Fatal(err)
					}
					if err := s.log.Close(); err != nil {
						t.Fatal(err)
					}
					if saved == "missing" {
						if err := os.Remove(filepath.Join(s.dir, "env.json")); err != nil {
							t.Fatal(err)
						}
					} else if err := writeEnvFile(filepath.Join(s.dir, "env.json"), map[string]string{"PATH": "/stale/resume/path"}); err != nil {
						t.Fatal(err)
					}
					// Use a new daemon and the real metadata restore path, not its old map.
					fresh := New(h.d.cfg)
					fresh.ctx = ctx
					if err := fresh.restoreWithSessionList(func() ([]tmux.SessionInfo, error) { return nil, nil }); err != nil {
						t.Fatal(err)
					}
					defer func() {
						for _, restored := range fresh.sessions {
							if restored.stop != nil {
								restored.stop()
								<-restored.done
							}
							restored.log.Close()
						}
					}()
					if err := os.WriteFile(calls, nil, 0600); err != nil {
						t.Fatal(err)
					}
					resumed, err := fresh.resumeContextBy(context.Background(), s.meta.ID, originLocal)
					if err != nil {
						t.Fatalf("client task session could not resume after daemon restart: %v", err)
					}
					resumed.stop()
					<-resumed.done
					assertShell(resumed)
				})
			}
		}
	}
}
