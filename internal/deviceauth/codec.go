// Package deviceauth implements the protocol-10 signed membership chain and
// the device-signed envelopes that carry account data.
package deviceauth

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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

	// Lease is the membership lease; MaxLease is the largest accepted lease.
	Lease    = 72 * 60 * 60 * 1000
	MaxLease = 7 * 24 * 60 * 60 * 1000

	// CounterBlock is reserved durably before use.
	CounterBlock = 4096
)

var (
	ErrInvalid     = errors.New("security: invalid authenticated object")
	ErrUntrusted   = errors.New("security: untrusted domain or signature")
	ErrStale       = errors.New("security: trusted state must be synchronized")
	ErrUnknown     = errors.New("security: unknown state")
	ErrUnavailable = errors.New("security: content is unavailable before the recovery boundary")
	ErrStorage     = errors.New("security: local security state unavailable")
	ErrCapacity    = errors.New("security: capacity exceeded")
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

func (d *decoder) u32() uint32 {
	if d.err != nil || len(d.b) < 4 {
		d.err = ErrInvalid
		return 0
	}
	n := binary.BigEndian.Uint32(d.b)
	d.b = d.b[4:]
	return n
}
func (d *decoder) u64() uint64 {
	if d.err != nil || len(d.b) < 8 {
		d.err = ErrInvalid
		return 0
	}
	n := binary.BigEndian.Uint64(d.b)
	d.b = d.b[8:]
	return n
}
func (d *decoder) raw(n int) []byte {
	if d.err != nil || n < 0 || len(d.b) < n {
		d.err = ErrInvalid
		return nil
	}
	b := d.b[:n]
	d.b = d.b[n:]
	return b
}
func (d *decoder) lp(max int) []byte {
	n := d.u32()
	if d.err != nil || uint64(n) > uint64(max) || int(n) > len(d.b) {
		d.err = ErrInvalid
		return nil
	}
	return d.raw(int(n))
}
func (d *decoder) str() string { return string(d.lp(MaxStateBytes)) }
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
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
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
func maxU64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
func minU64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
func addLease(now uint64) uint64 {
	if now > math.MaxUint64-Lease {
		return math.MaxUint64
	}
	return now + Lease
}
