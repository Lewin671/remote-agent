import Foundation
import RACore
import RAProto
import Security
import Testing
import os

@testable import RemoteAgent

/// The Keychain as the vault uses it, on the simulator's real Keychain: each test has a service of its own and removes it.
@Suite struct VaultTests {
    let vault = Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")
    let server = "https://ra.example.com"
    let devId = "00112233445566778899aabbccddeeff"
    let notify = Data((0..<32).map { UInt8($0 &* 7) })

    private func attributes(_ accountName: String, service: String? = nil) -> [String: Any]? {
        let q: [CFString: Any] = [
            kSecClass: kSecClassGenericPassword,
            kSecAttrService: service ?? vault.service,
            kSecAttrAccount: accountName,
            kSecAttrSynchronizable: kSecAttrSynchronizableAny,
            kSecReturnAttributes: true,
            kSecMatchLimit: kSecMatchLimitOne,
        ]
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess else { return nil }
        return out as? [String: Any]
    }

    @Test func nothingIsStoredAtFirst() throws {
        #expect(!vault.hasPersistedState())
        #expect(!vault.hasUpgradeNotice())
        #expect(vault.readNotifyBlob() == nil)
        #expect(vault.loadLoginToken(server: server, deviceId: devId) == nil)
        #expect(try !vault.detectAndClearLegacy())
        let boot = vault.executeStorage(.with { $0.op = .readBootstrap })
        #expect(boot.error.isEmpty && !boot.legacyCleared && boot.entries.isEmpty && !boot.hasDeviceEd_p && !boot.hasDeviceX_p)
    }

    @Test func bucketsKeysAndNotifyMirrorFollowAccessibilityAndCasContract() throws {
        defer { try? vault.clear() }
        let edPub = try vault.generateKey(kind: .deviceEd, slot: "", persist: true)
        let xPub = try vault.generateKey(kind: .deviceX, slot: "", persist: true)
        #expect(edPub.count == 32 && xPub.count == 32)

        let msg = Data("hello-v4".utf8)
        let sig = try vault.signKey(kind: .deviceEd, slot: "", message: msg)
        #expect(sig.count == 64)

        let peer = try vault.generateKey(kind: .ephemeralX, slot: "ephem-1", persist: false)
        let agreed = try vault.agreeKey(kind: .deviceX, slot: "", peerPublic: peer)
        #expect(agreed.count == 32)
        vault.discardKey(kind: .ephemeralX, slot: "ephem-1")
        #expect(throws: (any Error).self) {
            try vault.agreeKey(kind: .ephemeralX, slot: "ephem-1", peerPublic: xPub)
        }

        let write1 = vault.executeStorage(.with {
            $0.op = .writeCas
            $0.bucket = .record
            $0.key = "record"
            $0.createOnly = true
            $0.nextRevision = "r1"
            $0.data = Data("rec-v1".utf8)
        })
        #expect(write1.error.isEmpty && !write1.conflict && write1.hasDeviceEd_p && write1.hasDeviceX_p)
        #expect(vault.hasPersistedState())

        // CAS mismatch fails cleanly without overwriting.
        let conflict = vault.executeStorage(.with {
            $0.op = .writeCas
            $0.bucket = .record
            $0.key = "record"
            $0.expectedRevision = "stale-rev"
            $0.nextRevision = "r2"
            $0.data = Data("rec-v2".utf8)
        })
        #expect(conflict.conflict)

        let batch = vault.executeStorage(.with {
            $0.op = .batchCommit
            $0.batch = [
                .with {
                    $0.bucket = .notifyRecord
                    $0.key = "notify"
                    $0.nextRevision = "n1"
                    $0.data = notify
                },
            ]
        })
        #expect(batch.error.isEmpty && !batch.conflict)
        #expect(vault.readNotifyBlob() == notify)

        let tok = SavedLogin(token: "tok-1", expiresMs: 1_800_000_000_000)
        vault.saveLoginToken(tok, server: server, deviceId: devId)
        try vault.write(.pendingKey, Data(repeating: 4, count: 32))

        // Notify mirror must use AfterFirstUnlockThisDeviceOnly; keys/buckets/token/pendingKey use WhenUnlockedThisDeviceOnly.
        let notifyAttr = try #require(attributes("security-v4-notify"))
        #expect(notifyAttr[kSecAttrAccessible as String] as? String == kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly as String)
        #expect((notifyAttr[kSecAttrSynchronizable as String] as? NSNumber)?.boolValue != true)

        for name in ["security-v4-buckets", "security-v4-keys", "security-v4-token", "pending-key"] {
            let a = try #require(attributes(name))
            #expect(a[kSecAttrAccessible as String] as? String == kSecAttrAccessibleWhenUnlockedThisDeviceOnly as String)
            #expect((a[kSecAttrSynchronizable as String] as? NSNumber)?.boolValue != true)
        }

