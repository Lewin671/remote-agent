package relay

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestCredentialFlightOutlivesCanceledLeaderAndKeepsSuccessor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		c := &credentials{login: func(ctx context.Context) (string, error) {
			calls.Add(1)
			select {
			case <-release:
				return "shared", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}}
		defer c.close()
		ctx, cancel := context.WithCancel(context.Background())
		leader := make(chan error, 1)
		follower := make(chan string, 1)
		go func() { _, err := c.get(ctx, ""); leader <- err }()
		synctest.Wait()
		go func() { value, _ := c.get(context.Background(), ""); follower <- value }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if err := <-leader; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if calls.Load() != 1 {
			t.Fatal("one waiter canceled the shared login")
		}
		select {
		case <-follower:
			t.Fatal("live follower lost its shared result")
		default:
		}
		close(release)
		synctest.Wait()
		if <-follower != "shared" || calls.Load() != 1 {
			t.Fatal("shared login restarted")
		}
	})
}

func TestCredentialRetiredResponseCannotClearOrInstallOverSuccessor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		oldRelease := make(chan struct{})
		newRelease := make(chan struct{})
		c := &credentials{login: func(context.Context) (string, error) {
			if calls.Add(1) == 1 {
				<-oldRelease
				return "obsolete", nil
			}
			<-newRelease
			return "current", nil
		}}
		defer c.close()
		defer func() {
			select {
			case <-oldRelease:
			default:
				close(oldRelease)
			}
			select {
			case <-newRelease:
			default:
				close(newRelease)
			}
		}()
		old := make(chan string, 1)
		next := make(chan string, 1)
		go func() { v, _ := c.get(context.Background(), ""); old <- v }()
		synctest.Wait()
		c.networkChanged()
		go func() { v, _ := c.get(context.Background(), ""); next <- v }()
		synctest.Wait()
		c.mu.Lock()
		successor := c.flight
		c.mu.Unlock()
		close(oldRelease)
		synctest.Wait()
		c.mu.Lock()
		same := c.flight == successor && c.token == ""
		c.mu.Unlock()
		if !same {
			t.Fatal("old login retired its successor or installed credentials")
		}
		close(newRelease)
		synctest.Wait()
		if <-old != "current" || <-next != "current" || calls.Load() != 2 {
			t.Fatal("retired waiter did not join successor")
		}
	})
}

type ownershipSecurityClient struct {
	remoteagentv1connect.ClientServiceClient
	read func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error)
}

func (c ownershipSecurityClient) GetSecurityStates(ctx context.Context, _ *connect.Request[pb.GetSecurityStatesRequest]) (*connect.Response[pb.GetSecurityStatesResponse], error) {
	return c.read(ctx)
}

func TestSecurityPullOutlivesCanceledLeader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &conn{}
		initializeTestSecurity(t, c)
		s := c.security
		release := make(chan struct{})
		var calls atomic.Int32
		var canceled atomic.Bool
		s.client = ownershipSecurityClient{read: func(ctx context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
			calls.Add(1)
			select {
			case <-release:
				return connect.NewResponse(&pb.GetSecurityStatesResponse{HeadEpoch: s.endpoint.HeadEpoch()}), nil
			case <-ctx.Done():
				canceled.Store(true)
				return nil, ctx.Err()
			}
		}}
		ctx, cancel := context.WithCancel(context.Background())
		leader := make(chan error, 1)
		follower := make(chan error, 1)
		go func() { leader <- s.refresh(ctx) }()
		synctest.Wait()
		go func() { follower <- s.refresh(context.Background()) }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if err := <-leader; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if canceled.Load() {
			t.Fatal("one waiter canceled the shared security HTTP")
		}
		close(release)
		synctest.Wait()
		if err := <-follower; err != nil || calls.Load() != 1 {
			t.Fatal("live waiter lost shared pull result", err)
		}
	})
}

func TestNetworkRetirementDoesNotAdmitAPullBeforeRetiringItsCredentials(t *testing.T) {
	credentials := &credentials{}
	retiring, retired, started, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	s := &securitySession{credentials: credentials, syncNow: make(chan struct{}, 1),
		syncing:    &securityRefresh{cancel: func() { close(retiring) }},
		beforeSync: func() error { close(started); return errors.New("fixture finished") }}
	t.Cleanup(func() { <-retired; <-finished })
	func() {
		credentials.mu.Lock()
		defer credentials.mu.Unlock()
		go func() { s.networkChanged(); close(retired) }()
		<-retiring
		go func() { _ = s.refresh(context.Background()); close(finished) }()
		select {
		case <-started:
			t.Fatal("successor pull entered before credential retirement settled")
		case <-time.After(100 * time.Millisecond):
		}
	}()
	<-finished
}
