package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventview"
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
	c := &conn{acct: &Account{DeviceID: "machine"}}
	initializeTestSecurity(t, c)
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
			req, err := c.notifyRequest(context.Background(), "session", &pb.Session{Title: "session"}, pb.NotifyKind_NOTIFY_KIND_ATTENTION, &pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: a}})
			if err != nil {
				t.Fatal(err)
			}
			got := &pb.NotifyContent{}
			if _, err := openTestSecureMessage(c, nil, req.Content, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: c.acct.DeviceID, SessionId: "session", RequestId: "approval", Sender: c.acct.DeviceID}, got); err != nil {
				t.Fatal(err)
			}
			if got.Approval.Truncated != (field != "complete") || got.Approval.ApprovalId != "approval" {
				t.Fatal("notification truncation not marked")
			}
		})
	}
}

func TestSkeletonNotificationHasNoApprovalActions(t *testing.T) {
	c := &conn{acct: &Account{DeviceID: "machine"}}
	initializeTestSecurity(t, c)
	for _, e := range []*pb.Event{
		{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "private-prefix…#0123456789abcdef" + eventview.SkeletonNote, Description: eventview.SkeletonNote}}},
		{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "question", Questions: []*pb.Question{{Text: eventview.SkeletonNote}}}}},
		{Kind: &pb.Event_PlanProposed{PlanProposed: &pb.PlanProposed{Text: eventview.SkeletonNote}}},
	} {
		e.Seq = 7
		req, err := c.notifyRequest(context.Background(), "session", &pb.Session{}, pb.NotifyKind_NOTIFY_KIND_ATTENTION, e)
		if err != nil {
			t.Fatal(err)
		}
		got := &pb.NotifyContent{}
		if _, err := openTestSecureMessage(c, nil, req.Content, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY}, got); err != nil {
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

// A large identity can force fitEvent to shorten the command below the
// notification's own limit. clip must not erase that earlier truncation.
func TestApprovalNotificationRetainsFitTruncation(t *testing.T) {
	c := &conn{acct: &Account{DeviceID: "machine"}}
	initializeTestSecurity(t, c)
	original := &pb.Event{TurnId: strings.Repeat("t", eventview.MaxBytes-512), Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "approval", Command: strings.Repeat("c", 4000), Description: "why"}}}
	fitted := eventview.Fit(original)
	a := fitted.GetApprovalRequested()
	if eventview.IsSkeleton(fitted) || !strings.HasSuffix(a.Command, eventview.TruncatedNote) || len([]rune(a.Command)) >= notifyCommand {
		t.Fatal("fixture did not produce an already shortened, sub-limit command")
	}
	if _, cut := clip(a.Command, notifyCommand); cut {
		t.Fatal("fixture clipped again")
	}
	req, err := c.notifyRequest(context.Background(), "session", &pb.Session{}, pb.NotifyKind_NOTIFY_KIND_ATTENTION, fitted)
	if err != nil {
		t.Fatal(err)
	}
	got := &pb.NotifyContent{}
	if _, err := openTestSecureMessage(c, nil, req.Content, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: c.acct.DeviceID, SessionId: "session", RequestId: "approval", Sender: c.acct.DeviceID}, got); err != nil {
		t.Fatal(err)
	}
	if got.Approval == nil || !got.Approval.Truncated || got.Approval.Command != a.Command {
		t.Fatal("notification lost fitEvent's truncation flag")
	}
	if original.GetApprovalRequested().Command == a.Command {
		t.Fatal("original event was modified")
	}
}

