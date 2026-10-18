package deviceauth

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hpke"
	"crypto/rand"
	"encoding/hex"
	"slices"
)

type Kind uint32

const (
	Machine Kind = 1
	Client  Kind = 2
)

type Member struct {
	ID       string
	EdPublic [32]byte
	XPublic  [32]byte
	Kind     Kind
}

func NewMember(ed ed25519.PublicKey, x []byte, kind Kind) (m Member, err error) {
	if len(ed) != 32 || len(x) != 32 {
		return m, ErrInvalid
	}
	m.ID = ID(ed)
	copy(m.EdPublic[:], ed)
	copy(m.XPublic[:], x)
	m.Kind = kind
	return m, m.validate()
}
func ID(ed []byte) string { h := digest("", ed); return hex.EncodeToString(h[:16]) }
func (m Member) validate() error {
	if !deviceID(m.ID) || m.ID != ID(m.EdPublic[:]) || (m.Kind != Machine && m.Kind != Client) || m.XPublic == [32]byte{} {
		return ErrInvalid
	}
	return nil
}

type Core struct {
	Account      string
	Domain       Hash
	AuthorityID  string
	AuthorityEd  [32]byte
	AuthorityX   [32]byte
	Parent       Hash
	Epoch        uint64
	ContentEpoch uint64
	Members      []Member
	Removed      []string
}

func (c Core) Member(id string) (Member, bool) {
	i, ok := slices.BinarySearchFunc(c.Members, id, func(m Member, id string) int {
		if m.ID < id {
			return -1
		}
		if m.ID > id {
			return 1
		}
		return 0
	})
	if !ok {
		return Member{}, false
	}
	return c.Members[i], true
}
func (c Core) validate() error {
	if !deviceID(c.Account) || c.Domain == (Hash{}) || !deviceID(c.AuthorityID) || c.AuthorityEd == [32]byte{} || c.AuthorityX == [32]byte{} || c.Epoch == 0 || c.ContentEpoch != c.Epoch || len(c.Members) == 0 || len(c.Members) > MaxMembers || len(c.Removed) > MaxMembers {
		return ErrInvalid
	}
	for i, m := range c.Members {
		if m.validate() != nil || i > 0 && c.Members[i-1].ID >= m.ID {
			return ErrInvalid
		}
	}
	a, ok := c.Member(c.AuthorityID)
	if !ok || a.Kind != Machine || a.EdPublic == c.AuthorityEd || a.XPublic == c.AuthorityX {
		return ErrInvalid
	}
	for i, id := range c.Removed {
		if !deviceID(id) || i > 0 && c.Removed[i-1] >= id {
			return ErrInvalid
		}
		if _, ok := c.Member(id); ok {
			return ErrInvalid
		}
	}
	if c.Epoch == 1 && (c.Parent != (Hash{}) || len(c.Members) != 1 || len(c.Removed) != 0) || c.Epoch > 1 && c.Parent == (Hash{}) {
		return ErrInvalid
	}
	return nil
}
func (c Core) MarshalBinary() ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	e := encoder{}
	e.u32(Version)
	e.str(c.Account)
	e.raw(c.Domain[:])
	e.str(c.AuthorityID)
	e.raw(c.AuthorityEd[:])
	e.raw(c.AuthorityX[:])
	e.raw(c.Parent[:])
	e.u64(c.Epoch)
	e.u64(c.ContentEpoch)
	e.u32(uint32(len(c.Members)))
	for _, m := range c.Members {
		e.str(m.ID)
		e.raw(m.EdPublic[:])
		e.raw(m.XPublic[:])
		e.u32(uint32(m.Kind))
	}
	e.u32(uint32(len(c.Removed)))
	for _, id := range c.Removed {
		e.str(id)
	}
	return e.b, nil
}
func ParseCore(b []byte) (c Core, err error) {
	if len(b) > MaxStateBytes {
		return c, ErrInvalid
	}
	d := decoder{b: b}
	if d.u32() != Version {
		return c, ErrInvalid
	}
	c.Account = d.str()
	c.Domain = d.hash()
	c.AuthorityID = d.str()
	copy(c.AuthorityEd[:], d.raw(32))
	copy(c.AuthorityX[:], d.raw(32))
	c.Parent = d.hash()
	c.Epoch = d.u64()
	c.ContentEpoch = d.u64()
	n := d.u32()
	if n > MaxMembers {
		return c, ErrInvalid
	}
	for range n {
		m := Member{ID: d.str()}
		copy(m.EdPublic[:], d.raw(32))
		copy(m.XPublic[:], d.raw(32))
		m.Kind = Kind(d.u32())
		c.Members = append(c.Members, m)
	}
	n = d.u32()
	if n > MaxMembers {
		return c, ErrInvalid
	}
	for range n {
		c.Removed = append(c.Removed, d.str())
	}
	if d.done() != nil {
		return c, ErrInvalid
	}
	return c, c.validate()
}

