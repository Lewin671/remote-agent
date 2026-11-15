package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/proto"
)

type approvalLoadBackend struct {
	Backend
	release chan struct{}
	entered chan string
}
type approvalLoadLocal struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	b *approvalLoadBackend
}

func (b *approvalLoadBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return approvalLoadLocal{b: b}
}
func (b *approvalLoadBackend) SessionIDs() []string {
	out := []string{"approval-session"}
	for i := range 16 {
		out = append(out, fmt.Sprintf("input-%d", i))
	}
	return out
}
func (l approvalLoadLocal) GetSettings(ctx context.Context, _ *connect.Request[pb.GetSettingsRequest]) (*connect.Response[pb.GetSettingsResponse], error) {
	l.b.entered <- "ordinary"
	select {
	case <-l.b.release:
		return connect.NewResponse(&pb.GetSettingsResponse{}), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (l approvalLoadLocal) Send(ctx context.Context, _ *connect.Request[pb.SendRequest]) (*connect.Response[pb.SendResponse], error) {
	l.b.entered <- "input"
	select {
	case <-l.b.release:
		return connect.NewResponse(&pb.SendResponse{}), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (l approvalLoadLocal) ResolveApproval(context.Context, *connect.Request[pb.ResolveApprovalRequest]) (*connect.Response[pb.ResolveApprovalResponse], error) {
	return connect.NewResponse(&pb.ResolveApprovalResponse{}), nil
}

// Run separately on an otherwise idle host. Measures the actual authentication,
// scheduler and result-sealing entry, without network/model time in the queue
// metric. Persistent-disk and native CLI acceptance are separate measurements.
func TestApprovalAdmissionMeasurements(t *testing.T) {
	output := os.Getenv("RA_1B_APPROVAL_METRICS")
	if output == "" {
		t.Skip("set RA_1B_APPROVAL_METRICS for admission timing")
	}
	backend := &approvalLoadBackend{release: make(chan struct{}), entered: make(chan string, 32)}
	e := testEndpoint(t, New(backend))
	client := endpointClient(t, e)
	defer func() { close(backend.release); e.link.DrainCalls(); e.admissionOwner().close() }()
	var mu sync.Mutex
	var delays []float64
	granted := 0
	e.r.observeApproval = func(acquired bool, d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		delays = append(delays, float64(d)/float64(time.Millisecond))
		if acquired {
			granted++
		}
	}
	seal := func(id, session string, cmd *pb.Command) *pb.Envelope {
		cmd.ProtocolVersion = protocol.Version
		body, err := proto.Marshal(cmd)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := client.Prepare(deviceauth.Header{Purpose: deviceauth.Command, Kind: 5, Machine: e.acct.DeviceID, Session: session, Request: id, Deadline: uint64(time.Now().Add(time.Minute).UnixMilli())}, nil, body)
		if err != nil {
			t.Fatal(err)
		}
		env, err := wire.Envelope()
		if err != nil {
			t.Fatal(err)
		}
		return env
	}
	for i := range 4 {
		env := seal(fmt.Sprintf("ordinary-load-%02d", i), "", &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
		e.dispatch(context.Background(), &pb.ForwardCall{Command: env}, func(*pb.ForwardReply) {})
		select {
		case <-backend.entered:
		case <-time.After(time.Second):
			t.Fatal("ordinary load did not start")
		}
	}
	env := seal("input-load-prime", "input-0", &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "input-0", Text: "load"}}})
	e.dispatch(context.Background(), &pb.ForwardCall{Command: env}, func(*pb.ForwardReply) {})
	select {
	case <-backend.entered:
	case <-time.After(time.Second):
		t.Fatal("input load did not start")
	}
	for i := range 64 {
		session := fmt.Sprintf("input-%d", i%16)
		env := seal(fmt.Sprintf("input-flood-%04d", i), session, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: session, Text: "load"}}})
		e.dispatch(context.Background(), &pb.ForwardCall{Command: env}, func(*pb.ForwardReply) {})
	}
	const samples = 100
	completed, authBusy, queueBusy := 0, 0, 0
	for i := range samples {
		reply := make(chan *pb.ForwardReply, 1)
		env := seal(fmt.Sprintf("approval-measure-%04d", i), "approval-session", &pb.Command{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: "approval-session", ApprovalId: fmt.Sprintf("approval-%d", i), Allow: true}}})
		e.dispatch(context.Background(), &pb.ForwardCall{Command: env}, func(r *pb.ForwardReply) { reply <- r })
		select {
		case r := <-reply:
			if r.EntryFailure {
				authBusy++
				continue
			}
			wire, err := deviceauth.FromEnvelope(r.Result)
			if err != nil {
				t.Fatal(err)
			}
			h, plain, err := client.Open(wire, nil, true, uint64(time.Now().UnixMilli()))
			if err != nil {
				t.Fatal(err)
			}
			plain, err = deviceauth.Unpack(h, plain)
			if err != nil {
				t.Fatal(err)
			}
			result := &pb.CommandResult{}
			if err = proto.Unmarshal(plain, result); err != nil {
				t.Fatal(err)
			}
			if result.Outcome == "executed" && result.ErrorCode == "" {
				completed++
			} else if result.Outcome == "unknown" && result.Reason == "busy" {
				queueBusy++
			} else {
				t.Fatalf("unexpected approval result: %s/%s", result.Outcome, result.Reason)
			}
		case <-time.After(time.Second):
			t.Fatal("approval response timed out")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	sort.Float64s(delays)
	q := func(p float64) float64 {
		if len(delays) == 0 {
			return -1
		}
		return delays[int(math.Ceil(float64(len(delays))*p))-1]
	}
	summary := map[string]any{"attempts": samples, "authenticated_queue_decisions": len(delays), "acquired": granted, "completed": completed, "auth_backpressure": authBusy, "queue_backpressure": queueBusy, "acquired_fraction": float64(granted) / samples, "completed_fraction": float64(completed) / samples, "queue_ms": map[string]float64{"p50": q(.5), "p95": q(.95), "p99": q(.99)}, "load": map[string]int{"ordinary_running": 4, "input_running": 1, "additional_input_attempts": 64}, "scope": "production daemon authentication/admission/result sealing; in-memory security fixture and bounded fake backend; no model or network time"}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(output, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if q(.99) > 100 || granted < 90 || completed < 90 {
		t.Fatalf("approval budget/availability: p99=%.3f ms acquired=%d completed=%d of %d", q(.99), granted, completed, samples)
	}
}
