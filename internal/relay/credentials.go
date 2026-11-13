package relay

import (
	"context"
	"sync"

	"connectrpc.com/connect"
)

// credentials belongs to one HTTP component, never to a WebSocket session.
// Security and publication use different instances so a slow publication login
// cannot hold up membership synchronization. Only renewals share this mutex.
type credentials struct {
	mu    sync.Mutex
	token string
	login func(context.Context) (string, error)
}

func (c *credentials) get(ctx context.Context, rejected string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.token != "" && c.token != rejected {
		return c.token, nil
	}
	c.token = ""
	token, err := c.login(ctx)
	if err != nil {
		return "", err
	}
	c.token = token
	return token, nil
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
				return nil, err
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
