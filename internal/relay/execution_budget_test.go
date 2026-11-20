package relay

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/contextio"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

type stalledReadBackend struct {
	Backend
	release, entered chan struct{}
	calls            atomic.Int32
	wrapped          bool
}
type stalledReadLocal struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	b *stalledReadBackend
}

func (b *stalledReadBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return stalledReadLocal{b: b}
}
func (l stalledReadLocal) GetSettings(ctx context.Context, _ *connect.Request[pb.GetSettingsRequest]) (*connect.Response[pb.GetSettingsResponse], error) {
	read := func() (*pb.GetSettingsResponse, error) {
		if l.b.calls.Add(1) == 1 {
			close(l.b.entered)
		}
		<-l.b.release
		return &pb.GetSettingsResponse{}, nil
	}
	var value *pb.GetSettingsResponse
	var err error
	if l.b.wrapped {
		value, err = contextio.Read(ctx, read)
	} else {
		value, err = read()
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(value), nil
}
func TestOverdueBackendAndContextIOKeepSlotAndQuarantine(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(map[bool]string{false: "callback", true: "contextio"}[wrapped], func(t *testing.T) {
			backend := &stalledReadBackend{release: make(chan struct{}), entered: make(chan struct{}), wrapped: wrapped}
			e := testEndpoint(t, New(backend))
			client := endpointClient(t, e)
			var once sync.Once
			defer func() { once.Do(func() { close(backend.release) }); e.link.DrainCalls(); e.admissionOwner().close() }()
			body, _ := proto.Marshal(&pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
			wire, err := client.Prepare(devicesec.Header{Purpose: devicesec.Command, Kind: 5, Machine: e.acct.DeviceID, Request: "overdue-read-request", Deadline: uint64(time.Now().Add(1500 * time.Millisecond).UnixMilli())}, nil, body)
			if err != nil {
				t.Fatal(err)
			}
			env, err := deviceauth.Envelope(wire)
			if err != nil {
				t.Fatal(err)
			}
			results := make(chan *pb.ForwardReply, 4)
			reply := func(r *pb.ForwardReply) { results <- r }
			e.dispatch(context.Background(), &pb.ForwardCall{CallId: 1, Command: env}, reply)
			select {
			case <-backend.entered:
			case <-time.After(time.Second):
				t.Fatal("backend was not admitted")
			}
			select {
			case r := <-results:
				if r.Outcome != "unknown" {
					t.Fatalf("overdue outcome=%s", r.Outcome)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("deadline did not report unknown")
			}
			if got := e.admission.scheduler.Active(); got[1] != 1 {
				t.Fatalf("slot released before real I/O exit: %v", got)
			}
			next := commandEnvelope(t, client, e.acct.DeviceID, "quarantined-read-request", &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
			e.dispatch(context.Background(), &pb.ForwardCall{CallId: 2, Command: next}, reply)
			select {
			case r := <-results:
				if r.CallId != 2 || r.Outcome != "unknown" || r.Reason != "busy" {
					t.Fatalf("quarantine result id=%d outcome=%s reason=%s", r.CallId, r.Outcome, r.Reason)
				}
			case <-time.After(time.Second):
				t.Fatal("quarantine did not reject new admission")
			}
			if backend.calls.Load() != 1 {
				t.Fatal("quarantine started another backend call")
			}
			healthy := commandEnvelope(t, client, e.acct.DeviceID, "healthy-other-resource", &pb.Command{Kind: &pb.Command_ListModels{ListModels: &pb.ListModelsRequest{}}})
			e.dispatch(context.Background(), &pb.ForwardCall{CallId: 3, Command: healthy}, reply)
			select {
			case r := <-results:
				if r.CallId != 3 || r.Outcome != "executed" {
					t.Fatal("unrelated resource was quarantined")
				}
			case <-time.After(time.Second):
				t.Fatal("unrelated resource stalled")
			}
			once.Do(func() { close(backend.release) })
			e.link.DrainCalls()
			if got := e.admission.scheduler.Active(); got != [3]int{} {
				t.Fatalf("actual exit leaked slot: %v", got)
			}
		})
	}
}

func TestLargeDecodedSendAndApprovalKeepTheirClass(t *testing.T) {
	backend := &approvalLoadBackend{release: make(chan struct{}), entered: make(chan string, 4)}
	close(backend.release)
	r := New(backend)
	fixedNow := time.Now()
	SetSchedulerClockForTest(r, func() time.Time { return fixedNow }, func(time.Duration, func()) func() bool {
		return func() bool { return false }
	})
	e := testEndpoint(t, r)
	client := endpointClient(t, e)
	defer func() { e.link.DrainCalls(); e.admissionOwner().close() }()
	message := strings.Repeat("x", 3<<20)
	for i, cmd := range []*pb.Command{
		{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "input-0", Text: message}}},
		{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: "approval-session", ApprovalId: "large-reason", Message: message}}},
	} {
		env := commandEnvelope(t, client, e.acct.DeviceID, fmt.Sprintf("large-decoded-request-%d", i), cmd)
		reply := make(chan *pb.ForwardReply, 1)
		e.dispatch(context.Background(), &pb.ForwardCall{Command: env}, func(r *pb.ForwardReply) { reply <- r })
		select {
		case r := <-reply:
			if r.Outcome != "executed" {
				t.Fatalf("legal decoded request %d refused: %s/%s", i, r.Outcome, r.Reason)
			}
		case <-t.Context().Done():
			t.Fatal("large decoded request stalled")
		}
	}
}

