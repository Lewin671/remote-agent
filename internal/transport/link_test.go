package transport

import (
	"context"
	"errors"
	"testing"
)

func TestLinkOwnsExecutionsAndStreamsUntilClose(t *testing.T) {
	link := NewLink(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	if !link.Run(func(ctx context.Context) { close(entered); <-release; done <- ctx.Err() }) {
		t.Fatal("execution refused")
	}
	<-entered
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	link.DrainCalls()
	stream := make(chan error, 1)
	if !link.ServeStream(func() { <-link.Context().Done(); stream <- link.Context().Err() }) {
		t.Fatal("stream refused")
	}
	link.Close()
	if !errors.Is(<-stream, context.Canceled) {
		t.Fatal("stream was not canceled")
	}
	if link.Run(func(context.Context) {}) || link.ServeStream(func() {}) {
		t.Fatal("closed owner admitted work")
	}
}
