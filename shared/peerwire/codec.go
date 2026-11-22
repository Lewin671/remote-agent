// Package peerwire implements the authenticated protocol 16 peer transport.
// The owner supplies only current, verified membership and checks its lease
// before both receipt and eventual dispatch. No account content key is used.
//
// The package is key-free and sans-IO: it never holds a private key, reads a
// clock or draws randomness. The caller computes the X25519 shared secret with
// the peer, supplies frame entropy, and signs the bytes Prepare returns.
package peerwire

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strconv"
)

var ErrPacket = errors.New("invalid peer packet")

// Codec binds an immutable verified membership snapshot. A new head requires
// a new Codec; do not mutate an existing generation's cryptographic context.
type Codec struct {
	account, accountDomain, head []byte
	memberEpoch, protocol        uint64
}

// NewCodec binds the protocol version the caller speaks; it is signed into
// every context and AAD, so both ends must pass the same value.
func NewCodec(account string, domain, head [32]byte, epoch, protocol uint64) (*Codec, error) {
	if len(account) != 32 || domain == [32]byte{} || head == [32]byte{} || epoch == 0 || protocol == 0 {
		return nil, ErrPacket
	}
	return &Codec{[]byte(account), append([]byte(nil), domain[:]...), append([]byte(nil), head[:]...), epoch, protocol}, nil
}

type Field struct {
	N     uint64
	B     []byte
	U     uint64
	Bytes bool
}

func B(n uint64, b []byte) Field { return Field{N: n, B: b, Bytes: true} }
func U(n, u uint64) Field        { return Field{N: n, U: u} }
func varint(v uint64) []byte     { return appendUvarint(nil, v) }
//go:noinline
func PB(fields ...Field) []byte {
	for i := 1; i < len(fields); i++ {
		f := fields[i]
		j := i - 1
		for j >= 0 && fields[j].N > f.N {
			fields[j+1] = fields[j]
			j--
		}
		fields[j+1] = f
	}
	var out []byte
	for _, f := range fields {
		wire := uint64(0)
		if f.Bytes {
			wire = 2
		}
		out = append(out, varint(f.N<<3|wire)...)
		if f.Bytes {
			out = append(out, varint(uint64(len(f.B)))...)
			out = append(out, f.B...)
		} else {
			out = append(out, varint(f.U)...)
		}
	}
	return out
}

// ServerFrameBody strips the single outer oneof while retaining the signed
// delivery bytes. The four-byte wrapper is not part of the 64 KiB frame limit.
func ServerFrameBody(raw []byte, field byte) ([]byte, error) {
	if len(raw) < 2 || len(raw) > 65540 || raw[0] != field<<3|2 {
		return nil, ErrPacket
	}
	n, k := uvarint(raw[1:])
	if k <= 0 || !bytes.Equal(raw[1:1+k], varint(n)) || n > 65536 || n != uint64(len(raw)-1-k) {
		return nil, ErrPacket
	}
	return raw[1+k:], nil
}

// Parse rejects duplicate fields, reordered fields, non-minimal integers,
// unknown wire types and lengths before slicing/allocating payloads.
func Parse(raw []byte, maxFields int) (map[uint64]Field, error) {
	if len(raw) > 65536 {
		return nil, ErrPacket
	}
	fields := map[uint64]Field{}
	last := uint64(0)
	read := func() (uint64, error) {
		v, n := uvarint(raw)
		if n <= 0 || !bytes.Equal(raw[:n], varint(v)) {
			return 0, ErrPacket
		}
		raw = raw[n:]
		return v, nil
	}
	for len(raw) > 0 {
		tag, e := read()
		if e != nil {
			return nil, e
		}
		n, wire := tag>>3, tag&7
		if n <= last || n > 4095 || len(fields) >= maxFields {
			return nil, ErrPacket
		}
		last = n
		f := Field{N: n}
		switch wire {
		case 0:
			f.U, e = read()
		case 2:
			var size uint64
			size, e = read()
			if e == nil {
				if size > uint64(len(raw)) {
					return nil, ErrPacket
				}
				f.Bytes = true
				f.B = raw[:size]
				raw = raw[size:]
			}
		default:
			return nil, ErrPacket
		}
		if e != nil {
			return nil, e
		}
		fields[n] = f
	}
	return fields, nil
}
func need(fields map[uint64]Field, schema string) error {
	if len(fields) != len(schema) {
		return ErrPacket
	}
	for i, w := range schema {
		f, ok := fields[uint64(i+1)]
		if !ok || f.Bytes != (w == 'b') {
			return ErrPacket
		}
	}
	return nil
}
func Hash(b []byte) []byte { v := sha256.Sum256(b); return v[:] }
func LP(values ...[]byte) []byte {
	var out []byte
	for _, v := range values {
		out = appendBig(out, uint64(uint32(len(v))), 4)
		out = append(out, v...)
	}
	return out
}
func Num(n uint64) []byte { return appendBig(nil, n, 8) }
func KDF(secret, info []byte) []byte {
	salt := Hash([]byte("remote-agent/peer-hkdf/v1"))
	m := hmac.New(sha256.New, salt)
	m.Write(secret)
	prk := m.Sum(nil)
	m = hmac.New(sha256.New, prk)
	m.Write(info)
	m.Write([]byte{1})
	return m.Sum(nil)
}
func GCM(key, nonce, aad, body []byte, seal bool) ([]byte, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(b)
	if e != nil || len(nonce) != 12 {
		return nil, ErrPacket
	}
	if seal {
		return a.Seal(nil, nonce, body, aad), nil
	}
	return a.Open(nil, nonce, body, aad)
}

