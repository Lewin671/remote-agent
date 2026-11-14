package relay

import (
	"context"
	"github.com/Lewin671/remote-agent/internal/transport"
	"time"
)

// SetNetworkChangesForTest injects OS observations without changing host networking.
func SetNetworkChangesForTest(r *Relay, changes <-chan uint64) {
	r.networkChanges = func(context.Context) <-chan uint64 { return changes }
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

func ActiveCallsForTest(r *Relay) int { return len(r.calls) }
