// Package entropy implements a single shared sans-IO buffer of raw OS CSPRNG
// bytes supplied by the host shell. It contains no PRNG or DRBG expansion.
package entropy

import "errors"

const (
	// RefillBytes is the chunk size requested from the shell's OS CSPRNG.
	RefillBytes = 4096
	// LowWaterBytes triggers proactive refill before byte starvation.
	LowWaterBytes = 1024
	// MaxBytes bounds buffered random bytes.
	MaxBytes = 16384
)

// ErrNeedEntropy is returned when an exact byte request exceeds available OS entropy.
var ErrNeedEntropy = errors.New("entropy: insufficient buffered OS random bytes")

// Pool buffers raw OS CSPRNG bytes for sync, transport, and security engines.
type Pool struct {
	buf         []byte
	randomIDs   []string
	unitFloats  []float64
	fallbackSeq uint64
}

// Avail returns the count of buffered raw OS random bytes.
func (p *Pool) Avail() int {
	if p == nil {
		return 0
	}
	return len(p.buf)
}

// Append appends raw OS CSPRNG bytes up to MaxBytes.
func (p *Pool) Append(raw []byte) {
	if p == nil || len(raw) == 0 || len(p.buf) >= MaxBytes {
		return
	}
	rem := MaxBytes - len(p.buf)
	if len(raw) > rem {
		raw = raw[:rem]
	}
	p.buf = append(p.buf, raw...)
}

// PushLegacy records pre-formatted IDs or unit floats supplied by legacy callers/tests.
func (p *Pool) PushLegacy(ids []string, floats []float64) {
	if p == nil {
		return
	}
	for _, id := range ids {
		if id != "" {
			p.randomIDs = append(p.randomIDs, id)
		}
	}
	for _, f := range floats {
		if f < 0 {
			f = 0
		} else if f >= 1 {
			f = 0.999999999
		}
		p.unitFloats = append(p.unitFloats, f)
	}
}

// NeedsRefill reports whether raw OS bytes are below threshold.
func (p *Pool) NeedsRefill() bool {
	if p == nil {
		return false
	}
	return len(p.buf) < LowWaterBytes
}

// LegacyCounts reports the number of buffered legacy string IDs and unit floats.
func (p *Pool) LegacyCounts() (ids, floats int) {
	if p == nil {
		return 0, 0
	}
	return len(p.randomIDs), len(p.unitFloats)
}

// Read fills dst completely from buffered OS bytes or returns ErrNeedEntropy
// without consuming partial bytes.
func (p *Pool) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if p == nil || len(p.buf) < len(dst) {
		return 0, ErrNeedEntropy
	}
	n := copy(dst, p.buf[:len(dst)])
	copy(p.buf, p.buf[n:])
	p.buf = p.buf[:len(p.buf)-n]
	return n, nil
}

// TakeUnitFloat draws a uniform float64 in [0, 1) from buffered OS entropy
// (or legacy float buffer when present).
func (p *Pool) TakeUnitFloat() (float64, bool) {
	if p == nil {
		return 0, false
	}
	var b [8]byte
	if _, err := p.Read(b[:]); err == nil {
		u := uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
			uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
		return float64(u>>11) / (1 << 53), true
	}
	if len(p.unitFloats) > 0 {
		out := p.unitFloats[0]
		p.unitFloats = p.unitFloats[1:]
		return out, true
	}
	return 0, false
}

// TakeUnitFloatOrFallback draws a uniform float in [0, 1) or uses a deterministic
// sequence only if the pool has not been seeded yet (e.g. cold jitter backoff).
func (p *Pool) TakeUnitFloatOrFallback() float64 {
	if v, ok := p.TakeUnitFloat(); ok {
		return v
	}
	if p == nil {
		return 0
	}
	p.fallbackSeq++
	return float64((p.fallbackSeq*618033988749895)%1_000_000) / 1_000_000.0
}

// TakeID draws a 32-character lowercase hex ID from 16 buffered OS bytes
// (or legacy string ID buffer when present).
func (p *Pool) TakeID() (string, bool) {
	if p == nil {
		return "", false
	}
	var raw [16]byte
	if _, err := p.Read(raw[:]); err == nil {
		return encodeHex(raw[:]), true
	}
	if len(p.randomIDs) > 0 {
		out := p.randomIDs[0]
		p.randomIDs = p.randomIDs[1:]
		return out, true
	}
	return "", false
}

// TakeIDOrFallback returns a random ID or a sequential non-cryptographic ID when empty.
func (p *Pool) TakeIDOrFallback() string {
	if id, ok := p.TakeID(); ok {
		return id
	}
	if p == nil {
		return "core-1"
	}
	p.fallbackSeq++
	return "core-" + formatUint(p.fallbackSeq)
}

func encodeHex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[2*i] = digits[c>>4]
		out[2*i+1] = digits[c&0x0f]
	}
	return string(out)
}

func formatUint(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
