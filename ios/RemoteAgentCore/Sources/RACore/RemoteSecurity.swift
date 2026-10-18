import Foundation
import RAProto
import SwiftProtobuf

extension Remote {
    func securitySession() throws -> DeviceSecurityClient {
        if let deviceSecurity { return deviceSecurity }
        guard let securityStorage else { throw DeviceSecurity.Failure.storage }
        let record = try securityStorage.readSecurity()
        guard record.memberID == account.deviceId,
            record.pin.account == deviceId(publicKey: keys.account.publicKey)
        else { throw DeviceSecurity.Failure.untrusted }
        let client = DeviceSecurityClient(
            store: securityStorage, memberID: account.deviceId, signingKey: device,
            exchange: { [weak self] capsule in
                guard let self else { throw DeviceSecurity.Failure.storage }
                return try await self.exchangeSecurity(capsule)
            })
        deviceSecurity = client
        return client
    }
    private func exchangeSecurity(_ capsule: DeviceSecurity.Capsule) async throws
        -> DeviceSecurity.Capsule
    {
        if token.isEmpty { token = try await relogin(false) }
        var request = PBForwardRequest()
        request.machineID = capsule.to
        request.securityRpc = try DeviceSecurity.capsuleBytes(capsule)
        let reply = try await api.client.forward(
            request: request, headers: Api.headers(token: token)
        )
        .value()
        guard !reply.hasResult else { throw DeviceSecurity.Failure.untrusted }
        return try DeviceSecurity.parseCapsule(reply.securityRpc)
    }
    func authorizationStateHash() async throws -> Data {
        do { return try DeviceSecurity.stateHash(await securitySession().sync().state) } catch {
            if error is CancellationError { throw error }
            throw CommandError("authorization_unavailable", "未能核实当前安全状态，操作没有发出；请重试")
        }
    }
    func authorizePrepared(_ p: PreparedCommand, verifyObservation: Bool = true) async throws
        -> PBEnvelope
    {
        do { return try await authorizeIntent(p, verifyObservation: verifyObservation) } catch {
            if error is CancellationError { throw error }
            throw CommandError("authorization_unavailable", "安全授权或当前请求尚未核实，操作没有发出；请重试")
        }
    }
    private func authorizeIntent(_ p: PreparedCommand, verifyObservation: Bool) async throws
        -> PBEnvelope
    {
        let client = try securitySession()
        guard let cmd = p.command else {
            _ = try DeviceSecurity.fromEnvelope(p.envelope)
            return p.envelope
        }
        if verifyObservation && isObservedAction(cmd) {
            let fresh = try await fetchLiveSnapshot(p.sessionId)
            let verified = try observedCommand(cmd, snapshot: fresh)
            guard verified == cmd else {
                throw CommandError("failed_precondition", "当前请求已变化，请重新核对")
            }
        }
        return try await client.seal(
            purpose: .command, machine: p.machineId, session: p.sessionId, request: p.requestId,
            deadline: UInt64(bitPattern: p.deadlineMs), plaintext: cmd.serializedData())
    }
    public func securityRPC(_ method: DeviceSecurity.Method, jsonBody: Data) async throws -> Data {
        try await securitySession().rpc(method, jsonBody: jsonBody)
    }
    public func securityIdentity() throws -> (DeviceSecurity.Pin, DeviceSecurity.State) {
        guard let storage = securityStorage else { throw DeviceSecurity.Failure.storage }
        let r = try storage.readSecurity()
        guard r.memberID == account.deviceId,
            r.pin.account == deviceId(publicKey: keys.account.publicKey)
        else { throw DeviceSecurity.Failure.untrusted }
        return (r.pin, r.state)
    }
    func openSecureMessage<M: SwiftProtobuf.Message>(
        envelope: PBEnvelope?, want: Place, as type: M.Type
    ) async throws -> (PBHeader, M) {
        guard let envelope else { throw DeviceSecurity.Failure.invalid }
        // Old images are readable transcript data only. They never establish
        // current permissions, directory metadata, or command results.
        if want.kind == .image, try PBHeader(serializedBytes: envelope.header).version == 1 {
            guard let key = sessionKeys[want.sessionId] else {
                throw DeviceSecurity.Failure.invalid
            }
            return try Envelopes.openMessage(key: key, envelope: envelope, want: want, as: type)
        }
        // A fixed request/stream and sequence authenticate delayed pre-fence reads.
        // Historical verification never authorizes an incoming control operation.
        let live = false
        let seed: [DeviceSecurity.Purpose] = [
            .event, .image, .terminalInput, .terminalOutput, .terminalHistory, .terminalResize,
        ]
        let header = try DeviceSecurity.parseHeader(envelope.securityHeader)
        let sessionSeed = seed.contains(header.purpose) ? sessionKeys[header.session] : nil
        let (h, m) = try await securitySession().openMessage(
            envelope, as: type, history: !live, sessionSeed: sessionSeed)
        let projected = projection(h)
        try Envelopes.expect(projected, want)
        return (projected, m)
    }
    func projection(_ h: DeviceSecurity.Header) -> PBHeader {
        var out = PBHeader()
        out.version = 2
        out.kind = PBPayloadKind(rawValue: Int(h.kind))!
        out.machineID = h.machine
        out.sessionID = h.session
        out.requestID = h.request
        out.seq = h.seq
        out.deadlineMs = Int64(bitPattern: h.deadline)
        out.sender = h.sender
        out.compression = PBCompression(rawValue: Int(h.compression))!
        return out
    }
}
