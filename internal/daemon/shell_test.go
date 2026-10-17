package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/tmux"
)

// A client-created session starts like a new terminal tab: the shell's own
// environment, not the daemon's stale copy.
func TestShellStartEnvIsANewTerminalEnvironment(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("USER", "tester")
	t.Setenv("LOGNAME", "tester")
	t.Setenv("TMPDIR", "/tmp/tester")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	// The daemon's PATH is a snapshot from when it started, holding an nvm
	// version since replaced; the shell must not start with it.
	t.Setenv("PATH", "/home/tester/.nvm/versions/node/v22.17.0/bin:/usr/bin:/bin")

	env := shellStartEnv("/bin/zsh")
	if env["PATH"] != systemPath() {
		t.Fatalf("the shell starts with PATH %q, want the system's %q", env["PATH"], systemPath())
	}
	if env["SHELL"] != "/bin/zsh" {
		t.Fatalf("SHELL is %q", env["SHELL"])
	}
	for k, want := range map[string]string{
		"HOME": "/home/tester", "USER": "tester", "LOGNAME": "tester",
		"TMPDIR": "/tmp/tester", "LANG": "en_US.UTF-8", "SSH_AUTH_SOCK": "/tmp/agent.sock",
	} {
		if env[k] != want {
			t.Fatalf("%s is %q, want %q", k, env[k], want)
		}
	}
}

func TestShellLaunchArgvKeepsArgumentsWhole(t *testing.T) {
	args := []string{"--model", "a b", "it's", "line1\nline2"}
	argv := shellLaunchArgv("/bin/zsh", "/tmp/session", "codex", args)
	want := []string{"/bin/zsh", "-l", "-i", "-c"}
	for i, w := range want {
		if argv[i] != w {
			t.Fatalf("argv[%d] = %q, want %q", i, argv[i], w)
		}
	}
	script := argv[4]
	if !strings.Contains(script, "/tmp/session/"+shellBinFile) || !strings.Contains(script, "/tmp/session/"+shellErrorFile) {
		t.Fatalf("the script does not report into the session directory:\n%s", script)
	}
	if strings.Contains(script, "--model") || strings.Contains(script, "a b") {
		t.Fatalf("the CLI's arguments were spliced into the script:\n%s", script)
	}
	rest := argv[5:]
	if len(rest) != len(args)+2 || rest[0] != "zsh" || rest[1] != "codex" {
		t.Fatalf("the command and its arguments are not positional: %q", rest)
	}
	for i, a := range args {
		if rest[i+2] != a {
			t.Fatalf("argument %d is %q, want %q", i, rest[i+2], a)
		}
	}
}

func TestShellLaunchArgvForFish(t *testing.T) {
	argv := shellLaunchArgv("/usr/bin/fish", "/tmp/session", "claude", []string{"-p"})
	if len(argv) != 7 || argv[0] != "/usr/bin/fish" || argv[1] != "-l" || argv[2] != "-i" || argv[3] != "-c" {
		t.Fatalf("argv %q", argv)
	}
	if !strings.Contains(argv[4], "exec $_ra_bin $argv[2..-1]") {
		t.Fatalf("the fish script does not exec its argument list:\n%s", argv[4])
	}
	if argv[5] != "claude" || argv[6] != "-p" {
		t.Fatalf("fish arguments are not positional: %q", argv[5:])
	}
}

func TestParseUserShell(t *testing.T) {
	for _, c := range []struct{ out, want string }{
		{"UserShell: /bin/zsh\n", "/bin/zsh"},
		{"NFSHomeDirectory: /Users/x\nUserShell: /bin/bash\n", "/bin/bash"},
		{"", ""},
		{"No such key: UserShell\n", ""},
	} {
		if got := parseUserShell(c.out); got != c.want {
			t.Fatalf("parseUserShell(%q) = %q, want %q", c.out, got, c.want)
		}
	}
}

func TestPasswdShellLine(t *testing.T) {
	passwd := "root:x:0:0:root:/root:/bin/bash\ntester:x:501:20::/home/tester:/bin/zsh\n"
	for _, c := range []struct{ uid, want string }{
		{"501", "/bin/zsh"},
		{"0", "/bin/bash"},
		{"999", ""},
	} {
		if got := passwdShellLine(passwd, c.uid); got != c.want {
			t.Fatalf("passwdShellLine(uid %s) = %q, want %q", c.uid, got, c.want)
		}
	}
}

func TestLoginShellIsRunnable(t *testing.T) {
	shell := loginShell()
	if st, err := os.Stat(shell); err != nil || st.IsDir() {
		t.Fatalf("loginShell() = %q: %v", shell, err)
	}
}

