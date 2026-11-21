package devicesec

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"io"
	"strings"
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

// Kind is the payload kind a header with this purpose must carry.
func (p Purpose) Kind() uint32 {
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
}

func (h Header) validate() error {
	if !deviceID(h.Account) || h.Domain == (Hash{}) || h.Epoch == 0 || h.State == (Hash{}) || h.ContentEpoch == 0 || h.ContentEpoch > h.Epoch || !deviceID(h.Sender) || h.Kind == 0 || h.Kind != h.Purpose.Kind() || h.Compression > 1 || h.Machine != "" && !deviceID(h.Machine) || h.Session != "" && !validID(h.Session) || h.Request != "" && !validID(h.Request) {
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
	case Image:
		if h.Machine == "" || h.Session == "" || h.Request == "" {
			return ErrInvalid
		}
	case Notify:
		if h.Machine == "" || h.Request == "" || h.Session == "" && !strings.HasPrefix(h.Request, "disk:") {
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
	e := Encoder{}
	e.U32(Version)
	e.Str(h.Account)
	e.Raw(h.Domain[:])
	e.U64(h.Epoch)
	e.Raw(h.State[:])
	e.U64(h.ContentEpoch)
	e.Str(h.Sender)
	e.U32(h.Kind)
	e.U32(uint32(h.Purpose))
	e.Str(h.Machine)
	e.Str(h.Session)
	e.Str(h.Request)
	e.U64(h.Seq)
	e.U64(h.Deadline)
	e.U32(h.Compression)
	return e.B, nil
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

func DeriveKey(secret, sessionSeed []byte, h Header) ([]byte, error) {
	purposeKey, err := DerivePurposeKey(secret, h)
	if err != nil {
		return nil, err
	}
	return DeriveKeyFromPurposeKey(purposeKey, sessionSeed, h)
}

func DeriveKeyFromPurposeKey(purposeKey, sessionSeed []byte, h Header) ([]byte, error) {
	if len(purposeKey) != 32 || h.validate() != nil {
		return nil, ErrInvalid
	}
	var salt []byte
	if h.Purpose.sessionKey() {
		if len(sessionSeed) != 32 {
			return nil, ErrInvalid
		}
		salt = sessionSeed
	}
	e := Encoder{B: []byte("ra3/content-key\x00")}
	e.Str(h.Account)
	e.Raw(h.Domain[:])
	e.U64(h.Epoch)
	e.Raw(h.State[:])
	e.U64(h.ContentEpoch)
	e.U32(uint32(h.Purpose))
	e.Str(h.Sender)
	e.Str(h.Machine)
	e.Str(h.Session)
	e.Str(h.Request)
	e.U64(h.Seq)
	return hkdf.Key(sha256.New, purposeKey, salt, string(e.B), 32)
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
	e := Encoder{}
	e.LP(c.Header)
	e.LP(c.Nonce)
	e.LP(c.Cipher)
	return Digest("ra3/ciphertext\x00", e.B), nil
}
func originBytes(h Header, hh, commit Hash) []byte {
	e := Encoder{B: []byte("ra3/cipher-origin\x00")}
	e.Str(h.Account)
	e.Raw(h.Domain[:])
	e.Raw(h.State[:])
	e.Str(h.Sender)
	e.Raw(hh[:])
	e.Raw(commit[:])
	return e.B
}
// OriginVerifyInput checks header validity, ScopeAt(s, head, live, now), and
// ciphertext commitment, returning the parsed header, sender Ed25519 public key,
// and exact origin message bytes to verify without running Ed25519 in Go.
func (c Ciphertext) OriginVerifyInput(s State, head State, live bool, now uint64) (Header, [32]byte, []byte, error) {
	h, err := ParseHeader(c.Header)
	if err != nil {
		return h, [32]byte{}, nil, err
	}
	if err := h.ScopeAt(s, head, live, now); err != nil {
		return h, [32]byte{}, nil, err
	}
	commit, err := c.Commitment()
	if err != nil {
		return h, [32]byte{}, nil, err
	}
	m, _ := s.Core.Member(h.Sender)
	hh := Digest("", c.Header)
	return h, m.EdPublic, originBytes(h, hh, commit), nil
}

func (c Ciphertext) verifyOrigin(s State, head State, live bool, now uint64) (Header, Hash, error) {
	h, edPub, msg, err := c.OriginVerifyInput(s, head, live, now)
	if err != nil {
		return h, Hash{}, err
	}
	commit, _ := c.Commitment()
	if !ed25519.Verify(edPub[:], msg, c.Origin) {
		return h, commit, ErrUntrusted
	}
	return h, commit, nil
}

// ScopeAt is the live authority recheck for already authenticated bytes.
func (h Header) ScopeAt(s State, head State, live bool, now uint64) error {
	if err := h.Scope(s); err != nil {
		return err
	}
	if live {
		if _, ok := head.Core.Member(h.Sender); !ok {
			return ErrUntrusted
		}
		if h.Purpose.Control() {
			if head.Core.IssuedAt > now+MaxClockSkew || head.Core.ContentEpoch != h.ContentEpoch {
				return ErrStale
			}
		}
	}
	return nil
}

// PrepareCiphertext marshals h, derives the 32-byte AES-GCM key, and reads a
// 12-byte nonce from random for callers that seal the AEAD externally (e.g. WebCrypto).
func PrepareCiphertext(secret, sessionSeed []byte, h Header, plainLen int, random io.Reader) (headerBytes, key, nonce []byte, err error) {
	if plainLen < 0 || plainLen > MaxCipherBytes-16 {
		return nil, nil, nil, ErrInvalid
	}
	headerBytes, err = h.MarshalBinary()
	if err != nil {
		return nil, nil, nil, err
	}
	key, err = DeriveKey(secret, sessionSeed, h)
	if err != nil {
		return nil, nil, nil, err
	}
	nonce = make([]byte, 12)
	if _, err = io.ReadFull(random, nonce); err != nil {
		return nil, nil, nil, err
	}
	return headerBytes, key, nonce, nil
}

// AssembleCiphertext wraps externally sealed AES-GCM ciphertext into a Ciphertext
// and returns the canonical OriginBytes to sign.
func AssembleCiphertext(headerBytes, nonce, cipherBytes []byte) (Ciphertext, []byte, error) {
	c := Ciphertext{Header: headerBytes, Nonce: nonce, Cipher: cipherBytes}
	msg, err := c.OriginBytes()
	if err != nil {
		return Ciphertext{}, nil, err
	}
	return c, msg, nil
}

// NewCiphertext encrypts plain under the key derived for h, with a 12-byte
// nonce read from random. Origin is left empty: the sender named in h signs
// OriginBytes.
func NewCiphertext(secret, sessionSeed []byte, h Header, plain []byte, random io.Reader) (c Ciphertext, err error) {
	headerBytes, key, nonce, err := PrepareCiphertext(secret, sessionSeed, h, len(plain), random)
	if err != nil {
		return c, err
	}
	aead, err := gcm(key)
	if err != nil {
		return c, err
	}
	c.Header = headerBytes
	c.Nonce = nonce
	c.Cipher = aead.Seal(nil, c.Nonce, plain, c.Header)
	return c, nil
}

// OriginBytes is what the sender named in the header signs into Origin.
func (c Ciphertext) OriginBytes() ([]byte, error) {
	commit, err := c.Commitment()
	if err != nil {
		return nil, err
	}
	h, err := ParseHeader(c.Header)
	if err != nil {
		return nil, err
	}
	return originBytes(h, Digest("", c.Header), commit), nil
}

// DeriveCipherKey validates c's header and commitment and returns the derived
// 32-byte AES-GCM key so external AEAD implementations (e.g. WebCrypto) can open c.
func DeriveCipherKey(secret, sessionSeed []byte, c Ciphertext) (Header, []byte, error) {
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
	return h, key, nil
}

// OpenCipher decrypts c without re-checking origin (use after VerifyOrigin or external Ed25519 verify).
func OpenCipher(secret, sessionSeed []byte, c Ciphertext) (Header, []byte, error) {
	return openCipher(secret, sessionSeed, c)
}

// OpenCipherWithPurposeKey decrypts c using a pre-derived purpose key without re-checking origin.
func OpenCipherWithPurposeKey(purposeKey, sessionSeed []byte, c Ciphertext) (Header, []byte, error) {
	h, err := ParseHeader(c.Header)
	if err != nil {
		return h, nil, err
	}
	if _, err := c.Commitment(); err != nil {
		return h, nil, err
	}
	key, err := DeriveKeyFromPurposeKey(purposeKey, sessionSeed, h)
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

func openCipher(secret, sessionSeed []byte, c Ciphertext) (Header, []byte, error) {
	h, key, err := DeriveCipherKey(secret, sessionSeed, c)
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

// Open only authenticates and decrypts: it verifies the origin like
// VerifyOrigin and opens the AEAD. It does NOT validate the body. The
// plaintext may still be a payload of the wrong type for the header, carry
// unknown fields, or not match the header's routing, so the caller must check
// the body against the header before using the plaintext in any way. That
// check needs the protobuf message types, which this package does not have:
// the daemon uses deviceauth.OpenEnvelope (Open followed by
// deviceauth.ValidateBody); a client core must apply its own equivalent.
func (c Ciphertext) Open(secret, sessionSeed []byte, s State, head State, live bool, now uint64) (Header, []byte, error) {
	h, err := c.VerifyOrigin(s, head, live, now)
	if err != nil {
		return h, nil, err
	}
	return openCipher(secret, sessionSeed, c)
}
func (c Ciphertext) MarshalBinary() ([]byte, error) {
	if _, err := c.Commitment(); err != nil || len(c.Origin) != 64 {
		return nil, ErrInvalid
	}
	e := Encoder{}
	e.LP(c.Header)
	e.LP(c.Nonce)
	e.LP(c.Cipher)
	e.LP(c.Origin)
	return e.B, nil
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
func DerivePurposeKeyRaw(secret []byte, account string, domain Hash, contentEpoch uint64, purpose Purpose) ([]byte, error) {
	if len(secret) != 32 || !deviceID(account) || domain == (Hash{}) || contentEpoch == 0 || purpose.Kind() == 0 {
		return nil, ErrInvalid
	}
	e := Encoder{B: []byte("ra3/purpose-key\x00")}
	e.Str(account)
	e.Raw(domain[:])
	e.U64(contentEpoch)
	e.U32(uint32(purpose))
	return hkdf.Key(sha256.New, secret, nil, string(e.B), 32)
}

func DerivePurposeKey(secret []byte, h Header) ([]byte, error) {
	if err := h.validate(); err != nil {
		return nil, ErrInvalid
	}
	return DerivePurposeKeyRaw(secret, h.Account, h.Domain, h.ContentEpoch, h.Purpose)
}
