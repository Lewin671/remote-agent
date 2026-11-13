package relay

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
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

func TestFullDuplicateWaitTableLeavesReaderAndFreshAdmissionRoom(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &conn{r: New(nil)}
	keys := initializeTestSecurityKeys(t, c)
	pin, _, _ := c.security.endpoint.Snapshot()
	member := keys.states[0].Core.Members[0]
	lease, err := deviceauth.BuildLease(keys.states[1], keys.phone, keys.states[1].Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	client, err := deviceauth.InitEndpoint("", pin, keys.states, []*deviceauth.RecoveryStatement{nil, nil}, member.ID, keys.phone.Seed(), keys.phoneX, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	body, _ := proto.Marshal(&pb.Command{Kind: &pb.Command_UpdateSettings{UpdateSettings: &pb.UpdateSettingsRequest{}}})
	makeWire := func(id string) deviceauth.Wire {
		w, err := client.Prepare(deviceauth.Header{Purpose: deviceauth.Command, Kind: 5, Machine: c.acct.DeviceID, Request: id, Deadline: now + 60000}, nil, body)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	original, fresh := makeWire("reader-original-request"), makeWire("reader-fresh-request")
	running, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	first := make(chan error, 1)
	go func() {
		_, err := c.security.acceptWith(ctx, original, nil, now, c.acquireCall, func(deviceauth.Header, []byte) (deviceauth.Completion, error) {
			close(running)
			<-release
			return deviceauth.Final(nil), nil
		})
		first <- err
	}()
	<-running
	for range maxConcurrentCalls - 1 {
		c.r.calls <- struct{}{}
	}
	frames := make(chan *pb.ServerFrame)
	registered := make(chan struct{}, deviceauth.MaxWaiters)
	fullReply, slotQueue, newReply := make(chan error, 1), make(chan struct{}, 1), make(chan error, 1)
	var workers sync.WaitGroup
	reader := make(chan error, 1)
	defer func() { cancel(); releaseOnce.Do(func() { close(release) }); <-reader; workers.Wait(); <-first }()
	go func() {
		reader <- readForwardCalls(ctx, ctx, c.r.admissions, &workers, func() (*pb.ServerFrame, error) {
			select {
			case f := <-frames:
				return f, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}, func(ctx context.Context, call *pb.ForwardCall) {
			if call.CallId <= uint64(deviceauth.MaxWaiters) {
				_, err := c.security.acceptWith(&registeredWaitContext{Context: ctx, registered: registered}, original, nil, now, c.acquireCall, nil)
				if err != nil && err != deviceauth.ErrOutcomeUnknown {
					t.Error(err)
				}
			} else if call.CallId == uint64(deviceauth.MaxWaiters+1) {
				_, err := c.security.acceptWith(ctx, original, nil, now, c.acquireCall, nil)
				fullReply <- err
			} else {
				acquire := func(ctx context.Context) (func(), error) { slotQueue <- struct{}{}; return c.acquireCall(ctx) }
				_, err := c.security.acceptWith(ctx, fresh, nil, now, acquire, func(deviceauth.Header, []byte) (deviceauth.Completion, error) { return deviceauth.Final(nil), nil })
				newReply <- err
			}
		})
	}()
	send := func(id int) {
		t.Helper()
		select {
		case frames <- &pb.ServerFrame{Kind: &pb.ServerFrame_ForwardCall{ForwardCall: &pb.ForwardCall{CallId: uint64(id)}}}:
		case <-time.After(5 * time.Second):
			t.Fatal("waiters stalled reader")
		}
	}
	for i := 1; i <= deviceauth.MaxWaiters; i++ {
		send(i)
	}
	for range deviceauth.MaxWaiters {
		select {
		case <-registered:
		case <-time.After(5 * time.Second):
			t.Fatal("waiters not registered")
		}
	}
	if len(c.r.calls) != maxConcurrentCalls {
		t.Fatal("duplicate changed execution slots")
	}
	send(deviceauth.MaxWaiters + 1)
	select {
	case err := <-fullReply:
		if !errors.Is(err, deviceauth.ErrOutcomeUnknown) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("full wait table blocked reader")
	}
	send(deviceauth.MaxWaiters + 2)
	select {
	case <-slotQueue:
	case <-time.After(time.Second):
		t.Fatal("fresh request could not reach slot queue")
	}
	<-c.r.calls // one of the seven synthetic occupied slots becomes free
	select {
	case err := <-newReply:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh request could not execute")
	}
	if cap(c.r.admissions) != maxConcurrentCalls+deviceauth.MaxWaiters+16 {
		t.Fatal("production budget drift")
	}
}
