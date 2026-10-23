package relay

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type prefixBackend struct {
	Backend
	dir     string
	events  []*pb.Event
	reads   int
	readErr error
}

func (b *prefixBackend) SessionDir(string) string { return b.dir }
func (b *prefixBackend) SessionInfo(id string) (*pb.Session, error) {
	b.reads++
	title := "before"
	// A metadata edit makes flush exercise PutSession after UploadEvents.
	if b.reads >= 3 {
		title = "after"
	}
	return &pb.Session{Id: id, Title: title, LastSeq: uint64(len(b.events)), Status: pb.SessionStatus_SESSION_STATUS_EXITED}, nil
}
func (b *prefixBackend) EachEvents(_ string, after uint64, fn func(*pb.Event) error) (<-chan struct{}, error) {
	for _, e := range b.events {
		if e.Seq > after {
			if b.readErr != nil && e.Seq == 3 {
				return nil, b.readErr
			}
			if err := fn(e); err != nil {
				return nil, err
			}
		}
	}
	return make(chan struct{}), nil
}

type prefixMachine struct {
	remoteagentv1connect.MachineServiceClient
	fail                    string
	cause                   error
	puts, uploads, notifies int
	events                  []*pb.StoredEvent
}

func (m *prefixMachine) UploadEvents(_ context.Context, req *connect.Request[pb.UploadEventsRequest]) (*connect.Response[pb.UploadEventsResponse], error) {
	m.uploads++
	if m.fail == "upload_events" {
		return nil, m.cause
	}
	m.events = append(m.events, req.Msg.Events...)
	return connect.NewResponse(&pb.UploadEventsResponse{LastSeq: m.events[len(m.events)-1].Seq}), nil
}
func (m *prefixMachine) ListSessionHeads(context.Context, *connect.Request[pb.ListSessionHeadsRequest]) (*connect.Response[pb.ListSessionHeadsResponse], error) {
	return connect.NewResponse(&pb.ListSessionHeadsResponse{}), nil
}

func (m *prefixMachine) PutSession(context.Context, *connect.Request[pb.PutSessionRequest]) (*connect.Response[pb.PutSessionResponse], error) {
	m.puts++
	if m.fail == "put_session" && m.puts == 2 {
		return nil, m.cause
	}
	return connect.NewResponse(&pb.PutSessionResponse{}), nil
}
func (m *prefixMachine) Notify(context.Context, *connect.Request[pb.NotifyRequest]) (*connect.Response[pb.NotifyResponse], error) {
	m.notifies++
	if m.fail == "notify" {
		return nil, m.cause
	}
	return connect.NewResponse(&pb.NotifyResponse{}), nil
}

