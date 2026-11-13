package deviceauth

import (
	"math"
	"testing"
)

// Keys are fixed-size and preserve full-width sequence identity.
func TestAcceptedKeysPreserveSequence(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range []uint64{0, 1, 9, 10, 99, 100, 1<<32 - 1, 1 << 32, 1<<53 + 1, 1 << 63, math.MaxUint64} {
		h := Header{Sender: "sender", Purpose: Command, Machine: "machine", Session: "session", Request: "request", Seq: n}
		k := acceptedKey(h)
		if len(k) != 32 || seen[k] {
			t.Fatalf("invalid/repeated key for %d", n)
		}
		seen[k] = true
		h.Epoch++
		h.ContentEpoch++
		if acceptedKey(h) != k {
			t.Fatal("epoch changed logical identity")
		}
	}
}