// Public names one device process: member ID and keys, plus the process
// instance and boot nonce once known. Box is the X25519 public key; the
// caller derives the shared secret from it with its own private key.
type Public struct {
	ID, Instance, Boot []byte
	Role               uint64
	Sign               ed25519.PublicKey
	Box                []byte
}

type Context struct {
	Domain                                                                 []byte
	From, To                                                               []byte
	FromRole, ToRole                                                       uint64
	Machine                                                                []byte
	SenderInstance, ReceiverInstance, SenderBoot, ReceiverBoot, Connection []byte
	Use, Generation, Seq, Issued, Deadline, Class, RecipientUnknown        uint64
}

func NewContext(from, to Public, connection []byte, use, generation, seq, now, class uint64, boot bool) Context {
	domain := []byte("signal")
	if class == 2 {
		domain = []byte("control")
	}
	if boot {
		domain = []byte("bootstrap")
	}
	machine := to.ID
	if from.Role == 2 {
		machine = from.ID
	}
	return Context{domain, from.ID, to.ID, from.Role, to.Role, machine, from.Instance, to.Instance, from.Boot, to.Boot, connection, use, generation, seq, now, now + 10000, class, 0}
}
func (w *Codec) encode(c Context) []byte {
	return PB(B(1, c.Domain), B(2, w.account), B(3, c.From), B(4, c.To), U(5, c.FromRole), U(6, c.ToRole), B(7, c.Machine), U(8, w.protocol), U(9, w.memberEpoch), B(10, w.head), B(11, c.SenderInstance), B(12, c.ReceiverInstance), B(13, c.SenderBoot), B(14, c.ReceiverBoot), B(15, c.Connection), U(16, c.Use), U(17, c.Generation), U(18, c.Seq), U(19, c.Issued), U(20, c.Deadline), U(21, c.Class), B(22, c.From), B(23, w.accountDomain), U(24, c.RecipientUnknown))
}
func (w *Codec) parseContext(raw []byte) (Context, error) {
	f, e := ParseMessage(raw, "Context")
	if e != nil || need(f, "bbbbuubuubbbbbbuuuuuubbu") != nil {
		return Context{}, ErrPacket
	}
	if !bytes.Equal(f[23].B, w.accountDomain) || !bytes.Equal(f[2].B, w.account) || f[8].U != w.protocol || f[9].U != w.memberEpoch || !bytes.Equal(f[10].B, w.head) || !bytes.Equal(f[22].B, f[3].B) {
		return Context{}, ErrPacket
	}
	return Context{f[1].B, f[3].B, f[4].B, f[5].U, f[6].U, f[7].B, f[11].B, f[12].B, f[13].B, f[14].B, f[15].B, f[16].U, f[17].U, f[18].U, f[19].U, f[20].U, f[21].U, f[24].U}, nil
}
func route(from, to []byte, class uint64, frameID []byte) []byte {
	return PB(B(1, from), B(2, to), U(3, class), B(4, frameID))
}
func (w *Codec) wrapInfo(from, to []byte) []byte {
	ids := [][]byte{from, to}
	if bytes.Compare(ids[0], ids[1]) > 0 {
		ids[0], ids[1] = ids[1], ids[0]
	}
	return LP([]byte("remote-agent/peer-context/v1"), w.account, w.accountDomain, ids[0], ids[1], from, to)
}
func (w *Codec) bodyInfo(c Context) []byte {
	if string(c.Domain) == "bootstrap" {
		return LP([]byte("remote-agent/peer-bootstrap/v1"), w.account, w.accountDomain, c.From, c.To)
	}
	return LP([]byte("remote-agent/peer-body/v1"), w.account, w.accountDomain, c.From, c.To, c.SenderInstance, c.ReceiverInstance, c.SenderBoot, c.ReceiverBoot, c.Connection, Num(c.Use), Num(c.Generation), Num(c.Class))
}
func (w *Codec) aad(from, to []byte, class uint64, id []byte) []byte {
	return LP([]byte("remote-agent/peer-aad/v1"), w.account, w.accountDomain, Num(w.protocol), route(from, to, class, id))
}
func sigInput(a, nc, ec, nb, eb []byte) []byte {
	return LP([]byte("remote-agent/peer-signature/v1"), a, nc, ec, nb, eb)
}

