// Keeps the account's secrets on this device (architecture §15), the
// counterpart of Android's data/Vault.kt. Android wraps them with a
// Keystore key and stores the result in a file; here the Keychain holds
// them itself, as generic-password items that are readable only while the
// device is unlocked, never leave it (no iCloud Keychain, no backup to
// another device) and are never written anywhere weaker when the Keychain
// refuses: a failure is thrown, with the system's status.
//
// The Keychain protects storage only. Signing and pairing need the raw
// bytes, so they are in memory while the app uses them. Nothing here logs
// or prints key material; the types that carry it describe themselves
// without it.
import Foundation
import Security

/// The account as it is kept on this device: where the server is, the root
/// key everything is derived from, and this device's signing seed and ID.
struct VaultAccount: Equatable, Sendable, CustomStringConvertible, CustomDebugStringConvertible,
    CustomReflectable
{
    var server: String
    /// Empty when this device joined by pairing and has no account root.
    var root: Data
    var deviceSeed: Data
    var deviceId: String
    /// The security domain's account ID. Older entries derive it from root.
    var accountId: String = ""

    var description: String { "VaultAccount(server=\(server), device=\(deviceId))" }
    var debugDescription: String { description }
    /// What `dump` and string interpolation of a containing value may see: not the keys.
    var customMirror: Mirror { Mirror(self, children: ["server": server, "deviceId": deviceId]) }
}

/// The server's bearer token of the last login, kept so a relaunch does
/// not sign in again before it runs out. It is kept with whose it is, the
/// server and this device's ID there, and is only ever given back for
/// exactly those: a token saved for one account is never offered to another.
struct LoginToken: Equatable, Sendable, CustomStringConvertible, CustomDebugStringConvertible,
    CustomReflectable
{
    var token: String
    var expiresAt: Date
    var server: String
    var deviceId: String

    var description: String {
        "LoginToken(server=\(server), device=\(deviceId), expiresAt=\(expiresAt))"
    }
    var debugDescription: String { description }
    var customMirror: Mirror {
        Mirror(self, children: ["server": server, "deviceId": deviceId, "expiresAt": expiresAt])
    }
}

/// The Keychain refused, or what it holds cannot be read. `status` is the system's OSStatus, 0 when the data itself is at fault.
struct VaultError: Error, LocalizedError, Equatable, CustomStringConvertible {
    enum Operation: String, Sendable {
        case read = "读取"
        case save = "保存"
        case delete = "删除"
    }

    let operation: Operation
    let status: OSStatus

    /// Stored bytes that are not what this version writes.
    static let unreadable = VaultError(operation: .read, status: 0)

    /// The device is locked: items here are readable only while it is unlocked.
    var isLocked: Bool { status == errSecInteractionNotAllowed }

    var errorDescription: String? {
        if isLocked { return "设备锁定时不能\(operation.rawValue)账户密钥，解锁后再试" }
        if status == 0 { return "钥匙串里的账户密钥读不出来，需要重新加入账户" }
        let detail = SecCopyErrorMessageString(status, nil).map { $0 as String } ?? "未知错误"
        return "没能\(operation.rawValue)钥匙串里的账户密钥：\(detail)（\(status)）"
    }

    var description: String { "VaultError(\(operation), status=\(status))" }
}

/// The Keychain items of one installation. Calls block on the Keychain; make them off the main actor.
struct Vault: Sendable {
    /// One Keychain item of the vault.
    enum Item: String, Sendable, CaseIterable {
        /// Server, device ID, root key and device seed, in one item so that they are written and read together.
        case account
        case securityState = "security-v3"
        /// The notify key (architecture §9), on its own so that a push can be opened without the root key.
        case notifyKey = "notify-key"
        case loginToken = "login-token"
        /// The key images waiting to be sent are sealed with on disk (`PendingStore`).
        case pendingKey = "pending-key"
    }

    /// The Keychain service the items are under. The debug build's bundle ID differs, so its items are its own.
    let service: String

    init(
        service: String = (Bundle.main.bundleIdentifier ?? "io.github.lewin671.remoteagent")
            + ".vault"
    ) {
        self.service = service
    }

    // MARK: The account

    /// Whether an account is stored, without reading it: true also while the device is locked.
    func hasAccount() throws -> Bool { try exists(.account) }

    /// The stored account, or nil when this device has not joined one.
    func load() throws -> VaultAccount? {
        guard let blob = try read(.account) else { return nil }
        guard
            let plist = try? PropertyListSerialization.propertyList(from: blob, format: nil)
                as? [String: Any],
            let v = plist["v"] as? Int, v == 1 || v == 2,
            let server = plist["server"] as? String, let deviceId = plist["deviceId"] as? String,
            let root = plist["root"] as? Data, let seed = plist["seed"] as? Data
        else { throw VaultError.unreadable }
        let accountId = plist["accountId"] as? String ?? ""
        return VaultAccount(
            server: server, root: root, deviceSeed: seed, deviceId: deviceId, accountId: accountId)
    }

