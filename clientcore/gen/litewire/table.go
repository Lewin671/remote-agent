package litewire

import (
	"math"
	"unsafe"
)

// FieldKind identifies the protobuf wire/Go storage kind of a descriptor field.
type FieldKind uint8

const (
	KindBool FieldKind = iota + 1
	KindInt32
	KindUint32
	KindInt64
	KindUint64
	KindSint32
	KindSint64
	KindFixed32
	KindSfixed32
	KindFloat
	KindFixed64
	KindSfixed64
	KindDouble
	KindString
	KindBytes
	KindMessage
	KindCustom
)

// FieldFlags modifies how a descriptor field is accessed in memory.
type FieldFlags uint8

const (
	FlagOptional    FieldFlags = 1 << 0
	FlagRepeated    FieldFlags = 1 << 1
	FlagPacked      FieldFlags = 1 << 2
	FlagMarshalOnly FieldFlags = 1 << 3
)

// Field describes a single field or custom marshal slot inside a generated message.
type Field struct {
	Sub    *MessageDesc
	Offset uintptr
	Num    Number
	Kind   FieldKind
	Flags  FieldFlags
}

// MessageDesc describes a protobuf message for reflection-free table marshal/unmarshal.
type MessageDesc struct {
	Fields         []Field
	New            func() unsafe.Pointer
	Strict         bool
	HasUnknownOff  uintptr
	ExtraMarshal   func(base unsafe.Pointer, b []byte, idx Number) []byte
	ExtraUnmarshal func(base unsafe.Pointer, num Number, wtyp Type, bp *[]byte) (bool, error)
}

type sliceHeader struct {
	Data unsafe.Pointer
	Len  int
	Cap  int
}

func fieldWireType(k FieldKind) Type {
	switch k {
	case KindString, KindBytes, KindMessage:
		return BytesType
	case KindFixed32, KindSfixed32, KindFloat:
		return Fixed32Type
	case KindFixed64, KindSfixed64, KindDouble:
		return Fixed64Type
	default:
		return VarintType
	}
}

// AppendTableMessage appends a length-delimited nested message at wire field number num.
//
//go:noinline
func AppendTableMessage(b []byte, num Number, sub unsafe.Pointer, desc *MessageDesc) []byte {
	if sub == nil {
		return b
	}
	b, start := BeginMessage(b, num)
	b = AppendTable(b, sub, desc)
	return EndMessage(b, start)
}

// AppendTable encodes the message at base using desc in declaration order.
//
//go:noinline
func AppendTable(b []byte, base unsafe.Pointer, desc *MessageDesc) []byte {
	if base == nil || desc == nil {
		return b
	}
	for i := range desc.Fields {
		f := &desc.Fields[i]
		if f.Kind == KindCustom {
			if desc.ExtraMarshal != nil {
				b = desc.ExtraMarshal(base, b, f.Num)
			}
			continue
		}
		p := unsafe.Add(base, f.Offset)
		if f.Flags&FlagRepeated != 0 {
			b = appendTableRepeated(b, f, p)
			continue
		}
		if f.Flags&FlagOptional != 0 {
			q := *(*unsafe.Pointer)(p)
			if q == nil {
				continue
			}
			b = appendTableScalar(b, f.Num, f.Kind, q, true)
			continue
		}
		if f.Kind == KindMessage {
			sub := *(*unsafe.Pointer)(p)
			if sub != nil {
				b = AppendTableMessage(b, f.Num, sub, f.Sub)
			}
			continue
		}
		b = appendTableScalar(b, f.Num, f.Kind, p, false)
	}
	return b
}

