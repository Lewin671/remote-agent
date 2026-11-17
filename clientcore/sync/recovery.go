package sync

import "math"

// RecoveryBudget implements a sans-IO token-bucket rate limiter.
type RecoveryBudget struct {
	capacity float64
	rate     float64
	tokens   float64
	hasAt    bool
	atMs     int64
}

// NewRecoveryBudget creates a token bucket with the given burst capacity and refill rate (tokens/sec).
func NewRecoveryBudget(capacity, rate float64) *RecoveryBudget {
	return &RecoveryBudget{
		capacity: capacity,
		rate:     rate,
		tokens:   capacity,
	}
}

// Delay returns milliseconds to wait before one token is available at monoMs.
func (b *RecoveryBudget) Delay(monoMs int64) int64 {
	last := monoMs
	if b.hasAt {
		last = b.atMs
	}
	elapsed := monoMs - last
	if elapsed > 0 {
		b.tokens = min(b.capacity, b.tokens+float64(elapsed)*b.rate/1000.0)
	}
	if !b.hasAt || monoMs > last {
		b.atMs = monoMs
		b.hasAt = true
	}
	if b.tokens >= 1.0 {
		return 0
	}
	return int64(math.Ceil((1.0 - b.tokens) * 1000.0 / b.rate))
}

// Take consumes one token from the bucket.
func (b *RecoveryBudget) Take() {
	b.tokens--
}

// Recovery combines global + per-target token buckets with jittered exponential backoff.
type Recovery struct {
	global     *RecoveryBudget
	target     *RecoveryBudget
	failures   int
	retryAtMs  int64
	generation uint64
}

// NewRecovery constructs a Recovery tracker sharing global budget.
func NewRecovery(global *RecoveryBudget) *Recovery {
	if global == nil {
		global = NewRecoveryBudget(4.0, 2.0)
	}
	return &Recovery{
		global: global,
		target: NewRecoveryBudget(2.0, 1.0),
	}
}

// Delay returns the required wait time in milliseconds before the next reconnect attempt.
func (r *Recovery) Delay(monoMs int64) int64 {
	wait := max(r.target.Delay(monoMs), r.global.Delay(monoMs))
	if r.retryAtMs > monoMs {
		wait = max(wait, r.retryAtMs-monoMs)
	}
	return wait
}

// Take checks Delay and atomically consumes from both target and global budgets when ready.
func (r *Recovery) Take(monoMs int64) bool {
	if r.Delay(monoMs) > 0 {
		return false
	}
	r.target.Take()
	r.global.Take()
	return true
}

// Failed records one failed connection attempt and schedules full-jitter exponential backoff up to 30s.
func (r *Recovery) Failed(monoMs int64, unitFloat float64) {
	if unitFloat < 0 {
		unitFloat = 0
	} else if unitFloat >= 1 {
		unitFloat = 0.999999999
	}
	shift := min(r.failures, 5)
	r.failures++
	capMs := min(int64(1000)<<shift, int64(30_000))
	r.retryAtMs = monoMs + int64(unitFloat*float64(capMs))
}

// Ready resets failure backoff and advances generation (signaling PathChanged to active callers).
func (r *Recovery) Ready() {
	r.failures = 0
	r.retryAtMs = 0
	r.generation++
}

// Generation returns the monotonically increasing ready generation.
func (r *Recovery) Generation() uint64 {
	return r.generation
}

// LegLiveness tracks stream heartbeat intervals and detects suspect dead links.
type LegLiveness struct {
	lastFrameAtMs      int64
	heartbeats         int
	lastHeartbeatAtMs  int64
	observedIntervalMs int64
}

// NewLegLiveness initializes a liveness tracker at initialMonoMs.
func NewLegLiveness(initialMonoMs int64) *LegLiveness {
	return &LegLiveness{lastFrameAtMs: initialMonoMs}
}

// OnFrame records incoming stream traffic or heartbeat frames.
func (l *LegLiveness) OnFrame(monoMs int64, isHeartbeat bool) {
	if monoMs > l.lastFrameAtMs {
		l.lastFrameAtMs = monoMs
	}
	if !isHeartbeat {
		return
	}
	l.heartbeats++
	if l.heartbeats >= 2 {
		delta := monoMs - l.lastHeartbeatAtMs
		if delta < 0 {
			delta = 0
		}
		l.observedIntervalMs = delta
	}
	l.lastHeartbeatAtMs = monoMs
}

// ThresholdMs returns the silence threshold (60s before 2 heartbeats, else max(3s, 3x observed interval)).
func (l *LegLiveness) ThresholdMs() int64 {
	if l.heartbeats < 2 {
		return 60_000
	}
	return max(3_000, 3*l.observedIntervalMs)
}

// Suspect reports whether no frames have arrived within ThresholdMs at monoMs.
func (l *LegLiveness) Suspect(monoMs int64) bool {
	return monoMs-l.lastFrameAtMs >= l.ThresholdMs()
}
