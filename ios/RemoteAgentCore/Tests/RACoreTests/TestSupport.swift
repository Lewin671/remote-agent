import Foundation
@testable import RACore

struct LatchTimedOut: Error, CustomStringConvertible {
    let label: String
    var description: String { "TestLatch timed out waiting for \(label)" }
}

/// A one-shot gate for holding async work until the test releases it.
/// Waiters respond promptly to task cancellation and optional bounded timeouts so tests cannot hang.
actor TestLatch {
    private var released = false
    private var waiting: [UUID: CheckedContinuation<Void, Error>] = [:]

    func wait() async throws {
        if released { return }
        try Task.checkCancellation()
        let id = UUID()
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
                if released {
                    cont.resume()
                } else if Task.isCancelled {
                    cont.resume(throwing: CancellationError())
                } else {
                    waiting[id] = cont
                }
            }
        } onCancel: {
            Task { await self.cancelWaiter(id) }
        }
    }

    func wait(timeoutMs: Int64, label: String = "signal") async throws {
        if released { return }
        try await withThrowingTaskGroup(of: Void.self) { group in
            group.addTask { try await self.wait() }
            group.addTask {
                try await Task.sleep(for: .milliseconds(timeoutMs))
                throw LatchTimedOut(label: label)
            }
            defer { group.cancelAll() }
            try await group.next()
        }
    }

    private func cancelWaiter(_ id: UUID) {
        if let cont = waiting.removeValue(forKey: id) {
            cont.resume(throwing: CancellationError())
        }
    }

    func open() {
        released = true
        let all = Array(waiting.values)
        waiting.removeAll()
        for waiter in all { waiter.resume() }
    }
}
