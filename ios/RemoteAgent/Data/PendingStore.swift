// Images waiting to be sent, the counterpart of Android's
// data/PendingStore.kt: kept encrypted on disk so that the system
// reclaiming the app, or the picker's grant running out, does not lose
// them. A random key seals them with AES-256-GCM in process; the Keychain
// keeps that key (`Vault.Item.pendingKey`). Nothing here goes into backups,
// and logging out deletes it all.
//
// Text drafts are not here, as on Android: they live in memory for the run.
import CryptoKit
import Foundation
import RACore

/// Named files in folders, sealed at rest: what image drafts and the outbox are kept in.
protocol SealedFiles: Sendable {
    func put(folder: String, name: String, data: Data) throws
    func get(folder: String, name: String) -> Data?
    func remove(folder: String, name: String)
    func clearFolder(_ folder: String)
}

/// File-only rules of the pending images, as Android's core/ImageDiskPolicy.kt.
enum PendingPolicy {
    /// Drafts are user work: they are never evicted, and storing more is refused at this size.
    static let draftLimit: Int64 = 256 * 1024 * 1024
    /// A draft folder nothing touched for this long expires.
    static let draftTTL: TimeInterval = 7 * 24 * 60 * 60
}

final class PendingStore: SealedFiles, @unchecked Sendable {
    private let directory: URL
    private let vault: Vault
    private let defaults: UserDefaults
    private let limit: Int64
    private let now: @Sendable () -> Date
    private let lock = NSRecursiveLock()
    /// Read or made at first use; forgotten with everything else on logout,
    /// so the next account's images are sealed under a key that is kept.
    private var cachedKey: SymmetricKey?
    /// Counts the accounts this store has served: `clearAll` ends one. A
    /// `PendingScope` made before it writes and reads nothing after.
    private var epoch = 0
    private static let expiredKey = "pending.expired-drafts"

    static var defaultDirectory: URL {
        URL.applicationSupportDirectory.appending(path: "RemoteAgent/pending-images", directoryHint: .isDirectory)
    }

    init(
        directory: URL = PendingStore.defaultDirectory, vault: Vault = Vault(), defaults: UserDefaults = .standard,
        limit: Int64 = PendingPolicy.draftLimit, now: @escaping @Sendable () -> Date = { Date() }
    ) {
        self.directory = directory
        self.vault = vault
        self.defaults = defaults
        self.limit = limit
        self.now = now
    }

    /// Bytes the pending images take on disk.
    var used: Int64 { lock.withLock { bytes(of: directory) } }

    /// How many images expired unsent since the count was last cleared, for telling the user once.
    var expired: Int { defaults.integer(forKey: Self.expiredKey) }

    func clearExpiredCount() { defaults.removeObject(forKey: Self.expiredKey) }

    /// Removes draft folders nothing touched for `PendingPolicy.draftTTL`, except those the outbox names; returns how many images went.
    @discardableResult
    func expire() -> Int {
        lock.withLock {
            var removed = 0
            for folder in folders() where !FileManager.default.fileExists(atPath: folder.appending(path: ".keep").path) {
                let files = (try? FileManager.default.contentsOfDirectory(at: folder, includingPropertiesForKeys: [.contentModificationDateKey])) ?? []
                let last = ([folder] + files).compactMap { modified($0) }.max() ?? .distantPast
                if now().timeIntervalSince(last) < PendingPolicy.draftTTL { continue }
                let count = files.filter { $0.pathExtension == "meta" }.count
                if (try? FileManager.default.removeItem(at: folder)) != nil { removed += count }
            }
            if removed > 0 { defaults.set(expired + removed, forKey: Self.expiredKey) }
            return removed
        }
    }

    /// The store as one account's writers see it: once that account is
    /// logged out (`clearAll`) the scope stores nothing and finds nothing, so
    /// work that was on its way cannot leave images for the next account.
    func scoped() -> PendingScope {
        PendingScope(store: self, epoch: lock.withLock { epoch })
    }

    /// Runs `body` under the lock if `scope` is still the account in use; nil when it is not.
    fileprivate func current<T>(_ scope: Int, _ body: () throws -> T) rethrows -> T? {
        try lock.withLock { scope == epoch ? try body() : nil }
    }

