package eventlog

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func msg(text string) *pb.Event {
	return &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: text}}}
}

func TestLongHistoryHasBoundedCacheAndPagedReads(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	const count = 4096
	batch := make([]*pb.Event, count)
	for i := range batch {
		batch[i] = msg(strings.Repeat("x", 2048))
	}
	if err := l.Append("s1", batch, "/r.jsonl", 10); err != nil {
		t.Fatal(err)
	}
	if len(l.events) > cacheEvents || l.cacheBytes > cacheBytes || len(l.index) > indexLimit {
		t.Fatalf("unbounded cache/index: events=%d bytes=%d anchors=%d", len(l.events), l.cacheBytes, len(l.index))
	}
	if len(l.events) != cacheEvents || l.events[0].Seq != count-cacheEvents+1 {
		t.Fatalf("tail cache starts at %d with %d events", l.cacheStart, len(l.events))
	}
	if l.n != count || l.events[len(l.events)-1].Seq != count {
		t.Fatalf("last sequence %d, cached tail %d", l.n, l.events[len(l.events)-1].Seq)
	}

	// Pages cover all history in order while each individual read stays
	// bounded. The old implementation returned all 4,096 events at once.
	var next uint64
	for next < count {
		page, _, err := l.PageSince(next, 127)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 || len(page) > 127 {
			t.Fatalf("page after %d has %d events", next, len(page))
		}
		for _, e := range page {
			next++
			if e.Seq != next || e.GetAgentMessage().Text != strings.Repeat("x", 2048) {
				t.Fatalf("event %d has seq %d or lost payload", next, e.Seq)
			}
		}
	}

	// Release closes the append handle and discards even the bounded cache;
	// a streaming scan still sees the complete history.
	l.Release(0)
	if l.f != nil || l.events != nil {
		t.Fatal("release kept the file handle or event cache")
	}
	seen := uint64(0)
	_, err = l.EachSince(0, func(e *pb.Event) error {
		seen++
		if e.Seq != seen {
			t.Fatalf("stream event %d has seq %d", seen, e.Seq)
		}
		return nil
	})
	if err != nil || seen != count {
		t.Fatalf("stream saw %d events, err=%v", seen, err)
	}
	if len(l.events) != 0 {
		t.Fatalf("streaming full history retained %d events", len(l.events))
	}

	// Since keeps its explicit full-result semantics for callers that really
	// request a materialized history.
	all, _, err := l.Since(0)
	if err != nil || len(all) != count || all[len(all)-1].Seq != count {
		t.Fatalf("Since returned %d events, err=%v", len(all), err)
	}
}