type Grant struct {
	Recipient string
	Enc       [32]byte
	Cipher    []byte
}
type State struct {
	Core      Core
	Grants    []Grant
	Signature []byte
}
type Pin struct {
	Account     string
	Domain      Hash
	Genesis     Hash
	AuthorityID string
	AuthorityEd [32]byte
	AuthorityX  [32]byte
}

func (s State) coreHash() (Hash, error) {
	b, err := s.Core.MarshalBinary()
	if err != nil {
		return Hash{}, err
	}
	return digest("ra2/core\x00", b), nil
}
func (s State) grantBytes() ([]byte, error) {
	if len(s.Grants) != len(s.Core.Members) {
		return nil, ErrInvalid
	}
	e := encoder{}
	e.u32(uint32(len(s.Grants)))
	for i, g := range s.Grants {
		if g.Recipient != s.Core.Members[i].ID || len(g.Cipher) != 48 || g.Enc == [32]byte{} {
			return nil, ErrInvalid
		}
		e.str(g.Recipient)
		e.lp(g.Enc[:])
		e.lp(g.Cipher)
	}
	return e.b, nil
}
func (s State) Hash() (Hash, error) {
	ch, err := s.coreHash()
	if err != nil {
		return Hash{}, err
	}
	g, err := s.grantBytes()
	if err != nil {
		return Hash{}, err
	}
	gh := digest("ra2/grants\x00", g)
	return digest("ra2/state\x00", ch[:], gh[:]), nil
}
func (s State) signBytes() ([]byte, error) {
	h, err := s.Hash()
	if err != nil {
		return nil, err
	}
	e := encoder{b: []byte("ra2/state-sig\x00")}
	e.str(s.Core.Account)
	e.raw(s.Core.Domain[:])
	e.u64(s.Core.Epoch)
	e.raw(h[:])
	return e.b, nil
}
func (s State) MarshalBinary() ([]byte, error) {
	c, err := s.Core.MarshalBinary()
	if err != nil {
		return nil, err
	}
	g, err := s.grantBytes()
	if err != nil || len(s.Signature) != 64 {
		return nil, ErrInvalid
	}
	e := encoder{}
	e.lp(c)
	e.raw(g)
	e.lp(s.Signature)
	if len(e.b) > MaxStateBytes {
		return nil, ErrCapacity
	}
	return e.b, nil
}
func ParseState(b []byte) (s State, err error) {
	if len(b) > MaxStateBytes {
		return s, ErrInvalid
	}
	d := decoder{b: b}
	s.Core, err = ParseCore(d.lp(MaxStateBytes))
	if err != nil {
		return s, err
	}
	n := d.u32()
	if n > MaxMembers {
		return s, ErrInvalid
	}
	for range n {
		g := Grant{Recipient: d.str()}
		enc := d.lp(32)
		if len(enc) != 32 {
			return s, ErrInvalid
		}
		copy(g.Enc[:], enc)
		g.Cipher = d.lp(48)
		s.Grants = append(s.Grants, g)
	}
	s.Signature = d.lp(64)
	if d.done() != nil || len(s.Signature) != 64 {
		return s, ErrInvalid
	}
	_, err = s.Hash()
	return s, err
}
func (s State) Pin() (Pin, error) {
	if s.Core.Epoch != 1 {
		return Pin{}, ErrInvalid
	}
	h, err := s.Hash()
	if err != nil {
		return Pin{}, err
	}
	return Pin{s.Core.Account, s.Core.Domain, h, s.Core.AuthorityID, s.Core.AuthorityEd, s.Core.AuthorityX}, nil
}
func (p Pin) Verify(s State) error {
	c := s.Core
	if c.Account != p.Account || c.Domain != p.Domain || c.AuthorityID != p.AuthorityID || c.AuthorityEd != p.AuthorityEd || c.AuthorityX != p.AuthorityX || p.Genesis == (Hash{}) {
		return ErrUntrusted
	}
	b, err := s.signBytes()
	if err != nil {
		return err
	}
	if !ed25519.Verify(p.AuthorityEd[:], b, s.Signature) {
		return ErrUntrusted
	}
	if c.Epoch == 1 {
		h, _ := s.Hash()
		if h != p.Genesis {
			return ErrUntrusted
		}
	}
	return nil
}

