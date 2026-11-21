package linkcore

import (
	"crypto/hmac"
	"crypto/sha256"
	"math"
)

var drbgReseedDomain = []byte("reseed")

// DRBG is an HMAC-SHA256 deterministic bit generator fed solely by caller
// entropy seeds (no crypto/rand). It satisfies io.Reader for linkcore.Session.
type DRBG struct {
	key     [32]byte
	counter uint64
}

// NewDRBG initializes a generator from seed. An empty seed produces a fixed zero
// key until Reseed is called; callers in production must Reseed with OS CSPRNG bytes.
func NewDRBG(seed []byte) *DRBG {
	d := &DRBG{}
	d.Reseed(seed)
	return d
}

// Reseed mixes additional entropy into the generator state.
func (d *DRBG) Reseed(seed []byte) {
	if len(seed) == 0 {
		return
	}
	mac := hmac.New(sha256.New, d.key[:])
	mac.Write(drbgReseedDomain)
	var ctr [8]byte
	putUint64BE(ctr[:], d.counter)
	mac.Write(ctr[:])
	mac.Write(seed)
	copy(d.key[:], mac.Sum(nil))
	d.counter = 0
}

// Read fills p with pseudorandom bytes and ratchets the internal key forward
// for backtracking resistance.
func (d *DRBG) Read(p []byte) (int, error) {
	var blk, ctr [32]byte
	written := 0
	for written < len(p) {
		d.counter++
		mac := hmac.New(sha256.New, d.key[:])
		putUint64BE(ctr[:8], d.counter)
		mac.Write(ctr[:8])
		sum := mac.Sum(blk[:0])
		n := copy(p[written:], sum)
		written += n
	}
	// Ratchet key after each Read call.
	mac := hmac.New(sha256.New, d.key[:])
	mac.Write([]byte{0xff})
	putUint64BE(ctr[:8], d.counter)
	mac.Write(ctr[:8])
	copy(d.key[:], mac.Sum(blk[:0]))
	return len(p), nil
}

// Uint64 returns a pseudorandom uint64.
func (d *DRBG) Uint64() uint64 {
	var b [8]byte
	_, _ = d.Read(b[:])
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
}

// UnitFloat returns a uniform pseudorandom float64 in [0, 1).
func (d *DRBG) UnitFloat() float64 {
	// Top 53 bits normalized to [0, 1).
	v := float64(d.Uint64()>>11) / float64(uint64(1)<<53)
	return math.Min(math.Max(v, 0), 0.9999999999999999)
}

func putUint64BE(b []byte, v uint64) {
	b[0] = byte(v >> 56)
	b[1] = byte(v >> 48)
	b[2] = byte(v >> 40)
	b[3] = byte(v >> 32)
	b[4] = byte(v >> 24)
	b[5] = byte(v >> 16)
	b[6] = byte(v >> 8)
	b[7] = byte(v)
}
