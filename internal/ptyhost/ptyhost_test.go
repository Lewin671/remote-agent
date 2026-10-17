package ptyhost

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "ra-pty")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func waitForSocket(t *testing.T, path string) *Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := Dial(path); err == nil {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("socket %s never came up", path)
	return nil
}

// A command started with an environment of its own gets exactly that
// environment: nothing of the host process's leaks into it. The login
// shell a client-created session runs must not inherit the daemon's.
func TestRunEnvReplacesTheHostEnvironment(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "c.sock")
	out := filepath.Join(dir, "out")
	t.Setenv("RA_HOST_ONLY", "host")

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinW.Close()
	code, err := run(sock, []string{"/bin/sh", "-c", `printf '%s|%s' "$RA_ONLY" "$RA_HOST_ONLY" > ` + out},
		stdinR, io.Discard, runOptions{env: []string{"RA_ONLY=session", "PATH=" + os.Getenv("PATH")}})
	if err != nil || code != 0 {
		t.Fatalf("run: %d %v", code, err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "session|" {
		t.Fatalf("the command saw %q, want the given environment only", got)
	}
}

// Local bytes typed while input is held must reach the child after the
// injected bytes, not interleaved with them and not lost.
func TestHoldBuffersLocalInput(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "c.sock")
	out := filepath.Join(dir, "out")
	ready := filepath.Join(dir, "ready")

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinW.Close()

	done := make(chan int, 1)
	go func() {
		code, err := run(sock, []string{"/bin/sh", "-c", "stty raw -echo; printf ready > " + ready + "; head -c 2 > " + out + "; printf read > " + ready + ".prefix; head -c 10 >> " + out}, stdinR, io.Discard, runOptions{})
		if err != nil {
			t.Error(err)
		}
		done <- code
	}()

	c := waitForSocket(t, sock)
	defer c.Close()
	if err := waitForFile(ready, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	if _, err := stdinW.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	if err := waitForFile(ready+".prefix", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Hold(30 * time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := stdinW.Write([]byte("cdef")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := c.Write([]byte("XYZWVU")); err != nil {
		t.Fatal(err)
	}
	if err := c.Release(); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("child exit code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit")
	}
	got, _ := os.ReadFile(out)
	if string(got) != "abXYZWVUcdef" {
		t.Fatalf("child read %q, want %q", got, "abXYZWVUcdef")
	}
}

// Closing the control connection releases a hold it made.
func TestDisconnectReleasesHold(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "c.sock")
	out := filepath.Join(dir, "out")
	ready := filepath.Join(dir, "ready")
	stdinR, stdinW, _ := os.Pipe()
	defer stdinW.Close()
	done := make(chan int, 1)
	go func() {
		code, _ := run(sock, []string{"/bin/sh", "-c", "stty raw -echo; printf ready > " + ready + "; head -c 3 > " + out}, stdinR, io.Discard, runOptions{})
		done <- code
	}()
	c := waitForSocket(t, sock)
	if err := waitForFile(ready, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Hold(30 * time.Second); err != nil {
		t.Fatal(err)
	}
	stdinW.Write([]byte("abc"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := c.Status()
		if err != nil {
			t.Fatal(err)
		}
		if st.LastLocalInput != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("local bytes did not reach the host")
		}
		time.Sleep(time.Millisecond)
	}
	c.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("held input was not released after disconnect")
	}
	got, _ := os.ReadFile(out)
	if string(got) != "abc" {
		t.Fatalf("got %q", got)
	}
}

// A second controller can neither take over nor break a hold.
func TestHoldIsExclusive(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "c.sock")
	stdinR, stdinW, _ := os.Pipe()
	defer stdinW.Close()
	go run(sock, []string{"/bin/sh", "-c", "sleep 5"}, stdinR, io.Discard, runOptions{})
	a := waitForSocket(t, sock)
	defer a.Close()
	b := waitForSocket(t, sock)
	defer b.Close()
	if _, err := a.Hold(time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Hold(time.Second); err == nil {
		t.Fatal("second controller took the hold")
	}
	if err := b.Write([]byte("x")); err == nil {
		t.Fatal("second controller wrote during the hold")
	}
	if err := b.Release(); err != nil {
		t.Fatal(err)
	}
	if st, _ := a.Status(); !st.Held {
		t.Fatal("another controller's release ended the hold")
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Hold(time.Second); err != nil {
		t.Fatalf("hold after release: %v", err)
	}
}

func TestListenMakesControlDirectoryPrivate(t *testing.T) {
	dir := shortTempDir(t)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "private.sock")
	ln, err := listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	defer os.Remove(socket)
	for _, item := range []struct {
		path string
		perm os.FileMode
	}{{dir, 0o700}, {socket, 0o600}} {
		fi, err := os.Stat(item.path)
		if err != nil || fi.Mode().Perm() != item.perm {
			t.Fatalf("permissions for %s: %v, %v", item.path, fi, err)
		}
	}
}

func TestExitedHostDoesNotRemoveReplacedSocket(t *testing.T) {
	h := startRunHelper(t, "prompt-hup")
	replacement, err := listen(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	want, err := os.Stat(h.socket)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	if err := h.wait(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(h.socket)
	if err != nil {
		t.Fatalf("new listener's socket was removed when the old host exited: %v", err)
	}
	if !os.SameFile(want, got) {
		t.Fatal("socket path no longer refers to the replacement listener")
	}
	c, err := Dial(h.socket)
	if err != nil {
		t.Fatalf("replacement socket no longer accepts connections: %v", err)
	}
	_ = c.Close()
}

// An expiry callback can already be running when renewal calls Timer.Stop.
// Reproduce its eventual arrival directly: an old callback must not release
// a renewed hold, or local keystrokes can enter a checked remote draft.
func TestExpiredCallbackCannotReleaseRenewedHold(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	h := &Host{ptmx: w, cmd: &exec.Cmd{}}
	if err := h.hold(1, time.Second); err != nil {
		t.Fatal(err)
	}
	old := h.holdGeneration
	h.local([]byte("local draft"))
	if err := h.hold(1, time.Second); err != nil {
		t.Fatal(err)
	}
	h.expire(1, old)
	if !h.status().Held || string(h.pending) != "local draft" {
		t.Fatal("expired callback released renewed lease and flushed local input")
	}
	h.expire(1, h.holdGeneration)
	if h.status().Held {
		t.Fatal("current lease did not expire")
	}
	b := make([]byte, len("local draft"))
	if _, err := io.ReadFull(r, b); err != nil || string(b) != "local draft" {
		t.Fatalf("buffered input = %q, err = %v", b, err)
	}
}

func TestExpiredCallbackCannotReleaseReacquiredHold(t *testing.T) {
	h := &Host{cmd: &exec.Cmd{}}
	if err := h.hold(1, time.Second); err != nil {
		t.Fatal(err)
	}
	old := h.holdGeneration
	h.release(1)
	if err := h.hold(1, time.Second); err != nil {
		t.Fatal(err)
	}
	defer h.release(1)
	h.expire(1, old)
	if !h.status().Held {
		t.Fatal("expired callback released reacquired hold")
	}
}

type failingWriter struct{ marker string }

func (w failingWriter) Write([]byte) (int, error) {
	_ = os.WriteFile(w.marker, []byte("failed"), 0o600)
	return 0, errors.New("stdout closed")
}

type runHelper struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	done       chan struct{}
	waitErr    error
	childPID   int
	socket     string
	result     string
	marker     string
	complete   string
	term       string
	child      string
	grandchild string
}

func startRunHelper(t *testing.T, mode string) *runHelper {
	t.Helper()
	dir := shortTempDir(t)
	h := &runHelper{
		socket:     filepath.Join(dir, "control.sock"),
		result:     filepath.Join(dir, "result"),
		marker:     filepath.Join(dir, "marker"),
		complete:   filepath.Join(dir, "complete"),
		term:       filepath.Join(dir, "term"),
		child:      filepath.Join(dir, "child.pid"),
		grandchild: filepath.Join(dir, "grandchild.pid"),
	}
	h.cmd = exec.Command(os.Args[0], "-test.run=^TestPtyhostRunHelper$")
	h.cmd.Env = append(os.Environ(),
		"PTYHOST_TEST_MODE="+mode,
		"PTYHOST_TEST_SOCKET="+h.socket,
		"PTYHOST_TEST_RESULT="+h.result,
		"PTYHOST_TEST_MARKER="+h.marker,
		"PTYHOST_TEST_COMPLETE="+h.complete,
		"PTYHOST_TEST_TERM="+h.term,
		"PTYHOST_TEST_CHILD="+h.child,
		"PTYHOST_TEST_GRANDCHILD="+h.grandchild,
	)
	var err error
	h.stdin, err = h.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.stdin.Close() })
	h.cmd.Stdout, h.cmd.Stderr = io.Discard, io.Discard
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h.done = make(chan struct{})
	go func() {
		h.waitErr = h.cmd.Wait()
		close(h.done)
	}()
	t.Cleanup(func() { h.stop() })
	c := waitForHelperSocket(t, h)
	status, err := c.Status()
	_ = c.Close()
	if err != nil {
		t.Fatal(err)
	}
	h.childPID = status.PID
	return h
}

func waitForHelperSocket(t *testing.T, h *runHelper) *Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-h.done:
			t.Fatalf("pty-host helper exited before socket appeared: %v", h.waitErr)
		default:
		}
		if c, err := Dial(h.socket); err == nil {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("socket %s never came up", h.socket)
	return nil
}