// Advance accepts only a contiguous signed transition. A relay cannot replace
// the pinned authority, insert its directory, skip a link, or choose a fork.
func (p Pin) Advance(old, s State) error {
	if p.Verify(old) != nil || p.Verify(s) != nil {
		return ErrUntrusted
	}
	h, _ := old.Hash()
	n, err := next(old.Core.Epoch)
	if err != nil || s.Core.Epoch != n || s.Core.Parent != h {
		return ErrStale
	}
	removed := []string{}
	added := 0
	for _, m := range old.Core.Members {
		if nm, ok := s.Core.Member(m.ID); ok {
			if nm != m {
				return ErrInvalid
			}
		} else {
			removed = append(removed, m.ID)
		}
	}
	for _, m := range s.Core.Members {
		if _, ok := old.Core.Member(m.ID); !ok {
			added++
		}
	}
	if !slices.Equal(removed, s.Core.Removed) || added > 1 || added > 0 && len(removed) > 0 || added == 0 && len(removed) == 0 {
		return ErrInvalid
	}
	return nil
}
func grantContext(c Core, ch Hash, id string) []byte {
	e := encoder{}
	e.str(c.Account)
	e.raw(c.Domain[:])
	e.u64(c.ContentEpoch)
	e.raw(ch[:])
	e.str(id)
	return e.b
}
func sealHPKE(pub, info, aad, plain []byte) (enc, ct []byte, err error) {
	pk, err := hpke.DHKEM(ecdh.X25519()).NewPublicKey(pub)
	if err != nil {
		return nil, nil, ErrInvalid
	}
	enc, s, err := hpke.NewSender(pk, hpke.HKDFSHA256(), hpke.AES256GCM(), info)
	if err != nil {
		return nil, nil, err
	}
	ct, err = s.Seal(aad, plain)
	return
}
func openHPKE(priv, enc, info, aad, ct []byte) ([]byte, error) {
	sk, err := hpke.DHKEM(ecdh.X25519()).NewPrivateKey(priv)
	if err != nil {
		return nil, ErrInvalid
	}
	r, err := hpke.NewRecipient(enc, sk, hpke.HKDFSHA256(), hpke.AES256GCM(), info)
	if err != nil {
		return nil, ErrUntrusted
	}
	plain, err := r.Open(aad, ct)
	if err != nil {
		return nil, ErrUntrusted
	}
	return plain, nil
}
func NewXKey() (private, public []byte, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return k.Bytes(), k.PublicKey().Bytes(), nil
}
func BuildState(c Core, secret []byte, authority ed25519.PrivateKey) (s State, err error) {
	s.Core = c
	if len(secret) != 32 || len(authority) != 64 || !bytes.Equal(authority.Public().(ed25519.PublicKey), c.AuthorityEd[:]) {
		return s, ErrInvalid
	}
	ch, err := s.coreHash()
	if err != nil {
		return s, err
	}
	for _, m := range c.Members {
		ctx := grantContext(c, ch, m.ID)
		enc, ct, err := sealHPKE(m.XPublic[:], append([]byte("ra2/grant-info\x00"), ctx...), append([]byte("ra2/grant-aad\x00"), ctx...), secret)
		if err != nil {
			return s, err
		}
		g := Grant{Recipient: m.ID, Cipher: ct}
		copy(g.Enc[:], enc)
		s.Grants = append(s.Grants, g)
	}
	b, err := s.signBytes()
	if err != nil {
		return s, err
	}
	s.Signature = ed25519.Sign(authority, b)
	return s, nil
}
func (p Pin) OpenGrant(s State, id string, private []byte) ([]byte, error) {
	if err := p.Verify(s); err != nil {
		return nil, err
	}
	m, ok := s.Core.Member(id)
	if !ok {
		return nil, ErrUntrusted
	}
	sk, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil || !bytes.Equal(sk.PublicKey().Bytes(), m.XPublic[:]) {
		return nil, ErrUntrusted
	}
	ch, _ := s.coreHash()
	for _, g := range s.Grants {
		if g.Recipient == id {
			ctx := grantContext(s.Core, ch, id)
			plain, err := openHPKE(private, g.Enc[:], append([]byte("ra2/grant-info\x00"), ctx...), append([]byte("ra2/grant-aad\x00"), ctx...), g.Cipher)
			if err != nil || len(plain) != 32 {
				return nil, ErrUntrusted
			}
			return plain, nil
		}
	}
	return nil, ErrInvalid
}
