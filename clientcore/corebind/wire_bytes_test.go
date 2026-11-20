package corebind

import (
	"bytes"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"testing"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	stdcorev1 "github.com/Lewin671/remote-agent/gen/remoteagent/core/v1"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The lite encoder (cmd/protoc-gen-go-lite) writes fields in declaration order, oneofs where
// their first field is declared, and proto3 presence rules; refEncode does the same through
// protobuf-go reflection, so changing how the encoder is generated cannot change the bytes the
// shells receive. Maps with several entries are compared by length only: lite writes them in
// Go map order.
func requireCanonical(t *testing.T, what string, got []byte, msg proto.Message) {
	t.Helper()
	if err := proto.Unmarshal(got, msg); err != nil {
		t.Fatalf("%s: decode lite bytes: %v", what, err)
	}
	want := refEncode(nil, msg.ProtoReflect())
	if hasMultiEntryMap(msg.ProtoReflect()) {
		if len(got) != len(want) {
			t.Fatalf("%s: lite wrote %d bytes, reference %d", what, len(got), len(want))
		}
		return
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: lite bytes differ from reference\nlite: %x\nwant: %x", what, got, want)
	}
}

func refEncode(b []byte, m protoreflect.Message) []byte {
	fields := m.Descriptor().Fields()
	seen := make(map[protoreflect.OneofDescriptor]bool)
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if od := fd.ContainingOneof(); od != nil && !od.IsSynthetic() {
			if !seen[od] {
				seen[od] = true
				if set := m.WhichOneof(od); set != nil {
					b = refValue(b, set, m.Get(set))
				}
			}
			continue
		}
		if !m.Has(fd) {
			continue
		}
		v := m.Get(fd)
		switch {
		case fd.IsMap():
			var keys []protoreflect.MapKey
			v.Map().Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
				keys = append(keys, k)
				return true
			})
			for _, k := range keys {
				entry := refValue(nil, fd.MapKey(), k.Value())
				entry = refValue(entry, fd.MapValue(), v.Map().Get(k))
				b = protowire.AppendTag(b, fd.Number(), protowire.BytesType)
				b = protowire.AppendBytes(b, entry)
			}
		case fd.IsList() && fd.Kind() != protoreflect.StringKind && fd.Kind() != protoreflect.BytesKind && fd.Kind() != protoreflect.MessageKind:
			var packed []byte
			for j := 0; j < v.List().Len(); j++ {
				packed = refScalar(packed, fd, v.List().Get(j))
			}
			b = protowire.AppendTag(b, fd.Number(), protowire.BytesType)
			b = protowire.AppendBytes(b, packed)
		case fd.IsList():
			for j := 0; j < v.List().Len(); j++ {
				b = refValue(b, fd, v.List().Get(j))
			}
		default:
			b = refValue(b, fd, v)
		}
	}
	return b
}

func refValue(b []byte, fd protoreflect.FieldDescriptor, v protoreflect.Value) []byte {
	switch fd.Kind() {
	case protoreflect.MessageKind:
		b = protowire.AppendTag(b, fd.Number(), protowire.BytesType)
		return protowire.AppendBytes(b, refEncode(nil, v.Message()))
	case protoreflect.StringKind, protoreflect.BytesKind:
		b = protowire.AppendTag(b, fd.Number(), protowire.BytesType)
	case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind:
		b = protowire.AppendTag(b, fd.Number(), protowire.Fixed32Type)
	case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind:
		b = protowire.AppendTag(b, fd.Number(), protowire.Fixed64Type)
	default:
		b = protowire.AppendTag(b, fd.Number(), protowire.VarintType)
	}
	return refScalar(b, fd, v)
}

