import Connect
import Foundation
import RAProto
import os

/// Owns call/delivery evidence and logical streams; socket lifetimes cannot
/// settle authorizations or call the product listener directly.
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
    func call<T: Sendable>(_ prepared: PreparedCommand, prior: Bool, work: @Sendable (DispatchState.Call) async throws -> T) async throws -> T {
        let call = prepared.dispatch.open(prior: prior)
        defer { call.close() }
        do { return try await work(call) }
        catch {
            let command = error as? CommandError
            if call.uncertain && command?.unknown != true && !(command?.fromDaemon == true && command?.outcome != .notSent) {
                throw CommandError.unknownBecause(errorText(error))
            }
            throw error
        }
    }

    /// `direct` gives a DirectPath attempt for a request the routing sends there, or nothing: then the relay carries it.
    func delivery(_ call: DispatchState.Call, direct: DirectBuilder? = nil) -> Delivery {
        Delivery(path: router.path, evidence: call.delivery(), direct: direct)
    }
    final class Delivery: PathAttempt {
        var attemptId: String { attempt.attemptId }
        var isDirect: Bool { attempt.isDirect }
        private let attempt: any PathAttempt
        private let evidence: DispatchState.Call.Delivery
        init(path: any Path, evidence: DispatchState.Call.Delivery, direct: DirectBuilder? = nil) {
            self.evidence = evidence
            let handoff: @Sendable () throws -> Void = { try evidence.dispatch() }
            attempt = direct?(handoff) ?? path.attempt(handoff: handoff)
        }
        func prepare(prior: Bool) async throws { try await attempt.prepare(prior: prior) }
        func send(machineId: String, command: PBEnvelope) async throws -> PBForwardResponse { try await attempt.send(machineId: machineId, command: command) }
        func refreshAuth(prior: Bool) async throws { try await attempt.refreshAuth(prior: prior) }
        func refusal(_ error: ConnectError) -> PathRefusal? {
            let proof = attempt.refusal(error)
            if proof != nil { evidence.notForwarded() }
            return proof
        }
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
