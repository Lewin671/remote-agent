package relay

import (
	"context"
	"sync"
)

// connectionWrites owns publication mutations, not their preceding reads,
// backend snapshots, RPCs, or command callbacks. Shutdown cancels admission
// and drains a mutation that already started before closing the endpoint.
type connectionWrites struct {
	mu     sync.Mutex
	closed bool
}

func (w *connectionWrites) run(ctx context.Context, write func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return context.Canceled
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return write()
}

func (w *connectionWrites) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
}

func (c *conn) writeDisk(write func() error) error {
	return c.writes.run(c.lifetime, write)
}