// fakeTmux records every tmux call's arguments, one per line, so a test
// can see how a session was started. has-session answers "no", so a test
// can resume the session through it.
func fakeTmux(t *testing.T, h *harness) string {
	t.Helper()
	bin := filepath.Join(h.dir, "fake-tmux")
	calls := filepath.Join(h.dir, "tmux.calls")
	script := "#!/bin/sh\nprintf 'ARG: %s\\n' \"$@\" >> '" + calls + "'\nprintf 'END\\n' >> '" + calls + "'\ncase \"$3\" in has-session) exit 1;; esac\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	h.d.cfg.Tmux = &tmux.Client{Bin: bin, Socket: "test"}
	return calls
}

func tmuxCalls(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sessionEnvFile(t *testing.T, dir string) map[string]string {
	t.Helper()
	env, err := readEnvFile(filepath.Join(dir, "env.json"))
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// createSession starts a session the way callers do: through the daemon's
// API, with the environment a terminal's command carries or none at all,
// as a client's command arrives.
func createSession(t *testing.T, h *harness, req *pb.CreateSessionRequest) *session {
	t.Helper()
	resp, err := (&handler{d: h.d}).CreateSession(agent.WithOrigin(context.Background(), originLocal), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	s, err := h.d.exact(resp.Msg.Session.Id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A session a client creates (no environment in the request) runs through
// the user's login shell, with a new terminal's environment.
func TestClientSessionStartsThroughTheLoginShell(t *testing.T) {
	t.Setenv("PATH", "/stale/nvm/bin:/usr/bin:/bin")
	h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex", Env: map[string]string{"HTTPS_PROXY": "http://proxy.example"}})
	calls := fakeTmux(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // no real CLI or background work is needed
	h.d.ctx = ctx

	s := createSession(t, h, &pb.CreateSessionRequest{Cwd: h.dir, Agent: pb.Agent_AGENT_CODEX})
	defer func() { s.stop(); <-s.done; _ = s.log.Close() }()

	if !s.meta.Shell {
		t.Fatal("the session was not recorded as started through the login shell")
	}
	got := tmuxCalls(t, calls)
	for _, want := range []string{"ARG: --clean-env", "ARG: -l", "ARG: -i", "ARG: -c", "ARG: codex"} {
		if !strings.Contains(got, want+"\n") {
			t.Fatalf("the tmux session was started without %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ARG: /bin/echo") {
		t.Fatalf("the CLI's path was passed instead of its name:\n%s", got)
	}
	env := sessionEnvFile(t, s.dir)
	if env["PATH"] != systemPath() {
		t.Fatalf("the session's environment has PATH %q, want the system's %q", env["PATH"], systemPath())
	}
	if env["HTTPS_PROXY"] != "http://proxy.example" {
		t.Fatalf("agent.env is missing from the session: %v", env)
	}
	if env["RA_SESSION"] != s.meta.ID || env["RA_EXE"] == "" {
		t.Fatalf("remote-agent's own variables are missing: %v", env)
	}
}

// A session started from a terminal keeps the caller's environment, as
// before: the blocking of the new login-shell path must not touch it.
func TestTerminalSessionKeepsItsEnvironment(t *testing.T) {
	h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"})
	calls := fakeTmux(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.d.ctx = ctx

	s := createSession(t, h, &pb.CreateSessionRequest{Cwd: h.dir, Agent: pb.Agent_AGENT_CODEX, Env: map[string]string{
		"PATH": "/my/terminal/bin:" + systemPath(), "RA_TERMINAL_MARK": "yes",
	}})
	defer func() { s.stop(); <-s.done; _ = s.log.Close() }()

	if s.meta.Shell {
		t.Fatal("a terminal-created session was recorded as starting through the login shell")
	}
	got := tmuxCalls(t, calls)
	if strings.Contains(got, "ARG: --clean-env") {
		t.Fatalf("the daemon's environment was replaced for a terminal session:\n%s", got)
	}
	if !strings.Contains(got, "ARG: /bin/echo\n") {
		t.Fatalf("the CLI was not started directly:\n%s", got)
	}
	env := sessionEnvFile(t, s.dir)
	if env["PATH"] != "/my/terminal/bin:"+systemPath() || env["RA_TERMINAL_MARK"] != "yes" {
		t.Fatalf("the terminal's environment was not kept: %v", env)
	}
}

// A session started through the login shell is resumed the same way, with
// its environment computed again; one started from a terminal resumes
// from its saved environment, as before.
func TestResumeFollowsTheLaunchStyle(t *testing.T) {
	for _, shell := range []bool{true, false} {
		t.Run(map[bool]string{true: "shell", false: "direct"}[shell], func(t *testing.T) {
			h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"})
			h.d.sessions[h.s.meta.ID] = h.s
			calls := fakeTmux(t, h)
			h.d.ctx = context.Background()
			saved := map[string]string{"PATH": "/saved/bin", "RA_SAVED_MARK": "yes"}
			if err := writeEnvFile(filepath.Join(h.dir, "env.json"), saved); err != nil {
				t.Fatal(err)
			}
			h.s.mu.Lock()
			h.s.meta.Shell = shell
			h.s.mu.Unlock()
			h.s.markExited("test")

			if _, err := h.d.resume(h.s.meta.ID); err != nil {
				t.Fatal(err)
			}
			got := tmuxCalls(t, calls)
			env := sessionEnvFile(t, h.dir)
			if shell {
				for _, want := range []string{"ARG: --clean-env", "ARG: codex"} {
					if !strings.Contains(got, want+"\n") {
						t.Fatalf("the resume did not start through the login shell (%q):\n%s", want, got)
					}
				}
				if env["PATH"] != systemPath() {
					t.Fatalf("the resumed session kept PATH %q, want the system's %q", env["PATH"], systemPath())
				}
			} else {
				if strings.Contains(got, "ARG: --clean-env") {
					t.Fatalf("a terminal session resumed through the login shell:\n%s", got)
				}
				if env["PATH"] != "/saved/bin" || env["RA_SAVED_MARK"] != "yes" {
					t.Fatalf("the saved environment was not used: %v", env)
				}
			}
		})
	}
}

// A fork of a session started through the login shell starts that way too.
func TestForkFollowsTheSourceLaunchStyle(t *testing.T) {
	for _, shell := range []bool{true, false} {
		t.Run(map[bool]string{true: "shell", false: "direct"}[shell], func(t *testing.T) {
			h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"})
			h.d.sessions[h.s.meta.ID] = h.s
			calls := fakeTmux(t, h)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			h.d.ctx = ctx
			saved := map[string]string{"PATH": "/saved/bin", "RA_SAVED_MARK": "yes"}
			if err := writeEnvFile(filepath.Join(h.dir, "env.json"), saved); err != nil {
				t.Fatal(err)
			}
			// A source that can be forked: it ran a conversation and is
			// over, with its own transcript read up to some point.
			rollout := filepath.Join(h.dir, "rollout.jsonl")
			h.writeRollout(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`)
			h.s.bindRollout("cli-src", rollout)
			h.s.readRollout()
			h.s.mu.Lock()
			h.s.meta.Shell = shell
			h.s.meta.RolloutPath = rollout
			h.s.mu.Unlock()
			h.s.markExited("test")

			fs, err := h.d.fork(h.s.meta.ID, originLocal)
			if err != nil {
				t.Fatal(err)
			}
			got := tmuxCalls(t, calls)
			env := sessionEnvFile(t, fs.dir)
			if shell {
				for _, want := range []string{"ARG: --clean-env", "ARG: codex"} {
					if !strings.Contains(got, want+"\n") {
						t.Fatalf("the fork did not start through the login shell (%q):\n%s", want, got)
					}
				}
				if env["PATH"] != systemPath() {
					t.Fatalf("the fork kept PATH %q, want the system's %q", env["PATH"], systemPath())
				}
				if !fs.meta.Shell {
					t.Fatal("the fork did not record its launch style")
				}
			} else {
				if strings.Contains(got, "ARG: --clean-env") {
					t.Fatalf("a terminal source's fork started through the login shell:\n%s", got)
				}
				if env["PATH"] != "/saved/bin" || env["RA_SAVED_MARK"] != "yes" {
					t.Fatalf("the source's environment was not used: %v", env)
				}
			}
		})
	}
}

// The script is run by a real login shell here: it starts the command it
// is given, hands its arguments over whole, and reports what it resolved.
func TestShellLaunchScriptRunsInARealShell(t *testing.T) {
	shell := loginShell()
	dir := t.TempDir()
	argv := shellLaunchArgv(shell, dir, "ra-no-such-cli", nil)
	out, err := runShellScript(dir, argv)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 127 {
		t.Fatalf("a command the shell cannot find ended with %v (%s)", err, out)
	}
	msg, err := os.ReadFile(filepath.Join(dir, shellErrorFile))
	if err != nil || !strings.Contains(string(msg), "could not find ra-no-such-cli") {
		t.Fatalf("the shell did not report the missing command: %q, %v", msg, err)
	}

	// A command that is there (as when the user configured its path): the
	// shell execs it with the arguments whole and the environment it was
	// given.
	args := filepath.Join(dir, "args")
	cli := filepath.Join(dir, "ra-test-cli")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + args + "'\nprintf '%s' \"$RA_TEST_MARK\" >> '" + args + "'\n"
	if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	argv = shellLaunchArgv(shell, dir, cli, []string{"a b", "it's"})
	if out, err := runShellScript(dir, argv); err != nil {
		t.Fatalf("the script failed: %v (%s)", err, out)
	}
	got, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a b\nit's\ngot" {
		t.Fatalf("the command received %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, shellBinFile)); err != nil || strings.TrimSpace(string(b)) != cli {
		t.Fatalf("the shell reported %q, want %q (%v)", b, cli, err)
	}
}

// runShellScript runs a login-shell launch with a fresh environment, the
// way the daemon does through pty-host.
func runShellScript(dir string, argv []string) ([]byte, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"HOME=" + dir, "TMPDIR=" + dir, "PATH=" + systemPath(), "RA_TEST_MARK=got"}
	return cmd.CombinedOutput()
}

// The CLI a shell launch resolved becomes the daemon's, so that version
// checks and the installed list follow what sessions actually run.
func TestShellReportIsLearned(t *testing.T) {
	h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"})
	bin := filepath.Join(h.dir, "codex-from-shell")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	h.s.meta.Shell = true
	h.s.mu.Unlock()
	h.s.noteShellLaunch()
	if err := os.WriteFile(filepath.Join(h.dir, shellBinFile), []byte(bin+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !h.s.checkShellStart(context.Background()) {
		t.Fatal("the session was ended although the shell reported its CLI")
	}
	if got := h.d.adapter(pb.Agent_AGENT_CODEX).Bin(); got != bin {
		t.Fatalf("the daemon still uses %q, want the CLI the shell started (%q)", got, bin)
	}
	h.s.mu.Lock()
	pending := !h.s.shellStart.IsZero()
	h.s.mu.Unlock()
	if pending {
		t.Fatal("the startup watchdog is still armed after the report")
	}
}

// A shell whose configuration never starts the CLI must not leave the
// session starting forever.
func TestStuckLoginShellEndsTheSession(t *testing.T) {
	h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex", ShellStartTimeout: 20 * time.Millisecond})
	h.s.mu.Lock()
	h.s.meta.Shell = true
	h.s.mu.Unlock()
	h.s.noteShellLaunch()
	time.Sleep(30 * time.Millisecond)

	if h.s.checkShellStart(context.Background()) {
		t.Fatal("a shell that never started the CLI did not end the session")
	}
	if !h.s.meta.Exited {
		t.Fatal("the session is not marked exited")
	}
	if h.term.Alive() {
		t.Fatal("the session's terminal was left running")
	}
	if detail := h.s.info().StatusDetail; !strings.Contains(detail, "the login shell did not start the CLI") {
		t.Fatalf("the session ended without a reason: %q", detail)
	}
}

// A shell that fails to find the CLI reports why; the client sees that
// reason as the session ends, not a session that stays starting.
func TestShellNotFoundIsReportedWhenTheSessionEnds(t *testing.T) {
	h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"})
	h.s.mu.Lock()
	h.s.meta.Shell = true
	h.s.mu.Unlock()
	msg := "the login shell could not find codex in PATH"
	if err := os.WriteFile(filepath.Join(h.dir, shellErrorFile), []byte(msg+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.term.mu.Lock()
	h.term.alive = false
	h.term.mu.Unlock()

	if h.s.checkScreen() {
		t.Fatal("the screen check did not notice the exit")
	}
	if detail := h.s.info().StatusDetail; detail != msg {
		t.Fatalf("exit detail %q, want %q", detail, msg)
	}
}

// A login-shell launch that dies before running the shell's script (a
// configuration that exits) also ends with a reason.
func TestShellThatNeverRanTheScriptReportsAReason(t *testing.T) {
	h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"})
	h.s.mu.Lock()
	h.s.meta.Shell = true
	h.s.mu.Unlock()
	h.term.mu.Lock()
	h.term.alive = false
	h.term.mu.Unlock()

	if h.s.checkScreen() {
		t.Fatal("the screen check did not notice the exit")
	}
	if detail := h.s.info().StatusDetail; detail != "the login shell did not start the CLI" {
		t.Fatalf("exit detail %q", detail)
	}
}

// A terminal-created session ending keeps its plain detail: the shell
// reasoning applies only to sessions started through the login shell.
func TestDirectSessionExitKeepsItsPlainDetail(t *testing.T) {
	h := newHarness(t, Config{Exe: "remote-agent-test", CodexBin: "/bin/echo", CodexCmd: "codex"})
	h.term.mu.Lock()
	h.term.alive = false
	h.term.mu.Unlock()

	if h.s.checkScreen() {
		t.Fatal("the screen check did not notice the exit")
	}
	if detail := h.s.info().StatusDetail; detail != "" {
		t.Fatalf("exit detail %q, want none", detail)
	}
}
