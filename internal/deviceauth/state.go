package deviceauth

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hpke"
	"crypto/rand"
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
func ID(ed []byte) string { h := digest("", ed); return h.String()[:32] }
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
	RecoveryEd   [32]byte
	Parent       Hash
	Epoch        uint64
	ContentEpoch uint64
	IssuedAt     uint64

	Members []Member
	Removed []string
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
	if !deviceID(c.Account) || c.Domain == (Hash{}) || !deviceID(c.AuthorityID) || c.RecoveryEd == [32]byte{} || c.Epoch == 0 || c.ContentEpoch == 0 || c.ContentEpoch > c.Epoch || c.IssuedAt == 0 || len(c.Members) == 0 || len(c.Members) > MaxMembers || len(c.Removed) > MaxMembers {
		return ErrInvalid
	}
	for i, m := range c.Members {
		if m.validate() != nil || i > 0 && c.Members[i-1].ID >= m.ID {
			return ErrInvalid
		}
	}
	a, ok := c.Member(c.AuthorityID)
	if !ok || a.Kind != Client {
		return ErrInvalid
	}
	for i, id := range c.Removed {
		if !deviceID(id) || i > 0 && c.Removed[i-1] >= id {
			return ErrInvalid
		}
		if _, ok := c.Member(id); ok {
			return ErrInvalid
		}
		if id == c.AuthorityID {
			return ErrInvalid
		}
	}
	if c.Epoch == 1 && (c.Parent != (Hash{}) || len(c.Members) != 1 || len(c.Removed) != 0 || c.ContentEpoch != 1) || c.Epoch > 1 && c.Parent == (Hash{}) {
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
	e.raw(c.RecoveryEd[:])
	e.raw(c.Parent[:])
	e.u64(c.Epoch)
	e.u64(c.ContentEpoch)
	e.u64(c.IssuedAt)

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
	copy(c.RecoveryEd[:], d.raw(32))
	c.Parent = d.hash()
	c.Epoch = d.u64()
	c.ContentEpoch = d.u64()
	c.IssuedAt = d.u64()

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
	Core       Core
	Grants     []Grant
	PrevSecret []byte
	Signature  []byte
}

func (s State) coreHash() (Hash, error) {
	b, err := s.Core.MarshalBinary()
	if err != nil {
		return Hash{}, err
	}
	return digest("ra3/core\x00", b), nil
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
	p := encoder{}
	p.lp(s.PrevSecret)
	return digest("ra3/state\x00", ch[:], g, p.b), nil
}
func (s State) signBytes() ([]byte, error) {
	h, err := s.Hash()
	if err != nil {
		return nil, err
	}
	e := encoder{b: []byte("ra3/state-sig\x00")}
	e.str(s.Core.Account)
	e.raw(s.Core.Domain[:])
	e.raw(s.Core.Parent[:])
	e.u64(s.Core.Epoch)
	e.u64(s.Core.ContentEpoch)
	e.u64(s.Core.IssuedAt)

	e.str(s.Core.AuthorityID)
	e.raw(s.Core.RecoveryEd[:])
	e.raw(h[:])
	return e.b, nil
}
func (s State) MarshalBinary() ([]byte, error) {
	c, err := s.Core.MarshalBinary()
	if err != nil {
		return nil, err
	}
	g, err := s.grantBytes()
	if err != nil || len(s.PrevSecret) > MaxStateBytes || len(s.Signature) != 64 {
		return nil, ErrInvalid
	}
	e := encoder{}
	e.lp(c)
	e.raw(g)
	e.lp(s.PrevSecret)
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
	s.PrevSecret = d.lp(MaxStateBytes)
	s.Signature = d.lp(64)
	if d.done() != nil || len(s.Signature) != 64 {
		return s, ErrInvalid
	}
	if _, err = s.Hash(); err != nil {
		return s, err
	}
	canonical, err := s.MarshalBinary()
	if err != nil || !bytes.Equal(canonical, b) {
		return s, ErrInvalid
	}
	return s, nil
}

type Pin struct {
	Account string
	Domain  Hash
	Genesis Hash
}

func (s State) Pin() (Pin, error) {
	if s.Core.Epoch != 1 {
		return Pin{}, ErrInvalid
	}
	h, err := s.Hash()
	if err != nil {
		return Pin{}, err
	}
	return Pin{s.Core.Account, s.Core.Domain, h}, nil
}
func (p Pin) Verify(s State) error {
	if s.Core.Account != p.Account || s.Core.Domain != p.Domain || p.Genesis == (Hash{}) {
		return ErrUntrusted
	}
	if s.Core.Epoch == 1 {
		h, _ := s.Hash()
		if h != p.Genesis {
			return ErrUntrusted
		}
	}
	return nil
}
func (p Pin) VerifyGenesis(s State) error {
	if p.Verify(s) != nil || s.Core.Epoch != 1 {
		return ErrUntrusted
	}
	m, ok := s.Core.Member(s.Core.AuthorityID)
	if !ok || m.Kind != Client || len(s.Core.Members) != 1 || s.Core.Members[0].ID != m.ID {
		return ErrUntrusted
	}
	b, err := s.signBytes()
	if err != nil || !ed25519.Verify(m.EdPublic[:], b, s.Signature) {
		return ErrUntrusted
	}
	return nil
}

