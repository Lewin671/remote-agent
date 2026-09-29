package eventlog

import (
	"os"
	"path/filepath"
	"testing"

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
