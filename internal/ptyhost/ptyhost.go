// Package ptyhost runs the agent CLI in a pseudo-terminal inside the tmux
// pane and arbitrates its input.
//
// Keystrokes from the local terminal pass straight through. The daemon
// connects to a control socket to inject remote input: it first holds local
// input, checks the screen, writes the input, and releases. Local bytes that
// arrive while input is held are buffered, never dropped or interleaved with
// the injected bytes.
package ptyhost

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// MaxHold bounds how long local input can be held, so that a stuck daemon
// never locks the user out of their own terminal.
const MaxHold = 10 * time.Second

// Request is one control message, sent as a JSON line.
type Request struct {
	Op     string `json:"op"` // "hold", "release", "write", "status"
	Data   []byte `json:"data,omitempty"`
	HoldMS int    `json:"hold_ms,omitempty"`
}

// Response answers one Request.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	PID   int    `json:"pid,omitempty"`
	Held  bool   `json:"held,omitempty"`
	// Unix milliseconds of the last local keystroke, 0 if none yet.
	LastLocalInput int64 `json:"last_local_input,omitempty"`
}

// Host owns the child process and its PTY.
type Host struct {
	ptmx *os.File
	cmd  *exec.Cmd

	mu        sync.Mutex // guards writes to ptmx and the fields below
	held      bool
	holder    int // connection that holds input, 0 when not held
	nextConn  int
	holdTimer *time.Timer
	pending   []byte
	lastLocal time.Time
}

// Run starts argv in a PTY attached to the process's own terminal, serves
// the control socket at socketPath, and returns the child's exit code.
func Run(socketPath string, argv []string) (int, error) {
	return run(socketPath, argv, os.Stdin, os.Stdout)
}

func run(socketPath string, argv []string, stdin *os.File, stdout io.Writer) (int, error) {
	if len(argv) == 0 {
		return 2, errors.New("pty-host: no command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = os.Environ()

	stdinFD := int(stdin.Fd())
	isTTY := term.IsTerminal(stdinFD)
	var size *pty.Winsize
	if isTTY {
		size, _ = pty.GetsizeFull(stdin)
	}
	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return 1, err
	}
	defer ptmx.Close()

	h := &Host{ptmx: ptmx, cmd: cmd}

	if isTTY {
		old, err := term.MakeRaw(stdinFD)
		if err == nil {
			defer term.Restore(stdinFD, old)
		}
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				_ = pty.InheritSize(stdin, ptmx)
			}
		}()
	}
	// tmux sends SIGHUP when the session is killed; pass it on.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()

	ln, err := listen(socketPath)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return 1, err
	}
	defer os.Remove(socketPath)
	defer ln.Close()
	go h.serve(ln)

	go func() { _, _ = io.Copy(stdout, ptmx) }()
	go h.pumpStdin(stdin)

	err = cmd.Wait()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

func listen(path string) (net.Listener, error) {
	if err := paths.EnsureDir(dirOf(path)); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		os.Remove(path)
		return nil, err
	}
	return ln, nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

func (h *Host) pumpStdin(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			h.local(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// local handles bytes typed in the local terminal.
func (h *Host) local(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastLocal = time.Now()
	if h.held {
		h.pending = append(h.pending, b...)
		return
	}
	_, _ = h.ptmx.Write(b)
}

// errHeld is returned when another connection already holds input.
var errHeld = errors.New("input is held by another controller")

// hold holds local input on behalf of connection conn. Only one connection
// may hold at a time, so that one controller's release cannot cut short
// another's check-and-write.
func (h *Host) hold(conn int, d time.Duration) error {
	if d <= 0 || d > MaxHold {
		d = MaxHold
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held && h.holder != conn {
		return errHeld
	}
	h.held, h.holder = true, conn
	if h.holdTimer != nil {
		h.holdTimer.Stop()
	}
	h.holdTimer = time.AfterFunc(d, func() { h.release(conn) })
	return nil
}

// release ends conn's hold and flushes buffered local input. It does
// nothing if conn does not hold input.
func (h *Host) release(conn int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.held || h.holder != conn {
		return
	}
	if h.holdTimer != nil {
		h.holdTimer.Stop()
		h.holdTimer = nil
	}
	h.held, h.holder = false, 0
	if len(h.pending) > 0 {
		_, _ = h.ptmx.Write(h.pending)
		h.pending = nil
	}
}

// write sends b to the CLI. While input is held, only the holder may
// write.
func (h *Host) write(conn int, b []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held && h.holder != conn {
		return errHeld
	}
	_, err := h.ptmx.Write(b)
	return err
}

func (h *Host) status() Response {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := Response{OK: true, Held: h.held}
	if h.cmd.Process != nil {
		r.PID = h.cmd.Process.Pid
	}
	if !h.lastLocal.IsZero() {
		r.LastLocalInput = h.lastLocal.UnixMilli()
	}
	return r
}

func (h *Host) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go h.handle(c)
	}
}

func (h *Host) handle(c net.Conn) {
	defer c.Close()
	h.mu.Lock()
	h.nextConn++
	conn := h.nextConn
	h.mu.Unlock()
	// A controller that disconnects while holding must not leave the
	// terminal blocked.
	defer h.release(conn)
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(c)
	for sc.Scan() {
		var req Request
		var resp Response
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			resp = Response{Error: "bad request: " + err.Error()}
		} else {
			switch req.Op {
			case "hold":
				if err := h.hold(conn, time.Duration(req.HoldMS)*time.Millisecond); err != nil {
					resp = Response{Error: err.Error()}
				} else {
					resp = h.status()
				}
			case "release":
				h.release(conn)
				resp = h.status()
			case "write":
				if err := h.write(conn, req.Data); err != nil {
					resp = Response{Error: err.Error()}
				} else {
					resp = Response{OK: true}
				}
			case "status":
				resp = h.status()
			default:
				resp = Response{Error: "unknown op " + req.Op}
			}
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}
