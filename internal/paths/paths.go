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

// Lock is held by the running daemon.
func Lock() string { return filepath.Join(Dir(), "daemon.lock") }

// SessionsDir holds one directory per session.
func SessionsDir() string { return filepath.Join(Dir(), "sessions") }

// SessionDir is the directory of one session.
func SessionDir(id string) string { return filepath.Join(SessionsDir(), id) }

// PTYSocket is the control socket of a session's PTY host. Unix socket
// paths are limited to about 104 bytes on macOS, so it uses a short name.
func PTYSocket(id string) string {
	return filepath.Join(Dir(), "run", id[:min(len(id), 12)]+".sock")
}

// TmuxSocketName is the tmux -L socket name used for all sessions.
func TmuxSocketName() string {
	if n := os.Getenv("REMOTE_AGENT_TMUX_SOCKET"); n != "" {
		return n
	}
	return "remote-agent"
}

// EnsureDir creates dir with owner-only permissions.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}
