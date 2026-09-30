// Package tail reads complete lines appended to a file.
//
// Memory stays bounded however long a line is. The CLIs write a whole
// message per line, pasted images included as base64: one message with
// several original photos can be tens of megabytes. A line longer than
// MaxLine is not skipped (that would lose the message) but compacted: its
// JSON is copied with every long string replaced, either by a Blob marker
// that ReadBlob resolves from the file later (base64 data, which the image
// pipeline decodes on its own terms), or by its beginning (text).
package tail

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// MaxLine is the longest line returned as it is. Longer lines are
// compacted.
const MaxLine = 8 << 20

// Compaction limits: strings longer than ElideAbove are replaced; text
// keeps its first KeepText bytes. A compacted line longer than MaxLine is
// skipped after all.
const (
	ElideAbove = 256 << 10
	KeepText   = 64 << 10
)

// BlobPrefix starts the marker that stands for an elided base64 string:
// BlobPrefix + "<start>:<end>", the file offsets of the string's JSON
// contents (between the quotes).
const BlobPrefix = "\x1bra-blob:"

// Line is one complete line and the file offset just past its newline.
type Line struct {
	Data []byte
	End  int64
	// Compacted: long strings of the line were replaced (see the package
	// comment).
	Compacted bool
}

// Reader returns lines appended to a file since the last call. A trailing
// line without a newline is read again once it is completed.
type Reader struct {
	Path string
	off  int64 // offset of the first byte not yet returned as a line
}

// NewReader starts reading path at offset off.
func NewReader(path string, off int64) *Reader {
	return &Reader{Path: path, off: off}
}

// Offset is where the next complete line starts.
func (r *Reader) Offset() int64 { return r.off }

