// Code manually kept for minimal reflection-free google.protobuf.Timestamp wire support.

package timestamppb

import "google.golang.org/protobuf/encoding/protowire"

// Timestamp mirrors google.protobuf.Timestamp without reflection or JSON runtime dependencies.
type Timestamp struct {
	Seconds int64
	Nanos   int32
}

func (m *Timestamp) Reset() {
	if m != nil {
		*m = Timestamp{}
	}
}

func (m *Timestamp) GetSeconds() int64 {
	if m != nil {
		return m.Seconds
	}
	return 0
}

func (m *Timestamp) GetNanos() int32 {
	if m != nil {
		return m.Nanos
	}
	return 0
}

func (m *Timestamp) Clone() *Timestamp {
	if m == nil {
		return nil
	}
	cp := *m
	return &cp
}

func (m *Timestamp) AppendMarshal(b []byte) []byte {
	if m == nil {
		return b
	}
	if m.Seconds != 0 {
		b = protowire.AppendTag(b, 1, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(m.Seconds))
	}
	if m.Nanos != 0 {
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(int64(m.Nanos)))
	}
	return b
}

func (m *Timestamp) Marshal() ([]byte, error) {
	return m.AppendMarshal(nil), nil
}

func (m *Timestamp) Unmarshal(b []byte) error {
	for len(b) > 0 {
		num, wtyp, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		switch num {
		case 1:
			if wtyp == protowire.VarintType {
				v, vn := protowire.ConsumeVarint(b)
				if vn < 0 {
					return protowire.ParseError(vn)
				}
				m.Seconds = int64(v)
				b = b[vn:]
				continue
			}
		case 2:
			if wtyp == protowire.VarintType {
				v, vn := protowire.ConsumeVarint(b)
				if vn < 0 {
					return protowire.ParseError(vn)
				}
				m.Nanos = int32(v)
				b = b[vn:]
				continue
			}
		}
		sn := protowire.ConsumeFieldValue(num, wtyp, b)
		if sn < 0 {
			return protowire.ParseError(sn)
		}
		b = b[sn:]
	}
	return nil
}
