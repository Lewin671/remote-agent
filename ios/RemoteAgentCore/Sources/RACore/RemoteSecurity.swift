import Foundation
import RAProto
import SwiftProtobuf

extension Remote {
    func securitySession() throws -> DeviceSecurityClient {
        if let deviceSecurity { return deviceSecurity }
        guard let securityStorage else { throw DeviceSecurity.Failure.storage }
        let record = try securityStorage.readSecurity()
        guard record.memberID == account.deviceId else {
            throw DeviceSecurity.Failure.untrusted
        }
        if !account.accountId.isEmpty, record.pin.account != account.accountId {
            throw DeviceSecurity.Failure.untrusted
        }
        let api = self.api
        let client = DeviceSecurityClient(
            store: securityStorage, memberID: account.deviceId, signingKey: device,
            clock: { [clock] in UInt64(bitPattern: clock()) },
            fetchStates: { [weak self] after in
                guard let self else { throw DeviceSecurity.Failure.storage }
                let token = try await self.currentToken()
                var request = PBGetSecurityStatesRequest()
                request.afterEpoch = after
                request.limit = 8
                return try await api.client.getSecurityStates(
                    request: request, headers: Api.headers(token: token)
                ).value()
            })
        deviceSecurity = client
        return client
    }
    func authorizationStateHash() async throws -> Data {
        do { return try await securitySession().headStateHash() } catch {
            if error is CancellationError { throw error }
            throw CommandError("authorization_unavailable", "未能核实当前安全状态，操作没有发出；请重试")
        }
    }
    func authorizePrepared(_ p: PreparedCommand, verifyObservation: Bool = true) async throws
        -> PBEnvelope
    {
        do {
            return try await authorizeIntent(p, verifyObservation: verifyObservation)
        } catch let e as CommandError {
            throw e
        } catch DeviceSecurity.Failure.lease {
            throw CommandError(
                "failed_precondition", "远程控制已暂停：请在手机上打开 App 续期后再试")
        } catch {
            if error is CancellationError { throw error }
            throw CommandError("authorization_unavailable", "安全状态尚未核实，操作没有发出；请重试")
        }
    }
    private func authorizeIntent(_ p: PreparedCommand, verifyObservation: Bool) async throws
        -> PBEnvelope
    {
        guard clockWarning.isEmpty else {
            throw CommandError(
                "failed_precondition", "本机时间比安全状态慢 5 分钟以上，控制已暂停；请校准系统时间后重试")
        }
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
    public func securityIdentity() throws -> (DeviceSecurity.Pin, DeviceSecurity.State) {
        guard let storage = securityStorage else { throw DeviceSecurity.Failure.storage }
        let r = try storage.readSecurity()
        guard r.memberID == account.deviceId else { throw DeviceSecurity.Failure.untrusted }
        if !account.accountId.isEmpty, r.pin.account != account.accountId {
            throw DeviceSecurity.Failure.untrusted
        }
        return (r.pin, r.state)
    }
    func openSecureMessage<M: SwiftProtobuf.Message>(
        envelope: PBEnvelope?, want: Place, as type: M.Type, history: Bool = false
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
        // Live frames are authorized by the state they name and the sender's
        // current membership; stored history is checked against its own state.
        let seed: [DeviceSecurity.Purpose] = [
            .event, .image, .terminalInput, .terminalOutput, .terminalHistory, .terminalResize,
        ]
        let header = try DeviceSecurity.parseHeader(envelope.securityHeader)
        let sessionSeed = seed.contains(header.purpose) ? sessionKeys[header.session] : nil
        let (h, m) = try await securitySession().openMessage(
            envelope, as: type, history: history, sessionSeed: sessionSeed)
        let projected = projection(h)
        try Envelopes.expect(projected, want)
        return (projected, m)
    }
    func projection(_ h: DeviceSecurity.Header) -> PBHeader {
        var out = PBHeader()
        out.version = 3
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
