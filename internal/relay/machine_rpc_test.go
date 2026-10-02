package relay

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
)

func TestMachineRPCWaitIsCancelableAndSlotsAreReleased(t *testing.T) {
	r := &Relay{machineRPCs: make(chan struct{}, maxConcurrentMachineRPCs)}
	for range maxConcurrentMachineRPCs {
		r.machineRPCs <- struct{}{}
	}
	called := false
	failure := errors.New("request failed")
	call := r.limitMachineRPCs()(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, failure
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := call(ctx, nil)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || called {
			t.Fatalf("canceled queued call: err=%v called=%v", err, called)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled RPC remained blocked behind occupied slots")
	}
	for range maxConcurrentMachineRPCs {
		<-r.machineRPCs
	}
	if _, err := call(context.Background(), nil); !errors.Is(err, failure) {
		t.Fatalf("request error = %v", err)
	}
	if !called || len(r.machineRPCs) != 0 {
		t.Fatal("failed RPC retained its concurrency slot")
	}
}
