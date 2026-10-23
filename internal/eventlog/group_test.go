package eventlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/tail"
	"google.golang.org/protobuf/proto"
)

type groupFile struct {
	file
	writes, syncs int
	write         func([]byte, int64) (int, error)
	sync          func() error
}

func (f *groupFile) WriteAt(b []byte, off int64) (int, error) {
	f.writes++
	if f.write != nil {
		return f.write(b, off)
	}
	return f.file.WriteAt(b, off)
}

func (f *groupFile) Sync() error {
	f.syncs++
	if f.sync != nil {
		return f.sync()
	}
	return f.file.Sync()
}

func TestAppendGroupPreservesLineBatchesAndRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	f := &groupFile{file: l.f}
	l.f = f
	var batches []Batch
	for i := range 19 {
		batches = append(batches, Batch{Events: []*pb.Event{msg("a"), msg("b")}, Rollout: "/r.jsonl", Offset: int64(i+1) * 100})
	}
	for _, want := range []int{8, 8, 3} {
		n, err := l.AppendGroup("s1", batches)
		if err != nil || n != want {
			t.Fatalf("committed %d lines, want %d: %v", n, want, err)
		}
		batches = batches[n:]
	}
	if f.writes != 3 || f.syncs != 3 {
		t.Fatalf("writes=%d syncs=%d, want three groups", f.writes, f.syncs)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatal(err)
		}
		if rec.More != (i%2 == 0) {
			t.Fatalf("line batch marker lost at event %d", i+1)
		}
		if i%2 == 1 && (rec.Rollout != "/r.jsonl" || rec.Offset != int64(i/2+1)*100) {
			t.Fatalf("line checkpoint lost at event %d: %+v", i+1, rec)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	events, _ := since(t, restored, 0)
	if len(events) != 38 || restored.CheckpointSeq() != 38 {
		t.Fatalf("restored events=%d checkpointSeq=%d", len(events), restored.CheckpointSeq())
	}
	for i, e := range events {
		if e.Seq != uint64(i+1) || e.SessionId != "s1" || e.GetAgentMessage().Text != []string{"a", "b"}[i%2] {
			t.Fatalf("restored event %d: %v", i+1, e)
		}
	}
	if path, off := restored.Checkpoint(); path != "/r.jsonl" || off != 1900 {
		t.Fatalf("restored checkpoint %q %d", path, off)
	}
}

func TestAppendGroupBoundsEncodedBytes(t *testing.T) {
	l, _, _ := openFaulty(t)
	defer l.Close()
	f := &groupFile{file: l.f}
	l.f = f
	var batches []Batch
	for range 4 {
		batches = append(batches, Batch{Events: []*pb.Event{msg(strings.Repeat("x", maxGroupBytes/3))}})
	}
	before := l.size
	n, err := l.AppendGroup("s1", batches)
	if err != nil || n != 2 || l.size-before > maxGroupBytes {
		t.Fatalf("byte-bounded group: n=%d bytes=%d err=%v", n, l.size-before, err)
	}
	// A source line is indivisible, even if it alone exceeds the cap.
	large := Batch{Events: []*pb.Event{msg(strings.Repeat("x", maxGroupBytes)), msg("same source line")}, Rollout: "/r.jsonl", Offset: 200}
	before = l.size
	n, err = l.AppendGroup("s1", []Batch{large, {Events: []*pb.Event{msg("next line")}}})
	if err != nil || n != 1 || l.size-before <= maxGroupBytes {
		t.Fatalf("oversized standalone line: n=%d bytes=%d err=%v", n, l.size-before, err)
	}
	if f.syncs != 2 {
		t.Fatalf("syncs=%d, want 2", f.syncs)
	}
}

