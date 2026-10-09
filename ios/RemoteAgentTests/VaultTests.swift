import Foundation
import Security
import Testing

@testable import RemoteAgent

/// The Keychain as the vault uses it, on the simulator's real Keychain: each test has a service of its own and removes it.
@Suite struct VaultTests {
    let vault = Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")
    let account = VaultAccount(
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
        #expect(try vault.loginToken() == nil)
    }

    @Test func theAccountComesBackAsItWasSaved() throws {
        defer { try? vault.clear() }
        try vault.save(account, notifyKey: notify)
        #expect(try vault.hasAccount())
        #expect(try vault.load() == account)
        #expect(try vault.notifyKey() == notify)
    }

    @Test func savingAgainReplacesTheAccount() throws {
        defer { try? vault.clear() }
        try vault.save(account, notifyKey: notify)
        var other = account
        other.server = "http://localhost:8080"
        other.root = Data(repeating: 9, count: 32)
        try vault.save(other, notifyKey: Data(repeating: 1, count: 32))
        #expect(try vault.load() == other)
        #expect(try vault.notifyKey() == Data(repeating: 1, count: 32))
    }

    @Test func itemsAreForThisDeviceOnlyWhileUnlockedAndDoNotSync() throws {
        defer { try? vault.clear() }
        try vault.save(account, notifyKey: notify)
        try vault.saveLoginToken(LoginToken(token: "t", expiresAt: Date(timeIntervalSince1970: 1_800_000_000)))
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
        let token = LoginToken(token: "bearer-abc", expiresAt: Date(timeIntervalSince1970: 1_800_000_000))
        try vault.save(account, notifyKey: notify)
        try vault.saveLoginToken(token)
        #expect(try vault.loginToken() == token)
        try vault.deleteLoginToken()
        #expect(try vault.loginToken() == nil)
        #expect(try vault.load() == account)
        // Deleting what is not there is not an error.
        try vault.deleteLoginToken()
    }

    @Test func clearRemovesEveryItemAndOnlyThisVaults() throws {
        let other = Vault(service: vault.service + ".other")
        defer { try? other.clear() }
        try vault.save(account, notifyKey: notify)
        try vault.saveLoginToken(LoginToken(token: "t", expiresAt: .now))
        try vault.write(.pendingKey, Data(repeating: 3, count: 32))
        try other.save(account, notifyKey: notify)
        try vault.clear()
        #expect(try !vault.hasAccount())
        #expect(try vault.load() == nil)
        #expect(try vault.notifyKey() == nil)
        #expect(try vault.loginToken() == nil)
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
        #expect(throws: VaultError.unreadable) { try vault.loginToken() }
    }

    @Test func descriptionsLeaveTheKeysOut() {
        let token = LoginToken(token: "bearer-secret-value", expiresAt: Date(timeIntervalSince1970: 0))
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
