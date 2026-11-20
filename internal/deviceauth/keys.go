package deviceauth

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hpke"
	"crypto/rand"

	"github.com/Lewin671/remote-agent/shared/devicesec"
)

// The daemon holds its private keys in memory, so it completes every step
// shared/devicesec leaves to the caller synchronously: crypto/hpke for
// content grants, crypto/rand for nonces, ed25519.Sign for signatures.

type hpkeSender struct{}

func (hpkeSender) Seal(recipient, info, aad, plain []byte) (enc, ct []byte, err error) {
	pk, err := hpke.DHKEM(ecdh.X25519()).NewPublicKey(recipient)
	if err != nil {
		return nil, nil, devicesec.ErrInvalid
	}
	enc, s, err := hpke.NewSender(pk, hpke.HKDFSHA256(), hpke.AES256GCM(), info)
	if err != nil {
		return nil, nil, err
	}
	ct, err = s.Seal(aad, plain)
	return
}

// xRecipient opens grants with an X25519 private key.
type xRecipient []byte

func (x xRecipient) Public() []byte {
	pub, err := XPublic(x)
	if err != nil {
		return nil
	}
	return pub
}

func (x xRecipient) Open(enc, info, aad, ct []byte) ([]byte, error) {
	sk, err := hpke.DHKEM(ecdh.X25519()).NewPrivateKey(x)
	if err != nil {
		return nil, devicesec.ErrInvalid
	}
	r, err := hpke.NewRecipient(enc, sk, hpke.HKDFSHA256(), hpke.AES256GCM(), info)
	if err != nil {
		return nil, devicesec.ErrUntrusted
	}
	plain, err := r.Open(aad, ct)
	if err != nil {
		return nil, devicesec.ErrUntrusted
	}
	return plain, nil
}

