import Foundation
import os

private let recoveryOrigin = ContinuousClock.now
func recoveryTime() -> Int64 {
    let parts = recoveryOrigin.duration(to: .now).components
    return parts.seconds * 1000 + parts.attoseconds / 1_000_000_000_000_000
}

struct RecoveryBucket: Sendable {
    var tokens: Double
    var at: Int64?
    let capacity: Double
    let rate: Double
    init(_ capacity: Double, _ rate: Double) { self.capacity=capacity; self.rate=rate; tokens=capacity }
    mutating func delay(_ now: Int64) -> Int64 {
        let last = at ?? now
        tokens = min(capacity, tokens + Double(max(0,now-last))*rate/1000)
        at = max(now,last)
        return Int64(ceil(max(0,(1-tokens)*1000/rate)))
    }
}
final class RecoveryBudget: Sendable {
    fileprivate let state = OSAllocatedUnfairLock(initialState: RecoveryBucket(4,2))
}
private let processRecoveryBudget = RecoveryBudget()
struct PathChanged: Error {}

/// Terminal reopen budget & backoff used exclusively by `TerminalRecovery`.
private final class Recovery: Sendable {
    private struct State: Sendable {
        var bucket = RecoveryBucket(2, 1)
        var failures = 0
        var retryAt: Int64 = 0
    }
    private let state = OSAllocatedUnfairLock(initialState: State())
    private let budget: RecoveryBudget
    private let now: @Sendable () -> Int64
    private let random: @Sendable () -> Double
    init(now: @escaping @Sendable () -> Int64 = recoveryTime,
         random: @escaping @Sendable () -> Double = { Double.random(in: 0..<1) },
         budget: RecoveryBudget = processRecoveryBudget) {
        self.now = now; self.random = random; self.budget = budget
    }
    func delay() -> Int64 {
        let at = now()
        return budget.state.withLock { global in
            state.withLockUnchecked { s in max(s.bucket.delay(at), global.delay(at), s.retryAt - at, 0) }
        }
    }
    func take() -> Bool {
        let at = now()
        return budget.state.withLock { global in
            state.withLockUnchecked { s in
                guard max(s.bucket.delay(at), global.delay(at), s.retryAt - at, 0) == 0 else { return false }
                s.bucket.tokens -= 1; global.tokens -= 1; return true
            }
        }
    }
    func failed() {
        let at = now(), jitter = random()
        state.withLock { s in
            let cap = min(Int64(1000) << min(s.failures, 5), 30000)
            s.failures += 1; s.retryAt = at + Int64(jitter * Double(cap))
        }
    }
    func ready() {
        state.withLock { s in
            s.failures = 0; s.retryAt = 0
        }
    }
}

/// Per-terminal reopen recovery sharing the process-wide global budget (4/2s)
/// while keeping an independent target budget (2/1s) and full-jitter backoff (1..30s).
public final class TerminalRecovery: Sendable {
    private let recovery: Recovery
    private let reopenedInStreak = OSAllocatedUnfairLock(initialState: false)

    public init() {
        self.recovery = Recovery()
    }

    init(
        now: @escaping @Sendable () -> Int64 = recoveryTime,
        random: @escaping @Sendable () -> Double = { Double.random(in: 0..<1) },
        budget: RecoveryBudget = processRecoveryBudget
    ) {
        self.recovery = Recovery(now: now, random: random, budget: budget)
    }

    public func recordFailureIfUnstabilized() {
        let active = reopenedInStreak.withLock { $0 }
        if active {
            recovery.failed()
        }
    }

    public func takeOrDelayMs() -> Int64? {
        if recovery.take() {
            reopenedInStreak.withLock { $0 = true }
            return nil
        }
        return max(1, recovery.delay())
    }

    public func delayMs() -> Int64 {
        recovery.delay()
    }

    public func onStabilized() {
        reopenedInStreak.withLock { $0 = false }
        recovery.ready()
    }

    public func reset() {
        reopenedInStreak.withLock { $0 = false }
        recovery.ready()
    }
}
