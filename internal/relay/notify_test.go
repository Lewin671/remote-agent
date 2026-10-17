package relay

import (
	"context"
	"errors"
	"fmt"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// A retry canceled while it still waited for its turn among the machine's
// calls fails with the context's own error, not a Connect one: the
// notification is still owed.
func TestNotificationCanceledBeforeItWasSentIsStillOwed(t *testing.T) {
	for _, err := range []error{
		context.Canceled, context.DeadlineExceeded,
		fmt.Errorf("waiting for a slot: %w", context.Canceled),
		connect.NewError(connect.CodeCanceled, context.Canceled),
		connect.NewError(connect.CodeResourceExhausted, errors.New("busy")),
	} {
		if !notifyOwed(err) {
			t.Errorf("%v: taken for a refusal", err)
		}
	}
	for _, err := range []error{
		connect.NewError(connect.CodeInvalidArgument, errors.New("no")),
		connect.NewError(connect.CodePermissionDenied, errors.New("no")),
	} {
		if notifyOwed(err) {
			t.Errorf("%v: still owed", err)
		}
	}
}

func TestOwedNotifyCopiesBoundedTail(t *testing.T) {
	r := &Relay{owedNotify: map[string][]*pb.Event{}}
	original := make([]*pb.Event, maxOwedNotify+3)
	for i := range original {
		original[i] = &pb.Event{Seq: uint64(i + 1)}
	}
	r.oweNotify("session", original)
	// Replacing entries in the caller's backing array must not alter the
	// retained tail, which is what keeps the 16-event memory cap real.
	original[3], original[len(original)-1] = nil, nil
	got := r.takeOwedNotify("session")
	if len(got) != maxOwedNotify || got[0].Seq != 4 || got[len(got)-1].Seq != uint64(maxOwedNotify+3) {
		t.Fatalf("owed notification tail = %v", got)
	}
}

func TestApprovalNotificationMarksAnyShortenedContent(t *testing.T) {
	key := make([]byte, 32)
	keys, err := e2e.Derive(key)
	if err != nil {
		t.Fatal(err)
	}
	c := &conn{acct: &Account{DeviceID: "machine"}, keys: keys}
	for _, field := range []string{"description", "command", "tool_input", "complete"} {
		t.Run(field, func(t *testing.T) {
			a := &pb.ApprovalRequested{ApprovalId: "approval", Command: "short", Description: "why"}
			switch field {
			case "description":
				a.Description = strings.Repeat("长", notifyText+1)
			case "command":
				a.Command = strings.Repeat("长", notifyCommand+1)
			case "tool_input":
				a.Command = ""
				a.ToolInputJson = strings.Repeat("长", notifyCommand+1)
			}
			req, err := c.notifyRequest("session", &pb.Session{Title: "session"}, pb.NotifyKind_NOTIFY_KIND_ATTENTION, &pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: a}})
			if err != nil {
				t.Fatal(err)
			}
			got := &pb.NotifyContent{}
			if _, err := e2e.OpenMessage(c.keys.Notify, req.Content, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: "machine", SessionId: "session", RequestId: "approval", Sender: "machine"}, got); err != nil {
				t.Fatal(err)
			}
			if got.Approval.Truncated != (field != "complete") || got.Approval.ApprovalId != "approval" {
				t.Fatal("notification truncation not marked")
			}
		})
	}
}

func TestSkeletonNotificationHasNoApprovalActions(t *testing.T) {
	keys, err := e2e.Derive(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	c := &conn{acct: &Account{DeviceID: "machine"}, keys: keys}
	for _, e := range []*pb.Event{
		{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "private-prefix…#0123456789abcdef" + skeletonNote, Description: skeletonNote}}},
		{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "question", Questions: []*pb.Question{{Text: skeletonNote}}}}},
		{Kind: &pb.Event_PlanProposed{PlanProposed: &pb.PlanProposed{Text: skeletonNote}}},
	} {
		e.Seq = 7
		req, err := c.notifyRequest("session", &pb.Session{}, pb.NotifyKind_NOTIFY_KIND_ATTENTION, e)
		if err != nil {
			t.Fatal(err)
		}
		got := &pb.NotifyContent{}
		if _, err := e2e.OpenMessage(keys.Notify, req.Content, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY}, got); err != nil {
			t.Fatal(err)
		}
		if req.DedupeKey != "skeleton:7" || strings.Contains(string(req.Content.Header), "private-prefix") {
			t.Fatal("skeleton identity leaked into clear notification metadata")
		}
		if got.Approval != nil || got.Text != "内容过大，请在终端查看/回答" || proto.Size(req.Content) > 4096 {
			t.Fatal("skeleton notification reintroduced an approval action")
		}
	}
}
