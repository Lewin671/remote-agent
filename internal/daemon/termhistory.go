package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/tmux"
)

const historyPageLines = 200
const historyPageBytes = 256 << 10
const historyCaptureBytes = 64 << 20

// The unlinked file is private to this stream. It does not survive a crash,
// and keeps the full capture out of client memory and the relay server.
type terminalHistory struct {
	file       *os.File
	token      string
	offsets    []int64
	size       int64
	hasContent bool
}

func (h *terminalHistory) Write(p []byte) (int, error) {
	if h.size+int64(len(p)) > historyCaptureBytes {
		return 0, errors.New("terminal history exceeds capture limit")
	}
	n, err := h.file.Write(p)
	if len(bytes.TrimSpace(p[:n])) > 0 {
		h.hasContent = true
	}
	for i, b := range p[:n] {
		if b == '\n' {
			h.offsets = append(h.offsets, h.size+int64(i)+1)
		}
	}
	h.size += int64(n)
	return n, err
}
func (t *TermStream) captureHistory() (*terminalHistory, error) {
	file, err := os.CreateTemp("", "ra-terminal-history-*")
	if err != nil {
		return nil, err
	}
	// macOS/Linux keep an open, unlinked file available until its last close.
	if err = os.Remove(file.Name()); err != nil {
		file.Close()
		return nil, err
	}
	h := &terminalHistory{file: file, offsets: []int64{0}}
	token := make([]byte, 16)
	if _, err = rand.Read(token); err != nil {
		file.Close()
		return nil, err
	}
	h.token = hex.EncodeToString(token)
	parent := t.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	argv := t.d.cfg.Tmux.Command("capture-pane", "-p", "-e", "-S", "-"+strconv.Itoa(tmux.HistoryLines), "-t", "="+t.name+":")
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = h
	if err = cmd.Run(); err != nil {
		file.Close()
		return nil, err
	}
	if h.offsets[len(h.offsets)-1] != h.size {
		h.offsets = append(h.offsets, h.size)
	}
	return h, nil
}

// History returns whole lines preceding a cursor in a fixed capture. Live
// output, trimming and resizing cannot change that capture's row numbers.
func (t *TermStream) History(req *pb.TerminalHistoryRequest) (*pb.TerminalHistoryPage, error) {
	t.historyMu.Lock()
	defer t.historyMu.Unlock()
	page := &pb.TerminalHistoryPage{RequestId: req.RequestId}
	fail := func(message string) (*pb.TerminalHistoryPage, error) { page.Error = message; return page, nil }
	if len(req.RequestId) == 0 || len(req.RequestId) > 128 || len(req.Cursor) > 128 {
		return fail("invalid history request")
	}
	if t.ctx != nil && t.ctx.Err() != nil {
		return fail("terminal closed")
	}
	if req.Cursor == "" {
		h, err := t.captureHistory()
		if err != nil {
			return fail("cannot capture terminal history; retry or return to the live terminal")
		}
		if t.history != nil {
			t.history.file.Close()
		}
		t.history = h
	}
	h := t.history
	if h == nil {
		return fail("history expired; reopen history")
	}
	end := len(h.offsets) - 1
	if req.Cursor != "" {
		parts := strings.Split(req.Cursor, ":")
		if len(parts) != 2 || parts[0] != h.token {
			return fail("history expired; reopen history")
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n <= 0 || n > end {
			return fail("invalid history cursor")
		}
		end = n
	}
	if !h.hasContent {
		return page, nil
	}
	start := end
	for start > 0 && end-start < historyPageLines {
		if h.offsets[end]-h.offsets[start-1] > historyPageBytes {
			break
		}
		start--
	}
	if start == end && end > 0 {
		return fail("a terminal history line is too large to read")
	}
	data := make([]byte, h.offsets[end]-h.offsets[start])
	if _, err := h.file.ReadAt(data, h.offsets[start]); err != nil && err != io.EOF {
		return fail("cannot read terminal history; retry")
	}
	if !utf8.Valid(data) {
		data = bytes.ToValidUTF8(data, []byte("�"))
	}
	if len(data) > 0 {
		page.Lines = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	if start > 0 {
		page.NextCursor = fmt.Sprintf("%s:%d", h.token, start)
	}
	return page, nil
}
