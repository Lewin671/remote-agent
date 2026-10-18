package deviceauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
)

// Purpose is separate from the protobuf payload kind.
type Purpose uint32

const (
	Event           Purpose = 1
	SessionMeta     Purpose = 2
	SessionKey      Purpose = 3
	MachineMeta     Purpose = 4
	Command         Purpose = 5
	Result          Purpose = 6
	TerminalOutput  Purpose = 7
	TerminalInput   Purpose = 8
	Notify          Purpose = 10
	Image           Purpose = 11
	Prefs           Purpose = 12
	DeviceMeta      Purpose = 13
	TerminalResize  Purpose = 14
	TerminalHistory Purpose = 15
)

func (p Purpose) kind() uint32 {
	switch p {
	case TerminalResize, TerminalHistory:
		return 8
	case Event, SessionMeta, SessionKey, MachineMeta, Command, Result, TerminalOutput, TerminalInput, Notify, Image, Prefs, DeviceMeta:
		return uint32(p)
	}
	return 0
}
func (p Purpose) Mutable() bool {
	return p == SessionMeta || p == MachineMeta || p == DeviceMeta || p == Prefs
}
func (p Purpose) Control() bool {
	return p == Command || p == TerminalInput || p == TerminalResize || p == TerminalHistory
}
func (p Purpose) sessionKey() bool {
	return p == Event || p == Image || p == TerminalOutput || p == TerminalInput || p == TerminalResize || p == TerminalHistory
}

type Header struct {
	Account      string
	Domain       Hash
	Epoch        uint64
	State        Hash
	ContentEpoch uint64
	Sender       string
	Kind         uint32
	Purpose      Purpose
	Machine      string
	Session      string
	Request      string
	Seq          uint64
	Deadline     uint64
	Compression  uint32
	Counter      uint64
}

func (h Header) validate() error {
	if !deviceID(h.Account) || h.Domain == (Hash{}) || h.Epoch == 0 || h.State == (Hash{}) || h.ContentEpoch == 0 || h.ContentEpoch > h.Epoch || !deviceID(h.Sender) || h.Kind == 0 || h.Kind != h.Purpose.kind() || h.Compression > 1 || h.Counter == 0 || h.Machine != "" && !deviceID(h.Machine) || h.Session != "" && !validID(h.Session) || h.Request != "" && !validID(h.Request) {
		return ErrInvalid
	}
	switch h.Purpose {
	case Prefs:
		if h.Request != "project-prefs" || h.Machine != "" || h.Session != "" || h.Seq == 0 {
			return ErrInvalid
		}
	case DeviceMeta:
		if !deviceID(h.Request) || h.Machine != "" || h.Session != "" || h.Seq == 0 {
			return ErrInvalid
		}
	case MachineMeta:
		if h.Machine == "" || h.Session != "" || h.Request != "" || h.Seq == 0 {
			return ErrInvalid
		}
	case SessionMeta, SessionKey, Event:
		if h.Machine == "" || h.Session == "" || h.Request != "" || h.Seq == 0 {
			return ErrInvalid
		}
	case Command, Result:
		if h.Machine == "" || len(h.Request) < 16 {
			return ErrInvalid
		}
	case TerminalInput, TerminalResize, TerminalHistory, TerminalOutput:
		if h.Machine == "" || h.Session == "" || len(h.Request) < 16 || h.Seq == 0 {
			return ErrInvalid
		}
	case Image, Notify:
		if h.Machine == "" || h.Session == "" || h.Request == "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if h.Purpose.Control() && h.Deadline == 0 {
		return ErrInvalid
	}
	return nil
}
func (h Header) MarshalBinary() ([]byte, error) {
	if err := h.validate(); err != nil {
		return nil, err
	}
	e := encoder{}
	e.u32(Version)
	e.str(h.Account)
	e.raw(h.Domain[:])
	e.u64(h.Epoch)
	e.raw(h.State[:])
	e.u64(h.ContentEpoch)
	e.str(h.Sender)
	e.u32(h.Kind)
	e.u32(uint32(h.Purpose))
	e.str(h.Machine)
	e.str(h.Session)
	e.str(h.Request)
	e.u64(h.Seq)
	e.u64(h.Deadline)
	e.u32(h.Compression)
	e.u64(h.Counter)
	return e.b, nil
}
func ParseHeader(b []byte) (h Header, err error) {
	if len(b) > MaxHeaderBytes {
		return h, ErrInvalid
	}
	d := decoder{b: b}
	if d.u32() != Version {
		return h, ErrInvalid
	}
	h.Account = d.str()
	h.Domain = d.hash()
	h.Epoch = d.u64()
	h.State = d.hash()
	h.ContentEpoch = d.u64()
	h.Sender = d.str()
	h.Kind = d.u32()
	h.Purpose = Purpose(d.u32())
	h.Machine = d.str()
	h.Session = d.str()
	h.Request = d.str()
	h.Seq = d.u64()
	h.Deadline = d.u64()
	h.Compression = d.u32()
	h.Counter = d.u64()
	if d.done() != nil {
		return h, ErrInvalid
	}
	return h, h.validate()
}

