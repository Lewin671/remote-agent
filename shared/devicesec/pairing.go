package devicesec

import (
	"crypto/hkdf"
	"crypto/sha256"
)

// Pairing: an existing device (the initiator) hands account trust to a new
// device (the joiner) over the untrusted server. Both make an X25519 key
// pair. The initiator commits to its public key first; the joiner sends
// its public key; then the initiator reveals its key. Because the
// initiator's key is fixed before the joiner's is seen, a server that
// swaps keys cannot search for a pair whose short authentication strings
// match. Both devices show four emoji derived from the shared secret and
// the whole exchange; the user checks they are the same before anything is
// sealed with a key from the same secret. The X25519 exchange itself is the
// caller's; DerivePairSecrets takes its output.

const pairTranscriptContext = "remote-agent/v1/pair\x00"

// PairKeySize is the size of the derived pairing key.
const PairKeySize = 32

func writeLP(h interface{ Write([]byte) (int, error) }, part []byte) {
	h.Write(appendU32(nil, uint32(len(part))))
	h.Write(part)
}

// PairTranscript binds everything both sides agreed on.
func PairTranscript(pairingID string, initiatorPublic, joinerPublic []byte, joinerKind int32) []byte {
	h := sha256.New()
	h.Write([]byte(pairTranscriptContext))
	for _, part := range [][]byte{[]byte(pairingID), initiatorPublic, joinerPublic} {
		writeLP(h, part)
	}
	h.Write(appendU32(nil, uint32(joinerKind)))
	return h.Sum(nil)
}

// PairIdentityTranscript binds the approved long-term signing identity as
// well as the ephemeral exchange. Every current pairing uses this transcript.
func PairIdentityTranscript(id string, initiator, joiner, device []byte, kind int32) []byte {
	h := sha256.New()
	h.Write([]byte("remote-agent/v1/pair-identity\x00"))
	h.Write(PairTranscript(id, initiator, joiner, kind))
	writeLP(h, device)
	return h.Sum(nil)
}

// PairDomainTranscript additionally binds the candidate's independent long-term
// recipient key and the precommitted authority trust descriptor, including genesis.
// Neither a shared account root nor a relay directory can substitute this pin.
func PairDomainTranscript(id string, initiator, joiner, device, hpke []byte, kind int32, trustDescriptor []byte) []byte {
	h := sha256.New()
	h.Write([]byte("ra3/pair-domain\x00"))
	for _, part := range [][]byte{PairIdentityTranscript(id, initiator, joiner, device, kind), hpke, trustDescriptor} {
		writeLP(h, part)
	}
	return h.Sum(nil)
}

func PairDomainCommit(initiator, trustDescriptor []byte) []byte {
	h := sha256.New()
	h.Write([]byte("ra3/pair-commit\x00"))
	for _, part := range [][]byte{initiator, trustDescriptor} {
		writeLP(h, part)
	}
	return h.Sum(nil)
}

// PairSecrets are what both sides derive from the exchange.
type PairSecrets struct {
	// SAS picks the four emoji, one byte each.
	SAS [4]byte
	// Key seals what the pairing transfers.
	Key []byte
}

// DerivePairSecrets derives the SAS and key from the X25519 shared secret
// (the caller's DH output, rejected by the caller if all zero) and the
// transcript.
func DerivePairSecrets(shared, transcript []byte) (*PairSecrets, error) {
	sas, err := hkdf.Key(sha256.New, shared, transcript, "remote-agent/v1/pair-sas", 4)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, shared, transcript, "remote-agent/v1/pair-key", PairKeySize)
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
// internal/e2e/testdata/vectors.json keeps them equal.
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
