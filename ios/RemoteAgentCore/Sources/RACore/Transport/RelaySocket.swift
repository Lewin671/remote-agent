// One WebSocket to the server, for the main stream and for terminals. It
// puts URLSessionWebSocketTask's pieces in one place: what is sent first,
// pings, and how the connection ended.
import Foundation
import os

/// How often stream connections ping; the server pings at the same pace.
let streamPingMs: Int64 = 20_000

/// The WebSocket close code the server refuses a hello with.
let policyViolation = 1008

/// The server's frames stay under 2 MiB (maxFrameBytes in internal/server);
/// URLSession's own limit of 1 MiB would end the stream on a full push.
private let maxMessageBytes = 4 << 20

final class Socket: FrameStream {
    private enum PrimedReceive {
        case pending
        case ready(Result<URLSessionWebSocketTask.Message, Error>)
        case consumed
    }

    private struct State {
        var opened = false
        var closeCode: Int?
        var failure: Error?
        /// Dropped by this side: what the task fails with after that says
        /// nothing. A failure noted before it is still why the connection ended.
        var dropped = false
        var end: PathEnd?
        var waiters: [CheckedContinuation<PathEnd, Never>] = []
        var openWaiters: [CheckedContinuation<Bool, Never>] = []
        var primed: PrimedReceive = .pending
        var primedWaiter: CheckedContinuation<URLSessionWebSocketTask.Message, Error>?
        var awaitingPong = false
        var pinger: Task<Void, Never>?
    }

    private let task: URLSessionWebSocketTask
    private let state = OSAllocatedUnfairLock(uncheckedState: State())

    /// Opens a socket to `url`. `hello` is its first frame, sent when the
    /// upgrade is done: the server sends nothing before it has the token.
    /// Pings start then too, so that a link that died silently, as when a
    /// phone changes networks, is noticed and replaced.
    init(http: HTTP, url: URL, hello: Data, pingMs: Int64 = streamPingMs, sleep: @escaping Sleeper) {
        let box = OSAllocatedUnfairLock<Socket?>(initialState: nil)
        task = http.webSocketTask(URLRequest(url: url), handlers: SocketHandlers(
            opened: { box.withLock { $0 }?.didOpen(hello: hello, pingMs: pingMs, sleep: sleep) },
            closed: { code in box.withLock { $0 }?.state.withLockUnchecked { $0.closeCode = code } },
            completed: { error in
                box.withLock { s in
                    defer { s = nil }
                    return s
                }?.didComplete(error)
            }))
        task.maximumMessageSize = maxMessageBytes
        // The handlers keep the socket until its task is done with, and no longer.
        box.withLock { $0 = self }
        primeReceive()
        task.resume()
    }

    private func didOpen(hello: Data, pingMs: Int64, sleep: @escaping Sleeper) {
        task.send(.data(hello)) { [weak self] error in
            if let error { self?.failHello(error) }
        }
        let pinger = Task { [weak self] in
            while (try? await sleep(pingMs)) != nil, !Task.isCancelled {
                guard let self, self.ping() else { return }
            }
        }
        let waiters = state.withLockUnchecked { s -> [CheckedContinuation<Bool, Never>] in
            s.opened = true
            if s.end == nil { s.pinger = pinger } else { pinger.cancel() }
            defer { s.openWaiters = [] }
            return s.openWaiters
        }
        for w in waiters { w.resume(returning: true) }
    }

    /// Records an initial hello send error without cancelling before `receive()` can drain any server close frame.
    private func failHello(_ error: Error) {
        let code = task.closeCode.rawValue
        let cancelNow = state.withLockUnchecked { s -> Bool in
            if s.closeCode == nil, code != 0 { s.closeCode = code }
            if s.failure == nil, s.closeCode == nil, !s.dropped { s.failure = error }
            return s.closeCode != nil || s.dropped
        }
        if cancelNow { task.cancel() }
    }

    /// Sends a ping unless the one before it is still unanswered, which ends the connection.
    private func ping() -> Bool {
        let late = state.withLockUnchecked { s -> Bool in
            if s.awaitingPong || s.end != nil { return true }
            s.awaitingPong = true
            return false
        }
        if late {
            fail(URLError(.timedOut))
            return false
        }
        task.sendPing { [weak self] error in
            guard let self else { return }
            if let error { self.fail(error) } else { self.state.withLockUnchecked { $0.awaitingPong = false } }
        }
        return true
    }

    private func didComplete(_ error: Error?) {
        let code = task.closeCode.rawValue
        let (waiters, openWaiters) = state.withLockUnchecked { s -> ([CheckedContinuation<PathEnd, Never>], [CheckedContinuation<Bool, Never>]) in
            if s.closeCode == nil, code != 0 { s.closeCode = code }
            // A connection the server closed, or this side dropped, did not break.
            if s.failure == nil, s.closeCode == nil, !s.dropped { s.failure = error }
            let end = PathEnd(closeCode: s.closeCode, error: s.closeCode != nil ? nil : s.failure)
            s.end = end
            s.pinger?.cancel()
            s.pinger = nil
            defer {
                s.waiters = []
                s.openWaiters = []
            }
            return (s.waiters, s.openWaiters)
        }
        let end = state.withLockUnchecked { $0.end! }
        for w in waiters { w.resume(returning: end) }
        for w in openWaiters { w.resume(returning: false) }
    }

