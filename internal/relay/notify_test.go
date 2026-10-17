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