// VerifyTransition checks the signed transition from parent to next. A recovery
// transition also needs a RecoveryStatement signed by the parent's recovery key.
// It cannot open content grants; Install checks PrevSecret against the opened
// new secret.
func VerifyTransition(parent, next State, recovery *RecoveryStatement) error {
	ph, _ := parent.Hash()
	if next.Core.Parent != ph || next.Core.Epoch != parent.Core.Epoch+1 || next.Core.Account != parent.Core.Account || next.Core.Domain != parent.Core.Domain {
		return ErrStale
	}
	if next.Core.IssuedAt < parent.Core.IssuedAt {
		return ErrInvalid
	}
	authority, ok := next.Core.Member(next.Core.AuthorityID)
	if !ok || authority.Kind != Client {
		return ErrInvalid
	}
	if recovery != nil {
		if err := recovery.Verify(parent, next); err != nil {
			return err
		}
		if next.Core.ContentEpoch != parent.Core.ContentEpoch+1 || len(next.PrevSecret) != 0 {
			return ErrInvalid
		}
		if _, removed := next.Core.Member(parent.Core.AuthorityID); removed {
			return ErrInvalid
		}
		if !slices.Contains(next.Core.Removed, parent.Core.AuthorityID) {
			return ErrInvalid
		}
	} else {
		if next.Core.AuthorityID != parent.Core.AuthorityID {
			if next.Core.ContentEpoch != parent.Core.ContentEpoch+1 || len(next.PrevSecret) == 0 || next.Core.RecoveryEd == parent.Core.RecoveryEd {
				return ErrInvalid
			}
			if _, retained := next.Core.Member(parent.Core.AuthorityID); retained {
				return ErrInvalid
			}
			if err := handoffDelta(parent, next); err != nil {
				return err
			}
		}
		oldAuthority, ok := parent.Core.Member(parent.Core.AuthorityID)
		sigBytes, err := next.signBytes()
		if !ok || err != nil || !ed25519.Verify(oldAuthority.EdPublic[:], sigBytes, next.Signature) {
			return ErrUntrusted
		}
		switch {
		case next.Core.ContentEpoch == parent.Core.ContentEpoch:
			if next.Core.RecoveryEd == parent.Core.RecoveryEd || !slices.Equal(next.Core.Removed, parent.Core.Removed) || !sameMembers(next.Core.Members, parent.Core.Members) || len(next.PrevSecret) != 0 {
				return ErrInvalid
			}
		case next.Core.ContentEpoch == parent.Core.ContentEpoch+1:
			if len(next.PrevSecret) == 0 {
				return ErrInvalid
			}
			if err := membershipDelta(parent, next); err != nil && next.Core.AuthorityID == parent.Core.AuthorityID {
				return err
			}
		default:
			return ErrInvalid
		}
		return nil
	}
	b, err := next.signBytes()
	if err != nil || !ed25519.Verify(authority.EdPublic[:], b, next.Signature) {
		return ErrUntrusted
	}
	if !slices.Contains(next.Core.Removed, parent.Core.AuthorityID) {
		return ErrInvalid
	}
	return recoveryDelta(parent, next)
}
func mustSignBytes(s State) []byte {
	b, err := s.signBytes()
	if err != nil {
		panic("security: internal state cannot sign")
	}
	return b
}
func sameMembers(a, b []Member) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func recoveryDelta(parent, next State) error {
	removed := slices.Clone(parent.Core.Removed)
	removed = append(removed, parent.Core.AuthorityID)
	slices.Sort(removed)
	if !slices.Equal(removed, next.Core.Removed) {
		return ErrInvalid
	}
	for _, m := range parent.Core.Members {
		if m.ID == parent.Core.AuthorityID {
			continue
		}
		nm, ok := next.Core.Member(m.ID)
		if !ok || nm != m {
			return ErrInvalid
		}
	}
	for _, m := range next.Core.Members {
		if m.ID == next.Core.AuthorityID {
			continue
		}
		if _, ok := parent.Core.Member(m.ID); !ok {
			return ErrInvalid
		}
	}
	return nil
}