    /// Ends the connection because it broke; the first reason is the one kept.
    private func fail(_ error: Error) {
        state.withLockUnchecked { s in
            if s.failure == nil, !s.dropped { s.failure = error }
        }
        task.cancel()
    }

    /// Whether the upgrade went through; false when the connection ended before it did.
    func opened() async -> Bool {
        await withCheckedContinuation { c in
            let now: Bool? = state.withLockUnchecked { s in
                if s.opened { return true }
                if s.end != nil { return false }
                s.openWaiters.append(c)
                return nil
            }
            if let now { c.resume(returning: now) }
        }
    }

    /// Arms an initial receive before `resume()` so an immediate server close frame
    /// is drained even if the peer closes before `receive()` starts or `hello` finishes sending.
    private func primeReceive() {
        task.receive { [weak self] result in
            guard let self else { return }
            let code = self.task.closeCode.rawValue
            let waiter = self.state.withLockUnchecked { s -> CheckedContinuation<URLSessionWebSocketTask.Message, Error>? in
                if case .failure(let error) = result {
                    if s.closeCode == nil, code != 0 { s.closeCode = code }
                    if s.failure == nil, s.closeCode == nil, !s.dropped { s.failure = error }
                }
                let waiter = s.primedWaiter
                if waiter != nil {
                    s.primedWaiter = nil
                    s.primed = .consumed
                } else {
                    s.primed = .ready(result)
                }
                return waiter
            }
            if case .failure = result {
                // Nothing more can come; this makes the task report that it is over.
                self.task.cancel()
            }
            waiter?.resume(with: result)
        }
    }

    private static func decode(_ message: URLSessionWebSocketTask.Message) -> Data {
        switch message {
        case .data(let d): return d
        case .string(let s): return Data(s.utf8)
        @unknown default: return Data()
        }
    }

    /// The next binary frame. Throws when the connection is over; `ended` says how.
    func receive() async throws -> Data {
        enum Step {
            case ready(Result<URLSessionWebSocketTask.Message, Error>)
            case wait
            case next
        }
        let step = state.withLockUnchecked { s -> Step in
            switch s.primed {
            case .ready(let result):
                s.primed = .consumed
                return .ready(result)
            case .pending:
                return .wait
            case .consumed:
                return .next
            }
        }
        switch step {
        case .ready(let result):
            return Self.decode(try result.get())
        case .wait:
            let message = try await withCheckedThrowingContinuation { c in
                enum ResumeNow {
                    case ready(Result<URLSessionWebSocketTask.Message, Error>)
                    case parked
                    case fallback
                }
                let action = state.withLockUnchecked { s -> ResumeNow in
                    switch s.primed {
                    case .ready(let result):
                        s.primed = .consumed
                        return .ready(result)
                    case .pending:
                        s.primedWaiter = c
                        return .parked
                    case .consumed:
                        return .fallback
                    }
                }
                switch action {
                case .ready(let result): c.resume(with: result)
                case .parked: break
                case .fallback: c.resume(throwing: CancellationError())
                }
            }
            return Self.decode(message)
        case .next:
            do {
                return Self.decode(try await task.receive())
            } catch {
                let code = task.closeCode.rawValue
                state.withLockUnchecked { s in
                    if s.closeCode == nil, code != 0 { s.closeCode = code }
                    if s.failure == nil, s.closeCode == nil, !s.dropped { s.failure = error }
                }
                // Nothing more can come; this makes the task report that it is over.
                task.cancel()
                throw error
            }
        }
    }

    /// Sends a frame; `done` hears whether it was written.
    func send(_ data: Data, done: @escaping @Sendable (Error?) -> Void = { _ in }) {
        task.send(.data(data), completionHandler: done)
    }

    /// Drops the connection without a goodbye.
    func cancel() {
        state.withLockUnchecked { $0.dropped = true }
        task.cancel()
    }

    /// Closes the connection normally.
    func close() {
        state.withLockUnchecked { $0.dropped = true }
        task.cancel(with: .normalClosure, reason: nil)
    }

    /// How the connection ended; waits until the task is done with.
    func ended() async -> PathEnd {
        await withCheckedContinuation { c in
            let end = state.withLockUnchecked { s -> PathEnd? in
                if s.end == nil { s.waiters.append(c) }
                return s.end
            }
            if let end { c.resume(returning: end) }
        }
    }
}

/// Counts queued frames and the one being processed, until processing returns.
final class StreamByteBudget: Sendable {
    private let used = OSAllocatedUnfairLock(initialState: 0)
    private let limit: Int

    init(limit: Int) { self.limit = max(0, limit) }

    func reserve(_ bytes: Int) -> Bool {
        used.withLock {
            guard bytes <= limit - $0 else { return false }
            $0 += bytes
            return true
        }
    }

    func release(_ bytes: Int) { used.withLock { $0 -= bytes } }
}