// Scope checks ownership from the state the sender claims.
func (h Header) Scope(s State) error {
	sh, err := s.Hash()
	if err != nil {
		return err
	}
	if h.Account != s.Core.Account || h.Domain != s.Core.Domain || h.State != sh || h.Epoch != s.Core.Epoch || h.ContentEpoch != s.Core.ContentEpoch {
		return ErrStale
	}
	writer, ok := s.Core.Member(h.Sender)
	if !ok {
		return ErrUntrusted
	}
	if h.Machine != "" {
		m, ok := s.Core.Member(h.Machine)
		if !ok || m.Kind != Machine {
			return ErrUntrusted
		}
	}
	switch h.Purpose {
	case DeviceMeta:
		if h.Sender != h.Request {
			return ErrUntrusted
		}
		m, ok := s.Core.Member(h.Request)
		if !ok || m.Kind != Client {
			return ErrUntrusted
		}
	case Prefs:
		if writer.Kind != Client {
			return ErrUntrusted
		}
	case Command, TerminalInput, TerminalResize, TerminalHistory:
		if writer.Kind != Client {
			return ErrUntrusted
		}
	default:
		if writer.Kind != Machine || writer.ID != h.Machine {
			return ErrUntrusted
		}
	}
	return nil
}

func (h Header) objectKey() string {
	e := encoder{}
	e.u32(uint32(h.Purpose))
	e.str(h.Machine)
	e.str(h.Session)
	if h.Purpose == DeviceMeta || h.Purpose == Prefs {
		e.str(h.Request)
	}
	return string(e.b)
}