// SealEntropy is the random input a frame needs: a 16-byte frame ID, then
// the 12-byte context nonce and the 12-byte body nonce.
const SealEntropy = 40

// Unsigned is an encrypted frame waiting for its sender's Ed25519 signature
// over Message. Finish appends the signature and builds the delivery bytes.
type Unsigned struct {
	Message                []byte
	from, to, frameID      []byte
	nonceC, ec, nonceB, eb []byte
	class                  uint64
}

// Prepare encrypts a frame from -> to. shared is the X25519 secret between
// the two members; entropy supplies the frame ID and nonces (SealEntropy).
func (w *Codec) Prepare(from, to Public, shared []byte, c Context, typ uint64, payload, entropy []byte) (Unsigned, error) {
	if len(shared) != 32 || len(entropy) != SealEntropy || len(from.Sign) != ed25519.PublicKeySize || len(to.Sign) != ed25519.PublicKeySize {
		return Unsigned{}, ErrPacket
	}
	entropy = append([]byte(nil), entropy...)
	frameID, nonceC, nonceB := entropy[:16], entropy[16:28], entropy[28:40]
	ad := w.aad(from.ID, to.ID, c.Class, frameID)
	ec, e := GCM(KDF(shared, w.wrapInfo(from.ID, to.ID)), nonceC, ad, w.encode(c), true)
	if e != nil {
		return Unsigned{}, e
	}
	typed := PB(B(1, PB(U(1, typ), B(2, payload))))
	eb, e := GCM(KDF(shared, w.bodyInfo(c)), nonceB, LP(ad, Hash(ec)), typed, true)
	if e != nil {
		return Unsigned{}, e
	}
	return Unsigned{sigInput(ad, nonceC, ec, nonceB, eb), from.ID, to.ID, frameID, nonceC, ec, nonceB, eb, c.Class}, nil
}

// Finish attaches the signature over Message and enforces the frame limit.
func (u Unsigned) Finish(sig []byte) ([]byte, error) {
	if len(sig) != ed25519.SignatureSize || u.Message == nil {
		return nil, ErrPacket
	}
	sealed := PB(B(1, u.nonceC), B(2, u.ec), B(3, u.nonceB), B(4, u.eb), B(5, sig))
	delivery := PB(B(1, u.from), B(2, u.to), U(3, u.class), B(4, u.frameID), B(5, sealed))
	limit := 65536
	if u.class == 2 {
		limit = 16384
	}
	if len(delivery) > limit {
		return nil, ErrPacket
	}
	return delivery, nil
}

type Opened struct {
	Context                     Context
	Type                        uint64
	Payload, FrameID, Hash, Raw []byte
}

// Open authenticates a frame from -> to (the local process). shared is the
// X25519 secret between the two members.
func (w *Codec) Open(raw []byte, from, to Public, shared []byte, now uint64) (Opened, error) {
	return w.openShared(raw, from, to, shared, now)
}