// Next returns the complete lines appended since the last call. A missing
// file yields no lines and no error. If the file shrank below the current
// offset it was replaced, and reading restarts from the beginning;
// truncated reports that.
func (r *Reader) Next() (lines []Line, truncated bool, err error) {
	f, err := os.Open(r.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	size := st.Size()
	if size < r.off {
		r.off, truncated = 0, true
	}
	if size == r.off {
		return nil, truncated, nil
	}
	if _, err := f.Seek(r.off, io.SeekStart); err != nil {
		return nil, truncated, err
	}
	br := bufio.NewReaderSize(io.LimitReader(f, size-r.off), 64<<10)
	pos := r.off // file offset just past what was read
	var buf []byte
	long := false // the current line passed MaxLine; buf is not kept
	for {
		chunk, err := br.ReadSlice('\n')
		pos += int64(len(chunk))
		if !long {
			buf = append(buf, chunk...)
			if len(buf) > MaxLine+1 {
				long, buf = true, nil
			}
		}
		switch {
		case err == nil:
			// A complete line, ending at pos.
			if long {
				if line, lerr := compactLine(r.Path, r.off, pos-1); lerr == nil {
					lines = append(lines, Line{Data: line, End: pos, Compacted: true})
				}
			} else if data := bytes.TrimSuffix(buf, []byte("\n")); len(bytes.TrimSpace(data)) > 0 {
				lines = append(lines, Line{Data: data, End: pos})
			}
			r.off, buf, long = pos, nil, false
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			// An incomplete last line is read again once it is complete.
			return lines, truncated, nil
		default:
			return lines, truncated, err
		}
	}
}

// compactLine copies the JSON line in [start, end) of the file at path,
// replacing long strings.
func compactLine(path string, start, end int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	c := &compactor{r: bufio.NewReaderSize(io.LimitReader(f, end-start), 64<<10), pos: start}
	if err := c.run(); err != nil {
		return nil, err
	}
	return c.out, nil
}

// compactor streams JSON, copying everything but long strings.
type compactor struct {
	r   *bufio.Reader
	pos int64 // file offset of the next byte
	out []byte
}

func (c *compactor) readByte() (byte, error) {
	b, err := c.r.ReadByte()
	if err == nil {
		c.pos++
	}
	return b, err
}

// errTooLong stops compaction of a line that stays over MaxLine.
var errTooLong = errors.New("line stays too long after compaction")

func (c *compactor) run() error {
	for {
		if len(c.out) > MaxLine {
			return errTooLong
		}
		b, err := c.readByte()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if b != '"' {
			c.out = append(c.out, b)
			continue
		}
		if err := c.str(); err != nil {
			return err
		}
	}
}

// str copies or replaces one string; the opening quote was read.
func (c *compactor) str() error {
	contentStart := c.pos
	var s []byte // contents while short
	escaped := false
	for {
		b, err := c.readByte()
		if err != nil {
			return fmt.Errorf("unterminated string at %d", contentStart)
		}
		if escaped {
			escaped = false
		} else if b == '\\' {
			escaped = true
		} else if b == '"' {
			break
		}
		if len(s) <= ElideAbove {
			s = append(s, b) // enough to decide how to replace it
		}
	}
	contentEnd := c.pos - 1
	if contentEnd-contentStart <= ElideAbove {
		c.out = append(c.out, '"')
		c.out = append(c.out, s...)
		c.out = append(c.out, '"')
		return nil
	}
	if looksBase64(s) {
		marker, _ := json.Marshal(fmt.Sprintf("%s%d:%d", BlobPrefix, contentStart, contentEnd))
		c.out = append(c.out, marker...)
		return nil
	}
	keep := s[:safeCut(s, KeepText)]
	// Do not end inside a UTF-8 character.
	for len(keep) > 0 && keep[len(keep)-1]&0xC0 == 0x80 {
		keep = keep[:len(keep)-1]
	}
	if len(keep) > 0 && keep[len(keep)-1] >= 0xC0 {
		keep = keep[:len(keep)-1]
	}
	c.out = append(c.out, '"')
	c.out = append(c.out, keep...)
	c.out = append(c.out, "…"...)
	c.out = append(c.out, '"')
	return nil
}

// looksBase64 tells base64 data, possibly as a data URL, from text by its
// beginning.
func looksBase64(s []byte) bool {
	head := s[:min(len(s), 4096)]
	if bytes.HasPrefix(head, []byte("data:")) {
		return true
	}
	for _, b := range head {
		if !(b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '+' || b == '/' || b == '=' || b == '\\') {
			return false
		}
	}
	return true
}

// IsBlob reports whether s is a Blob marker.
func IsBlob(s string) bool { return strings.HasPrefix(s, BlobPrefix) }

// ReadBlob returns the string a Blob marker stands for, read from the file
// at path; strings longer than limit bytes are refused.
func ReadBlob(path, marker string, limit int64) (string, error) {
	rest, ok := strings.CutPrefix(marker, BlobPrefix)
	if !ok {
		return "", errors.New("not a blob marker")
	}
	a, b, ok := strings.Cut(rest, ":")
	start, err1 := strconv.ParseInt(a, 10, 64)
	end, err2 := strconv.ParseInt(b, 10, 64)
	if !ok || err1 != nil || err2 != nil || end < start {
		return "", errors.New("malformed blob marker")
	}
	if end-start > limit {
		return "", fmt.Errorf("blob of %d bytes is over the limit", end-start)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw := make([]byte, end-start+2)
	raw[0], raw[len(raw)-1] = '"', '"'
	if _, err := f.ReadAt(raw[1:len(raw)-1], start); err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

// safeCut returns the largest length up to n at which the JSON string
// contents s can be cut without splitting an escape sequence.
func safeCut(s []byte, n int) int {
	cut := 0
	for i := 0; i < len(s) && i < n; {
		step := 1
		if s[i] == '\\' {
			step = 2
			if i+1 < len(s) && s[i+1] == 'u' {
				step = 6
			}
		}
		if i+step > n {
			break
		}
		i += step
		cut = i
	}
	return cut
}