    func put(folder: String, name: String, data: Data) throws {
        try lock.withLock {
            expire()
            // Sealed first: the first use of the key may clear what it cannot open.
            let sealed = try seal(data)
            let dir = try self.folder(folder, create: true)
            let file = dir.appending(path: name)
            let replacing = Int64((try? file.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0)
            guard bytes(of: directory) - replacing + Int64(sealed.count) <= limit else {
                throw UserError("手机待发图片空间不足，请发送或移除旧草稿")
            }
            do {
                try sealed.write(to: file, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            } catch {
                throw UserError("没能保存待发图片")
            }
            if folder == "outbox" && name == "index" {
                // Protect only records named by the durable outbox index. A
                // failed save before that leaves ordinary expiring draft files.
                var protected: Set<String> = [dir.lastPathComponent]
                for line in String(decoding: data, as: UTF8.self).split(separator: "\n", omittingEmptySubsequences: false) {
                    let parts = line.split(separator: "\t", omittingEmptySubsequences: false)
                    if parts.count == 2 { protected.insert(Self.folderName("out:\(parts[0]):\(parts[1])")) }
                }
                for d in folders() {
                    let marker = d.appending(path: ".keep")
                    if protected.contains(d.lastPathComponent) {
                        FileManager.default.createFile(atPath: marker.path, contents: nil)
                    } else {
                        try? FileManager.default.removeItem(at: marker)
                    }
                }
            }
            touch(dir)
        }
    }

    func get(folder: String, name: String) -> Data? {
        lock.withLock {
            expire()
            guard let dir = try? self.folder(folder, create: false),
                  let sealed = try? Data(contentsOf: dir.appending(path: name)) else { return nil }
            touch(dir)
            return try? open(sealed)
        }
    }

    func remove(folder: String, name: String) {
        lock.withLock {
            guard let dir = try? self.folder(folder, create: false) else { return }
            try? FileManager.default.removeItem(at: dir.appending(path: name))
        }
    }

    func clearFolder(_ folder: String) {
        lock.withLock {
            guard let dir = try? self.folder(folder, create: false) else { return }
            try? FileManager.default.removeItem(at: dir)
        }
    }

    /// Logging out: the files, their key and the expired count.
    func clearAll() throws {
        try lock.withLock {
            epoch += 1
            cachedKey = nil
            if FileManager.default.fileExists(atPath: directory.path) { try FileManager.default.removeItem(at: directory) }
            try vault.delete(.pendingKey)
            clearExpiredCount()
        }
    }

    // MARK: Files

    /// A draft's folder is named by a hash of its key, which may hold a path or a session ID.
    private static func folderName(_ key: String) -> String { sha256(Data(key.utf8)).hex.prefix(32).description }

    private func folder(_ key: String, create: Bool) throws -> URL {
        let dir = directory.appending(path: Self.folderName(key), directoryHint: .isDirectory)
        if create {
            try protectedDirectory(directory)
            try protectedDirectory(dir)
        }
        return dir
    }

    private func folders() -> [URL] {
        let all = (try? FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: [.isDirectoryKey])) ?? []
        return all.filter { (try? $0.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true }
    }

    private func modified(_ url: URL) -> Date? {
        try? url.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate
    }

    private func touch(_ url: URL) {
        try? FileManager.default.setAttributes([.modificationDate: now()], ofItemAtPath: url.path)
    }

    private func bytes(of dir: URL) -> Int64 {
        guard let walk = FileManager.default.enumerator(at: dir, includingPropertiesForKeys: [.fileSizeKey, .isRegularFileKey]) else { return 0 }
        var total: Int64 = 0
        for case let url as URL in walk {
            let v = try? url.resourceValues(forKeys: [.fileSizeKey, .isRegularFileKey])
            if v?.isRegularFile == true { total += Int64(v?.fileSize ?? 0) }
        }
        return total
    }

    // MARK: Sealing

    private func seal(_ plain: Data) throws -> Data {
        // Nonce, ciphertext and tag in one piece, as Android writes them.
        guard let combined = try AES.GCM.seal(plain, using: key()).combined else { throw UserError("没能保存待发图片") }
        return combined
    }

    private func open(_ sealed: Data) throws -> Data {
        try AES.GCM.open(AES.GCM.SealedBox(combined: sealed), using: key())
    }

    private func key() throws -> SymmetricKey {
        if let cachedKey { return cachedKey }
        if let stored = try vault.read(.pendingKey), stored.count == keySize {
            let k = SymmetricKey(data: stored)
            cachedKey = k
            return k
        }
        // No key: what was stored under the one before is lost.
        if FileManager.default.fileExists(atPath: directory.path) { try? FileManager.default.removeItem(at: directory) }
        let fresh = randomBytes(keySize)
        try vault.write(.pendingKey, fresh)
        let k = SymmetricKey(data: fresh)
        cachedKey = k
        return k
    }
}

/// A `PendingStore` for the account that was in use when it was made
/// (`PendingStore.scoped`).
struct PendingScope: SealedFiles, RACore.SealedFiles {
    fileprivate let store: PendingStore
    fileprivate let epoch: Int

    func put(folder: String, name: String, data: Data) throws {
        let stored: Void? = try store.current(epoch) { try store.put(folder: folder, name: name, data: data) }
        if stored == nil { throw UserError("已退出账户") }
    }

    func get(folder: String, name: String) -> Data? {
        store.current(epoch) { store.get(folder: folder, name: name) } ?? nil
    }

    func remove(folder: String, name: String) {
        store.current(epoch) { store.remove(folder: folder, name: name) }
    }

    func clearFolder(_ folder: String) {
        store.current(epoch) { store.clearFolder(folder) }
    }
}

/// Logging out. The keys go first: once they are gone nothing else stored
/// here can be opened, so being cut short leaves ciphertext at worst. Stores
/// opened on the cache must be closed before; this device's preferences
/// (`SettingsStore.clear`) are the caller's to reset.
enum LocalAccountData {
    static func erase(vault: Vault = Vault(), cacheRoot: URL = CipherStore.defaultRoot, pending: PendingStore) throws {
        try vault.clear()
        try CipherStore.eraseAll(root: cacheRoot)
        try pending.clearAll()
    }
}
