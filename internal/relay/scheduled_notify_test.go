package relay

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScheduledTurnEndReplayNeverPushes(t *testing.T) {
	// Exactly the durable bytes uploaded on reconnect after daemon restart;
	// no in-memory task map or running-session state is available here.
	e := &pb.Event{Time: timestamppb.Now(), Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED, ScheduledTaskId: "task", ScheduledRunId: "run", ScheduledNotified: true}}}
	b, err := proto.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	restored := &pb.Event{}
	if err := proto.Unmarshal(b, restored); err != nil {
		t.Fatal(err)
	}
	if notifyKind(restored) != pb.NotifyKind_NOTIFY_KIND_UNSPECIFIED {
		t.Fatal("scheduled completion pushed on replay")
	}
	restored.GetTurnEnded().ScheduledNotified = false
	if notifyKind(restored) != pb.NotifyKind_NOTIFY_KIND_UNSPECIFIED {
		t.Fatal("silent completion pushed")
	}
	restored.GetTurnEnded().ScheduledTaskId = ""
	if notifyKind(restored) != pb.NotifyKind_NOTIFY_KIND_TURN_ENDED {
		t.Fatal("ordinary completion suppressed")
	}
	notice := &pb.Event{Time: timestamppb.Now(), Kind: &pb.Event_ScheduledNotification{ScheduledNotification: &pb.ScheduledNotification{TaskId: "task", RunId: "run", Text: "result"}}}
	if notifyKind(notice) != pb.NotifyKind_NOTIFY_KIND_SCHEDULED {
		t.Fatal("notify_user not delivered")
	}
}

func TestScheduledNotificationEncryptedContent(t *testing.T) {
	c := &conn{acct: &Account{DeviceID: "machine"}}
	initializeTestSecurity(t, c)
	e := &pb.Event{Seq: 7, Kind: &pb.Event_ScheduledNotification{ScheduledNotification: &pb.ScheduledNotification{TaskId: "private-task", RunId: "private-run", Title: "Task result", Text: "A useful result"}}}
	req, err := c.notifyRequest(context.Background(), "session", &pb.Session{Title: "Conversation"}, pb.NotifyKind_NOTIFY_KIND_SCHEDULED, e)
	if err != nil {
		t.Fatal(err)
	}
	if req.DedupeKey != "scheduled:7" {
		t.Fatal("private task or run identity leaked to routing")
	}
	got := &pb.NotifyContent{}
	if _, err := openTestSecureMessage(c, nil, req.Content, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: c.acct.DeviceID, SessionId: "session", RequestId: req.DedupeKey, Sender: c.acct.DeviceID}, got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != pb.NotifyKind_NOTIFY_KIND_SCHEDULED || got.Title != "Task result" || got.Text != "A useful result" || got.SourceEventSeq != 7 {
		t.Fatalf("content: %v", got)
	}
}

func TestScheduledNotificationTitleSurvivesColdDelivery(t *testing.T) {
	c := &conn{acct: &Account{DeviceID: "machine"}}
	initializeTestSecurity(t, c)
	for _, title := range []string{"派发时的任务名", "恢复对话的名称", "Agent explicit title"} {
		for _, info := range []*pb.Session{nil, {Title: "Later unrelated title"}} {
			e := &pb.Event{Seq: 8, Kind: &pb.Event_ScheduledNotification{ScheduledNotification: &pb.ScheduledNotification{TaskId: "private-task", RunId: "private-run", Title: title, Text: "Result"}}}
			// Replay the committed event, as after a daemon restart or push retry.
			b, err := proto.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			restored := &pb.Event{}
			if err := proto.Unmarshal(b, restored); err != nil {
				t.Fatal(err)
			}
			req, err := c.notifyRequest(context.Background(), "session", info, pb.NotifyKind_NOTIFY_KIND_SCHEDULED, restored)
			if err != nil {
				t.Fatal(err)
			}
			got := &pb.NotifyContent{}
			if _, err := openTestSecureMessage(c, nil, req.Content, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: c.acct.DeviceID, SessionId: "session", RequestId: "scheduled:8", Sender: c.acct.DeviceID}, got); err != nil {
				t.Fatal(err)
			}
			if got.Title != title || got.Text != "Result" || got.SourceEventSeq != 8 {
				t.Fatalf("cold notification lost its resolved title: %v", got)
			}
		}
	}
}

