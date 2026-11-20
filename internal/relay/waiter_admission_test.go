package relay

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

type registeredWaitContext struct {
	context.Context
	once       sync.Once
	registered chan<- struct{}
}

func (c *registeredWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { c.registered <- struct{}{} })
	return c.Context.Done()
}

func endpointClient(t *testing.T, e *endpoint) *deviceauth.Endpoint {
	t.Helper()
	keys := initializeEndpointSecurityKeys(t, e)
	pin, _, _ := e.security.endpoint.Snapshot()
	member := keys.states[0].Core.Members[0]
	lease, err := deviceauth.BuildLease(keys.states[1], keys.phone, keys.states[1].Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	client, err := deviceauth.InitEndpoint("", pin, keys.states, []*devicesec.RecoveryStatement{nil, nil}, member.ID, keys.phone.Seed(), keys.phoneX, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}
func commandEnvelope(t *testing.T, client *deviceauth.Endpoint, machine, id string, cmd *pb.Command) *pb.Envelope {
	t.Helper()
	cmd.ProtocolVersion = protocol.Version
	body, err := proto.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	session := ""
	if kind := cmd.ProtoReflect().WhichOneof(cmd.ProtoReflect().Descriptor().Oneofs().ByName("kind")); kind != nil && cmd.GetSchedule() == nil {
		message := cmd.ProtoReflect().Get(kind).Message()
		if field := message.Descriptor().Fields().ByName("session_id"); field != nil {
			session = message.Get(field).String()
		}
	}
	wire, err := client.Prepare(devicesec.Header{Purpose: devicesec.Command, Kind: 5, Machine: machine, Session: session, Request: id, Deadline: uint64(time.Now().Add(time.Minute).UnixMilli())}, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	env, err := deviceauth.Envelope(wire)
	if err != nil {
		t.Fatal(err)
	}
	return env
}
func TestFullDuplicateWaitTableLeavesReaderAndFreshAdmissionRoom(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	e := testEndpoint(t, New(blockingScheduleBackend{calls: &calls, entered: entered, release: release}))
	client := endpointClient(t, e)
	defer func() { close(release); e.link.DrainCalls(); e.admissionOwner().close() }()
	env := commandEnvelope(t, client, e.acct.DeviceID, "reader-original-request", &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "pause"}}})
	results := make(chan *pb.ForwardReply, deviceauth.MaxWaitersPerKey+2)
	reply := func(r *pb.ForwardReply) { results <- r }
	e.dispatch(context.Background(), &pb.ForwardCall{Command: env}, reply)
	<-entered
	registered := make(chan struct{}, 1)
	for i := range deviceauth.MaxWaitersPerKey {
		waiting := &registeredWaitContext{Context: context.Background(), registered: registered}
		e.dispatch(waiting, &pb.ForwardCall{CallId: uint64(i + 1), Command: env}, reply)
		select {
		case <-registered:
		case <-time.After(time.Second):
			t.Fatal("duplicate did not reach independent waiter table")
		}
	}
	e.dispatch(context.Background(), &pb.ForwardCall{CallId: 100, Command: env}, reply)
	select {
	case r := <-results:
		if r.CallId != 100 || r.Outcome != "unknown" || r.Reason != "busy" {
			t.Fatalf("full waiter reply: id=%d outcome=%s reason=%s", r.CallId, r.Outcome, r.Reason)
		}
	case <-time.After(time.Second):
		t.Fatal("full wait table blocked")
	}
	if got := e.admission.scheduler.Active(); got != [3]int{0, 1, 0} {
		t.Fatalf("duplicates took execution slots: %v", got)
	}
	fresh := commandEnvelope(t, client, e.acct.DeviceID, "reader-fresh-request", &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
	e.dispatch(context.Background(), &pb.ForwardCall{CallId: 101, Command: fresh}, reply)
	select {
	case r := <-results:
		if r.CallId != 101 || r.Outcome != "executed" {
			t.Fatalf("fresh result: id=%d outcome=%s reason=%s", r.CallId, r.Outcome, r.Reason)
		}
	case <-time.After(time.Second):
		t.Fatal("waiters exhausted authentication workers")
	}
}
