import Foundation
import RAProto
import SwiftProtobuf

extension Remote {
    // MARK: - Core Security Lifecycle & Workflows

    func emitSecurityCoreEvent(_ ev: PBCoreEvent) async {
        guard !closed else { return }
        if let onCore = hostHooks?.onCoreEvent {
            await onCore(ev)
        }
    }

    public func setSecurityForeground(_ foreground: Bool) async {
        foregroundActive = foreground
        await emitSecurityCoreEvent(PBCoreEvent.with {
            $0.securityForeground = .with { $0.foreground = foreground }
        })
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

    private func withSecurityAndRefresh(_ op: (SecurityClient) async throws -> Void) async throws {
        try await op(securityClient)
        if !closed { await refreshDevices() }
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
        try await securityClient.deleteAccount()
    }

    // MARK: - Crypto Envelope Operations

    private func runSecurityOp(_ configure: @Sendable (inout PBSecurityOpRequestEvent) -> Void) async throws -> PBSecurityOpResultEffect {
        guard let onOp = hostHooks?.onSecurityOp else {
            throw SecurityFailure.storage
        }
        var req = PBSecurityOpRequestEvent()
        configure(&req)
        let res = await onOp(req)
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