func TestAppendGroupFailureRollsBackDiskMemoryAndInputs(t *testing.T) {
	for _, failure := range []string{"write", "short write", "sync", "encoding"} {
		t.Run(failure, func(t *testing.T) {
			l, _, path := openFaulty(t)
			defer l.Close()
			// The failed group would add an index anchor and evict cached
			// events, so rollback must leave both structures untouched.
			seed := make([]*pb.Event, cacheEvents-1)
			for i := range seed {
				seed[i] = msg("seed")
			}
			if err := l.Append("s1", seed, "", 0); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := l.snapshotLocked()
			if err != nil {
				t.Fatal(err)
			}
			checkpointSeq := l.CheckpointSeq()
			batches := []Batch{
				{Rollout: "/r.jsonl", Offset: 150},
				{Events: []*pb.Event{msg("b"), msg("c")}, Rollout: "/r.jsonl", Offset: 200},
				{Events: []*pb.Event{msg("d"), msg("e")}, Rollout: "/r.jsonl", Offset: 300},
				{Rollout: "/r.jsonl", Offset: 400},
			}
			f := &groupFile{file: l.f}
			l.f = f
			wantErr := errDisk
			switch failure {
			case "write", "short write":
				f.write = func(b []byte, off int64) (int, error) {
					// Leave a whole line prefix and part of the next line.
					n, err := f.file.WriteAt(b[:len(b)*3/4], off)
					if err != nil {
						return n, err
					}
					if failure == "short write" {
						return n, nil
					}
					return n, errDisk
				}
				if failure == "short write" {
					wantErr = io.ErrShortWrite
				}
			case "sync":
				f.sync = func() error { return errDisk }
			case "encoding":
				batches[2].Events[1].GetAgentMessage().Text = "\xff"
			}
			originals := make([]*pb.Event, 0)
			for _, batch := range batches {
				for _, e := range batch.Events {
					originals = append(originals, proto.Clone(e).(*pb.Event))
				}
			}
			n, err := l.AppendGroup("s1", batches)
			if n != 0 || err == nil || (failure != "encoding" && !errors.Is(err, wantErr)) {
				t.Fatalf("failed group: n=%d err=%v", n, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed group changed disk: %v", err)
			}
			got, err := l.snapshotLocked()
			if err != nil || !reflect.DeepEqual(snapshot, got) || l.checkpointSeq != checkpointSeq || l.rollout != "/r.jsonl" || l.offset != 100 || l.dirty {
				t.Fatalf("failed group changed memory: %+v err=%v", got, err)
			}
			i := 0
			for _, batch := range batches {
				for _, e := range batch.Events {
					if !proto.Equal(e, originals[i]) {
						t.Fatalf("failed group modified caller event %d", i)
					}
					i++
				}
			}
			f.write, f.sync = nil, nil
			batches[2].Events[1].GetAgentMessage().Text = "e"
			if n, err := l.AppendGroup("s1", batches); err != nil || n != 4 {
				t.Fatalf("retry: n=%d err=%v", n, err)
			}
			if _, off := l.Checkpoint(); off != 400 || l.CheckpointSeq() != cacheEvents+4 {
				t.Fatalf("empty line checkpoint=%d seq=%d", off, l.CheckpointSeq())
			}
			texts := []string{"a"}
			for range seed {
				texts = append(texts, "seed")
			}
			texts = append(texts, "b", "c", "d", "e")
			reopened(t, l, path, texts...)
		})
	}
}

func TestAppendGroupDoesNotPublishBeforeSync(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			l, _, _ := openFaulty(t)
			defer l.Close()
			old, changed := since(t, l, 0)
			snapshot, err := l.snapshotLocked()
			if err != nil {
				t.Fatal(err)
			}
			// Exercise an old disk scan, rather than serving this snapshot
			// from its cache while the writer has unsynced bytes on disk.
			snapshot.events, snapshot.start = nil, snapshot.n+1
			entered, finish := make(chan struct{}), make(chan struct{})
			f := &groupFile{file: l.f}
			f.sync = func() error {
				close(entered)
				<-finish
				if fail {
					return errDisk
				}
				return f.file.Sync()
			}
			l.f = f
			events := []*pb.Event{msg("b"), msg("c")}
			done := make(chan error, 1)
			go func() {
				_, err := l.AppendGroup("s1", []Batch{
					{Events: events[:1], Rollout: "/r.jsonl", Offset: 200},
					{Events: events[1:], Rollout: "/r.jsonl", Offset: 300},
				})
				done <- err
			}()
			<-entered
			// Every new reader must take this mutex to obtain a snapshot.
			if l.mu.TryLock() {
				l.mu.Unlock()
				t.Error("readers can acquire an unsynced snapshot")
			}
			select {
			case <-changed:
				t.Error("reader notified before Sync")
			default:
			}
			if l.n != 1 || l.offset != 100 || l.checkpointSeq != 1 || len(l.events) != 1 || len(l.index) != 1 {
				t.Error("unsynced log state was published")
			}
			for _, e := range events {
				if e.Seq != 0 || e.SessionId != "" {
					t.Error("unsynced caller event was modified")
				}
			}
			// A reader already scanning a durable snapshot is limited to its
			// old size, even though unsynced bytes are now present on disk.
			var scanned []*pb.Event
			if err := scanRange(snapshot, 0, snapshot.n, func(e *pb.Event) error {
				scanned = append(scanned, e)
				return nil
			}); err != nil || len(scanned) != 1 || !proto.Equal(old[0], scanned[0]) {
				t.Errorf("old snapshot saw unsynced events: %v %v", scanned, err)
			}
			close(finish)
			err = <-done
			if (err != nil) != fail {
				t.Fatalf("commit error=%v, failure=%v", err, fail)
			}
			want := 3
			if fail {
				want = 1
			}
			page, next, err := l.PageSince(0, 10)
			if err != nil || len(page) != want || (next == changed) != fail {
				t.Fatalf("reader after Sync: events=%d same notification=%v err=%v", len(page), next == changed, err)
			}
			select {
			case <-changed:
				if fail {
					t.Fatal("failed group notified readers")
				}
			default:
				if !fail {
					t.Fatal("successful group did not notify readers")
				}
			}
		})
	}
}

