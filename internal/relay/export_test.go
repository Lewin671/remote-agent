package relay

import (
	"context"
	"github.com/Lewin671/remote-agent/internal/transport"
	"net"
	"time"
)

// SetNetworkChangesForTest injects OS observations without changing host networking.
func SetNetworkChangesForTest(r *Relay, changes <-chan uint64) {
	r.networkChanges = func(context.Context) <-chan uint64 { return changes }
}

// SetWakeEventsForTest injects scheduler-pause/wake events without waiting for wall jumps.
func SetWakeEventsForTest(r *Relay, wakes <-chan struct{}) {
	r.wakeEvents = func(context.Context) <-chan struct{} { return wakes }
}

// SetRouteAvailableForTest overrides route verification on a new network hint.
func SetRouteAvailableForTest(r *Relay, available func(net.IP) bool) {
	r.routeAvailable = available
}

// SetBeforeDialResultForTest runs inside the dial goroutine just before sending dialResult.
func SetBeforeDialResultForTest(r *Relay, hook func(uint64)) {
	r.beforeDialResult = hook
}

// SetBeforeHelloForTest runs inside dialMain after websocket.Dial and right before StreamHello.
func SetBeforeHelloForTest(r *Relay, hook func()) {
	r.beforeHello = hook
}

// SetRecoveryNowForTest overrides the clock used by relay leg liveness and recovery budgets.
func SetRecoveryNowForTest(r *Relay, now func() time.Time) {
	r.recoveryNow = now
}

// SetSyncIntervalForTest changes only this relay's polling schedule. Set it
// before Run; production relays retain New's five-second interval.
func SetSyncIntervalForTest(r *Relay, interval time.Duration) { r.syncInterval = interval }

// SetSyncRetryBaseForTest changes retry timing before Run. Backoff and the
// one-minute cap still follow the production algorithm.
func SetSyncRetryBaseForTest(r *Relay, base time.Duration) { r.syncRetryBase = base }

func SetReadyTimeoutForTest(r *Relay, timeout time.Duration) { r.readyTimeout = timeout }

// HasOwedNotificationForTest observes the retained outcome, after Notify's
// response has been processed, rather than racing the server handler's count.
func HasOwedNotificationForTest(r *Relay, id string) bool { return r.hasOwedNotify(id) }

// HasBacklogWorkerForTest is false after an ended worker has returned and
// released its admission slot. It does not require a scheduler sleep.
func HasBacklogWorkerForTest(r *Relay) bool { return len(r.backlog) != 0 }

func SetSecurityIntervalForTest(r *Relay, interval time.Duration) { r.securityInterval = interval }

// SetRetryNowForTest shares a process publication clock across socket changes.
func SetRetryNowForTest(r *Relay, now func() time.Time) { r.timing.now = now }

func ReadyCountForTest(r *Relay) uint64 { return r.readyCount.Load() }

// IsolateRecoveryBudgetForTest gives a simulated daemon process its own global budget.
func IsolateRecoveryBudgetForTest(r *Relay) { r.recoveryBudget = &transport.Budget{} }

// ObserveRecoveryForTest records only lifecycle timing, never frames or identifiers.
func ObserveRecoveryForTest(r *Relay, observer func(string)) {
	r.observeRecovery = func(event recoveryEvent) { observer([]string{"hint", "suspect", "lost", "dial", "ready"}[event]) }
}

func ActiveCallsForTest(r *Relay) int {
	if s := r.scheduling.Load(); s != nil {
		n := s.Active()
		return n[0] + n[1] + n[2]
	}
	return 0
}

func ActivePoolsForTest(r *Relay) [3]int {
	if s := r.scheduling.Load(); s != nil {
		return s.Active()
	}
	return [3]int{}
}

func SetDirectLoopbackForTest(r *Relay)                   { r.directLoopback = true; r.directSTUN = []string{} }
func SetDirectClockForTest(r *Relay, fn func() time.Time) { r.directClock = fn }
func SetSchedulerClockForTest(r *Relay, now func() time.Time, after func(time.Duration, func()) func() bool) {
	r.SchedulerNow = now
	r.SchedulerAfter = after
}
func DirectFreshForTest(r *Relay) bool {
	s := r.security.Load()
	return s != nil && s.directFresh.Load() != nil
}
func SetTerminalClockForTest(r *Relay, ttl time.Duration, fn func() time.Time) {
	r.termZeroLegTTL = ttl
	r.termClock = fn
}
func TerminalRegistryActiveForTest(r *Relay) int {
	h := r.termHubRef.Load()
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.registry.ActiveCount()
}
func TerminalRelayAttachGenForTest(r *Relay, streamID string) uint64 {
	h := r.termHubRef.Load()
	if h == nil {
		return 0
	}
	lt := h.getLive(streamID)
	if lt == nil {
		return 0
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.relayAttachGen
}
func TerminalHasDirectLegForTest(r *Relay, streamID string) bool {
	h := r.termHubRef.Load()
	if h == nil {
		return false
	}
	lt := h.getLive(streamID)
	if lt == nil {
		return false
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.stream.HasLeg("direct")
}
func TerminalHasRelayLegForTest(r *Relay, streamID string) bool {
	h := r.termHubRef.Load()
	if h == nil {
		return false
	}
	lt := h.getLive(streamID)
	if lt == nil {
		return false
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.relayPath != nil
}
func TerminalOutSeqForTest(r *Relay, streamID string) uint64 {
	h := r.termHubRef.Load()
	if h == nil {
		return 0
	}
	lt := h.getLive(streamID)
	if lt == nil {
		return 0
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.outSeq
}
func TerminalDirectLegIDForTest(r *Relay, streamID string) string {
	h := r.termHubRef.Load()
	if h == nil {
		return ""
	}
	lt := h.getLive(streamID)
	if lt == nil {
		return ""
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.stream.LegID("direct")
}
func TerminalHasPendingDirectForTest(r *Relay, streamID string) bool {
	h := r.termHubRef.Load()
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.pendingDirect[streamID]
	return ok
}
func PeerTerminalDirectForTest(r *Relay, peerID string) bool {
	dm := r.directRef.Load()
	if dm == nil {
		return false
	}
	dp, ok := dm.activePeerFor(peerID)
	return ok && dp.peerTerminalDirect.Load()
}
