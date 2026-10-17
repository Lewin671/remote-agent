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
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// MaxHold bounds how long local input can be held, so that a stuck daemon
// never locks the user out of their own terminal.
const MaxHold = 10 * time.Second

// Leave time for the control reply before Conn's five-second deadline.
const remoteWriteTimeout = 2 * time.Second

const (
	// Both CLIs persist history as they work; allow time for orderly hangup
	// handling and a final flush before treating the process as stuck.
	childSignalGrace = 5 * time.Second
	// Give helpers time to exit after the process group receives SIGTERM.
	groupTermGrace = 2 * time.Second
)

type runOptions struct {
	childSignalGrace time.Duration
	groupTermGrace   time.Duration
}

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

	mu             sync.Mutex    // guards the fields below, never held during PTY I/O
	writing        chan struct{} // nil when idle; closed when the active writer finishes
	held           bool
	holder         int // connection that holds input, 0 when not held
	nextConn       int
	holdTimer      *time.Timer
	holdGeneration uint64 // distinguishes renewed leases from already-running timer callbacks
	pending        []byte
	lastLocal      time.Time
}

// Run starts argv in a PTY attached to the process's own terminal, serves
// the control socket at socketPath, and returns the child's exit code.
func Run(socketPath string, argv []string) (int, error) {
	return run(socketPath, argv, os.Stdin, os.Stdout, runOptions{
		childSignalGrace: childSignalGrace,
		groupTermGrace:   groupTermGrace,
	})
}

func run(socketPath string, argv []string, stdin *os.File, stdout io.Writer, shutdown runOptions) (int, error) {
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
	// pty returns a blocking file on macOS. Register a nonblocking duplicate
	// with Go's poller so write deadlines actually interrupt a full PTY.
	pollable, err := pollablePTY(ptmx)
	_ = ptmx.Close()
	if err != nil {
		_ = signalProcessGroup(cmd, syscall.SIGKILL)
		_ = cmd.Wait()
		return 1, err
	}
	ptmx = pollable
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
	go drainOutput(stdout, ptmx)

	ln, err := listen(socketPath)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return 1, err
	}
	defer ln.Close()
	go h.serve(ln)

	go h.pumpStdin(stdin)

	err = waitForChild(cmd, sigs, shutdown)
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

// drainOutput copies the CLI's output to the pane. Once the pane is gone it
// keeps reading and drops the output: a CLI whose terminal fills up blocks
// in its writes and never gets to exit.
func drainOutput(stdout io.Writer, ptmx io.Reader) {
	if _, err := io.Copy(stdout, ptmx); err != nil {
		_, _ = io.Copy(io.Discard, ptmx)
	}
}

// waitForChild preserves the CLI's exit result and always cleans up the
// process group created by pty.Start, including after a prompt leader exit.
func waitForChild(cmd *exec.Cmd, sigs <-chan os.Signal, options runOptions) error {
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	var err error
	var exited bool
	select {
	case err = <-wait:
		exited = true
	case sig := <-sigs:
		_ = cmd.Process.Signal(sig)
		err, exited = waitForExit(wait, sigs, cmd, options.childSignalGrace)
	}
	return finishProcessGroup(cmd, wait, sigs, options.groupTermGrace, err, exited)
}

// The leader's Wait result says nothing about its helpers. Give the group
// its own TERM grace period, then KILL any survivors regardless of when the
// leader exits. Only the process group of this particular launch is signaled.
func finishProcessGroup(cmd *exec.Cmd, wait <-chan error, sigs <-chan os.Signal, grace time.Duration, result error, exited bool) error {
	if err := signalProcessGroup(cmd, syscall.SIGTERM); err != nil && !exited {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	if exited {
		wait = nil
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
cleanup:
	for !errors.Is(signalProcessGroup(cmd, 0), syscall.ESRCH) {
		select {
		case result = <-wait:
			exited, wait = true, nil
		case sig := <-sigs:
			if !exited {
				_ = cmd.Process.Signal(sig)
			}
		case <-tick.C:
		case <-timer.C:
			break cleanup
		}
	}
	if err := signalProcessGroup(cmd, syscall.SIGKILL); err != nil && !exited {
		_ = cmd.Process.Kill()
	}
	if !exited {
		result = <-wait
	}
	return result
}

func waitForExit(wait <-chan error, sigs <-chan os.Signal, cmd *exec.Cmd, d time.Duration) (error, bool) {
	if d <= 0 {
		select {
		case err := <-wait:
			return err, true
		default:
			return nil, false
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case err := <-wait:
			return err, true
		case sig := <-sigs:
			_ = cmd.Process.Signal(sig)
		case <-timer.C:
			return nil, false
		}
	}
}

func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	return syscall.Kill(-cmd.Process.Pid, sig)
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
	unixLn, ok := ln.(*net.UnixListener)
	if !ok {
		_ = ln.Close()
		return nil, errors.New("pty-host: control listener is not a Unix socket")
	}
	unixLn.SetUnlinkOnClose(false)
	bound, err := os.Lstat(path)
	if err != nil {
		_ = unixLn.Close()
		return nil, err
	}
	owned := &socketListener{UnixListener: unixLn, path: path, bound: bound}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = owned.Close()
		return nil, err
	}
	return owned, nil
}

