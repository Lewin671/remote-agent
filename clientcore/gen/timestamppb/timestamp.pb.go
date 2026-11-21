// Code manually kept for minimal reflection-free google.protobuf.Timestamp wire support.

package timestamppb

import "github.com/Lewin671/remote-agent/clientcore/gen/litewire"

// Timestamp mirrors google.protobuf.Timestamp without reflection or JSON runtime dependencies.
type Timestamp struct {
	Seconds    int64
	Nanos      int32
	hasUnknown bool
}

func (m *Timestamp) HasUnknown() bool {
	return m != nil && m.hasUnknown
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

func (m *Timestamp) Size() int {
	return len(m.AppendMarshal(nil))
}

func (m *Timestamp) AppendMarshal(b []byte) []byte {
	if m == nil {
		return b
	}
	if m.Seconds != 0 {
		b = litewire.AppendVarint(b, 1, uint64(m.Seconds))
	}
	if m.Nanos != 0 {
		b = litewire.AppendInt32(b, 2, m.Nanos)
	}
	return b
}

func (m *Timestamp) Marshal() ([]byte, error) {
	return m.AppendMarshal(nil), nil
}

func (m *Timestamp) Unmarshal(b []byte) error {
	for len(b) > 0 {
		num, wtyp, err := litewire.ReadTag(&b)
		if err != nil {
			return err
		}
		switch num {
		case 1:
			if wtyp == litewire.VarintType {
				v, err := litewire.ReadVarint(&b)
				if err != nil {
					return err
				}
				m.Seconds = int64(v)
				continue
			}
		case 2:
			if wtyp == litewire.VarintType {
				v, err := litewire.ReadVarint(&b)
				if err != nil {
					return err
				}
				m.Nanos = int32(v)
				continue
			}
		}
		m.hasUnknown = true
		if err := litewire.Skip(&b, num, wtyp); err != nil {
			return err
		}
	}
	return nil
}