func appendTableScalar(b []byte, num Number, kind FieldKind, p unsafe.Pointer, always bool) []byte {
	switch kind {
	case KindBool:
		v := *(*bool)(p)
		if always {
			return AppendBoolAlways(b, num, v)
		}
		return AppendBool(b, num, v)
	case KindInt32:
		v := *(*int32)(p)
		if always {
			return AppendInt32Always(b, num, v)
		}
		return AppendInt32(b, num, v)
	case KindUint32:
		v := uint64(*(*uint32)(p))
		if always {
			return AppendVarintAlways(b, num, v)
		}
		return AppendVarint(b, num, v)
	case KindInt64:
		v := uint64(*(*int64)(p))
		if always {
			return AppendVarintAlways(b, num, v)
		}
		return AppendVarint(b, num, v)
	case KindUint64:
		v := *(*uint64)(p)
		if always {
			return AppendVarintAlways(b, num, v)
		}
		return AppendVarint(b, num, v)
	case KindSint32:
		v := int64(*(*int32)(p))
		if always {
			return AppendZigZagAlways(b, num, v)
		}
		return AppendZigZag(b, num, v)
	case KindSint64:
		v := *(*int64)(p)
		if always {
			return AppendZigZagAlways(b, num, v)
		}
		return AppendZigZag(b, num, v)
	case KindFixed32:
		v := *(*uint32)(p)
		if always {
			return AppendFixed32Always(b, num, v)
		}
		return AppendFixed32(b, num, v)
	case KindSfixed32:
		v := uint32(*(*int32)(p))
		if always {
			return AppendFixed32Always(b, num, v)
		}
		return AppendFixed32(b, num, v)
	case KindFloat:
		v := *(*float32)(p)
		if always {
			return AppendFloatAlways(b, num, v)
		}
		return AppendFloat(b, num, v)
	case KindFixed64:
		v := *(*uint64)(p)
		if always {
			return AppendFixed64Always(b, num, v)
		}
		return AppendFixed64(b, num, v)
	case KindSfixed64:
		v := uint64(*(*int64)(p))
		if always {
			return AppendFixed64Always(b, num, v)
		}
		return AppendFixed64(b, num, v)
	case KindDouble:
		v := *(*float64)(p)
		if always {
			return AppendDoubleAlways(b, num, v)
		}
		return AppendDouble(b, num, v)
	case KindString:
		v := *(*string)(p)
		if always {
			return AppendStringAlways(b, num, v)
		}
		return AppendString(b, num, v)
	case KindBytes:
		v := *(*[]byte)(p)
		if always {
			return AppendBytesAlways(b, num, v)
		}
		return AppendBytes(b, num, v)
	}
	return b
}

func appendTableRepeated(b []byte, f *Field, p unsafe.Pointer) []byte {
	sh := *(*sliceHeader)(p)
	if sh.Len == 0 {
		return b
	}
	switch f.Kind {
	case KindMessage:
		for _, elem := range *(*[]unsafe.Pointer)(p) {
			if elem != nil {
				b = AppendTableMessage(b, f.Num, elem, f.Sub)
			}
		}
		return b
	case KindString:
		for _, v := range *(*[]string)(p) {
			b = AppendStringAlways(b, f.Num, v)
		}
		return b
	case KindBytes:
		for _, v := range *(*[][]byte)(p) {
			b = AppendBytesAlways(b, f.Num, v)
		}
		return b
	case KindBool:
		b = AppendMessagePrefix(b, f.Num, sh.Len)
		for _, v := range *(*[]bool)(p) {
			b = AppendRawVarint(b, EncodeBool(v))
		}
		return b
	case KindFixed32:
		b = AppendMessagePrefix(b, f.Num, sh.Len*4)
		for _, v := range *(*[]uint32)(p) {
			b = AppendRawFixed32(b, v)
		}
		return b
	case KindSfixed32:
		b = AppendMessagePrefix(b, f.Num, sh.Len*4)
		for _, v := range *(*[]int32)(p) {
			b = AppendRawFixed32(b, uint32(v))
		}
		return b
	case KindFloat:
		b = AppendMessagePrefix(b, f.Num, sh.Len*4)
		for _, v := range *(*[]float32)(p) {
			b = AppendRawFixed32(b, math.Float32bits(v))
		}
		return b
	case KindFixed64:
		b = AppendMessagePrefix(b, f.Num, sh.Len*8)
		for _, v := range *(*[]uint64)(p) {
			b = AppendRawFixed64(b, v)
		}
		return b
	case KindSfixed64:
		b = AppendMessagePrefix(b, f.Num, sh.Len*8)
		for _, v := range *(*[]int64)(p) {
			b = AppendRawFixed64(b, uint64(v))
		}
		return b
	case KindDouble:
		b = AppendMessagePrefix(b, f.Num, sh.Len*8)
		for _, v := range *(*[]float64)(p) {
			b = AppendRawFixed64(b, math.Float64bits(v))
		}
		return b
	}

	b, start := BeginMessage(b, f.Num)
	switch f.Kind {
	case KindInt32:
		for _, v := range *(*[]int32)(p) {
			b = AppendRawVarint(b, uint64(int64(v)))
		}
	case KindUint32:
		for _, v := range *(*[]uint32)(p) {
			b = AppendRawVarint(b, uint64(v))
		}
	case KindInt64:
		for _, v := range *(*[]int64)(p) {
			b = AppendRawVarint(b, uint64(v))
		}
	case KindUint64:
		for _, v := range *(*[]uint64)(p) {
			b = AppendRawVarint(b, v)
		}
	case KindSint32:
		for _, v := range *(*[]int32)(p) {
			b = AppendRawVarint(b, EncodeZigZag(int64(v)))
		}
	case KindSint64:
		for _, v := range *(*[]int64)(p) {
			b = AppendRawVarint(b, EncodeZigZag(v))
		}
	}
	return EndMessage(b, start)
}

