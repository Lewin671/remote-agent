package deviceauth

import (
	"math"
	"testing"
)

// Literal decimal bytes pin the existing accepted-log key representation.
// In particular, max uint64 must never go through a signed int or float.
func TestAcceptedKeysKeepDecimalBytes(t *testing.T) {
	for _, tc := range []struct {
		n       uint64
		decimal string
	}{
		{0, "0"}, {1, "1"}, {9, "9"}, {10, "10"}, {99, "99"}, {100, "100"},
		{1<<32 - 1, "4294967295"}, {1 << 32, "4294967296"},
		{1<<53 + 1, "9007199254740993"}, {1 << 63, "9223372036854775808"},
		{math.MaxUint64, "18446744073709551615"},
	} {
		h := Header{Sender: "sender", Purpose: Command, Machine: "machine", Session: "session", Request: "request", Seq: tc.n}
		// Command's wire value is 5; this prefix is part of the stored key.
		want := "sender/5/machine/session/request/" + tc.decimal
		if got := acceptedKey(h); got != want {
			t.Fatalf("%d: %q != %q", tc.n, got, want)
		}
	}
}
