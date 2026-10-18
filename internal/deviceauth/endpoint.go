package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"sync"
)

type endpointData struct {
	Pin      Pin
	State    State
	MemberID string
	EdSeed   []byte
	XPrivate []byte
	Secret   []byte
	Counter  uint64
	Floor    Floor
}
type Floor struct {
	Epoch    uint64
	State    Hash
	Group    string
	Revision uint64
}

// Endpoint owns the same gate for backend acceptance and durable fences. It
// deliberately does not release the gate before the actual backend call ends.
// Result encryption/authorization belongs AFTER Execute returns, outside gate.
type Endpoint struct {
	mu   sync.Mutex
	gate sync.RWMutex
	disk *localStore
	data endpointData
}

func InitEndpoint(path string, p Pin, s State, id string, edSeed, xPrivate []byte) (*Endpoint, error) {
	secret, err := p.OpenGrant(s, id, xPrivate)
	if err != nil || len(edSeed) != 32 || ID(ed25519.NewKeyFromSeed(edSeed).Public().(ed25519.PublicKey)) != id {
		return nil, ErrUntrusted
	}
	sh, _ := s.Hash()
	d := endpointData{Pin: p, State: s, MemberID: id, EdSeed: bytes.Clone(edSeed), XPrivate: bytes.Clone(xPrivate), Secret: secret, Floor: Floor{Epoch: s.Core.Epoch, State: sh}}
	disk, err := openStore(path, d)
	if err != nil {
		return nil, err
	}
	if err = disk.save(d, &s); err != nil {
		disk.close()
		return nil, err
	}
	if err := disk.cacheHistory(s, secret); err != nil {
		disk.close()
		return nil, err
	}
	return &Endpoint{disk: disk, data: d}, nil
}
func OpenEndpoint(path string) (*Endpoint, error) {
	disk, err := openStore(path, nil)
	if err != nil {
		return nil, err
	}
	e := &Endpoint{disk: disk}
	if disk.load(&e.data) != nil || e.validate() != nil {
		disk.close()
		return nil, ErrStorage
	}
	return e, nil
}
func (e *Endpoint) validate() error {
	d := e.data
	secret, err := d.Pin.OpenGrant(d.State, d.MemberID, d.XPrivate)
	if err != nil || len(d.EdSeed) != 32 || ID(ed25519.NewKeyFromSeed(d.EdSeed).Public().(ed25519.PublicKey)) != d.MemberID || !bytes.Equal(secret, d.Secret) {
		return ErrStorage
	}
	sh, _ := d.State.Hash()
	if d.Floor.Epoch != d.State.Core.Epoch || d.Floor.State != sh {
		return ErrStorage
	}
	return nil
}
func (e *Endpoint) Close() error { e.gate.Lock(); defer e.gate.Unlock(); return e.disk.close() }
func (e *Endpoint) Snapshot() (Pin, State, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.data.Pin, clone(e.data.State), e.data.MemberID
}

// Prepare reserves a message counter durably before forming the ciphertext. A
// crash can waste a counter but cannot reuse one. Network retries retain c.
func (e *Endpoint) Prepare(h Header, sessionSeed, plain []byte) (Ciphertext, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := clone(e.data)
	if d.Counter == ^uint64(0) {
		return Ciphertext{}, ErrCapacity
	}
	counter := d.Counter + 1
	var err error
	d.Counter = counter
	sh, _ := d.State.Hash()
	h.Account = d.Pin.Account
	h.Domain = d.Pin.Domain
	h.Epoch = d.State.Core.Epoch
	h.State = sh
	h.ContentEpoch = d.State.Core.ContentEpoch
	h.Sender = d.MemberID
	h.Counter = counter
	if h.Scope(d.State) != nil {
		return Ciphertext{}, ErrUntrusted
	}
	if err = e.disk.save(d, nil); err != nil {
		return Ciphertext{}, err
	}
	e.data = d
	return Seal(d.Secret, sessionSeed, h, plain, ed25519.NewKeyFromSeed(d.EdSeed))
}
func (e *Endpoint) Open(w Wire, sessionSeed []byte) (Header, []byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.disk.check(); err != nil {
		return Header{}, nil, err
	}
	return w.Open(e.data.Pin, e.data.State, e.data.Secret, sessionSeed)
}