// XPublic derives the public half of an X25519 private key.
func XPublic(private []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return nil, devicesec.ErrInvalid
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

// OpenGrant returns the content secret sealed to member id in s, opened with
// that member's X25519 private key.
func OpenGrant(pin devicesec.Pin, s devicesec.State, id string, xPrivate []byte) ([]byte, error) {
	return pin.OpenGrant(s, id, xRecipient(xPrivate))
}

func signedBy(signer ed25519.PrivateKey, public []byte) bool {
	return len(signer) == ed25519.PrivateKeySize && bytes.Equal(signer.Public().(ed25519.PublicKey), public)
}

func signState(s devicesec.State, err error, signer ed25519.PrivateKey) (devicesec.State, error) {
	if err != nil {
		return devicesec.State{}, err
	}
	b, err := s.SigningBytes()
	if err != nil {
		return devicesec.State{}, err
	}
	s.Signature = ed25519.Sign(signer, b)
	return s, nil
}

func BuildGenesis(account string, domain devicesec.Hash, member devicesec.Member, recoveryEd [32]byte, secret []byte, signer ed25519.PrivateKey, now uint64) (devicesec.State, error) {
	if !signedBy(signer, member.EdPublic[:]) {
		return devicesec.State{}, devicesec.ErrInvalid
	}
	s, err := devicesec.NewGenesis(account, domain, member, recoveryEd, secret, now, hpkeSender{})
	return signState(s, err, signer)
}

func BuildState(parent devicesec.State, members []devicesec.Member, removed []string, secret, prevSecret []byte, signer ed25519.PrivateKey, now uint64) (devicesec.State, error) {
	if len(signer) != ed25519.PrivateKeySize {
		return devicesec.State{}, devicesec.ErrInvalid
	}
	s, err := devicesec.NewState(parent, members, removed, secret, prevSecret, now, hpkeSender{})
	return signState(s, err, signer)
}

// BuildRecoveryKeyRotation changes only the emergency authority credential.
func BuildRecoveryKeyRotation(parent devicesec.State, secret []byte, recoveryEd [32]byte, signer ed25519.PrivateKey, now uint64) (devicesec.State, error) {
	if len(signer) != ed25519.PrivateKeySize {
		return devicesec.State{}, devicesec.ErrInvalid
	}
	s, err := devicesec.NewRecoveryKeyRotation(parent, secret, recoveryEd, now, hpkeSender{})
	return signState(s, err, signer)
}

// BuildMembershipState builds a normal state that changes the member list
// (add, remove, or X rotation) and seals the previous content key.
func BuildMembershipState(parent devicesec.State, members []devicesec.Member, removed []string, oldSecret, newSecret []byte, signer ed25519.PrivateKey, now uint64) (devicesec.State, error) {
	if len(signer) != ed25519.PrivateKeySize {
		return devicesec.State{}, devicesec.ErrInvalid
	}
	s, err := devicesec.NewMembershipState(parent, members, removed, oldSecret, newSecret, now, hpkeSender{}, rand.Reader)
	return signState(s, err, signer)
}

// BuildPairState adds one candidate. The caller (Android) has its own builder;
// this is the Go test/helper path.
func BuildPairState(parent devicesec.State, candidate devicesec.Member, candidateX []byte, oldSecret, secret []byte, signer ed25519.PrivateKey, now uint64) (devicesec.State, error) {
	if len(signer) != ed25519.PrivateKeySize {
		return devicesec.State{}, devicesec.ErrInvalid
	}
	s, err := devicesec.NewPairState(parent, candidate, candidateX, oldSecret, secret, now, hpkeSender{}, rand.Reader)
	return signState(s, err, signer)
}

// BuildHandoff is signed by the retiring authority; the incoming phone is an
// already paired member and has confirmed its new emergency code offline.
func BuildHandoff(parent devicesec.State, incoming string, recoveryEd [32]byte, oldSecret, newSecret []byte, signer ed25519.PrivateKey, now uint64) (devicesec.State, error) {
	if len(signer) != ed25519.PrivateKeySize {
		return devicesec.State{}, devicesec.ErrInvalid
	}
	s, err := devicesec.NewHandoff(parent, incoming, recoveryEd, oldSecret, newSecret, now, hpkeSender{}, rand.Reader)
	return signState(s, err, signer)
}

// BuildRecovery builds the recovery state, signed by the new authority, and
// its statement, signed by the parent's recovery key.
func BuildRecovery(parent devicesec.State, newMember devicesec.Member, members []devicesec.Member, secret []byte, newSigner ed25519.PrivateKey, oldRecovery ed25519.PrivateKey, newRecoveryEd [32]byte, now uint64) (devicesec.State, devicesec.RecoveryStatement, error) {
	var st devicesec.RecoveryStatement
	if !signedBy(newSigner, newMember.EdPublic[:]) {
		return devicesec.State{}, st, devicesec.ErrInvalid
	}
	s, err := devicesec.NewRecovery(parent, newMember, members, secret, newRecoveryEd, now, hpkeSender{})
	if s, err = signState(s, err, newSigner); err != nil {
		return devicesec.State{}, st, err
	}
	if st, err = devicesec.NewRecoveryStatement(parent, s, rand.Reader); err != nil {
		return devicesec.State{}, st, err
	}
	if err := SignRecoveryStatement(&st, oldRecovery); err != nil {
		return devicesec.State{}, st, err
	}
	return s, st, nil
}

// SignRecoveryStatement signs r with the parent's recovery key.
func SignRecoveryStatement(r *devicesec.RecoveryStatement, oldRecovery ed25519.PrivateKey) error {
	b, err := r.SigningBytes()
	if err != nil || len(oldRecovery) != ed25519.PrivateKeySize {
		return devicesec.ErrInvalid
	}
	r.Signature = ed25519.Sign(oldRecovery, b)
	return nil
}

// BuildLease issues head's lease, signed by head's authority.
func BuildLease(head devicesec.State, signer ed25519.PrivateKey, now uint64) (devicesec.LeaseCertificate, error) {
	m, ok := head.Core.Member(head.Core.AuthorityID)
	if !ok || !signedBy(signer, m.EdPublic[:]) {
		return devicesec.LeaseCertificate{}, devicesec.ErrUntrusted
	}
	l, err := devicesec.NewLease(head, now)
	if err != nil {
		return l, err
	}
	b, _ := l.SigningBytes()
	l.Signature = ed25519.Sign(signer, b)
	return l, nil
}

// Seal encrypts plain for h and signs the origin as h.Sender.
func Seal(secret, sessionSeed []byte, h devicesec.Header, plain []byte, writer ed25519.PrivateKey) (devicesec.Ciphertext, error) {
	if len(writer) != ed25519.PrivateKeySize || devicesec.ID(writer.Public().(ed25519.PublicKey)) != h.Sender {
		return devicesec.Ciphertext{}, devicesec.ErrInvalid
	}
	c, err := devicesec.NewCiphertext(secret, sessionSeed, h, plain, rand.Reader)
	if err != nil {
		return c, err
	}
	b, err := c.OriginBytes()
	if err != nil {
		return c, err
	}
	c.Origin = ed25519.Sign(writer, b)
	return c, nil
}

// OpenEnvelope verifies and decrypts like Ciphertext.Open, then checks the
// plaintext is a well-formed body for the header.
func OpenEnvelope(c devicesec.Ciphertext, secret, sessionSeed []byte, s, head devicesec.State, live bool, now uint64) (devicesec.Header, []byte, error) {
	h, plain, err := c.Open(secret, sessionSeed, s, head, live, now)
	if err != nil {
		return h, nil, err
	}
	if err := ValidateBody(h, plain); err != nil {
		return h, nil, err
	}
	return h, plain, nil
}
