import Foundation
import RAProto
import SwiftProtobuf

extension Remote {
    // MARK: - Core Security Lifecycle & Workflows

    func ensureSecurityConfigured() async {
        guard !closed, !securityConfiguredInCore else { return }
        securityConfiguredInCore = true
        let cfg = PBSecurityConfigEvent.with {
            $0.account = account.accountId
            $0.deviceID = account.deviceId
            $0.server = account.server
            $0.inlineCrypto = true
            $0.foreground = foregroundActive
        }
        await emitSecurityCoreEvent(PBCoreEvent.with { $0.securityConfig = cfg })
    }

    public func ensureSecurityBootstrapped() async throws {
        if closed || securityBootstrapped { return }
        await ensureSecurityConfigured()
        if let existing = securityBootstrapTask {
            try await existing.value
            return
        }
        let task = Task<Void, Error> {
            let st = try await securityClient.bootstrap()
            await applySecurityStatusSnapshot(st)
            securityBootstrapped = true
        }
        securityBootstrapTask = task
        do {
            try await task.value
            securityBootstrapTask = nil
        } catch {
            securityBootstrapTask = nil
            throw error
        }
    }

    func emitSecurityCoreEvent(_ ev: PBCoreEvent) async {
        guard !closed else { return }
        if let onCore = hostHooks?.onCoreEvent {
            await onCore(ev)
            return
        }
        ensureFallbackSecurityDriver()?.enqueueEvent(ev)
    }

    func ensureFallbackSecurityDriver() -> CoreSecurityDriver? {
        guard !closed else { return nil }
        if let fallbackSecurityDriver { return fallbackSecurityDriver }
        let driver = try? CoreSecurityDriver(
            executor: securityExecutor,
            client: securityClient,
            clock: clock,
            sleep: sleep,
            onStatusSnapshot: { [weak self] st in
                Task { await self?.applySecurityStatusSnapshot(st) }
            },
            onTokenUpdated: { [weak self] eff in
                Task { await self?.applySecurityTokenUpdated(eff) }
            },
            onDispatchCommand: { [weak self] eff in
                guard let self else {
                    return PBCoreCommandOutcomeEvent.with {
                        $0.requestID = eff.requestID
                        $0.attemptID = eff.attemptID
                        $0.source = .local
                        $0.notForwarded = true
                        $0.transportCode = "canceled"
                    }
                }
                return await self.dispatchCommandAttempt(eff)
            },
            onTerminalEffect: { [weak self] eff in
                guard let self else { return }
                switch eff.effect {
                case .terminalDirectSend(let e)?: self.handleTerminalDirectSend(e)
                case .terminalOpenSocket(let e)?: self.handleTerminalOpenSocket(e)
                case .terminalCloseSocket(let e)?: self.handleTerminalCloseSocket(e)
                case .terminalDeliverOutput(let e)?: self.handleTerminalDeliverOutput(e)
                case .terminalStateChanged(let e)?: self.handleTerminalStateChanged(e)
                default: break
                }
            },
            onSessionImageLoaded: { [weak self] eff in
                self?.handleSessionImageLoaded(eff)
            },
            relayPipe: true
        )
        fallbackSecurityDriver = driver
        return driver
    }

    public func setSecurityForeground(_ foreground: Bool) async {
        foregroundActive = foreground
        if securityConfiguredInCore {
            await emitSecurityCoreEvent(PBCoreEvent.with {
                $0.securityForeground = .with { $0.foreground = foreground }
            })
        }
    }

    public func applySecurityTokenUpdated(_ eff: PBSecurityTokenUpdatedEffect) {
        if eff.cleared {
            cachedToken = nil
            securityStore.clearLoginToken(server: account.server, deviceId: account.deviceId)
            return
        }
        let saved = SavedLogin(token: eff.token, expiresMs: eff.expiresMs)
        cachedToken = saved
        securityStore.saveLoginToken(saved, server: account.server, deviceId: account.deviceId)
    }

    public func applySecurityStatusSnapshot(_ status: PBSecurityWorkflowStatus) async {
        if status.removed {
            cachedToken = nil
            securityStore.clearLoginToken(server: account.server, deviceId: account.deviceId)
        }
    }

    private func withSecurity<T>(_ op: (SecurityClient) async throws -> T) async throws -> T {
        try await ensureSecurityBootstrapped()
        return try await op(securityClient)
    }

    private func withSecurityStatus(_ op: (SecurityClient) async throws -> PBSecurityWorkflowStatus) async throws -> PBSecurityWorkflowStatus {
        let st = try await withSecurity(op)
        await applySecurityStatusSnapshot(st)
        return st
    }

    private func withSecurityAndRefresh(_ op: (SecurityClient) async throws -> Void) async throws {
        try await withSecurity(op)
        if !closed { await refreshDevices() }
    }