func TestApprovalNotificationRecognizesExistingMarkersInEveryField(t *testing.T) {
	c := &conn{acct: &Account{DeviceID: "machine"}}
	initializeTestSecurity(t, c)
	for _, field := range []string{"description", "command", "tool_input"} {
		for _, note := range []string{eventview.TruncatedNote, eventview.SkeletonNote} {
			t.Run(field+note, func(t *testing.T) {
				a := &pb.ApprovalRequested{ApprovalId: "approval", Command: "true", Description: "why", ToolInputJson: "{}"}
				switch field {
				case "description":
					a.Description += note
				case "command":
					a.Command += note
				case "tool_input":
					a.ToolInputJson += note
				}
				if !eventview.ApprovalTextTruncated(a) {
					t.Fatal("existing marker missed")
				}
				req, err := c.notifyRequest(context.Background(), "session", &pb.Session{}, pb.NotifyKind_NOTIFY_KIND_ATTENTION, &pb.Event{Seq: 1, Kind: &pb.Event_ApprovalRequested{ApprovalRequested: a}})
				if err != nil {
					t.Fatal(err)
				}
				got := &pb.NotifyContent{}
				if _, err := openTestSecureMessage(c, nil, req.Content, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: c.acct.DeviceID, SessionId: "session", RequestId: req.DedupeKey, Sender: c.acct.DeviceID}, got); err != nil {
					t.Fatal(err)
				}
				if note == eventview.SkeletonNote {
					if got.Approval != nil {
						t.Fatal("skeleton offered notification approval")
					}
				} else if got.Approval == nil || !got.Approval.Truncated {
					t.Fatal("already truncated field offered notification allow")
				}
			})
		}
	}
}

func TestTurnNotificationCountsSharedFiles(t *testing.T) {
	file := func(turn string, seq uint64, ids ...string) *pb.Event {
		call := &pb.ToolCall{Kind: pb.ToolKind_TOOL_KIND_MCP, Title: "remote_agent.send_file", Status: "completed"}
		for _, id := range ids {
			call.SharedFiles = append(call.SharedFiles, &pb.SharedFile{FileId: id, Name: "private.txt", Path: "/private/project/private.txt"})
		}
		return &pb.Event{TurnId: turn, Seq: seq, Kind: &pb.Event_ToolCall{ToolCall: call}}
	}
	for _, tc := range []struct {
		name   string
		events []*pb.Event
		want   uint32
	}{
		{"zero", nil, 0},
		{"one", []*pb.Event{file("turn", 2, "a")}, 1},
		{"same file twice", []*pb.Event{file("turn", 2, "a", "a"), file("turn", 3, "a")}, 1},
		{"two files", []*pb.Event{file("turn", 2, "a"), file("turn", 3, "b")}, 2},
		{"failed send_file", []*pb.Event{{TurnId: "turn", Seq: 2, Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{Title: "remote_agent.send_file", Status: "failed", Output: "refused"}}}}, 0},
		{"only ended turn and preceding events", []*pb.Event{file("previous", 1, "old"), file("turn", 2, "a"), file("next", 11, "new"), file("turn", 12, "late")}, 1},
		{"missing file id", []*pb.Event{file("turn", 2, "")}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &conn{acct: &Account{DeviceID: "machine"}, r: &Relay{backend: &prefixBackend{events: tc.events}}}
			initializeTestSecurity(t, c)
			end := &pb.Event{TurnId: "turn", Seq: 10, Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{}}}
			// Serialize and restore the events as a daemon reconnect does.
			b := c.r.backend.(*prefixBackend)
			for i, e := range b.events {
				raw, err := proto.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				b.events[i] = &pb.Event{}
				if err := proto.Unmarshal(raw, b.events[i]); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				req, err := c.notifyRequest(context.Background(), "session", &pb.Session{Preview: "summary"}, pb.NotifyKind_NOTIFY_KIND_TURN_ENDED, end)
				if err != nil {
					t.Fatal(err)
				}
				got := &pb.NotifyContent{}
				if _, err := openTestSecureMessage(c, nil, req.Content, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY}, got); err != nil {
					t.Fatal(err)
				}
				if got.SharedFileCount != tc.want || got.Text != "summary" || got.SourceEventSeq != 10 {
					t.Fatalf("notification = %v; want count %d", got, tc.want)
				}
				if strings.Contains(string(req.Content.Header), "private") || strings.Contains(got.Text, "private") {
					t.Fatal("notification includes file name/path")
				}
			}
		})
	}
}

func TestTurnSharedFileCountReadFailure(t *testing.T) {
	failure := errors.New("unreadable events")
	c := &conn{r: &Relay{backend: &prefixBackend{events: []*pb.Event{{Seq: 3}}, readErr: failure}}}
	if _, err := c.turnSharedFileCount(context.Background(), "session", &pb.Event{TurnId: "turn", Seq: 10}); !errors.Is(err, failure) {
		t.Fatalf("event read error lost: %v", err)
	}
}
