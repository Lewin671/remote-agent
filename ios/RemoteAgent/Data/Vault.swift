// Keeps the account's secrets on this device (architecture §15), the
// counterpart of Android's data/Vault.kt. Keychain generic-password items never
// sync or leave this device (`kSecAttrSynchronizable = false`):
// - Security buckets, private keys, login token, upgrade notice, pending image key:
//   WhenUnlockedThisDeviceOnly
// - Notify decryption mirror (`security-v4-notify`): AfterFirstUnlockThisDeviceOnly
//   (no device private keys; deleted on logout / non-member)
// Locked Keychain reads fail cleanly (`errSecInteractionNotAllowed`) without
// deleting or mutating any existing items.
import Foundation
import RACore
import RAProto
import Security

/// The Keychain refused, or what it holds cannot be read. `status` is the system's OSStatus, 0 when the data itself is at fault.
struct VaultError: Error, LocalizedError, Equatable, CustomStringConvertible {
    enum Operation: Sendable {
        case read, save, delete
        var text: String {
            switch self { case .read: L10n.securityIosRead(); case .save: L10n.commonSave(); case .delete: L10n.scheduleDelete() }
        }
    }

    let operation: Operation
    let status: OSStatus

    /// Stored bytes that are not what this version writes.
    static let unreadable = VaultError(operation: .read, status: 0)

    /// The device is locked: items here are readable only while it is unlocked.
    var isLocked: Bool { status == errSecInteractionNotAllowed }

    var errorDescription: String? {
        if isLocked { return L10n.securityIosCannotAccountKeysWhileTheDeviceIsLockedUnlock(value: String(describing: operation.text)) }
        if status == 0 { return L10n.securityIosCannotReadAccountKeysFromKeychainRejoinTheAccount() }
        let detail = SecCopyErrorMessageString(status, nil).map { $0 as String } ?? L10n.securityIosUnknownError()
        return L10n.securityIosCouldNotAccountKeysInKeychain(detail: String(describing: detail), extra: String(describing: status), value: String(describing: operation.text))
    }

    var description: String { "VaultError(\(operation), status=\(status))" }
}

/// Keychain wrapper so tests can inject simulated lock errors (`errSecInteractionNotAllowed`)
/// while production uses OS Keychain directly.
struct KeychainOps: Sendable {
    var copyMatching: @Sendable ([CFString: Any], UnsafeMutablePointer<CFTypeRef?>?) -> OSStatus
    var add: @Sendable ([CFString: Any], UnsafeMutablePointer<CFTypeRef?>?) -> OSStatus
    var update: @Sendable ([CFString: Any], [CFString: Any]) -> OSStatus
    var delete: @Sendable ([CFString: Any]) -> OSStatus

    static let live = KeychainOps(
        copyMatching: { SecItemCopyMatching($0 as CFDictionary, $1) },
        add: { SecItemAdd($0 as CFDictionary, $1) },
        update: { SecItemUpdate($0 as CFDictionary, $1 as CFDictionary) },
        delete: { SecItemDelete($0 as CFDictionary) }
    )
}

/// The Keychain-backed S6 v4 security store of one installation.
struct Vault: SecurityStoreV4Proxy, @unchecked Sendable {
    /// Non-security Keychain items exposed directly to app subsystems (`PendingStore`).
    enum Item: String, Sendable, CaseIterable {
        case pendingKey = "pending-key"
    }

    private enum KeychainAccount {
        static let buckets = "security-v4-buckets"
        static let keys = "security-v4-keys"
        static let notify = "security-v4-notify"
        static let token = "security-v4-token"
        static let upgradeNotice = "security-v4-upgrade-notice"

        static let legacyExact: Set<String> = [
            "account",
            "security-v3",
            "notify-key",
            "login-token",
            "registration-intent",
            "pair-delivery",
            "authority-operation",
        ]
        static let legacyPrefixes = ["account-public-", "pairing-identity-"]
        static let v4SecurityAccounts = [buckets, keys, notify, token]

        static func isLegacy(_ name: String) -> Bool {
            legacyExact.contains(name) || legacyPrefixes.contains(where: { name.hasPrefix($0) })
        }
    }

    let service: String
    private let lock = NSRecursiveLock()
    private let kc: KeychainOps
    let securityCore: SecurityStoreV4Core

    private final class KeychainBackend: SecurityStoreV4Backend, @unchecked Sendable {
        let storage: KeychainStorage
        let onLegacyWipe: (@Sendable () -> Void)?

        init(storage: KeychainStorage, onLegacyWipe: (@Sendable () -> Void)?) {
            self.storage = storage
            self.onLegacyWipe = onLegacyWipe
        }

