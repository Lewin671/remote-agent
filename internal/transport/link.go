package transport

import (
	"context"
	"sync"
)

// Link owns process work and logical streams. A path supplies a waiting
// context, never the lifetime of admitted execution or an independent stream.
// The dispatcher stops readers before draining the link on process retirement.
type Link struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.RWMutex
	closed  bool
	calls   sync.WaitGroup
	streams sync.WaitGroup
	Router  Router
}

func NewLink(ctx context.Context) *Link {
	ctx, cancel := context.WithCancel(ctx)
	return &Link{ctx: ctx, cancel: cancel}
}
func (l *Link) Context() context.Context { return l.ctx }

// Run owns already bounded work. Queue, execution and waiter admission happens
// at their respective owners before this method; it never blocks a reader.
func (l *Link) Run(handle func(context.Context)) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed || l.ctx.Err() != nil {
		return false
	}
	l.calls.Go(func() { handle(l.ctx) })
	return true
}

func (l *Link) ServeStream(serve func()) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return false
	}
	l.streams.Go(serve)
	return true
}

// DrainCalls is used when retiring authorization: admitted operations retain
// their original deadlines, while maintenance and new admission have stopped.
func (l *Link) DrainCalls() { l.calls.Wait() }

func (l *Link) Close() {
	l.cancel()
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	l.calls.Wait()
	l.streams.Wait()
}
