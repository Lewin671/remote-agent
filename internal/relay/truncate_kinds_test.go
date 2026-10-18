package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The table explicitly covers every kind in the current descriptor. All values
// except the selected display strings and explicitly oversized IDs survive, including
// IDs, choice labels, state codes, timestamps, sequence and origin metadata.
func TestFitEventEveryKind(t *testing.T) {
	huge := strings.Repeat("正文🙂", 160000)
	texts := map[string]string{
		"userMessage":       `{"text":%s,"requestId":"request","images":[{"imageId":"image","cliIndex":2}]}`,
		"agentMessage":      `{"text":%s,"phase":"final_answer"}`,
		"reasoning":         `{"text":%s}`,
		"toolCall":          `{"callId":"call","input":%s,"output":%s,"title":%s,"outputTruncated":true,"kind":1,"status":"completed","exitCode":3,"durationMs":"99","itemType":"CommandExecution","inputDigest":"digest","background":1,"backgroundId":"bg"}`,
		"turnStarted":       `{}`,
		"turnEnded":         `{"outcome":2,"reason":%s,"durationMs":"99"}`,
		"tokenUsage":        `{"totalTokens":"123","planType":"pro","messageId":"message"}`,
		"turnContext":       `{"model":"model","effort":"high","approvalPolicy":"on-request","sandbox":"workspace-write","collaborationMode":"default","permissionMode":"plan","approvalsReviewer":"user"}`,
		"approvalRequested": `{"approvalId":"approval","toolName":"Bash","command":%s,"toolInputJson":%s,"description":%s,"inputDigest":"digest","deadline":"2026-10-16T00:00:00Z"}`,
		"approvalResolved":  `{"approvalId":"approval","decision":6,"message":%s}`,
		"questionAsked":     `{"questionId":"question","async":true,"questions":[{"id":"q1","header":%s,"text":%s,"multiSelect":true,"options":[{"label":"A","description":%s},{"label":"B","description":%s}]},{"id":"q2","text":"keep","options":[{"label":"C"}]}]}`,
		"questionAnswered":  `{"questionId":"question","answers":[{"questionId":"q1","answers":[%s,"B"]}]}`,
		"sessionState":      `{"status":4,"detail":%s,"planOpen":true}`,
		"inputState":        `{"requestId":"request","status":1,"reason":"timeout","stage":2,"text":%s,"imageIds":["a","b"],"mode":2,"paused":true}`,
		"planProposed":      `{"text":%s,"callId":"plan"}`,
		"settingsChanged":   `{"model":"model","effort":"high","mode":4,"compacted":true}`,
		"toolStarted":       `{"callId":"call","title":%s,"input":%s,"kind":1,"itemType":"Bash","inputDigest":"digest"}`,
		"imageGenerated":    `{"callId":"image-call","revisedPrompt":%s,"images":[{"imageId":"image","cliIndex":1}]}`,
		"planAnswered":      `{"choice":2}`,
		"sessionForked":     `{"sourceSessionId":"source","sourceTitle":%s}`,
	}
	kinds := (&pb.Event{}).ProtoReflect().Descriptor().Oneofs().ByName("kind").Fields()
	if len(texts) != kinds.Len() {
		t.Fatal("update kind coverage")
	}
	for i := 0; i < kinds.Len(); i++ {
		name := kinds.Get(i).JSONName()
		t.Run(name, func(t *testing.T) {
			body, ok := texts[name]
			if !ok {
				t.Fatal("missing kind")
			}
			encoded, _ := json.Marshal(huge)
			body = strings.ReplaceAll(body, "%s", string(encoded))
			raw := `{"seq":"7","sessionId":"session","turnId":"turn","time":"2026-10-16T00:00:00Z","origin":{"kind":1,"deviceId":"device","requestId":"origin-request"},"` + name + `":` + body + `}`
			e := &pb.Event{}
			if err := protojson.Unmarshal([]byte(raw), e); err != nil {
				t.Fatal(err)
			}
			before := proto.CloneOf(e)
			// Kinds with no display text can only be oversized by protected fields.
			// Their skeleton must keep the kind and shorten only the abnormal ID.
			if !strings.Contains(texts[name], "%s") {
				e.TurnId = huge
				before = proto.CloneOf(e)
				got := fitEvent(e)
				if proto.Size(got) > maxEventBytes || reflect.TypeOf(got.Kind) != reflect.TypeOf(e.Kind) || got.Seq != e.Seq || got.SessionId != e.SessionId || !eventSkeleton(got) || got.TurnId != shortSkeletonID(huge) {
					t.Fatal("oversized identity skeleton lost")
				}
			} else {
				got := mustFitEvent(t, e)
				if reflect.TypeOf(got.Kind) != reflect.TypeOf(e.Kind) || proto.Size(got) > maxEventBytes {
					t.Fatal("kind lost or still oversized")
				}
				compareFitted(t, before.ProtoReflect(), got.ProtoReflect(), huge)
			}
			if !proto.Equal(e, before) {
				t.Fatal("logged event modified")
			}
		})
	}
}

