package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type record struct {
	Version     int
	Pin         Pin
	States      map[uint64]State
	HeadEpoch   uint64
	MemberID    string
	EdSeed      []byte
	XPrivate    []byte
	Secret      []byte
	Counter     uint64
	BlockEnd    uint64
	Floor       uint64
	ContentKeys map[uint64][]byte
	Recovery    map[uint64]RecoveryStatement
	Accepted    map[string]accepted
}

type accepted struct {
	Deadline uint64
	Done     bool
	Result   []byte
	Failed   bool
}

type Endpoint struct {
	mu   sync.Mutex
	path string
	data record
}

func chainHead(r record) State { return r.States[r.HeadEpoch] }

func verifyChain(pin Pin, states []State, proofs []*RecoveryStatement) error {
	if len(states) == 0 || len(states) > MaxStates || len(proofs) != len(states) {
		return ErrInvalid
	}
	if pin.VerifyGenesis(states[0]) != nil {
		return ErrUntrusted
	}
	for i := 1; i < len(states); i++ {
		if err := VerifyTransition(states[i-1], states[i], proofs[i]); err != nil {
			return err
		}
	}
	return nil
}

// InitEndpoint verifies a chain from the pinned genesis and stores the local
// device state. An empty path keeps it in memory.
func InitEndpoint(path string, pin Pin, states []State, proofs []*RecoveryStatement, id string, edSeed, xPrivate []byte) (*Endpoint, error) {
	if len(edSeed) != 32 || ID(ed25519.NewKeyFromSeed(edSeed).Public().(ed25519.PublicKey)) != id {
		return nil, ErrInvalid
	}
	if err := verifyChain(pin, states, proofs); err != nil {
		return nil, err
	}
	secret, err := pin.OpenGrant(states[len(states)-1], id, xPrivate)
	if err != nil {
		return nil, err
	}
	r := record{Version: Version, Pin: pin, States: map[uint64]State{}, HeadEpoch: states[len(states)-1].Core.Epoch, MemberID: id, EdSeed: bytes.Clone(edSeed), XPrivate: bytes.Clone(xPrivate), Secret: secret, ContentKeys: map[uint64][]byte{}, Recovery: map[uint64]RecoveryStatement{}, Accepted: map[string]accepted{}}
	for i, s := range states {
		r.States[s.Core.Epoch] = s
		if proofs[i] != nil {
			r.Recovery[s.Core.Epoch] = *proofs[i]
		}
	}
	e := &Endpoint{path: path, data: r}
	if err := e.cacheChainKeys(); err != nil {
		return nil, err
	}
	if path != "" {
		if err := e.save(); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func OpenEndpoint(path string) (*Endpoint, error) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || len(b) > 32<<20 {
		return nil, ErrStorage
	}
	var r record
	if json.Unmarshal(b, &r) != nil || r.Version != Version || r.States == nil || r.Recovery == nil || r.Accepted == nil || len(r.States) == 0 || r.States[r.HeadEpoch].Core.Epoch != r.HeadEpoch || r.MemberID == "" || len(r.XPrivate) != 32 || len(r.EdSeed) != 32 || len(r.Secret) != 32 {
		return nil, ErrStorage
	}
	e := &Endpoint{path: path, data: r}
	if err := e.validate(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Endpoint) validate() error {
	r := e.data
	if r.Pin.VerifyGenesis(r.States[1]) != nil {
		return ErrStorage
	}
	var prev State
	for ep := uint64(1); ep <= r.HeadEpoch; ep++ {
		s, ok := r.States[ep]
		if !ok {
			return ErrStorage
		}
		if ep == 1 {
			if r.Pin.VerifyGenesis(s) != nil {
				return ErrStorage
			}
		} else {
			var proof *RecoveryStatement
			if p, ok := r.Recovery[ep]; ok {
				proof = &p
			}
			if VerifyTransition(prev, s, proof) != nil {
				return ErrStorage
			}
		}
		prev = s
	}
	if r.MemberID != ID(ed25519.NewKeyFromSeed(r.EdSeed).Public().(ed25519.PublicKey)) {
		return ErrStorage
	}
	secret, err := r.Pin.OpenGrant(chainHead(r), r.MemberID, r.XPrivate)
	if err != nil || !bytes.Equal(secret, r.Secret) {
		return ErrStorage
	}
	return nil
}

func (e *Endpoint) save() error {
	if e.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(e.path), 0700); err != nil {
		return ErrStorage
	}
	b, err := json.Marshal(e.data)
	if err != nil || len(b) > 32<<20 {
		return ErrCapacity
	}
	f, err := os.OpenFile(e.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return ErrStorage
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return ErrStorage
	}
	return nil
}
func (e *Endpoint) Close() error { e.mu.Lock(); defer e.mu.Unlock(); return e.save() }
func (e *Endpoint) Snapshot() (Pin, State, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.data.Pin, chainHead(e.data), e.data.MemberID
}
func (e *Endpoint) StateCount() int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.data.States) }

