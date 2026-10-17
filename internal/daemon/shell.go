package daemon

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// A session a client created starts like a new terminal tab on the
// machine: the user's login shell runs interactively, loads the user's
// configuration, and execs the CLI. This file holds that launch — which
// shell, what it starts with, the shell code that finds the CLI, and what
// the shell reports back. Starting through the shell means the sessions
// environment is computed by the shell's own configuration every time,
// instead of the daemon's snapshot of it: a new nvm version, a moved CLI
// or a changed shell configuration applies without reinstalling anything.

// Files a login-shell launch writes in the session directory: shell-bin
// holds the CLI the shell resolved, shell-error why it found none. The
// daemon reads them to learn which CLI sessions actually run, and to tell
// a shell that never got as far as the CLI (a configuration that exits or
// waits) from a CLI that ran and ended.
const (
	shellBinFile   = "shell-bin"
	shellErrorFile = "shell-error"
)

// loginShell is the shell a client-created session starts through: the
// account's login shell, else the daemon's SHELL — a snapshot from when
// the daemon started, so it is only a fallback — else /bin/sh.
func loginShell() string {
	if s := accountShell(); runnableShell(s) {
		return s
	}
	if s := os.Getenv("SHELL"); runnableShell(s) {
		return s
	}
	return "/bin/sh"
}

func runnableShell(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// accountShell reads the login shell from the account database: the
// directory service on macOS, /etc/passwd elsewhere.
func accountShell() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("dscl", ".", "-read", "/Users/"+u.Username, "UserShell").Output()
		if err != nil {
			return ""
		}
		return parseUserShell(string(out))
	}
	return passwdShell(strconv.Itoa(os.Getuid()))
}

// parseUserShell reads the shell out of dscl's "UserShell: /bin/zsh".
func parseUserShell(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "UserShell:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// passwdShell is the login shell of uid in /etc/passwd.
func passwdShell(uid string) string {
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	return passwdShellLine(string(b), uid)
}

// passwdShellLine is the shell field of the entry with this uid.
func passwdShellLine(passwd, uid string) string {
	for _, line := range strings.Split(passwd, "\n") {
		if f := strings.Split(line, ":"); len(f) >= 7 && f[2] == uid {
			return f[6]
		}
	}
	return ""
}

// systemPath is the PATH a login shell is given before its configuration
// runs: what launchd gives a login session on macOS, and the usual system
// directories elsewhere. The shell's own files then build the real PATH;
// the daemon's PATH is deliberately not used, because it is a snapshot
// from when the daemon started.
func systemPath() string {
	if runtime.GOOS == "darwin" {
		return "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	return "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
}

// shellStartEnv is the environment a client-created session's login shell
// starts with: what a terminal would hand a new tab, not the daemon's
// environment. Identity, locale, the user session's ssh agent and the
// system PATH come along so the shell starts from a sane state;
// everything else it computes itself.
func shellStartEnv(shell string) map[string]string {
	env := map[string]string{}
	for _, k := range []string{"HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES",
		"__CF_USER_TEXT_ENCODING", "SSH_AUTH_SOCK"} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	if u, err := user.Current(); err == nil {
		if env["HOME"] == "" {
			env["HOME"] = u.HomeDir
		}
		if env["USER"] == "" {
			env["USER"] = u.Username
		}
		if env["LOGNAME"] == "" {
			env["LOGNAME"] = u.Username
		}
	}
	if env["TMPDIR"] == "" {
		env["TMPDIR"] = os.TempDir()
	}
	env["SHELL"] = shell
	env["PATH"] = systemPath()
	return env
}

// shellFamily is how a shell takes the CLI and its arguments.
type shellFamily int

const (
	// shellPosix shells take them as positional parameters.
	shellPosix shellFamily = iota
	// shellFish takes them in its argument variable.
	shellFish
)

func shellFamilyOf(shell string) shellFamily {
	if filepath.Base(shell) == "fish" {
		return shellFish
	}
	return shellPosix
}

// shellLaunchArgv starts cmd with args through the user's login shell, in
// dir (the session's directory, which the shell reports into). The shell
// runs the script as an interactive login shell, so the user's
// configuration is loaded the way it is in a terminal. The CLI's command
// and arguments are positional parameters, never spliced into the script,
// so no argument needs quoting.
func shellLaunchArgv(shell, dir, cmd string, args []string) []string {
	argv := []string{shell, "-l", "-i", "-c", shellLaunchScript(shell, dir)}
	if shellFamilyOf(shell) == shellPosix {
		argv = append(argv, filepath.Base(shell)) // $0, as a terminal's login shell would have it
	}
	return append(append(argv, cmd), args...)
}

// shellLaunchScript is the shell code that resolves the CLI the way the
// user would (the shell's own PATH), reports what it found, and execs it,
// replacing the shell so the CLI remains the pane's process. It reports
// before it execs, so a shell that never ran the code left no report, and
// a plain name (an alias or function, not a file) reports nothing the
// daemon can run.
func shellLaunchScript(shell, dir string) string {
	bin, errFile := shellQuote(filepath.Join(dir, shellBinFile)), shellQuote(filepath.Join(dir, shellErrorFile))
	if shellFamilyOf(shell) == shellFish {
		return strings.NewReplacer("{bin}", bin, "{error}", errFile).Replace(fishLaunchScript)
	}
	return strings.NewReplacer("{bin}", bin, "{error}", errFile).Replace(posixLaunchScript)
}

const posixLaunchScript = `_ra_cmd=$1
shift
if _ra_bin=$(command -v -- "$_ra_cmd" 2>/dev/null); then
	case $_ra_bin in
	/*) printf '%s\n' "$_ra_bin" > {bin}; exec "$_ra_bin" "$@";;
	esac
	printf '%s\n' "$_ra_cmd" > {bin}
	exec "$_ra_cmd" "$@"
fi
printf 'the login shell could not find %s in PATH\n' "$_ra_cmd" > {error}
echo "remote-agent: $_ra_cmd was not found in PATH; check your shell configuration" >&2
exit 127
`

const fishLaunchScript = `set -l _ra_cmd $argv[1]
if not command -v -- $_ra_cmd >/dev/null
	printf 'the login shell could not find %s in PATH\n' $_ra_cmd > {error}
	echo "remote-agent: $_ra_cmd was not found in PATH; check your shell configuration" >&2
	exit 127
end
set -l _ra_bin (command -v -- $_ra_cmd)
printf '%s\n' $_ra_bin > {bin}
exec $_ra_bin $argv[2..-1]
`

// shellQuote makes s safe to put in a script as one word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellReport reads what a login-shell launch left in the session
// directory. ok is false while the shell has not got that far yet.
func shellReport(dir string) (bin, errMsg string, ok bool) {
	if b, err := os.ReadFile(filepath.Join(dir, shellBinFile)); err == nil {
		return strings.TrimSpace(string(b)), "", true
	}
	if b, err := os.ReadFile(filepath.Join(dir, shellErrorFile)); err == nil {
		return "", strings.TrimSpace(string(b)), true
	}
	return "", "", false
}

// clearShellReport removes a previous launch's report, so that a session
// resumed in the same directory does not read it as this launch's.
func clearShellReport(dir string) {
	for _, name := range []string{shellBinFile, shellErrorFile} {
		_ = os.Remove(filepath.Join(dir, name))
	}
}