// OpenSent re-authenticates a frame this process sealed for to.
func (w *Codec) OpenSent(raw []byte, from, to Public, shared []byte, now uint64) (Opened, error) {
	o, e := w.openShared(raw, from, to, shared, now)
	if e != nil {
		return o, e
	}
	if !bytes.Equal(o.Context.SenderInstance, from.Instance) || !bytes.Equal(o.Context.SenderBoot, from.Boot) {
		return Opened{}, ErrPacket
	}
	return o, nil
}
func (w *Codec) openShared(raw []byte, from, to Public, shared []byte, now uint64) (Opened, error) {
	fail := func() (Opened, error) { return Opened{}, ErrPacket }
	if len(shared) != 32 || len(from.Sign) != ed25519.PublicKeySize || len(to.Sign) != ed25519.PublicKeySize || len(raw) > 65536 {
		return fail()
	}
	raw = append([]byte(nil), raw...)
	f, e := ParseMessage(raw, "PeerFrameDelivery")
	if e != nil || need(f, "bbubb") != nil {
		return fail()
	}
	class := f[3].U
	if class != 1 && class != 2 || len(raw) > 65536 || class == 2 && len(raw) > 16384 || len(f[4].B) != 16 || !bytes.Equal(f[1].B, from.ID) || !bytes.Equal(f[2].B, to.ID) {
		return fail()
	}
	packet, e := ParseMessage(f[5].B, "SealedPeerPacket")
	if e != nil || need(packet, "bbbbb") != nil {
		return fail()
	}
	nc, ec, nb, eb, sig := packet[1].B, packet[2].B, packet[3].B, packet[4].B, packet[5].B
	ad := w.aad(from.ID, to.ID, class, f[4].B)
	if len(nc) != 12 || len(nb) != 12 || len(sig) != 64 || !ed25519.Verify(from.Sign, sigInput(ad, nc, ec, nb, eb), sig) {
		return fail()
	}
	rawCtx, e := GCM(KDF(shared, w.wrapInfo(from.ID, to.ID)), nc, ad, ec, false)
	if e != nil {
		return fail()
	}
	c, e := w.parseContext(rawCtx)
	if e != nil {
		return fail()
	}
	if !bytes.Equal(c.From, from.ID) || !bytes.Equal(c.To, to.ID) || c.Class != class || c.FromRole != from.Role || c.ToRole != to.Role || from.Role == to.Role || len(c.Connection) != 16 || c.Seq == 0 || c.Issued >= c.Deadline || c.Deadline-c.Issued > 30000 || now >= c.Deadline || c.Issued > now+300000 {
		return fail()
	}
	machine := to.ID
	if from.Role == 2 {
		machine = from.ID
	}
	if !bytes.Equal(c.Machine, machine) {
		return fail()
	}
	boot := string(c.Domain) == "bootstrap"
	if len(c.SenderInstance) != 16 || len(c.SenderBoot) != 32 || c.FromRole < 1 || c.FromRole > 2 || c.ToRole < 1 || c.ToRole > 2 {
		return fail()
	}
	if c.RecipientUnknown > 1 || (c.RecipientUnknown == 1 && (len(c.ReceiverInstance) != 0 || len(c.ReceiverBoot) != 0)) || (c.RecipientUnknown == 0 && (len(c.ReceiverInstance) != 16 || len(c.ReceiverBoot) != 32 || !bytes.Equal(c.ReceiverInstance, to.Instance) || !bytes.Equal(c.ReceiverBoot, to.Boot))) {
		return fail()
	}
	expectedDomain := "signal"
	if class == 2 {
		expectedDomain = "control"
	}
	if !boot && (string(c.Domain) != expectedDomain) {
		return fail()
	}
	if !boot && (!bytes.Equal(c.SenderInstance, from.Instance) || !bytes.Equal(c.ReceiverInstance, to.Instance) || !bytes.Equal(c.SenderBoot, from.Boot) || !bytes.Equal(c.ReceiverBoot, to.Boot) || c.Generation == 0 || c.Use > 2) {
		return fail()
	}
	body, e := GCM(KDF(shared, w.bodyInfo(c)), nb, LP(ad, Hash(ec)), eb, false)
	if e != nil {
		return fail()
	}
	wrapper, e := ParseMessage(body, "PeerSignal")
	if e != nil || need(wrapper, "b") != nil {
		return fail()
	}
	t, e := ParseMessage(wrapper[1].B, "TypedPayload")
	if e != nil || need(t, "ub") != nil || t[1].U > 0xffffffff {
		return fail()
	}
	if boot && (class != 1 || c.Use != 0 || c.Generation != 0 || (t[1].U != 1 && t[1].U != 2)) {
		return fail()
	}
	if !boot && class == 1 && c.Use == 0 && t[1].U != 8 && t[1].U != 9 {
		return fail()
	}
	if c.RecipientUnknown == 1 && (!boot || class != 1 || t[1].U != 1) {
		return fail()
	}
	return Opened{c, t[1].U, t[2].B, f[4].B, Hash(raw), append([]byte(nil), raw...)}, nil
}

// Key identifies a frame's sequence slot: hex sender, instance and connection,
// then decimal use, generation, class and sequence, separated by '/'.
func Key(c Context) string {
	out := appendHex(nil, c.From)
	out = appendHex(append(out, '/'), c.SenderInstance)
	out = appendHex(append(out, '/'), c.Connection)
	for _, n := range []uint64{c.Use, c.Generation, c.Class, c.Seq} {
		out = strconv.AppendUint(append(out, '/'), n, 10)
	}
	return string(out)
}

func (p Public) Member() Public { p.Instance = nil; p.Boot = nil; return p }
func NewProbeContext(from, to Public, conn []byte, seq, now uint64) Context {
	c := NewContext(from, to.Member(), conn, 0, 0, seq, now, 1, true)
	c.RecipientUnknown = 1
	return c
}
