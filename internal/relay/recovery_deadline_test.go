package relay

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestSecurityOwnerDeadlineReleasesTheNextPull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &conn{}
		initializeTestSecurity(t, c)
		s := c.security
		var calls atomic.Int32
		s.client = ownershipSecurityClient{read: func(ctx context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return connect.NewResponse(&pb.GetSecurityStatesResponse{HeadEpoch: s.endpoint.HeadEpoch()}), nil
		}}
		start := time.Now()
		err := s.refresh(context.Background())
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
			t.Fatal("shared pull retained beyond its recovery deadline", err)
		}
		if err = s.refresh(context.Background()); err != nil || calls.Load() != 2 {
			t.Fatal("expired pull was retained", err)
		}
	})
}
func TestSecurityCredentialOwnerDeadlineReleasesTheNextLogin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		c := accountCredentials(context.Background(), &Account{})
		c.timeout = 3 * time.Second
		c.login = func(ctx context.Context) (string, error) {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				return "", ctx.Err()
			}
			return "renewed", nil
		}
		defer c.close()
		start := time.Now()
		if _, err := c.get(context.Background(), ""); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
			t.Fatal("login owner did not expire", err)
		}
		time.Sleep(time.Second)
		if token, err := c.get(context.Background(), ""); token != "renewed" || err != nil || calls.Load() != 2 {
			t.Fatal("failed credential was retained", err)
		}
	})
}
