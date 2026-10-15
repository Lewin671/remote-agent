package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// TermStream is a live view of a session's terminal: a tmux control-mode
// client whose pane output is passed on, plus input and resizing. It never
// changes the window size unless asked to fit a client's screen, which
// clients ask for when they open it.
type TermStream struct {
	d      *Daemon
	name   string // tmux session
	pane   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    chan []byte
	resize chan struct{} // the window's size changed
	done   chan struct{}
	closed sync.Once

	mu     sync.Mutex
	fitted bool
	live   bool // output is passed on; before the snapshot it is dropped
	reason string

	historyMu sync.Mutex
	history   *terminalHistory
	ctx       context.Context
	cancel    context.CancelFunc
}

// termQueue is how many output chunks may wait for a slow reader before
// the stream ends; the client reconnects for a fresh snapshot.
const termQueue = 256

// OpenTerminal starts streaming a session's terminal.
func (d *Daemon) OpenTerminal(sessionID string) (*TermStream, error) {
	s, err := d.exact(sessionID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	exited, name := s.meta.Exited, s.meta.Tmux
	s.mu.Unlock()
	if exited {
		return nil, errExited
	}
	tc := d.cfg.Tmux
	pane, err := tc.Display(name, "#{pane_id}")
	if err != nil {
		return nil, err
	}
	argv := tc.Command("-C", "attach-session", "-t", "="+name)
	cmd := exec.Command(argv[0], argv[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	t := &TermStream{d: d, name: name, pane: strings.TrimSpace(pane), cmd: cmd, stdin: stdin, out: make(chan []byte, termQueue), resize: make(chan struct{}, 1), done: make(chan struct{})}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	go t.read(stdout)
	return t, nil
}

// read parses control-mode output until tmux exits.
func (t *TermStream) read(stdout io.Reader) {
	r := bufio.NewReaderSize(stdout, 64<<10)
	prefix := "%output " + t.pane + " "
	for {
		line, err := r.ReadString('\n')
		if strings.HasPrefix(line, prefix) {
			data := decodeOutput(strings.TrimSuffix(line[len(prefix):], "\n"))
			t.mu.Lock()
			live := t.live
			t.mu.Unlock()
			if live {
				select {
				case t.out <- data:
				default:
					t.end("output is not being read fast enough")
					return
				}
			}
		} else if strings.HasPrefix(line, "%layout-change") {
			// Fitted to a client or resized by a local terminal: the
			// client needs a new snapshot at the new size.
			select {
			case t.resize <- struct{}{}:
			default:
			}
		} else if strings.HasPrefix(line, "%exit") {
			t.end("the session ended")
			return
		}
		if err != nil {
			t.end("the terminal closed")
			return
		}
	}
}

// decodeOutput undoes control mode's escaping: bytes below space and the
// backslash arrive as \ooo.
func decodeOutput(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				out = append(out, byte(v))
				i += 3
				continue
			}
		}
		out = append(out, s[i])
	}
	return out
}