func TestSkeletonUploadAndPrefixFailure(t *testing.T) {
	for _, tc := range []struct {
		stage, category string
		code            connect.Code
		after           uint64
		connection      bool
	}{
		{after: 3},
		{stage: "read", after: 2},
		{stage: "upload_events", category: "upload_events/permission_denied", code: connect.CodePermissionDenied},
		{stage: "put_session", category: "put_session/failed_precondition", code: connect.CodeFailedPrecondition, after: 3},
		{stage: "notify", category: "notify/unavailable", code: connect.CodeUnavailable, after: 3, connection: true},
	} {
		name := tc.stage
		if name == "" {
			name = "prefix_success"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
			b := &prefixBackend{dir: t.TempDir(), events: []*pb.Event{
				{Seq: 1, SessionId: "session", Time: timestamppb.Now(), Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "approval", Command: "echo prefix"}}},
				{Seq: 2, SessionId: "session", Time: timestamppb.Now(), Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{}}},
				{Seq: 3, SessionId: "session", Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: strings.Repeat("q", maxEventBytes+1)}}},
			}}
			m := &prefixMachine{fail: tc.stage, cause: connect.NewError(tc.code, errors.New("injected flush failure"))}
			if tc.stage == "read" {
				m.cause = errors.New("damaged record")
				b.readErr = m.cause
			}
			r := New(b)
			c := &conn{r: r, acct: &Account{DeviceID: "machine"}, machine: m}
			initializeTestSecurity(t, c)
			after, _, skipped, err := c.syncSession(context.Background(), "session", nil, nil)
			var se *syncError
			classified := errors.As(err, &se)
			if (tc.stage == "" && err != nil) || (tc.stage != "" && tc.stage != "read" && (!classified || se.category != tc.category || se.connection != tc.connection)) || (tc.stage == "read" && classified) || skipped || after != tc.after {
				t.Fatalf("after=%d skipped=%v error=%v", after, skipped, err)
			}
			if tc.stage != "" && !errors.Is(err, m.cause) {
				t.Fatalf("flush error was replaced: %v", err)
			}
			if m.uploads != 1 {
				t.Fatalf("prefix upload attempts=%d", m.uploads)
			}
			if tc.stage == "upload_events" {
				if len(m.events) != 0 {
					t.Fatal("failed upload advanced cursor")
				}
				return
			}
			if len(m.events) != int(tc.after) || m.events[0].Seq != 1 || m.events[1].Seq != 2 {
				t.Fatal("prefix missing or oversized event uploaded")
			}
			if (tc.stage == "" || tc.stage == "read") && m.notifies != 2 {
				t.Fatalf("notifications=%d, want 2", m.notifies)
			}
			if tc.stage == "put_session" || tc.stage == "notify" {
				if !r.hasOwedNotify("session") {
					t.Fatal("uploaded prefix notifications not retained after failure")
				}
				// The next pass starts after the successfully uploaded skeleton:
				// retry notification debt even with no events left to flush.
				m.fail = ""
				before := m.notifies
				after, _, _, err = c.syncSession(context.Background(), "session", &pb.SessionHead{LastSeq: after}, nil)
				if err != nil || after != 3 || m.uploads != 1 || m.notifies-before != 2 || r.hasOwedNotify("session") {
					t.Fatalf("notification debt blocked: after=%d uploads=%d notifies=%d error=%v", after, m.uploads, m.notifies-before, err)
				}
			}
		})
	}
}

type imagePrefixBackend struct{ prefixBackend }

func (b *imagePrefixBackend) Image(string, string) ImageUpload          { return ImageUpload{Payload: []byte{}} }
func (b *imagePrefixBackend) ImageUploaded(string, string, bool, int64) {}

type imagePrefixMachine struct {
	prefixMachine
	entered chan int
	release chan struct{}
}

func (m *imagePrefixMachine) PutImage(context.Context, *connect.Request[pb.PutImageRequest]) (*connect.Response[pb.PutImageResponse], error) {
	m.entered <- len(m.events)
	<-m.release
	return connect.NewResponse(&pb.PutImageResponse{}), nil
}

func TestSyncFlushesPrefixBeforeBlockedImage(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	b := &imagePrefixBackend{prefixBackend{dir: t.TempDir(), events: []*pb.Event{
		{Seq: 1, SessionId: "session", Time: timestamppb.Now(), Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "approval", Command: "echo prefix"}}},
		{Seq: 2, SessionId: "session", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "before image"}}},
		{Seq: 3, SessionId: "session", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Images: []*pb.ImageRef{{ImageId: "image", Sync: pb.ImageSync_IMAGE_SYNC_SYNCED}}}}},
	}}}
	m := &imagePrefixMachine{entered: make(chan int), release: make(chan struct{})}
	c := &conn{r: New(b), acct: &Account{}, machine: m}
	initializeTestSecurity(t, c)
	done := make(chan error, 1)
	go func() {
		_, _, _, err := c.syncSession(context.Background(), "session", nil, nil)
		done <- err
	}()
	if n := <-m.entered; n != 2 {
		t.Errorf("events published before blocked image = %d, want 2", n)
	}
	if m.notifies != 1 {
		t.Errorf("approval notifications before image = %d, want 1", m.notifies)
	}
	close(m.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(m.events) != 3 || m.events[2].Seq != 3 {
		t.Fatal("image event did not follow its upload")
	}
}
