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

/// File-only rules of the pending images, as Android's core/ImageDiskPolicy.kt.
enum PendingPolicy {
    /// Drafts are user work: they are never evicted, and storing more is refused at this size.
    static let draftLimit = ImageDiskPolicy.draftLimit
    /// A draft folder nothing touched for this long expires.
    static let draftTTL = TimeInterval(ImageDiskPolicy.draftTTL) / 1000
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
    var used: Int64 { lock.withLock { ImageDiskPolicy.bytes(directory) } }

    /// How many images expired unsent since the count was last cleared, for telling the user once.
    var expired: Int { defaults.integer(forKey: Self.expiredKey) }

    func clearExpiredCount() { defaults.removeObject(forKey: Self.expiredKey) }

    /// Removes draft folders nothing touched for `PendingPolicy.draftTTL`, except those the outbox names; returns how many images went.
    @discardableResult
    func expire() -> Int {
        lock.withLock {
            // An index that is there and does not open says nothing about what to keep: nothing goes.
            guard let kept = outboxFolders() else { return 0 }
            var removed = 0
            for folder in folders() where !kept.contains(folder.lastPathComponent) {
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
            // Seal before creating folders, so a key failure leaves the draft untouched.
            let sealed = try seal(data)
            let dir = try self.folder(folder, create: true)
            let file = dir.appending(path: name)
            let replacing = Int64((try? file.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0)
            guard ImageDiskPolicy.bytes(directory) - replacing + Int64(sealed.count) <= limit else {
                throw UserError(L10nMessage.attachmentIosThisPhoneSPendingImageStorageIsFullSend())
            }
            do {
                try sealed.write(to: file, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            } catch {
                throw UserError(L10nMessage.attachmentIosCouldNotSavePendingImages())
            }
            touch(dir)
        }
    }

    func get(folder: String, name: String) throws -> Data? {
        try lock.withLock {
            expire()
            let dir = try self.folder(folder, create: false)
            let sealed: Data
            do { sealed = try Data(contentsOf: dir.appending(path: name)) }
            catch where Self.isMissing(error) { return nil }
            // Reading must never create a replacement key or erase old ciphertext.
            guard let key = try storedKey() else { throw UserError(L10nMessage.attachmentIosPendingAttachmentKeysAreUnavailableRetryShortly()) }
            let plain = try AES.GCM.open(AES.GCM.SealedBox(combined: sealed), using: key)
            touch(dir)
            return plain
        }
    }

    private static func isMissing(_ error: any Error) -> Bool {
        let error = error as NSError
        return error.domain == NSCocoaErrorDomain && error.code == NSFileReadNoSuchFileError
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

    /// Logging out: the files, their key and the expired count. The
    /// account's scopes end and the key in memory is forgotten first, and
    /// whatever fails after that: files that would not go are then
    /// ciphertext nothing here can open, and no scope of the account left
    /// reaches them.
    func clearAll() throws {
        try lock.withLock {
            epoch += 1
            cachedKey = nil
            clearExpiredCount()
            try everyStep([
                { if FileManager.default.fileExists(atPath: self.directory.path) { try FileManager.default.removeItem(at: self.directory) } },
                { try self.vault.delete(.pendingKey) },
            ])
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

    /// The folders that do not expire: the outbox's own, and those of the
    /// messages its index names, which wait to be confirmed or removed
    /// however long that takes. Read from the index every time, which is
    /// what says a message is waiting: a mark left beside each folder could
    /// be missing after a full disk or a process that ended in between. Nil
    /// when the index is there and does not open.
    private func outboxFolders() -> Set<String>? {
        let outbox = Self.folderName("outbox")
        let file = directory.appending(path: outbox, directoryHint: .isDirectory).appending(path: "index")
        let sealed: Data
        do { sealed = try Data(contentsOf: file) }
        catch { return Self.isMissing(error) ? [] : nil }
        guard let key = try? storedKey(),
              let index = try? AES.GCM.open(AES.GCM.SealedBox(combined: sealed), using: key)
        else { return nil }
        return Set(ImageDiskPolicy.outboxKeys(String(decoding: index, as: UTF8.self)).map(Self.folderName)).union([outbox])
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

    // MARK: Sealing

    private func seal(_ plain: Data) throws -> Data {
        // Nonce, ciphertext and tag in one piece, as Android writes them.
        guard let combined = try AES.GCM.seal(plain, using: key()).combined else { throw UserError(L10nMessage.attachmentIosCouldNotSavePendingImages()) }
        return combined
    }

    /// The key there is, without making one.
    private func storedKey() throws -> SymmetricKey? {
        if let cachedKey { return cachedKey }
        guard let stored = try vault.read(.pendingKey), stored.count == keySize else { return nil }
        let k = SymmetricKey(data: stored)
        cachedKey = k
        return k
    }

    private func key() throws -> SymmetricKey {
        if let stored = try storedKey() { return stored }
        // An unavailable key is a recovery failure, not permission to erase drafts.
        do {
            guard try FileManager.default.contentsOfDirectory(atPath: directory.path).isEmpty else {
                throw UserError(L10nMessage.attachmentIosPendingAttachmentKeysAreUnavailableRetryShortly())
            }
        } catch where Self.isMissing(error) {}
        let fresh = randomBytes(keySize)
        try vault.write(.pendingKey, fresh)
        let k = SymmetricKey(data: fresh)
        cachedKey = k
        return k
    }
}

/// A `PendingStore` for the account that was in use when it was made
/// (`PendingStore.scoped`).
struct PendingScope: SealedFiles {
    fileprivate let store: PendingStore
    fileprivate let epoch: Int

    func put(folder: String, name: String, data: Data) throws {
        let stored: Void? = try store.current(epoch) { try store.put(folder: folder, name: name, data: data) }
        if stored == nil { throw UserError(L10nMessage.attachmentIosSignedOutOfTheAccount()) }
    }

    func get(folder: String, name: String) throws -> Data? {
        try store.current(epoch) { try store.get(folder: folder, name: name) } ?? nil
    }

    func remove(folder: String, name: String) {
        store.current(epoch) { store.remove(folder: folder, name: name) }
    }

    func clearFolder(_ folder: String) {
        store.current(epoch) { store.clearFolder(folder) }
    }
}

/// Logging out. The keys go first: once they are gone nothing else stored
/// here can be opened, so being cut short leaves ciphertext at worst. With
/// the keys gone the account is: the cache and the pending images are both
/// cleared whichever of them fails, and the first failure is thrown.
/// Erasing again finishes what an earlier try left. Stores opened on the
/// cache must be closed before; this device's preferences
/// (`SettingsStore.clear`) are the caller's to reset.
enum LocalAccountData {
    static func erase(vault: Vault = Vault(), cacheRoot: URL = CipherStore.defaultRoot, pending: PendingStore) throws {
        try vault.clear()
        try everyStep([
            { try CipherStore.eraseAll(root: cacheRoot) },
            { try pending.clearAll() },
        ])
    }
}

/// Runs every step, also after one failed, then throws the first failure.
private func everyStep(_ steps: [() throws -> Void]) throws {
    var first: (any Error)?
    for step in steps {
        do { try step() } catch { first = first ?? error }
    }
    if let first { throw first }
}
