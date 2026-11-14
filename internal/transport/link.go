package transport

import (
	"context"
	"sync"
)

// Link owns process work and logical streams. A path supplies a waiting
// context, never the lifetime of admitted execution or an independent stream.
// The dispatcher stops readers before draining the link on process retirement.
type Link struct {
	ctx        context.Context
	cancel     context.CancelFunc
	admissions chan struct{}
	slots      chan struct{}
	mu         sync.RWMutex
	closed     bool
	calls      sync.WaitGroup
	streams    sync.WaitGroup
	Router     Router
}

func NewLink(ctx context.Context, admissions, executions chan struct{}) *Link {
	ctx, cancel := context.WithCancel(ctx)
	return &Link{ctx: ctx, cancel: cancel, admissions: admissions, slots: executions}
}

func (l *Link) Context() context.Context { return l.ctx }

// Dispatch bounds authentication and duplicate waiting separately from the
// execution slots. The caller's path may stop waiting after handoff; the
// handler must use waiting only for admission and use execution after Accept.
func (l *Link) Dispatch(waiting context.Context, handle func(execution context.Context)) error {
	select {
	case <-waiting.Done():
		return waiting.Err()
	case <-l.ctx.Done():
		return l.ctx.Err()
	case l.admissions <- struct{}{}:
	}
	if err := waiting.Err(); err != nil {
		<-l.admissions
		return err
	}
	if err := l.ctx.Err(); err != nil {
		<-l.admissions
		return err
	}
	l.mu.RLock()
	if l.closed {
		l.mu.RUnlock()
		<-l.admissions
		return context.Canceled
	}
	l.calls.Go(func() {
		defer func() { <-l.admissions }()
		handle(l.ctx)
	})
	l.mu.RUnlock()
	return nil
}

func (l *Link) Acquire(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.ctx.Done():
		return nil, l.ctx.Err()
	case l.slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-l.slots
			return nil, err
		}
		if err := l.ctx.Err(); err != nil {
			<-l.slots
			return nil, err
		}
		return func() { <-l.slots }, nil
	}
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
