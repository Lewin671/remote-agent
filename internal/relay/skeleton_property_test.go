package relay

import (
	"encoding/json"
	"fmt"
	"google.golang.org/protobuf/encoding/protojson"
	"os"
	"reflect"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func fillSkeletonProperty(m protoreflect.Message, huge string, largeIDs bool, copies, depth int) {
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		value := func() protoreflect.Value {
			switch f.Kind() {
			case protoreflect.StringKind:
				if identityField(f) && !largeIDs {
					return protoreflect.ValueOfString("id_" + string(f.Name()))
				}
				return protoreflect.ValueOfString(huge)
			case protoreflect.BytesKind:
				return protoreflect.ValueOfBytes([]byte("preview"))
			case protoreflect.BoolKind:
				return protoreflect.ValueOfBool(true)
			case protoreflect.EnumKind:
				return protoreflect.ValueOfEnum(1)
			case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
				return protoreflect.ValueOfInt32(1)
			case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
				return protoreflect.ValueOfInt64(1)
			case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
				return protoreflect.ValueOfUint32(1)
			case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
				return protoreflect.ValueOfUint64(1)
			case protoreflect.DoubleKind:
				return protoreflect.ValueOfFloat64(1)
			case protoreflect.FloatKind:
				return protoreflect.ValueOfFloat32(1)
			}
			panic(f.FullName())
		}
		if f.IsList() {
			l := m.Mutable(f).List()
			n := copies
			if depth > 0 {
				n = 2
			}
			for range n {
				if f.Kind() == protoreflect.MessageKind {
					v := l.NewElement()
					fillSkeletonProperty(v.Message(), huge, largeIDs, copies, depth+1)
					l.Append(v)
				} else {
					l.Append(value())
				}
			}
		} else if f.Kind() == protoreflect.MessageKind {
			fillSkeletonProperty(m.Mutable(f).Message(), huge, largeIDs, copies, depth+1)
		} else {
			m.Set(f, value())
		}
	}
}

func checkSkeletonIdentityAndState(t *testing.T, before, after protoreflect.Message) {
	t.Helper()
	after.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		old := before.Get(f)
		check := func(a, b protoreflect.Value) {
			if f.Kind() == protoreflect.MessageKind {
				checkSkeletonIdentityAndState(t, a.Message(), b.Message())
				return
			}
			if f.Kind() == protoreflect.StringKind && identityField(f) {
				if a.String() != b.String() && (len(a.String()) <= skeletonIDLimit || b.String() != shortSkeletonID(a.String())) {
					t.Fatalf("identity %s changed without deterministic prefix/hash", f.FullName())
				}
			} else if f.Kind() != protoreflect.StringKind && f.Kind() != protoreflect.BytesKind {
				if strings.HasPrefix(string(f.Name()), "preview_") {
					return
				}
				if !reflect.DeepEqual(a.Interface(), b.Interface()) {
					t.Fatalf("scalar state %s changed", f.FullName())
				}
			}
		}
		if f.IsList() {
			if v.List().Len() > old.List().Len() {
				return true
			} // A marker-only question / answer is permitted.
			for i := 0; i < v.List().Len(); i++ {
				check(old.List().Get(i), v.List().Get(i))
			}
		} else {
			check(old, v)
		}
		return true
	})
}

func TestSkeletonSizePropertyForEveryKind(t *testing.T) {
	kinds := (&pb.Event{}).ProtoReflect().Descriptor().Oneofs().ByName("kind").Fields()
	if kinds.Len() != 20 {
		t.Fatal("update exhaustive event coverage")
	}
	huge := strings.Repeat("内容🙂", 200000)
	for i := 0; i < kinds.Len(); i++ {
		for _, largeIDs := range []bool{false, true} {
			for _, copies := range []int{1, 64} {
				name := fmt.Sprintf("%s/large_ids_%v/copies_%d", kinds.Get(i).Name(), largeIDs, copies)
				t.Run(name, func(t *testing.T) {
					e := &pb.Event{Seq: 17, SessionId: "session", TurnId: "turn", Origin: &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "device", RequestId: "request"}}
					if largeIDs {
						e.TurnId = huge
					}
					fillSkeletonProperty(e.ProtoReflect().Mutable(kinds.Get(i)).Message(), huge, largeIDs, copies, 0)
					before := proto.CloneOf(e)
					got := fitEvent(e)
					if proto.Size(got) > maxEventBytes || reflect.TypeOf(got.Kind) != reflect.TypeOf(e.Kind) || got.Seq != 17 {
						t.Fatal("size, kind or sequence invariant failed")
					}
					checkSkeletonIdentityAndState(t, before.ProtoReflect(), got.ProtoReflect())
					if largeIDs && !eventSkeleton(got) {
						t.Fatal("rewritten ID lacks terminal-only marker")
					}
					if !proto.Equal(got, fitEvent(e)) {
						t.Fatal("fallback is not deterministic")
					}
					if !proto.Equal(e, before) {
						t.Fatal("local log event changed")
					}
					env, err := e2e.SealMessage(make([]byte, 32), &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, SessionId: "session", Seq: 17}, got)
					if err != nil || proto.Size(env) > 1<<20 {
						t.Fatalf("envelope bound: %v", err)
					}
				})
			}
		}
	}
}

