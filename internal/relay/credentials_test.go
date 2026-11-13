package relay

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
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
