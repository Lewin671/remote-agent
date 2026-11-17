import Connect
import Foundation
import RAProto
import os

final class RelayPath: Path {
    private let api: Api
    private let sleep: Sleeper
    private let prepareToken: @Sendable (Bool) async throws -> Void
    private let readToken: @Sendable () async throws -> String
    private let renewToken: @Sendable (Bool) async throws -> Void
    private struct Observation: Sendable {
        var peers: [String: PeerState] = [:]
        var fallback = PeerState()
    }
    private let observation = Feed(Observation())

    init(api: Api, sleep: @escaping Sleeper,
         prepare: @escaping @Sendable (Bool) async throws -> Void = { _ in },
         token: @escaping @Sendable () async throws -> String = { "" },
         renew: @escaping @Sendable (Bool) async throws -> Void = { _ in }) {
        self.api = api
        self.sleep = sleep
        prepareToken = prepare; readToken = token; renewToken = renew
    }
    func observe(_ peers: [String: PeerState], fallback: PeerState) { observation.set(Observation(peers: peers, fallback: fallback)) }
    func status(_ machineId: String) -> PeerState {
        let state = observation.value
        return state.peers[machineId] ?? state.fallback
    }
    static func discovery(_ machine: MachineView?, ready: Bool = true, authorized: Bool = true, unlinked: Bool = false) -> PeerState {
        var status = PeerState()
        status.reachable = machine?.online == true
        status.ready = ready
        status.authorized = authorized
        status.unlinked = unlinked
        status.protocolVersion = machine?.meta?.protocolVersion
        status.capabilities = machine?.meta.flatMap { $0.hasCapabilities ? $0.capabilities : nil }
        return status
    }
    func attempt(handoff: @escaping @Sendable () throws -> Void) -> any PathAttempt {
        attempt(attemptId: newRequestId(), handoff: handoff)
    }
    func attempt(attemptId: String, handoff: @escaping @Sendable () throws -> Void) -> any PathAttempt {
        Attempt(attemptId: attemptId.isEmpty ? newRequestId() : attemptId, path: self, handoff: handoff)
    }
    private final class Attempt: PathAttempt {
        let attemptId: String
        private let path: RelayPath
        private let handoff: @Sendable () throws -> Void
        private let epoch = OSAllocatedUnfairLock(initialState: "")
        init(attemptId: String, path: RelayPath, handoff: @escaping @Sendable () throws -> Void) {
            self.attemptId = attemptId; self.path = path; self.handoff = handoff
        }
        func prepare(prior: Bool) async throws { try await path.prepareToken(prior) }
        func send(machineId: String, command: PBEnvelope) async throws -> PBForwardResponse {
            let current = try await path.api.relayEpoch()
            try Task.checkCancellation()
            let token = try await path.readToken()
            try handoff()
            epoch.withLock { $0 = current }
            var request = PBForwardRequest()
            request.machineID = machineId; request.command = command
            request.attemptID = attemptId; request.relayEpoch = current
            var headers = Api.headers(token: token)
            headers["Remote-Agent-Attempt"] = [attemptId]
            return try await path.api.client.forward(request: request, headers: headers).value()
        }
        func refusal(_ error: ConnectError) -> PathRefusal? {
            let current = epoch.withLock { $0 }
            let proof = (error.unpackedDetails() as [PBNotForwarded]).first { $0.attemptID == attemptId && $0.relayEpoch == current && !current.isEmpty }
            return proof.map { PathRefusal(reason: $0.reason) }
        }
        func refreshAuth(prior: Bool) async throws { try await path.renewToken(prior) }
    }
    func openStream(_ route: String, hello: Data) -> any FrameStream {
        Socket(http: api.http, url: api.socketURL(route), hello: hello, sleep: sleep)
    }
}