func TestSkeletonPreservesIDsUntilMinimalFormIsStillTooLarge(t *testing.T) {
	id := strings.Repeat("identity", 16<<10)
	e := &pb.Event{Seq: 1, TurnId: id, Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: strings.Repeat("text", 1<<20)}}}
	if fitEvent(e).TurnId != id {
		t.Fatal("large but fitting identity was shortened")
	}
	e.Kind = &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}
	e.TurnId = strings.Repeat("长🙂", maxEventBytes)
	got := fitEvent(e)
	if got.TurnId != shortSkeletonID(e.TurnId) || !eventSkeleton(got) || proto.Size(got) > maxEventBytes {
		t.Fatal("minimal identity fallback failed")
	}
}

func TestSharedSkeletonCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/projection/skeletons.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Note   string
		Events []json.RawMessage
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Note != skeletonNote || len(corpus.Events) != 20 {
		t.Fatal("shared marker / kind coverage changed")
	}
	kinds := map[reflect.Type]bool{}
	for _, raw := range corpus.Events {
		e := &pb.Event{}
		if err := protojson.Unmarshal(raw, e); err != nil {
			t.Fatal(err)
		}
		if !eventSkeleton(e) || proto.Size(e) > maxEventBytes {
			t.Fatalf("shared skeleton %T is not marked or bounded", e.Kind)
		}
		kinds[reflect.TypeOf(e.Kind)] = true
	}
	if len(kinds) != 20 {
		t.Fatal("shared corpus must cover each kind exactly once")
	}
}

func TestSkeletonRetainedSchemaHasBoundedSize(t *testing.T) {
	kinds := (&pb.Event{}).ProtoReflect().Descriptor().Oneofs().ByName("kind").Fields()
	for i := 0; i < kinds.Len(); i++ {
		e := &pb.Event{Seq: ^uint64(0), SessionId: strings.Repeat("s", skeletonIDLimit), TurnId: strings.Repeat("t", skeletonIDLimit), Origin: &pb.Origin{DeviceId: strings.Repeat("d", skeletonIDLimit), RequestId: strings.Repeat("r", skeletonIDLimit)}}
		fillSkeletonProperty(e.ProtoReflect().Mutable(kinds.Get(i)).Message(), strings.Repeat("x", skeletonIDLimit), true, 1, 0)
		pruneSkeleton(e.ProtoReflect())
		markSkeleton(e)
		if n := proto.Size(e); n > maxEventBytes/2 {
			t.Fatalf("%s retained schema grew beyond its proven safety margin: %d", kinds.Get(i).Name(), n)
		}
	}
}

func TestSkeletonIDGoldenFormat(t *testing.T) {
	if got := shortSkeletonID(strings.Repeat("x", 5000)); got != strings.Repeat("x", 64)+"…#c59d3c0480cc2d71"+skeletonNote {
		t.Fatal("ASCII prefix / SHA-256 prefix changed")
	}
	if got := shortSkeletonID(strings.Repeat("长", 2000)); got != strings.Repeat("长", 21)+"…#6ab630594b4d0e9e"+skeletonNote {
		t.Fatal("UTF-8 prefix / SHA-256 prefix changed")
	}
	if shortSkeletonID("ordinary-id") != "ordinary-id" {
		t.Fatal("ordinary ID changed")
	}
}
