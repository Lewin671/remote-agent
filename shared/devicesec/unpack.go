package devicesec

import (
	"bytes"
	"compress/flate"
	"io"
)

// Unpack inflates envelope payload bytes according to h.Compression while
// enforcing MaxMutableBytes for mutable objects and MaxCipherBytes otherwise.
func Unpack(h Header, p []byte) ([]byte, error) {
	limit := MaxCipherBytes
	if h.Purpose.Mutable() {
		limit = MaxMutableBytes
	}
	switch h.Compression {
	case 0:
		if len(p) > limit {
			return nil, ErrCapacity
		}
		return p, nil
	case 1:
		return InflateBounded(p, limit)
	default:
		return nil, ErrInvalid
	}
}

// InflateBounded inflates raw deflate bytes up to limit bytes.
func InflateBounded(p []byte, limit int) ([]byte, error) {
	if limit <= 0 || limit > MaxCipherBytes {
		limit = MaxCipherBytes
	}
	r := flate.NewReader(bytes.NewReader(p))
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil || len(out) > limit {
		return nil, ErrInvalid
	}
	return out, nil
}
