package transport

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func TestExecutionQueueBudgetsAndEmptyPeerException(t *testing.T) {
	for _, entries := range []int{32, 16, 64, 16} {
		q := FairQueue[int]{Entries: entries, Bytes: ExecutionQueueBytes, PeerEntries: entries / 4, PeerBytes: ExecutionQueueBytes / 4, EmptyPeerException: true}
		if !q.Push("peer", "session", 4<<20, 1) {
			t.Fatal("legal decoded body rejected from empty queue")
		}
		if q.Push("peer", "other", 1, 2) {
			t.Fatal("second ID bypassed oversized peer reservation")
		}
		if q.Push("other", "other", 1, 3) {
			t.Fatal("global byte cap bypassed")
		}
		if !q.Remove(1) || q.UsedBytes() != 0 {
			t.Fatal("bytes were not returned")
		}
		for i := 0; i < entries/4; i++ {
			if !q.Push("peer", "session", 1, i) {
				t.Fatal("premature count refusal")
			}
		}
		if q.Push("peer", "session", 1, 999) {
			t.Fatal("peer entry cap bypassed")
		}
	}
}
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
func TestPoolsNeverBorrowAndApprovalsSurviveInputOccupancy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		link := NewLink(context.Background())
		defer link.Close()
		defer link.DrainCalls()
		scheduler := NewScheduler(link)
		defer scheduler.Close()
		release := make(chan struct{})
		defer close(release)
		started := make(chan Class, 16)
		submit := func(class Class, n int, approval bool) {
			for i := 0; i < n; i++ {
				j := &Job{Class: class, Peer: "peer", Session: fmt.Sprintf("%d-%d", class, i), Bytes: 1, Deadline: time.Now().Add(time.Minute), Approval: approval, Reject: func() {}, Run: func(context.Context, *Execution) { started <- class; <-release }}
				if !scheduler.Submit(context.Background(), j) {
					t.Fatal("submit")
				}
			}
		}
		submit(Ordinary, 6, false)
		submit(Bulk, 4, false)
		submit(Input, 4, false)
		synctest.Wait()
		if got := scheduler.Active(); got != [3]int{1, 4, 2} {
			t.Fatalf("pool occupancy %v", got)
		}
		approval := make(chan struct{})
		if !scheduler.Submit(context.Background(), &Job{Class: Control, Peer: "peer", Session: "approval", Bytes: 1, Approval: true, Deadline: time.Now().Add(time.Minute), Reject: func() { t.Error("approval rejected") }, Run: func(context.Context, *Execution) { close(approval) }}) {
			t.Fatal("approval submit")
		}
		synctest.Wait()
		select {
		case <-approval:
		default:
			t.Fatal("other pools or input took the approval slot")
		}
	})
}
func TestControlPrioritySessionLimitAndPathIndependence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		link := NewLink(context.Background())
		defer link.Close()
		defer link.DrainCalls()
		scheduler := NewScheduler(link)
		defer scheduler.Close()
		release := make(chan struct{})
		approvalRelease := make(chan struct{})
		defer close(approvalRelease)
		order := make(chan string, 8)
		waiting, cancel := context.WithCancel(context.Background())
		defer cancel()
		makeJob := func(class Class, name, session string, approval bool, hold <-chan struct{}) *Job {
			return &Job{Class: class, Peer: "peer", Session: session, Approval: approval, Bytes: 1, Deadline: time.Now().Add(time.Minute), Reject: func() { t.Errorf("%s rejected", name) }, Run: func(ctx context.Context, _ *Execution) {
				order <- name
				if hold != nil {
					<-hold
				}
				if ctx.Err() != nil {
					t.Errorf("path canceled %s", name)
				}
			}}
		}
		if !scheduler.Submit(waiting, makeJob(Input, "running", "a", false, release)) {
			t.Fatal("submit")
		}
		scheduler.Submit(context.Background(), makeJob(Control, "approval", "b", true, approvalRelease))
		synctest.Wait()
		<-order
		<-order
		// Same-session approvals cannot use a second slot. Other control work must
		// nevertheless take precedence over already queued input after release.
		scheduler.Submit(context.Background(), makeJob(Input, "input", "c", false, nil))
		scheduler.Submit(context.Background(), makeJob(Control, "control", "a", true, nil))
		synctest.Wait()
		if len(order) != 0 {
			t.Fatal("same session occupied two interactive slots")
		}
		cancel()
		close(release)
		synctest.Wait()
		if got := <-order; got != "control" {
			t.Fatalf("input jumped ahead of control: %s", got)
		}
	})
}
func TestQueueExpiryAndQuarantineKeepOriginalSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		link := NewLink(context.Background())
		defer link.Close()
		defer link.DrainCalls()
		scheduler := NewScheduler(link)
		defer scheduler.Close()
		release := make(chan struct{})
		defer close(release)
		entered := make(chan struct{})
		j := &Job{Class: Ordinary, Peer: "peer", Resources: []string{"resource"}, Bytes: 1, Deadline: time.Now().Add(time.Second), Reject: func() { t.Error("initial reject") }, Run: func(ctx context.Context, e *Execution) { close(entered); <-ctx.Done(); e.Quarantine(); <-release }}
		scheduler.Submit(context.Background(), j)
		synctest.Wait()
		<-entered
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if scheduler.Active()[1] != 1 {
			t.Fatal("deadline released a running callback")
		}
		if scheduler.Submit(context.Background(), &Job{Class: Ordinary, Peer: "other", Resources: []string{"resource"}, Bytes: 1, Deadline: time.Now().Add(time.Minute)}) {
			t.Fatal("quarantined resource admitted work")
		}
		ran := false
		if !scheduler.Submit(context.Background(), &Job{Class: Control, Peer: "other", Session: "healthy", Approval: true, Bytes: 1, Deadline: time.Now().Add(time.Minute), Run: func(context.Context, *Execution) { ran = true }, Reject: func() { t.Error("healthy control rejected") }}) {
			t.Fatal("healthy submit")
		}
		synctest.Wait()
		if !ran {
			t.Fatal("quarantine affected unrelated resource")
		}
	})
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