        func readBuckets() throws -> [SecurityStoreBucketKey: SecurityStoreStoredBucket] {
            try storage.read(account: KeychainAccount.buckets).map(SecurityStoreCodec.decodeBuckets) ?? [:]
        }
        func writeBuckets(_ buckets: [SecurityStoreBucketKey: SecurityStoreStoredBucket]) throws {
            try storage.write(account: KeychainAccount.buckets, data: try SecurityStoreCodec.encodeBuckets(buckets), afterFirstUnlock: false)
        }
        func readKeys() throws -> [SecurityStoreKeyId: SecurityStoreStoredKey] {
            try storage.read(account: KeychainAccount.keys).map(SecurityStoreCodec.decodeKeys) ?? [:]
        }
        func writeKeys(_ keys: [SecurityStoreKeyId: SecurityStoreStoredKey]) throws {
            try storage.write(account: KeychainAccount.keys, data: try SecurityStoreCodec.encodeKeys(keys), afterFirstUnlock: false)
        }
        func readNotify() throws -> Data? { try storage.read(account: KeychainAccount.notify) }
        func writeNotify(_ data: Data) throws { try storage.write(account: KeychainAccount.notify, data: data, afterFirstUnlock: true) }
        func deleteNotify() throws { try storage.delete(account: KeychainAccount.notify) }
        func readLoginTokenRaw() throws -> Data? { try storage.read(account: KeychainAccount.token) }
        func writeLoginTokenRaw(_ data: Data) throws { try storage.write(account: KeychainAccount.token, data: data, afterFirstUnlock: false) }
        func deleteLoginToken() throws { try storage.delete(account: KeychainAccount.token) }
        func hasUpgradeNotice() throws -> Bool { try storage.read(account: KeychainAccount.upgradeNotice) != nil }
        func setUpgradeNotice(_ on: Bool) throws {
            if on { try storage.write(account: KeychainAccount.upgradeNotice, data: Data([1]), afterFirstUnlock: false) }
            else { try storage.delete(account: KeychainAccount.upgradeNotice) }
        }
        func detectAndClearLegacy() throws -> Bool {
            try Vault.detectAndClearLegacyLocked(storage: storage, onLegacyWipe: onLegacyWipe)
        }
        func clearAllBacking() throws {
            for name in KeychainAccount.v4SecurityAccounts + [KeychainAccount.upgradeNotice] {
                try storage.delete(account: name)
            }
            for name in try storage.listAccounts() where KeychainAccount.isLegacy(name) {
                try storage.delete(account: name)
            }
        }
    }

    init(
        service: String = (Bundle.main.bundleIdentifier ?? "io.github.lewin671.remoteagent") + ".vault",
        onLegacyWipe: (@Sendable () -> Void)? = nil,
        keychain: KeychainOps = .live
    ) {
        self.service = service
        self.kc = keychain
        self.securityCore = SecurityStoreV4Core(
            lock: lock,
            backend: KeychainBackend(storage: KeychainStorage(service: service, kc: keychain), onLegacyWipe: onLegacyWipe)
        )
    }

    // MARK: - Startup Legacy Detection & Notify Mirror

    @discardableResult
    func detectAndClearLegacy(onLegacyWipe: (@Sendable () -> Void)? = nil) throws -> Bool {
        try lock.withLock {
            let storage = KeychainStorage(service: service, kc: kc)
            return try Self.detectAndClearLegacyLocked(storage: storage, onLegacyWipe: onLegacyWipe)
        }
    }

    private static func detectAndClearLegacyLocked(
        storage: KeychainStorage,
        onLegacyWipe: (@Sendable () -> Void)?
    ) throws -> Bool {
        let existingBuckets: [SecurityStoreBucketKey: SecurityStoreStoredBucket]
        if let raw = try storage.read(account: KeychainAccount.buckets) {
            existingBuckets = (try? SecurityStoreCodec.decodeBuckets(raw)) ?? [:]
        } else {
            existingBuckets = [:]
        }
        let hasV4State = existingBuckets.values.contains { row in
            !row.data.isEmpty && (
                row.bucket == Int32(PBSecurityStoreBucket.record.rawValue)
                || row.bucket == Int32(PBSecurityStoreBucket.operation.rawValue)
                || row.bucket == Int32(PBSecurityStoreBucket.registrationIntent.rawValue)
            )
        }
        if hasV4State {
            return false
        }

        let accounts = try storage.listAccounts()
        let legacyAccounts = accounts.filter { KeychainAccount.isLegacy($0) }
        if !legacyAccounts.isEmpty {
            // Wipe legacy v3 and stray v4 security items while preserving pending image key (`pending-key`).
            for name in legacyAccounts {
                try storage.delete(account: name)
            }
            for name in KeychainAccount.v4SecurityAccounts {
                try storage.delete(account: name)
            }
            try storage.write(account: KeychainAccount.upgradeNotice, data: Data([1]), afterFirstUnlock: false)
            onLegacyWipe?()
            return true
        }
        return (try? storage.read(account: KeychainAccount.upgradeNotice)) != nil
    }