    @discardableResult
    public func syncSecurity(touch: Bool = false) async throws -> PBSecurityWorkflowStatus {
        let tok = cachedToken?.token ?? ""
        return try await withSecurityStatus { try await $0.syncNow(touch: touch, authToken: tok) }
    }

    public func getSecurityStatus() async throws -> PBSecurityWorkflowStatus {
        try await withSecurityStatus { try await $0.getStatus() }
    }

    @discardableResult
    public func renewLeaseBlocking(force: Bool = true) async throws -> PBSecurityWorkflowStatus {
        try await withSecurityStatus { try await $0.renewLease(force: force) }
    }

    public func renewLeaseNow() async {
        do { _ = try await renewLeaseBlocking(force: true) }
        catch { if !(error is CancellationError) { report(error) } }
    }

    public func resumeSecurityOperation() async throws {
        try await withSecurityAndRefresh { _ = try await $0.resumePendingOp() }
    }

    public func discardPendingOp(abandonHandoff: Bool = false) async throws {
        try await withSecurity { try await $0.discardPendingOp(abandonHandoff: abandonHandoff) }
    }

    public func prepareRecoveryRotation() async throws -> String {
        try await withSecurity { try await $0.prepareRecoveryRotation() }
    }

    public func confirmRecoveryRotation() async throws {
        try await withSecurity { try await $0.confirmRecoveryRotation() }
    }

    public func prepareHandoffCode(storagePersisted: Bool = true) async throws -> String {
        try await withSecurity { try await $0.prepareHandoffCode(storagePersisted: storagePersisted) }
    }

    public func publishHandoffOffer() async throws {
        try await withSecurityAndRefresh { try await $0.publishHandoffOffer() }
    }

    public func handoffAuthority(targetDeviceId: String) async throws {
        try await withSecurity { try await $0.handoffAuthority(targetDeviceId: targetDeviceId) }
    }

    public func completeAccountPublicProfile(server: String, code: String) async throws {
        try await withSecurity { try await $0.completeAccountPublicProfile(server: server, code: code) }
    }

    public func finishPairingDelivery(pairingId: String) async throws -> PBSecurityPairingInfo {
        let res = try await withSecurity { try await $0.finishPairingDelivery(pairingId: pairingId) }
        if res.delivered, !closed { await refreshDevices() }
        return res
    }

    public func checkExitAdvice() async throws -> PBSecurityExitAdvice {
        try await withSecurity { try await $0.checkExitAdvice() }
    }

    public func renameDevice(_ id: String, name: String) async throws {
        guard id == account.deviceId else {
            throw CommandError("failed_precondition", L10nMessage.errorsIosOnlyTheDeviceItselfCanChangeItsName())
        }
        try await withSecurityAndRefresh { try await $0.updateDeviceMeta(deviceId: id, name: name.trimmingCharacters(in: .whitespacesAndNewlines)) }
    }

    public func removeDevice(_ id: String) async throws {
        try await withSecurityAndRefresh { try await $0.removeDevice(id) }
    }

    public func deleteMachineRecords(_ id: String) async throws {
        try await withSecurityAndRefresh { try await $0.deleteMachineRecords(id) }
    }

    public func deleteAccount() async throws {
        try await withSecurity { try await $0.deleteAccount() }
    }

    // MARK: - Crypto Envelope Operations

    private func runSecurityOp(_ configure: @Sendable (inout PBSecurityOpRequestEvent) -> Void) async throws -> PBSecurityOpResultEffect {
        try await ensureSecurityBootstrapped()
        let res: PBSecurityOpResultEffect
        if let onOp = hostHooks?.onSecurityOp {
            var req = PBSecurityOpRequestEvent()
            configure(&req)
            res = await onOp(req)
        } else {
            guard let driver = ensureFallbackSecurityDriver() else {
                throw SecurityFailure.storage
            }
            res = try await driver.runSecurityOp(configure)
        }
        if !res.error.isEmpty {
            throw Self.mapOpError(res)
        }
        return res
    }

    static func mapOpError(_ res: PBSecurityOpResultEffect) -> Error {
        switch res.structuredErrorCode {
        case .unsupportedHistory:
            return UnsupportedHistoryError()
        case .recoveryUnavailable:
            return SecurityFailure.unavailable
        case .leaseExpired:
            return SecurityFailure.lease
        case .stale:
            return SecurityFailure.stale
        case .clockSkewed:
            return SecurityFailure.clock
        case .untrusted:
            return SecurityFailure.untrusted
        case .capacity:
            return SecurityFailure.capacity
        case .storage:
            return SecurityFailure.storage
        case .conflict:
            return SecurityFailure.conflict
        default:
            return SecurityFailure.invalid
        }
    }

    func sealEnvelopeOp(
        purpose: SecurityPurpose,
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
            throw SecurityFailure.invalid
        }
        return sealed
    }

    func openEnvelopeOp(
        envelope: PBEnvelope,
        expectedPurpose: SecurityPurpose,
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
            throw SecurityFailure.invalid
        }
        return opened
    }
}