// Execute verifies the current state AGAIN under the gate, not just when the
// wire arrived. fn must encompass the true backend acceptance boundary.
func (e *Endpoint) Execute(w Wire, sessionSeed []byte, now uint64, fn func(Header, []byte) error) error {
	_, err := e.Accept(w, sessionSeed, now, func(h Header, p []byte) ([]byte, error) { return nil, fn(h, p) })
	return err
}

// Accept covers verification, durable at-most-once admission and the ACTUAL
// backend call under the same fence gate. A crash after Started is unknown;
// neither a retry nor a re-encryption in a newer epoch executes it again.
// Result encryption and all authority/network round trips occur after return.
func (e *Endpoint) Accept(w Wire, sessionSeed []byte, now uint64, fn func(Header, []byte) ([]byte, error)) ([]byte, error) {
	e.gate.RLock()
	defer e.gate.RUnlock()
	e.mu.Lock()
	d := clone(e.data)
	err := e.disk.check()
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	h, p, err := w.Open(d.Pin, d.State, d.Secret, sessionSeed)
	if err != nil {
		return nil, err
	}
	if !h.Purpose.Control() || h.Epoch < d.Floor.Epoch || h.State != d.Floor.State || h.Machine != d.MemberID || h.Deadline > now && h.Deadline-now > 600000 {
		return nil, ErrUntrusted
	}
	var key encoder
	key.str(h.Sender)
	key.u32(uint32(h.Purpose))
	key.str(h.Machine)
	key.str(h.Session)
	key.str(h.Request)
	key.u64(h.Seq)
	cached, run, err := e.disk.start(key.b, h.Deadline, now)
	if err != nil || !run {
		return cached, err
	}
	result, callErr := fn(h, p)
	if err := e.disk.finish(key.b, result, callErr != nil); err != nil {
		return nil, err
	}
	return result, callErr
}

// Install verifies the contiguous state chain and the recipient grant before
// storing a higher executor floor. It does not consult relay online hints.
func (e *Endpoint) install(chain []State, group string, revision uint64) (FenceReceipt, error) {
	if len(chain) > 2 || group != "" && !validID(group) || group != "" && revision == 0 {
		return FenceReceipt{}, ErrInvalid
	}
	e.gate.Lock()
	defer e.gate.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	d := clone(e.data)
	for _, s := range chain {
		sh, _ := s.Hash()
		oldh, _ := d.State.Hash()
		if sh == oldh {
			continue
		}
		if err := d.Pin.Advance(d.State, s); err != nil {
			return FenceReceipt{}, err
		}
		secret, err := d.Pin.OpenGrant(s, d.MemberID, d.XPrivate)
		if err != nil {
			return FenceReceipt{}, err
		}
		if err := e.disk.cacheHistory(s, secret); err != nil {
			return FenceReceipt{}, err
		}
		d.State = clone(s)
		d.Secret = secret
		d.Counter = 0
	}
	sh, _ := d.State.Hash()
	if d.State.Core.Epoch < d.Floor.Epoch || d.State.Core.Epoch == d.Floor.Epoch && sh != d.Floor.State {
		return FenceReceipt{}, ErrStale
	}
	if d.Floor.Group == group && d.Floor.Revision > revision {
		return FenceReceipt{}, ErrStale
	}
	if group != "" || d.Floor.Epoch != d.State.Core.Epoch {
		d.Floor = Floor{Epoch: d.State.Core.Epoch, State: sh, Group: group, Revision: revision}
	}
	if err := e.disk.save(d, nil); err != nil {
		return FenceReceipt{}, err
	}
	e.data = d
	if group == "" {
		return FenceReceipt{}, nil
	}
	r := FenceReceipt{Account: d.Pin.Account, Domain: d.Pin.Domain, AuthorityID: d.Pin.AuthorityID, Executor: d.MemberID, Group: group, Revision: revision, Epoch: d.State.Core.Epoch, State: sh}
	b, err := r.signBytes()
	if err != nil {
		return FenceReceipt{}, err
	}
	r.Signature = ed25519.Sign(ed25519.NewKeyFromSeed(d.EdSeed), b)
	return r, nil
}
