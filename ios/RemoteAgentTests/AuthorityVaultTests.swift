import Foundation
import RACore
import RAProto
import Security
import Testing
import UIKit

@testable import RemoteAgent

@Suite struct AuthorityVaultTests {
    let vault = Vault(service: "io.github.lewin671.remoteagent.authority-test.\(UUID().uuidString)")



    @MainActor @Test func inactiveAndProtectedDataLossRevokeSigningWithoutDroppingInactiveStreamPolicy() throws {
        defer { try? vault.clear() }
        var parts = AppModel.Parts()
        parts.storage = AccountStorage(vault: vault, cacheRoot: FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString), pending: PendingStore(vault: vault))
        let model = AppModel(settings: SettingsStore(), parts: parts)
        model.scenePhase(.active)
        let ticket = try model.bootstrapGate.ticket()
        model.scenePhase(.inactive)
        #expect(model.foreground)
        #expect(throws: DeviceSecurity.Failure.self) { try model.bootstrapGate.check(ticket) }
        model.scenePhase(.active)
        let resigning = try model.bootstrapGate.ticket()
        NotificationCenter.default.post(name: UIApplication.willResignActiveNotification, object: nil)
        #expect(throws: DeviceSecurity.Failure.self) { try model.bootstrapGate.check(resigning) }
        model.scenePhase(.active)
        let next = try model.bootstrapGate.ticket()
        NotificationCenter.default.post(name: UIApplication.protectedDataWillBecomeUnavailableNotification, object: nil)
        #expect(throws: DeviceSecurity.Failure.self) { try model.bootstrapGate.check(next) }
        model.scenePhase(.background)
    }

}
