package daemon

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestRetryingCommitFailureUsesOnlyRateLimitedStorageLog(t *testing.T) {
	h := newHarness(t, Config{RetryInterval: time.Millisecond})
	if _, err := h.s.send("one", "r1"); err != nil {
		t.Fatal(err)
	}
	h.s.checkScreen()
	h.s.checkScreen()
	h.s.log.Close()
	sink := &diagnosticLogs{}
	old := slog.Default()
	slog.SetDefault(slog.New(sink))
	defer slog.SetDefault(old)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { h.s.injectLoop(ctx); close(done) }()
	<-done
	sink.mu.Lock()
	defer sink.mu.Unlock()
	n := 0
	for _, msg := range sink.messages {
		if msg == "inject" {
			t.Fatal("duplicate storage warnings bypassed rate limit")
		}
		if msg == "session storage failure" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("storage fault logs=%d; want one", n)
	}
}