    /// Stores the account with its notify key, replacing any before. The
    /// notify key goes first: an account is then never stored without it.
    func save(_ account: VaultAccount, notifyKey: Data) throws {
        let plist: [String: Any] = [
            "v": 2, "server": account.server, "deviceId": account.deviceId, "root": account.root,
            "seed": account.deviceSeed, "accountId": account.accountId,
        ]
        guard
            let blob = try? PropertyListSerialization.data(
                fromPropertyList: plist, format: .binary, options: 0)
        else { throw VaultError(operation: .save, status: errSecParam) }
        try write(.notifyKey, notifyKey)
        try write(.account, blob)
    }

    /// The notify key, for decrypting pushes.
    ///
    /// It has the account's accessibility for now. A notification extension
    /// that opens pushes while the device is locked will need it readable
    /// after the first unlock, in a shared access group; that is decided
    /// with the extension.
    func notifyKey() throws -> Data? { try read(.notifyKey) }

    // MARK: The login token

    /// The login kept for this device on this server, or nil: when none is
    /// kept, and just the same when the one kept is another account's or
    /// does not say whose it is. Such a token is not an error and not a
    /// login; whoever asks signs in anew, and the next one kept replaces it.
    func loginToken(server: String, deviceId: String) throws -> LoginToken? {
        guard let blob = try read(.loginToken) else { return nil }
        guard
            let plist = try? PropertyListSerialization.propertyList(from: blob, format: nil)
                as? [String: Any],
            let token = plist["token"] as? String, let expires = plist["expiresAt"] as? Date
        else { throw VaultError.unreadable }
        guard let owner = plist["server"] as? String, let device = plist["deviceId"] as? String,
            owner == server, device == deviceId, !deviceId.isEmpty
        else { return nil }
        return LoginToken(token: token, expiresAt: expires, server: owner, deviceId: device)
    }

    func saveLoginToken(_ token: LoginToken) throws {
        let plist: [String: Any] = [
            "token": token.token, "expiresAt": token.expiresAt, "server": token.server,
            "deviceId": token.deviceId,
        ]
        guard
            let blob = try? PropertyListSerialization.data(
                fromPropertyList: plist, format: .binary, options: 0)
        else { throw VaultError(operation: .save, status: errSecParam) }
        try write(.loginToken, blob)
    }

    func deleteLoginToken() throws { try delete(.loginToken) }

    // MARK: Everything

    /// Logging out: removes every item of the vault.
    func clear() throws {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        let status = SecItemDelete(query(nil) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw VaultError(operation: .delete, status: status)
        }
    }

    // MARK: Items

    func read(_ item: Item) throws -> Data? {
        var q = query(item)
        q[kSecReturnData] = true
        q[kSecMatchLimit] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = out as? Data else {
            throw VaultError(operation: .read, status: status)
        }
        return data
    }

    /// Adds the item or replaces its value; either way it ends up readable only while unlocked, on this device only.
    func write(_ item: Item, _ data: Data) throws {
        var add = query(item)
        add[kSecValueData] = data
        add[kSecAttrAccessible] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        var status = SecItemAdd(add as CFDictionary, nil)
        if status == errSecDuplicateItem {
            let change: [CFString: Any] = [
                kSecValueData: data,
                kSecAttrAccessible: kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
            ]
            status = SecItemUpdate(query(item) as CFDictionary, change as CFDictionary)
        }
        guard status == errSecSuccess else { throw VaultError(operation: .save, status: status) }
    }

    func delete(_ item: Item) throws {
        let status = SecItemDelete(query(item) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw VaultError(operation: .delete, status: status)
        }
    }

    func exists(_ item: Item) throws -> Bool {
        var q = query(item)
        q[kSecMatchLimit] = kSecMatchLimitOne
        q[kSecReturnAttributes] = true
        var out: CFTypeRef?
        switch SecItemCopyMatching(q as CFDictionary, &out) {
        case errSecSuccess, errSecInteractionNotAllowed: return true
        case errSecItemNotFound: return false
        case let status: throw VaultError(operation: .read, status: status)
        }
    }

    /// One item, or all of the service's. Only items that do not sync are ever written or matched.
    private func query(_ item: Item?) -> [CFString: Any] {
        var q: [CFString: Any] = [
            kSecClass: kSecClassGenericPassword, kSecAttrService: service,
            kSecAttrSynchronizable: false,
        ]
        if let item { q[kSecAttrAccount] = item.rawValue }
        return q
    }
    func readNamed(_ name: String) throws -> Data? {
        var q = query(nil)
        q[kSecAttrAccount] = name
        q[kSecReturnData] = true
        q[kSecMatchLimit] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = out as? Data else {
            throw VaultError(operation: .read, status: status)
        }
        return data
    }
    func writeNamed(_ name: String, _ data: Data) throws {
        var q = query(nil)
        q[kSecAttrAccount] = name
        var add = q
        add[kSecValueData] = data
        add[kSecAttrAccessible] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        var status = SecItemAdd(add as CFDictionary, nil)
        if status == errSecDuplicateItem {
            status = SecItemUpdate(
                q as CFDictionary,
                [
                    kSecValueData: data,
                    kSecAttrAccessible: kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
                ] as CFDictionary)
        }
        guard status == errSecSuccess else { throw VaultError(operation: .save, status: status) }
    }

}
