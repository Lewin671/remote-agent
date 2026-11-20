package transport

import (
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Lewin671/remote-agent/shared/linkcore"
)

// The relay leg's recovery rules live in shared/linkcore. These wrappers
// adapt them to the daemon: times become monotonic milliseconds, jitter comes
// from math/rand, and state shared across goroutines is locked here.

const SuspectAfter = linkcore.SuspectAfterMs * time.Millisecond

// processStart anchors MonotonicMs. time.Now() carries a monotonic reading.
var processStart = time.Now()

// MonotonicMs converts now to the milliseconds linkcore budgets expect. For
// a time.Now() value it reads the monotonic clock (Time.Sub uses it when both
// operands carry one), so a wall-clock step cannot stall refills or retries.
// Never pass UnixMilli: budgets and retry times would follow the wall clock.
func MonotonicMs(now time.Time) int64 { return now.Sub(processStart).Milliseconds() }

// AdaptiveLiveness is a locked linkcore.Liveness: the leg's reader reports
// frames while the supervisor polls Suspect. Times are milliseconds on one
// monotonic origin.
type AdaptiveLiveness struct {
	mu sync.Mutex
	l  linkcore.Liveness
}

func NewAdaptiveLiveness(nowMs int64) *AdaptiveLiveness {
	return &AdaptiveLiveness{l: linkcore.NewLiveness(nowMs)}
}

func (a *AdaptiveLiveness) OnFrame(nowMs int64, isHeartbeat bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.l.OnFrame(nowMs, isHeartbeat)
}

func (a *AdaptiveLiveness) Suspect(nowMs int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.l.Suspect(nowMs)
}

// Budget is shared by all targets in one process.
type Budget struct {
	mu sync.Mutex
	b  linkcore.Budget
}

var ProcessBudget Budget

// Recovery is owned by one target's event loop; only its Budget is shared.
type Recovery struct {
	Budget *Budget
	Random func() float64
	r      linkcore.Recovery
}

func (r *Recovery) budget() *Budget {
	if r.Budget != nil {
		return r.Budget
	}
	return &ProcessBudget
}
func (r *Recovery) Take(now time.Time) bool {
	b := r.budget()
	b.mu.Lock()
	defer b.mu.Unlock()
	return r.r.Take(MonotonicMs(now), &b.b)
}
func (r *Recovery) Failed(now time.Time) {
	random := r.Random
	if random == nil {
		random = rand.Float64
	}
	r.r.Failed(MonotonicMs(now), random())
}
func (r *Recovery) Ready() { r.r.Ready() }