        // Cold Vault instance reads persisted state accurately.
        let reopened = Vault(service: vault.service)
        #expect(reopened.hasPersistedState())
        #expect(reopened.readNotifyBlob() == notify)
        #expect(reopened.loadLoginToken(server: server, deviceId: devId) == tok)
        #expect(reopened.loadLoginToken(server: server, deviceId: "other-device") == nil)
        #expect(reopened.loadLoginToken(server: "https://other.example.com", deviceId: devId) == nil)
    }

    @Test func clearRemovesEveryItemAndOnlyThisVaults() throws {
        let other = Vault(service: vault.service + ".other")
        defer { try? other.clear() }
        _ = vault.executeStorage(.with {
            $0.op = .writeCas
            $0.bucket = .record
            $0.key = "record"
            $0.createOnly = true
            $0.nextRevision = "r1"
            $0.data = Data("rec".utf8)
        })
        vault.saveLoginToken(SavedLogin(token: "t", expiresMs: nowMs()), server: server, deviceId: devId)
        try vault.write(.pendingKey, Data(repeating: 3, count: 32))
        _ = other.executeStorage(.with {
            $0.op = .writeCas
            $0.bucket = .record
            $0.key = "record"
            $0.createOnly = true
            $0.nextRevision = "r1"
            $0.data = Data("other".utf8)
        })

        try vault.clear()
        #expect(!vault.hasPersistedState())
        #expect(vault.readNotifyBlob() == nil)
        #expect(vault.loadLoginToken(server: server, deviceId: devId) == nil)
        #expect(try vault.read(.pendingKey) == nil)
        #expect(other.hasPersistedState())
        try vault.clear()
    }

    @Test func legacyV3ItemsTriggerWipePreservePendingKeyAndPersistNoticeAcrossColdRestart() throws {
        defer { try? vault.clear() }
        let wipedCount = OSAllocatedUnfairLock(initialState: 0)
        let pendingSecret = Data((0..<32).map { UInt8($0 + 10) })
        try vault.write(.pendingKey, pendingSecret)

        // Seed v3 Keychain items + stray pre-migration v4 token.
        let legacyNames = [
            "account", "security-v3", "notify-key", "login-token",
            "registration-intent", "pair-delivery", "authority-operation",
            "account-public-abc", "pairing-identity-xyz",
        ]
        for name in legacyNames {
            try vault.writeRawKeychainItemForTest(account: name, data: Data("legacy-\(name)".utf8))
        }
        try vault.writeRawKeychainItemForTest(account: "security-v4-token", data: Data("stray-token".utf8))

        let wiped = try vault.detectAndClearLegacy { wipedCount.withLock { $0 += 1 } }
        #expect(wiped)
        #expect(wipedCount.withLock { $0 } == 1)
        #expect(try vault.read(.pendingKey) == pendingSecret)
        for name in legacyNames + ["security-v4-token", "security-v4-notify"] {
            #expect(try vault.readRawKeychainItemForTest(account: name) == nil)
        }
        #expect(vault.hasUpgradeNotice())
        let noticeAttr = try #require(attributes("security-v4-upgrade-notice"))
        #expect(noticeAttr[kSecAttrAccessible as String] as? String == kSecAttrAccessibleWhenUnlockedThisDeviceOnly as String)

        // Cold restart still sees the upgrade notice until dismissed or re-paired.
        let restarted = Vault(service: vault.service)
        #expect(restarted.hasUpgradeNotice())
        #expect(try restarted.detectAndClearLegacy())
        let boot = restarted.executeStorage(.with { $0.op = .readBootstrap })
        #expect(boot.legacyCleared)
        #expect(try restarted.read(.pendingKey) == pendingSecret)

        restarted.dismissLegacyRepairNotice()
        #expect(!restarted.hasUpgradeNotice())
        #expect(try !restarted.detectAndClearLegacy())
    }

    @Test func writingV4RecordClearsUpgradeNotice() throws {
        defer { try? vault.clear() }
        try vault.writeRawKeychainItemForTest(account: "account", data: Data("v3".utf8))
        #expect(try vault.detectAndClearLegacy())
        #expect(vault.hasUpgradeNotice())

        _ = vault.executeStorage(.with {
            $0.op = .writeCas
            $0.bucket = .record
            $0.key = "record"
            $0.createOnly = true
            $0.nextRevision = "r1"
            $0.data = Data("repaired".utf8)
        })
        #expect(!vault.hasUpgradeNotice())
    }

    @Test func lockedKeychainFailsClosedWithoutMutatingOrDeletingItems() throws {
        let locked = Vault(
            service: vault.service,
            keychain: KeychainOps(
                copyMatching: { _, _ in errSecInteractionNotAllowed },
                add: { _, _ in errSecInteractionNotAllowed },
                update: { _, _ in errSecInteractionNotAllowed },
                delete: { _ in
                    Issue.record("locked Keychain must never delete items")
                    return errSecInteractionNotAllowed
                }
            )
        )
        #expect(throws: VaultError(operation: .read, status: errSecInteractionNotAllowed)) {
            _ = try locked.detectAndClearLegacy()
        }
        #expect(locked.loadLoginToken(server: server, deviceId: devId) == nil)
        let boot = locked.executeStorage(.with { $0.op = .readBootstrap })
        #expect(!boot.error.isEmpty)
    }

    @Test func errorsSayWhatFailedInWords() {
        let locked = VaultError(operation: .read, status: errSecInteractionNotAllowed)
        #expect(locked.isLocked)
        #expect(locked.localizedDescription.contains("解锁"))
        #expect(VaultError.unreadable.localizedDescription.contains(L10n.securityRejoin()))
        #expect(VaultError(operation: .save, status: errSecParam).localizedDescription.contains(L10n.commonSave()))
    }
}
