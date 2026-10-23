package relay

import (
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventview"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

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
	if got.GetQuestionAsked() == nil || got.GetQuestionAsked().QuestionId != "call" || !eventview.IsSkeleton(got) || !proto.Equal(e, before) {
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
	if q == nil || q.QuestionId != "call" || got.Seq != 7 || !strings.HasSuffix(q.Questions[0].Text, eventview.TruncatedNote) || q.Questions[0].Options[1].Label != "B" {
		t.Fatal("encrypted question lost")
	}
}