type inflightChunkBackend struct {
	Backend
	entered, release chan struct{}
	calls            atomic.Int32
}
type inflightChunkLocal struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	b *inflightChunkBackend
}

func (b *inflightChunkBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return inflightChunkLocal{b: b}
}
func (b *inflightChunkBackend) SessionIDs() []string { return []string{"session"} }
func (l inflightChunkLocal) GetSharedFileChunk(context.Context, *connect.Request[pb.GetSharedFileChunkRequest]) (*connect.Response[pb.GetSharedFileChunkResponse], error) {
	if l.b.calls.Add(1) == 1 {
		close(l.b.entered)
	}
	<-l.b.release
	return connect.NewResponse(&pb.GetSharedFileChunkResponse{Data: make([]byte, 768<<10), TotalBytes: proto.Uint64(1)}), nil
}
func TestInflightSharedFileDuplicateKeepsUncompressedResult(t *testing.T) {
	b := &inflightChunkBackend{entered: make(chan struct{}), release: make(chan struct{})}
	e := testEndpoint(t, New(b))
	client := endpointClient(t, e)
	var once sync.Once
	defer func() { once.Do(func() { close(b.release) }); e.link.DrainCalls(); e.admissionOwner().close() }()
	env := commandEnvelope(t, client, e.acct.DeviceID, "inflight-chunk-identity", &pb.Command{Kind: &pb.Command_GetSharedFileChunk{GetSharedFileChunk: &pb.GetSharedFileChunkRequest{SessionId: "session", FileId: "fixture"}}})
	replies := make(chan *pb.ForwardReply, 2)
	reply := func(r *pb.ForwardReply) { replies <- r }
	e.dispatch(context.Background(), &pb.ForwardCall{Command: env}, reply)
	<-b.entered
	registered := make(chan struct{}, 1)
	e.dispatch(&registeredWaitContext{Context: context.Background(), registered: registered}, &pb.ForwardCall{Command: env}, reply)
	select {
	case <-registered:
	case <-time.After(time.Second):
		t.Fatal("download duplicate was not coordinated")
	}
	if got := e.admission.scheduler.Active(); got != [3]int{0, 0, 1} {
		t.Fatalf("duplicate consumed a second bulk slot: %v", got)
	}
	once.Do(func() { close(b.release) })
	for range 2 {
		select {
		case r := <-replies:
			h := &pb.Header{}
			if err := proto.Unmarshal(r.Result.GetHeader(), h); err != nil {
				t.Fatal(err)
			}
			if h.Compression != pb.Compression_COMPRESSION_NONE {
				t.Fatal("coalesced block reply was compressed")
			}
		case <-time.After(time.Second):
			t.Fatal("download reply missing")
		}
	}
	if b.calls.Load() != 1 {
		t.Fatal("duplicate repeated the file read")
	}
}