func refScalar(b []byte, fd protoreflect.FieldDescriptor, v protoreflect.Value) []byte {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protowire.AppendString(b, v.String())
	case protoreflect.BytesKind:
		return protowire.AppendBytes(b, v.Bytes())
	case protoreflect.BoolKind:
		return protowire.AppendVarint(b, protowire.EncodeBool(v.Bool()))
	case protoreflect.EnumKind:
		return protowire.AppendVarint(b, uint64(int64(v.Enum())))
	case protoreflect.Int32Kind, protoreflect.Int64Kind:
		return protowire.AppendVarint(b, uint64(v.Int()))
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind:
		return protowire.AppendVarint(b, v.Uint())
	case protoreflect.Sint32Kind, protoreflect.Sint64Kind:
		return protowire.AppendVarint(b, protowire.EncodeZigZag(v.Int()))
	case protoreflect.Fixed32Kind:
		return protowire.AppendFixed32(b, uint32(v.Uint()))
	case protoreflect.Sfixed32Kind:
		return protowire.AppendFixed32(b, uint32(v.Int()))
	case protoreflect.FloatKind:
		return protowire.AppendFixed32(b, math.Float32bits(float32(v.Float())))
	case protoreflect.Fixed64Kind:
		return protowire.AppendFixed64(b, v.Uint())
	case protoreflect.Sfixed64Kind:
		return protowire.AppendFixed64(b, uint64(v.Int()))
	case protoreflect.DoubleKind:
		return protowire.AppendFixed64(b, math.Float64bits(v.Float()))
	}
	panic("unsupported kind " + fd.Kind().String())
}

func hasMultiEntryMap(m protoreflect.Message) bool {
	found := false
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			if v.Map().Len() > 1 {
				found = true
			} else if fd.MapValue().Message() != nil {
				v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
					found = hasMultiEntryMap(mv.Message())
					return !found
				})
			}
		case fd.IsList() && fd.Message() != nil:
			for i := 0; i < v.List().Len() && !found; i++ {
				found = hasMultiEntryMap(v.List().Get(i).Message())
			}
		case fd.Message() != nil:
			found = hasMultiEntryMap(v.Message())
		}
		return !found
	})
	return found
}