    func hasUpgradeNotice() -> Bool {
        lock.withLock {
            (try? KeychainStorage(service: service, kc: kc).read(account: KeychainAccount.upgradeNotice)) != nil
        }
    }

    // MARK: - Pending Image Key & Full Service Clear

    func read(_ item: Item) throws -> Data? {
        try lock.withLock {
            try KeychainStorage(service: service, kc: kc).read(account: item.rawValue)
        }
    }

    func write(_ item: Item, _ data: Data) throws {
        try lock.withLock {
            try KeychainStorage(service: service, kc: kc).write(account: item.rawValue, data: data, afterFirstUnlock: false)
        }
    }

    func delete(_ item: Item) throws {
        try lock.withLock {
            try KeychainStorage(service: service, kc: kc).delete(account: item.rawValue)
        }
    }

    /// Removing the entire installation's Keychain service on explicit logout.
    func clear() throws {
        try lock.withLock {
            let status = kc.delete([
                kSecClass: kSecClassGenericPassword,
                kSecAttrService: service,
                kSecAttrSynchronizable: false,
            ])
            guard status == errSecSuccess || status == errSecItemNotFound else {
                throw VaultError(operation: .delete, status: status)
            }
        }
        securityCore.clearMemoryCache()
    }

    // MARK: - Test Helpers

    #if DEBUG
    func writeRawKeychainItemForTest(account: String, data: Data, afterFirstUnlock: Bool = false) throws {
        try lock.withLock {
            try KeychainStorage(service: service, kc: kc).write(account: account, data: data, afterFirstUnlock: afterFirstUnlock)
        }
    }

    func readRawKeychainItemForTest(account: String) throws -> Data? {
        try lock.withLock {
            try KeychainStorage(service: service, kc: kc).read(account: account)
        }
    }

    func readStoredBucketsForTest() -> [SecurityStoreBucketKey: SecurityStoreStoredBucket] {
        lock.withLock {
            guard let raw = try? KeychainStorage(service: service, kc: kc).read(account: KeychainAccount.buckets) else {
                return [:]
            }
            return (try? SecurityStoreCodec.decodeBuckets(raw)) ?? [:]
        }
    }
    #endif
}

private struct KeychainStorage: Sendable {
    let service: String
    let kc: KeychainOps

    private func baseQuery(account: String?) -> [CFString: Any] {
        var q: [CFString: Any] = [
            kSecClass: kSecClassGenericPassword,
            kSecAttrService: service,
            kSecAttrSynchronizable: false,
        ]
        if let account { q[kSecAttrAccount] = account }
        return q
    }

    func read(account: String) throws -> Data? {
        var q = baseQuery(account: account)
        q[kSecReturnData] = true
        q[kSecMatchLimit] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = kc.copyMatching(q, &out)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = out as? Data else {
            throw VaultError(operation: .read, status: status)
        }
        return data
    }

    func write(account: String, data: Data, afterFirstUnlock: Bool) throws {
        let accessible: CFString = afterFirstUnlock
            ? kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            : kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        var add = baseQuery(account: account)
        add[kSecValueData] = data
        add[kSecAttrAccessible] = accessible
        var status = kc.add(add, nil)
        if status == errSecDuplicateItem {
            let change: [CFString: Any] = [
                kSecValueData: data,
                kSecAttrAccessible: accessible,
            ]
            status = kc.update(baseQuery(account: account), change)
        }
        guard status == errSecSuccess else {
            throw VaultError(operation: .save, status: status)
        }
    }

    func delete(account: String) throws {
        let status = kc.delete(baseQuery(account: account))
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw VaultError(operation: .delete, status: status)
        }
    }

    func listAccounts() throws -> [String] {
        var q = baseQuery(account: nil)
        q[kSecReturnAttributes] = true
        q[kSecMatchLimit] = kSecMatchLimitAll
        var out: CFTypeRef?
        let status = kc.copyMatching(q, &out)
        if status == errSecItemNotFound { return [] }
        guard status == errSecSuccess, let rows = out as? [[String: Any]] else {
            throw VaultError(operation: .read, status: status)
        }
        return rows.compactMap { $0[kSecAttrAccount as String] as? String }
    }
}