// Snapshot returns the screen as it is now, as bytes that redraw it, and
// starts passing on output that follows.
func (t *TermStream) Snapshot() (*pb.TerminalSnapshot, error) {
	tc := t.d.cfg.Tmux
	info, err := tc.Display(t.name, "#{pane_width} #{pane_height} #{cursor_x} #{cursor_y} #{cursor_flag}")
	if err != nil {
		return nil, err
	}
	var cols, rows, cx, cy, visible int
	if _, err := fmt.Sscan(info, &cols, &rows, &cx, &cy, &visible); err != nil {
		return nil, fmt.Errorf("read the pane size: %w", err)
	}
	// Pass on output from before the capture: some of it may be in the
	// snapshot already and is drawn again, which for a TUI that positions
	// its cursor absolutely changes nothing, while output lost between
	// the capture and the switch would leave the screen wrong.
	t.mu.Lock()
	t.live = true
	t.mu.Unlock()
	// Include retained tmux history when opening or replacing the screen.
	// A snapshot is also used after resizing and reconnecting.
	screen, err := tc.Run("capture-pane", "-p", "-e", "-S", "-200", "-t", "="+t.name+":")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(screen, "\n"), "\n")
	// Keep the encrypted frame comfortably below the 1 MiB transport limit.
	// Drop whole oldest lines, so UTF-8 and SGR sequences stay intact. The
	// visible screen is always kept, even when a single row is very large.
	const historyBytes = 512 << 10
	bytes := 0
	start := len(lines)
	for start > 0 {
		n := len(lines[start-1]) + 2
		if len(lines)-start >= rows && bytes+n > historyBytes {
			break
		}
		bytes += n
		start--
	}
	lines = lines[start:]
	var b strings.Builder
	b.WriteString("\x1b[0m\x1b[H\x1b[2J")
	for i, l := range lines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(l)
	}
	fmt.Fprintf(&b, "\x1b[0m\x1b[%d;%dH", cy+1, cx+1)
	if visible == 0 {
		b.WriteString("\x1b[?25l")
	} else {
		b.WriteString("\x1b[?25h")
	}
	return &pb.TerminalSnapshot{Data: []byte(b.String()), Cols: int32(cols), Rows: int32(rows)}, nil
}

// Output delivers the pane's output after the snapshot.
func (t *TermStream) Output() <-chan []byte { return t.out }

// SizeChanged signals that the window was resized.
func (t *TermStream) SizeChanged() <-chan struct{} { return t.resize }

// Done is closed when the stream ends; Reason says why.
func (t *TermStream) Done() <-chan struct{} { return t.done }

func (t *TermStream) Reason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reason
}

// Input types bytes into the session like the local keyboard does: they
// reach the PTY host, which holds them while remote input is injected.
func (t *TermStream) Input(b []byte) error {
	for len(b) > 0 {
		n := min(len(b), 256)
		args := []string{"send-keys", "-t", "=" + t.name + ":", "-H"}
		for _, c := range b[:n] {
			args = append(args, fmt.Sprintf("%02x", c))
		}
		if _, err := t.d.cfg.Tmux.Run(args...); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// Resize fits the window to a client's screen, or with fit false lets it
// follow attached terminals again.
func (t *TermStream) Resize(cols, rows int, fit bool) error {
	tc := t.d.cfg.Tmux
	t.mu.Lock()
	t.fitted = fit
	t.mu.Unlock()
	if !fit {
		_, err := tc.Run("set-option", "-w", "-t", "="+t.name+":", "window-size", "latest")
		return err
	}
	if cols < 20 || rows < 5 || cols > 500 || rows > 200 {
		return errors.New("unreasonable terminal size")
	}
	_, err := tc.Run("resize-window", "-t", "="+t.name+":", "-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows))
	return err
}

// Close ends the stream and gives the window size back to the attached
// terminals if a client had fitted it.
func (t *TermStream) Close() error {
	t.end("closed")
	return nil
}

func (t *TermStream) end(reason string) {
	t.closed.Do(func() {
		if t.cancel != nil {
			t.cancel()
		}
		t.historyMu.Lock()
		if t.history != nil {
			t.history.file.Close()
			t.history = nil
		}
		t.historyMu.Unlock()
		t.mu.Lock()
		t.reason = reason
		fitted := t.fitted
		t.mu.Unlock()
		t.stdin.Close() // control mode exits when its input ends
		go func() {
			timer := time.AfterFunc(3*time.Second, func() { _ = t.cmd.Process.Kill() })
			_ = t.cmd.Wait()
			timer.Stop()
		}()
		if fitted {
			if err := t.Resize(0, 0, false); err != nil {
				slog.Warn("restore window size", "tmux", t.name, "err", errClass(err))
			}
		}
		close(t.done)
	})
}
