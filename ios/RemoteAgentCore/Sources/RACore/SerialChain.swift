import Foundation

/// Serializes queued async jobs in FIFO order without blocking actor executors.
final class SerialChain: @unchecked Sendable {
    private let lock = NSLock()
    private var tail: Task<Void, Never>?

    @discardableResult
    func run<T: Sendable>(_ work: @escaping @Sendable () async -> T) -> Task<T, Never> {
        lock.lock(); defer { lock.unlock() }
        let previous = tail
        let job = Task<T, Never> { await previous?.value; return await work() }
        tail = Task { _ = await job.value }
        return job
    }

    func drain() async {
        await run {}.value
    }
}
