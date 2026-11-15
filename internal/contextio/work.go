package contextio

import (
	"context"
	"sync"
)

type workKey struct{}

// Work joins actual I/O completion, not just the request wrapper. A transport
// execution retains its original slot until the backend AND these calls exit.
type Work struct {
	group    sync.WaitGroup
	once     sync.Once
	violated chan struct{}
}

func Track(ctx context.Context) (context.Context, *Work) {
	w := &Work{violated: make(chan struct{})}
	return context.WithValue(ctx, workKey{}, w), w
}
func (w *Work) Wait()                     { w.group.Wait() }
func (w *Work) Violated() <-chan struct{} { return w.violated }
func (w *Work) violate()                  { w.once.Do(func() { close(w.violated) }) }

func track(ctx context.Context) (*Work, func()) {
	w, _ := ctx.Value(workKey{}).(*Work)
	if w == nil {
		return nil, func() {}
	}
	w.group.Add(1)
	return w, w.group.Done
}
