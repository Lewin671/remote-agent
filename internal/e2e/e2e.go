// Package e2e implements the end-to-end encryption shared by the daemon and
// the clients: key derivation from the account root key, sealing and
// opening envelopes, and device signatures.
//
// Only standard primitives are used: HKDF-SHA256, AES-256-GCM and Ed25519.
// The web client implements the same scheme with WebCrypto; the test
// vectors in testdata keep the two in step.
package e2e

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// Version is the envelope format version in Header.version.
const Version = 1

// Signature contexts. Each signed message starts with its context so that a
// signature for one purpose never verifies for another.
const (
	RegisterContext = "remote-agent/v1/register\x00"
	LoginContext    = "remote-agent/v1/login\x00"
)

// KeySize is the size of the root key and of every derived key.
const KeySize = 32

// Keys are the keys derived from an account's root key. Each has one
// purpose, fixed by its HKDF info string.
type Keys struct {
	// Account signs device registrations.
	Account ed25519.PrivateKey
	// Wrap seals session keys.
	Wrap []byte
	// Meta seals machine metadata.
	Meta []byte
	// Command seals commands and their results.
	Command []byte
}

// Derive computes the keys of the account with root key root.
func Derive(root []byte) (*Keys, error) {
	if len(root) != KeySize {
		return nil, fmt.Errorf("root key must be %d bytes", KeySize)
	}
	k := &Keys{}
	derive := func(info string) ([]byte, error) {
		return hkdf.Key(sha256.New, root, nil, "remote-agent/v1/"+info, KeySize)
	}
	seed, err := derive("account-sign")
	if err != nil {
		return nil, err
	}
	k.Account = ed25519.NewKeyFromSeed(seed)
	if k.Wrap, err = derive("wrap"); err != nil {
		return nil, err
	}
	if k.Meta, err = derive("machine-meta"); err != nil {
		return nil, err
	}
	if k.Command, err = derive("command"); err != nil {
		return nil, err
	}
	return k, nil
}

// AccountPublicKey returns the public half of the account key.
func (k *Keys) AccountPublicKey() ed25519.PublicKey {
	return k.Account.Public().(ed25519.PublicKey)
}

// NewKey returns a random key, used for root and session keys.
func NewKey() []byte {
	b := make([]byte, KeySize)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// DeviceID identifies a device by its Ed25519 public key.
func DeviceID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:16])
}

// Seal encrypts plaintext under key, authenticating h as additional data.
func Seal(key []byte, h *pb.Header, plaintext []byte) (*pb.Envelope, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return sealWithNonce(key, h, plaintext, nonce)
}

func sealWithNonce(key []byte, h *pb.Header, plaintext, nonce []byte) (*pb.Envelope, error) {
	if h.Version == 0 {
		h = proto.CloneOf(h)
		h.Version = Version
	}
	header, err := proto.MarshalOptions{Deterministic: true}.Marshal(h)
	if err != nil {
		return nil, err
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return &pb.Envelope{Header: header, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plaintext, header)}, nil
}

// SealMessage marshals m and seals it.
func SealMessage(key []byte, h *pb.Header, m proto.Message) (*pb.Envelope, error) {
	b, err := proto.Marshal(m)
	if err != nil {
		return nil, err
	}
	return Seal(key, h, b)
}

// Errors from Open. They never include content.
var (
	ErrOpen    = errors.New("e2e: envelope does not open")
	ErrVersion = errors.New("e2e: unsupported envelope version")
	ErrPlace   = errors.New("e2e: envelope header does not match where it was found")
)

// Open authenticates and decrypts env. It returns the header exactly as
// sealed; the caller must check it with Expect.
func Open(key []byte, env *pb.Envelope) (*pb.Header, []byte, error) {
	if env == nil || len(env.Nonce) != 12 {
		return nil, nil, ErrOpen
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	plaintext, err := aead.Open(nil, env.Nonce, env.Ciphertext, env.Header)
	if err != nil {
		return nil, nil, ErrOpen
	}
	h := &pb.Header{}
	if err := proto.Unmarshal(env.Header, h); err != nil {
		return nil, nil, ErrOpen
	}
	if h.Version != Version {
		return nil, nil, ErrVersion
	}
	return h, plaintext, nil
}

// OpenMessage opens env into m and checks its header against want.
func OpenMessage(key []byte, env *pb.Envelope, want *pb.Header, m proto.Message) (*pb.Header, error) {
	h, b, err := Open(key, env)
	if err != nil {
		return nil, err
	}
	if err := Expect(h, want); err != nil {
		return nil, err
	}
	if err := proto.Unmarshal(b, m); err != nil {
		return nil, ErrOpen
	}
	return h, nil
}

// Expect checks that h has every field set in want. Zero fields in want
// are not checked, except kind, which must always match.
func Expect(h, want *pb.Header) error {
	switch {
	case h.Kind != want.Kind,
		want.MachineId != "" && h.MachineId != want.MachineId,
		want.SessionId != "" && h.SessionId != want.SessionId,
		want.Seq != 0 && h.Seq != want.Seq,
		want.RequestId != "" && h.RequestId != want.RequestId,
		want.Sender != "" && h.Sender != want.Sender:
		return ErrPlace
	}
	return nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Secret is what a new device needs to join the account: the root key and
// the server. Its text form is "ra1.<root key>.<server URL>", both parts
// base64url without padding. It grants full access and must be handled
// like a password.
type Secret struct {
	Root   []byte
	Server string
}

const secretPrefix = "ra1."

func (s Secret) String() string {
	enc := base64.RawURLEncoding
	return secretPrefix + enc.EncodeToString(s.Root) + "." + enc.EncodeToString([]byte(s.Server))
}

// ParseSecret parses the text form of a Secret.
func ParseSecret(text string) (Secret, error) {
	bad := errors.New("not a remote-agent account secret")
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), secretPrefix)
	if !ok {
		return Secret{}, bad
	}
	a, b, ok := strings.Cut(rest, ".")
	if !ok {
		return Secret{}, bad
	}
	enc := base64.RawURLEncoding
	root, err1 := enc.DecodeString(a)
	server, err2 := enc.DecodeString(b)
	if err1 != nil || err2 != nil || len(root) != KeySize || len(server) == 0 {
		return Secret{}, bad
	}
	return Secret{Root: root, Server: string(server)}, nil
}

// SignRegistration signs a serialized DeviceRegistration with the account key.
func (k *Keys) SignRegistration(registration []byte) []byte {
	return ed25519.Sign(k.Account, append([]byte(RegisterContext), registration...))
}

// VerifyRegistration checks an account signature over a registration.
func VerifyRegistration(account ed25519.PublicKey, registration, sig []byte) bool {
	return len(account) == ed25519.PublicKeySize &&
		ed25519.Verify(account, append([]byte(RegisterContext), registration...), sig)
}

// SignLogin signs a login challenge with a device key.
func SignLogin(device ed25519.PrivateKey, challenge []byte) []byte {
	return ed25519.Sign(device, append([]byte(LoginContext), challenge...))
}

// VerifyLogin checks a device signature over a login challenge.
func VerifyLogin(device ed25519.PublicKey, challenge, sig []byte) bool {
	return len(device) == ed25519.PublicKeySize &&
		ed25519.Verify(device, append([]byte(LoginContext), challenge...), sig)
}
