package relay

import (
	"context"
	"errors"
	"sync"
	"time"

	"connectrpc.com/connect"
)

// credentials belongs to one HTTP component, never to a WebSocket session.
// Security and publication use different instances so a slow publication login
// cannot hold up membership synchronization. Only renewals share this mutex.
type loginFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	retired bool
	token   string
	err     error
}
type credentials struct {
	mu       sync.Mutex
	lifetime context.Context
	workers  sync.WaitGroup
	closed   bool
	timeout  time.Duration
	token    string
	login    func(context.Context) (string, error)
	flight   *loginFlight
	failed   *loginFlight
	retryAt  time.Time
	now      func() time.Time
}

func (c *credentials) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
func (c *credentials) get(ctx context.Context, rejected string) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		c.mu.Lock()
		if c.closed || (c.lifetime != nil && c.lifetime.Err() != nil) {
			c.mu.Unlock()
			return "", context.Canceled
		}
		if c.token != "" && c.token != rejected {
			token := c.token
			c.mu.Unlock()
			return token, nil
		}
		f := c.flight
		if f == nil {
			if c.failed != nil && c.clock().Before(c.retryAt) {
				err := c.failed.err
				c.mu.Unlock()
				return "", err
			}
			owner := c.lifetime
			if owner == nil {
				owner = context.Background()
			}
			budget := c.timeout
			if budget == 0 {
				budget = 60 * time.Second
			}
			round, cancel := context.WithTimeout(owner, budget)
			f = &loginFlight{done: make(chan struct{}), cancel: cancel}
			c.flight = f
			c.token = ""
			c.workers.Go(func() {
				token, err := c.login(round)
				c.mu.Lock()
				if round.Err() != nil {
					err = round.Err()
				}
				f.token, f.err = token, err
				if c.flight == f {
					c.flight = nil
					if err == nil {
						c.token = token
						c.failed = nil
					} else if !errors.Is(err, context.Canceled) {
						c.failed = f
						c.retryAt = c.clock().Add(time.Second)
					}
				}
				close(f.done)
				c.mu.Unlock()
				cancel()
			})
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-f.done:
			c.mu.Lock()
			retired := f.retired
			c.mu.Unlock()
			if retired {
				continue
			}
			return f.token, f.err
		}
	}
}

func (c *credentials) networkChanged() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f := c.flight; f != nil {
		c.flight = nil
		f.retired = true
		f.cancel()
	}
	c.failed = nil
}
func (c *credentials) close() {
	c.mu.Lock()
	c.closed = true
	if f := c.flight; f != nil {
		c.flight = nil
		f.cancel()
	}
	c.mu.Unlock()
	c.workers.Wait()
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
func accountCredentials(ctx context.Context, acct *Account) *credentials {
	return &credentials{lifetime: ctx, login: func(ctx context.Context) (string, error) { return login(ctx, acct) }}
}