func TestAppendGroupCrashKeepsCompleteLinePrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(rollout, []byte("a\nb\nc\nd\n"), 0600); err != nil {
		t.Fatal(err)
	}
	makeBatch := func(text string, offset int64) Batch {
		return Batch{Events: []*pb.Event{msg(text), msg(text + " second")}, Rollout: rollout, Offset: offset}
	}
	first := makeBatch("a", 2)
	if err := l.Append("s1", first.Events, rollout, 2); err != nil {
		t.Fatal(err)
	}
	_, changed := since(t, l, 0)
	group := []Batch{makeBatch("b", 4), makeBatch("c", 6), makeBatch("d", 8)}
	f := &groupFile{file: l.f}
	l.f = f
	crash := errors.New("simulated crash before Sync")
	f.sync = func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		tail := bytes.SplitAfter(data[l.size:], []byte("\n"))
		// The first source line survives in full; the next line has one
		// complete event and a torn second event. No group ACK was issued.
		end := l.size + int64(len(tail[0])+len(tail[1])+len(tail[2])+10)
		if err := f.file.Truncate(end); err != nil {
			t.Fatal(err)
		}
		panic(crash) // bypass error rollback, as process death would
	}
	func() {
		defer func() {
			if got := recover(); got != crash {
				t.Fatalf("crash=%v", got)
			}
		}()
		_, _ = l.AppendGroup("s1", group)
		t.Fatal("simulated crash did not run")
	}()
	if l.LastSeq() != 2 {
		t.Fatal("unacknowledged group advanced memory")
	}
	select {
	case <-changed:
		t.Fatal("unacknowledged group notified readers")
	default:
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	checkpointPath, offset := restored.Checkpoint()
	if checkpointPath != rollout || offset != 4 || restored.LastSeq() != 4 || restored.CheckpointSeq() != 4 {
		t.Fatalf("surviving prefix: checkpoint=%q %d seq=%d", checkpointPath, offset, restored.LastSeq())
	}
	reader := tail.NewReader(checkpointPath, offset)
	lines, truncated, err := reader.Next()
	if err != nil || truncated || len(lines) != 2 {
		t.Fatalf("resume transcript: lines=%d truncated=%v err=%v", len(lines), truncated, err)
	}
	var remaining []Batch
	for _, line := range lines {
		remaining = append(remaining, makeBatch(strings.TrimSpace(string(line.Data)), line.End))
	}
	if n, err := restored.AppendGroup("s1", remaining); err != nil || n != 2 {
		t.Fatalf("resume append: n=%d err=%v", n, err)
	}
	events, _ := since(t, restored, 0)
	texts := []string{"a", "a second", "b", "b second", "c", "c second", "d", "d second"}
	if len(events) != len(texts) {
		t.Fatalf("duplicate or missing events: %v", events)
	}
	for i, e := range events {
		if e.Seq != uint64(i+1) || e.GetAgentMessage().Text != texts[i] {
			t.Fatalf("event %d: %v", i+1, e)
		}
	}
	if _, offset := restored.Checkpoint(); offset != 8 {
		t.Fatalf("final checkpoint=%d", offset)
	}
}
