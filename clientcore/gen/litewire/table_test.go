package litewire

import (
	"bytes"
	"math"
	"reflect"
	"testing"
	"unsafe"
)

type tblEnum int32

type tblChild struct {
	_          [0]func()
	Name       string
	Val        int32
	hasUnknown bool
}

var descTblChild = MessageDesc{
	Strict:        true,
	HasUnknownOff: unsafe.Offsetof(tblChild{}.hasUnknown),
	New:           func() unsafe.Pointer { return unsafe.Pointer(&tblChild{}) },
	Fields: []Field{
		{Num: 1, Offset: unsafe.Offsetof(tblChild{}.Name), Kind: KindString},
		{Num: 2, Offset: unsafe.Offsetof(tblChild{}.Val), Kind: KindInt32},
	},
}

type tblAll struct {
	_          [0]func()
	B          bool
	I32        int32
	U32        uint32
	I64        int64
	U64        uint64
	S32        int32
	S64        int64
	F32        uint32
	SF32       int32
	Fl         float32
	F64        uint64
	SF64       int64
	Db         float64
	Str        string
	Raw        []byte
	Enum       tblEnum
	Child      *tblChild
	OptBool    *bool
	OptInt32   *int32
	OptUint32  *uint32
	OptInt64   *int64
	OptUint64  *uint64
	OptSint32  *int32
	OptSint64  *int64
	OptFixed32 *uint32
	OptSfix32  *int32
	OptFloat   *float32
	OptFixed64 *uint64
	OptSfix64  *int64
	OptDouble  *float64
	OptString  *string
	RepBool    []bool
	RepInt32   []int32
	RepUint32  []uint32
	RepInt64   []int64
	RepUint64  []uint64
	RepSint32  []int32
	RepSint64  []int64
	RepFixed32 []uint32
	RepSfix32  []int32
	RepFloat   []float32
	RepFixed64 []uint64
	RepSfix64  []int64
	RepDouble  []float64
	RepString  []string
	RepBytes   [][]byte
	RepEnum    []tblEnum
	RepChild   []*tblChild
	Oneof      any
	hasUnknown bool
}

type tblOneofText struct {
	_    [0]func()
	Text string
}

