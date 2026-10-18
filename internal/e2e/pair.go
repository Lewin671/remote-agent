package e2e

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// Pairing: an existing device (the initiator) hands the root key to a new
// device (the joiner) over the untrusted server. Both make an X25519 key
// pair. The initiator commits to its public key first; the joiner sends
// its public key; then the initiator reveals its key. Because the
// initiator's key is fixed before the joiner's is seen, a server that
// swaps keys cannot search for a pair whose short authentication strings
// match. Both devices show four emoji derived from the shared secret and
// the whole exchange; the user checks they are the same before the
// initiator sends the root key, sealed with a key from the same secret.

const (
	pairCommitContext     = "remote-agent/v1/pair-commit\x00"
	pairTranscriptContext = "remote-agent/v1/pair\x00"
)

// PairKey is one side's ephemeral key pair.
type PairKey struct{ priv *ecdh.PrivateKey }

// NewPairKey makes a fresh X25519 key pair.
func NewPairKey() (*PairKey, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &PairKey{priv: k}, nil
}

// pairKeyFrom makes a key pair from fixed bytes, for test vectors.
func pairKeyFrom(b []byte) (*PairKey, error) {
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return nil, err
	}
	return &PairKey{priv: k}, nil
}

// Public is the public key to send.
func (k *PairKey) Public() []byte { return k.priv.PublicKey().Bytes() }

// PairCommit is the initiator's commitment to its public key.
func PairCommit(initiatorPublic []byte) []byte {
	sum := sha256.Sum256(append([]byte(pairCommitContext), initiatorPublic...))
	return sum[:]
}

// PairTranscript binds everything both sides agreed on.
func PairTranscript(pairingID string, initiatorPublic, joinerPublic []byte, joinerKind int32) []byte {
	h := sha256.New()
	h.Write([]byte(pairTranscriptContext))
	for _, part := range [][]byte{[]byte(pairingID), initiatorPublic, joinerPublic} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	var kind [4]byte
	binary.BigEndian.PutUint32(kind[:], uint32(joinerKind))
	h.Write(kind[:])
	return h.Sum(nil)
}

// PairIdentityTranscript binds the approved long-term signing identity as
// well as the ephemeral exchange. Every current pairing uses this transcript.
func PairIdentityTranscript(id string, initiator, joiner, device []byte, kind int32) []byte {
	h := sha256.New()
	h.Write([]byte("remote-agent/v1/pair-identity\x00"))
	h.Write(PairTranscript(id, initiator, joiner, kind))
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(device)))
	h.Write(n[:])
	h.Write(device)
	return h.Sum(nil)
}

// PairSecrets are what both sides derive from the exchange.
type PairSecrets struct {
	// SAS picks the four emoji, one byte each.
	SAS [4]byte
	// Key seals the root key.
	Key []byte
}

// ErrPairKey means the other side's public key is unusable.
var ErrPairKey = errors.New("e2e: bad pairing key")

// Derive computes the shared secrets with the other side's public key.
func (k *PairKey) Derive(otherPublic, transcript []byte) (*PairSecrets, error) {
	pub, err := ecdh.X25519().NewPublicKey(otherPublic)
	if err != nil {
		return nil, ErrPairKey
	}
	shared, err := k.priv.ECDH(pub) // fails on an all-zero result
	if err != nil {
		return nil, ErrPairKey
	}
	return derivePair(shared, transcript)
}

func derivePair(shared, transcript []byte) (*PairSecrets, error) {
	sas, err := hkdf.Key(sha256.New, shared, transcript, "remote-agent/v1/pair-sas", 4)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, shared, transcript, "remote-agent/v1/pair-key", KeySize)
	if err != nil {
		return nil, err
	}
	s := &PairSecrets{Key: key}
	copy(s.SAS[:], sas)
	return s, nil
}

// Emoji shows the SAS as four emoji.
func (s *PairSecrets) Emoji() []string {
	out := make([]string, 4)
	for i, b := range s.SAS {
		out[i] = SASEmoji[b]
	}
	return out
}

