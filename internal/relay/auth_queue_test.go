package relay

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

type heldMembershipClient struct {
	remoteagentv1connect.ClientServiceClient
	release chan struct{}
	calls   atomic.Int32
}

func (c *heldMembershipClient) GetSecurityStates(ctx context.Context, _ *connect.Request[pb.GetSecurityStatesRequest]) (*connect.Response[pb.GetSecurityStatesResponse], error) {
	c.calls.Add(1)
	<-c.release
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, context.Canceled
}

func TestUnknownEpochWaitQueueIsBoundedAndLeavesAuthenticationFree(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		e := testEndpoint(t, New(blockingScheduleBackend{calls: &calls}))
		client := endpointClient(t, e)
		membership := &heldMembershipClient{release: make(chan struct{})}
		e.security.client = membership
		a := e.admissionOwner()
		defer func() { close(membership.release); a.close(); e.security.Close() }()
		valid := commandEnvelope(t, client, e.acct.DeviceID, "healthy-auth-request", &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
		unknown := func(peer int) *pb.Envelope {
			wire, err := deviceauth.FromEnvelope(valid)
			if err != nil {
				t.Fatal(err)
			}
			h, err := deviceauth.ParseHeader(wire.Header)
			if err != nil {
				t.Fatal(err)
			}
			h.Epoch = 99
			h.Sender = fmt.Sprintf("%032x", peer+1)
			wire.Header, err = h.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			env, err := wire.Envelope()
			if err != nil {
				t.Fatal(err)
			}
			return env
		}
		results := make(chan *pb.ForwardReply, 16)
		reply := func(r *pb.ForwardReply) { results <- r }
		e.dispatch(context.Background(), &pb.ForwardCall{CallId: 0, Command: unknown(0)}, reply)
		synctest.Wait()
		e.dispatch(context.Background(), &pb.ForwardCall{CallId: 99, Command: unknown(0)}, reply)
		synctest.Wait()
		select {
		case result := <-results:
			if result.CallId != 99 || result.Reason != "busy" {
				t.Fatal("same peer obtained a second epoch wait")
			}
		default:
			t.Fatal("same peer was not rejected while global queue had room")
		}
		for i := 1; i < 8; i++ {
			e.dispatch(context.Background(), &pb.ForwardCall{CallId: uint64(i), Command: unknown(i)}, reply)
		}
		synctest.Wait()
		a.mu.Lock()
		pending := len(a.syncing)
		a.mu.Unlock()
		if pending != 8 {
			t.Fatalf("unknown epoch waiters=%d want 8", pending)
		}
		if membership.calls.Load() != 1 {
			t.Fatalf("membership owner was not shared: %d", membership.calls.Load())
		}
		for _, peer := range []int{0, 8} {
			e.dispatch(context.Background(), &pb.ForwardCall{CallId: 100 + uint64(peer), Command: unknown(peer)}, reply)
		}
		e.dispatch(context.Background(), &pb.ForwardCall{CallId: 200, Command: valid}, reply)
		synctest.Wait()
		busy, healthy := 0, false
		for len(results) > 0 {
			r := <-results
			if r.Reason == "busy" {
				busy++
			}
			if r.CallId == 200 && r.Outcome == "executed" {
				healthy = true
			}
		}
		if busy != 2 || !healthy {
			t.Fatalf("unknown waiters consumed auth workers or quota failed: busy=%d healthy=%v", busy, healthy)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		a.mu.Lock()
		pending = len(a.syncing)
		a.mu.Unlock()
		if pending != 0 {
			t.Fatalf("unknown epoch waits exceeded 5 seconds: %d", pending)
		}
	})
}
