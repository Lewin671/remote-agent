package relay

import (
	"context"
	"sync"
	"time"

	"connectrpc.com/connect"
)

// credentials belongs to one HTTP component, never to a WebSocket session.
// Security and publication use different instances so a slow publication login
// cannot hold up membership synchronization. Only renewals share this mutex.
type loginFlight struct {
	done  chan struct{}
	token string
	err   error
}
type credentials struct {
	mu      sync.Mutex
	token   string
	login   func(context.Context) (string, error)
	flight  *loginFlight
	failed  *loginFlight
	retryAt time.Time
	now     func() time.Time
}

func (c *credentials) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
func (c *credentials) get(ctx context.Context, rejected string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	if c.token != "" && c.token != rejected {
		token := c.token
		c.mu.Unlock()
		return token, nil
	}
	if c.flight != nil {
		f := c.flight
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-f.done:
			return f.token, f.err
		}
	}
	if c.failed != nil && c.clock().Before(c.retryAt) {
		f := c.failed
		c.mu.Unlock()
		return "", f.err
	}
	c.token = ""
	f := &loginFlight{done: make(chan struct{})}
	c.flight = f
	c.mu.Unlock()
	token, err := c.login(ctx)
	c.mu.Lock()
	f.token, f.err = token, err
	if err == nil {
		c.token = token
		c.failed = nil
	} else {
		c.failed = f
		c.retryAt = c.clock().Add(time.Second)
	}
	c.flight = nil
	close(f.done)
	c.mu.Unlock()
	return token, err
}
func (c *credentials) invalidate(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == token {
		c.token = ""
	}
}
func (c *credentials) bearer() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			token, err := c.get(ctx, "")
			if err != nil {
				return nil, err
			}
			req.Header().Set("Authorization", "Bearer "+token)
			res, err := next(ctx, req)
			if connect.CodeOf(err) != connect.CodeUnauthenticated {
				return res, err
			}
			renewed, err := c.get(ctx, token)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, err)
			}
			req.Header().Set("Authorization", "Bearer "+renewed)
			res, err = next(ctx, req)
			if connect.CodeOf(err) == connect.CodeUnauthenticated {
				c.invalidate(renewed)
			}
			return res, err
		}
	}
}
func accountCredentials(acct *Account) *credentials {
	return &credentials{login: func(ctx context.Context) (string, error) { return login(ctx, acct) }}
}
