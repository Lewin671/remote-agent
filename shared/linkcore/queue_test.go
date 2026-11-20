package linkcore

import "testing"

func TestQueueRotatesPeersAndSessions(t *testing.T) {
	q := FairQueue[string]{Entries: 64, Bytes: 4096, PeerEntries: 16, PeerBytes: 1024}
	for _, v := range []struct{ peer, flow, value string }{{"p", "a", "a1"}, {"p", "a", "a2"}, {"p", "b", "b1"}, {"q", "c", "c1"}} {
		if !q.Push(v.peer, v.flow, 1, v.value) {
			t.Fatal("push")
		}
	}
	for _, want := range []string{"a1", "c1", "b1", "a2"} {
		got, ok := q.Pop(func(string) bool { return true })
		if !ok || got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

func TestQueueSkipsBlockedResourceInSamePeerFlow(t *testing.T) {
	q := FairQueue[string]{Entries: 64, Bytes: 4096, PeerEntries: 16, PeerBytes: 1024}
	q.Push("peer", "machine", 1, "quarantined")
	q.Push("peer", "machine", 1, "healthy")
	got, ok := q.Pop(func(resource string) bool { return resource != "quarantined" })
	if !ok || got != "healthy" {
		t.Fatal("quarantined resource blocked an unrelated request from the same peer")
	}
	if q.Len() != 1 {
		t.Fatal("blocked request was discarded")
	}
}
