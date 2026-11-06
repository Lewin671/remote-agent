import Foundation
import Testing

/// UIKit layout tests share one foreground scene. Swift Testing runs
/// suites concurrently, so reserve that scene across asynchronous waits.
@MainActor
final class WindowTestGate {
    static let shared = WindowTestGate()
    private var busy = false
    private var waiting: [CheckedContinuation<Void, Never>] = []

    func acquire() async {
        if !busy {
            busy = true
            return
        }
        await withCheckedContinuation { waiting.append($0) }
    }

    func release() {
        if waiting.isEmpty { busy = false }
        else { waiting.removeFirst().resume() }
    }
}

/// A clock the test moves by hand.
final class TestClock: @unchecked Sendable {
    private let lock = NSLock()
    private var date: Date
    init(_ date: Date = Date()) { self.date = date }
    var now: Date { lock.withLock { date } }
    func advance(_ seconds: TimeInterval) { lock.withLock { date += seconds } }
}

/// Waits for observable work to finish, reporting a failure at a wall-clock deadline.
@MainActor
func eventually(_ what: String = "", _ condition: @MainActor () -> Bool) async {
    let deadline = ContinuousClock.now.advanced(by: .seconds(5))
    while !condition() {
        guard ContinuousClock.now < deadline else {
            Issue.record("never happened: \(what)")
            return
        }
        try? await Task.sleep(for: .milliseconds(2))
    }
}
