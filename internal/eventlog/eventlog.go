// Package eventlog stores a session's unified events in an append-only
// JSON-lines file.
//
// Each record also carries how far into the rollout file the events were
// derived from, so the log doubles as the rollout checkpoint: after a
// restart, reading resumes where the last record left off and no event is
// assigned a second sequence number.
package eventlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type record struct {
	Rollout string          `json:"rollout,omitempty"`
	Offset  int64           `json:"offset,omitempty"`
	Event   json.RawMessage `json:"event"`
}

// Log is an open event log. It is safe for concurrent use.
//
// The events are kept in memory only while something reads them: Release
// lets go of a log that has not been read for a while, and the next reader
// loads the file again. Appending and asking for events after the last one,
// which is all an idle session's watchers do, need only the count.
type Log struct {
	mu   sync.Mutex
	f    file
	path string
	// size is where the whole records end and the next one goes. dirty
	// says a failed append may have left bytes after it.
	size  int64
	dirty bool
	// events is nil while released; n is how many the file holds.
	events  []*pb.Event
	n       uint64
	usedAt  time.Time
	rollout string
	offset  int64
	changed chan struct{} // closed and replaced on every append
}

// file is what the log needs of its file.
type file interface {
	io.WriterAt
	Sync() error
	Truncate(size int64) error
	Close() error
}

// Open loads the log at path, creating it if needed. A torn last line from
// a crash is cut off.
func Open(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Log{f: f, path: path, changed: make(chan struct{}), usedAt: time.Now()}
	good, err := l.load(f)
	if err == nil {
		l.n = uint64(len(l.events))
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	if err := f.Truncate(good); err != nil {
		f.Close()
		return nil, err
	}
	l.size = good
	return l, nil
}

// load reads the events and the checkpoint from f, and returns how many
// bytes of it are whole records.
func (l *Log) load(f io.Reader) (int64, error) {
	r := bufio.NewReader(f)
	l.events = nil
	var good int64
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			return good, nil // a line without newline is torn; drop it
		}
		if err != nil {
			return 0, err
		}
		var rec record
		if json.Unmarshal(bytes.TrimSpace(line), &rec) != nil {
			return good, nil
		}
		e := &pb.Event{}
		if err := protojson.Unmarshal(rec.Event, e); err != nil {
			return good, nil
		}
		if want := uint64(len(l.events) + 1); e.Seq != want {
			return 0, fmt.Errorf("seq %d at position %d", e.Seq, want)
		}
		l.events = append(l.events, e)
		if rec.Rollout != "" {
			l.rollout, l.offset = rec.Rollout, rec.Offset
		}
		good += int64(len(line))
	}
}

// Close closes the file.
func (l *Log) Close() error { return l.f.Close() }

// Checkpoint returns the rollout path and offset recorded last.
func (l *Log) Checkpoint() (string, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rollout, l.offset
}

// LastSeq is the highest sequence number, 0 when empty.
func (l *Log) LastSeq() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}

// Release drops the in-memory events if nothing has read them for idle.
// The file keeps them; the next reader loads it again.
func (l *Log) Release(idle time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.events != nil && time.Since(l.usedAt) >= idle {
		l.events = nil
	}
}

// loaded makes the events available again after a Release. On failure it
// reports false and the caller sees no events: the file was readable when
// the log was opened, and nothing else writes it.
func (l *Log) loaded() bool {
	l.usedAt = time.Now()
	if l.events != nil || l.n == 0 {
		return true
	}
	f, err := os.Open(l.path)
	if err == nil {
		// The checkpoint in memory may be ahead of the file's.
		rollout, offset := l.rollout, l.offset
		_, err = l.load(f)
		l.rollout, l.offset = rollout, offset
		f.Close()
	}
	if err == nil && uint64(len(l.events)) != l.n {
		err = fmt.Errorf("%d events on disk, %d expected", len(l.events), l.n)
	}
	if err != nil {
		slog.Error("reload event log", "path", l.path, "err", err)
		l.events = nil
		return false
	}
	return true
}

// Append assigns sequence numbers to evs, writes them in one write, and
// records the rollout checkpoint. rollout may be empty for events that do
// not come from the rollout; the previous checkpoint is kept then. Events
// are modified in place.
func (l *Log) Append(sessionID string, evs []*pb.Event, rollout string, offset int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(evs) == 0 && rollout == "" {
		return nil
	}
	var buf bytes.Buffer
	seq := l.n
	for i, e := range evs {
		seq++
		e.Seq = seq
		e.SessionId = sessionID
		body, err := protojson.Marshal(e)
		if err != nil {
			return err
		}
		rec := record{Event: body}
		// The checkpoint goes on the last record of the batch, so a torn
		// batch never claims its rollout line was consumed.
		if i == len(evs)-1 && rollout != "" {
			rec.Rollout, rec.Offset = rollout, offset
		}
		line, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if len(evs) == 0 {
		// Nothing to emit, but remember that the rollout advanced. Only
		// the in-memory checkpoint moves; the file keeps the last one
		// written with an event, which merely re-reads lines that
		// produce no events.
		l.rollout, l.offset = rollout, offset
		return nil
	}
	// A failed append must leave no bytes behind: the next one would
	// follow them, and write the same sequence numbers a second time.
	if err := l.rollback(); err != nil {
		return err
	}
	l.dirty = true
	if _, err := l.f.WriteAt(buf.Bytes(), l.size); err != nil {
		_ = l.rollback()
		return err
	}
	if err := l.f.Sync(); err != nil {
		_ = l.rollback()
		return err
	}
	l.dirty = false
	l.size += int64(buf.Len())
	if l.events != nil || l.n == 0 {
		l.events = append(l.events, evs...)
	}
	l.n = seq
	if rollout != "" {
		l.rollout, l.offset = rollout, offset
	}
	close(l.changed)
	l.changed = make(chan struct{})
	return nil
}

// rollback cuts off what a failed append left in the file. Until that
// works nothing more is appended.
func (l *Log) rollback() error {
	if !l.dirty {
		return nil
	}
	if err := l.f.Truncate(l.size); err != nil {
		return fmt.Errorf("event log is not at a record boundary: %w", err)
	}
	l.dirty = false
	return nil
}

// Since returns events with seq > after and a channel that is closed when
// more events are appended.
func (l *Log) Since(after uint64) ([]*pb.Event, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*pb.Event
	if after < l.n && l.loaded() {
		out = append(out, l.events[after:]...)
	}
	return out, l.changed
}

// Poke wakes those waiting on Since without an event: something they
// send along with the events changed, as the session's title.
func (l *Log) Poke() {
	l.mu.Lock()
	defer l.mu.Unlock()
	close(l.changed)
	l.changed = make(chan struct{})
}

// Each calls fn for every event in order, under the lock.
func (l *Log) Each(fn func(*pb.Event)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.loaded() {
		return
	}
	for _, e := range l.events {
		fn(e)
	}
}
