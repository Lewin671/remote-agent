import Connect
import Foundation
import RAProto
import SwiftProtobuf

extension Remote {
    public nonisolated var usesCoreSecurity: Bool { true }

    func securitySession() throws -> AuthoritySecurity {
        if let deviceSecurity { return deviceSecurity }
        guard let securityStorage else { throw DeviceSecurity.Failure.storage }
        let record = try securityStorage.readSecurity()
        guard record.memberID == account.deviceId else {
            throw DeviceSecurity.Failure.untrusted
        }
        if !account.accountId.isEmpty, record.pin.account != account.accountId {
            throw DeviceSecurity.Failure.untrusted
        }
        latestXPrivateLock.withLock { $0 = record.xPrivateRaw }
        let client = AuthoritySecurity(
            store: securityStorage,
            memberID: account.deviceId,
            signingKey: device,
            clock: { [clock] in UInt64(bitPattern: clock()) },
            pullTimeoutMs: 3000,
            observer: securityObserver(),
            fetchStates: { [weak self] after in
                guard let self else { throw DeviceSecurity.Failure.storage }
                return try await self.fetchSecurityPage(after)
            }
        )
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

    func ensureFallbackRunner() throws -> CoreSecurityRunner {
        if let fallbackSecurityRunner { return fallbackSecurityRunner }
        let runner = try CoreSecurityRunner(executor: securityExecutor)
        fallbackSecurityRunner = runner
        return runner
    }

    func nextSecurityPushSeq() -> UInt64 {
        let seq = securityPushSeq
        securityPushSeq &+= 1
        return seq
    }

    func ensureSecurityConfigured() async {
        guard !securityConfiguredInCore else { return }
        if let onConfig = hostHooks?.onSecurityConfig {
            securityConfiguredInCore = true
            let cfg = PBSecurityConfigEvent.with {
                $0.account = account.accountId
                $0.deviceID = account.deviceId
                $0.inlineCrypto = true
            }
            await onConfig(cfg)
        } else {
            do {
                try ensureFallbackRunner().configure(account: account.accountId, deviceId: account.deviceId, nowMs: clock())
                securityConfiguredInCore = true
            } catch {
                // Propagated if an op runs against unconfigured runner.
            }
        }
    }

    func buildSecurityRecordEvent(
        record: DeviceSecurity.Record,
        opId: UInt64,
        pushSeq: UInt64,
        networkGeneration netGen: UInt64,
        recoveryPending: Bool,
        recoveryReadyMs: UInt64,
        recoveryObjectsPending: Bool
    ) throws -> PBSecurityRecordEvent {
        latestXPrivateLock.withLock { $0 = record.xPrivateRaw }
        authorityDeviceId = record.state.core.authorityID
        leaseEndMs = record.lease?.notAfter ?? 0
        return try CoreSecurityRunner.makeRecordEvent(
            record: record,
            opId: opId,
            pushSeq: pushSeq,
            networkGeneration: netGen,
            recoveryPending: recoveryPending,
            recoveryReadyMs: recoveryReadyMs,
            recoveryObjectsPending: recoveryObjectsPending
        )
    }

    func ensureSecurityRecordLoaded() async throws {
        await ensureSecurityConfigured()
        if securityRecordLoaded { return }
        guard let securityStorage else { throw DeviceSecurity.Failure.storage }
        let record = try securityStorage.readSecurity()
        let ev = try buildSecurityRecordEvent(
            record: record,
            opId: 0,
            pushSeq: nextSecurityPushSeq(),
            networkGeneration: networkGeneration,
            recoveryPending: recoveryPending,
            recoveryReadyMs: recoveryReadyMs,
            recoveryObjectsPending: recoveryObjectsPending
        )
        if let onWait = hostHooks?.onSecurityRecordAndWait {
            await onWait(ev)
        } else {
            try ensureFallbackRunner().syncRecord(ev, nowMs: clock())
        }
        securityRecordLoaded = true
    }

    func runSecurityOp(_ configure: @Sendable (inout PBSecurityOpRequestEvent) -> Void) async throws -> PBSecurityOpResultEffect {
        try await ensureSecurityRecordLoaded()
        if let onOp = hostHooks?.onSecurityOp {
            var req = PBSecurityOpRequestEvent()
            configure(&req)
            let res = await onOp(req)
            if !res.error.isEmpty {
                throw CoreSecurityRunner.mapOpError(res)
            }
            return res
        }
        let runner = try ensureFallbackRunner()
        if let record = try? securityStorage?.readSecurity() {
            try runner.syncRecord(
                record,
                recoveryPending: recoveryPending,
                recoveryReadyMs: recoveryReadyMs,
                recoveryObjectsPending: recoveryObjectsPending,
                nowMs: clock()
            )
        }
        return try runner.runOp(configure)
    }

    func sealEnvelopeOp(
        purpose: DeviceSecurity.Purpose,
        machineId: String = "",
        sessionId: String = "",
        requestId: String = "",
        seq: UInt64 = 0,
        deadlineMs: Int64 = 0,
        plaintext: Data,
        useSessionSeed: Bool = false,
        overrideSessionSeed: Data = Data()
    ) async throws -> PBSecuritySealEnvelopeResult {
        let res = try await runSecurityOp {
            $0.sealEnvelope = .with {
                $0.purpose = purpose.rawValue
                $0.machineID = machineId
                $0.sessionID = sessionId
                $0.requestID = requestId
                $0.seq = seq
                $0.deadlineMs = deadlineMs
                $0.plaintext = plaintext
                $0.useSessionSeed = useSessionSeed
                if !overrideSessionSeed.isEmpty { $0.overrideSessionSeed = overrideSessionSeed }
            }
        }
        guard case .sealed(let sealed)? = res.result else {
            throw DeviceSecurity.Failure.invalid
        }
        return sealed
    }

    func openEnvelopeOp(
        envelope: PBEnvelope,
        expectedPurpose: DeviceSecurity.Purpose,
        machineId: String = "",
        sessionId: String = "",
        requestId: String = "",
        seq: UInt64 = 0,
        history: Bool = false,
        overrideSessionSeed: Data = Data()
    ) async throws -> PBSecurityOpenEnvelopeResult {
        let res = try await runSecurityOp {
            $0.openEnvelope = .with {
                $0.envelope = envelope
                $0.expectedPurpose = expectedPurpose.rawValue
                $0.machineID = machineId
                $0.sessionID = sessionId
                $0.requestID = requestId
                $0.seq = seq
                $0.history = history
                if !overrideSessionSeed.isEmpty { $0.overrideSessionSeed = overrideSessionSeed }
            }
        }
        guard case .opened(let opened)? = res.result else {
            throw DeviceSecurity.Failure.invalid
        }
        return opened
    }

    func hasSessionSeed(_ sid: String) async -> Bool {
        guard !sid.isEmpty else { return false }
        do {
            let res = try await runSecurityOp {
                $0.getSessionSeed = .with { $0.sessionID = sid }
            }
            if case .sessionSeed(let seed)? = res.result {
                return seed.count == keySize
            }
            return false
        } catch {
            return false
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
    ) async throws -> PreparedCommand.AuthorizedCommand {
        do {
            guard !clockSkewed else {
                throw CommandError("failed_precondition", L10n.securityIosThisDeviceIsOver5MinutesBehindTheSecurity())
            }
            let client = try securitySession()
            _ = try await client.sync()
            let sealed = try await sealEnvelopeOp(
                purpose: .command,
                machineId: machineId,
                sessionId: sessionId,
                requestId: requestId,
                deadlineMs: deadlineMs,
                plaintext: commandBytes
            )
            return PreparedCommand.AuthorizedCommand(
                envelope: sealed.envelope,
                keyHash: sealed.requestKeyHash,
                payloadHash: sealed.payloadHash
            )
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
    func authorizePrepared(_ p: PreparedCommand) async throws -> PreparedCommand.AuthorizedCommand {
        try await authorizeCommandBytes(
            machineId: p.machineId,
            sessionId: p.sessionId,
            requestId: p.requestId,
            deadlineMs: p.deadlineMs,
            commandBytes: try p.commandBytes()
        )
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
        envelope: PBEnvelope?,
        want: Place,
        as type: M.Type,
        history: Bool = false
    ) async throws -> (PBHeader, M) {
        guard let envelope else { throw DeviceSecurity.Failure.invalid }
        let expectedPurpose: DeviceSecurity.Purpose
        switch want.kind {
        case .event: expectedPurpose = .event
        case .sessionMeta: expectedPurpose = .sessionMeta
        case .sessionKey: expectedPurpose = .sessionKey
        case .machineMeta: expectedPurpose = .machineMeta
        case .command: expectedPurpose = .command
        case .commandResult: expectedPurpose = .result
        case .terminalOutput: expectedPurpose = .terminalOutput
        case .terminalInput: expectedPurpose = .terminalInput
        case .notify: expectedPurpose = .notify
        case .image: expectedPurpose = .image
        case .accountPrefs: expectedPurpose = .prefs
        case .deviceMeta: expectedPurpose = .deviceMeta
        default: throw DeviceSecurity.Failure.invalid
        }
        let history = history || [PBPayloadKind.accountPrefs, .deviceMeta, .machineMeta, .sessionMeta, .image].contains(want.kind)
        let opened = try await openEnvelopeOp(
            envelope: envelope,
            expectedPurpose: expectedPurpose,
            machineId: want.machineId,
            sessionId: want.sessionId,
            requestId: want.requestId,
            seq: want.seq,
            history: history
        )
        let msg = try M(serializedBytes: opened.plaintext)
        return (Self.projection(opened), msg)
    }

    private static func projection(_ r: PBSecurityOpenEnvelopeResult) -> PBHeader {
        var out = PBHeader()
        out.version = 3
        let rawKind: Int = (r.purpose == DeviceSecurity.Purpose.terminalResize.rawValue
            || r.purpose == DeviceSecurity.Purpose.terminalHistory.rawValue)
            ? Int(DeviceSecurity.Purpose.terminalInput.rawValue)
            : Int(r.purpose)
        out.kind = PBPayloadKind(rawValue: rawKind) ?? .unspecified
        out.machineID = r.machineID
        out.sessionID = r.sessionID
        out.requestID = r.requestID
        out.seq = r.seq
        out.deadlineMs = r.deadlineMs
        out.sender = r.sender
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
