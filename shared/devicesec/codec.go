// Package devicesec is the pure half of the protocol-10 device security: the
// signed membership chain, content grants, leases, recovery statements,
// device-signed envelopes and the pairing transcripts.
//
// It depends only on the standard library and never reads the clock, draws
// randomness, holds a private key or starts a goroutine. Callers pass the time
// in milliseconds, an io.Reader for every nonce, an HPKE implementation for
// content grants, and sign the bytes a builder returns before setting the
// Signature or Origin field. The daemon's synchronous adapter lives in
// internal/deviceauth.
package devicesec

import (
	"crypto/sha256"
	"errors"
	"math"
)

const (
	// Version is the state/header binary format version.
	Version = 3

	MaxMembers      = 128
	MaxStateBytes   = 64 << 10
	MaxHeaderBytes  = 1024
	MaxCipherBytes  = 4 << 20
	MaxMutableBytes = 64 << 10
	MaxStates       = 4096
	// A pairing payload carries the chain inside one envelope capped at
	// 1 MiB; above this it is refused explicitly instead of failing as a
	// generic oversized envelope. No compaction is implemented yet.
	MaxPairStates     = 256
	MaxPairChainBytes = 800 << 10

	// Lease is the membership lease; MaxLease is the largest accepted lease.
	Lease    = 72 * 60 * 60 * 1000
	MaxLease = Lease

	// MaxClockSkew is how far ahead of the local clock a state's IssuedAt
	// may be. A phone far ahead would otherwise keep a lease alive past the
	// intended revocation window.
	MaxClockSkew = 5 * 60 * 1000
)

var (
	ErrInvalid       = errors.New("security: invalid authenticated object")
	ErrUntrusted     = errors.New("security: untrusted domain or signature")
	ErrStale         = errors.New("security: trusted state must be synchronized")
	ErrClock         = errors.New("security: state is from the future; check this device's clock")
	ErrCapacity      = errors.New("security: capacity exceeded")
	ErrChainTooLarge = errors.New("security: security chain is too large to pair; rebuild the account")
)

type Hash [32]byte

// Digest is SHA-256 over label followed by parts.
func Digest(label string, parts ...[]byte) Hash {
	h := sha256.New()
	h.Write([]byte(label))
	for _, p := range parts {
		h.Write(p)
	}
	var out Hash
	copy(out[:], h.Sum(nil))
	return out
}

// String is lowercase hex.
func (h Hash) String() string { return hexString(h[:]) }

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 2*len(b))
	for i, c := range b {
		out[2*i], out[2*i+1] = digits[c>>4], digits[c&15]
	}
	return string(out)
}

// Encoder appends the big-endian, length-prefixed fields every signed object
// in this package is made of.
type Encoder struct{ B []byte }

//go:noinline
func (e *Encoder) U32(n uint32) { e.B = appendU32(e.B, n) }

//go:noinline
func (e *Encoder) U64(n uint64) {
	e.B = append(e.B, byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32), byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}

//go:noinline
func (e *Encoder) Raw(b []byte) { e.B = append(e.B, b...) }

//go:noinline
func (e *Encoder) LP(b []byte)  { e.U32(uint32(len(b))); e.Raw(b) }

//go:noinline
func (e *Encoder) Str(s string) { e.LP([]byte(s)) }

func appendU32(b []byte, n uint32) []byte {
	return append(b, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}

type decoder struct {
	b   []byte
	err error
}

//go:noinline
func (d *decoder) u32() uint32 {
	if d.err != nil || len(d.b) < 4 {
		d.err = ErrInvalid
		return 0
	}
	n := uint32(d.b[0])<<24 | uint32(d.b[1])<<16 | uint32(d.b[2])<<8 | uint32(d.b[3])
	d.b = d.b[4:]
	return n
}

//go:noinline
func (d *decoder) u64() uint64 {
	if d.err != nil || len(d.b) < 8 {
		d.err = ErrInvalid
		return 0
	}
	var n uint64
	for _, c := range d.b[:8] {
		n = n<<8 | uint64(c)
	}
	d.b = d.b[8:]
	return n
}

//go:noinline
func (d *decoder) raw(n int) []byte {
	if d.err != nil || n < 0 || len(d.b) < n {
		d.err = ErrInvalid
		return nil
	}
	b := d.b[:n]
	d.b = d.b[n:]
	return b
}

//go:noinline
func (d *decoder) lp(max int) []byte {
	n := d.u32()
	if d.err != nil || uint64(n) > uint64(max) || int(n) > len(d.b) {
		d.err = ErrInvalid
		return nil
	}
	return d.raw(int(n))
}

//go:noinline
func (d *decoder) str() string { return string(d.lp(MaxStateBytes)) }

//go:noinline
func (d *decoder) hash() Hash {
	var h Hash
	copy(h[:], d.raw(32))
	return h
}
func (d *decoder) done() error {
	if d.err != nil {
		return d.err
	}
	if len(d.b) != 0 {
		return ErrInvalid
	}
	return nil
}
func validID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_' || c == '.' || c == ':') {
			return false
		}
	}
	return true
}
// IsDeviceID reports whether s is a canonical 32-char lowercase hex device ID.
func IsDeviceID(s string) bool { return deviceID(s) }

func deviceID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func addLease(now uint64) uint64 {
	if now > math.MaxUint64-Lease {
		return math.MaxUint64
	}
	return now + Lease
}

//go:noinline
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		v := s[i]
		j := i - 1
		for j >= 0 && s[j] > v {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = v
	}
}

//go:noinline
func sortMembers(s []Member) {
	for i := 1; i < len(s); i++ {
		v := s[i]
		j := i - 1
		for j >= 0 && s[j].ID > v.ID {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = v
	}
}
