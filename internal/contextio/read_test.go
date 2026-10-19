package contextio

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCancelledReadsBoundUncancellableWorkers(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, Limit)
	exited := make(chan struct{}, Limit)
	var calls atomic.Int32
	var group sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	for range Limit {
		group.Go(func() {
			_, err := Read(ctx, func() (int, error) {
				defer func() { exited <- struct{}{} }()
				calls.Add(1)
				entered <- struct{}{}
				<-release
				return 1, nil
			})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("read=%v", err)
			}
		})
	}
	for range Limit {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			cancel()
			close(release)
			t.Fatal("workers did not start")
		}
	}
	cancel()
	group.Wait() // The requests end, but the raw OS calls still hold all slots.
	for range Limit * 3 {
		group.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			_, err := Read(ctx, func() (int, error) { calls.Add(1); return 1, nil })
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("saturated reader=%v", err)
			}
		})
	}
	group.Wait()
	if n := calls.Load(); n != Limit {
		t.Errorf("spawned %d workers, want %d", n, Limit)
	}
	close(release)
	for range Limit {
		<-exited
	}
}
