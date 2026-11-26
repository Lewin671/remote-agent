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
}
