import Foundation
import Testing
import UIKit

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

/// Observe display frames until UIKit has completed the requested geometry.
/// The timer is only a failure deadline; readiness never depends on a sleep.
@MainActor
private final class LayoutReadiness: NSObject {
    private let ready: () -> Bool
    private var continuation: CheckedContinuation<Void, Error>?
    private var link: CADisplayLink?
    private var timeout: Timer?
    init(_ ready: @escaping () -> Bool) { self.ready = ready }
    func wait() async throws {
        try await withCheckedThrowingContinuation { continuation in
            self.continuation = continuation
            let link = CADisplayLink(target: self, selector: #selector(frame))
            self.link = link
            link.add(to: .main, forMode: .common)
            timeout = Timer.scheduledTimer(withTimeInterval: 5, repeats: false) { [weak self] _ in
                Task { @MainActor in self?.finish(.failure(LayoutTimeout())) }
            }
            frame()
        }
    }
    @objc private func frame() { if ready() { finish(.success(())) } }
    private func finish(_ result: Result<Void, Error>) {
        guard let continuation else { return }
        self.continuation = nil
        link?.invalidate(); link = nil
        timeout?.invalidate(); timeout = nil
        continuation.resume(with: result)
    }
    private struct LayoutTimeout: Error {}
}

@MainActor
func waitForLayout(_ ready: @escaping () -> Bool) async throws {
    try await LayoutReadiness(ready).wait()
}
