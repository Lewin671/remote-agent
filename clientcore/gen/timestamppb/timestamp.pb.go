// Code manually kept for minimal reflection-free google.protobuf.Timestamp wire support.

package timestamppb

import (
	"unsafe"

	"github.com/Lewin671/remote-agent/clientcore/gen/litewire"
)

// Timestamp mirrors google.protobuf.Timestamp without reflection or JSON runtime dependencies.
type Timestamp struct {
	Seconds    int64
	Nanos      int32
	hasUnknown bool
}

var LitewireDesc_Timestamp = litewire.MessageDesc{
	Strict:        true,
	HasUnknownOff: unsafe.Offsetof(Timestamp{}.hasUnknown),
	New:           func() unsafe.Pointer { return unsafe.Pointer(&Timestamp{}) },
	Fields: []litewire.Field{
		{Num: 1, Offset: unsafe.Offsetof(Timestamp{}.Seconds), Kind: litewire.KindInt64},
		{Num: 2, Offset: unsafe.Offsetof(Timestamp{}.Nanos), Kind: litewire.KindInt32},
	},
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
	return litewire.AppendTable(b, unsafe.Pointer(m), &LitewireDesc_Timestamp)
}

func (m *Timestamp) Marshal() ([]byte, error) {
	return m.AppendMarshal(nil), nil
}

func (m *Timestamp) Unmarshal(b []byte) error {
	return litewire.UnmarshalTable(b, unsafe.Pointer(m), &LitewireDesc_Timestamp)
}
