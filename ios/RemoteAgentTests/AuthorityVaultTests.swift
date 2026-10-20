import Foundation
import RACore
import RAProto
import Security
import Testing
import UIKit

@testable import RemoteAgent

@Suite struct AuthorityVaultTests {
    let vault = Vault(service: "io.github.lewin671.remoteagent.authority-test.\(UUID().uuidString)")

    @Test func independentPendingItemIsUnlockedLocalAndCasPreservesMonotonicProgress() async throws {
        defer { try? vault.clear() }
        let gate = AuthorityGate(); gate.set(active: true, protectedDataAvailable: true)
        let bootstrap = AuthorityBootstrap(storage: vault, gate: gate)
        let code = try await bootstrap.prepareCreation(server: "https://relay.example.com")
        let pending = try #require(try vault.readOperation())
        #expect(pending.kind == .creation && !pending.codeSaved)
        #expect(try vault.load() == nil)
        #expect(try vault.read(.securityState) == nil)
        let attributes: [CFString: Any] = [kSecClass: kSecClassGenericPassword,
            kSecAttrService: vault.service, kSecAttrAccount: "authority-operation",
            kSecReturnAttributes: true, kSecAttrSynchronizable: false]
        var out: CFTypeRef?
        #expect(SecItemCopyMatching(attributes as CFDictionary, &out) == errSecSuccess)
        let actual = try #require(out as? [CFString: Any])
        #expect(actual[kSecAttrAccessible] as? String == kSecAttrAccessibleWhenUnlockedThisDeviceOnly as String)
        #expect(actual[kSecAttrSynchronizable] as? Bool == false)
        let bytes = try #require(try vault.readNamed("authority-operation"))
        #expect(!bytes.contains(Data(code.utf8)))
        #expect(!bytes.contains(RecoveryCode.decode(code)!.seed))
        try await bootstrap.confirmRecoveryCodeSaved()
        let advanced = try #require(try vault.readOperation())
        #expect(advanced.codeSaved)
        #expect(throws: DeviceSecurity.Failure.self) { try vault.replaceOperation(expected: pending, next: nil) }
        #expect(throws: DeviceSecurity.Failure.self) { try vault.replaceOperation(expected: advanced, next: pending) }
        try vault.replaceOperation(expected: advanced, next: nil)
        #expect(try vault.readOperation() == nil)
    }

    @Test func erasedPendingCannotRestoreAccountAndCorruptionDoesNotCreateANewDraft() async throws {
        defer { try? vault.clear() }
        let gate = AuthorityGate(); gate.set(active: true, protectedDataAvailable: true)
        let bootstrap = AuthorityBootstrap(storage: vault, gate: gate)
        _ = try await bootstrap.prepareCreation(server: "https://relay.example.com")
        let expected = try #require(try vault.readOperation())
        try vault.clear()
        #expect(throws: DeviceSecurity.Failure.self) { try vault.replaceOperation(expected: expected, next: expected) }
        try vault.writeNamed("authority-operation", Data([0]))
        do { _ = try await bootstrap.prepareCreation(server: "https://relay.example.com"); Issue.record("overwrote corrupt receipt") } catch {}
        #expect(try vault.readNamed("authority-operation") == Data([0]))
    }

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
