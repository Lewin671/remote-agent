package relay

import (
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestNotifyRequestSealsApprovals(t *testing.T) {
	root := e2e.NewKey()
	keys, _ := e2e.Derive(root)
	c := &conn{acct: &Account{Root: root, DeviceID: "machine-1"}, keys: keys}
	deadline := time.Now().Add(10 * time.Minute)
	long := strings.Repeat("x", 5000)
	e := &pb.Event{Seq: 3, Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{
		ApprovalId: "appr-1", ToolName: "shell", Command: long, Deadline: timestamppb.New(deadline),
	}}}
	req, err := c.notifyRequest("s1", &pb.Session{Title: "修复登录超时"}, pb.NotifyKind_NOTIFY_KIND_ATTENTION, e)
	if err != nil {
		t.Fatal(err)
	}
	if req.DedupeKey != "appr-1" || req.SessionId != "s1" {
		t.Fatalf("request %v", req)
	}
	var nc pb.NotifyContent
	h, err := e2e.OpenMessage(keys.Notify, req.Content, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: "machine-1", SessionId: "s1", RequestId: "appr-1"}, &nc)
	if err != nil || h.Sender != "machine-1" {
		t.Fatalf("open: %v", err)
	}
	a := nc.Approval
	if nc.Title != "修复登录超时" || a.ApprovalId != "appr-1" || !a.Truncated || len([]rune(a.Command)) > notifyCommand+1 || a.DeadlineMs != deadline.UnixMilli() {
		t.Fatalf("content %v", &nc)
	}
	// Sealed with the notify key only.
	if _, _, err := e2e.Open(keys.Command, req.Content); err == nil {
		t.Fatal("opened with the command key")
	}
	// Small enough for FCM's 4 KB payload even with a huge command.
	if n := len(req.Content.Ciphertext) + len(req.Content.Header); n > 2400 {
		t.Fatalf("content is %d bytes", n)
	}
}