func compareFitted(t *testing.T, want, got protoreflect.Message, huge string) {
	t.Helper()
	want.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		g := got.Get(f)
		check := func(v, g protoreflect.Value) {
			if f.Kind() == protoreflect.MessageKind {
				compareFitted(t, v.Message(), g.Message(), huge)
				return
			}
			if f.Kind() == protoreflect.StringKind && v.String() == huge {
				if len(g.String()) >= len(huge) || !utf8.ValidString(g.String()) || !strings.HasSuffix(g.String(), truncatedNote) {
					t.Errorf("%s not marked / shortened", f.FullName())
				}
			} else if !reflect.DeepEqual(v.Interface(), g.Interface()) {
				t.Errorf("%s changed", f.FullName())
			}
		}
		if f.IsList() {
			if v.List().Len() != g.List().Len() {
				t.Errorf("%s list shape changed", f.FullName())
				return true
			}
			for i := 0; i < v.List().Len(); i++ {
				check(v.List().Get(i), g.List().Get(i))
			}
		} else {
			check(v, g)
		}
		return true
	})
}

func TestFitEventRetainsAllOptionLabels(t *testing.T) {
	q := &pb.Question{Id: "q", Text: "choose"}
	for i := 0; i < 4000; i++ {
		q.Options = append(q.Options, &pb.QuestionOption{Label: fmt.Sprintf("option-%04d-%s", i, strings.Repeat("x", 80)), Description: strings.Repeat("长", 300)})
	}
	e := &pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "call", Questions: []*pb.Question{q}}}}
	got := mustFitEvent(t, e).GetQuestionAsked()
	if got == nil || got.QuestionId != "call" || len(got.Questions[0].Options) != 4000 {
		t.Fatal("question shape lost")
	}
	for i, o := range got.Questions[0].Options {
		if o.Label != q.Options[i].Label || !strings.HasSuffix(o.Description, truncatedNote) {
			t.Fatal("option label or marker lost")
		}
	}
}

func TestSealEventFitsIrreducibleStructure(t *testing.T) {
	q := &pb.Question{Id: "q"}
	for range 4000 {
		q.Options = append(q.Options, &pb.QuestionOption{Label: strings.Repeat("private-label", 30)})
	}
	e := &pb.Event{Seq: 7, SessionId: "session", Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "call", Questions: []*pb.Question{q}}}}
	before := proto.CloneOf(e)
	c := &conn{acct: &Account{DeviceID: "machine"}}
	initializeTestSecurity(t, c)
	key := make([]byte, 32)
	env, err := c.sealEvent(context.Background(), key, "session", e)
	if err != nil || proto.Size(env) > 1<<20 {
		t.Fatalf("skeleton upload: %v", err)
	}
	got := &pb.Event{}
	if _, err := openTestSecureMessage(c, key, env, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, SessionId: "session", Seq: 7}, got); err != nil {
		t.Fatal(err)
	}
	if got.GetQuestionAsked() == nil || got.GetQuestionAsked().QuestionId != "call" || !eventSkeleton(got) || !proto.Equal(e, before) {
		t.Fatal("skeleton lost identity, marker or original log")
	}
}

func TestSealEventPreservesOversizedQuestion(t *testing.T) {
	e := &pb.Event{Seq: 7, SessionId: "session", Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "call", Questions: []*pb.Question{{Id: "q", Text: strings.Repeat("长", 1<<20), Options: []*pb.QuestionOption{{Label: "A"}, {Label: "B"}}}}}}}
	key := make([]byte, 32)
	c := &conn{acct: &Account{DeviceID: "machine"}}
	initializeTestSecurity(t, c)
	env, err := c.sealEvent(context.Background(), key, "session", e)
	if err != nil {
		t.Fatal(err)
	}
	got := &pb.Event{}
	if _, err := openTestSecureMessage(c, key, env, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: c.acct.DeviceID, SessionId: "session", Seq: 7, Sender: c.acct.DeviceID}, got); err != nil {
		t.Fatal(err)
	}
	q := got.GetQuestionAsked()
	if q == nil || q.QuestionId != "call" || got.Seq != 7 || !strings.HasSuffix(q.Questions[0].Text, truncatedNote) || q.Questions[0].Options[1].Label != "B" {
		t.Fatal("encrypted question lost")
	}
}
