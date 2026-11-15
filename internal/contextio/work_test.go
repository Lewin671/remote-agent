package contextio

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestTrackedReadRetainsExecutionAfterWrapperTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		ctx, work := Track(ctx)
		release := make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })
		result := make(chan error, 1)
		go func() { _, err := Read(ctx, func() (int, error) { <-release; return 1, nil }); result <- err }()
		synctest.Wait()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if <-result != context.DeadlineExceeded {
			t.Fatal("wrapper did not expire")
		}
		select {
		case <-work.Violated():
		default:
			t.Fatal("unreturned I/O was not marked")
		}
		drained := make(chan struct{})
		go func() { work.Wait(); close(drained) }()
		synctest.Wait()
		select {
		case <-drained:
			t.Fatal("execution released while I/O is running")
		default:
		}
		releaseOnce.Do(func() { close(release) })
		synctest.Wait()
		<-drained
	})
}
