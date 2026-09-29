// Package tmux runs tmux commands against remote-agent's own tmux socket.
//
// Every call passes an argument vector to the tmux binary; nothing is
// interpreted by a shell.
package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// MinVersion is the oldest tmux release remote-agent supports.
const MinVersion = "3.2"

// Client talks to one tmux server, selected with -L.
type Client struct {
	Bin    string // tmux binary; "tmux" when empty
	Socket string // -L socket name
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "tmux"
}

// Command returns the full argv for a tmux command on this socket.
func (c *Client) Command(args ...string) []string {
	return append([]string{c.bin(), "-L", c.Socket}, args...)
}

func (c *Client) run(args ...string) (string, error) {
	argv := c.Command(args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("tmux %s: %s", args[0], msg)
	}
	return stdout.String(), nil
}

var versionRE = regexp.MustCompile(`(\d+)\.(\d+)`)

// CheckVersion verifies that tmux exists and is at least MinVersion.
func (c *Client) CheckVersion() (string, error) {
	out, err := exec.Command(c.bin(), "-V").Output()
	if err != nil {
		return "", fmt.Errorf("tmux not found: %w", err)
	}
	v := strings.TrimSpace(string(out))
	if !VersionAtLeast(v, MinVersion) {
		return v, fmt.Errorf("%s is too old, need tmux %s or newer", v, MinVersion)
	}
	return v, nil
}

// VersionAtLeast reports whether the version in s (e.g. "tmux 3.7c") is at
// least min (e.g. "3.2").
func VersionAtLeast(s, min string) bool {
	m := versionRE.FindStringSubmatch(s)
	w := versionRE.FindStringSubmatch(min)
	if m == nil || w == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	wmajor, _ := strconv.Atoi(w[1])
	wminor, _ := strconv.Atoi(w[2])
	return major > wmajor || major == wmajor && minor >= wminor
}

// NewSessionOptions describes a detached session to create.
type NewSessionOptions struct {
	Name   string
	Dir    string
	Width  int
	Height int
	Env    map[string]string
	Argv   []string // command run in the pane, exec'd without a shell
}

// NewSession creates a detached session running opts.Argv.
func (c *Client) NewSession(opts NewSessionOptions) error {
	args := []string{"new-session", "-d", "-s", opts.Name}
	if opts.Dir != "" {
		args = append(args, "-c", opts.Dir)
	}
	if opts.Width > 0 && opts.Height > 0 {
		args = append(args, "-x", strconv.Itoa(opts.Width), "-y", strconv.Itoa(opts.Height))
	}
	keys := make([]string, 0, len(opts.Env))
	for k := range opts.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+opts.Env[k])
	}
	args = append(args, "--")
	args = append(args, opts.Argv...)
	_, err := c.run(args...)
	return err
}

// Run runs a tmux command against this server and returns its output.
func (c *Client) Run(args ...string) (string, error) { return c.run(args...) }

// Display formats information about a session's pane, as display-message.
func (c *Client) Display(session, format string) (string, error) {
	out, err := c.run("display-message", "-p", "-t", exactPane(session), format)
	return strings.TrimSpace(out), err
}

// SetSessionOption sets a session option such as a user option "@name".
func (c *Client) SetSessionOption(session, key, value string) error {
	_, err := c.run("set-option", "-t", exactPane(session), key, value)
	return err
}

// SessionInfo is one line of list-sessions.
type SessionInfo struct {
	Name      string
	SessionID string // value of the @ra_session_id option
	PanePID   int
}

// ErrNoServer means the tmux server for the socket is not running.
var ErrNoServer = errors.New("tmux server not running")

// ListSessions lists sessions on the socket.
func (c *Client) ListSessions() ([]SessionInfo, error) {
	out, err := c.run("list-sessions", "-F", "#{session_name}\t#{@ra_session_id}\t#{pane_pid}")
	if err != nil {
		if isNoServer(err) {
			return nil, nil
		}
		return nil, err
	}
	var infos []SessionInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			// tmux without a UTF-8 locale prints tabs as "_".
			return nil, fmt.Errorf("tmux list-sessions: unexpected line %q; is the locale UTF-8?", line)
		}
		pid, _ := strconv.Atoi(f[2])
		infos = append(infos, SessionInfo{Name: f[0], SessionID: f[1], PanePID: pid})
	}
	return infos, nil
}

// HasSession reports whether the named session exists.
func (c *Client) HasSession(name string) bool {
	_, err := c.run("has-session", "-t", exactTarget(name))
	return err == nil
}

// KillSession kills the named session.
func (c *Client) KillSession(name string) error {
	_, err := c.run("kill-session", "-t", exactTarget(name))
	return err
}

// Capture returns the visible contents of the session's active pane. With
// escapes, SGR attributes are kept so that dim text can be told apart.
func (c *Client) Capture(name string, escapes bool) (string, error) {
	args := []string{"capture-pane", "-p", "-t", exactPane(name)}
	if escapes {
		args = append(args, "-e")
	}
	return c.run(args...)
}

// AttachArgv returns the argv that attaches the current terminal to name.
func (c *Client) AttachArgv(name string) []string {
	return c.Command("attach-session", "-t", exactTarget(name))
}

// exactTarget names a session exactly, without tmux's prefix matching.
func exactTarget(name string) string { return "=" + name }

// exactPane names the active pane of a session exactly. Commands that take
// a pane or window target need the trailing colon.
func exactPane(name string) string { return "=" + name + ":" }

func isNoServer(err error) bool {
	s := err.Error()
	return strings.Contains(s, "no server running") ||
		strings.Contains(s, "error connecting to") ||
		strings.Contains(s, "No such file or directory")
}
