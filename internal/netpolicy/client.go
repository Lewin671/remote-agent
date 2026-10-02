// Package netpolicy defines the transport policy for authenticated relay requests.
package netpolicy

import (
	"errors"
	"net/http"
	"time"
)

// Client refuses redirects: 307/308 can resend credentials in request bodies,
// and a redirected WebSocket can receive a bearer token in its hello frame.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("relay redirects are not allowed")
	}}
}
