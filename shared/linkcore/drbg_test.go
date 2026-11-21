package linkcore

import (
	"bytes"
	"io"
	"testing"
)

func TestDRBGDeterministicAndRatchets(t *testing.T) {
	seed := []byte("0123456789abcdef0123456789abcdef")
	a := NewDRBG(seed)
	b := NewDRBG(seed)

	bufA := make([]byte, 73)
	bufB := make([]byte, 73)
	if _, err := io.ReadFull(a, bufA); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(b, bufB); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bufA, bufB) {
		t.Fatal("same seed produced different output")
	}

	next := make([]byte, 40)
	if _, err := io.ReadFull(a, next); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(bufA[:40], next) {
		t.Fatal("successive reads collided")
	}

	a.Reseed([]byte("extra"))
	if _, err := io.ReadFull(b, make([]byte, 40)); err != nil {
		t.Fatal(err)
	}
	outA, outB := make([]byte, 32), make([]byte, 32)
	_, _ = a.Read(outA)
	_, _ = b.Read(outB)
	if bytes.Equal(outA, outB) {
		t.Fatal("Reseed did not diverge state")
	}

	for i := 0; i < 100; i++ {
		f := a.UnitFloat()
		if f < 0 || f >= 1 {
			t.Fatalf("UnitFloat out of [0,1): %v", f)
		}
	}
}
