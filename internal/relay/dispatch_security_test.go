package relay

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// A malicious relay can send arbitrary frames without client authentication.
// Hold every handler open and flood frames: retained requests and workers
// must stay bounded, and cancellation must unblock the reader and workers.
func TestForwardFloodIsBoundedAndCancelable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slots := make(chan struct{}, maxConcurrentCalls)
	var reads, starts atomic.Int32
	done := make(chan error, 1)
	workers := make(chan struct{}, maxConcurrentCalls)
	go func() {
		done <- readForwardCalls(ctx, slots, func() (*pb.ServerFrame, error) {
			reads.Add(1)
			return &pb.ServerFrame{Kind: &pb.ServerFrame_ForwardCall{ForwardCall: &pb.ForwardCall{CallId: 1}}}, nil
		}, func(ctx context.Context, call *pb.ForwardCall) {
			starts.Add(1)
			<-ctx.Done()
			workers <- struct{}{}
		})
	}()
	deadline := time.After(3 * time.Second)
	for starts.Load() < maxConcurrentCalls || reads.Load() < maxConcurrentCalls+1 {
		select {
		case <-deadline:
			t.Fatal("workers did not fill")
		case <-time.After(time.Millisecond):
		}
	}
	if n := reads.Load(); n != maxConcurrentCalls+1 {
		t.Fatalf("unbounded retained requests: %d", n)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("reader = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader remained blocked after cancellation")
	}
	for range maxConcurrentCalls {
		select {
		case <-workers:
		case <-time.After(time.Second):
			t.Fatal("worker did not cancel")
		}
	}
	if n := starts.Load(); n != maxConcurrentCalls {
		t.Fatalf("started %d workers", n)
	}
}

func TestCommandReplayWaitCancelsWithoutChangingOriginal(t *testing.T) {
	log := openCommandLog(filepath.Join(t.TempDir(), "commands.json"))
	started, release, originalDone := make(chan struct{}), make(chan struct{}), make(chan *pb.CommandResult, 1)
	t.Cleanup(func() { close(release); <-originalDone })
	go func() {
		originalDone <- log.do(context.Background(), "same", time.Now().Add(time.Minute).UnixMilli(), nil, func() *pb.CommandResult {
			close(started)
			<-release
			return &pb.CommandResult{ErrorCode: "original"}
		})
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	retryDone := make(chan *pb.CommandResult, 1)
	go func() {
		retryDone <- log.do(ctx, "same", time.Now().Add(time.Minute).UnixMilli(), nil, func() *pb.CommandResult {
			return &pb.CommandResult{ErrorCode: "executed_twice"}
		})
	}()
	cancel()
	select {
	case r := <-retryDone:
		if r.ErrorCode != "canceled" {
			t.Fatalf("retry = %v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("inflight replay ignored cancellation")
	}
	log.mu.Lock()
	_, running := log.inflight["same"]
	log.mu.Unlock()
	if !running {
		t.Fatal("canceling replay altered original command")
	}
}
