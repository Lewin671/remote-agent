// Package eventlog stores a session's unified events in an append-only
// JSON-lines file.
//
// Each record also carries how far into the rollout file the events were
// derived from, so the log doubles as the rollout checkpoint: after a
// restart, reading resumes where the last record left off and no event is
// assigned a second sequence number.
//
// The events appended together, as those of one rollout line, are a batch:
// one record each, all but the last marked "more", the last carrying the
// checkpoint. A crash can leave the first records of a batch without the
// rest. They are dropped when the log is opened, so the line they came
// from, which the checkpoint still names as unread, yields them once.
package eventlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	cacheEvents = 256
	cacheBytes  = 1 << 20
	indexStride = 128
	indexLimit  = 256
	pageLimit   = 512
)

var ErrClosed = errors.New("event log is closed")

type record struct {
	Rollout string `json:"rollout,omitempty"`
	Offset  int64  `json:"offset,omitempty"`
	// More: further records of the same batch follow.
	More  bool            `json:"more,omitempty"`
	Event json.RawMessage `json:"event"`
}

type anchor struct {
	seq    uint64
	offset int64
}

// Log is an append-only event log. Reads use a bounded tail cache and a
// bounded sparse index; full history remains on disk and can be streamed.
// It is safe for concurrent use.
type Log struct {
	mu   sync.Mutex
	f    file
	path string
	// size is where the whole records end and the next one goes. dirty
	// says a failed append may have left bytes after it.
	size   int64
	dirty  bool
	closed bool

	events        []*pb.Event // contiguous tail starting at cacheStart
	cacheStart    uint64
	cacheBytes    int
	cacheOn       bool
	index         []anchor // recent offsets, one per indexStride events
	n             uint64
	usedAt        time.Time
	rollout       string
	offset        int64
	checkpointSeq uint64        // last event that durably advanced the rollout checkpoint
	changed       chan struct{} // closed and replaced on every append
}

// file is what the log needs of its append handle.
type file interface {
	io.WriterAt
	Sync() error
	Truncate(size int64) error
	Close() error
}

type snapshot struct {
	path    string
	size    int64
	n       uint64
	index   []anchor
	events  []*pb.Event
	start   uint64
	changed <-chan struct{}
}

// Open validates the log at path, creating it if needed. A torn last line
// from a crash is cut off, with the records of its batch before it; a log
// with a damaged record is refused and left as it is. Validation is
// streaming: opening a long session does not retain its events in memory.
func Open(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Log{f: f, path: path, changed: make(chan struct{}), usedAt: time.Now(), cacheStart: 1, cacheOn: true}
	good, err := l.load(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	if err := f.Truncate(good); err != nil {
		_ = f.Close()
		return nil, err
	}
	l.size = good
	return l, nil
}

// load checks sequence numbers and damaged records while building only the
// bounded cache and index for complete batches. It returns the end of the
// last complete batch so Open can discard any torn tail.
func (l *Log) load(f *os.File) (int64, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	b := bufio.NewReader(f)
	var good, whole int64
	var seq uint64
	var rollout, wholeRollout string
	var offset, wholeOffset int64
	var checkpointSeq, wholeCheckpointSeq uint64
	var pending []*pb.Event
	var pendingBytes int
	pendingDropped := false
	var pendingIndex []anchor
	for {
		line, err := b.ReadBytes('\n')
		if err == io.EOF {
			// A line without newline is torn; drop it and any complete
			// records still waiting for the rest of their batch.
			l.rollout, l.offset = wholeRollout, wholeOffset
			l.checkpointSeq = wholeCheckpointSeq
			return whole, nil
		}
		if err != nil {
			return 0, err
		}
		var rec record
		e := &pb.Event{}
		if json.Unmarshal(bytes.TrimSpace(line), &rec) != nil || protojson.Unmarshal(rec.Event, e) != nil {
			return 0, fmt.Errorf("damaged record at byte %d", good)
		}
		seq++
		if e.Seq != seq {
			return 0, fmt.Errorf("seq %d at position %d", e.Seq, seq)
		}
		if rec.Rollout != "" {
			rollout, offset = rec.Rollout, rec.Offset
			checkpointSeq = seq
		}
		if (seq-1)%indexStride == 0 {
			pendingIndex = append(pendingIndex, anchor{seq: seq, offset: good})
			if len(pendingIndex) > indexLimit {
				copy(pendingIndex, pendingIndex[len(pendingIndex)-indexLimit:])
				pendingIndex = pendingIndex[:indexLimit]
			}
		}
		if size := proto.Size(e); size > cacheBytes {
			pending = nil
			pendingBytes = 0
			pendingDropped = true
		} else {
			pending = append(pending, e)
			pendingBytes += proto.Size(e)
			for len(pending) > cacheEvents || pendingBytes > cacheBytes {
				pendingBytes -= proto.Size(pending[0])
				pending[0] = nil
				pending = pending[1:]
				pendingDropped = true
			}
		}
		good += int64(len(line))
		if !rec.More {
			whole = good
			l.n = seq
			wholeRollout, wholeOffset = rollout, offset
			wholeCheckpointSeq = checkpointSeq
			if pendingDropped {
				l.events = nil
				l.cacheBytes = 0
				l.cacheStart = seq + 1
			}
			for _, e := range pending {
				l.addCache(e)
			}
			for _, a := range pendingIndex {
				l.addIndex(a.seq, a.offset)
			}
			pending = nil
			pendingBytes = 0
			pendingDropped = false
			pendingIndex = nil
		}
	}
}

// addIndex retains one anchor every indexStride sequences. The finite
// window makes recent relay and watcher reads cheap while old history can
// still be scanned from the start.
func (l *Log) addIndex(seq uint64, offset int64) {
	if (seq-1)%indexStride != 0 {
		return
	}
	l.index = append(l.index, anchor{seq: seq, offset: offset})
	if len(l.index) > indexLimit {
		copy(l.index, l.index[len(l.index)-indexLimit:])
		l.index = l.index[:indexLimit]
	}
}

func (l *Log) addCache(e *pb.Event) {
	sz := proto.Size(e)
	if sz > cacheBytes {
		l.events = nil
		l.cacheBytes = 0
		l.cacheStart = e.Seq + 1
		return
	}
	if len(l.events) == 0 {
		l.cacheStart = e.Seq
	}
	l.events = append(l.events, e)
	l.cacheBytes += sz
	for len(l.events) > cacheEvents || l.cacheBytes > cacheBytes {
		l.cacheBytes -= proto.Size(l.events[0])
		l.events[0] = nil
		l.events = l.events[1:]
		l.cacheStart++
	}
}

// Close permanently closes the log. Release, by contrast, only closes an
// idle append handle and can be followed by reads or appends.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	close(l.changed)
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Checkpoint returns the rollout path and offset recorded last.
func (l *Log) Checkpoint() (string, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rollout, l.offset
}

// CheckpointSeq identifies the last durable rollout checkpoint. Daemon events
// and source lines that emit no event do not advance it.
func (l *Log) CheckpointSeq() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.checkpointSeq
}