var descTblAll = MessageDesc{
	Strict:        true,
	HasUnknownOff: unsafe.Offsetof(tblAll{}.hasUnknown),
	New:           func() unsafe.Pointer { return unsafe.Pointer(&tblAll{}) },
	Fields: []Field{
		{Num: 1, Offset: unsafe.Offsetof(tblAll{}.B), Kind: KindBool},
		{Num: 2, Offset: unsafe.Offsetof(tblAll{}.I32), Kind: KindInt32},
		{Num: 3, Offset: unsafe.Offsetof(tblAll{}.U32), Kind: KindUint32},
		{Num: 4, Offset: unsafe.Offsetof(tblAll{}.I64), Kind: KindInt64},
		{Num: 5, Offset: unsafe.Offsetof(tblAll{}.U64), Kind: KindUint64},
		{Num: 6, Offset: unsafe.Offsetof(tblAll{}.S32), Kind: KindSint32},
		{Num: 7, Offset: unsafe.Offsetof(tblAll{}.S64), Kind: KindSint64},
		{Num: 8, Offset: unsafe.Offsetof(tblAll{}.F32), Kind: KindFixed32},
		{Num: 9, Offset: unsafe.Offsetof(tblAll{}.SF32), Kind: KindSfixed32},
		{Num: 10, Offset: unsafe.Offsetof(tblAll{}.Fl), Kind: KindFloat},
		{Num: 11, Offset: unsafe.Offsetof(tblAll{}.F64), Kind: KindFixed64},
		{Num: 12, Offset: unsafe.Offsetof(tblAll{}.SF64), Kind: KindSfixed64},
		{Num: 13, Offset: unsafe.Offsetof(tblAll{}.Db), Kind: KindDouble},
		{Num: 14, Offset: unsafe.Offsetof(tblAll{}.Str), Kind: KindString},
		{Num: 15, Offset: unsafe.Offsetof(tblAll{}.Raw), Kind: KindBytes},
		{Num: 16, Offset: unsafe.Offsetof(tblAll{}.Enum), Kind: KindInt32},
		{Num: 17, Offset: unsafe.Offsetof(tblAll{}.Child), Kind: KindMessage, Sub: &descTblChild},
		{Num: 18, Offset: unsafe.Offsetof(tblAll{}.OptBool), Kind: KindBool, Flags: FlagOptional},
		{Num: 19, Offset: unsafe.Offsetof(tblAll{}.OptInt32), Kind: KindInt32, Flags: FlagOptional},
		{Num: 20, Offset: unsafe.Offsetof(tblAll{}.OptUint32), Kind: KindUint32, Flags: FlagOptional},
		{Num: 21, Offset: unsafe.Offsetof(tblAll{}.OptInt64), Kind: KindInt64, Flags: FlagOptional},
		{Num: 22, Offset: unsafe.Offsetof(tblAll{}.OptUint64), Kind: KindUint64, Flags: FlagOptional},
		{Num: 23, Offset: unsafe.Offsetof(tblAll{}.OptSint32), Kind: KindSint32, Flags: FlagOptional},
		{Num: 24, Offset: unsafe.Offsetof(tblAll{}.OptSint64), Kind: KindSint64, Flags: FlagOptional},
		{Num: 25, Offset: unsafe.Offsetof(tblAll{}.OptFixed32), Kind: KindFixed32, Flags: FlagOptional},
		{Num: 26, Offset: unsafe.Offsetof(tblAll{}.OptSfix32), Kind: KindSfixed32, Flags: FlagOptional},
		{Num: 27, Offset: unsafe.Offsetof(tblAll{}.OptFloat), Kind: KindFloat, Flags: FlagOptional},
		{Num: 28, Offset: unsafe.Offsetof(tblAll{}.OptFixed64), Kind: KindFixed64, Flags: FlagOptional},
		{Num: 29, Offset: unsafe.Offsetof(tblAll{}.OptSfix64), Kind: KindSfixed64, Flags: FlagOptional},
		{Num: 30, Offset: unsafe.Offsetof(tblAll{}.OptDouble), Kind: KindDouble, Flags: FlagOptional},
		{Num: 31, Offset: unsafe.Offsetof(tblAll{}.OptString), Kind: KindString, Flags: FlagOptional},
		{Num: 32, Offset: unsafe.Offsetof(tblAll{}.RepBool), Kind: KindBool, Flags: FlagRepeated | FlagPacked},
		{Num: 33, Offset: unsafe.Offsetof(tblAll{}.RepInt32), Kind: KindInt32, Flags: FlagRepeated | FlagPacked},
		{Num: 34, Offset: unsafe.Offsetof(tblAll{}.RepUint32), Kind: KindUint32, Flags: FlagRepeated | FlagPacked},
		{Num: 35, Offset: unsafe.Offsetof(tblAll{}.RepInt64), Kind: KindInt64, Flags: FlagRepeated | FlagPacked},
		{Num: 36, Offset: unsafe.Offsetof(tblAll{}.RepUint64), Kind: KindUint64, Flags: FlagRepeated | FlagPacked},
		{Num: 37, Offset: unsafe.Offsetof(tblAll{}.RepSint32), Kind: KindSint32, Flags: FlagRepeated | FlagPacked},
		{Num: 38, Offset: unsafe.Offsetof(tblAll{}.RepSint64), Kind: KindSint64, Flags: FlagRepeated | FlagPacked},
		{Num: 39, Offset: unsafe.Offsetof(tblAll{}.RepFixed32), Kind: KindFixed32, Flags: FlagRepeated | FlagPacked},
		{Num: 40, Offset: unsafe.Offsetof(tblAll{}.RepSfix32), Kind: KindSfixed32, Flags: FlagRepeated | FlagPacked},
		{Num: 41, Offset: unsafe.Offsetof(tblAll{}.RepFloat), Kind: KindFloat, Flags: FlagRepeated | FlagPacked},
		{Num: 42, Offset: unsafe.Offsetof(tblAll{}.RepFixed64), Kind: KindFixed64, Flags: FlagRepeated | FlagPacked},
		{Num: 43, Offset: unsafe.Offsetof(tblAll{}.RepSfix64), Kind: KindSfixed64, Flags: FlagRepeated | FlagPacked},
		{Num: 44, Offset: unsafe.Offsetof(tblAll{}.RepDouble), Kind: KindDouble, Flags: FlagRepeated | FlagPacked},
		{Num: 45, Offset: unsafe.Offsetof(tblAll{}.RepString), Kind: KindString, Flags: FlagRepeated},
		{Num: 46, Offset: unsafe.Offsetof(tblAll{}.RepBytes), Kind: KindBytes, Flags: FlagRepeated},
		{Num: 47, Offset: unsafe.Offsetof(tblAll{}.RepEnum), Kind: KindInt32, Flags: FlagRepeated | FlagPacked | FlagMarshalOnly},
		{Num: 48, Offset: unsafe.Offsetof(tblAll{}.RepChild), Kind: KindMessage, Flags: FlagRepeated | FlagMarshalOnly, Sub: &descTblChild},
		{Num: 1, Kind: KindCustom},
	},
}

