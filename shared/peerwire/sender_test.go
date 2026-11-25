package peerwire

import "testing"

func TestSenderAndBatcher(t *testing.T) {
	check := func(ok bool) {
		t.Helper()
		if !ok {
			t.Fatal("sender/batcher invariant")
		}
	}
	s := NewSender(10000)
	var rows [][]byte
	for i := 0; i < 33; i++ {
		rows = append(rows, []byte{byte(i)})
	}
	for _, r := range rows[:32] {
		check(s.Enqueue(r, 0, 10000, 1))
	}
	check(!s.Enqueue(rows[32], 0, 10000, 1))
	check(len(s.Tick(0)) == 32)
	check(len(s.Tick(499)) == 0)
	check(len(s.Tick(500)) == 32)
	check(len(s.Tick(1500)) == 32)
	check(len(s.Tick(2000)) == 0)
	for _, r := range rows[:32] {
		check(s.Ack(Hash(r)))
	}
	check(s.Enqueue(rows[32], 2000, 10000, 1))
	check(len(s.Tick(2000)) == 1)
	s.Limited(Hash(rows[32]), 2000, 9000)
	check(len(s.Tick(9999)) == 0)
	check(len(s.Tick(10000)) == 0 && s.TimedOut && len(s.Pending) == 0)
	check(s.Backoff(10000, .5) == 10500)
	for i := uint64(1); i <= 1000; i++ {
		s.NetworkChanged(i)
	}
	check(s.RetryAt == 10500)
	check(s.Backoff(10500, .5) == 11500)
	p := NewSender(10000)
	check(p.Enqueue(rows[0], 0, 10000, 1))
	check(len(p.Tick(0)) == 1)
	p.Limited(Hash(rows[0]), 0, 800)
	check(len(p.Tick(500)) == 0)
	check(len(p.Tick(800)) == 1)
	check(len(p.Tick(1500)) == 1)
	p.Limited(Hash(rows[0]), 10001, 5)
	check(len(p.Tick(10001)) == 0)
	b := CandidateBatcher{Generation: 2, Deadline: 10000, Binding: 1}
	check(!b.Add([]byte{1}, 1, 0, 1))
	for i := 0; i < 64; i++ {
		check(b.Add([]byte{byte(i)}, 2, 0, 1))
	}
	check(!b.Add([]byte{65}, 2, 0, 1))
	check(len(b.Flush(49, false, 2, 1)) == 0)
	check(len(b.Flush(50, false, 2, 1)) == 4)
	check(len(b.Flush(51, true, 2, 1)) == 0)
	check(!b.Add([]byte{1}, 2, 52, 1))
}

func TestOfflineDiscoveryKeepsOriginalRetryWindow(t *testing.T) {
	s := NewSender(10000)
	raw := []byte{1, 2, 3}
	if !s.Enqueue(raw, 0, 10000, 1) || len(s.Tick(0)) != 1 {
		t.Fatal("initial send")
	}
	s.Offline()
	if len(s.Tick(500)) != 0 {
		t.Fatal("offline retried without discovery")
	}
	s.Discovered()
	if len(s.Tick(1500)) != 1 || len(s.Tick(1501)) != 1 || len(s.Tick(1502)) != 0 {
		t.Fatal("discovery changed three-attempt cap")
	}
	s.Offline()
	s.Discovered()
	if len(s.Tick(10000)) != 0 || !s.TimedOut {
		t.Fatal("discovery extended total deadline")
	}
}

