import Connect
import Foundation
import RAProto
import os

final class RelayPath: Path {
    private let api: Api
    private let sleep: Sleeper
    private let prepareToken: @Sendable (Bool) async throws -> Void
    private let readToken: @Sendable () async throws -> String

    init(api: Api, sleep: @escaping Sleeper,
         prepare: @escaping @Sendable (Bool) async throws -> Void = { _ in },
         token: @escaping @Sendable () async throws -> String = { "" }) {
        self.api = api
        self.sleep = sleep
        prepareToken = prepare; readToken = token
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
    }
    func openStream(_ route: String, hello: Data) -> any FrameStream {
        Socket(http: api.http, url: api.socketURL(route), hello: hello, sleep: sleep)
    }
}
