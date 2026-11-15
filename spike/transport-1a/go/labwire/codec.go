// Package labwire implements the experimental, non-product wire draft.
package labwire

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

var ErrPacket = errors.New("invalid experimental peer packet")
var Account = []byte("0123456789abcdef0123456789abcdef")
var AccountDomain = Hash([]byte("lab/account-domain"))
var Head = Hash([]byte("lab/member-head/1"))
var MemberEpoch uint64 = 1

const Protocol uint64 = 16 // Draft only. Does not change the product version.

type Field struct {
	N     uint64
	B     []byte
	U     uint64
	Bytes bool
}

func B(n uint64, b []byte) Field { return Field{N: n, B: b, Bytes: true} }
func U(n, u uint64) Field        { return Field{N: n, U: u} }
func varint(v uint64) []byte     { var b [10]byte; n := binary.PutUvarint(b[:], v); return b[:n] }
func PB(fields ...Field) []byte {
	sort.Slice(fields, func(i, j int) bool { return fields[i].N < fields[j].N })
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

// Parse rejects duplicate fields, reordered fields, non-minimal integers,
// unknown wire types and lengths before slicing/allocating payloads.
func Parse(raw []byte, maxFields int) (map[uint64]Field, error) {
	if len(raw) > 65536 {
		return nil, ErrPacket
	}
	fields := map[uint64]Field{}
	last := uint64(0)
	read := func() (uint64, error) {
		v, n := binary.Uvarint(raw)
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
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(v)))
		out = append(out, size[:]...)
		out = append(out, v...)
	}
	return out
}
func Num(n uint64) []byte { var b [8]byte; binary.BigEndian.PutUint64(b[:], n); return b[:] }
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

type Identity struct {
	ID, Instance, Boot []byte
	Role               uint64
	Sign               ed25519.PrivateKey
	Box                *ecdh.PrivateKey
}
type Public struct {
	ID, Instance, Boot []byte
	Role               uint64
	Sign               ed25519.PublicKey
	Box                *ecdh.PublicKey
}

func Fixture(id string, role uint64) Identity {
	seed := Hash([]byte("lab/sign/" + id))
	box, e := ecdh.X25519().NewPrivateKey(Hash([]byte("lab/box/" + id)))
	if e != nil {
		panic(e)
	}
	sign := ed25519.NewKeyFromSeed(seed)
	deviceID := fmt.Sprintf("%x", Hash(sign.Public().(ed25519.PublicKey))[:16])
	return Identity{[]byte(deviceID), Hash([]byte("lab/instance/" + id))[:16], Hash([]byte("lab/boot/" + id)), role, sign, box}
}
func (i Identity) Public() Public {
	return Public{i.ID, i.Instance, i.Boot, i.Role, i.Sign.Public().(ed25519.PublicKey), i.Box.PublicKey()}
}

type Context struct {
	Domain                                                                 []byte
	From, To                                                               []byte
	FromRole, ToRole                                                       uint64
	Machine                                                                []byte
	SenderInstance, ReceiverInstance, SenderBoot, ReceiverBoot, Connection []byte
	Use, Generation, Seq, Issued, Deadline, Class                          uint64
}

func NewContext(from Identity, to Public, connection []byte, use, generation, seq, now, class uint64, boot bool) Context {
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
	return Context{domain, from.ID, to.ID, from.Role, to.Role, machine, from.Instance, to.Instance, from.Boot, to.Boot, connection, use, generation, seq, now, now + 10000, class}
}
func (c Context) Encode() []byte {
	return PB(B(1, c.Domain), B(2, Account), B(3, c.From), B(4, c.To), U(5, c.FromRole), U(6, c.ToRole), B(7, c.Machine), U(8, Protocol), U(9, MemberEpoch), B(10, Head), B(11, c.SenderInstance), B(12, c.ReceiverInstance), B(13, c.SenderBoot), B(14, c.ReceiverBoot), B(15, c.Connection), U(16, c.Use), U(17, c.Generation), U(18, c.Seq), U(19, c.Issued), U(20, c.Deadline), U(21, c.Class), B(22, c.From), B(23, AccountDomain))
}
func parseContext(raw []byte) (Context, error) {
	f, e := Parse(raw, 23)
	if e != nil || need(f, "bbbbuubuubbbbbbuuuuuubb") != nil {
		return Context{}, ErrPacket
	}
	if !bytes.Equal(f[23].B, AccountDomain) || !bytes.Equal(f[2].B, Account) || f[8].U != Protocol || f[9].U != MemberEpoch || !bytes.Equal(f[10].B, Head) || !bytes.Equal(f[22].B, f[3].B) {
		return Context{}, ErrPacket
	}
	return Context{f[1].B, f[3].B, f[4].B, f[5].U, f[6].U, f[7].B, f[11].B, f[12].B, f[13].B, f[14].B, f[15].B, f[16].U, f[17].U, f[18].U, f[19].U, f[20].U, f[21].U}, nil
}
func route(from, to []byte, class uint64, frameID []byte) []byte {
	return PB(B(1, from), B(2, to), U(3, class), B(4, frameID))
}
func wrapInfo(from, to []byte) []byte {
	ids := [][]byte{from, to}
	if bytes.Compare(ids[0], ids[1]) > 0 {
		ids[0], ids[1] = ids[1], ids[0]
	}
	return LP([]byte("remote-agent/peer-context/v1"), Account, AccountDomain, ids[0], ids[1], from, to)
}
func bodyInfo(c Context) []byte {
	if string(c.Domain) == "bootstrap" {
		return LP([]byte("remote-agent/peer-bootstrap/v1"), Account, AccountDomain, c.From, c.To)
	}
	return LP([]byte("remote-agent/peer-body/v1"), Account, AccountDomain, c.From, c.To, c.SenderInstance, c.ReceiverInstance, c.SenderBoot, c.ReceiverBoot, c.Connection, Num(c.Use), Num(c.Generation), Num(c.Class))
}
func aad(from, to []byte, class uint64, id []byte) []byte {
	return LP([]byte("remote-agent/peer-aad/v1"), Account, AccountDomain, Num(Protocol), route(from, to, class, id))
}
func sigInput(a, nc, ec, nb, eb []byte) []byte {
	return LP([]byte("remote-agent/peer-signature/v1"), a, nc, ec, nb, eb)
}
func Seal(from Identity, to Public, c Context, typ uint64, payload, frameID, nonceC, nonceB []byte) ([]byte, error) {
	if frameID == nil {
		frameID = make([]byte, 16)
		rand.Read(frameID)
	}
	if nonceC == nil {
		nonceC = make([]byte, 12)
		rand.Read(nonceC)
	}
	if nonceB == nil {
		nonceB = make([]byte, 12)
		rand.Read(nonceB)
	}
	shared, e := from.Box.ECDH(to.Box)
	if e != nil {
		return nil, e
	}
	ad := aad(from.ID, to.ID, c.Class, frameID)
	ec, e := GCM(KDF(shared, wrapInfo(from.ID, to.ID)), nonceC, ad, c.Encode(), true)
	if e != nil {
		return nil, e
	}
	typed := PB(B(1, PB(U(1, typ), B(2, payload))))
	eb, e := GCM(KDF(shared, bodyInfo(c)), nonceB, LP(ad, Hash(ec)), typed, true)
	if e != nil {
		return nil, e
	}
	sig := ed25519.Sign(from.Sign, sigInput(ad, nonceC, ec, nonceB, eb))
	sealed := PB(B(1, nonceC), B(2, ec), B(3, nonceB), B(4, eb), B(5, sig))
	delivery := PB(B(1, from.ID), B(2, to.ID), U(3, c.Class), B(4, frameID), B(5, sealed))
	limit := 65536
	if c.Class == 2 {
		limit = 16384
	}
	if len(delivery) > limit {
		return nil, ErrPacket
	}
	return delivery, nil
}

