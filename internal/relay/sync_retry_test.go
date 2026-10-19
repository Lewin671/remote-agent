package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
)

func TestSyncErrorBoundary(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnauthenticated, connect.CodeUnavailable, connect.CodeCanceled, connect.CodeDeadlineExceeded, connect.CodePermissionDenied, connect.CodeInvalidArgument, connect.CodeNotFound, connect.CodeResourceExhausted, connect.CodeFailedPrecondition, connect.CodeInternal, connect.CodeUnknown} {
		err := connect.NewError(code, errors.New("private content"))
		rpc := rpcSyncError("put_session", err).(*syncError)
		want := code == connect.CodeUnauthenticated || code == connect.CodeUnavailable || code == connect.CodeCanceled || code == connect.CodeDeadlineExceeded
		if rpc.connection != want {
			t.Errorf("RPC %s: connection=%v", code, rpc.connection)
		}
		local := localSyncError("metadata", err).(*syncError)
		if local.connection {
			t.Errorf("backend %s classified as connection failure", code)
		}
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if !rpcSyncError("upload_events", fmt.Errorf("wrapped: %w", err)).(*syncError).connection {
			t.Errorf("RPC context error %v not propagated", err)
		}
	}
}

func TestSyncRetryBackoffLogsAndRecovery(t *testing.T) {
	old := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)
	now := time.Now()
	s := syncRetry{}
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute} {
		s.fail("bad-session", "events", now, time.Second)
		if s.delay != want || !s.next.Equal(now.Add(want)) {
			t.Fatalf("attempt %d: %+v", i, s)
		}
		now = s.next
	}
	if n := strings.Count(logs.String(), "session sync blocked"); n != 3 {
		t.Fatalf("rate limit: %d logs: %s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "category=events") || !strings.Contains(logs.String(), "retry_at=") {
		t.Fatal("diagnostic lacks error category or retry time")
	}
	s.recovered("bad-session")
	if s != (syncRetry{}) || !strings.Contains(logs.String(), "session sync recovered") {
		t.Fatal("successful sync did not reset backoff")
	}
	s.fail("bad-session", "metadata", now, time.Second)
	if s.delay != time.Second {
		t.Fatalf("recovery did not reset delay: %v", s.delay)
	}
}

func TestProductionSyncIntervalIsUnchanged(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	r := New(nil)
	if r.readyTimeout != 15*time.Second || r.syncRetryBase != time.Second {
		t.Fatalf("production timing: ready=%v, sync retry=%v", r.readyTimeout, r.syncRetryBase)
	}
	if r.syncInterval != 5*time.Second || min(time.Second, r.syncInterval) != time.Second {
		t.Fatalf("production sync interval = %v; want 5s with a 1s scheduler tick", r.syncInterval)
	}
}
