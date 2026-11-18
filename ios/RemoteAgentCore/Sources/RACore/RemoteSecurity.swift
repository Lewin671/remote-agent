import Connect
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
        let client = DeviceSecurityClient(
            store: securityStorage, memberID: account.deviceId, signingKey: device,
            clock: { [clock] in UInt64(bitPattern: clock()) },
            pullTimeoutMs: 3000,
            observer: securityObserver(),
            fetchStates: { [weak self] after in
                guard let self else { throw DeviceSecurity.Failure.storage }
                return try await self.fetchSecurityPage(after)
            })
        deviceSecurity = client
        return client
    }

    private func fetchSecurityPage(_ after: UInt64) async throws -> PBGetSecurityStatesResponse {
        var req = PBGetSecurityStatesRequest(); req.afterEpoch = after; req.limit = 8
        let request = req
        do {
            let response = try await withAuthRetry { [api] headers in
                try await api.client.getSecurityStates(request: request, headers: headers).value()
            }
            try Task.checkCancellation()
            receivedRecoveryStatus(response)
            return response
        } catch {
            if !Task.isCancelled { markRecoveryStatusStale() }
            throw error
        }
    }

    func authorizationStateHash() async throws -> Data {
        do { return try await securitySession().headStateHash() } catch {
            if error is CancellationError { throw error }
            throw CommandError("authorization_unavailable", L10nMessage.securityIosCouldNotVerifyTheCurrentSecurityStateTheOperation())
        }
    }

    func authorizeCommandBytes(
        machineId: String,
        sessionId: String,
        requestId: String,
        deadlineMs: Int64,
        commandBytes: Data
    ) async throws -> PBEnvelope {
        do {
            guard !clockSkewed else {
                throw CommandError("failed_precondition", L10n.securityIosThisDeviceIsOver5MinutesBehindTheSecurity())
            }
            let client = try securitySession()
            return try await client.seal(
                purpose: .command, machine: machineId, session: sessionId, request: requestId,
                deadline: UInt64(bitPattern: deadlineMs), plaintext: commandBytes)
        } catch let e as CommandError {
            throw e
        } catch DeviceSecurity.Failure.lease {
            let devices = state.value.devices
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

    /// Transitional Step 4 helper used by `PreparedCommand` until Schedules migrate to Core.
    func authorizePrepared(_ p: PreparedCommand) async throws -> PBEnvelope {
        try await authorizeCommandBytes(
            machineId: p.machineId, sessionId: p.sessionId,
            requestId: p.requestId, deadlineMs: p.deadlineMs,
            commandBytes: try p.commandBytes())
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
        var expected = want
        if history && [.event, .image].contains(want.kind) && header.machineID != want.machineId {
            let proofs = machineReplacements[want.sessionId] ?? state.value.sessions[want.sessionId]?.info?.machineReplacements ?? []
            let origins = try await securitySession().replacementOrigins(target: want.machineId, proofs: proofs)
            if origins.contains(header.machineID) { expected.machineId = header.machineID }
        }
        return expected
    }

    func openSecureMessage<M: SwiftProtobuf.Message>(
        envelope: PBEnvelope?, want: Place, as type: M.Type, history: Bool = false
    ) async throws -> (PBHeader, M) {
        guard let envelope else { throw DeviceSecurity.Failure.invalid }
        let history = history || [PBPayloadKind.accountPrefs, .deviceMeta, .machineMeta, .sessionMeta, .image].contains(want.kind)
        try rejectRetiredHistory(envelope)
        let seed: [DeviceSecurity.Purpose] = [
            .event, .image, .terminalInput, .terminalOutput, .terminalHistory, .terminalResize,
        ]
        let header = try DeviceSecurity.parseHeader(envelope.securityHeader)
        let sessionSeed = seed.contains(header.purpose) ? sessionKeys[header.session] : nil
        let (h, m) = try await securitySession().openMessage(
            envelope, as: type, history: history, sessionSeed: sessionSeed)
        let projected = projection(h)
        try Envelopes.expect(projected, try await historicalPlace(want, header: projected, history: history))
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
    }

    func markRecoveryStatusStale() {
        recoveryStatusStale = true
    }
}
