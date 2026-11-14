package transport

import (
	"context"
	"sync"
)

// StreamRoute describes only relay routing, not an authorization or payload.
type StreamRoute struct{ ID string }
type OpenStream func(context.Context, StreamRoute) (Path, error)

// Router selects a transport adapter. Only RelayPath exists in this phase;
// replacing a main path does not own or close existing logical streams.
type Router struct {
	mu         sync.Mutex
	generation uint64
	open       OpenStream
}

func (r *Router) Attach(open OpenStream) func() {
	r.mu.Lock()
	r.generation++
	generation := r.generation
	r.open = open
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.generation == generation {
			r.open = nil
		}
	}
}

func (r *Router) OpenStream(ctx context.Context, route StreamRoute) (Path, error) {
	r.mu.Lock()
	open := r.open
	r.mu.Unlock()
	if open == nil {
		return nil, context.Canceled
	}
	return open(ctx, route)
}