// Install appends a contiguous chain segment. proofs[i] is set only when
// states[i] is a recovery transition.
func (e *Endpoint) Install(states []State, proofs []*RecoveryStatement) error {
	if len(states) == 0 || len(proofs) != len(states) {
		return ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.data
	oldSecret := bytes.Clone(d.Secret)
	for i, s := range states {
		if s.Core.Epoch <= d.HeadEpoch {
			if old, ok := d.States[s.Core.Epoch]; ok {
				h1, _ := old.Hash()
				h2, _ := s.Hash()
				if h1 != h2 {
					return ErrStale
				}
				continue
			}
			return ErrStale
		}
		prev := d.States[d.HeadEpoch]
		if err := VerifyTransition(prev, s, proofs[i]); err != nil {
			return err
		}
		if proofs[i] == nil && s.Core.ContentEpoch == prev.Core.ContentEpoch+1 {
			newSecret, err := d.Pin.OpenGrant(s, d.MemberID, d.XPrivate)
			if err != nil {
				return err
			}
			old, err := openPrevSecret(newSecret, s.PrevSecret)
			if err != nil || !bytes.Equal(old, oldSecret) {
				return ErrUntrusted
			}
		}
		if _, ok := s.Core.Member(d.MemberID); ok {
			secret, err := d.Pin.OpenGrant(s, d.MemberID, d.XPrivate)
			if err != nil {
				return err
			}
			if s.Core.ContentEpoch != prev.Core.ContentEpoch {
				d.ContentKeys[prev.Core.ContentEpoch] = oldSecret
				oldSecret = secret
			}
			d.Secret = secret
		}
		d.States[s.Core.Epoch] = s
		if proofs[i] != nil {
			d.Recovery[s.Core.Epoch] = *proofs[i]
		}
		d.HeadEpoch = s.Core.Epoch
	}
	if err := e.saveLocked(d); err != nil {
		return err
	}
	e.data = d
	return nil
}
func (e *Endpoint) saveLocked(d record) error {
	if e.path == "" {
		return nil
	}
	b, err := json.Marshal(d)
	if err != nil || len(b) > 32<<20 {
		return ErrCapacity
	}
	f, err := os.OpenFile(e.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return ErrStorage
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return ErrStorage
	}
	return nil
}

func (e *Endpoint) cacheChainKeys() error {
	d := e.data
	head := chainHead(d)
	for ep := head.Core.ContentEpoch; ep >= 1; ep-- {
		if key, ok := d.ContentKeys[ep]; ok && len(key) == 32 {
			continue
		}
		s, ok := stateWithContent(d, ep)
		if !ok {
			break
		}
		if s.Core.ContentEpoch == ep {
			key, err := d.Pin.OpenGrant(s, d.MemberID, d.XPrivate)
			if err == nil {
				d.ContentKeys[ep] = key
			}
		}
		if ep == 1 {
			break
		}
	}
	return nil
}
func stateWithContent(d record, content uint64) (State, bool) {
	for ep := d.HeadEpoch; ep >= 1; ep-- {
		if s, ok := d.States[ep]; ok && s.Core.ContentEpoch == content {
			return s, true
		}
	}
	return State{}, false
}

func (e *Endpoint) ContentSecret(epoch uint64) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.data
	if k, ok := d.ContentKeys[epoch]; ok && len(k) == 32 {
		return bytes.Clone(k), nil
	}
	head := chainHead(d)
	if head.Core.ContentEpoch < epoch {
		return nil, ErrStale
	}
	key := bytes.Clone(d.Secret)
	for ep := head.Core.Epoch; ep >= 1; ep-- {
		s, ok := d.States[ep]
		if !ok {
			return nil, ErrStorage
		}
		if s.Core.ContentEpoch < epoch {
			break
		}
		if s.Core.ContentEpoch == epoch {
			d.ContentKeys[epoch] = key
			return bytes.Clone(key), nil
		}
		prev, ok := d.States[ep-1]
		if !ok {
			return nil, ErrStorage
		}
		if prev.Core.ContentEpoch == s.Core.ContentEpoch {
			continue
		}
		if len(s.PrevSecret) == 0 {
			return nil, ErrUnavailable
		}
		old, err := openPrevSecret(key, s.PrevSecret)
		if err != nil {
			return nil, ErrUntrusted
		}
		key = old
	}
	if key, ok := d.ContentKeys[epoch]; ok {
		return bytes.Clone(key), nil
	}
	return nil, ErrUnavailable
}

func (e *Endpoint) Touch(now uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.data
	if now <= d.Floor {
		return nil
	}
	d.Floor = now
	if err := e.saveLocked(d); err != nil {
		return err
	}
	e.data = d
	return nil
}

