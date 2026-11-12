package daemon

import (
	"context"
	"sync"
)

// controlMutex serializes terminal ownership and lets requests abandon their
// wait without leaving a goroutine queued to acquire the lock later. The zero
// value is ready to use, like sync.Mutex.
type controlMutex struct {
	once sync.Once
	gate chan struct{}
}

func (m *controlMutex) init() {
	m.once.Do(func() { m.gate = make(chan struct{}, 1) })
}

func (m *controlMutex) Lock() {
	m.init()
	m.gate <- struct{}{}
}

func (m *controlMutex) LockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.init()
	select {
	case m.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *controlMutex) TryLock() bool {
	m.init()
	select {
	case m.gate <- struct{}{}:
		return true
	default:
		return false
	}
}

func (m *controlMutex) Unlock() { <-m.gate }
