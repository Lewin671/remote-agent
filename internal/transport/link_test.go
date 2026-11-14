package transport

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestLinkDispatchSeparatesWaitingAndExecutionLifetimes(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	admissions := make(chan struct{}, 2)
	slots := make(chan struct{}, 1)
	link := NewLink(parent, admissions, slots)

	// Canceling waiting before admission stops Dispatch without calling handle.
	canceled, cancelWaiting := context.WithCancel(context.Background())
	cancelWaiting()
	var ran atomic.Bool
	if err := link.Dispatch(canceled, func(context.Context) { ran.Store(true) }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiting: %v", err)
	}
	if ran.Load() {
		t.Fatal("canceled waiting still invoked handler")
	}

	// Once admitted, canceling waiting must not cancel execution.
	waiting, stopWaiting := context.WithCancel(context.Background())
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	if err := link.Dispatch(waiting, func(execution context.Context) {
		close(entered)
		<-release
		finished <- execution.Err()
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-entered
	stopWaiting()
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("waiting cancel canceled execution: %v", err)
	}
	link.DrainCalls()

	// Streams also run on Link lifetime and are waited on by Close.
	streamDone := make(chan error, 1)
	if !link.ServeStream(func() {
		<-link.Context().Done()
		streamDone <- link.Context().Err()
	}) {
		t.Fatal("ServeStream returned false before Close")
	}
	link.Close()
	select {
	case err := <-streamDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stream err: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel stream")
	}
	if link.ServeStream(func() {}) {
		t.Fatal("ServeStream succeeded after Close")
	}
}

func TestLinkAcquireRespectsWaitingAndLinkClose(t *testing.T) {
	link := NewLink(context.Background(), make(chan struct{}, 1), make(chan struct{}, 1))
	defer link.Close()

	release, err := link.Acquire(context.Background())
	if err != nil {
		t.Fatalf("initial Acquire: %v", err)
	}
	waiting, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := link.Acquire(waiting); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Acquire: %v", err)
	}
	release()

	// Closing the link unblocks waiting Acquire calls.
	release, err = link.Acquire(context.Background())
	if err != nil {
		t.Fatalf("re-Acquire: %v", err)
	}
	defer release()
	blocked := make(chan error, 1)
	go func() {
		_, err := link.Acquire(context.Background())
		blocked <- err
	}()
	time.Sleep(10 * time.Millisecond)
	link.Close()
	select {
	case err := <-blocked:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire after Link.Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire remained blocked after Link.Close")
	}
}