// LastSeq is the highest sequence number, 0 when empty.
func (l *Log) LastSeq() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}

// Release drops the in-memory event cache and closes the append handle if
// nothing has read or appended to the log for idle. The file and sparse
// index remain, and future operations can reopen it.
func (l *Log) Release(idle time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || time.Since(l.usedAt) < idle {
		return
	}
	l.events = nil
	l.cacheBytes = 0
	l.cacheStart = l.n + 1
	l.cacheOn = false
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

func (l *Log) touchLocked() { l.usedAt = time.Now() }

func (l *Log) snapshotLocked() (snapshot, error) {
	if l.closed {
		return snapshot{}, ErrClosed
	}
	l.touchLocked()
	s := snapshot{path: l.path, size: l.size, n: l.n, index: append([]anchor(nil), l.index...),
		start: l.cacheStart, changed: l.changed}
	s.events = append([]*pb.Event(nil), l.events...)
	return s, nil
}

func (l *Log) ensureOpenLocked() error {
	if l.closed {
		return ErrClosed
	}
	if l.f != nil {
		return nil
	}
	f, err := os.OpenFile(l.path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	l.f = f
	return nil
}

// Append assigns sequence numbers to evs, writes them in one write, and
// records the rollout checkpoint. rollout may be empty for events that do
// not come from the rollout; the previous checkpoint is kept then. Events
// are modified in place.
func (l *Log) Append(sessionID string, evs []*pb.Event, rollout string, offset int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	l.touchLocked()
	if len(evs) == 0 && rollout == "" {
		return nil
	}
	var buf bytes.Buffer
	var added []anchor
	seq := l.n
	for i, e := range evs {
		seq++
		e.Seq = seq
		e.SessionId = sessionID
		body, err := protojson.Marshal(e)
		if err != nil {
			return err
		}
		rec := record{Event: body, More: i < len(evs)-1}
		// The checkpoint goes on the last record of the batch, so a torn
		// batch never claims its rollout line was consumed.
		if !rec.More && rollout != "" {
			rec.Rollout, rec.Offset = rollout, offset
		}
		line, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		if (seq-1)%indexStride == 0 {
			added = append(added, anchor{seq: seq, offset: l.size + int64(buf.Len())})
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
	if err := l.ensureOpenLocked(); err != nil {
		return err
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
	for _, a := range added {
		l.index = append(l.index, a)
	}
	if len(l.index) > indexLimit {
		copy(l.index, l.index[len(l.index)-indexLimit:])
		l.index = l.index[:indexLimit]
	}
	if l.cacheOn {
		for _, e := range evs {
			l.addCache(e)
		}
	}
	l.n = seq
	if rollout != "" {
		l.rollout, l.offset = rollout, offset
		l.checkpointSeq = seq
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

// PageSince returns at most limit events after after, the append notification
// channel observed with that page, and any read error. The maximum page is
// fixed so callers cannot accidentally materialize unbounded history.
func (l *Log) PageSince(after uint64, limit int) ([]*pb.Event, <-chan struct{}, error) {
	if limit <= 0 {
		return nil, nil, errors.New("event page limit must be positive")
	}
	limit = min(limit, pageLimit)
	l.mu.Lock()
	s, err := l.snapshotLocked()
	l.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	if after >= s.n {
		return nil, s.changed, nil
	}
	first := after + 1
	last := min(s.n, after+uint64(limit))
	if s.start <= first && uint64(len(s.events)) >= last-s.start+1 && last <= s.start+uint64(len(s.events))-1 {
		from := first - s.start
		to := last - s.start + 1
		return append([]*pb.Event(nil), s.events[from:to]...), s.changed, nil
	}
	var out []*pb.Event
	err = scanRange(s, after, last, func(e *pb.Event) error {
		out = append(out, e)
		return nil
	})
	return out, s.changed, err
}

// Since returns every event with seq > after. It is intended only for
// explicit full-history operations; streaming consumers should use EachSince
// or PageSince. The returned events are independent of the bounded cache.
func (l *Log) Since(after uint64) ([]*pb.Event, <-chan struct{}, error) {
	l.mu.Lock()
	s, err := l.snapshotLocked()
	l.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	var out []*pb.Event
	err = scanRange(s, after, s.n, func(e *pb.Event) error {
		out = append(out, e)
		return nil
	})
	return out, s.changed, err
}

// EachSince visits events after after in order using one file scan and a fixed
// memory window. It returns the append notification channel observed with the
// snapshot. The snapshot is fixed when the call starts; events appended while
// fn runs are left for the next call, and close that channel. fn runs without
// the log mutex and may stop the scan by returning an error.
func (l *Log) EachSince(after uint64, fn func(*pb.Event) error) (<-chan struct{}, error) {
	l.mu.Lock()
	s, err := l.snapshotLocked()
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	err = scanRange(s, after, s.n, fn)
	return s.changed, err
}

// EachRange visits events in (after, through] from one stable snapshot.
// It is useful when a caller must make more than one streaming pass over
// the same history. If through is beyond the current log, it is clamped to
// the snapshot's last sequence.
func (l *Log) EachRange(after, through uint64, fn func(*pb.Event) error) error {
	l.mu.Lock()
	s, err := l.snapshotLocked()
	l.mu.Unlock()
	if err != nil {
		return err
	}
	return scanRange(s, after, min(through, s.n), fn)
}

// Each visits every event in sequence order with bounded memory.
func (l *Log) Each(fn func(*pb.Event)) error {
	_, err := l.EachSince(0, func(e *pb.Event) error {
		fn(e)
		return nil
	})
	return err
}

// Poke wakes those waiting on Since without an event: something they
// send along with the events changed, as the session's title.
func (l *Log) Poke() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	close(l.changed)
	l.changed = make(chan struct{})
}

// scanRange reads (after, through] from a snapshot. It starts at the nearest
// retained anchor at or before the first wanted sequence, so a single full
// history walk does not allocate a result slice or re-read prior pages.
func scanRange(s snapshot, after, through uint64, fn func(*pb.Event) error) error {
	if after >= through {
		return nil
	}
	first := after + 1
	if s.start <= first && len(s.events) > 0 {
		cacheEnd := s.start + uint64(len(s.events)) - 1
		if first >= s.start && through <= cacheEnd {
			for seq := first; seq <= through; seq++ {
				if err := fn(s.events[seq-s.start]); err != nil {
					return err
				}
			}
			return nil
		}
	}
	startOffset := int64(0)
	startSeq := uint64(1)
	for _, a := range s.index {
		if a.seq <= first && a.seq >= startSeq {
			startSeq, startOffset = a.seq, a.offset
		}
	}
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(io.LimitReader(f, s.size-startOffset))
	seq := startSeq
	for seq <= through {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("event %d ended during read: %w", seq, err)
		}
		var rec record
		e := &pb.Event{}
		if json.Unmarshal(bytes.TrimSpace(line), &rec) != nil || protojson.Unmarshal(rec.Event, e) != nil || e.Seq != seq {
			return fmt.Errorf("damaged record at event %d", seq)
		}
		if seq > after {
			if err := fn(e); err != nil {
				return err
			}
		}
		seq++
	}
	return nil
}