func extraMarshalTblAll(base unsafe.Pointer, b []byte, idx Number) []byte {
	m := (*tblAll)(base)
	if idx == 1 {
		if o, ok := m.Oneof.(*tblOneofText); ok && o != nil {
			b = AppendStringAlways(b, 49, o.Text)
		}
	}
	return b
}

func extraUnmarshalTblAll(base unsafe.Pointer, num Number, wtyp Type, bp *[]byte) (bool, error) {
	m := (*tblAll)(base)
	b := *bp
	switch num {
	case 47:
		if wtyp == BytesType {
			raw, err := ReadBytesRaw(&b)
			if err != nil {
				return true, err
			}
			for len(raw) > 0 {
				v, err := ReadVarint(&raw)
				if err != nil {
					return true, err
				}
				m.RepEnum = append(m.RepEnum, tblEnum(int32(v)))
			}
			*bp = b
			return true, nil
		}
		if wtyp == VarintType {
			v, err := ReadVarint(&b)
			if err != nil {
				return true, err
			}
			m.RepEnum = append(m.RepEnum, tblEnum(int32(v)))
			*bp = b
			return true, nil
		}
	case 48:
		if wtyp == BytesType {
			raw, err := ReadBytesRaw(&b)
			if err != nil {
				return true, err
			}
			elem := &tblChild{}
			if err := UnmarshalTable(raw, unsafe.Pointer(elem), &descTblChild); err != nil {
				return true, err
			}
			if elem.hasUnknown {
				m.hasUnknown = true
			}
			m.RepChild = append(m.RepChild, elem)
			*bp = b
			return true, nil
		}
	case 49:
		if wtyp == BytesType {
			v, err := ReadString(&b)
			if err != nil {
				return true, err
			}
			m.Oneof = &tblOneofText{Text: v}
			*bp = b
			return true, nil
		}
	}
	return false, nil
}

func init() {
	descTblAll.ExtraMarshal = extraMarshalTblAll
	descTblAll.ExtraUnmarshal = extraUnmarshalTblAll
}

