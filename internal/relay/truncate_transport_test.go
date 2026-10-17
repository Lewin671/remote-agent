package relay_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/relay"
)

type sizeFailureBackend struct {
	relay.Backend
	attempts atomic.Int32
}

func (b *sizeFailureBackend) EachEvents(id string, after uint64, fn func(*pb.Event) error) (<-chan struct{}, error) {
	if id == "bad" {
		b.attempts.Add(1)
	}
	return b.Backend.EachEvents(id, after, fn)
}

// Real HTTP server, account registration, encrypted upload / download and
// forwarding; only the local CLI backend is a fake. An impossible question
// must stay unuploaded while another question and client commands still pass.
func TestOversizedQuestionTransportAndFailureIsolation(t *testing.T) {
	var logins atomic.Int32
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/Login") {
				logins.Add(1)
			}
			next.ServeHTTP(w, r)
		})
	})
	bad := e.backend.add(t, "bad")
	q := &pb.Question{Id: "q"}
	for range 4000 {
		q.Options = append(q.Options, &pb.QuestionOption{Label: strings.Repeat("label", 60)})
	}
	if err := bad.Append("bad", []*pb.Event{{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "impossible", Questions: []*pb.Question{q}}}}}, "", 0); err != nil {
		t.Fatal(err)
	}
	healthy := e.backend.add(t, "healthy")
	if err := healthy.Append("healthy", []*pb.Event{{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "large", Questions: []*pb.Question{{Id: "q", Text: strings.Repeat("正文", 200000), Options: []*pb.QuestionOption{{Label: "A"}, {Label: "B"}}}}}}}}, "", 0); err != nil {
		t.Fatal(err)
	}
	backend := &sizeFailureBackend{Backend: e.backend}
	logins.Store(0)
	e.startRelayBackend(backend)
	s := e.session(t, "healthy", 1)
	events, err := e.client.Events(context.Background(), s, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("events: %v", err)
	}
	got := events[0].GetQuestionAsked()
	if got == nil || got.QuestionId != "large" || len(got.Questions[0].Options) != 2 || !strings.HasSuffix(got.Questions[0].Text, "\n…（remote-agent：内容过长，已截断）") {
		t.Fatal("question lost in transport")
	}
	waitFor(t, "size failure retries", func() bool { return backend.attempts.Load() >= 3 })
	// The unsent sequence is not skipped or replaced on any retry.
	bs := e.session(t, "bad", 0)
	badEvents, err := e.client.Events(context.Background(), bs, 0)
	if err != nil || len(badEvents) != 0 {
		t.Fatalf("oversized event was replaced/uploaded: %d %v", len(badEvents), err)
	}
	appendText(t, healthy, "healthy", "continues")
	s = e.session(t, "healthy", 2)
	_, err = e.client.Do(context.Background(), s, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "healthy", Text: "command during size failure"}}})
	if err != nil || e.backend.sendCount() != 1 {
		t.Fatalf("command blocked: %v", err)
	}
	if logins.Load() != 1 {
		t.Fatal("size failure caused reconnect")
	}
}
