package peerwire

// Hand-written replacements for encoding/binary and encoding/hex, which would
// pull fmt and reflect into the client core. Semantics match the standard
// library: uvarint returns n == 0 for a short buffer and n < 0 on overflow.

func uvarint(buf []byte) (uint64, int) {
	var x uint64
	var s uint
	for i, b := range buf {
		if i == 10 {
			return 0, -(i + 1)
		}
		if b < 0x80 {
			if i == 9 && b > 1 {
				return 0, -(i + 1)
			}
			return x | uint64(b)<<s, i + 1
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
	return 0, 0
}

func appendUvarint(out []byte, v uint64) []byte {
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func appendBig(out []byte, v uint64, size int) []byte {
	for i := size - 1; i >= 0; i-- {
		out = append(out, byte(v>>(8*i)))
	}
	return out
}

func appendHex(out []byte, b []byte) []byte {
	const digits = "0123456789abcdef"
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return out
}

func unhexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
