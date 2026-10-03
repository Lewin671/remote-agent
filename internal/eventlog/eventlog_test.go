package eventlog

import (
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
