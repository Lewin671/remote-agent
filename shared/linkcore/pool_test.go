package linkcore

import "testing"

func TestPoolCountsReplacementsAndAllUses(t *testing.T) {
	p := NewPool(8)
	for _, peer := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		if !p.Acquire(peer, "test", 1) || !p.Acquire(peer, "test", 2) || p.Acquire(peer, "test", 1) {
			t.Fatal("per-peer PC cap")
		}
	}
	if p.Acquire("i", "test", 1) {
		t.Fatal("peer cap")
	}
	if !p.Reserve("a", 0, 2<<20, 128) || p.Reserve("a", 0, 1, 1) {
		t.Fatal("peer send cap")
	}
	p.Free("a", 0, 2<<20, 128)
	if !p.Reserve("a", 1, 2<<20, 128) || p.Reserve("a", 1, 1, 0) {
		t.Fatal("peer receive cap")
	}
	p.Free("a", 1, 2<<20, 128)
	if p.Reserved(0) != 0 || p.Reserved(1) != 0 {
		t.Fatal("freed bytes stayed reserved")
	}
}

func TestTokensRefillByElapsedMilliseconds(t *testing.T) {
	var b Tokens
	b, ok := b.Take(1000, 100, 2, 1000, 2)
	if !ok || b.Frames != 1 || b.Bytes != 900 {
		t.Fatalf("fresh bucket starts full: %+v", b)
	}
	if b, ok = b.Take(1000, 100, 2, 1000, 2); !ok {
		t.Fatal("burst")
	}
	if b, ok = b.Take(1000, 1, 2, 1000, 2); ok || b.Frames != 0 {
		t.Fatal("frame burst exceeded")
	}
	// 250 ms at 2 frames/s refills half a frame: still refused, refill kept.
	if b, ok = b.Take(1250, 1, 2, 1000, 2); ok || b.Frames != 0.5 {
		t.Fatalf("partial refill: %+v", b)
	}
	if _, ok = b.Take(1500, 1, 2, 1000, 2); !ok {
		t.Fatal("refill by elapsed time")
	}
}
