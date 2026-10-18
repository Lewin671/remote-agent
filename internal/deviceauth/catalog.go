package deviceauth

import (
	"crypto/ed25519"
	"slices"
)

type CatalogQuery struct {
	After   []byte
	AtState Hash
}
type CatalogEntry struct {
	Object []byte
	Wire   Wire
}
type CatalogReply struct {
	State   State
	Entries []CatalogEntry
	Next    []byte
}

// catalog is invoked only inside the nonce-bound signed and HPKE RPC. A paging
// cursor also pins the state head; a concurrent membership change forces restart.
func (a *Authority) catalog(sender string, q CatalogQuery) (CatalogReply, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.disk.check(); err != nil {
		return CatalogReply{}, err
	}
	d := a.data
	if _, ok := d.State.Core.Member(sender); !ok {
		return CatalogReply{}, ErrUntrusted
	}
	sh, _ := d.State.Hash()
	if len(q.After) > 512 || q.AtState != (Hash{}) && q.AtState != sh {
		return CatalogReply{}, ErrStale
	}
	keys := []string{}
	for k, w := range d.Catalog {
		h, err := ParseHeader(w.Header)
		if err != nil {
			return CatalogReply{}, ErrStorage
		}
		if h.Purpose == DeviceMeta {
			if _, ok := d.State.Core.Member(h.Request); !ok {
				continue
			}
		}
		if h.Machine != "" {
			if _, ok := d.State.Core.Member(h.Machine); !ok {
				continue
			}
		}
		if k > string(q.After) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	out := CatalogReply{State: clone(d.State)}
	size := 0
	for _, key := range keys {
		w := d.Catalog[key]
		b, err := w.MarshalBinary()
		if err != nil {
			return CatalogReply{}, ErrStorage
		}
		if len(out.Entries) > 0 && size+len(b) > MaxMutableBytes {
			out.Next = []byte(out.Entries[len(out.Entries)-1].Object)
			break
		}
		out.Entries = append(out.Entries, CatalogEntry{[]byte(key), clone(w)})
		size += len(b)
	}
	return out, nil
}
func (a *Authority) authorize(sender string, nonce Hash, q AuthorizeRequest) (AuthorizeReply, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.disk.check(); err != nil {
		return AuthorizeReply{}, err
	}
	d := a.data
	h, err := ParseHeader(q.Header)
	if err != nil || h.Sender != sender || nonce == (Hash{}) || q.Commit == (Hash{}) {
		return AuthorizeReply{}, ErrInvalid
	}
	if err = h.Scope(d.State); err != nil {
		return AuthorizeReply{}, err
	}
	m, _ := d.State.Core.Member(sender)
	hh := digest("", q.Header)
	if !ed25519.Verify(m.EdPublic[:], originBytes(h, hh, q.Commit), q.Origin) {
		return AuthorizeReply{}, ErrUntrusted
	}
	if h.Purpose.Control() {
		now := uint64(a.now().UnixMilli())
		if h.Deadline <= now || h.Deadline-now > 600000 {
			return AuthorizeReply{}, ErrInvalid
		}
	}
	auth := WriteAuth{Header: h, WriterKind: m.Kind, HeaderHash: hh, Commit: q.Commit, Nonce: nonce}
	raw, err := auth.MarshalBinary()
	if err != nil {
		return AuthorizeReply{}, err
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(d.EdSeed), writeSignBytes(raw))
	out := AuthorizeReply{raw, sig}
	if h.Purpose.Mutable() {
		if q.Cipher == nil || len(q.Cipher.Cipher) > MaxMutableBytes || !slices.Equal(q.Cipher.Header, q.Header) || !slices.Equal(q.Cipher.Origin, q.Origin) {
			return AuthorizeReply{}, ErrInvalid
		}
		commit, err := q.Cipher.Commitment()
		if err != nil || commit != q.Commit {
			return AuthorizeReply{}, ErrUntrusted
		}
		_, plain, err := openCipher(d.Secrets[h.ContentEpoch], nil, *q.Cipher)
		if err != nil {
			return AuthorizeReply{}, err
		}
		if ValidateBody(h, plain) != nil {
			return AuthorizeReply{}, ErrInvalid
		}
		key := h.objectKey()
		if prev, ok := d.Catalog[key]; ok {
			ph, err := ParseHeader(prev.Header)
			if err != nil {
				return AuthorizeReply{}, ErrStorage
			}
			pc, _ := prev.Commitment()
			if h.Seq == ph.Seq && pc == commit {
				return AuthorizeReply{prev.Authorization, prev.AuthSignature}, nil
			}
			if h.Seq != ph.Seq+1 {
				return AuthorizeReply{}, ErrConflict
			}
		} else if h.Seq != 1 {
			return AuthorizeReply{}, ErrConflict
		}
		d = clone(d)
		wire := Wire{Ciphertext: clone(*q.Cipher), Authorization: raw, AuthSignature: sig}
		d.Catalog[key] = wire
		if len(d.Catalog) > MaxObjects {
			return AuthorizeReply{}, ErrCapacity
		}
		total := 0
		for _, w := range d.Catalog {
			total += len(w.Header) + len(w.Cipher) + len(w.Authorization) + len(w.Origin) + len(w.AuthSignature)
		}
		if total > MaxCatalogBytes {
			return AuthorizeReply{}, ErrCapacity
		}
		if err = a.save(d, nil); err != nil {
			return AuthorizeReply{}, err
		}
	} else if q.Cipher != nil {
		return AuthorizeReply{}, ErrInvalid
	}
	return out, nil
}
func AuthorizationRequest(c Ciphertext) (AuthorizeRequest, error) {
	h, err := ParseHeader(c.Header)
	if err != nil {
		return AuthorizeRequest{}, err
	}
	commit, err := c.Commitment()
	if err != nil {
		return AuthorizeRequest{}, err
	}
	q := AuthorizeRequest{Header: slices.Clone(c.Header), Commit: commit, Origin: slices.Clone(c.Origin)}
	if h.Purpose.Mutable() {
		cp := clone(c)
		q.Cipher = &cp
	}
	return q, nil
}
func (r AuthorizeReply) Attach(c Ciphertext, p Pin, s State) (Wire, error) {
	w := Wire{Ciphertext: clone(c), Authorization: slices.Clone(r.Authorization), AuthSignature: slices.Clone(r.Signature)}
	_, err := w.Verify(p, s)
	return w, err
}
