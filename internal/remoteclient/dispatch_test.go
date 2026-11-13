package remoteclient

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"google.golang.org/protobuf/proto"
)

type dispatchService struct {
	remoteagentv1connect.UnimplementedClientServiceHandler
	forward func(context.Context, *connect.Request[pb.ForwardRequest]) (*connect.Response[pb.ForwardResponse], error)
}

func (s *dispatchService) Forward(ctx context.Context, req *connect.Request[pb.ForwardRequest]) (*connect.Response[pb.ForwardResponse], error) {
	return s.forward(ctx, req)
}

func refusedAttempt(req *connect.Request[pb.ForwardRequest]) error {
	err := connect.NewError(connect.CodeUnavailable, errors.New("fixture offline"))
	detail, _ := connect.NewErrorDetail(&pb.NotForwarded{AttemptId: req.Msg.AttemptId, Reason: "machine_offline"})
	err.AddDetail(detail)
	return err
}

func expectDispatchOutcome(t *testing.T, err error, want outcome.Category) {
	t.Helper()
	var command *CommandError
	if !errors.As(err, &command) || command.Outcome != want {
		t.Fatalf("dispatch evidence: got %v, want %s", err, want)
	}
}

func restoredEnvelope(t *testing.T, env *pb.Envelope) *pb.Envelope {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pending-command.pb")
	bytes, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	bytes, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	restored := &pb.Envelope{}
	if err = proto.Unmarshal(bytes, restored); err != nil {
		t.Fatal(err)
	}
	return restored
}

func TestRestoredDispatchEvidenceSurvivesNotForwarded(t *testing.T) {
	for _, kind := range []string{"reopen-after-lost-result", "serialized-copy", "reseal", "external-client", "seal-reused-id", "changed-first-envelope", "raw-api-dispatch"} {
		t.Run(kind, func(t *testing.T) {
			client, machine, mid := syntheticTerminalSecurity(t)
			var mode atomic.Int32 // 0: refused, 1: execute then lose reply, 2: cached reply.
			var executions atomic.Int32
			var result *pb.ForwardResponse
			var resultMu sync.Mutex
			backend := &payloadServer{endpoint: machine}
			handler := &dispatchService{forward: func(ctx context.Context, req *connect.Request[pb.ForwardRequest]) (*connect.Response[pb.ForwardResponse], error) {
				if mode.Load() == 0 {
					return nil, refusedAttempt(req)
				}
				resultMu.Lock()
				defer resultMu.Unlock()
				if mode.Load() == 1 {
					response, err := backend.Forward(ctx, req)
					if err != nil {
						return nil, err
					}
					result = response.Msg
					executions.Add(1)
					return nil, connect.NewError(connect.CodeUnavailable, errors.New("fixture lost executed reply"))
				}
				cached := proto.Clone(result).(*pb.ForwardResponse)
				cached.AttemptId = req.Msg.AttemptId
				return connect.NewResponse(cached), nil
			}}
			_, service := remoteagentv1connect.NewClientServiceHandler(handler)
			server := httptest.NewServer(service)
			t.Cleanup(server.Close)
			client.api = remoteagentv1connect.NewClientServiceClient(server.Client(), server.URL)
			ctx := context.Background()
			cmd := &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: "/work/example"}}}
			deadline := time.Now().Add(time.Minute)
			seal := func(id string) *pb.Envelope {
				env, err := client.Seal(mid, "", id, deadline, cmd)
				if err != nil {
					t.Fatal(err)
				}
				return env
			}
			// Positive controls: provenance and an explicit never-dispatched assertion
			// still allow not_sent. Neither is the conservative default for imports.
			fresh := seal(NewRequestID())
			for range 2 {
				_, err := client.Forward(ctx, mid, fresh)
				expectDispatchOutcome(t, err, outcome.NotSent)
			}
			never := seal(NewRequestID())
			declared := NewClient(server.URL, "fixture", client.DeviceID, client.endpoint)
			_, err := declared.Forward(ctx, mid, restoredEnvelope(t, never), NeverDispatched)
			expectDispatchOutcome(t, err, outcome.NotSent)

			id := NewRequestID()
			env := seal(id)
			var prior []DispatchEvidence
			switch kind {
			case "reopen-after-lost-result":
				mode.Store(1)
				_, err = client.Forward(ctx, mid, env)
				expectDispatchOutcome(t, err, outcome.Unknown)
				if executions.Load() != 1 {
					t.Fatal("initial request was not executed")
				}
				env = restoredEnvelope(t, env)
				path := filepath.Join(t.TempDir(), "client.db")
				if err = client.endpoint.Save(path); err != nil {
					t.Fatal(err)
				}
				if err = client.endpoint.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := deviceauth.OpenEndpoint(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { reopened.Close() })
				client = NewClient(server.URL, "fixture", client.DeviceID, reopened)
			case "serialized-copy":
				env = restoredEnvelope(t, env)
			case "reseal":
				env, err = client.Reseal(env)
				if err != nil {
					t.Fatal(err)
				}
			case "external-client":
				client = NewClient(server.URL, "fixture", client.DeviceID, client.endpoint)
			case "seal-reused-id":
				env = seal(id)
			case "changed-first-envelope":
				changed, err := client.Reseal(env)
				if err != nil {
					t.Fatal(err)
				}
				proto.Reset(env)
				proto.Merge(env, changed)
			case "raw-api-dispatch":
				mode.Store(1)
				_, err = client.API().Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: mid, Command: env, AttemptId: NewRequestID()}))
				if err == nil || executions.Load() != 1 {
					t.Fatal("raw request did not lose its executed reply")
				}
				prior = []DispatchEvidence{MayHaveBeenDispatched}
			}
			mode.Store(0)
			_, err = client.Forward(ctx, mid, env, prior...)
			expectDispatchOutcome(t, err, outcome.Unknown)
			// Once recorded, prior evidence cannot be erased by a later declaration.
			_, err = client.Forward(ctx, mid, env, NeverDispatched)
			expectDispatchOutcome(t, err, outcome.Unknown)
			if kind == "reopen-after-lost-result" || kind == "raw-api-dispatch" {
				mode.Store(2)
				answer, err := client.Forward(ctx, mid, env)
				if err != nil || answer.Outcome != "executed" || executions.Load() != 1 {
					t.Fatalf("bound final proof did not settle restoration: %v", err)
				}
			}
		})
	}
}

func TestConcurrentRestoredRefusalsKeepPriorEvidence(t *testing.T) {
	owner, _, mid := syntheticTerminalSecurity(t)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	handler := &dispatchService{forward: func(ctx context.Context, req *connect.Request[pb.ForwardRequest]) (*connect.Response[pb.ForwardResponse], error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return nil, refusedAttempt(req)
	}}
	_, service := remoteagentv1connect.NewClientServiceHandler(handler)
	server := httptest.NewServer(service)
	t.Cleanup(server.Close)
	env, err := owner.Seal(mid, "", NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: "/work/example"}}})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(server.URL, "fixture", owner.DeviceID, owner.endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	replies := make(chan error, 2)
	go func() { _, err := client.Forward(ctx, mid, env); replies <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { _, err := client.Forward(ctx, mid, env, NeverDispatched); replies <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	releaseOnce.Do(func() { close(release) })
	for range 2 {
		expectDispatchOutcome(t, <-replies, outcome.Unknown)
	}
}
