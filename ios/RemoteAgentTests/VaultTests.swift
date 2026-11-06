import Foundation
import RACore
import Security
import Testing

@testable import RemoteAgent

/// The Keychain as the vault uses it, on the simulator's real Keychain: each test has a service of its own and removes it.
@Suite struct VaultTests {
    let vault = Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")
    let account = Account(
        server: "https://ra.example.com", root: Data((0..<32).map { UInt8($0) }),
        deviceSeed: Data((0..<32).map { UInt8(200 - $0) }), deviceId: "00112233445566778899aabbccddeeff")
    let notify = Data((0..<32).map { UInt8($0 &* 7) })

    private func attributes(_ item: Vault.Item) -> [String: Any]? {
        let q: [CFString: Any] = [
            kSecClass: kSecClassGenericPassword, kSecAttrService: vault.service, kSecAttrAccount: item.rawValue,
            kSecAttrSynchronizable: kSecAttrSynchronizableAny, kSecReturnAttributes: true, kSecMatchLimit: kSecMatchLimitOne,
        ]
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess else { return nil }
        return out as? [String: Any]
    }

    @Test func nothingIsStoredAtFirst() throws {
        #expect(try !vault.hasAccount())
        #expect(try vault.load() == nil)
        #expect(try vault.notifyKey() == nil)
        #expect(try vault.loginToken(server: account.server, deviceId: account.deviceId) == nil)
    }

    @Test func theAccountComesBackAsItWasSaved() throws {
        defer { try? vault.clear() }
        try vault.save(account, notifyKey: notify)
        #expect(try vault.hasAccount())
        #expect(try vault.load() == account)
        #expect(try vault.notifyKey() == notify)
    }

