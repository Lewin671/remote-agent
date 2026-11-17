import Foundation
import os

/// Wall and monotonic time plus sleep, replaceable so backoff, timeouts and deadlines are testable without waiting.
struct DirectClock: Sendable {
    var now: @Sendable () -> UInt64 = { UInt64(Date().timeIntervalSince1970 * 1000) }
    var monotonic: @Sendable () -> Double = { Double(DispatchTime.now().uptimeNanoseconds) / 1e6 }
    var sleep: @Sendable (UInt64) async throws -> Void = { ms in try await Task.sleep(nanoseconds: ms * 1_000_000) }
}

/// One job at a time, in order, each after the previous one finished: the shape of the web's
/// promise chain. State owned by a chain is touched only inside its jobs.
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
}

/// Wakes writers waiting for the DataChannel buffer to drain. A signal that arrives before the
/// wait is not lost; a wait also ends after `ms` so a missed callback cannot stall a writer.
final class AsyncSignal: @unchecked Sendable {
    private let lock = NSLock()
    private var waiters: [UUID: CheckedContinuation<Void, Never>] = [:]
    private var pending = false
    func signal() {
        lock.lock(); let all = Array(waiters.values); waiters.removeAll(); pending = all.isEmpty; lock.unlock()
        for waiter in all { waiter.resume() }
    }
    func wait(ms: UInt64) async {
        let id = UUID()
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            lock.lock()
            if pending { pending = false; lock.unlock(); c.resume(); return }
            waiters[id] = c; lock.unlock()
            Task { [weak self] in
                try? await Task.sleep(nanoseconds: ms * 1_000_000)
                self?.expire(id)
            }
        }
    }
    private func expire(_ id: UUID) {
        lock.lock(); let c = waiters.removeValue(forKey: id); lock.unlock(); c?.resume()
    }
}

/// Shared by both PC uses and replacement connections; hints never reset it (docs/transport.md §2).
final class DirectBudget: @unchecked Sendable {
    private struct Usage { var owner: String; var uses = Set<Int>(); var bytes = [0, 0]; var frames = [0, 0] }
    private let lock = NSLock()
    private var peers: [String: Usage] = [:]
    private var total = [0, 0]
    let maxPeers: Int
    init(maxPeers: Int = 4) { self.maxPeers = maxPeers }
    func acquire(_ peer: String, _ owner: String, _ use: Int) -> Bool {
        lock.lock(); defer { lock.unlock() }
        var current = peers[peer]
        if current == nil {
            if peers.count >= maxPeers { return false }
            current = Usage(owner: owner)
        }
        guard current!.owner == owner, !current!.uses.contains(use), current!.uses.count < 2 else { return false }
        current!.uses.insert(use); peers[peer] = current
        return true
    }
    func ownerAvailable(_ peer: String, _ owner: String) -> Bool {
        lock.lock(); defer { lock.unlock() }
        return peers[peer] == nil || peers[peer]!.owner == owner
    }
    func release(_ peer: String, _ use: Int) {
        lock.lock(); defer { lock.unlock() }
        guard var current = peers[peer] else { return }
        current.uses.remove(use); peers[peer] = current; prune(peer)
    }
    func reserve(_ peer: String, _ owner: String, _ use: Int, _ direction: Int, _ bytes: Int, _ frames: Int) -> Bool {
        lock.lock(); defer { lock.unlock() }
        guard var current = peers[peer], current.owner == owner, current.uses.contains(use) else { return false }
        guard bytes >= 0, frames >= 0, current.bytes[direction] + bytes <= 2 << 20, current.frames[direction] + frames <= 128, total[direction] + bytes <= 16 << 20 else { return false }
        current.bytes[direction] += bytes; current.frames[direction] += frames; total[direction] += bytes
        peers[peer] = current
        return true
    }
    func free(_ peer: String, _ direction: Int, _ bytes: Int, _ frames: Int) {
        lock.lock(); defer { lock.unlock() }
        guard var current = peers[peer] else { return }
        current.bytes[direction] -= bytes; current.frames[direction] -= frames; total[direction] -= bytes
        peers[peer] = current; prune(peer)
    }
    private func prune(_ peer: String) {
        guard let current = peers[peer] else { return }
        if current.uses.isEmpty && current.bytes == [0, 0] && current.frames == [0, 0] { peers.removeValue(forKey: peer) }
    }
    /// For the tests: nothing may stay reserved once everything is closed.
    var isEmpty: Bool { lock.lock(); defer { lock.unlock() }; return peers.isEmpty && total == [0, 0] }
}

/// Runs each step even when an earlier one throws; Swift has no finally for a list of closings.
func cleanup(_ steps: (() -> Void)...) { for step in steps { step() } }
