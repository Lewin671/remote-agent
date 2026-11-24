import Connect
import Foundation
import RAProto
import os

/// Owns logical stream tracking and relay delivery attempts;
/// Core owns route selection, command lifecycle, retries, and outcome classification.
final class Link: Sendable {
    let relay: RelayPath
    private struct Streams {
        var closed = false
        var active: [UUID: Stream] = [:]
    }
    private let streams = OSAllocatedUnfairLock(initialState: Streams())

    init(relay: RelayPath) {
        self.relay = relay
    }

    func delivery(
        attemptId: String = newRequestId(),
        handoff: @escaping @Sendable () throws -> Void = {}
    ) -> Delivery {
        Delivery(path: relay, attemptId: attemptId, handoff: handoff)
    }

    final class Delivery: PathAttempt {
        var attemptId: String { attempt.attemptId }
        private let attempt: any PathAttempt
        init(path: RelayPath, attemptId: String, handoff: @escaping @Sendable () throws -> Void) {
            attempt = path.attempt(attemptId: attemptId, handoff: handoff)
        }
        func prepare(prior: Bool) async throws { try await attempt.prepare(prior: prior) }
        func send(machineId: String, command: PBEnvelope) async throws -> PBForwardResponse { try await attempt.send(machineId: machineId, command: command) }
        func refusal(_ error: ConnectError) -> PathRefusal? { attempt.refusal(error) }
    }

    func openStream(stop: @escaping @Sendable (Bool) -> Void, onClose: @escaping @Sendable (String) -> Void) -> Stream {
        let stream = Stream(owner: self, stop: stop, onClose: onClose)
        let registered = streams.withLock { state in
            guard !state.closed else { return false }
            state.active[stream.id] = stream
            return true
        }
        if !registered { stream.end() }
        return stream
    }

    final class Stream: Sendable {
        let id = UUID()
        private let remove: @Sendable (UUID) -> Void
        private let ended = OSAllocatedUnfairLock(initialState: false)
        private let stop: @Sendable (Bool) -> Void
        private let onClose: @Sendable (String) -> Void
        init(owner: Link, stop: @escaping @Sendable (Bool) -> Void, onClose: @escaping @Sendable (String) -> Void) {
            self.remove = { [weak owner] id in _ = owner?.streams.withLock { $0.active.removeValue(forKey: id) } }
            self.stop = stop
            self.onClose = onClose
        }
        func end(_ reason: String? = nil, cancelPath: Bool = true) {
            guard ended.withLock({ value in if value { return false }; value = true; return true }) else { return }
            remove(id)
            stop(cancelPath)
            if let reason { onClose(reason) }
        }
    }

    func closeStreams() {
        let owned = streams.withLock { state in
            state.closed = true
            return Array(state.active.values)
        }
        for stream in owned { stream.end() }
    }
}
