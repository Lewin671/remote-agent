package linkcore

import (
	"crypto/sha256"
	"testing"
)

func TestCipherDedupBoundsAndExpiry(t *testing.T) {
	d := NewCipherDedup()
	h1 := sha256.Sum256([]byte("ciphertext-1"))
	h2 := sha256.Sum256([]byte("ciphertext-2"))

	// Unrecorded hash is not seen.
	if d.Seen("p1", h1, 1000) {
		t.Fatal("unverified hash must not be seen")
	}

	d.RecordVerified("p1", h1, 1000)
	if !d.Seen("p1", h1, 1000) || !d.Seen("p1", h1, 10_999) {
		t.Fatal("expected hash seen within 10s TTL")
	}
	if d.Seen("p2", h1, 1000) {
		t.Fatal("dedup cache must be isolated per peer")
	}
	if d.Seen("p1", h2, 1000) {
		t.Fatal("different ciphertext hash must not hit")
	}
	// Expires at exact 10s boundary.
	if d.Seen("p1", h1, 11_000) {
		t.Fatal("expected hash expired at 10s")
	}
	if d.Count("p1") != 0 {
		t.Fatalf("expected peer pruned after expiry, count=%d", d.Count("p1"))
	}

	// Cap at 4,096 entries per peer: inserting 4,100 evicts the oldest 4.
	var firstHash, lastHash [32]byte
	for i := 0; i < DedupMaxPerPeer+4; i++ {
		var buf [8]byte
		buf[0] = byte(i)
		buf[1] = byte(i >> 8)
		h := sha256.Sum256(buf[:])
		if i == 0 {
			firstHash = h
		}
		lastHash = h
		d.RecordVerified("p1", h, 20_000)
	}
	if d.Count("p1") != DedupMaxPerPeer {
		t.Fatalf("expected capped count %d, got %d", DedupMaxPerPeer, d.Count("p1"))
	}
	if d.Seen("p1", firstHash, 20_001) {
		t.Fatal("expected oldest entry evicted at cap")
	}
	if !d.Seen("p1", lastHash, 20_001) {
		t.Fatal("expected newest entry retained")
	}
}
