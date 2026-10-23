package eventview

import (
	"slices"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"google.golang.org/protobuf/proto"
)

// MaxBytes keeps a sealed event well below the server's 1 MiB
// envelope limit, including after a terminal-only skeleton fallback.
const MaxBytes = 768 << 10

const TruncatedNote = "\n…（remote-agent：内容过长，已截断）"

// fitEvent cuts display text and previews without changing event semantics.
// The local log keeps the full event. If display truncation is insufficient,
// keep the kind and scalar state in a terminal-only skeleton. Only an ID
// that still makes that skeleton oversized is replaced by prefix + hash.
func fitEvent(e *pb.Event) *pb.Event {
	if proto.Size(e) <= MaxBytes {
		return e
	}
	e = proto.CloneOf(e)
	// Previews go first, largest first: the text, the image refs and the
	// tool's state matter more than a thumbnail, which clients replace
	// with a placeholder of the right shape.
	refs := slices.Clone(agent.EventImages(e))
	slices.SortFunc(refs, func(a, b *pb.ImageRef) int { return len(b.Preview) - len(a.Preview) })
	for _, ref := range refs {
		if proto.Size(e) <= MaxBytes {
			return e
		}
		ref.Preview, ref.PreviewFormat, ref.PreviewWidth, ref.PreviewHeight = nil, pb.ImageFormat_IMAGE_FORMAT_UNSPECIFIED, 0, 0
	}
	if proto.Size(e) <= MaxBytes {
		return e
	}
	var fields []*string
	switch k := e.Kind.(type) {
	case *pb.Event_UserMessage:
		fields = []*string{&k.UserMessage.Text}
	case *pb.Event_AgentMessage:
		fields = []*string{&k.AgentMessage.Text}
	case *pb.Event_Reasoning:
		fields = []*string{&k.Reasoning.Text}
	case *pb.Event_ToolCall:
		fields = []*string{&k.ToolCall.Output, &k.ToolCall.Input, &k.ToolCall.Title}
		output := k.ToolCall.Output
		defer func() {
			if k.ToolCall.Output != output {
				k.ToolCall.OutputTruncated = true
			}
		}()
	case *pb.Event_ToolStarted:
		fields = []*string{&k.ToolStarted.Title, &k.ToolStarted.Input}
	case *pb.Event_TurnEnded:
		fields = []*string{&k.TurnEnded.Reason}
	case *pb.Event_ApprovalResolved:
		fields = []*string{&k.ApprovalResolved.Message}
	case *pb.Event_QuestionAsked:
		for _, q := range k.QuestionAsked.Questions {
			fields = append(fields, &q.Header, &q.Text)
			for _, o := range q.Options {
				// Labels are the selectable answers, not disposable previews.
				fields = append(fields, &o.Description)
			}
		}
	case *pb.Event_QuestionAnswered:
		for _, a := range k.QuestionAnswered.Answers {
			for i := range a.Answers {
				fields = append(fields, &a.Answers[i])
			}
		}
	case *pb.Event_SessionState:
		fields = []*string{&k.SessionState.Detail}
	case *pb.Event_SessionForked:
		fields = []*string{&k.SessionForked.SourceTitle}
	case *pb.Event_InputState:
		fields = []*string{&k.InputState.Text}
	case *pb.Event_ImageGenerated:
		fields = []*string{&k.ImageGenerated.RevisedPrompt}
	case *pb.Event_PlanProposed:
		fields = []*string{&k.PlanProposed.Text}
	case *pb.Event_ApprovalRequested:
		fields = []*string{&k.ApprovalRequested.ToolInputJson, &k.ApprovalRequested.Command, &k.ApprovalRequested.Description}
	}
	// Keep a visible marker on every shortened field. Start with a shared
	// text budget, then shrink it if IDs / labels / repeated structure take
	// more room. Never inflate short strings just to add a marker.
	original := make([]string, len(fields))
	for i, f := range fields {
		original[i] = *f
	}
	for cap := MaxBytes / 2 / max(1, len(fields)); ; cap /= 2 {
		for i, f := range fields {
			if len(original[i]) > cap+len(TruncatedNote) {
				*f = cutUTF8(original[i], cap) + TruncatedNote
			}
		}
		if proto.Size(e) <= MaxBytes {
			return e
		}
		if cap == 0 {
			// Option labels can be enormous too. Once selectable content
			// changes, the entire request becomes terminal-only.
			if q := e.GetQuestionAsked(); q != nil {
				for _, question := range q.Questions {
					for _, option := range question.Options {
						if len(option.Label) > 64 {
							option.Label = cutUTF8(option.Label, 64) + TruncatedNote
						}
					}
				}
				markSkeleton(e)
				if proto.Size(e) <= MaxBytes {
					return e
				}
			}
			return minimalEvent(e)
		}
	}
}

// cutUTF8 shortens s to at most n bytes without splitting a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
