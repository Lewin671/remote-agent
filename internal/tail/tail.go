// Package tail reads complete lines appended to a file.
package tail

import (
	"bytes"
	"io"
	"os"
)

// MaxLine bounds a single line; longer lines are skipped.
const MaxLine = 64 << 20

// Line is one complete line and the file offset just past its newline.
type Line struct {
	Data []byte
	End  int64
}

// Reader returns lines appended to a file since the last call. A trailing
// line without a newline is kept until it is completed.
type Reader struct {
	Path    string
	off     int64 // offset of the first byte not yet returned as a line
	partial []byte
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
	readFrom := r.off + int64(len(r.partial))
	if st.Size() < readFrom {
		r.off, r.partial, readFrom, truncated = 0, nil, 0, true
	}
	if st.Size() == readFrom {
		return nil, truncated, nil
	}
	if _, err := f.Seek(readFrom, io.SeekStart); err != nil {
		return nil, truncated, err
	}
	buf, err := io.ReadAll(io.LimitReader(f, st.Size()-readFrom))
	if err != nil {
		return nil, truncated, err
	}
	data := append(r.partial, buf...)
	r.partial = nil
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		lineData := data[:i]
		r.off += int64(i + 1)
		data = data[i+1:]
		if len(bytes.TrimSpace(lineData)) == 0 || len(lineData) > MaxLine {
			continue
		}
		lines = append(lines, Line{Data: append([]byte(nil), lineData...), End: r.off})
	}
	if len(data) > MaxLine {
		// An oversized line without a newline yet: drop what we have and
		// skip ahead; the rest of it will be dropped when it completes.
		r.off += int64(len(data))
	} else if len(data) > 0 {
		r.partial = append([]byte(nil), data...)
	}
	return lines, truncated, nil
}
