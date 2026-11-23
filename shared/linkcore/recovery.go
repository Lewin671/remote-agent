// Package linkcore holds the sans-IO link rules shared by the daemon and the
// client core: relay recovery budgets and liveness, fair queues, direct peer
// budgets and peer-scope ordering. Times are Unix (or monotonic) milliseconds
// supplied by the caller; randomness arrives as a unit float; nothing here
// locks, so an owner shared across goroutines supplies its own mutex.
package linkcore

const (
	HeartbeatMs        = 1000
	SuspectAfterMs     = 3 * HeartbeatMs
	DefaultDeadAfterMs = 60000
)

// Liveness tracks observed server heartbeat intervals on a single leg.
// Before two heartbeats are observed, silence uses DefaultDeadAfterMs (60s);
// afterwards, it uses max(SuspectAfterMs, 3*observedInterval).
type Liveness struct {
	lastFrameAt      int64
	heartbeats       int
	lastHeartbeatAt  int64
	observedInterval int64
}

func NewLiveness(now int64) Liveness { return Liveness{lastFrameAt: now} }

func (l *Liveness) OnFrame(now int64, isHeartbeat bool) {
	if now > l.lastFrameAt {
		l.lastFrameAt = now
	}
	if !isHeartbeat {
		return
	}
	l.heartbeats++
	if l.heartbeats >= 2 {
		l.observedInterval = max(0, now-l.lastHeartbeatAt)
	}
	l.lastHeartbeatAt = now
}

// ThresholdMs is the silence after which the leg is suspect.
func (l *Liveness) ThresholdMs() int64 {
	if l.heartbeats < 2 {
		return DefaultDeadAfterMs
	}
	return max(SuspectAfterMs, 3*l.observedInterval)
}

func (l *Liveness) Suspect(now int64) bool { return now-l.lastFrameAt >= l.ThresholdMs() }

// Budget is shared by all targets in one process. A notification never refills
// it. Both buckets are checked before either is charged.
type Budget struct{ global bucket }

type bucket struct {
	tokens      float64
	at          int64
	initialized bool
}

func (b *bucket) refill(now int64, capacity, perSecond float64) {
	if !b.initialized {
		b.tokens = capacity
		b.at = now
		b.initialized = true
	}
	if now > b.at {
		b.tokens = min(capacity, b.tokens+float64(now-b.at)/1000*perSecond)
		b.at = now
	}
}

// Recovery is owned by one target's event loop, across all path generations.
// Delay is side-effect free except for refilling elapsed tokens; Take commits
// one attempt. Failure records full jitter, which hints cannot reset.
type Recovery struct {
	local    bucket
	failures uint
	retryAt  int64
	retrying bool
}

// DelayMs is how long until Take can succeed.
func (r *Recovery) DelayMs(now int64, b *Budget) int64 {
	r.local.refill(now, 2, 1)
	b.global.refill(now, 4, 2)
	var d int64
	if r.retrying {
		d = max(0, r.retryAt-now)
	}
	if r.local.tokens < 1 {
		d = max(d, ceilMs((1-r.local.tokens)*1000))
	}
	if b.global.tokens < 1 {
		d = max(d, ceilMs((1-b.global.tokens)*1000/2))
	}
	return d
}

func ceilMs(v float64) int64 {
	n := int64(v)
	if float64(n) < v {
		n++
	}
	return n
}

func (r *Recovery) Take(now int64, b *Budget) bool {
	if r.DelayMs(now, b) > 0 {
		return false
	}
	r.local.tokens--
	b.global.tokens--
	return true
}

// Failures returns the number of consecutive failed attempts recorded since Ready.
func (r *Recovery) Failures() int {
	if r == nil {
		return 0
	}
	return int(r.failures)
}

// Failed schedules the next attempt with full jitter; unit is uniform in [0,1).
func (r *Recovery) Failed(now int64, unit float64) {
	cap := min(int64(1000)<<min(r.failures, 5), 30000)
	r.failures++
	r.retryAt = now + int64(max(0, min(unit, 1))*float64(cap))
	r.retrying = true
}

func (r *Recovery) Ready() { r.failures = 0; r.retrying = false }
