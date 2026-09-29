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
	"os"
	"sync"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type record struct {
	Rollout string          `json:"rollout,omitempty"`
	Offset  int64           `json:"offset,omitempty"`
	Event   json.RawMessage `json:"event"`
}

// Log is an open event log. It is safe for concurrent use.
type Log struct {
	mu      sync.Mutex
	f       *os.File
	events  []*pb.Event
	rollout string
	offset  int64
	changed chan struct{} // closed and replaced on every append
}

// Open loads the log at path, creating it if needed. A torn last line from
// a crash is cut off.
func Open(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Log{f: f, changed: make(chan struct{})}
	good, err := l.load()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	if err := f.Truncate(good); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(good, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return l, nil
}

func (l *Log) load() (int64, error) {
	r := bufio.NewReader(l.f)
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
	return uint64(len(l.events))
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
	seq := uint64(len(l.events))
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
	if _, err := l.f.Write(buf.Bytes()); err != nil {
		return err
	}
	if err := l.f.Sync(); err != nil {
		return err
	}
	l.events = append(l.events, evs...)
	if rollout != "" {
		l.rollout, l.offset = rollout, offset
	}
	close(l.changed)
	l.changed = make(chan struct{})
	return nil
}

// Since returns events with seq > after and a channel that is closed when
// more events are appended.
func (l *Log) Since(after uint64) ([]*pb.Event, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*pb.Event
	if after < uint64(len(l.events)) {
		out = append(out, l.events[after:]...)
	}
	return out, l.changed
}

// Each calls fn for every event in order, under the lock.
func (l *Log) Each(fn func(*pb.Event)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.events {
		fn(e)
	}
}
