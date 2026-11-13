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
                var request = PBGetSecurityStatesRequest()
                request.afterEpoch = after
                request.limit = 8
                do {
                    let token = try await self.currentToken()
                    let result = try await api.client.getSecurityStates(request: request, headers: Api.headers(token: token)).value()
                    await self.receivedRecoveryStatus(result)
                    return result
                } catch {
                    await self.markRecoveryStatusStale()
                    throw error
                }
            })
        deviceSecurity = client
        return client
    }
    func authorizationStateHash() async throws -> Data {
        do { return try await securitySession().headStateHash() } catch {
            if error is CancellationError { throw error }
            throw CommandError("authorization_unavailable", L10nMessage.securityIosCouldNotVerifyTheCurrentSecurityStateTheOperation())
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
            let renewal = authorityDeviceId == account.deviceId
                ? L10n.securityIosOpenSettingsAccountSecurityAndTapRenewNow()
                : L10n.securityIosOpenToRenew(value: String(describing: clientName(devices[authorityDeviceId], id: authorityDeviceId, among: devices)))
            throw CommandError(
                "failed_precondition", L10nMessage.errorsIosExpiredControlRenewal(renewal: renewal))
        } catch {
            if error is CancellationError { throw error }
            throw CommandError("authorization_unavailable", L10nMessage.securityIosSecurityStateIsUnverifiedTheOperationWasNotSent())
        }
    }
    private func authorizeIntent(_ p: PreparedCommand, verifyObservation: Bool) async throws
        -> PBEnvelope
    {
        guard clockWarning.isEmpty else {
            throw CommandError(
                "failed_precondition", L10n.securityIosThisDeviceIsOver5MinutesBehindTheSecurity())
        }
        let client = try securitySession()
        let cmd = p.command
        if verifyObservation && isObservedAction(cmd) {
            let fresh = try await fetchLiveSnapshot(p.sessionId)
            let verified = try observedCommand(cmd, snapshot: fresh)
            guard verified == cmd else {
                throw CommandError("failed_precondition", L10nMessage.securityIosTheCurrentRequestChangedReviewItAgain())
            }
        }
        return try await client.seal(
            purpose: .command, machine: p.machineId, session: p.sessionId, request: p.requestId,
            deadline: UInt64(bitPattern: p.deadlineMs), plaintext: p.commandBytes())
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
    func historicalPlace(_ want: Place, header: PBHeader, history: Bool) async throws -> Place {
        var expected=want
        if history && [.event,.image].contains(want.kind) && header.machineID != want.machineId {
            let origins=try await securitySession().replacementOrigins(target:want.machineId,proofs:sessions[want.sessionId]?.info?.machineReplacements ?? [])
            if origins.contains(header.machineID) {expected.machineId=header.machineID}
        }
        return expected
    }
    func openSecureMessage<M: SwiftProtobuf.Message>(
        envelope: PBEnvelope?, want: Place, as type: M.Type, history: Bool = false
    ) async throws -> (PBHeader, M) {
        guard let envelope else { throw DeviceSecurity.Failure.invalid }
        let history = history || [PBPayloadKind.accountPrefs, .deviceMeta, .machineMeta, .sessionMeta, .image].contains(want.kind)
        try rejectRetiredHistory(envelope)
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
        try Envelopes.expect(projected, try await historicalPlace(want,header:projected,history:history))
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

extension Remote {
    func receivedRecoveryStatus(_ response: PBGetSecurityStatesResponse) {
        recoveryPending = response.recoveryPending
        recoveryReadyMs = response.recoveryReadyMs
        recoveryStatusStale = false
        publish()
    }
    func markRecoveryStatusStale() { recoveryStatusStale = true; publish() }
}
