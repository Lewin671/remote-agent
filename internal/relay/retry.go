package relay

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// retryTiming is injectable so outage/recovery tests never sleep in real time.
// Jitter is additive like the WS retry, and the final delay is capped at 60s.
type retryTiming struct {
	now    func() time.Time
	wait   func(context.Context, time.Duration)
	jitter func(time.Duration) time.Duration
}

func defaultRetryTiming() retryTiming {
	return retryTiming{time.Now, sleep, func(d time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(d/4 + 1))) }}
}
func (r *Relay) retryTime() retryTiming {
	t := r.timing
	fallback := defaultRetryTiming()
	if t.now == nil {
		t.now = fallback.now
	}
	if t.wait == nil {
		t.wait = fallback.wait
	}
	if t.jitter == nil {
		t.jitter = fallback.jitter
	}
	return t
}

type retryBackoff struct {
	base, next time.Duration
	jitter     func(time.Duration) time.Duration
}

func (b *retryBackoff) failure() time.Duration {
	d := b.next
	if d == 0 {
		d = max(b.base, time.Millisecond)
	}
	d = min(d, time.Minute)
	b.next = min(d*2, time.Minute)
	return min(d+max(b.jitter(d), 0), time.Minute)
}
func (b *retryBackoff) reset() { b.next = 0 }

// warningGate spans retry generations; a brief success must not re-enable
// one warning per second during an unstable outage.
type warningGate struct {
	mu   sync.Mutex
	last time.Time
	seen bool
}

func (g *warningGate) allow(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen && now.Sub(g.last) < time.Minute {
		return false
	}
	g.last = now
	g.seen = true
	return true
}
