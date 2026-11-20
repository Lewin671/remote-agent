package peerwire

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// testProtocol is the daemon's current protocol version. The frozen vectors
// are sealed with it, so a protocol bump must regenerate them; shared code
// cannot import internal/protocol, so the test reads its source.
var testProtocol = func() uint64 {
	src, e := os.ReadFile("../../internal/protocol/protocol.go")
	if e != nil {
		panic(e)
	}
	_, rest, ok := strings.Cut(string(src), "\nconst Version = ")
	line, _, _ := strings.Cut(rest, "\n")
	v, e := strconv.ParseUint(strings.TrimSpace(line), 10, 64)
	if !ok || e != nil || v == 0 {
		panic("protocol version not found")
	}
	return v
}()

// identity holds a test device's private keys; production keys stay with the
// caller (the daemon's direct.Identity or a client shell).
type identity struct {
	Public
	sign ed25519.PrivateKey
	box  *ecdh.PrivateKey
}

func fixture(id string, role uint64) identity {
	seed := Hash([]byte("lab/sign/" + id))
	box, e := ecdh.X25519().NewPrivateKey(Hash([]byte("lab/box/" + id)))
	if e != nil {
		panic(e)
	}
	sign := ed25519.NewKeyFromSeed(seed)
	deviceID := fmt.Sprintf("%x", Hash(sign.Public().(ed25519.PublicKey))[:16])
	pub := Public{[]byte(deviceID), Hash([]byte("lab/instance/" + id))[:16], Hash([]byte("lab/boot/" + id)), role, sign.Public().(ed25519.PublicKey), box.PublicKey().Bytes()}
	return identity{pub, sign, box}
}

func (i identity) shared(peer Public) []byte {
	key, e := ecdh.X25519().NewPublicKey(peer.Box)
	if e != nil {
		panic(e)
	}
	s, e := i.box.ECDH(key)
	if e != nil {
		panic(e)
	}
	return s
}

func fixtureCodec() *Codec {
	return &Codec{[]byte("0123456789abcdef0123456789abcdef"), Hash([]byte("lab/account-domain")), Hash([]byte("lab/member-head/1")), 1, testProtocol}
}

func newTestSignal(codec *Codec, self identity, peer Public, connection []byte, use, now uint64) *Signal {
	return NewSignal(codec, self.Public, peer, self.shared(peer), connection, use, now)
}

// seal runs both signing phases. A nil entropy derives a fixed one from the
// context so tests stay deterministic.
func (w *Codec) seal(from identity, to Public, c Context, typ uint64, payload, entropy []byte) ([]byte, error) {
	if entropy == nil {
		entropy = append(Hash(w.encode(c)), Hash(payload)[:8]...)
	}
	u, e := w.Prepare(from.Public, to, from.shared(to), c, typ, payload, entropy)
	if e != nil {
		return nil, e
	}
	return u.Finish(ed25519.Sign(from.sign, u.Message))
}

func (w *Codec) open(raw []byte, from Public, to identity, now uint64) (Opened, error) {
	return w.Open(raw, from, to.Public, to.shared(from), now)
}

func TestTwoPhaseSealRejectsBadInputs(t *testing.T) {
	a, b := fixture("client", 1), fixture("machine", 2)
	codec := fixtureCodec()
	now := uint64(1700000000000)
	c := NewContext(a.Public, b.Public, Hash([]byte("connection"))[:16], 1, 1, 1, now, 1, false)
	if _, e := codec.Prepare(a.Public, b.Public, a.shared(b.Public), c, 3, nil, make([]byte, SealEntropy-1)); e == nil {
		t.Fatal("short entropy accepted")
	}
	if _, e := codec.Prepare(a.Public, b.Public, nil, c, 3, nil, make([]byte, SealEntropy)); e == nil {
		t.Fatal("missing shared secret accepted")
	}
	u, e := codec.Prepare(a.Public, b.Public, a.shared(b.Public), c, 3, nil, make([]byte, SealEntropy))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = u.Finish(make([]byte, 63)); e == nil {
		t.Fatal("short signature accepted")
	}
	forged, e := u.Finish(make([]byte, 64))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = codec.open(forged, a.Public, b, now); e == nil {
		t.Fatal("frame with a wrong signature authenticated")
	}
	good, e := u.Finish(ed25519.Sign(a.sign, u.Message))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = codec.open(good, a.Public, b, now); e != nil {
		t.Fatal(e)
	}
	if _, e = codec.Open(good, a.Public, b.Public, fixture("other", 2).shared(a.Public), now); e == nil {
		t.Fatal("frame opened with another member's shared secret")
	}
}

func TestWireHelpersMatchStandardLibrary(t *testing.T) {
	values := []uint64{0, 1, 127, 128, 255, 300, 1<<14 - 1, 1 << 14, 1<<32 - 1, 1 << 35, 1<<63 - 1, 1 << 63, ^uint64(0)}
	for _, v := range values {
		want := binary.AppendUvarint(nil, v)
		if got := appendUvarint(nil, v); !bytes.Equal(got, want) {
			t.Fatalf("appendUvarint(%d) = %x, want %x", v, got, want)
		}
		if !bytes.Equal(Num(v), binary.BigEndian.AppendUint64(nil, v)) {
			t.Fatalf("Num(%d)", v)
		}
	}
	inputs := [][]byte{nil, {0x80}, {0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}, {0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x02}, {0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01}, {0x80, 0x00}, {0x81, 0x01, 0x05}}
	for _, v := range values {
		inputs = append(inputs, binary.AppendUvarint(nil, v))
	}
	for _, in := range inputs {
		v, n := uvarint(in)
		wv, wn := binary.Uvarint(in)
		if v != wv || n != wn {
			t.Fatalf("uvarint(%x) = %d,%d want %d,%d", in, v, n, wv, wn)
		}
	}
	c := Context{From: []byte{0xab, 0x01}, SenderInstance: []byte{0}, Connection: []byte{0xff}, Use: 1, Generation: 22, Class: 2, Seq: ^uint64(0)}
	if got, want := Key(c), fmt.Sprintf("%x/%x/%x/%d/%d/%d/%d", c.From, c.SenderInstance, c.Connection, c.Use, c.Generation, c.Class, c.Seq); got != want {
		t.Fatalf("Key = %q, want %q", got, want)
	}
}
