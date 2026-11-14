package transport

import (
	"math"
	"math/rand/v2"
	"sync"
	"time"
)

const HeartbeatInterval = time.Second
const SuspectAfter = 3 * HeartbeatInterval

// Budget is shared by all targets in one process. A notification never refills
// it. Both buckets are checked before either is charged.
type Budget struct {
	mu     sync.Mutex
	global bucket
}
type bucket struct {
	tokens      float64
	at          time.Time
	initialized bool
}

func (b *bucket) refill(now time.Time, capacity, rate float64) {
	if !b.initialized {
		b.tokens = capacity
		b.at = now
		b.initialized = true
	}
	if now.After(b.at) {
		b.tokens = math.Min(capacity, b.tokens+now.Sub(b.at).Seconds()*rate)
		b.at = now
	}
}

var ProcessBudget Budget

// Recovery is owned by one target's event loop, across all path generations.
// Delay is side-effect free except for refilling elapsed tokens; Take commits
// one attempt. Failure records full jitter, which hints cannot reset.
type Recovery struct {
	Budget   *Budget
	local    bucket
	failures uint
	retryAt  time.Time
	Random   func() float64
}

func (r *Recovery) budget() *Budget {
	if r.Budget != nil {
		return r.Budget
	}
	return &ProcessBudget
}
func (r *Recovery) delay(now time.Time, b *Budget) time.Duration {
	r.local.refill(now, 2, 1)
	b.global.refill(now, 4, 2)
	d := max(time.Duration(0), r.retryAt.Sub(now))
	if r.local.tokens < 1 {
		d = max(d, time.Duration(math.Ceil((1-r.local.tokens)*float64(time.Second))))
	}
	if b.global.tokens < 1 {
		d = max(d, time.Duration(math.Ceil((1-b.global.tokens)*float64(time.Second)/2)))
	}
	return d
}
func (r *Recovery) Delay(now time.Time) time.Duration {
	b := r.budget()
	b.mu.Lock()
	defer b.mu.Unlock()
	return r.delay(now, b)
}
func (r *Recovery) Take(now time.Time) bool {
	b := r.budget()
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.delay(now, b) > 0 {
		return false
	}
	r.local.tokens--
	b.global.tokens--
	return true
}
func (r *Recovery) Failed(now time.Time) {
	cap := min(time.Second<<min(r.failures, 5), 30*time.Second)
	r.failures++
	random := r.Random
	if random == nil {
		random = rand.Float64
	}
	r.retryAt = now.Add(time.Duration(max(0, math.Min(random(), 1)) * float64(cap)))
}
func (r *Recovery) Ready() { r.failures = 0; r.retryAt = time.Time{} }
