package deviceauth

import (
	"bytes"
	"compress/flate"
	"io"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Unpack has an independent expansion limit. Authority metadata verification
// uses the smaller mutable cap, not the bulk payload's allowance.
func Unpack(h Header, p []byte) ([]byte, error) {
	limit := MaxCipherBytes
	if h.Purpose.Mutable() {
		limit = MaxMutableBytes
	}
	if h.Compression == 0 {
		if len(p) > limit {
			return nil, ErrCapacity
		}
		return p, nil
	}
	if h.Compression != 1 {
		return nil, ErrInvalid
	}
	r := flate.NewReader(bytes.NewReader(p))
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil || len(out) > limit {
		return nil, ErrInvalid
	}
	return out, nil
}
func known(m protoreflect.Message) bool {
	if len(m.GetUnknown()) != 0 {
		return false
	}
	ok := true
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if f.IsMap() {
			if f.MapValue().Message() != nil {
				v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool { ok = known(mv.Message()); return ok })
			}
		} else if f.IsList() {
			if f.Message() != nil {
				l := v.List()
				for i := 0; i < l.Len() && ok; i++ {
					ok = known(l.Get(i).Message())
				}
			}
		} else if f.Message() != nil {
			ok = known(v.Message())
		}
		return ok
	})
	return ok
}

// ValidateBody binds the decrypted frame direction/type/object to the signed
// purpose. Ownership is checked by Header.Scope, independently of the relay.
func ValidateBody(h Header, p []byte) error {
	plain, err := Unpack(h, p)
	if err != nil {
		return err
	}
	var m proto.Message
	switch h.Purpose {
	case Event:
		m = &pb.Event{}
	case SessionMeta:
		m = &pb.Session{}
	case SessionKey:
		if len(plain) != 32 {
			return ErrInvalid
		}
		return nil
	case MachineMeta:
		m = &pb.MachineMeta{}
	case Command:
		m = &pb.Command{}
	case Result:
		m = &pb.CommandResult{}
	case TerminalInput, TerminalResize, TerminalHistory, TerminalOutput:
		m = &pb.TerminalFrame{}
	case Notify:
		m = &pb.NotifyContent{}
	case Image:
		m = &pb.ImagePayload{}
	case Prefs:
		m = &pb.ProjectPrefs{}
	case DeviceMeta:
		m = &pb.DeviceMeta{}
	default:
		return ErrInvalid
	}
	if proto.Unmarshal(plain, m) != nil || !known(m.ProtoReflect()) {
		return ErrInvalid
	}
	switch v := m.(type) {
	case *pb.Event:
		if v.SessionId != h.Session || v.Seq != h.Seq || v.Kind == nil {
			return ErrInvalid
		}
	case *pb.Session:
		if v.Id != h.Session {
			return ErrInvalid
		}
	case *pb.Command:
		if v.Kind == nil {
			return ErrInvalid
		}
	case *pb.TerminalFrame:
		switch h.Purpose {
		case TerminalInput:
			if _, ok := v.Kind.(*pb.TerminalFrame_Input); !ok {
				return ErrInvalid
			}
		case TerminalResize:
			if _, ok := v.Kind.(*pb.TerminalFrame_Resize); !ok {
				return ErrInvalid
			}
		case TerminalHistory:
			if _, ok := v.Kind.(*pb.TerminalFrame_HistoryRequest); !ok {
				return ErrInvalid
			}
		case TerminalOutput:
			switch v.Kind.(type) {
			case *pb.TerminalFrame_Snapshot, *pb.TerminalFrame_Output, *pb.TerminalFrame_HistoryPage, *pb.TerminalFrame_Closed:
			default:
				return ErrInvalid
			}
		}
	case *pb.NotifyContent:
		if v.MachineId != h.Machine || v.SessionId != h.Session || (v.Kind != pb.NotifyKind_NOTIFY_KIND_ATTENTION && v.Kind != pb.NotifyKind_NOTIFY_KIND_TURN_ENDED) {
			return ErrInvalid
		}
	case *pb.ProjectPrefs:
		if v.Schema != 1 {
			return ErrInvalid
		}
	}
	return nil
}
