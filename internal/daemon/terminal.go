package daemon

import (
	"context"
	"errors"
	"net"
	"syscall"
	"time"

	"github.com/Lewin671/remote-agent/internal/ptyhost"
	"github.com/Lewin671/remote-agent/internal/tmux"
)

// terminal is the session's view of its tmux pane and PTY host. Tests
// replace it with a fake.
type terminal interface {
	// Capture returns the screen with SGR sequences kept.
	Capture() (string, error)
	// Poll returns the visible screen and liveness in one bounded operation.
	Poll(context.Context) (string, bool, error)
	// Alive reports whether the tmux session still exists.
	Alive() bool
	// HostAlive reports whether the PTY-host control socket still accepts connections.
	HostAlive(context.Context) (bool, error)
	// Kill ends the session.
	Kill() error
	// Control opens a connection to the PTY host.
	Control() (control, error)
}

// control is a connection to the PTY host.
type control interface {
	Hold(d time.Duration) error
	Release() error
	Write(b []byte) error
	Close() error
}

type tmuxTerminal struct {
	tmux   *tmux.Client
	name   string
	socket string
}

func (t *tmuxTerminal) Capture() (string, error) { return t.tmux.Capture(t.name, true) }
func (t *tmuxTerminal) Poll(ctx context.Context) (string, bool, error) {
	return t.tmux.Poll(ctx, t.name)
}
func (t *tmuxTerminal) Alive() bool { return t.tmux.HasSession(t.name) }
func (t *tmuxTerminal) Kill() error { return t.tmux.KillSession(t.name) }

func (t *tmuxTerminal) HostAlive(ctx context.Context) (bool, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", t.socket)
	if err == nil {
		_ = c.Close()
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOTSOCK) ||
		errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENAMETOOLONG) {
		return false, nil
	}
	return false, err
}

func (t *tmuxTerminal) Control() (control, error) {
	c, err := ptyhost.Dial(t.socket)
	if err != nil {
		return nil, err
	}
	return ptyControl{c}, nil
}

type ptyControl struct{ c *ptyhost.Conn }

func (p ptyControl) Hold(d time.Duration) error { _, err := p.c.Hold(d); return err }
func (p ptyControl) Release() error             { return p.c.Release() }
func (p ptyControl) Write(b []byte) error       { return p.c.Write(b) }
func (p ptyControl) Close() error               { return p.c.Close() }
