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
	// 1. Small initial host candidates flush promptly at 50ms without waiting for end=true.
	b := CandidateBatcher{Generation: 1, Deadline: 10000, Binding: 1}
	for i := 0; i < 4; i++ {
		if !b.Add([]byte{byte(i)}, 1, 5, 1) {
			t.Fatalf("add %d failed", i)
		}
	}
	groups := b.Flush(55, false, 1, 1)
	if len(groups) != 1 || len(groups[0]) != 4 {
		t.Fatalf("expected 1 early batch of 4 at 55ms, got %d batches", len(groups))
	}
	if b.Batches != 1 || b.Count != 4 {
		t.Fatalf("after batch 0: batches=%d count=%d", b.Batches, b.Count)
	}

	// 2. Subsequent srflx candidates in batches 1 and 2 also trickle at 50ms.
	if !b.Add([]byte{10}, 1, 80, 1) {
		t.Fatal("add srflx 1 failed")
	}
	groups = b.Flush(130, false, 1, 1)
	if len(groups) != 1 || len(groups[0]) != 1 || b.Batches != 2 {
		t.Fatalf("expected batch 1 of 1 at 130ms, got %d batches (total=%d)", len(groups), b.Batches)
	}
	if !b.Add([]byte{11}, 1, 200, 1) {
		t.Fatal("add srflx 2 failed")
	}
	groups = b.Flush(250, false, 1, 1)
	if len(groups) != 1 || len(groups[0]) != 1 || b.Batches != 3 {
		t.Fatalf("expected batch 2 of 1 at 250ms, got %d batches (total=%d)", len(groups), b.Batches)
	}

	// 3. Batch 3 (4th and final wire batch) waits for 16 candidates or end=true without timer spin.
	if !b.Add([]byte{12}, 1, 300, 1) {
		t.Fatal("add batch 3 cand failed")
	}
	if b.Due != 0 {
		t.Fatalf("expected no 50ms timer spin on final batch 3, got Due=%d", b.Due)
	}
	if got := b.Flush(500, false, 1, 1); len(got) != 0 {
		t.Fatalf("expected final batch 3 to wait for 16 or end, got %d batches", len(got))
	}

	// 4. Adding remaining candidates up to 64 total never panics, respects 4-batch wire limit,
	// keeps Count == total emitted, and rejects the 65th candidate.
	emitted := 4 + 1 + 1 // already emitted in batches 0..2
	for i := 7; i < 64; i++ {
		if !b.Add([]byte{byte(i)}, 1, 600, 1) {
			t.Fatalf("add #%d (within 64 budget) unexpectedly rejected", i+1)
		}
	}
	if b.Add([]byte{99}, 1, 600, 1) {
		t.Fatal("65th candidate must be rejected")
	}
	for _, g := range b.Flush(700, true, 1, 1) {
		emitted += len(g)
	}
	if b.Batches > 4 {
		t.Fatalf("batches exceeded 4: %d", b.Batches)
	}
	if b.Count != emitted {
		t.Fatalf("EndCandidates Count mismatch: b.Count=%d emitted=%d", b.Count, emitted)
	}
	if emitted != 6+16 {
		t.Fatalf("expected 6+16=22 emitted across 4 wire batches, got %d", emitted)
	}

	// 5. End=true flushes immediately even before 50ms.
	b2 := CandidateBatcher{Generation: 1, Deadline: 10000, Binding: 1}
	if !b2.Add([]byte{1}, 1, 10, 1) {
		t.Fatal("b2 add failed")
	}
	groups = b2.Flush(15, true, 1, 1)
	if len(groups) != 1 || len(groups[0]) != 1 || !b2.Ended {
		t.Fatalf("expected immediate flush on end=true, got %d", len(groups))
	}
}