func (e *Endpoint) Prepare(h Header, sessionSeed, plain []byte) (Ciphertext, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.data
	if d.Counter >= d.BlockEnd {
		if d.BlockEnd > ^uint64(0)-CounterBlock {
			return Ciphertext{}, ErrCapacity
		}
		d.BlockEnd += CounterBlock
		if err := e.saveLocked(d); err != nil {
			return Ciphertext{}, err
		}
	}
	counter := d.Counter + 1
	d.Counter = counter
	sh, _ := chainHead(d).Hash()
	h.Account = d.Pin.Account
	h.Domain = d.Pin.Domain
	h.Epoch = chainHead(d).Core.Epoch
	h.State = sh
	h.ContentEpoch = chainHead(d).Core.ContentEpoch
	h.Sender = d.MemberID
	h.Counter = counter
	if h.Scope(chainHead(d)) != nil {
		return Ciphertext{}, ErrUntrusted
	}
	c, err := Seal(d.Secret, sessionSeed, h, plain, ed25519.NewKeyFromSeed(d.EdSeed))
	if err != nil {
		return Ciphertext{}, err
	}
	if err := e.saveLocked(d); err != nil {
		return Ciphertext{}, err
	}
	e.data = d
	return c, nil
}

func (e *Endpoint) Open(w Wire, sessionSeed []byte, live bool, now uint64) (Header, []byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := ParseHeader(w.Header)
	if err != nil {
		return h, nil, err
	}
	s, ok := e.data.States[h.Epoch]
	if !ok {
		return h, nil, ErrUnknown
	}
	head := chainHead(e.data)
	secret, err := e.contentSecretLocked(h.ContentEpoch)
	if err != nil {
		return h, nil, err
	}
	h, plain, err := w.Open(secret, sessionSeed, s, head, live, maxU64(now, e.data.Floor))
	return h, plain, err
}

func (e *Endpoint) contentSecretLocked(epoch uint64) ([]byte, error) {
	d := e.data
	if k, ok := d.ContentKeys[epoch]; ok && len(k) == 32 {
		return bytes.Clone(k), nil
	}
	head := chainHead(d)
	if head.Core.ContentEpoch < epoch {
		return nil, ErrStale
	}
	key := bytes.Clone(d.Secret)
	for ep := head.Core.Epoch; ep >= 1; ep-- {
		s, ok := d.States[ep]
		if !ok {
			return nil, ErrStorage
		}
		if s.Core.ContentEpoch == epoch {
			return bytes.Clone(key), nil
		}
		prev, ok := d.States[ep-1]
		if !ok {
			return nil, ErrStorage
		}
		if prev.Core.ContentEpoch == s.Core.ContentEpoch {
			continue
		}
		if len(s.PrevSecret) == 0 {
			return nil, ErrUnavailable
		}
		old, err := openPrevSecret(key, s.PrevSecret)
		if err != nil {
			return nil, ErrUntrusted
		}
		key = old
	}
	return nil, ErrUnavailable
}

func (e *Endpoint) Accept(w Wire, sessionSeed []byte, now uint64, fn func(Header, []byte) ([]byte, error)) ([]byte, error) {
	e.mu.Lock()
	h, err := ParseHeader(w.Header)
	if err != nil {
		e.mu.Unlock()
		return nil, err
	}
	if !h.Purpose.Control() {
		e.mu.Unlock()
		return nil, ErrInvalid
	}
	now = maxU64(now, e.data.Floor)
	if h.Deadline <= now || h.Deadline-now > 10*60*1000 {
		e.mu.Unlock()
		return nil, ErrInvalid
	}
	if e.data.Floor < now {
		d := e.data
		d.Floor = now
		_ = e.saveLocked(d)
		e.data = d
	}
	key := h.Sender + "/" + string(rune(h.Purpose)) + "/" + h.Machine + "/" + h.Session + "/" + h.Request + "/" + itoa(h.Seq)
	if a, ok := e.data.Accepted[key]; ok && a.Done {
		e.mu.Unlock()
		if a.Failed {
			return nil, errors.New("security: previously accepted operation failed")
		}
		return a.Result, nil
	}
	e.mu.Unlock()

	out, callErr := func() ([]byte, error) {
		_, plain, err := e.Open(w, sessionSeed, true, now)
		if err != nil {
			return nil, err
		}
		return fn(h, plain)
	}()
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.data
	d.Accepted[key] = accepted{Deadline: h.Deadline, Done: true, Result: bytes.Clone(out), Failed: callErr != nil}
	if len(d.Accepted) > 8192 {
		return nil, ErrCapacity
	}
	if err := e.saveLocked(d); err != nil {
		return nil, err
	}
	e.data = d
	return out, callErr
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