func TestLiteWireBytesScreens(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join("..", "..", "testdata", "screens", "*.input.json"))
	if err != nil || len(inputs) == 0 {
		t.Fatalf("no screen fixtures: %v", err)
	}
	sort.Strings(inputs)
	for _, path := range inputs {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var in struct {
				Steps []struct {
					Event json.RawMessage `json:"event"`
					Views []struct {
						Screen json.RawMessage `json:"screen"`
					} `json:"views"`
				} `json:"steps"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Fatal(err)
			}
			c := New()
			defer c.Close()
			for i, step := range in.Steps {
				out, err := c.Dispatch(stdWire(t, step.Event, &stdcorev1.Event{}))
				if err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
				requireCanonical(t, "effects", out, &stdcorev1.EffectBatch{})
				for _, v := range step.Views {
					vm, err := c.View(stdWire(t, v.Screen, &stdcorev1.Screen{}))
					if err != nil {
						t.Fatalf("step %d view: %v", i, err)
					}
					requireCanonical(t, "view", vm, &stdcorev1.ViewModel{})
				}
			}
		})
	}
}

func TestLiteWireBytesProjection(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "projection", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Vectors []struct {
			Name   string            `json:"name"`
			Agent  int32             `json:"agent"`
			Events []json.RawMessage `json:"events"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, v := range corpus.Vectors {
		batch := &stdcorev1.EventBatch{}
		for _, e := range v.Events {
			ev := &stdv1.Event{}
			if err := protojson.Unmarshal(e, ev); err != nil {
				t.Fatalf("%s: %v", v.Name, err)
			}
			batch.Events = append(batch.Events, ev)
			wire, err := proto.Marshal(batch)
			if err != nil {
				t.Fatal(err)
			}
			out, err := Reduce(v.Agent, wire)
			if err != nil {
				t.Fatalf("%s: %v", v.Name, err)
			}
			requireCanonical(t, v.Name, out, &stdcorev1.ProjectionState{})
		}
	}
}

// Wire messages the core both decodes and re-encodes, filled at random: decoding protobuf-go
// bytes and encoding them again must give the same bytes.
func TestLiteWireBytesRoundTrip(t *testing.T) {
	type lite interface {
		Unmarshal([]byte) error
		Marshal() ([]byte, error)
	}
	cases := []struct {
		std  func() proto.Message
		lite func() lite
	}{
		{func() proto.Message { return &stdv1.Event{} }, func() lite { return &v1.Event{} }},
		{func() proto.Message { return &stdv1.LiveSnapshot{} }, func() lite { return &v1.LiveSnapshot{} }},
		{func() proto.Message { return &stdv1.Session{} }, func() lite { return &v1.Session{} }},
		{func() proto.Message { return &stdv1.Command{} }, func() lite { return &v1.Command{} }},
		{func() proto.Message { return &stdv1.MachineMeta{} }, func() lite { return &v1.MachineMeta{} }},
		{func() proto.Message { return &stdv1.DeviceMeta{} }, func() lite { return &v1.DeviceMeta{} }},
		{func() proto.Message { return &stdv1.ProjectPrefs{} }, func() lite { return &v1.ProjectPrefs{} }},
		{func() proto.Message { return &stdv1.UsageStatsResponse{} }, func() lite { return &v1.UsageStatsResponse{} }},
	}
	r := rand.New(rand.NewPCG(1, 2))
	for _, tc := range cases {
		name := string(tc.std().ProtoReflect().Descriptor().FullName())
		for i := 0; i < 200; i++ {
			src := tc.std()
			fillRandom(r, src.ProtoReflect(), 0)
			wire, err := proto.Marshal(src)
			if err != nil {
				t.Fatal(err)
			}
			l := tc.lite()
			if err := l.Unmarshal(wire); err != nil {
				t.Fatalf("%s #%d: lite decode: %v", name, i, err)
			}
			got, err := l.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			requireCanonical(t, name, got, tc.std())
		}
	}
}

func stdWire(t *testing.T, raw json.RawMessage, m proto.Message) []byte {
	t.Helper()
	if err := protojson.Unmarshal(raw, m); err != nil {
		t.Fatalf("protojson %T: %v", m, err)
	}
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fillRandom(r *rand.Rand, m protoreflect.Message, depth int) {
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if od := fd.ContainingOneof(); od != nil && !od.IsSynthetic() {
			continue
		}
		if r.IntN(3) == 0 {
			continue
		}
		switch {
		case fd.IsMap():
			if r.IntN(2) == 0 {
				mp := m.Mutable(fd).Map()
				mp.Set(randomScalar(r, fd.MapKey()).MapKey(), randomValue(r, mp.NewValue, fd.MapValue(), depth))
			}
		case fd.IsList():
			l := m.Mutable(fd).List()
			for n := r.IntN(3); n > 0; n-- {
				l.Append(randomValue(r, l.NewElement, fd, depth))
			}
		default:
			if fd.Message() != nil && depth > 4 {
				continue
			}
			m.Set(fd, randomValue(r, func() protoreflect.Value { return m.NewField(fd) }, fd, depth))
		}
	}
	oneofs := m.Descriptor().Oneofs()
	for i := 0; i < oneofs.Len(); i++ {
		od := oneofs.Get(i)
		if od.IsSynthetic() || r.IntN(4) == 0 {
			continue
		}
		fd := od.Fields().Get(r.IntN(od.Fields().Len()))
		if fd.Message() != nil && depth > 4 {
			continue
		}
		m.Set(fd, randomValue(r, func() protoreflect.Value { return m.NewField(fd) }, fd, depth))
	}
}

func randomValue(r *rand.Rand, newMsg func() protoreflect.Value, fd protoreflect.FieldDescriptor, depth int) protoreflect.Value {
	if fd.Message() != nil {
		v := newMsg()
		fillRandom(r, v.Message(), depth+1)
		return v
	}
	return randomScalar(r, fd)
}

func randomScalar(r *rand.Rand, fd protoreflect.FieldDescriptor) protoreflect.Value {
	small := r.IntN(2) == 0
	u := r.Uint64()
	if small {
		u %= 300
	}
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(u%2 == 1)
	case protoreflect.EnumKind:
		vals := fd.Enum().Values()
		return protoreflect.ValueOfEnum(vals.Get(int(u % uint64(vals.Len()))).Number())
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(int32(u))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(uint32(u))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(int64(u))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(u)
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(float32(u%1000) / 8)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(float64(u%100000) / 16)
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(randomText(r))
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes([]byte(randomText(r)))
	}
	panic("unsupported kind " + fd.Kind().String())
}

func randomText(r *rand.Rand) string {
	// Long enough now and then to need a two-byte length prefix.
	n := r.IntN(8)
	if r.IntN(5) == 0 {
		n = 100 + r.IntN(200)
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + r.IntN(26))
	}
	return string(b)
}
