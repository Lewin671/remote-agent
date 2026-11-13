package relay

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRetryBackoffJitterCapAndRecovery(t *testing.T) {
	clock := time.Unix(0, 0)
	var waits []time.Duration
	timing := retryTiming{now: func() time.Time { return clock }, wait: func(_ context.Context, d time.Duration) { waits = append(waits, d); clock = clock.Add(d) }, jitter: func(d time.Duration) time.Duration { return d / 4 }}
	b := retryBackoff{base: time.Second, jitter: timing.jitter}
	for range 8 {
		timing.wait(context.Background(), b.failure())
	}
	want := []time.Duration{1250 * time.Millisecond, 2500 * time.Millisecond, 5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits %v", waits)
	}
	b.reset()
	if got := b.failure(); got != 1250*time.Millisecond {
		t.Fatal(got)
	}
	var g warningGate
	if !g.allow(timing.now()) || g.allow(timing.now().Add(59*time.Second)) || !g.allow(timing.now().Add(time.Minute)) {
		t.Fatal("warning rate not bounded")
	}
}
func TestDispatcherInitializationOutageUsesBackoff(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	c := &conn{}
	initializeTestSecurityKeys(t, c)
	c.acct.Server = "https://example.com"
	// No pending enrollment exists: the failure is entirely local, with no DNS.
	if err := c.acct.Save(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Unix(0, 0)
	var waits []time.Duration
	r := New(publishBackend{})
	r.timing = retryTiming{now: func() time.Time { return now }, jitter: func(time.Duration) time.Duration { return 0 }, wait: func(_ context.Context, d time.Duration) {
		waits = append(waits, d)
		now = now.Add(d)
		if len(waits) == 8 {
			cancel()
		}
	}}
	r.Run(ctx)
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits %v", waits)
	}
	if n := strings.Count(logs.String(), "security initialization failed"); n > 3 {
		t.Fatalf("warnings not limited: %d", n)
	}
}
