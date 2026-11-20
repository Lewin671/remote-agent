package devicesec

import (
	"bytes"
	"crypto/ed25519"
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

// SigningBytes is what the head's authority signs into Signature.
func (l LeaseCertificate) SigningBytes() ([]byte, error) {
	if l.Domain == (Hash{}) || l.Head == (Hash{}) || l.IssuedAt == 0 || l.NotAfter <= l.IssuedAt || l.NotAfter-l.IssuedAt > MaxLease {
		return nil, ErrInvalid
	}
	e := Encoder{B: []byte("ra3/lease\x00")}
	e.Raw(l.Domain[:])
	e.Raw(l.Head[:])
	e.U64(l.IssuedAt)
	e.U64(l.NotAfter)
	return e.B, nil
}
func (l LeaseCertificate) MarshalBinary() ([]byte, error) {
	b, err := l.SigningBytes()
	if err != nil || len(l.Signature) != 64 {
		return nil, ErrInvalid
	}
	e := Encoder{}
	e.LP(b)
	e.LP(l.Signature)
	return e.B, nil
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

// NewLease builds the unsigned lease for head starting at now (never before
// the head was issued); head's authority signs SigningBytes.
func NewLease(head State, now uint64) (LeaseCertificate, error) {
	if _, ok := head.Core.Member(head.Core.AuthorityID); !ok {
		return LeaseCertificate{}, ErrUntrusted
	}
	h, err := head.Hash()
	if err != nil {
		return LeaseCertificate{}, err
	}
	now = max(now, head.Core.IssuedAt)
	l := LeaseCertificate{Domain: head.Core.Domain, Head: h, IssuedAt: now, NotAfter: addLease(now)}
	if _, err := l.SigningBytes(); err != nil {
		return l, err
	}
	return l, nil
}
func (l LeaseCertificate) Verify(head State) error {
	h, err := head.Hash()
	if err != nil || h != l.Head || l.Domain != head.Core.Domain || l.IssuedAt < head.Core.IssuedAt {
		return ErrUntrusted
	}
	m, ok := head.Core.Member(head.Core.AuthorityID)
	b, err := l.SigningBytes()
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