    /// Binary plists produced by the pre-consolidation writer, with synthetic material.
    @Test func currentAccountAndLoginPlistsKeepTheirFields() throws {
        defer { try? vault.clear() }
        let accountBytes = Data(base64Encoded: "YnBsaXN0MDDWAQIDBAUGBwgJCgsMVnNlcnZlclF2VHNlZWRYZGV2aWNlSWRUcm9vdFlhY2NvdW50SWRfEBlodHRwczovL3JlbGF5LmV4YW1wbGUuY29tEAJDBAUGXmZpeHR1cmUtZGV2aWNlQwECA18QD2ZpeHR1cmUtYWNjb3VudAgVHB4jLDE7V1ldbHAAAAAAAAABAQAAAAAAAAANAAAAAAAAAAAAAAAAAAAAgg==")!
        let loginBytes = Data(base64Encoded: "YnBsaXN0MDDUAQIDBAUGBwhZZXhwaXJlc0F0VXRva2VuVnNlcnZlclhkZXZpY2VJZDNByH0EwAAAAF1maXh0dXJlLXRva2VuXxAZaHR0cHM6Ly9yZWxheS5leGFtcGxlLmNvbV5maXh0dXJlLWRldmljZQgRGyEoMTpIZAAAAAAAAAEBAAAAAAAAAAkAAAAAAAAAAAAAAAAAAABz")!
        try vault.write(.account, accountBytes)
        try vault.write(.loginToken, loginBytes)
        let loaded = try #require(try vault.load())
        #expect(loaded == Account(server: "https://relay.example.com", root: Data([1, 2, 3]),
            deviceSeed: Data([4, 5, 6]), deviceId: "fixture-device", accountId: "fixture-account"))
        let login = try #require(try vault.loginToken(server: loaded.server, deviceId: loaded.deviceId))
        #expect(login == SavedLogin(token: "fixture-token", expiresMs: 1_800_000_000_000))
        try vault.save(loaded, notifyKey: notify)
        try vault.saveLoginToken(login, server: loaded.server, deviceId: loaded.deviceId)
        // Foundation can reorder binary-plist dictionary keys between processes.
        // Compare every typed value, including raw key bytes and the Date representation.
        for (item, bytes) in [(Vault.Item.account, accountBytes), (.loginToken, loginBytes)] {
            let written = try #require(try vault.read(item))
            let before = try PropertyListSerialization.propertyList(from: bytes, format: nil) as! NSDictionary
            let after = try PropertyListSerialization.propertyList(from: written, format: nil) as! NSDictionary
            #expect(before == after)
        }
    }

    @Test func storedRootAccountStillResolvesItsDomainIdOnlyForUse() throws {
        defer { try? vault.clear() }
        try vault.save(account, notifyKey: notify)
        let loaded = try #require(try vault.load())
        #expect(loaded.accountId.isEmpty)
        let expected = try deviceId(publicKey: Keys.derive(root: account.root).account.publicKey)
        #expect(loaded.resolvingAccountId().accountId == expected)
        let named = Account(server: loaded.server, root: loaded.root, deviceSeed: loaded.deviceSeed,
            deviceId: loaded.deviceId, accountId: "explicit-account")
        #expect(named.resolvingAccountId() == named)
    }

    @Test func savingAgainReplacesTheAccount() throws {
        defer { try? vault.clear() }
        try vault.save(account, notifyKey: notify)
        let other = Account(server: "http://localhost:8080", root: Data(repeating: 9, count: 32),
            deviceSeed: account.deviceSeed, deviceId: account.deviceId)
        try vault.save(other, notifyKey: Data(repeating: 1, count: 32))
        #expect(try vault.load() == other)
        #expect(try vault.notifyKey() == Data(repeating: 1, count: 32))
    }

    @Test func itemsAreForThisDeviceOnlyWhileUnlockedAndDoNotSync() throws {
        defer { try? vault.clear() }
        try vault.save(account, notifyKey: notify)
        try vault.saveLoginToken(SavedLogin(
            token: "t", expiresMs: 1_800_000_000_000), server: account.server, deviceId: account.deviceId)
        for item in [Vault.Item.account, .notifyKey, .loginToken] {
            let a = try #require(attributes(item))
            #expect(a[kSecAttrAccessible as String] as? String == kSecAttrAccessibleWhenUnlockedThisDeviceOnly as String)
            #expect((a[kSecAttrSynchronizable as String] as? NSNumber)?.boolValue != true)
        }
        // Replacing a value keeps the item's protection.
        try vault.save(account, notifyKey: notify)
        let again = try #require(attributes(.account))
        #expect(again[kSecAttrAccessible as String] as? String == kSecAttrAccessibleWhenUnlockedThisDeviceOnly as String)
    }

    @Test func theLoginTokenIsKeptAndDeletedOnItsOwn() throws {
        defer { try? vault.clear() }
        let token = SavedLogin(token: "bearer-abc", expiresMs: 1_800_000_000_000)
        try vault.save(account, notifyKey: notify)
        try vault.saveLoginToken(token, server: account.server, deviceId: account.deviceId)
        #expect(try vault.loginToken(server: account.server, deviceId: account.deviceId) == token)
        try vault.deleteLoginToken()
        #expect(try vault.loginToken(server: account.server, deviceId: account.deviceId) == nil)
        #expect(try vault.load() == account)
        // Deleting what is not there is not an error.
        try vault.deleteLoginToken()
    }

    @Test func clearRemovesEveryItemAndOnlyThisVaults() throws {
        let other = Vault(service: vault.service + ".other")
        defer { try? other.clear() }
        try vault.save(account, notifyKey: notify)
        try vault.saveLoginToken(SavedLogin(token: "t", expiresMs: nowMs()), server: account.server, deviceId: account.deviceId)
        try vault.write(.pendingKey, Data(repeating: 3, count: 32))
        try other.save(account, notifyKey: notify)
        try vault.clear()
        #expect(try !vault.hasAccount())
        #expect(try vault.load() == nil)
        #expect(try vault.notifyKey() == nil)
        #expect(try vault.loginToken(server: account.server, deviceId: account.deviceId) == nil)
        #expect(try vault.read(.pendingKey) == nil)
        #expect(try other.load() == account)
        // Clearing an empty vault is not an error.
        try vault.clear()
    }

    @Test func bytesThatAreNotAnAccountAreAnErrorNotAMissingAccount() throws {
        defer { try? vault.clear() }
        try vault.write(.account, Data("not a property list".utf8))
        #expect(try vault.hasAccount())
        #expect(throws: VaultError.unreadable) { try vault.load() }
        try vault.write(.loginToken, Data([1, 2, 3]))
        #expect(throws: VaultError.unreadable) { try vault.loginToken(server: account.server, deviceId: account.deviceId) }
    }

    // MARK: Whose login it is

    @Test func aLoginKeptForOneAccountIsNeverGivenToAnother() throws {
        defer { try? vault.clear() }
        let token = SavedLogin(
            token: "bearer-abc", expiresMs: 1_800_000_000_000)
        try vault.saveLoginToken(token, server: account.server, deviceId: account.deviceId)
        #expect(try vault.loginToken(server: account.server, deviceId: account.deviceId) == token)
        // Another device of the same server, the same device ID on another server, and neither: none of them gets it.
        #expect(try vault.loginToken(server: account.server, deviceId: "another-device") == nil)
        #expect(try vault.loginToken(server: "https://other.example.com", deviceId: account.deviceId) == nil)
        #expect(try vault.loginToken(server: "https://other.example.com", deviceId: "another-device") == nil)
        // Close is not the same: the server is compared as it is written, and no device ID matches nothing.
        #expect(try vault.loginToken(server: account.server + "/", deviceId: account.deviceId) == nil)
        #expect(try vault.loginToken(server: account.server, deviceId: "") == nil)
        // It is still kept for its own.
        #expect(try vault.loginToken(server: account.server, deviceId: account.deviceId) == token)
    }

    @Test func aLoginThatDoesNotSayWhoseItIsIsNone() throws {
        defer { try? vault.clear() }
        let plist: [String: Any] = ["token": "bearer-abc", "expiresAt": Date(timeIntervalSince1970: 1_800_000_000)]
        try vault.write(.loginToken, PropertyListSerialization.data(fromPropertyList: plist, format: .binary, options: 0))
        #expect(try vault.loginToken(server: account.server, deviceId: account.deviceId) == nil)
    }

    @Test func theKeeperOnlyHandsOverItsOwnAccountsLogin() {
        defer { try? vault.clear() }
        let mine = LoginKeeper(vault: vault, server: account.server, deviceId: account.deviceId)
        mine.keep(SavedLogin(token: "bearer-mine", expiresMs: 1_800_000_000_000))
        // `load` runs after what was asked before it.
        #expect(mine.load()?.token == "bearer-mine")
        // The account on this device changed without the vault being cleared between: the old login is not the new one's.
        let other = LoginKeeper(vault: vault, server: account.server, deviceId: "another-device")
        #expect(other.load() == nil)
        // Its own replaces the one kept, which is then nobody else's either.
        other.keep(SavedLogin(token: "bearer-other", expiresMs: 1_800_000_000_000))
        #expect(other.load()?.token == "bearer-other")
        #expect(mine.load() == nil)
        // A keeper that was closed neither reads nor writes.
        other.close()
        other.keep(nil)
        #expect(other.load() == nil)
        #expect(LoginKeeper(vault: vault, server: account.server, deviceId: "another-device").load()?.token == "bearer-other")
    }

    @Test func descriptionsLeaveTheKeysOut() {
        let token = SavedLogin(token: "bearer-secret-value", expiresMs: 0)
        var dumped = ""
        dump(account, to: &dumped)
        dump(token, to: &dumped)
        let texts = [String(describing: account), String(reflecting: account), "\(account)", "\([account])", dumped,
                     String(describing: token), String(reflecting: token)]
        for text in texts {
            #expect(!text.contains(account.root.hex))
            #expect(!text.contains(account.deviceSeed.hex))
            #expect(!text.contains(account.root.base64EncodedString()))
            #expect(!text.contains("32 bytes"))
            #expect(!text.contains("bearer-secret-value"))
        }
        #expect(String(describing: account).contains("ra.example.com"))
    }

    @Test func errorsSayWhatFailedInWords() {
        let locked = VaultError(operation: .read, status: errSecInteractionNotAllowed)
        #expect(locked.isLocked)
        #expect(locked.localizedDescription.contains("解锁"))
        #expect(VaultError.unreadable.localizedDescription.contains("重新加入"))
        #expect(VaultError(operation: .save, status: errSecParam).localizedDescription.contains("保存"))
    }
}