type Opened struct {
	Context                Context
	Type                   uint64
	Payload, FrameID, Hash []byte
}

func Open(raw []byte, from Public, to Identity, now uint64) (Opened, error) {
	fail := func() (Opened, error) { return Opened{}, ErrPacket }
	f, e := Parse(raw, 5)
	if e != nil || need(f, "bbubb") != nil {
		return fail()
	}
	class := f[3].U
	if class != 1 && class != 2 || len(raw) > 65536 || class == 2 && len(raw) > 16384 || len(f[4].B) != 16 || !bytes.Equal(f[1].B, from.ID) || !bytes.Equal(f[2].B, to.ID) {
		return fail()
	}
	packet, e := Parse(f[5].B, 5)
	if e != nil || need(packet, "bbbbb") != nil {
		return fail()
	}
	nc, ec, nb, eb, sig := packet[1].B, packet[2].B, packet[3].B, packet[4].B, packet[5].B
	ad := aad(from.ID, to.ID, class, f[4].B)
	if len(nc) != 12 || len(nb) != 12 || len(sig) != 64 || !ed25519.Verify(from.Sign, sigInput(ad, nc, ec, nb, eb), sig) {
		return fail()
	}
	shared, e := to.Box.ECDH(from.Box)
	if e != nil {
		return fail()
	}
	rawCtx, e := GCM(KDF(shared, wrapInfo(from.ID, to.ID)), nc, ad, ec, false)
	if e != nil {
		return fail()
	}
	c, e := parseContext(rawCtx)
	if e != nil {
		return fail()
	}
	if !bytes.Equal(c.From, from.ID) || !bytes.Equal(c.To, to.ID) || c.Class != class || c.FromRole != from.Role || c.ToRole != to.Role || from.Role == to.Role || len(c.Connection) != 16 || c.Seq == 0 || c.Issued > c.Deadline || c.Deadline-c.Issued > 30000 || now > c.Deadline || c.Issued > now+300000 {
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
	if len(c.SenderInstance) != 16 || len(c.ReceiverInstance) != 16 || len(c.SenderBoot) != 32 || len(c.ReceiverBoot) != 32 || c.FromRole < 1 || c.FromRole > 2 || c.ToRole < 1 || c.ToRole > 2 {
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
	body, e := GCM(KDF(shared, bodyInfo(c)), nb, LP(ad, Hash(ec)), eb, false)
	if e != nil {
		return fail()
	}
	w, e := Parse(body, 1)
	if e != nil || need(w, "b") != nil {
		return fail()
	}
	t, e := Parse(w[1].B, 2)
	if e != nil || need(t, "ub") != nil || t[1].U > 0xffffffff {
		return fail()
	}
	if boot && (class != 1 || (t[1].U != 1 && t[1].U != 2)) {
		return fail()
	}
	if !boot && class == 1 && c.Use == 0 && t[1].U != 8 && t[1].U != 9 {
		return fail()
	}
	return Opened{c, t[1].U, t[2].B, f[4].B, Hash(raw)}, nil
}
func Key(c Context) string {
	return fmt.Sprintf("%x/%x/%x/%d/%d/%d/%d", c.From, c.SenderInstance, c.Connection, c.Use, c.Generation, c.Class, c.Seq)
}
