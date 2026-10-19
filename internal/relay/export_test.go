package relay

import "time"

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
