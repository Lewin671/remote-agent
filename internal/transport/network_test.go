package transport

import (
	"context"
	"crypto/sha256"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestNetworkObservationsCoalesceAndNoticeSameInterfaceAddressChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		hints := make(chan struct{})
		ticks := make(chan time.Time)
		scanned := make(chan struct{})
		var snapshot atomic.Value
		snapshot.Store(sha256.Sum256([]byte("interface-7/address-old")))
		var stopped atomic.Bool
		out := observeNetworkChanges(ctx, hints, ticks, func() [32]byte {
			value := snapshot.Load().([32]byte)
			scanned <- struct{}{}
			return value
		}, func() { stopped.Store(true) })
		<-scanned
		synctest.Wait()
		for range 1000 {
			hints <- struct{}{}
			<-scanned
		}
		synctest.Wait()
		select {
		case <-out:
			t.Fatal("duplicate hints advanced generation")
		default:
		}
		snapshot.Store(sha256.Sum256([]byte("interface-7/address-new")))
		hints <- struct{}{}
		<-scanned
		if generation := <-out; generation != 1 {
			t.Fatalf("same-interface change: %d", generation)
		}
		for n := 2; n <= 5; n++ {
			snapshot.Store(sha256.Sum256([]byte{byte(n)}))
			ticks <- time.Now()
			<-scanned
		}
		synctest.Wait()
		if generation := <-out; generation != 5 {
			t.Fatalf("did not retain newest generation: %d", generation)
		}
		cancel()
		synctest.Wait()
		if _, ok := <-out; ok || !stopped.Load() {
			t.Fatal("network observer leaked")
		}
	})
}
