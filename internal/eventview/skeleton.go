package eventview

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A visible suffix in existing strings, including rewritten IDs for kinds
// without a text field. All clients treat this as terminal-only content.
const SkeletonNote = "\n…（remote-agent：内容过大，请在终端查看/回答）"
const skeletonIDLimit = 4096

func identityField(f protoreflect.FieldDescriptor) bool {
	n := string(f.Name())
	return n == "id" || strings.HasSuffix(n, "_id") || strings.HasSuffix(n, "_ids") || strings.HasSuffix(n, "_digest")
}

// A schema-bounded skeleton: at most one member of each repeated field,
// bounded non-identity text, no binary payload or unknown fields. Keep all
// scalar state (including enum, bool, seq and timestamps) and retained IDs.
func pruneSkeleton(m protoreflect.Message) {
	m.SetUnknown(nil)
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if f.IsList() {
			l := v.List()
			if l.Len() > 1 {
				l.Truncate(1)
			}
			if l.Len() == 0 {
				return true
			}
			if f.Kind() == protoreflect.MessageKind {
				pruneSkeleton(l.Get(0).Message())
			}
			if f.Kind() == protoreflect.StringKind && !identityField(f) && len(l.Get(0).String()) > 256 {
				l.Set(0, protoreflect.ValueOfString(cutUTF8(l.Get(0).String(), 256)+TruncatedNote))
			}
		} else {
			switch f.Kind() {
			case protoreflect.MessageKind:
				pruneSkeleton(v.Message())
			case protoreflect.BytesKind:
				m.Clear(f)
			case protoreflect.StringKind:
				if !identityField(f) && len(v.String()) > 256 {
					m.Set(f, protoreflect.ValueOfString(cutUTF8(v.String(), 256)+TruncatedNote))
				}
			}
		}
		return true
	})
}

func shortSkeletonID(s string) string {
	if len(s) <= skeletonIDLimit {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return cutUTF8(s, 64) + "…#" + hex.EncodeToString(sum[:8]) + SkeletonNote
}

func compactSkeletonIDs(m protoreflect.Message) {
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {

		if f.IsList() {
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				if f.Kind() == protoreflect.MessageKind {
					compactSkeletonIDs(l.Get(i).Message())
				}
				if f.Kind() == protoreflect.StringKind && identityField(f) {
					l.Set(i, protoreflect.ValueOfString(shortSkeletonID(l.Get(i).String())))
				}
			}
		} else if f.Kind() == protoreflect.MessageKind {
			compactSkeletonIDs(v.Message())
		} else if f.Kind() == protoreflect.StringKind && identityField(f) {
			m.Set(f, protoreflect.ValueOfString(shortSkeletonID(v.String())))
		}
		return true
	})
}

func markSkeleton(e *pb.Event) {
	switch k := e.Kind.(type) {
	case *pb.Event_UserMessage:
		k.UserMessage.Text = SkeletonNote
	case *pb.Event_AgentMessage:
		k.AgentMessage.Text = SkeletonNote
	case *pb.Event_Reasoning:
		k.Reasoning.Text = SkeletonNote
	case *pb.Event_ToolCall:
		k.ToolCall.Title = SkeletonNote
		k.ToolCall.OutputTruncated = true
	case *pb.Event_ScheduledNotification:
		k.ScheduledNotification.Text = SkeletonNote
	case *pb.Event_TurnEnded:
		k.TurnEnded.Reason = SkeletonNote
	case *pb.Event_TokenUsage:
		k.TokenUsage.PlanType = SkeletonNote
	case *pb.Event_TurnContext:
		k.TurnContext.Model = SkeletonNote
	case *pb.Event_ApprovalRequested:
		k.ApprovalRequested.Description = SkeletonNote
	case *pb.Event_ApprovalResolved:
		k.ApprovalResolved.Message = SkeletonNote
	case *pb.Event_QuestionAsked:
		if len(k.QuestionAsked.Questions) == 0 {
			k.QuestionAsked.Questions = []*pb.Question{{}}
		}
		k.QuestionAsked.Questions[0].Text = SkeletonNote
	case *pb.Event_QuestionAnswered:
		if len(k.QuestionAnswered.Answers) == 0 {
			k.QuestionAnswered.Answers = []*pb.QuestionAnswer{{}}
		}
		k.QuestionAnswered.Answers[0].Answers = []string{SkeletonNote}
	case *pb.Event_SessionState:
		k.SessionState.Detail = SkeletonNote
	case *pb.Event_InputState:
		k.InputState.Text = SkeletonNote
	case *pb.Event_PlanProposed:
		k.PlanProposed.Text = SkeletonNote
	case *pb.Event_SettingsChanged:
		k.SettingsChanged.Model = SkeletonNote
	case *pb.Event_ToolStarted:
		k.ToolStarted.Title = SkeletonNote
	case *pb.Event_ImageGenerated:
		k.ImageGenerated.RevisedPrompt = SkeletonNote
	case *pb.Event_SessionForked:
		k.SessionForked.SourceTitle = SkeletonNote
	case *pb.Event_TurnStarted, *pb.Event_PlanAnswered:
		// No payload text: oversized IDs carry the same suffix when rewritten.
	}
}

func minimalEvent(e *pb.Event) *pb.Event {
	pruneSkeleton(e.ProtoReflect())
	markSkeleton(e)
	// IDs change only after the skeleton still exceeds the budget.
	if proto.Size(e) > MaxBytes {
		compactSkeletonIDs(e.ProtoReflect())
	}
	return e
}

func IsSkeleton(e *pb.Event) bool {
	var has func(protoreflect.Message) bool
	has = func(m protoreflect.Message) bool {
		found := false
		m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			check := func(v protoreflect.Value) bool {
				if f.Kind() == protoreflect.StringKind {
					return strings.HasSuffix(v.String(), SkeletonNote)
				}
				return f.Kind() == protoreflect.MessageKind && has(v.Message())
			}
			if f.IsList() {
				for i := 0; i < v.List().Len(); i++ {
					if check(v.List().Get(i)) {
						found = true
						break
					}
				}
			} else {
				found = check(v)
			}
			return !found
		})
		return found
	}
	return has(e.ProtoReflect())
}