func TestCandidateBatcherTrickleAndBounds(t *testing.T) {
	// 1. Typical Trickle timing: initial host burst flushes at 50ms (batch 0),
	//    subsequent STUN srflx flushes at 50ms (batch 1), batches 2 & 3 wait for 16 or end=true.
	b := CandidateBatcher{Generation: 1, Deadline: 10000, Binding: 1}
	for i := 0; i < 4; i++ {
		if !b.Add([]byte{byte(i)}, 1, 5, 1) {
			t.Fatalf("add host %d failed", i)
		}
	}
	groups := b.Flush(55, false, 1, 1)
	if len(groups) != 1 || len(groups[0]) != 4 || b.Batches != 1 || b.Count != 4 || b.Dropped != 0 {
		t.Fatalf("batch 0 host burst: groups=%d batches=%d count=%d dropped=%d", len(groups), b.Batches, b.Count, b.Dropped)
	}

	// STUN srflx arrives later at 80ms -> flushes as batch 1 at 130ms without waiting for end=true.
	if !b.Add([]byte{10}, 1, 80, 1) || !b.Add([]byte{11}, 1, 85, 1) {
		t.Fatal("add srflx failed")
	}
	groups = b.Flush(130, false, 1, 1)
	if len(groups) != 1 || len(groups[0]) != 2 || b.Batches != 2 || b.Count != 6 || b.Dropped != 0 {
		t.Fatalf("batch 1 srflx: groups=%d batches=%d count=%d dropped=%d", len(groups), b.Batches, b.Count, b.Dropped)
	}

	// Batch 2 and 3 hold until 16 candidates accumulate or end=true (no 50ms timer spin).
	if !b.Add([]byte{12}, 1, 200, 1) {
		t.Fatal("add batch 2 cand failed")
	}
	if b.Due != 0 {
		t.Fatalf("expected no 50ms timer spin on batch 2, got Due=%d", b.Due)
	}
	if got := b.Flush(300, false, 1, 1); len(got) != 0 {
		t.Fatalf("expected batch 2 to wait for 16 or end, got %d batches", len(got))
	}
	groups = b.Flush(400, true, 1, 1)
	if len(groups) != 1 || len(groups[0]) != 1 || b.Batches != 3 || b.Count != 7 || b.Dropped != 0 {
		t.Fatalf("tail flush: groups=%d batches=%d count=%d dropped=%d", len(groups), b.Batches, b.Count, b.Dropped)
	}

	// 2. Worst-case 40 candidates (1 in batch 0, 1 in batch 1, then 38 more):
	//    never panics, emits >= 34 (exact 1+1+16+16=34), counts 6 drops, End Count == emitted,
	//    and still enforces the 64-candidate hard budget.
	w := CandidateBatcher{Generation: 1, Deadline: 10000, Binding: 1}
	emitted := 0
	flushEmitted := func(now uint64, end bool) {
		for _, g := range w.Flush(now, end, 1, 1) {
			emitted += len(g)
		}
	}
	if !w.Add([]byte{0}, 1, 0, 1) {
		t.Fatal("add #1")
	}
	flushEmitted(50, false) // batch 0: 1
	if !w.Add([]byte{1}, 1, 100, 1) {
		t.Fatal("add #2")
	}
	flushEmitted(150, false) // batch 1: 1
	if w.Batches != 2 || emitted != 2 {
		t.Fatalf("expected 2 early batches with 2 emitted, got batches=%d emitted=%d", w.Batches, emitted)
	}
	for i := 2; i < 40; i++ {
		if !w.Add([]byte{byte(i)}, 1, 200+uint64(i), 1) {
			t.Fatalf("add #%d rejected unexpectedly", i+1)
		}
		if len(w.Pending) >= 16 {
			flushEmitted(200+uint64(i), false)
		}
	}
	flushEmitted(500, true)
	if w.Batches != 4 {
		t.Fatalf("expected 4 wire batches, got %d", w.Batches)
	}
	if emitted < 34 || emitted != 34 {
		t.Fatalf("expected >=34 (exact 34) emitted in worst-case 40-candidate burst, got %d", emitted)
	}
	if w.Dropped != 6 {
		t.Fatalf("expected 6 dropped candidates counted, got %d", w.Dropped)
	}
	if w.Count != emitted {
		t.Fatalf("EndCandidates Count mismatch: w.Count=%d emitted=%d", w.Count, emitted)
	}

	// 3. 64-candidate hard budget still rejects the 65th candidate even when batches are full.
	capB := CandidateBatcher{Generation: 1, Deadline: 10000, Binding: 1}
	for i := 0; i < 64; i++ {
		if !capB.Add([]byte{byte(i)}, 1, 0, 1) {
			t.Fatalf("capB add #%d failed", i+1)
		}
	}
	if capB.Add([]byte{65}, 1, 0, 1) {
		t.Fatal("65th candidate must be rejected")
	}

	// 4. End=true flushes immediately even before 50ms.
	b2 := CandidateBatcher{Generation: 1, Deadline: 10000, Binding: 1}
	if !b2.Add([]byte{1}, 1, 10, 1) {
		t.Fatal("b2 add failed")
	}
	groups = b2.Flush(15, true, 1, 1)
	if len(groups) != 1 || len(groups[0]) != 1 || !b2.Ended {
		t.Fatalf("expected immediate flush on end=true, got %d", len(groups))
	}
}
