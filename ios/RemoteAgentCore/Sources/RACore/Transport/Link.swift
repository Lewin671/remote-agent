import Connect
import Foundation
import RAProto
import os

/// Owns transport route selection and logical streams; Core owns command
/// lifecycle, retries, and outcome classification.
final class Link: Sendable {
    typealias DirectBuilder = @Sendable (@escaping @Sendable () throws -> Void) -> (any PathAttempt)?
    let router: Router
    let peers = Feed<[String: PeerState]>([:])
    private let relay: RelayPath
    private struct Streams {
        var closed = false
        var active: [UUID: Stream] = [:]
    }
    private let streams = OSAllocatedUnfairLock(initialState: Streams())

    init(relay: RelayPath) {
        self.relay = relay
        router = Router(path: relay)
    }
    func status(_ machineId: String) -> PeerState { router.status(machineId) }
    func observe(_ state: RemoteState) -> [String: PeerState] {
        let observations = state.machines.mapValues { RelayPath.discovery($0, ready: state.conn == .online, authorized: !state.removed, unlinked: state.unlinked) }
        relay.observe(observations, fallback: RelayPath.discovery(nil, ready: state.conn == .online, authorized: !state.removed, unlinked: state.unlinked))
        let states = state.machines.mapValues { status($0.id) }
        if states != peers.value { peers.set(states) }
        return states
    }

    /// `direct` gives a DirectPath attempt when routing selects direct; otherwise the relay carries it.
    func delivery(
        attemptId: String = newRequestId(),
        direct: DirectBuilder? = nil,
        handoff: @escaping @Sendable () throws -> Void = {}
    ) -> Delivery {
        Delivery(path: relay, attemptId: attemptId, direct: direct, handoff: handoff)
    }

    final class Delivery: PathAttempt {
        var attemptId: String { attempt.attemptId }
        var isDirect: Bool { attempt.isDirect }
        private let attempt: any PathAttempt
        init(path: RelayPath, attemptId: String, direct: DirectBuilder? = nil, handoff: @escaping @Sendable () throws -> Void) {
            attempt = direct?(handoff) ?? path.attempt(attemptId: attemptId, handoff: handoff)
        }
        func prepare(prior: Bool) async throws { try await attempt.prepare(prior: prior) }
        func send(machineId: String, command: PBEnvelope) async throws -> PBForwardResponse { try await attempt.send(machineId: machineId, command: command) }
        func refreshAuth(prior: Bool) async throws { try await attempt.refreshAuth(prior: prior) }
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
            self.remove = { [weak owner] id in _ = owner?.streams.withLock { $0.active.removeValue(forKey: id) } }; self.stop = stop; self.onClose = onClose
        }
        func end(_ reason: String? = nil, cancelPath: Bool = true) {
            guard ended.withLock({ value in if value { return false }; value = true; return true }) else { return }
            remove(id)
            stop(cancelPath)
            // Preserve relay-only closing policy at the logical boundary.
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
