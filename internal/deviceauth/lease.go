package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"slices"
)

// Lease is an independent certificate for one membership head. Renewal never
// changes the membership chain, grants, or a prepared recovery's parent.
type LeaseCertificate struct {
	Domain    Hash
	Head      Hash
	IssuedAt  uint64
	NotAfter  uint64
	Signature []byte
}

func (l LeaseCertificate) signBytes() ([]byte, error) {
	if l.Domain == (Hash{}) || l.Head == (Hash{}) || l.IssuedAt == 0 || l.NotAfter <= l.IssuedAt || l.NotAfter-l.IssuedAt > MaxLease {
		return nil, ErrInvalid
	}
	e := encoder{b: []byte("ra3/lease\x00")}
	e.raw(l.Domain[:])
	e.raw(l.Head[:])
	e.u64(l.IssuedAt)
	e.u64(l.NotAfter)
	return e.b, nil
}
func (l LeaseCertificate) MarshalBinary() ([]byte, error) {
	b, err := l.signBytes()
	if err != nil || len(l.Signature) != 64 {
		return nil, ErrInvalid
	}
	e := encoder{}
	e.lp(b)
	e.lp(l.Signature)
	return e.b, nil
}
func ParseLease(b []byte) (l LeaseCertificate, err error) {
	if len(b) > 256 {
		return l, ErrInvalid
	}
	d := decoder{b: b}
	body := d.lp(128)
	l.Signature = bytes.Clone(d.lp(64))
	if d.done() != nil || !bytes.HasPrefix(body, []byte("ra3/lease\x00")) {
		return l, ErrInvalid
	}
	r := decoder{b: body[len("ra3/lease\x00"):]}
	l.Domain = r.hash()
	l.Head = r.hash()
	l.IssuedAt = r.u64()
	l.NotAfter = r.u64()
	if r.done() != nil {
		return l, ErrInvalid
	}
	canonical, err := l.MarshalBinary()
	if err != nil || !bytes.Equal(b, canonical) {
		return l, ErrInvalid
	}
	return l, nil
}
func BuildLease(head State, signer ed25519.PrivateKey, now uint64) (LeaseCertificate, error) {
	m, ok := head.Core.Member(head.Core.AuthorityID)
	if !ok || len(signer) != 64 || !bytes.Equal(signer.Public().(ed25519.PublicKey), m.EdPublic[:]) {
		return LeaseCertificate{}, ErrUntrusted
	}
	h, err := head.Hash()
	if err != nil {
		return LeaseCertificate{}, err
	}
	l := LeaseCertificate{Domain: head.Core.Domain, Head: h, IssuedAt: now, NotAfter: addLease(now)}
	b, err := l.signBytes()
	if err != nil {
		return l, err
	}
	l.Signature = ed25519.Sign(signer, b)
	return l, nil
}
func (l LeaseCertificate) Verify(head State) error {
	h, err := head.Hash()
	if err != nil || h != l.Head || l.Domain != head.Core.Domain || l.IssuedAt < head.Core.IssuedAt {
		return ErrUntrusted
	}
	m, ok := head.Core.Member(head.Core.AuthorityID)
	b, err := l.signBytes()
	if !ok || err != nil || !ed25519.Verify(m.EdPublic[:], b, l.Signature) {
		return ErrUntrusted
	}
	return nil
}
func (l LeaseCertificate) Valid(head State, now uint64) error {
	if err := l.Verify(head); err != nil {
		return err
	}
	if l.IssuedAt > now+MaxClockSkew {
		return ErrClock
	}
	if now >= l.NotAfter {
		return ErrStale
	}
	return nil
}

// handoffDelta retains every other member exactly, and retires the old phone.
func handoffDelta(parent, next State) error {
	if _, ok := parent.Core.Member(next.Core.AuthorityID); !ok {
		return ErrInvalid
	}
	return recoveryDelta(parent, next)
}

// BuildHandoff is signed by the retiring authority; the incoming phone is an
// already paired member and has confirmed its new emergency code offline.
func BuildHandoff(parent State, incoming string, recoveryEd [32]byte, oldSecret, newSecret []byte, signer ed25519.PrivateKey, now uint64) (State, error) {
	if incoming == parent.Core.AuthorityID || recoveryEd == parent.Core.RecoveryEd || recoveryEd == [32]byte{} {
		return State{}, ErrInvalid
	}
	m, ok := parent.Core.Member(incoming)
	if !ok || m.Kind != Client {
		return State{}, ErrInvalid
	}
	members := []Member{}
	for _, m := range parent.Core.Members {
		if m.ID != parent.Core.AuthorityID {
			members = append(members, m)
		}
	}
	removed := append([]string{}, parent.Core.Removed...)
	removed = append(removed, parent.Core.AuthorityID)
	prev, err := sealPrevSecret(newSecret, oldSecret)
	if err != nil {
		return State{}, err
	}
	c := parent.Core
	c.Parent, _ = parent.Hash()
	c.Epoch++
	c.ContentEpoch++
	c.IssuedAt = maxU64(now, c.IssuedAt)
	c.Members = members
	c.Removed = removed
	c.AuthorityID = incoming
	c.RecoveryEd = recoveryEd
	slices.Sort(c.Removed)
	return build(c, newSecret, prev, signer)
}

func leaseValid(r record, now uint64) error {
	h, _ := chainHead(r).Hash()
	if r.Lease == nil || r.Lease.Head != h {
		return ErrStale
	}
	return r.Lease.Valid(chainHead(r), now)
}

// InstallLease durably accepts a verified certificate. A newer member head
// invalidates the prior certificate automatically; the signed chain is untouched.
func (e *Endpoint) InstallLease(l LeaseCertificate, now uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := l.Verify(chainHead(e.data)); err != nil {
		return err
	}
	if l.IssuedAt > maxU64(now, e.data.Floor)+MaxClockSkew {
		return ErrClock
	}
	if old := e.data.Lease; old != nil && old.Head == l.Head {
		if l.IssuedAt < old.IssuedAt || l.NotAfter < old.NotAfter {
			return ErrStale
		}
		if l.IssuedAt == old.IssuedAt && l.NotAfter != old.NotAfter {
			return ErrUntrusted
		}
	}
	raw, err := l.MarshalBinary()
	if err != nil {
		return err
	}
	floor := maxU64(now, e.data.Floor)
	tx, err := e.db.Begin()
	if err != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT OR REPLACE INTO lease(id,certificate) VALUES(1,?)`, raw); err != nil {
		return ErrStorage
	}
	if _, err = tx.Exec(`UPDATE security SET floor_ms=?,floor_saved_ms=? WHERE id=1`, floor, floor); err != nil {
		return ErrStorage
	}
	if err = tx.Commit(); err != nil {
		return ErrStorage
	}
	e.data.Lease = &l
	e.data.Floor = floor
	e.data.FloorSaved = floor
	return nil
}
func (e *Endpoint) Lease() *LeaseCertificate {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.data.Lease == nil {
		return nil
	}
	l := *e.data.Lease
	l.Signature = bytes.Clone(l.Signature)
	return &l
}
func (e *Endpoint) CheckLease(now uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if now+MaxClockSkew < e.data.Floor {
		return ErrClock
	}
	return leaseValid(e.data, maxU64(now, e.data.Floor))
}