func TestTableRoundTripAndEdgeCases(t *testing.T) {
	if got := AppendTable(nil, nil, &descTblAll); got != nil {
		t.Fatalf("nil base marshal = %x, want nil", got)
	}

	zBool := false
	zInt32 := int32(0)
	zUint32 := uint32(0)
	zInt64 := int64(0)
	zUint64 := uint64(0)
	zFloat := float32(0)
	zDouble := float64(0)
	zStr := ""

	in := &tblAll{
		B:          true,
		I32:        -42,
		U32:        300,
		I64:        -999999999,
		U64:        math.MaxUint64 - 5,
		S32:        -12345,
		S64:        -9876543210,
		F32:        0x12345678,
		SF32:       -777,
		Fl:         1.5,
		F64:        0x0102030405060708,
		SF64:       -888888,
		Db:         -3.25,
		Str:        "hello",
		Raw:        []byte{1, 2, 3},
		Enum:       -2,
		Child:      &tblChild{Name: "nested", Val: 9},
		OptBool:    &zBool,
		OptInt32:   &zInt32,
		OptUint32:  &zUint32,
		OptInt64:   &zInt64,
		OptUint64:  &zUint64,
		OptSint32:  &zInt32,
		OptSint64:  &zInt64,
		OptFixed32: &zUint32,
		OptSfix32:  &zInt32,
		OptFloat:   &zFloat,
		OptFixed64: &zUint64,
		OptSfix64:  &zInt64,
		OptDouble:  &zDouble,
		OptString:  &zStr,
		RepBool:    []bool{true, false, true},
		RepInt32:   []int32{-1, 0, 2},
		RepUint32:  []uint32{1, 200},
		RepInt64:   []int64{-5, 10},
		RepUint64:  []uint64{7, math.MaxUint64},
		RepSint32:  []int32{-3, 4},
		RepSint64:  []int64{-9, 8},
		RepFixed32: []uint32{11, 22},
		RepSfix32:  []int32{-11, 22},
		RepFloat:   []float32{2.5, -0.5},
		RepFixed64: []uint64{100, 200},
		RepSfix64:  []int64{-100, 200},
		RepDouble:  []float64{6.25, -1.5},
		RepString:  []string{"a", "", "b"},
		RepBytes:   [][]byte{{9}, {}},
		RepEnum:    []tblEnum{1, -1, 3},
		RepChild:   []*tblChild{nil, {Name: "rc", Val: 4}},
		Oneof:      &tblOneofText{Text: "custom"},
	}

	wire := AppendTable(nil, unsafe.Pointer(in), &descTblAll)
	var out tblAll
	if err := UnmarshalTable(wire, unsafe.Pointer(&out), &descTblAll); err != nil {
		t.Fatalf("UnmarshalTable failed: %v", err)
	}
	if out.hasUnknown {
		t.Fatalf("unexpected unknown fields after round-trip")
	}
	// Nil elements in RepChild are skipped on marshal, and aliasing on Raw is isolated.
	in.RepChild = []*tblChild{{Name: "rc", Val: 4}}
	if !reflect.DeepEqual(&out, in) {
		t.Fatalf("round-trip mismatch:\ngot:  %+v\nwant: %+v", out, in)
	}

	// Wrong wire type for a known scalar triggers strict hasUnknown and skips safely.
	badWire := AppendVarintAlways(nil, 14, 1) // #14 is KindString, VarintType is wrong
	var outWrong tblAll
	if err := UnmarshalTable(badWire, unsafe.Pointer(&outWrong), &descTblAll); err != nil {
		t.Fatalf("UnmarshalTable wrong wire type failed: %v", err)
	}
	if !outWrong.hasUnknown || outWrong.Str != "" {
		t.Fatalf("expected hasUnknown=true and empty Str, got hasUnknown=%v Str=%q", outWrong.hasUnknown, outWrong.Str)
	}

	// Strict propagation from nested singular child with unknown field.
	childWithUnknown := AppendStringAlways(nil, 1, "c")
	childWithUnknown = AppendVarintAlways(childWithUnknown, 99, 7)
	parentWire := AppendBytesAlways(nil, 17, childWithUnknown)
	var outNested tblAll
	if err := UnmarshalTable(parentWire, unsafe.Pointer(&outNested), &descTblAll); err != nil {
		t.Fatalf("UnmarshalTable nested unknown failed: %v", err)
	}
	if !outNested.hasUnknown || outNested.Child == nil || !outNested.Child.hasUnknown || outNested.Child.Name != "c" {
		t.Fatalf("nested strict propagation failed: parentUnknown=%v child=%+v", outNested.hasUnknown, outNested.Child)
	}

	// Repeated scalars arrive unpacked on wire + FlagMarshalOnly fields stay ignored in table scan.
	negSeven := int64(-7)
	unpacked := AppendVarintAlways(nil, 33, uint64(negSeven))
	unpacked = AppendVarintAlways(unpacked, 47, 5)
	var outUnpacked tblAll
	if err := UnmarshalTable(unpacked, unsafe.Pointer(&outUnpacked), &descTblAll); err != nil {
		t.Fatalf("UnmarshalTable unpacked failed: %v", err)
	}
	if !bytes.Equal(AppendTable(nil, unsafe.Pointer(&outUnpacked), &descTblAll), AppendTable(nil, unsafe.Pointer(&tblAll{RepInt32: []int32{-7}, RepEnum: []tblEnum{5}}), &descTblAll)) {
		t.Fatalf("unpacked decode mismatch: %+v", outUnpacked)
	}
}