func DeriveKey(secret, sessionSeed []byte, h Header) ([]byte, error) {
	if len(secret) != 32 || h.validate() != nil {
		return nil, ErrInvalid
	}
	var salt []byte
	if h.Purpose.sessionKey() {
		if len(sessionSeed) != 32 {
			return nil, ErrInvalid
		}
		salt = sessionSeed
	}
	e := encoder{b: []byte("ra3/content-key\x00")}
	e.str(h.Account)
	e.raw(h.Domain[:])
	e.u64(h.Epoch)
	e.raw(h.State[:])
	e.u64(h.ContentEpoch)
	e.u32(uint32(h.Purpose))
	e.str(h.Sender)
	e.str(h.Machine)
	e.str(h.Session)
	e.str(h.Request)
	e.u64(h.Seq)
	e.u64(h.Counter)
	purposeKey, err := DerivePurposeKey(secret, h)
	if err != nil {
		return nil, err
	}
	return hkdf.Key(sha256.New, purposeKey, salt, string(e.b), 32)
}
func gcm(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, ErrInvalid
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

type Ciphertext struct {
	Header []byte
	Nonce  []byte
	Cipher []byte
	Origin []byte
}

type Wire = Ciphertext

func (c Ciphertext) Commitment() (Hash, error) {
	if len(c.Header) > MaxHeaderBytes || len(c.Nonce) != 12 || len(c.Cipher) < 16 || len(c.Cipher) > MaxCipherBytes {
		return Hash{}, ErrInvalid
	}
	if _, err := ParseHeader(c.Header); err != nil {
		return Hash{}, err
	}
	e := encoder{}
	e.lp(c.Header)
	e.lp(c.Nonce)
	e.lp(c.Cipher)
	return digest("ra3/ciphertext\x00", e.b), nil
}
func originBytes(h Header, hh, commit Hash) []byte {
	e := encoder{b: []byte("ra3/cipher-origin\x00")}
	e.str(h.Account)
	e.raw(h.Domain[:])
	e.raw(h.State[:])
	e.str(h.Sender)
	e.raw(hh[:])
	e.raw(commit[:])
	return e.b
}
func (c Ciphertext) verifyOrigin(s State, head State, live bool, now uint64) (Header, Hash, error) {
	h, err := ParseHeader(c.Header)
	if err != nil {
		return h, Hash{}, err
	}
	if err := h.Scope(s); err != nil {
		return h, Hash{}, err
	}
	if live {
		if _, ok := head.Core.Member(h.Sender); !ok {
			return h, Hash{}, ErrUntrusted
		}
		if h.Purpose.Control() {
			if head.Core.NotAfter < now || head.Core.IssuedAt > now+MaxClockSkew || head.Core.ContentEpoch != h.ContentEpoch {
				return h, Hash{}, ErrStale
			}
		}
	}
	commit, err := c.Commitment()
	if err != nil {
		return h, commit, err
	}
	m, _ := s.Core.Member(h.Sender)
	hh := digest("", c.Header)
	if !ed25519.Verify(m.EdPublic[:], originBytes(h, hh, commit), c.Origin) {
		return h, commit, ErrUntrusted
	}
	return h, commit, nil
}
func Seal(secret, sessionSeed []byte, h Header, plain []byte, writer ed25519.PrivateKey) (c Ciphertext, err error) {
	if len(plain) > MaxCipherBytes-16 || len(writer) != 64 || ID(writer.Public().(ed25519.PublicKey)) != h.Sender {
		return c, ErrInvalid
	}
	c.Header, err = h.MarshalBinary()
	if err != nil {
		return c, err
	}
	key, err := DeriveKey(secret, sessionSeed, h)
	if err != nil {
		return c, err
	}
	aead, err := gcm(key)
	if err != nil {
		return c, err
	}
	c.Nonce = make([]byte, 12)
	if _, err = rand.Read(c.Nonce); err != nil {
		return c, err
	}
	c.Cipher = aead.Seal(nil, c.Nonce, plain, c.Header)
	commit, _ := c.Commitment()
	c.Origin = ed25519.Sign(writer, originBytes(h, digest("", c.Header), commit))
	return c, nil
}
func openCipher(secret, sessionSeed []byte, c Ciphertext) (Header, []byte, error) {
	h, err := ParseHeader(c.Header)
	if err != nil {
		return h, nil, err
	}
	if _, err := c.Commitment(); err != nil {
		return h, nil, err
	}
	key, err := DeriveKey(secret, sessionSeed, h)
	if err != nil {
		return h, nil, err
	}
	aead, err := gcm(key)
	if err != nil {
		return h, nil, err
	}
	plain, err := aead.Open(nil, c.Nonce, c.Cipher, c.Header)
	if err != nil {
		return h, nil, ErrUntrusted
	}
	return h, plain, nil
}

// VerifyOrigin checks the sender state and, for live traffic, that the sender is
// still in the current head. History is verified with head = s and live=false.
func (c Ciphertext) VerifyOrigin(s State, head State, live bool, now uint64) (Header, error) {
	h, _, err := c.verifyOrigin(s, head, live, now)
	return h, err
}
func (c Ciphertext) Open(secret, sessionSeed []byte, s State, head State, live bool, now uint64) (Header, []byte, error) {
	h, err := c.VerifyOrigin(s, head, live, now)
	if err != nil {
		return h, nil, err
	}
	h, plain, err := openCipher(secret, sessionSeed, c)
	if err != nil {
		return h, nil, err
	}
	if err := ValidateBody(h, plain); err != nil {
		return h, nil, err
	}
	return h, plain, nil
}
func (c Ciphertext) MarshalBinary() ([]byte, error) {
	if _, err := c.Commitment(); err != nil || len(c.Origin) != 64 {
		return nil, ErrInvalid
	}
	e := encoder{}
	e.lp(c.Header)
	e.lp(c.Nonce)
	e.lp(c.Cipher)
	e.lp(c.Origin)
	return e.b, nil
}
func ParseWire(b []byte) (w Wire, err error) {
	if len(b) > MaxCipherBytes+4096 {
		return w, ErrInvalid
	}
	d := decoder{b: b}
	w.Header = d.lp(MaxHeaderBytes)
	w.Nonce = d.lp(12)
	w.Cipher = d.lp(MaxCipherBytes)
	w.Origin = d.lp(64)
	if d.done() != nil {
		return w, ErrInvalid
	}
	_, err = w.MarshalBinary()
	return w, err
}
func randomHash() (h Hash, err error) { _, err = rand.Read(h[:]); return }

func DerivePurposeKey(secret []byte, h Header) ([]byte, error) {
	if len(secret) != 32 || h.validate() != nil {
		return nil, ErrInvalid
	}
	e := encoder{b: []byte("ra3/purpose-key\x00")}
	e.str(h.Account)
	e.raw(h.Domain[:])
	e.u64(h.ContentEpoch)
	e.u32(uint32(h.Purpose))
	return hkdf.Key(sha256.New, secret, nil, string(e.b), 32)
}
