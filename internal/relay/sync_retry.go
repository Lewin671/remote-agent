package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
)

// Only RPC failures can invalidate the connection. A backend error (even one
// wrapping a context or Connect error) belongs to the session being read.
type syncError struct {
	category   string
	connection bool
	err        error
}

func (e *syncError) Error() string { return fmt.Sprintf("%s: %v", e.category, e.err) }
func (e *syncError) Unwrap() error { return e.err }

func localSyncError(category string, err error) error {
	return &syncError{category: category, err: err}
}

func rpcSyncError(operation string, err error) error {
	code := connect.CodeOf(err)
	connection := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	switch code {
	case connect.CodeUnauthenticated, connect.CodeUnavailable, connect.CodeCanceled, connect.CodeDeadlineExceeded:
		connection = true
	}
	return &syncError{category: operation + "/" + code.String(), connection: connection, err: err}
}

// syncRetry is owned by the scheduler, never by a session worker. Log only
// categories: backend and server errors may contain private session content.
type syncRetry struct {
	delay  time.Duration
	next   time.Time
	logged time.Time
}

func (s *syncRetry) fail(id, category string, now time.Time, base time.Duration) {
	s.delay = min(max(base, s.delay*2), time.Minute)
	s.next = now.Add(s.delay)
	if s.logged.IsZero() || now.Sub(s.logged) >= time.Minute {
		slog.Warn("relay: session sync blocked", "session", id, "category", category, "retry_at", s.next.UTC().Format(time.RFC3339))
		s.logged = now
	}
}

func (s *syncRetry) recovered(id string) {
	if s.delay != 0 {
		slog.Info("relay: session sync recovered", "session", id)
	}
	*s = syncRetry{}
}
