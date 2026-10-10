package eventlog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func msg(text string) *pb.Event {
	return &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: text}}}
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
	ch := func() <-chan struct{} { _, c := l.Since(0); return c }()
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
	evs, _ := l.Since(2)
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
	if evs, _ := l.Since(2); len(evs) != 0 || l.events != nil {
		t.Fatalf("waiting for more loaded the log: %v", evs)
	}
	if err := l.Append("s1", []*pb.Event{msg("c")}, "", 0); err != nil {
		t.Fatal(err)
	}
	if l.events != nil || l.LastSeq() != 3 {
		t.Fatalf("append to a released log: loaded=%v last=%d", l.events != nil, l.LastSeq())
	}
	evs, _ := l.Since(1)
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
	if evs, _ := l.Since(0); len(evs) != 4 || evs[3].Seq != 4 {
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
	evs, _ := l.Since(0)
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
			if evs, _ := l.Since(0); len(evs) != 4 || evs[3].Seq != 4 {
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
