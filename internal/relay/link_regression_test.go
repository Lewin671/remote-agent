package relay

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

type blockingScheduleBackend struct {
	Backend
	calls   *atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (b blockingScheduleBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return blockingScheduleLocal{calls: b.calls, entered: b.entered, release: b.release}
}

type blockingScheduleLocal struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	calls   *atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (l blockingScheduleLocal) Schedule(ctx context.Context, _ *connect.Request[pb.ScheduleRequest]) (*connect.Response[pb.ScheduleResponse], error) {
	if l.calls.Add(1) == 1 {
		close(l.entered)
	}
	<-l.release
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.ScheduleResponse{ResultJson: []byte(`{"ok":true}`)}), nil
}

type scriptPath struct {
	mu     sync.Mutex
	writes [][]byte
	readCh chan []byte
	closed chan struct{}
}

func newScriptPath(frames ...[]byte) *scriptPath {
	ch := make(chan []byte, len(frames)+4)
	for _, f := range frames {
		ch <- f
	}
	return &scriptPath{readCh: ch, closed: make(chan struct{})}
}

func (p *scriptPath) Read(ctx context.Context) ([]byte, error) {
	select {
	case b := <-p.readCh:
		return b, nil
	case <-p.closed:
		return nil, errors.New("path closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *scriptPath) Write(_ context.Context, b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, append([]byte(nil), b...))
	return nil
}

func (p *scriptPath) Finish() { p.Close() }

func (p *scriptPath) Close() {
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
}

// Regression: canceling a WS path after readLoop hands a ForwardCall to
// Link.Dispatch must not cancel the admitted execution on endpoint.handleCall,
// and the durable result remains recoverable by replaying the same sealed request.
func TestReadLoopPathCancelLeavesAdmittedExecutionDurable(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	r := New(blockingScheduleBackend{calls: &calls, entered: entered, release: release})
	procCtx, cancelProc := context.WithCancel(context.Background())
	defer cancelProc()
	link := transport.NewLink(procCtx)
	defer func() { releaseOnce.Do(func() { close(release) }); link.Close() }()

	ep := &endpoint{r: r, lifetime: link.Context(), link: link}
	keys := initializeEndpointSecurityKeys(t, ep)
	pin, _, _ := ep.security.endpoint.Snapshot()
	member := keys.states[0].Core.Members[0]
	lease, err := deviceauth.BuildLease(keys.states[1], keys.phone, keys.states[1].Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	client, err := deviceauth.InitEndpoint("", pin, keys.states, []*devicesec.RecoveryStatement{nil, nil}, member.ID, keys.phone.Seed(), keys.phoneX, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	now := uint64(time.Now().UnixMilli())
	body, _ := proto.Marshal(&pb.Command{ProtocolVersion: protocol.PeerVersion, Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "pause"}}})
	wire, err := client.Prepare(devicesec.Header{Kind: 5, Purpose: devicesec.Command, Machine: ep.acct.DeviceID, Request: "link-path-disconnect-regression", Deadline: now + 60000}, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	env, err := deviceauth.Envelope(wire)
	if err != nil {
		t.Fatal(err)
	}
	frameBytes, err := proto.Marshal(&pb.ServerFrame{Kind: &pb.ServerFrame_ForwardCall{ForwardCall: &pb.ForwardCall{CallId: 1, AttemptId: "att-1", Command: env}}})
	if err != nil {
		t.Fatal(err)
	}

	pathCtx, cancelPath := context.WithCancel(context.Background())
	path := newScriptPath(frameBytes)
	disp := &dispatcher{ctx: procCtx, link: link, endpoint: ep, security: ep.security, acct: ep.acct}
	c := &relaySession{pathLifetime: pathCtx, dispatcher: disp, path: path}

	readDone := make(chan error, 1)
	go func() { readDone <- c.readLoop(pathCtx) }()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not enter")
	}
	cancelPath()
	path.Close()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("readLoop returned nil after path cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("readLoop did not stop on path cancel")
	}

	releaseOnce.Do(func() { close(release) })
	link.DrainCalls()

	// Replay the same sealed command on a fresh path: the durable result must be returned.
	replayed := make(chan *pb.ForwardReply, 1)
	ep.dispatch(context.Background(), &pb.ForwardCall{CallId: 2, AttemptId: "att-2", Command: env}, func(rep *pb.ForwardReply) {
		replayed <- rep
	})
	rep := <-replayed
	if rep.Outcome != "executed" || rep.Result == nil {
		t.Fatalf("unexpected replay reply: outcome=%s reason=%s", rep.Outcome, rep.Reason)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 execution, got %d", calls.Load())
	}
}