// UnmarshalTable decodes wire bytes b into the message at base using desc.
//
//go:noinline
func UnmarshalTable(b []byte, base unsafe.Pointer, desc *MessageDesc) error {
	for len(b) > 0 {
		num, wtyp, err := ReadTag(&b)
		if err != nil {
			return err
		}
		var handled bool
		for i := range desc.Fields {
			f := &desc.Fields[i]
			if f.Num != num || f.Kind == KindCustom || f.Flags&FlagMarshalOnly != 0 {
				continue
			}
			p := unsafe.Add(base, f.Offset)
			if f.Flags&FlagRepeated != 0 {
				handled, err = unmarshalTableRepeated(f, wtyp, &b, p)
			} else if f.Flags&FlagOptional != 0 {
				if wtyp == fieldWireType(f.Kind) {
					handled = true
					err = unmarshalTableOptionalScalar(f.Kind, &b, p)
				}
			} else if f.Kind == KindMessage {
				if wtyp == BytesType {
					handled = true
					err = unmarshalTableSubMessage(base, desc, f, &b, p)
				}
			} else if wtyp == fieldWireType(f.Kind) {
				handled = true
				err = unmarshalTableScalar(f.Kind, &b, p)
			}
			if err != nil {
				return err
			}
			break
		}
		if !handled && desc.ExtraUnmarshal != nil {
			handled, err = desc.ExtraUnmarshal(base, num, wtyp, &b)
			if err != nil {
				return err
			}
		}
		if handled {
			continue
		}
		if desc.Strict {
			*(*bool)(unsafe.Add(base, desc.HasUnknownOff)) = true
		}
		if err := Skip(&b, num, wtyp); err != nil {
			return err
		}
	}
	return nil
}

func unmarshalTableSubMessage(parent unsafe.Pointer, desc *MessageDesc, f *Field, bp *[]byte, p unsafe.Pointer) error {
	slot := (*unsafe.Pointer)(p)
	sub := *slot
	if sub == nil {
		sub = f.Sub.New()
		*slot = sub
	}
	raw, err := ReadBytesRaw(bp)
	if err != nil {
		return err
	}
	if err := UnmarshalTable(raw, sub, f.Sub); err != nil {
		return err
	}
	if desc.Strict && f.Sub.Strict && *(*bool)(unsafe.Add(sub, f.Sub.HasUnknownOff)) {
		*(*bool)(unsafe.Add(parent, desc.HasUnknownOff)) = true
	}
	return nil
}

func unmarshalTableScalar(kind FieldKind, bp *[]byte, p unsafe.Pointer) error {
	switch kind {
	case KindString:
		v, err := ReadString(bp)
		if err != nil {
			return err
		}
		*(*string)(p) = v
	case KindBytes:
		v, err := ReadBytes(bp)
		if err != nil {
			return err
		}
		*(*[]byte)(p) = v
	case KindBool:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*bool)(p) = DecodeBool(v)
	case KindInt32:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*int32)(p) = int32(v)
	case KindUint32:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*uint32)(p) = uint32(v)
	case KindInt64:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*int64)(p) = int64(v)
	case KindUint64:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*uint64)(p) = v
	case KindSint32:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*int32)(p) = int32(DecodeZigZag(v))
	case KindSint64:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*int64)(p) = DecodeZigZag(v)
	case KindFixed32:
		v, err := ReadFixed32(bp)
		if err != nil {
			return err
		}
		*(*uint32)(p) = v
	case KindSfixed32:
		v, err := ReadFixed32(bp)
		if err != nil {
			return err
		}
		*(*int32)(p) = int32(v)
	case KindFloat:
		v, err := ReadFixed32(bp)
		if err != nil {
			return err
		}
		*(*float32)(p) = math.Float32frombits(v)
	case KindFixed64:
		v, err := ReadFixed64(bp)
		if err != nil {
			return err
		}
		*(*uint64)(p) = v
	case KindSfixed64:
		v, err := ReadFixed64(bp)
		if err != nil {
			return err
		}
		*(*int64)(p) = int64(v)
	case KindDouble:
		v, err := ReadFixed64(bp)
		if err != nil {
			return err
		}
		*(*float64)(p) = math.Float64frombits(v)
	}
	return nil
}

