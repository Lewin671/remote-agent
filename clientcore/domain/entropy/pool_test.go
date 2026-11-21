package entropy

import (
	"bytes"
	"testing"
)

func TestPoolReadAllOrNothingAndCaps(t *testing.T) {
	var p Pool
	p.Append(bytes.Repeat([]byte{0xab}, 10))
	if p.Avail() != 10 {
		t.Fatalf("got avail %d, want 10", p.Avail())
	}
	dst := make([]byte, 12)
	if _, err := p.Read(dst); err != ErrNeedEntropy {
		t.Fatalf("got err %v, want ErrNeedEntropy", err)
	}
	if p.Avail() != 10 {
		t.Fatalf("partial consumption: avail=%d want 10", p.Avail())
	}
	if n, err := p.Read(dst[:4]); err != nil || n != 4 {
		t.Fatalf("read 4 failed: n=%d err=%v", n, err)
	}
	if p.Avail() != 6 {
		t.Fatalf("got avail %d, want 6", p.Avail())
	}

	p.Append(make([]byte, MaxBytes*2))
	if p.Avail() != MaxBytes {
		t.Fatalf("got capped avail %d, want %d", p.Avail(), MaxBytes)
	}
}

func TestPoolTakeIDAndUnitFloat(t *testing.T) {
	var p Pool
	var raw [24]byte
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	p.Append(raw[:])

	id, ok := p.TakeID()
	if !ok || len(id) != 32 {
		t.Fatalf("TakeID got (%q, %v)", id, ok)
	}
	f, ok := p.TakeUnitFloat()
	if !ok || f < 0 || f >= 1 {
		t.Fatalf("TakeUnitFloat got (%v, %v)", f, ok)
	}
	if p.Avail() != 0 {
		t.Fatalf("expected empty pool, got %d", p.Avail())
	}
}