func membershipDelta(parent, next State) error {
	newlyRemoved := []string{}
	added := 0
	rotated := 0
	for _, m := range parent.Core.Members {
		nm, ok := next.Core.Member(m.ID)
		if !ok {
			newlyRemoved = append(newlyRemoved, m.ID)
		} else if nm.EdPublic != m.EdPublic || nm.Kind != m.Kind {
			return ErrInvalid
		} else if nm.XPublic != m.XPublic {
			rotated++
		}
	}
	for _, m := range next.Core.Members {
		if _, ok := parent.Core.Member(m.ID); !ok {
			if slices.Contains(parent.Core.Removed, m.ID) {
				return ErrInvalid // a removed ID can never be re-added
			}
			added++
		}
	}
	// Removed is the accumulated, sorted tombstone set, not just this
	// transition's removals.
	wantRemoved := append(slices.Clone(parent.Core.Removed), newlyRemoved...)
	slices.Sort(wantRemoved)
	if !slices.Equal(wantRemoved, next.Core.Removed) {
		return ErrInvalid
	}
	if added > 1 || rotated > 1 || added > 0 && len(newlyRemoved) > 0 || rotated > 0 && (added > 0 || len(newlyRemoved) > 0) || added == 0 && len(newlyRemoved) == 0 && rotated == 0 {
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

// XPublic derives the public half of an X25519 private key.
func XPublic(private []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return nil, ErrInvalid
	}
	return k.PublicKey().Bytes(), nil
}

func NewXKey() (private, public []byte, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return k.Bytes(), k.PublicKey().Bytes(), nil
}

func BuildGenesis(account string, domain Hash, member Member, recoveryEd [32]byte, secret []byte, signer ed25519.PrivateKey, now uint64) (State, error) {
	if member.validate() != nil || member.Kind != Client || len(secret) != 32 || recoveryEd == [32]byte{} || len(signer) != 64 || !bytes.Equal(signer.Public().(ed25519.PublicKey), member.EdPublic[:]) {
		return State{}, ErrInvalid
	}
	c := Core{Account: account, Domain: domain, AuthorityID: member.ID, RecoveryEd: recoveryEd, Epoch: 1, ContentEpoch: 1, IssuedAt: now, Members: []Member{member}}
	return build(c, secret, nil, signer)
}

func BuildState(parent State, members []Member, removed []string, secret, prevSecret []byte, signer ed25519.PrivateKey, now uint64) (State, error) {
	if len(signer) != 64 || len(secret) != 32 {
		return State{}, ErrInvalid
	}
	ph, _ := parent.Hash()
	c := parent.Core
	c.Parent = ph
	c.Epoch++
	c.ContentEpoch++
	c.IssuedAt = maxU64(now, c.IssuedAt)

	c.Members = slices.Clone(members)
	slices.SortFunc(c.Members, func(a, b Member) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	c.Removed = removed
	if len(prevSecret) > 0 && len(prevSecret) != 60 {
		return State{}, ErrInvalid
	}
	return build(c, secret, prevSecret, signer)
}

// BuildRecoveryKeyRotation changes only the emergency authority credential.
func BuildRecoveryKeyRotation(parent State, secret []byte, recoveryEd [32]byte, signer ed25519.PrivateKey, now uint64) (State, error) {
	if len(signer) != 64 || len(secret) != 32 || recoveryEd == [32]byte{} || recoveryEd == parent.Core.RecoveryEd {
		return State{}, ErrInvalid
	}
	ph, _ := parent.Hash()
	c := parent.Core
	c.Parent = ph
	c.Epoch++
	c.IssuedAt = maxU64(now, c.IssuedAt)
	c.RecoveryEd = recoveryEd
	return build(c, secret, nil, signer)
}
func build(c Core, secret, prevSecret []byte, signer ed25519.PrivateKey) (State, error) {
	s := State{Core: c, PrevSecret: bytes.Clone(prevSecret)}
	if err := c.validate(); err != nil {
		return State{}, err
	}
	ch, _ := s.coreHash()
	for _, m := range c.Members {
		ctx := grantContext(c, ch, m.ID)
		enc, ct, err := sealHPKE(m.XPublic[:], append([]byte("ra3/grant-info\x00"), ctx...), append([]byte("ra3/grant-aad\x00"), ctx...), secret)
		if err != nil {
			return State{}, err
		}
		g := Grant{Recipient: m.ID, Cipher: ct}
		copy(g.Enc[:], enc)
		s.Grants = append(s.Grants, g)
	}
	b, err := s.signBytes()
	if err != nil {
		return State{}, err
	}
	s.Signature = ed25519.Sign(signer, b)
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
			plain, err := openHPKE(private, g.Enc[:], append([]byte("ra3/grant-info\x00"), ctx...), append([]byte("ra3/grant-aad\x00"), ctx...), g.Cipher)
			if err != nil || len(plain) != 32 {
				return nil, ErrUntrusted
			}
			return plain, nil
		}
	}
	return nil, ErrInvalid
}

func sealPrevSecret(newSecret, oldSecret []byte) ([]byte, error) {
	if len(newSecret) != 32 || len(oldSecret) != 32 {
		return nil, ErrInvalid
	}
	a, err := gcm(newSecret)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, a.Seal(nil, nonce, oldSecret, []byte("ra3/prev-secret\x00"))...), nil
}
func openPrevSecret(newSecret, blob []byte) ([]byte, error) {
	if len(newSecret) != 32 || len(blob) != 60 {
		return nil, ErrInvalid
	}
	a, err := gcm(newSecret)
	if err != nil {
		return nil, err
	}
	out, err := a.Open(nil, blob[:12], blob[12:], []byte("ra3/prev-secret\x00"))
	if err != nil {
		return nil, ErrUntrusted
	}
	if len(out) != 32 {
		return nil, ErrInvalid
	}
	return out, nil
}