func unmarshalTableOptionalScalar(kind FieldKind, bp *[]byte, p unsafe.Pointer) error {
	switch kind {
	case KindString:
		v, err := ReadString(bp)
		if err != nil {
			return err
		}
		*(**string)(p) = &v
	case KindBool:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		x := DecodeBool(v)
		*(**bool)(p) = &x
	case KindInt32:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		x := int32(v)
		*(**int32)(p) = &x
	case KindUint32:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		x := uint32(v)
		*(**uint32)(p) = &x
	case KindInt64:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		x := int64(v)
		*(**int64)(p) = &x
	case KindUint64:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(**uint64)(p) = &v
	case KindSint32:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		x := int32(DecodeZigZag(v))
		*(**int32)(p) = &x
	case KindSint64:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		x := DecodeZigZag(v)
		*(**int64)(p) = &x
	case KindFixed32:
		v, err := ReadFixed32(bp)
		if err != nil {
			return err
		}
		*(**uint32)(p) = &v
	case KindSfixed32:
		v, err := ReadFixed32(bp)
		if err != nil {
			return err
		}
		x := int32(v)
		*(**int32)(p) = &x
	case KindFloat:
		v, err := ReadFixed32(bp)
		if err != nil {
			return err
		}
		x := math.Float32frombits(v)
		*(**float32)(p) = &x
	case KindFixed64:
		v, err := ReadFixed64(bp)
		if err != nil {
			return err
		}
		*(**uint64)(p) = &v
	case KindSfixed64:
		v, err := ReadFixed64(bp)
		if err != nil {
			return err
		}
		x := int64(v)
		*(**int64)(p) = &x
	case KindDouble:
		v, err := ReadFixed64(bp)
		if err != nil {
			return err
		}
		x := math.Float64frombits(v)
		*(**float64)(p) = &x
	}
	return nil
}

func unmarshalTableRepeated(f *Field, wtyp Type, bp *[]byte, p unsafe.Pointer) (bool, error) {
	if f.Flags&FlagPacked != 0 && wtyp == BytesType {
		raw, err := ReadBytesRaw(bp)
		if err != nil {
			return true, err
		}
		for len(raw) > 0 {
			if err := unmarshalTableScalarIntoSlice(f.Kind, &raw, p); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	if wtyp != fieldWireType(f.Kind) {
		return false, nil
	}
	return true, unmarshalTableScalarIntoSlice(f.Kind, bp, p)
}

func unmarshalTableScalarIntoSlice(kind FieldKind, bp *[]byte, p unsafe.Pointer) error {
	switch kind {
	case KindString:
		v, err := ReadString(bp)
		if err != nil {
			return err
		}
		*(*[]string)(p) = append(*(*[]string)(p), v)
	case KindBytes:
		v, err := ReadBytes(bp)
		if err != nil {
			return err
		}
		*(*[][]byte)(p) = append(*(*[][]byte)(p), v)
	case KindBool:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*[]bool)(p) = append(*(*[]bool)(p), DecodeBool(v))
	case KindInt32:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*[]int32)(p) = append(*(*[]int32)(p), int32(v))
	case KindUint32:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*[]uint32)(p) = append(*(*[]uint32)(p), uint32(v))
	case KindInt64:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*[]int64)(p) = append(*(*[]int64)(p), int64(v))
	case KindUint64:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*[]uint64)(p) = append(*(*[]uint64)(p), v)
	case KindSint32:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*[]int32)(p) = append(*(*[]int32)(p), int32(DecodeZigZag(v)))
	case KindSint64:
		v, err := ReadVarint(bp)
		if err != nil {
			return err
		}
		*(*[]int64)(p) = append(*(*[]int64)(p), DecodeZigZag(v))
	case KindFixed32:
		v, err := ReadFixed32(bp)
		if err != nil {
			return err
		}
		*(*[]uint32)(p) = append(*(*[]uint32)(p), v)
	case KindSfixed32:
		v, err := ReadFixed32(bp)
		if err != nil {
			return err
		}
		*(*[]int32)(p) = append(*(*[]int32)(p), int32(v))
	case KindFloat:
		v, err := ReadFixed32(bp)
		if err != nil {
			return err
		}
		*(*[]float32)(p) = append(*(*[]float32)(p), math.Float32frombits(v))
	case KindFixed64:
		v, err := ReadFixed64(bp)
		if err != nil {
			return err
		}
		*(*[]uint64)(p) = append(*(*[]uint64)(p), v)
	case KindSfixed64:
		v, err := ReadFixed64(bp)
		if err != nil {
			return err
		}
		*(*[]int64)(p) = append(*(*[]int64)(p), int64(v))
	case KindDouble:
		v, err := ReadFixed64(bp)
		if err != nil {
			return err
		}
		*(*[]float64)(p) = append(*(*[]float64)(p), math.Float64frombits(v))
	}
	return nil
}