// SASEmoji maps a byte to an emoji. The web client has the same table;
// testdata/vectors.json keeps them equal.
var SASEmoji = [256]string{
	"🐶", "🐱", "🐭", "🐹", "🐰", "🦊", "🐻", "🐼", "🐨", "🐯", "🦁", "🐮", "🐷", "🐸", "🐵", "🐔",
	"🐧", "🐦", "🐤", "🦆", "🦅", "🦉", "🦇", "🐺", "🐗", "🐴", "🦄", "🐝", "🐛", "🦋", "🐌", "🐞",
	"🐜", "🦗", "🕷", "🦂", "🐢", "🐍", "🦎", "🦖", "🦕", "🐙", "🦑", "🦐", "🦞", "🦀", "🐡", "🐠",
	"🐟", "🐬", "🐳", "🐋", "🦈", "🐊", "🐅", "🐆", "🦓", "🦍", "🦧", "🐘", "🦛", "🦏", "🐪", "🐫",
	"🦒", "🦘", "🐃", "🐂", "🐄", "🐎", "🐖", "🐏", "🐑", "🦙", "🐐", "🦌", "🐕", "🐩", "🦮", "🐈",
	"🐓", "🦃", "🦚", "🦜", "🦢", "🦩", "🕊", "🐇", "🦝", "🦨", "🦡", "🦦", "🦥", "🐁", "🐀", "🐿",
	"🦔", "🌵", "🎄", "🌲", "🌳", "🌴", "🌱", "🌿", "☘", "🍀", "🎍", "🎋", "🍃", "🍂", "🍁", "🍄",
	"🐚", "🌾", "💐", "🌷", "🌹", "🥀", "🌺", "🌸", "🌼", "🌻", "🌞", "🌝", "🌛", "🌜", "🌚", "🌕",
	"🌖", "🌗", "🌘", "🌑", "🌒", "🌓", "🌔", "🌙", "🌎", "🌍", "🌏", "🪐", "💫", "⭐", "🌟", "✨",
	"⚡", "☄", "💥", "🔥", "🌪", "🌈", "☀", "🌤", "⛅", "🌥", "☁", "🌦", "🌧", "⛈", "🌩", "🌨",
	"❄", "☃", "⛄", "🌬", "💨", "💧", "💦", "☔", "☂", "🌊", "🍏", "🍎", "🍐", "🍊", "🍋", "🍌",
	"🍉", "🍇", "🍓", "🫐", "🍈", "🍒", "🍑", "🥭", "🍍", "🥥", "🥝", "🍅", "🍆", "🥑", "🥦", "🥬",
	"🥒", "🌶", "🫑", "🌽", "🥕", "🧄", "🧅", "🥔", "🍠", "🥐", "🥯", "🍞", "🥖", "🥨", "🧀", "🥚",
	"🍳", "🧈", "🥞", "🧇", "🥓", "🥩", "🍗", "🍖", "🌭", "🍔", "🍟", "🍕", "🥪", "🥙", "🧆", "🌮",
	"🌯", "🥗", "🥘", "🥫", "🍝", "🍜", "🍲", "🍛", "🍣", "🍱", "🥟", "🦪", "🍤", "🍙", "🍚", "🍘",
	"🍥", "🥠", "🥮", "🍢", "🍡", "🍧", "🍨", "🍦", "🥧", "🧁", "🍰", "🎂", "🍮", "🍭", "🍬", "🍫",
}

// PairDomainTranscript additionally binds the candidate's independent long-term
// recipient key and the precommitted authority trust descriptor, including genesis.
// Neither a shared account root nor a relay directory can substitute this pin.
func PairDomainTranscript(id string, initiator, joiner, device, hpke []byte, kind int32, trustDescriptor []byte) []byte {
	h := sha256.New()
	h.Write([]byte("ra3/pair-domain\x00"))
	for _, part := range [][]byte{PairIdentityTranscript(id, initiator, joiner, device, kind), hpke, trustDescriptor} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	return h.Sum(nil)
}

func PairDomainCommit(initiator, trustDescriptor []byte) []byte {
	h := sha256.New()
	h.Write([]byte("ra3/pair-commit\x00"))
	for _, part := range [][]byte{initiator, trustDescriptor} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	return h.Sum(nil)
}