// socketListener disables net.UnixListener's path-based unlink on close.
// A newer pty-host may already have replaced path while this listener was
// serving, so only unlink the socket inode this listener originally bound.
type socketListener struct {
	*net.UnixListener
	path  string
	bound os.FileInfo
}

func (l *socketListener) Close() error {
	err := l.UnixListener.Close()
	removeSocketIfSame(l.path, l.bound)
	return err
}

func removeSocketIfSame(path string, bound os.FileInfo) {
	if bound == nil {
		return
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(bound, current) {
		return
	}
	_ = os.Remove(path)
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

// local handles bytes typed in the local terminal. A single writer drains
// them outside mu; input arriving during a hold or a write stays ordered.
func (h *Host) local(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastLocal = time.Now()
	h.pending = append(h.pending, b...)
	h.flushLocalLocked()
}

// flushLocalLocked starts at most one writer. Local bytes have no expiry:
// a CLI that resumes reading must still receive every buffered keystroke.
func (h *Host) flushLocalLocked() {
	if h.held || h.writing != nil || len(h.pending) == 0 {
		return
	}
	b := h.pending
	h.pending = nil
	h.writing = make(chan struct{})
	go func() {
		_, _ = h.ptmx.Write(b)
		h.mu.Lock()
		defer h.mu.Unlock()
		close(h.writing)
		h.writing = nil
		h.flushLocalLocked()
	}()
}

// errHeld is returned when another connection already holds input.
var errHeld = errors.New("input is held by another controller")

// Waiting for prior input shares the same bound as the remote write itself.
var errWriting = errors.New("input write deadline exceeded waiting for previous input")

// waitForWriterLocked returns with mu held. Renewing the current owner's
// lease never waits for I/O; a new hold or write must follow all prior bytes.
// Each wake-up rechecks ownership and the active writer, since another local
// batch or controller may have acquired the input in the meantime.
func (h *Host) waitForWriterLocked(conn int, renewHold bool, deadline time.Time) error {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		if h.held && h.holder != conn {
			return errHeld
		}
		if renewHold && h.held && h.holder == conn {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errWriting
		}
		if h.writing == nil {
			return nil
		}
		done := h.writing
		h.mu.Unlock()
		timedOut := false
		select {
		case <-done:
		case <-timer.C:
			timedOut = true
		}
		h.mu.Lock()
		if timedOut {
			return errWriting
		}
	}
}

// hold holds local input on behalf of connection conn. Only one connection
// may hold at a time, so that one controller's release cannot cut short
// another's check-and-write.
func (h *Host) hold(conn int, d time.Duration) error {
	if d <= 0 || d > MaxHold {
		d = MaxHold
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.waitForWriterLocked(conn, true, time.Now().Add(remoteWriteTimeout)); err != nil {
		return err
	}
	h.held, h.holder = true, conn
	if h.holdTimer != nil {
		h.holdTimer.Stop()
	}
	h.holdGeneration++
	generation := h.holdGeneration
	h.holdTimer = time.AfterFunc(d, func() { h.expire(conn, generation) })
	return nil
}

// expire releases only the lease that scheduled this callback. Timer.Stop
// cannot cancel a callback that has already started and is waiting for mu;
// that callback must not release a newer lease of the same controller.
func (h *Host) expire(conn int, generation uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.holdGeneration == generation {
		h.releaseLocked(conn)
	}
}

// release ends conn's hold and flushes buffered local input. It does
// nothing if conn does not hold input.
func (h *Host) release(conn int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.releaseLocked(conn)
}

func (h *Host) releaseLocked(conn int) {
	if !h.held || h.holder != conn {
		return
	}
	if h.holdTimer != nil {
		h.holdTimer.Stop()
		h.holdTimer = nil
	}
	h.held, h.holder = false, 0
	h.flushLocalLocked()
}

// write sends b to the CLI. While input is held, only the holder may
// write.
func (h *Host) write(conn int, b []byte) error {
	// Queueing and I/O consume one shared budget, so contention cannot push
	// the reply past the control connection's five-second deadline.
	deadline := time.Now().Add(remoteWriteTimeout)
	h.mu.Lock()
	if err := h.waitForWriterLocked(conn, false, deadline); err != nil {
		h.mu.Unlock()
		return err
	}
	h.writing = make(chan struct{})
	h.mu.Unlock()

	// A timeout may follow a partial write. Never retry its remainder: that
	// could enter a later draft after the controller has reported failure.
	err := h.ptmx.SetWriteDeadline(deadline)
	if err == nil {
		_, err = h.ptmx.Write(b)
	}
	_ = h.ptmx.SetWriteDeadline(time.Time{})
	h.mu.Lock()
	close(h.writing)
	h.writing = nil
	h.flushLocalLocked()
	h.mu.Unlock()
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return errors.New("PTY write deadline exceeded; input may have been partially written")
	}
	return err
}

// pollablePTY transfers I/O to a descriptor registered with Go's poller.
// NewFile preserves O_NONBLOCK even when resize ioctls call File.Fd().
func pollablePTY(f *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	p := os.NewFile(uintptr(fd), f.Name())
	if err := p.SetWriteDeadline(time.Time{}); err != nil {
		_ = p.Close()
		return nil, err
	}
	return p, nil
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
