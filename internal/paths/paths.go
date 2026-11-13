// Package paths locates remote-agent's files on the local machine.
package paths

import (
	"os"
	"path/filepath"
)

// Dir returns the state directory, $REMOTE_AGENT_HOME or ~/.remote-agent.
func Dir() string {
	if d := os.Getenv("REMOTE_AGENT_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".remote-agent")
}

// Socket is the daemon's unix socket.
func Socket() string { return filepath.Join(Dir(), "daemon.sock") }

// Account is the machine's account membership, including the root key.
func Account() string { return filepath.Join(Dir(), "account.json") }

// SessionsDir holds one directory per session.
func SessionsDir() string { return filepath.Join(Dir(), "sessions") }

// SessionDir is the directory of one session.
func SessionDir(id string) string { return filepath.Join(SessionsDir(), id) }

// TmuxSocketName is the tmux -L socket name used for all sessions.
func TmuxSocketName() string {
	if n := os.Getenv("REMOTE_AGENT_TMUX_SOCKET"); n != "" {
		return n
	}
	return "remote-agent"
}

// EnsureDir creates dir with owner-only permissions.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}