type ownershipBackend struct{ prefixBackend }

func (b *ownershipBackend) SessionInfo(id string) (*pb.Session, error) {
	return &pb.Session{Id: id, TaskOwned: true, ScheduledTaskId: "private-task", Status: pb.SessionStatus_SESSION_STATUS_EXITED}, nil
}

type ownershipMachine struct {
	prefixMachine
	last *pb.PutSessionRequest
}

func (m *ownershipMachine) PutSession(ctx context.Context, q *connect.Request[pb.PutSessionRequest]) (*connect.Response[pb.PutSessionResponse], error) {
	m.last = q.Msg
	return m.prefixMachine.PutSession(ctx, q)
}

func TestScheduledOwnershipHeadMismatchRepublishes(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	backend := &ownershipBackend{prefixBackend: prefixBackend{dir: t.TempDir()}}
	service := &ownershipMachine{}
	c := &conn{r: New(backend), acct: &Account{DeviceID: "machine"}, machine: service}
	initializeTestSecurity(t, c)
	sync := func(head *pb.SessionHead) {
		t.Helper()
		_, _, _, err := c.syncSession(context.Background(), "session", head, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	sync(nil)
	if service.puts != 1 || !service.last.TaskOwned {
		t.Fatal("did not publish ownership")
	}
	header, err := deviceauth.ParseHeader(service.last.Meta.SecurityHeader)
	if err != nil {
		t.Fatal(err)
	}
	// Server lost just the clear ownership set, not encrypted metadata or its revision.
	head := &pb.SessionHead{SessionId: "session", MetaRevision: header.Seq, EndedMs: 1, TaskOwned: false}
	sync(head)
	if service.puts != 2 || !service.last.TaskOwned {
		t.Fatal("ownership mismatch was not repaired")
	}
	header, err = deviceauth.ParseHeader(service.last.Meta.SecurityHeader)
	if err != nil {
		t.Fatal(err)
	}
	head.MetaRevision, head.TaskOwned = header.Seq, true
	sync(head)
	if service.puts != 2 {
		t.Fatal("identical routing metadata needlessly resent")
	}
}

type publicationFailureBackend struct {
	ownershipBackend
	refused string
}

func (b *publicationFailureBackend) SessionPublicationFailed(id, reason string) error {
	b.refused = id + ":" + reason
	return nil
}
func TestRelaySessionLimitReachesBackend(t *testing.T) {
	b := &publicationFailureBackend{ownershipBackend: ownershipBackend{prefixBackend: prefixBackend{dir: t.TempDir()}}}
	service := &sessionRevisionMachine{validate: func(*pb.PutSessionRequest) error {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("relay_session_limit: full"))
	}}
	mux := http.NewServeMux()
	path, h := remoteagentv1connect.NewMachineServiceHandler(service)
	mux.Handle(path, h)
	server := httptest.NewServer(mux)
	defer server.Close()
	c := &conn{r: New(b), acct: &Account{DeviceID: "machine"}, machine: remoteagentv1connect.NewMachineServiceClient(server.Client(), server.URL)}
	initializeTestSecurity(t, c)
	if _, err := c.putSession(context.Background(), "session", make([]byte, 32), &pb.Session{Id: "session"}, 0); err == nil {
		t.Fatal("quota response lost")
	}
	if b.refused != "session:relay_session_limit" {
		t.Fatal("daemon not told why publication failed")
	}
}
