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

/// Target and process budgets survive every network generation. Only Ready
/// resets failure backoff; it never refills either bucket.
final class Recovery: Sendable {
    private struct State: Sendable {
        var bucket = RecoveryBucket(2,1)
        var failures = 0
        var retryAt: Int64 = 0
        var listeners: [UUID: AsyncStream<Void>.Continuation] = [:]
    }
    private let state = OSAllocatedUnfairLock(initialState: State())
    private let budget: RecoveryBudget
    private let now: @Sendable () -> Int64
    private let random: @Sendable () -> Double
    init(now: @escaping @Sendable () -> Int64 = recoveryTime,
         random: @escaping @Sendable () -> Double = { Double.random(in: 0..<1) },
         budget: RecoveryBudget = processRecoveryBudget) {
        self.now=now; self.random=random; self.budget=budget
    }
    func delay() -> Int64 {
        let at=now()
        return budget.state.withLock { global in
            state.withLockUnchecked { s in max(s.bucket.delay(at),global.delay(at),s.retryAt-at,0) }
        }
    }
    func take() -> Bool {
        let at=now()
        return budget.state.withLock { global in
            state.withLockUnchecked { s in
                guard max(s.bucket.delay(at),global.delay(at),s.retryAt-at,0)==0 else {return false}
                s.bucket.tokens-=1;global.tokens-=1;return true
            }
        }
    }
    func failed() {
        let at=now(), jitter=random()
        state.withLock { s in
            let cap=min(Int64(1000)<<min(s.failures,5),30000)
            s.failures+=1;s.retryAt=at+Int64(jitter*Double(cap))
        }
    }
    func ready() {
        let listeners=state.withLock { s in
            s.failures=0;s.retryAt=0;return Array(s.listeners.values)
        }
        for listener in listeners {listener.yield(())}
    }
    func follow<T: Sendable>(_ send: @escaping @Sendable () async throws -> T) async throws -> T {
        let id=UUID()
        let (events,continuation)=AsyncStream<Void>.makeStream(bufferingPolicy:.bufferingNewest(1))
        state.withLock {$0.listeners[id]=continuation}
        defer {state.withLock {_=$0.listeners.removeValue(forKey:id)};continuation.finish()}
        return try await withThrowingTaskGroup(of:T.self) { group in
            group.addTask {try await send()}
            group.addTask {for await _ in events {throw PathChanged()};throw CancellationError()}
            defer {group.cancelAll()}
            return try await group.next()!
        }
    }
}

final class PathActivity: Sendable {
    private let value = OSAllocatedUnfairLock(initialState:Int64(0))
    func received(_ now: Int64) {value.withLock {$0=now}}
    var last: Int64 {value.withLock {$0}}
}