func TestSparseIndexRollsOverAndOldHistoryStillStreams(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	const count = indexStride*indexLimit + 1
	batch := make([]*pb.Event, count)
	for i := range batch {
		batch[i] = msg("x")
	}
	if err := l.Append("s1", batch, "", 0); err != nil {
		t.Fatal(err)
	}
	if len(l.index) != indexLimit || l.index[0].seq != 129 || l.index[len(l.index)-1].seq != count {
		t.Fatalf("sparse index window = %d entries from %d to %d", len(l.index), l.index[0].seq, l.index[len(l.index)-1].seq)
	}
	seen := uint64(0)
	if _, err := l.EachSince(0, func(e *pb.Event) error {
		seen++
		if e.Seq != seen {
			t.Fatalf("old-history sequence %d, want %d", e.Seq, seen)
		}
		return nil
	}); err != nil || seen != count {
		t.Fatalf("EachSince scanned %d events, err=%v", seen, err)
	}
	var tail []uint64
	if err := l.EachRange(count-20, count, func(e *pb.Event) error {
		tail = append(tail, e.Seq)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(tail) != 20 || tail[0] != count-19 || tail[19] != count {
		t.Fatalf("tail range = %v", tail)
	}
}

func TestEachSinceSnapshotDoesNotHoldLockOrMissConcurrentAppend(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Append("s1", []*pb.Event{msg("a"), msg("b"), msg("c")}, "", 0); err != nil {
		t.Fatal(err)
	}
	started, resume := make(chan struct{}), make(chan struct{})
	var got []uint64
	result := make(chan struct {
		changed <-chan struct{}
		err     error
	}, 1)
	go func() {
		changed, err := l.EachSince(0, func(e *pb.Event) error {
			got = append(got, e.Seq)
			if e.Seq == 1 {
				close(started)
				<-resume
			}
			return nil
		})
		result <- struct {
			changed <-chan struct{}
			err     error
		}{changed, err}
	}()
	<-started
	// This would deadlock if EachSince held the append lock while invoking
	// its callback.
	if err := l.Append("s1", []*pb.Event{msg("d")}, "", 0); err != nil {
		t.Fatal(err)
	}
	close(resume)
	out := <-result
	if out.err != nil {
		t.Fatal(out.err)
	}
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("snapshot included or missed events: %v", got)
	}
	select {
	case <-out.changed:
	default:
		t.Fatal("append during scan did not close the captured change channel")
	}
	_, err = l.EachSince(3, func(e *pb.Event) error {
		if e.Seq != 4 {
			t.Fatalf("tail seq %d", e.Seq)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReleaseReopensButCloseIsFinal(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("s1", []*pb.Event{msg("a")}, "", 0); err != nil {
		t.Fatal(err)
	}
	l.Release(0)
	if l.f != nil {
		t.Fatal("release left an append handle open")
	}
	if err := l.Append("s1", []*pb.Event{msg("b")}, "", 0); err != nil {
		t.Fatal(err)
	}
	if l.LastSeq() != 2 || l.f == nil {
		t.Fatalf("append did not reopen the log: seq=%d open=%v", l.LastSeq(), l.f != nil)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Append("s1", []*pb.Event{msg("c")}, "", 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("append after close: %v", err)
	}
	if _, _, err := l.PageSince(0, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after release and close: %v", err)
	}
}

func TestPageReadErrorIsNotEmptyHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("s1", []*pb.Event{msg("a"), msg("b")}, "", 0); err != nil {
		t.Fatal(err)
	}
	l.Release(0)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if page, _, err := l.PageSince(0, 1); err == nil || len(page) != 0 {
		t.Fatalf("missing file returned page %v, err=%v", page, err)
	}
	_ = l.Close()
}

func since(t *testing.T, l *Log, after uint64) ([]*pb.Event, <-chan struct{}) {
	t.Helper()
	evs, changed, err := l.Since(after)
	if err != nil {
		t.Fatal(err)
	}
	return evs, changed
}

func TestAppendReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("s1", []*pb.Event{msg("a"), msg("b")}, "/r.jsonl", 100); err != nil {
		t.Fatal(err)
	}
	if err := l.Append("s1", []*pb.Event{msg("approval")}, "", 0); err != nil {
		t.Fatal(err)
	}
	_, ch := since(t, l, 0)
	if err := l.Append("s1", []*pb.Event{msg("c")}, "/r.jsonl", 200); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("append did not signal waiters")
	}
	// Advancing without events moves only the in-memory checkpoint.
	l.Append("s1", nil, "/r.jsonl", 250)
	if r, off := l.Checkpoint(); r != "/r.jsonl" || off != 250 {
		t.Fatalf("checkpoint %s %d", r, off)
	}
	l.Close()

	// Simulate a torn write at the end.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"event":{"seq":5`)
	f.Close()

	l, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.LastSeq() != 4 {
		t.Fatalf("last seq %d", l.LastSeq())
	}
	if r, off := l.Checkpoint(); r != "/r.jsonl" || off != 200 {
		t.Fatalf("checkpoint after reopen %s %d", r, off)
	}
	evs, _ := since(t, l, 2)
	if len(evs) != 2 || evs[0].GetAgentMessage().Text != "approval" || evs[0].Seq != 3 || evs[0].SessionId != "s1" {
		t.Fatalf("since: %v", evs)
	}
	if err := l.Append("s1", []*pb.Event{msg("d")}, "", 0); err != nil {
		t.Fatal(err)
	}
	if l.LastSeq() != 5 {
		t.Fatalf("seq after torn line %d", l.LastSeq())
	}
}

func TestReleasedLogReadsItsFileAgain(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Append("s1", []*pb.Event{msg("a"), msg("b")}, "/r.jsonl", 100); err != nil {
		t.Fatal(err)
	}
	l.Append("s1", nil, "/r.jsonl", 150)
	l.Release(time.Hour)
	if l.events == nil {
		t.Fatal("a log just read was released")
	}
	l.Release(0)
	if l.events != nil {
		t.Fatal("not released")
	}
	// Watching for more and appending need no events in memory.
	if evs, _ := since(t, l, 2); len(evs) != 0 || l.events != nil {
		t.Fatalf("waiting for more loaded the log: %v", evs)
	}
	if err := l.Append("s1", []*pb.Event{msg("c")}, "", 0); err != nil {
		t.Fatal(err)
	}
	if l.events != nil || l.LastSeq() != 3 {
		t.Fatalf("append to a released log: loaded=%v last=%d", l.events != nil, l.LastSeq())
	}
	evs, _ := since(t, l, 1)
	if len(evs) != 2 || evs[0].GetAgentMessage().Text != "b" || evs[1].Seq != 3 || evs[1].GetAgentMessage().Text != "c" {
		t.Fatalf("events after reload: %v", evs)
	}
	if r, off := l.Checkpoint(); r != "/r.jsonl" || off != 150 {
		t.Fatalf("reload moved the checkpoint back: %s %d", r, off)
	}
	l.Release(0)
	n := 0
	l.Each(func(*pb.Event) { n++ })
	if n != 3 {
		t.Fatalf("each saw %d events", n)
	}
	if err := l.Append("s1", []*pb.Event{msg("d")}, "", 0); err != nil {
		t.Fatal(err)
	}
	if evs, _ := since(t, l, 0); len(evs) != 4 || evs[3].Seq != 4 {
		t.Fatalf("append to a loaded log: %v", evs)
	}
}

// faultyFile fails writes and syncs on demand, as a full or failing disk
// does.
type faultyFile struct {
	*os.File
	// tornWrite writes this many bytes of the next write and fails it.
	tornWrite int
	failSync  bool
	// failTruncate fails this many truncations.
	failTruncate int
}

var errDisk = errors.New("disk failure")

func (f *faultyFile) WriteAt(b []byte, off int64) (int, error) {
	if n := f.tornWrite; n > 0 {
		f.tornWrite = 0
		n = min(n, len(b))
		f.File.WriteAt(b[:n], off)
		return n, errDisk
	}
	return f.File.WriteAt(b, off)
}

func (f *faultyFile) Sync() error {
	if f.failSync {
		f.failSync = false
		return errDisk
	}
	return f.File.Sync()
}

func (f *faultyFile) Truncate(size int64) error {
	if f.failTruncate > 0 {
		f.failTruncate--
		return errDisk
	}
	return f.File.Truncate(size)
}

func openFaulty(t *testing.T) (*Log, *faultyFile, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ff := &faultyFile{File: l.f.(*os.File)}
	l.f = ff
	if err := l.Append("s1", []*pb.Event{msg("a")}, "/r.jsonl", 100); err != nil {
		t.Fatal(err)
	}
	return l, ff, path
}

// reopened checks that the file holds exactly the texts, in order.
func reopened(t *testing.T, l *Log, path string, texts ...string) {
	t.Helper()
	l.Close()
	l, err := Open(path)
	if err != nil {
		t.Fatalf("the log does not load after a failed append: %v", err)
	}
	defer l.Close()
	evs, _ := since(t, l, 0)
	if len(evs) != len(texts) {
		t.Fatalf("%d events on disk, want %d", len(evs), len(texts))
	}
	for i, e := range evs {
		if e.Seq != uint64(i+1) || e.GetAgentMessage().Text != texts[i] {
			t.Fatalf("event %d on disk: seq %d %q", i+1, e.Seq, e.GetAgentMessage().Text)
		}
	}
}

func TestFailedAppendLeavesNoBytesBehind(t *testing.T) {
	for name, fail := range map[string]func(*faultyFile){
		"torn write":  func(f *faultyFile) { f.tornWrite = 30 },
		"failed sync": func(f *faultyFile) { f.failSync = true },
	} {
		t.Run(name, func(t *testing.T) {
			l, ff, path := openFaulty(t)
			fail(ff)
			if err := l.Append("s1", []*pb.Event{msg("b"), msg("c")}, "/r.jsonl", 200); !errors.Is(err, errDisk) {
				t.Fatalf("append: %v", err)
			}
			if r, off := l.Checkpoint(); l.LastSeq() != 1 || r != "/r.jsonl" || off != 100 {
				t.Fatalf("a failed append moved the log: seq %d, checkpoint %s %d", l.LastSeq(), r, off)
			}
			// The daemon reads the line again, and other events go on.
			if err := l.Append("s1", []*pb.Event{msg("approval")}, "", 0); err != nil {
				t.Fatal(err)
			}
			if err := l.Append("s1", []*pb.Event{msg("b"), msg("c")}, "/r.jsonl", 200); err != nil {
				t.Fatal(err)
			}
			if evs, _ := since(t, l, 0); len(evs) != 4 || evs[3].Seq != 4 {
				t.Fatalf("events in memory: %v", evs)
			}
			reopened(t, l, path, "a", "approval", "b", "c")
		})
	}
}

func TestNothingIsAppendedAfterBytesThatCouldNotBeRemoved(t *testing.T) {
	l, ff, path := openFaulty(t)
	// The failed append cannot be cut off, and neither can it the next time.
	ff.tornWrite, ff.failTruncate = 30, 2
	if err := l.Append("s1", []*pb.Event{msg("b")}, "", 0); !errors.Is(err, errDisk) {
		t.Fatalf("append: %v", err)
	}
	if err := l.Append("s1", []*pb.Event{msg("c")}, "", 0); !errors.Is(err, errDisk) {
		t.Fatalf("append after bytes that were left behind: %v", err)
	}
	if l.LastSeq() != 1 {
		t.Fatalf("last seq %d", l.LastSeq())
	}
	// Once the file can be cut back the log goes on where it was.
	if err := l.Append("s1", []*pb.Event{msg("d")}, "", 0); err != nil {
		t.Fatal(err)
	}
	reopened(t, l, path, "a", "d")
}

func TestDamagedRecordIsNotTakenForATornTail(t *testing.T) {
	for name, damage := range map[string]func([]byte) []byte{
		// The second of three records no longer parses.
		"in the middle": func(b []byte) []byte {
			i := bytes.IndexByte(b, '\n') + 1
			return append(append(bytes.Clone(b[:i]), "{\"event\":{\"seq\":2,\n"...), b[i+bytes.IndexByte(b[i:], '\n')+1:]...)
		},
		// A whole last line is not what a torn write leaves either: the
		// record was written, and the server may hold its number.
		"at the end": func(b []byte) []byte {
			i := bytes.LastIndexByte(b[:len(b)-1], '\n') + 1
			return append(bytes.Clone(b[:i]), "{\"event\":{\"seq\":3,\n"...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			l, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"a", "b", "c"} {
				if err := l.Append("s1", []*pb.Event{msg(text)}, "", 0); err != nil {
					t.Fatal(err)
				}
			}
			l.Close()
			whole, _ := os.ReadFile(path)
			damaged := damage(whole)
			if err := os.WriteFile(path, damaged, 0o600); err != nil {
				t.Fatal(err)
			}
			if l, err := Open(path); err == nil {
				l.Close()
				t.Error("a log with a damaged record opened")
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(after, damaged) {
				t.Fatalf("opening a damaged log changed it: %d bytes, was %d", len(after), len(damaged))
			}
		})
	}
}

// tornAfter writes a log that holds "a", then the batch "b", "c", "d" read
// from one rollout line, and cuts the file after keep bytes of the
// batch's second record, as a crash during the write may: its first
// record is whole.
func tornAfter(t *testing.T, keep int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("s1", []*pb.Event{msg("a")}, "/r.jsonl", 100); err != nil {
		t.Fatal(err)
	}
	if err := l.Append("s1", []*pb.Event{msg("b"), msg("c"), msg("d")}, "/r.jsonl", 200); err != nil {
		t.Fatal(err)
	}
	l.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(b, []byte("\n"))
	if len(lines) != 5 { // four records and what follows the last newline
		t.Fatalf("%d lines", len(lines))
	}
	if err := os.WriteFile(path, b[:len(lines[0])+len(lines[1])+keep], 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A batch is the events of one rollout line, and its last record carries
// the checkpoint. A batch torn after its first record is dropped whole:
// kept, its events would be appended a second time when the line is read
// again from the old checkpoint.
func TestTornBatchIsDroppedWhole(t *testing.T) {
	for _, keep := range []int{0, 10} {
		path := tornAfter(t, keep)
		l, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if r, off := l.Checkpoint(); l.LastSeq() != 1 || r != "/r.jsonl" || off != 100 {
			t.Fatalf("keep %d: last seq %d, checkpoint %s %d", keep, l.LastSeq(), r, off)
		}
		// The line is read again, once.
		if err := l.Append("s1", []*pb.Event{msg("b"), msg("c"), msg("d")}, "/r.jsonl", 200); err != nil {
			t.Fatal(err)
		}
		reopened(t, l, path, "a", "b", "c", "d")
		l, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if r, off := l.Checkpoint(); r != "/r.jsonl" || off != 200 {
			t.Fatalf("keep %d: checkpoint %s %d", keep, r, off)
		}
		l.Close()
	}
}

// The first batch of a log, as a fork's copied history is: torn, nothing
// of it is left.
func TestTornFirstBatchLeavesAnEmptyLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("s1", []*pb.Event{msg("a"), msg("b"), msg("c")}, "", 0); err != nil {
		t.Fatal(err)
	}
	l.Close()
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, b[:bytes.LastIndexByte(b[:len(b)-1], '\n')+1], 0o600); err != nil {
		t.Fatal(err)
	}
	l, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if evs, _ := since(t, l, 0); l.LastSeq() != 0 || len(evs) != 0 {
		t.Fatalf("last seq %d, events %v", l.LastSeq(), evs)
	}
	if st, _ := os.Stat(path); st.Size() != 0 {
		t.Fatalf("%d bytes left", st.Size())
	}
}

// A complete batch is kept, also when a torn line follows it, and after
// the events were released from memory.
func TestCompleteBatchIsKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("s1", []*pb.Event{msg("a"), msg("b"), msg("c")}, "/r.jsonl", 100); err != nil {
		t.Fatal(err)
	}
	l.Release(0)
	if evs, _ := since(t, l, 0); len(evs) != 3 {
		t.Fatalf("%d events read again from the file", len(evs))
	}
	reopened(t, l, path, "a", "b", "c")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"more":true,"event":{"seq":4`)
	f.Close()
	l, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if r, off := l.Checkpoint(); l.LastSeq() != 3 || r != "/r.jsonl" || off != 100 {
		t.Fatalf("last seq %d, checkpoint %s %d", l.LastSeq(), r, off)
	}
}

// A log written before batches were marked reads as it always did: every
// whole record counts, the last batch's too.
func TestLogWithoutBatchMarksLoadsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	old := `{"rollout":"/r.jsonl","offset":100,"event":{"seq":"1","sessionId":"s1","agentMessage":{"text":"a"}}}
{"event":{"seq":"2","sessionId":"s1","agentMessage":{"text":"b"}}}
{"event":{"seq":"3","sessionId":"s1","agentMessage":{"text":"c"}}}
`
	if err := os.WriteFile(path, []byte(old+`{"rollout":"/r.jsonl","offset":200,"event":{"seq":"4"`), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if r, off := l.Checkpoint(); l.LastSeq() != 3 || r != "/r.jsonl" || off != 100 {
		t.Fatalf("last seq %d, checkpoint %s %d", l.LastSeq(), r, off)
	}
	reopened(t, l, path, "a", "b", "c")
	if b, _ := os.ReadFile(path); string(b) != old {
		t.Fatalf("file changed: %q", b)
	}
}
