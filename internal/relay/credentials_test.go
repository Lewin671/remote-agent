package relay

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCredentialsCoalesceRenewalAndDropFailedToken(t *testing.T) {
	var logins atomic.Int32
	c := &credentials{login: func(context.Context) (string, error) { return fmt.Sprintf("test-token-%d", logins.Add(1)), nil }}
	old, err := c.get(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			token, err := c.get(context.Background(), old)
			if err != nil || token == old {
				t.Error("token was not renewed")
			}
		})
	}
	workers.Wait()
	if logins.Load() != 2 {
		t.Fatalf("concurrent renewal performed %d logins", logins.Load())
	}
	c.invalidate("test-token-1")
	if token, err := c.get(context.Background(), ""); err != nil || token != "test-token-2" {
		t.Fatal("stale response invalidated new credentials")
	}
	c.invalidate("test-token-2")
	c.login = func(context.Context) (string, error) {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("login refused"))
	}
	if token, err := c.get(context.Background(), ""); err == nil || token != "" {
		t.Fatal("retained refused credentials")
	}
}

func TestCredentialsWaitHonorsContextAndCoalescesFailure(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var attempts atomic.Int32
	now := time.Unix(0, 0)
	c := &credentials{now: func() time.Time { return now }, login: func(context.Context) (string, error) {
		attempts.Add(1)
		close(started)
		<-release
		return "", context.DeadlineExceeded
	}}
	leader := make(chan error, 1)
	go func() { _, err := c.get(context.Background(), ""); leader <- err }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			_, err := c.get(context.Background(), "")
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Error(err)
			}
		})
	}
	close(release)
	<-leader
	workers.Wait()
	if attempts.Load() != 1 {
		t.Fatalf("failed logins were serialized: %d", attempts.Load())
	}
	now = now.Add(time.Second)
	c.login = func(context.Context) (string, error) { attempts.Add(1); return "recovered", nil }
	if token, err := c.get(context.Background(), ""); err != nil || token != "recovered" {
		t.Fatal(err)
	}
}
