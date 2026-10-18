// Package deviceauth implements the pinned-authority security domain. It never
// treats an account-root signature or a relay directory as membership evidence.
package deviceauth

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
)

const (
	Version         = 2
	MaxMembers      = 128
	MaxStateBytes   = 64 << 10
	MaxHeaderBytes  = 1024
	MaxCipherBytes  = 4 << 20
	MaxMutableBytes = 64 << 10
	MaxObjects      = 4096
	MaxCatalogBytes = 16 << 20
	MaxIDBytes      = 128
)

var (
	ErrInvalid   = errors.New("security: invalid authenticated object")
	ErrUntrusted = errors.New("security: untrusted domain or signature")
	ErrStale     = errors.New("security: trusted state must be synchronized")
	ErrConflict  = errors.New("security: object changed; refresh before retrying")
	ErrPending   = errors.New("security: revocation is pending")
	ErrStorage   = errors.New("security: local security state unavailable")
	ErrCapacity  = errors.New("security: capacity exceeded")
)

type Hash [32]byte

func digest(label string, parts ...[]byte) Hash {
	h := sha256.New()
	h.Write([]byte(label))
	for _, p := range parts {
		h.Write(p)
	}
	var out Hash
	copy(out[:], h.Sum(nil))
	return out
}
func (h Hash) String() string { return hex.EncodeToString(h[:]) }

type encoder struct{ b []byte }

func (e *encoder) u32(n uint32) { e.b = binary.BigEndian.AppendUint32(e.b, n) }
func (e *encoder) u64(n uint64) { e.b = binary.BigEndian.AppendUint64(e.b, n) }
func (e *encoder) raw(b []byte) { e.b = append(e.b, b...) }
func (e *encoder) lp(b []byte)  { e.u32(uint32(len(b))); e.raw(b) }
func (e *encoder) str(s string) { e.lp([]byte(s)) }

type decoder struct {
	b   []byte
	err error
}

func (d *decoder) raw(n int) []byte {
	if n < 0 || n > len(d.b) || d.err != nil {
		d.err = ErrInvalid
		return nil
	}
	b := d.b[:n]
	d.b = d.b[n:]
	return b
}
func (d *decoder) u32() uint32 {
	b := d.raw(4)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}
func (d *decoder) u64() uint64 {
	b := d.raw(8)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}
func (d *decoder) lp(max int) []byte {
	n := d.u32()
	if uint64(n) > uint64(max) {
		d.err = ErrInvalid
		return nil
	}
	return append([]byte(nil), d.raw(int(n))...)
}
func (d *decoder) str() string    { return string(d.lp(MaxIDBytes)) }
func (d *decoder) hash() (h Hash) { copy(h[:], d.raw(32)); return }
func (d *decoder) done() error {
	if d.err != nil || len(d.b) != 0 {
		return ErrInvalid
	}
	return nil
}
func validID(s string) bool {
	if len(s) == 0 || len(s) > MaxIDBytes {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':') {
			return false
		}
	}
	return true
}
func deviceID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func next(n uint64) (uint64, error) {
	if n == 0 || n == math.MaxUint64 {
		return 0, ErrInvalid
	}
	return n + 1, nil
}