func (h *runHelper) stop() {
	select {
	case <-h.done:
		return
	default:
	}
	if h.childPID > 0 {
		_ = syscall.Kill(-h.childPID, syscall.SIGKILL)
	}
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
	}
	if h.childPID > 0 {
		_ = syscall.Kill(-h.childPID, syscall.SIGKILL)
	}
}

func (h *runHelper) wait(d time.Duration) error {
	select {
	case <-h.done:
		return h.waitErr
	case <-time.After(d):
		return fmt.Errorf("pty-host helper did not exit within %s", d)
	}
}

func (h *runHelper) exitCode(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(h.result)
	if err != nil {
		t.Fatalf("read helper result: %v", err)
	}
	code, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatalf("helper result %q: %v", b, err)
	}
	return code
}

// TestPtyhostRunHelper runs run() in an isolated process so its signals and
// PTY lifetime can be controlled without affecting the test process.
func TestPtyhostRunHelper(t *testing.T) {
	mode := os.Getenv("PTYHOST_TEST_MODE")
	if mode == "" {
		return
	}
	socket := os.Getenv("PTYHOST_TEST_SOCKET")
	result := os.Getenv("PTYHOST_TEST_RESULT")
	marker := os.Getenv("PTYHOST_TEST_MARKER")
	complete := os.Getenv("PTYHOST_TEST_COMPLETE")
	grandchild := os.Getenv("PTYHOST_TEST_GRANDCHILD")
	child := os.Getenv("PTYHOST_TEST_CHILD")
	defer func() {
		if _, err := os.Stat(result); err == nil {
			return
		}
		if b, err := os.ReadFile(child); err == nil {
			if pid, err := strconv.Atoi(string(b)); err == nil {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	}()
	var script string
	var stdout io.Writer = io.Discard
	switch mode {
	case "failed-stdout":
		stdout = failingWriter{marker: marker}
		script = "dd if=/dev/zero bs=65536 count=128 2>/dev/null; printf complete > " + shellQuote(complete) + "; sleep 1"
	case "blocked-input":
		script = `stty raw -echo; printf ready > "$PTYHOST_TEST_GRANDCHILD"; while [ ! -f "$PTYHOST_TEST_COMPLETE" ]; do sleep 0.01; done; cat > "$PTYHOST_TEST_TERM"`
	case "ignore-signals":
		grandchildScript := "trap '' HUP TERM; printf '%s' \"$$\" > " + shellQuote(grandchild) + "; while :; do sleep 1; done"
		script = "trap '' HUP TERM; sh -c " + shellQuote(grandchildScript) + " & wait"
	case "prompt-hup":
		stdout = failingWriter{marker: marker}
		script = `trap 'dd if=/dev/zero bs=65536 count=128 2>/dev/null; printf hup > "$PTYHOST_TEST_COMPLETE"; exit 23' HUP; trap 'printf term > "$PTYHOST_TEST_TERM"; exit 99' TERM; printf ready > "$PTYHOST_TEST_GRANDCHILD"; while :; do sleep 0.01; done`
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	script = `printf '%s' "$$" > "$PTYHOST_TEST_CHILD"; ` + script
	shutdown := runOptions{childSignalGrace: 150 * time.Millisecond, groupTermGrace: 150 * time.Millisecond}
	if mode == "prompt-hup" {
		shutdown.childSignalGrace = time.Second
	}
	code, err := run(socket, []string{"/bin/sh", "-c", script}, os.Stdin, stdout, shutdown)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := os.WriteFile(result, []byte(strconv.Itoa(code)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func TestRunDrainsOutputAfterStdoutFails(t *testing.T) {
	h := startRunHelper(t, "failed-stdout")
	if err := waitForFile(h.marker, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.wait(8 * time.Second); err != nil {
		t.Fatal(err)
	}
	if code := h.exitCode(t); code != 0 {
		t.Fatalf("child exit code %d, want 0", code)
	}
	if b, err := os.ReadFile(h.marker); err != nil || string(b) != "failed" {
		t.Fatalf("failing stdout marker = %q, err = %v", b, err)
	}
	if b, err := os.ReadFile(h.complete); err != nil || string(b) != "complete" {
		t.Fatalf("child completion marker = %q, err = %v", b, err)
	}
}

func TestRunSignalEscalatesToWholeProcessGroup(t *testing.T) {
	h := startRunHelper(t, "ignore-signals")
	if err := waitForFile(h.grandchild, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(h.grandchild)
	if err != nil {
		t.Fatal(err)
	}
	grandchildPID, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatalf("grandchild PID %q: %v", b, err)
	}
	started := time.Now()
	if err := h.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	if err := h.wait(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("shutdown took %s, want under 2s", elapsed)
	}
	if code := h.exitCode(t); code != 128+int(syscall.SIGKILL) {
		t.Fatalf("child exit code %d, want %d after SIGKILL", code, 128+int(syscall.SIGKILL))
	}
	if processRunning(h.childPID) {
		t.Fatalf("child process %d is still running", h.childPID)
	}
	if err := waitForProcessExit(grandchildPID, 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestRunDoesNotEscalatePromptSIGHUPExit(t *testing.T) {
	h := startRunHelper(t, "prompt-hup")
	if err := waitForFile(h.grandchild, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := h.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	if err := h.wait(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("shutdown took %s, want under 2s", elapsed)
	}
	if code := h.exitCode(t); code != 23 {
		t.Fatalf("child exit code %d, want 23 from SIGHUP handler", code)
	}
	if b, err := os.ReadFile(h.marker); err != nil || string(b) != "failed" {
		t.Fatalf("failing stdout marker = %q, err = %v", b, err)
	}
	if b, err := os.ReadFile(h.complete); err != nil || string(b) != "hup" {
		t.Fatalf("signal handler marker = %q, err = %v", b, err)
	}
	if _, err := os.Stat(h.term); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("TERM handler ran, stat error = %v", err)
	}
}

func waitForFile(path string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		// Shell redirections create the file before writing to it.
		if st, err := os.Stat(path); err == nil && st.Size() > 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("file %s was not written within %s", path, d)
}

func processRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return !errors.Is(err, syscall.ESRCH)
}

func waitForProcessExit(pid int, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !processRunning(pid) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("process %d is still running", pid)
}

// A non-reading CLI must not lock state or retain a timed-out paste. Once
// it reads again, buffered local keys precede subsequent remote input.
func TestBlockedPTYWriteExpiresAndRestoresLocalInput(t *testing.T) {
	h := startRunHelper(t, "blocked-input")
	if err := waitForFile(h.grandchild, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	c := waitForSocket(t, h.socket)
	defer c.Close()
	observer := waitForSocket(t, h.socket)
	defer observer.Close()
	// The lease is clearly shorter than remoteWriteTimeout so it expires
	// while the paste is blocked, but long enough for the write request to
	// reach the PTY first.
	const lease = time.Second
	if _, err := c.Hold(lease); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- c.Write(bytes.Repeat([]byte("x"), 1<<20)) }()
	// Conn.do holds its mutex from before the request is encoded until its
	// response arrives. Seeing it held proves the write request has left the
	// client. The remaining assumption is that the server reads that
	// already-sent request and enters the PTY write before the one-second
	// lease lapses; the socket is local and the lease leaves a full second
	// against the two-second write deadline for it.
	sent := time.Now().Add(5 * time.Second)
	for c.mu.TryLock() {
		c.mu.Unlock()
		if time.Now().After(sent) {
			t.Fatal("the remote write did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := h.stdin.Write([]byte("LOCAL")); err != nil {
		t.Fatal(err)
	}
	// Status must keep answering while the write is blocked, and the lease
	// must lapse on its own before the write's two-second deadline. Each
	// status call is bounded so a stuck status cannot hang the test.
	statusWithin := func(d time.Duration) (Response, error) {
		type status struct {
			st  Response
			err error
		}
		ch := make(chan status, 1)
		go func() {
			st, err := observer.Status()
			ch <- status{st, err}
		}()
		select {
		case r := <-ch:
			return r.st, r.err
		case <-time.After(d):
			return Response{}, fmt.Errorf("status did not return within %s", d)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := statusWithin(time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if st.LastLocalInput == 0 {
			if time.Now().After(deadline) {
				t.Fatal("local input was not recorded")
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		select {
		case err := <-result:
			t.Fatalf("the write returned before its lease lapsed: %v", err)
		default:
		}
		if st.Held {
			if time.Now().After(deadline) {
				t.Fatal("the lease did not expire while the write was blocked")
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		break // the lease lapsed while the write was still blocked
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "deadline") {
			t.Fatalf("blocked paste result = %v, want explicit deadline failure", err)
		}
	case <-time.After(remoteWriteTimeout + 2*time.Second):
		t.Fatal("blocked paste did not return a bounded failure")
	}
	// The timed-out paste left the PTY full. The lease ended on its own, so
	// local input is now flushing and blocks there; a new hold must not
	// overtake those keys.
	if _, err := observer.Hold(time.Second); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("hold overtook blocked local input: %v", err)
	}
	if st, err := statusWithin(time.Second); err != nil || st.Held {
		t.Fatalf("state during blocked local flush = %+v, %v", st, err)
	}
	if err := observer.Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.complete, []byte("read"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(h.term)
		if bytes.HasSuffix(b, []byte("LOCAL")) {
			prefix := bytes.TrimSuffix(b, []byte("LOCAL"))
			if len(prefix) == 0 || len(prefix) >= 1<<20 || bytes.Count(prefix, []byte("x")) != len(prefix) {
				t.Fatalf("unexpected partial paste before local input: %d bytes", len(prefix))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("local input did not recover after the CLI resumed reading")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := c.Write([]byte("REMOTE")); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(h.term)
		if bytes.HasSuffix(b, []byte("LOCALREMOTE")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subsequent remote input was lost or reordered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Cleanup follows the process group lifetime, even when its leader exits
// promptly on HUP, during TERM escalation, or before a stop signal arrives.
func TestExitedLeaderStillCleansProcessGroup(t *testing.T) {
	for _, mode := range []string{"hangup", "termination", "natural"} {
		t.Run(mode, func(t *testing.T) {
			dir := shortTempDir(t)
			pidfile := filepath.Join(dir, "grandchild")
			child := "trap '' HUP TERM; printf '%s' \"$$\" > " + shellQuote(pidfile) + "; while :; do sleep 1; done"
			script := "trap 'exit 0' HUP; sh -c " + shellQuote(child) + " & wait"
			if mode == "termination" {
				script = "trap '' HUP; trap 'exit 0' TERM; sh -c " + shellQuote(child) + " & wait"
			}
			if mode == "natural" {
				script = "sh -c " + shellQuote(child) + " & while [ ! -s " + shellQuote(pidfile) + " ]; do sleep 0.01; done; exit 0"
			}
			cmd := exec.Command("/bin/sh", "-c", script)
			master, err := pty.Start(cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err := waitForFile(pidfile, 5*time.Second); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(pidfile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(b))
			if err != nil {
				t.Fatal(err)
			}
			sigs := make(chan os.Signal, 1)
			if mode != "natural" {
				sigs <- syscall.SIGHUP
			}
			started := time.Now()
			if err := waitForChild(cmd, sigs, runOptions{childSignalGrace: 100 * time.Millisecond, groupTermGrace: 150 * time.Millisecond}); err != nil {
				t.Fatal(err)
			}
			master.Close()
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("cleanup took %s, want bounded shutdown", elapsed)
			}
			if err := waitForProcessExit(pid, time.Second); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A write lasting longer than daemon holdFor/2 must not reject its owner's
// renewal or let buffered keys through at the original lease's expiry.
func TestHolderRenewsDuringOwnBlockedWrite(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	h := &Host{ptmx: w, cmd: &exec.Cmd{}}
	const holdFor = 3 * time.Second // daemon renews this lease every 1.5 s
	if err := h.hold(1, holdFor); err != nil {
		t.Fatal(err)
	}
	defer h.release(1)
	started := time.Now()
	result := make(chan error, 1)
	go func() { result <- h.write(1, bytes.Repeat([]byte("x"), 1<<20)) }()
	// Receiving a prefix proves the write is in flight before renewal.
	if _, err := io.ReadFull(r, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	h.local([]byte("LOCAL"))
	time.Sleep(holdFor/2 + 100*time.Millisecond)
	select {
	case err := <-result:
		t.Fatalf("write finished before renewal: %v", err)
	default:
	}
	if err := h.hold(1, holdFor); err != nil {
		t.Fatalf("owner could not renew during its own write: %v", err)
	}
	if err := <-result; err == nil {
		t.Fatal("expected the non-reading pipe to time out")
	}
	if remaining := time.Until(started.Add(holdFor + 100*time.Millisecond)); remaining > 0 {
		time.Sleep(remaining)
	}
	if !h.status().Held {
		t.Fatal("renewed lease expired at the original deadline")
	}
	h.mu.Lock()
	pending := string(h.pending)
	h.mu.Unlock()
	if pending != "LOCAL" {
		t.Fatalf("local input escaped renewed hold: %q", pending)
	}
}

// Brief local backpressure should delay new remote operations, not fail
// them. Both operations must remain responsive to status while waiting.
func TestRemoteOperationsWaitForLocalFlush(t *testing.T) {
	for _, op := range []string{"hold", "write"} {
		t.Run(op, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			h := &Host{ptmx: w, cmd: &exec.Cmd{}}
			defer h.release(1)
			local := bytes.Repeat([]byte("x"), 1<<20)
			h.local(local)
			first := make([]byte, 1)
			if _, err := io.ReadFull(r, first); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				if op == "hold" {
					result <- h.hold(1, time.Second)
				} else {
					result <- h.write(1, []byte("REMOTE"))
				}
			}()
			select {
			case err := <-result:
				t.Fatalf("%s returned before local flush finished: %v", op, err)
			case <-time.After(50 * time.Millisecond):
			}
			if !h.status().OK {
				t.Fatal("status failed during writer wait")
			}
			rest := make([]byte, len(local)-1)
			if _, err := io.ReadFull(r, rest); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(append(first, rest...), local) {
				t.Fatal("local bytes changed")
			}
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("operation did not resume after local flush")
			}
			if op == "hold" {
				if !h.status().Held {
					t.Fatal("hold not acquired after local flush")
				}
			} else {
				remote := make([]byte, len("REMOTE"))
				if _, err := io.ReadFull(r, remote); err != nil || string(remote) != "REMOTE" {
					t.Fatalf("remote input after local flush = %q, %v", remote, err)
				}
			}
		})
	}
}

// Contention must not grant a fresh I/O budget after the prior local
// writer drains; the remote caller has one deadline for the whole call.
func TestRemoteWriteSharesDeadlineWithWriterWait(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	h := &Host{ptmx: w, cmd: &exec.Cmd{}}
	local := bytes.Repeat([]byte("l"), 1<<20)
	h.local(local)
	if _, err := io.ReadFull(r, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	started := time.Now()
	go func() { result <- h.write(1, bytes.Repeat([]byte("r"), 1<<20)) }()
	time.Sleep(remoteWriteTimeout/2 + 100*time.Millisecond)
	select {
	case err := <-result:
		t.Fatalf("remote write returned before local drain: %v", err)
	default:
	}
	if _, err := io.ReadFull(r, make([]byte, len(local)-1)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "deadline") {
			t.Fatalf("remote write = %v, want deadline failure", err)
		}
		if elapsed := time.Since(started); elapsed > remoteWriteTimeout+700*time.Millisecond {
			t.Fatalf("writer wait and I/O exceeded shared deadline: %s", elapsed)
		}
	case <-time.After(remoteWriteTimeout):
		t.Fatal("writer wait reset the I/O deadline")
	}
}
