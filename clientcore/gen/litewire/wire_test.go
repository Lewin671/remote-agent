package litewire

import (
	"math"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestLitewireMatchesProtowire(t *testing.T) {
	u64s := []uint64{0, 1, 127, 128, 16383, 16384, 1<<31 - 1, math.MaxUint64}
	for _, v := range u64s {
		got := AppendRawVarint(nil, v)
		want := protowire.AppendVarint(nil, v)
		if string(got) != string(want) {
			t.Fatalf("AppendRawVarint(%d) = %x, want %x", v, got, want)
		}
		if sz := SizeVarint(v); sz != len(want) {
			t.Fatalf("SizeVarint(%d) = %d, want %d", v, sz, len(want))
		}
		dv, n := ConsumeVarint(got)
		if n != len(got) || dv != v {
			t.Fatalf("ConsumeVarint(%x) = (%d, %d), want (%d, %d)", got, dv, n, v, len(got))
		}
	}

	i32s := []int32{0, 1, -1, -2, math.MinInt32, math.MaxInt32}
	for _, v := range i32s {
		got := AppendInt32Always(nil, 3, v)
		want := protowire.AppendTag(nil, 3, protowire.VarintType)
		want = protowire.AppendVarint(want, uint64(int64(v)))
		if string(got) != string(want) {
			t.Fatalf("AppendInt32Always(%d) = %x, want %x", v, got, want)
		}
		if sz := SizeInt32Always(3, v); sz != len(want) {
			t.Fatalf("SizeInt32Always(%d) = %d, want %d", v, sz, len(want))
		}
	}

	i64s := []int64{0, 1, -1, 12345, -12345, math.MinInt64, math.MaxInt64}
	for _, v := range i64s {
		if got, want := EncodeZigZag(v), protowire.EncodeZigZag(v); got != want {
			t.Fatalf("EncodeZigZag(%d) = %d, want %d", v, got, want)
		}
		if got, want := DecodeZigZag(EncodeZigZag(v)), protowire.DecodeZigZag(protowire.EncodeZigZag(v)); got != want {
			t.Fatalf("DecodeZigZag(%d) = %d, want %d", v, got, want)
		}
	}

	// Group skip round-trip.
	var grp []byte
	grp = protowire.AppendTag(grp, 7, protowire.StartGroupType)
	grp = protowire.AppendTag(grp, 1, protowire.VarintType)
	grp = protowire.AppendVarint(grp, 42)
	grp = protowire.AppendTag(grp, 2, protowire.BytesType)
	grp = protowire.AppendString(grp, "nested")
	grp = protowire.AppendTag(grp, 7, protowire.EndGroupType)
	grp = protowire.AppendTag(grp, 8, protowire.VarintType)
	grp = protowire.AppendVarint(grp, 99)

	num, typ, err := ReadTag(&grp)
	if err != nil || num != 7 || typ != StartGroupType {
		t.Fatalf("ReadTag = (%d, %d, %v)", num, typ, err)
	}
	if err := Skip(&grp, num, typ); err != nil {
		t.Fatalf("Skip group: %v", err)
	}
	num, typ, err = ReadTag(&grp)
	if err != nil || num != 8 || typ != VarintType {
		t.Fatalf("ReadTag after group = (%d, %d, %v)", num, typ, err)
	}
	val, err := ReadVarint(&grp)
	if err != nil || val != 99 || len(grp) != 0 {
		t.Fatalf("ReadVarint after group = (%d, %v, rem=%d)", val, err, len(grp))
	}

	// Overflow varint (10th byte > 1).
	overflow := []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x02}
	if _, n := ConsumeVarint(overflow); n != errCodeOverflow {
		t.Fatalf("ConsumeVarint(overflow) = %d, want %d", n, errCodeOverflow)
	}
}

type fakeMsg struct {
	text string
}

func (m *fakeMsg) AppendMarshal(b []byte) []byte {
	return AppendString(b, 1, m.text)
}

func (m *fakeMsg) Unmarshal(b []byte) error {
	for len(b) > 0 {
		num, wtyp, err := ReadTag(&b)
		if err != nil {
			return err
		}
		if num == 1 && wtyp == BytesType {
			v, err := ReadString(&b)
			if err != nil {
				return err
			}
			m.text = v
			continue
		}
		if err := Skip(&b, num, wtyp); err != nil {
			return err
		}
	}
	return nil
}

func TestAppendAndReadMessage(t *testing.T) {
	in := &fakeMsg{text: "hello"}
	wire := AppendMessage(nil, 4, in)
	num, wtyp, err := ReadTag(&wire)
	if err != nil || num != 4 || wtyp != BytesType {
		t.Fatalf("ReadTag = (%d, %d, %v)", num, wtyp, err)
	}
	var out fakeMsg
	if err := ReadMessage(&wire, &out); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if out.text != "hello" || len(wire) != 0 {
		t.Fatalf("got %q rem=%d", out.text, len(wire))
	}

	// The length placeholder widens to the minimal varint, whatever precedes the message.
	for _, n := range []int{0, 125, 126, 127, 16380, 16381, 16382, 2097150} {
		prefix := []byte{0xaa, 0xbb}
		body := &fakeMsg{text: string(make([]byte, n))}
		got := AppendMessage(prefix, 4, body)
		want := protowire.AppendBytes(protowire.AppendTag([]byte{0xaa, 0xbb}, 4, protowire.BytesType), AppendString(nil, 1, body.text))
		if string(got) != string(want) {
			t.Fatalf("AppendMessage with %d-byte text: got %d bytes, want %d", n, len(got), len(want))
		}
	}

	trunc := []byte{0x05, 'a', 'b'}
	if err := ReadMessage(&trunc, &out); err == nil {
		t.Fatal("expected error for truncated message")
	}
}
