// Encrypted local persistence for Go Core's draft/outbox records and local
// attachment handles (photos, prepared images, thumbnails, security-scoped
// bookmarks, and gallery videos).
//
// All records, image bytes, and bookmarks are sealed in process with AES-256-GCM
// using a random 32-byte key kept in Keychain (`Vault.Item.pendingKey`). Every
// directory excludes itself from backups and sets iOS file protection
// (`completeFileProtectionUntilFirstUserAuthentication`). Logging out bumps the
// store epoch, forgets the key, deletes all files, and removes the Keychain key.
import CryptoKit
import Foundation
import RACore
import RAProto

/// Disk quota for pending drafts, outbox records, and local attachment blobs.
enum PendingPolicy {
    static let draftLimit = ImageDiskPolicy.draftLimit
}

final class PendingStore: @unchecked Sendable {
    private static let blobMagic = Data("RABLOB1\0".utf8)
    private static let blobHeaderSize = 20 // 8 magic + 8 plainSize + 4 chunkSize
    private static let blobChunkSize = 256 * 1024
    private static let gcmOverhead = 28 // 12-byte nonce + 16-byte tag
    private static let rawPrefix = "__raw__:"
    private static let legacyPrefix = "__legacy__:"

    private let directory: URL
    private let vault: Vault?
    private let limit: Int64
    private let ephemeral: Bool
    private let lock = NSRecursiveLock()
    private var cachedKey: SymmetricKey?
    /// Counts the accounts this store has served: `clearAll` ends one. A
    /// `PendingScope` made before it writes and reads nothing after.
    private var epoch = 0

    // In-memory backing used when `ephemeral == true` (demo mode & unit tests).
    private var memDrafts: [String: Data] = [:]
    private var memOutbox: [String: Data] = [:]
    private var memBlobs: [String: Data] = [:]
    private var memBookmarks: [String: Data] = [:]
    private var memVideos: [String: URL] = [:]

    static var defaultDirectory: URL {
        URL.applicationSupportDirectory.appending(path: "RemoteAgent/pending-images", directoryHint: .isDirectory)
    }

    init(
        directory: URL = PendingStore.defaultDirectory,
        vault: Vault = Vault(),
        limit: Int64 = PendingPolicy.draftLimit
    ) {
        self.directory = directory
        self.vault = vault
        self.limit = limit
        self.ephemeral = false
    }

    private init(ephemeralLimit: Int64) {
        self.directory = FileManager.default.temporaryDirectory
            .appending(path: "ra-ephemeral-pending-\(UUID().uuidString)", directoryHint: .isDirectory)
        self.vault = nil
        self.limit = ephemeralLimit
        self.ephemeral = true
    }

    /// In-memory store for `-demo` mode and unit tests: touches neither Keychain nor permanent storage.
    static func ephemeral(limit: Int64 = PendingPolicy.draftLimit) -> PendingStore {
        PendingStore(ephemeralLimit: limit)
    }

    /// Bytes the pending drafts, outbox records, and local attachment handles take on disk.
    var used: Int64 {
        lock.withLock {
            if ephemeral {
                let d = memDrafts.values.reduce(Int64(0)) { $0 + Int64($1.count) }
                let o = memOutbox.values.reduce(Int64(0)) { $0 + Int64($1.count) }
                let b = memBlobs.values.reduce(Int64(0)) { $0 + Int64($1.count) }
                let k = memBookmarks.values.reduce(Int64(0)) { $0 + Int64($1.count) }
                return d + o + b + k + ImageDiskPolicy.bytes(directory)
            }
            return ImageDiskPolicy.bytes(directory)
        }
    }

    /// The store as one account's writers see it: once `clearAll` runs the scope
    /// reads/writes/deletes nothing so late work cannot leak into the next account.
    func scoped() -> PendingScope {
        PendingScope(store: self, epoch: lock.withLock { epoch })
    }

    fileprivate func current<T>(_ scope: Int, _ body: () throws -> T) rethrows -> T? {
        try lock.withLock { scope == epoch ? try body() : nil }
    }

    // MARK: - Core DraftStorage Effects

    func list(opID: UInt64) -> PBDraftStorageListedEvent {
        lock.withLock {
            if ephemeral {
                var entries: [PBDraftStorageEntry] = []
                for (k, v) in memDrafts {
                    entries.append(.with { $0.bucket = .draft; $0.key = k; $0.data = v })
                }
                for (k, v) in memOutbox {
                    entries.append(.with { $0.bucket = .outbox; $0.key = k; $0.data = v })
                }
                let refs = Array(memBlobs.keys) + Array(memBookmarks.keys) + Array(memVideos.keys)
                return .with {
                    $0.opID = opID
                    $0.entries = entries
                    $0.localRefs = refs
                }
            }

            let fm = FileManager.default
            guard fm.fileExists(atPath: directory.path) else {
                return .with { $0.opID = opID }
            }

            let v1 = v1Root
            let hasEncryptedV1 = hasEncryptedV1Files()
            let loadedKey: SymmetricKey?
            do {
                loadedKey = try storedKey()
            } catch {
                return .with {
                    $0.opID = opID
                    $0.errorKind = .other
                    $0.errorDetail = error.localizedDescription
                }
            }
            if loadedKey == nil && hasEncryptedV1 {
                return .with {
                    $0.opID = opID
                    $0.errorKind = .other
                    $0.errorDetail = "pending attachment key unavailable"
                }
            }

            var entries: [PBDraftStorageEntry] = []
            var localRefs: [String] = []

            if let key = loadedKey {
                for (bucket, dirURL) in [(PBDraftStorageBucket.draft, draftsDir), (PBDraftStorageBucket.outbox, outboxDir)] {
                    let files = Self.visibleFiles(in: dirURL)
                    for file in files {
                        if let sealed = try? Data(contentsOf: file),
                           let plain = try? AES.GCM.open(AES.GCM.SealedBox(combined: sealed), using: key),
                           let decoded = Self.decodeRecord(plain)
                        {
                            entries.append(.with {
                                $0.bucket = bucket
                                $0.key = decoded.key
                                $0.data = decoded.payload
                                $0.corrupt = false
                            })
                        } else {
                            entries.append(.with {
                                $0.bucket = bucket
                                $0.key = Self.rawPrefix + file.lastPathComponent
                                $0.corrupt = true
                            })
                        }
                    }
                }
            }

            // Report pre-v1 legacy files/folders in root as corrupt so Core deletes them cleanly.
            if let rootItems = try? fm.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil) {
                for item in rootItems {
                    let name = item.lastPathComponent
                    if name == "v1" || name.hasPrefix(".") { continue }
                    entries.append(.with {
                        $0.bucket = .draft
                        $0.key = Self.legacyPrefix + name
                        $0.corrupt = true
                    })
                }
            }

            if fm.fileExists(atPath: v1.path) {
                for file in Self.visibleFiles(in: blobsDir) {
                    let ext = file.pathExtension
                    let stem = file.deletingPathExtension().lastPathComponent
                    if ext == "blob", !stem.isEmpty {
                        localRefs.append("blob:\(stem)")
                    } else if ext == "bmk", !stem.isEmpty {
                        localRefs.append("bmk:\(stem)")
                    }
                }
                if let videoDirs = try? fm.contentsOfDirectory(at: videosDir, includingPropertiesForKeys: [.isDirectoryKey]) {
                    for dir in videoDirs {
                        let name = dir.lastPathComponent
                        if name.hasPrefix(".") { continue }
                        localRefs.append("vid:\(name)")
                    }
                }
            }

            return .with {
                $0.opID = opID
                $0.entries = entries
                $0.localRefs = localRefs
            }
        }
    }

    func read(opID: UInt64, bucket: PBDraftStorageBucket, key: String) -> PBDraftStorageReadResultEvent {
        lock.withLock {
            if ephemeral {
                let data = bucket == .outbox ? memOutbox[key] : memDrafts[key]
                return .with {
                    $0.opID = opID
                    $0.bucket = bucket
                    $0.key = key
                    $0.found = data != nil
                    if let data { $0.data = data }
                }
            }
            if key.hasPrefix(Self.rawPrefix) || key.hasPrefix(Self.legacyPrefix) {
                return .with {
                    $0.opID = opID; $0.bucket = bucket; $0.key = key
                    $0.found = false; $0.errorKind = .corruptStore
                }
            }
            let file = recordURL(bucket: bucket, key: key)
            let sealed: Data
            do {
                sealed = try Data(contentsOf: file)
            } catch where Self.isMissing(error) {
                return .with { $0.opID = opID; $0.bucket = bucket; $0.key = key; $0.found = false }
            } catch {
                return .with {
                    $0.opID = opID; $0.bucket = bucket; $0.key = key
                    $0.found = false; $0.errorKind = attachmentIOErrorKind(for: error)
                    $0.errorDetail = error.localizedDescription
                }
            }

            let loadedKey: SymmetricKey
            do {
                guard let k = try storedKey() else {
                    return .with {
                        $0.opID = opID; $0.bucket = bucket; $0.key = key
                        $0.found = false; $0.errorKind = .other
                        $0.errorDetail = "pending attachment key unavailable"
                    }
                }
                loadedKey = k
            } catch {
                return .with {
                    $0.opID = opID; $0.bucket = bucket; $0.key = key
                    $0.found = false; $0.errorKind = .other
                    $0.errorDetail = error.localizedDescription
                }
            }

            guard let plain = try? AES.GCM.open(AES.GCM.SealedBox(combined: sealed), using: loadedKey),
                  let decoded = Self.decodeRecord(plain),
                  decoded.key == key
            else {
                return .with {
                    $0.opID = opID; $0.bucket = bucket; $0.key = key
                    $0.found = false; $0.errorKind = .corruptStore
                }
            }
            return .with {
                $0.opID = opID; $0.bucket = bucket; $0.key = key
                $0.found = true; $0.data = decoded.payload
            }
        }
    }

    func write(opID: UInt64, bucket: PBDraftStorageBucket, key: String, data: Data) -> PBDraftStorageWriteResultEvent {
        lock.withLock {
            if ephemeral {
                let existing = Int64((bucket == .outbox ? memOutbox[key] : memDrafts[key])?.count ?? 0)
                guard used - existing + Int64(data.count) <= limit else {
                    return .with {
                        $0.opID = opID; $0.bucket = bucket; $0.key = key
                        $0.ok = false; $0.errorKind = .storageFull
                    }
                }
                if bucket == .outbox { memOutbox[key] = data } else { memDrafts[key] = data }
                return .with { $0.opID = opID; $0.bucket = bucket; $0.key = key; $0.ok = true }
            }

            do {
                let plain = Self.encodeRecord(key: key, payload: data)
                let sealed = try seal(plain)
                try ensureDir(bucket == .outbox ? outboxDir : draftsDir)
                let file = recordURL(bucket: bucket, key: key)
                let replacing = Self.fileSize(file)
                guard ImageDiskPolicy.bytes(directory) - replacing + Int64(sealed.count) <= limit else {
                    return .with {
                        $0.opID = opID; $0.bucket = bucket; $0.key = key
                        $0.ok = false; $0.errorKind = .storageFull
                    }
                }
                try sealed.write(to: file, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
                return .with { $0.opID = opID; $0.bucket = bucket; $0.key = key; $0.ok = true }
            } catch {
                return .with {
                    $0.opID = opID; $0.bucket = bucket; $0.key = key
                    $0.ok = false
                    $0.errorKind = attachmentIOErrorKind(for: error)
                    $0.errorDetail = error.localizedDescription
                }
            }
        }
    }

    func delete(bucket: PBDraftStorageBucket, key: String) {
        lock.withLock {
            if ephemeral {
                if bucket == .outbox { memOutbox.removeValue(forKey: key) }
                else { memDrafts.removeValue(forKey: key) }
                return
            }
            let fm = FileManager.default
            if key.hasPrefix(Self.rawPrefix) {
                let name = String(key.dropFirst(Self.rawPrefix.count))
                guard Self.isSafeFilename(name) else { return }
                let dir = bucket == .outbox ? outboxDir : draftsDir
                try? fm.removeItem(at: dir.appending(path: name))
                return
            }
            if key.hasPrefix(Self.legacyPrefix) {
                let name = String(key.dropFirst(Self.legacyPrefix.count))
                guard Self.isSafeFilename(name), name != "v1" else { return }
                try? fm.removeItem(at: directory.appending(path: name))
                return
            }
            try? fm.removeItem(at: recordURL(bucket: bucket, key: key))
        }
    }

    /// Logging out: bumps epoch, forgets key, removes directory and Keychain key.
    func clearAll() throws {
        try lock.withLock {
            epoch += 1
            cachedKey = nil
            if ephemeral {
                memDrafts.removeAll()
                memOutbox.removeAll()
                memBlobs.removeAll()
                memBookmarks.removeAll()
                memVideos.removeAll()
                if FileManager.default.fileExists(atPath: directory.path) {
                    try? FileManager.default.removeItem(at: directory)
                }
                return
            }
            try everyStep([
                { if FileManager.default.fileExists(atPath: self.directory.path) { try FileManager.default.removeItem(at: self.directory) } },
                { try self.vault?.delete(.pendingKey) },
            ])
        }
    }

    // MARK: - Local Attachment Handles (`blob:`, `bmk:`, `vid:`)

    /// Stores raw or prepared image/thumbnail bytes as a chunked AES-256-GCM blob and returns `"blob:<id>"`.
    func storeBlob(_ data: Data, prefix: String = "b") throws -> String {
        try lock.withLock {
            let id = "\(prefix)-\(newRequestId())"
            let ref = "blob:\(id)"
            if ephemeral {
                guard used + Int64(data.count) <= limit else {
                    throw AttachmentFileIOError(.storageFull)
                }
                memBlobs[ref] = data
                return ref
            }
            let key = try self.key()
            let totalPlain = Int64(data.count)
            let chunkSize = Self.blobChunkSize
            let chunkCount = totalPlain == 0 ? 0 : Int((totalPlain + Int64(chunkSize) - 1) / Int64(chunkSize))
            let estimatedBytes = Int64(Self.blobHeaderSize) + totalPlain + Int64(chunkCount) * Int64(Self.gcmOverhead)
            guard ImageDiskPolicy.bytes(directory) + estimatedBytes <= limit else {
                throw AttachmentFileIOError(.storageFull)
            }
            try ensureDir(blobsDir)
            let target = blobsDir.appending(path: "\(id).blob")
            let tmp = blobsDir.appending(path: ".\(id).blob.tmp")
            FileManager.default.createFile(
                atPath: tmp.path,
                contents: nil,
                attributes: [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication]
            )
            do {
                let handle = try FileHandle(forWritingTo: tmp)
                defer { try? handle.close() }
                var header = Data()
                header.reserveCapacity(Self.blobHeaderSize)
                header.append(Self.blobMagic)
                var sizeBE = UInt64(totalPlain).bigEndian
                withUnsafeBytes(of: &sizeBE) { header.append(contentsOf: $0) }
                var chunkBE = UInt32(chunkSize).bigEndian
                withUnsafeBytes(of: &chunkBE) { header.append(contentsOf: $0) }
                try handle.write(contentsOf: header)

                var offset = 0
                var chunkIdx = 0
                while offset < data.count {
                    let end = min(offset + chunkSize, data.count)
                    try autoreleasepool {
                        let slice = data[offset..<end]
                        guard let combined = try AES.GCM.seal(
                            slice,
                            using: key,
                            authenticating: Self.blobAAD(ref: ref, chunkIndex: chunkIdx)
                        ).combined else {
                            throw AttachmentFileIOError(.other, detail: "seal failed")
                        }
                        try handle.write(contentsOf: combined)
                    }
                    offset = end
                    chunkIdx += 1
                }
                try handle.synchronize()
                try FileManager.default.moveItem(at: tmp, to: target)
                return ref
            } catch {
                try? FileManager.default.removeItem(at: tmp)
                throw error
            }
        }
    }

    /// Reads and decrypts a full `"blob:<id>"` (for `AttachmentPrepareImageEffect`, thumbnails, and viewer).
    func readBlob(_ ref: String) throws -> Data {
        try lock.withLock {
            guard let id = Self.refID(ref, prefix: "blob:") else {
                throw AttachmentFileIOError(.sourceMissing)
            }
            if ephemeral {
                guard let data = memBlobs[ref] else { throw AttachmentFileIOError(.sourceMissing) }
                return data
            }
            let file = blobsDir.appending(path: "\(id).blob")
            guard FileManager.default.fileExists(atPath: file.path) else {
                throw AttachmentFileIOError(.sourceMissing)
            }
            guard let key = try storedKey() else {
                throw AttachmentFileIOError(.other, detail: "pending attachment key unavailable")
            }
            let handle = try FileHandle(forReadingFrom: file)
            defer { try? handle.close() }
            let (plainSize, chunkSize) = try Self.readBlobHeader(handle)
            if plainSize == 0 { return Data() }
            var out = Data()
            out.reserveCapacity(Int(plainSize))
            let totalChunks = Int((plainSize + Int64(chunkSize) - 1) / Int64(chunkSize))
            for idx in 0..<totalChunks {
                let plainLen = idx == totalChunks - 1
                    ? Int(plainSize - Int64(idx) * Int64(chunkSize))
                    : chunkSize
                let sealedLen = plainLen + Self.gcmOverhead
                let chunk = try autoreleasepool {
                    guard let sealed = try handle.read(upToCount: sealedLen), sealed.count == sealedLen else {
                        throw AttachmentFileIOError(.corruptStore)
                    }
                    return try AES.GCM.open(
                        AES.GCM.SealedBox(combined: sealed),
                        using: key,
                        authenticating: Self.blobAAD(ref: ref, chunkIndex: idx)
                    )
                }
                out.append(chunk)
            }
            return out
        }
    }

    /// Streams an exact `[offset, offset + length)` slice from `"blob:<id>"` without decoding the whole file.
    func readBlobChunk(_ ref: String, offset: Int64, length: Int, expectedSizeBytes: Int64) throws -> Data {
        try lock.withLock {
            guard let id = Self.refID(ref, prefix: "blob:") else {
                throw AttachmentFileIOError(.sourceMissing)
            }
            guard offset >= 0, length >= 0, expectedSizeBytes >= 0, offset + Int64(length) <= expectedSizeBytes else {
                throw AttachmentFileIOError(.sourceChanged)
            }
            if ephemeral {
                guard let data = memBlobs[ref] else { throw AttachmentFileIOError(.sourceMissing) }
                guard Int64(data.count) == expectedSizeBytes else { throw AttachmentFileIOError(.sourceChanged) }
                if length == 0 { return Data() }
                return Data(data[Int(offset)..<Int(offset + Int64(length))])
            }
            let file = blobsDir.appending(path: "\(id).blob")
            guard FileManager.default.fileExists(atPath: file.path) else {
                throw AttachmentFileIOError(.sourceMissing)
            }
            guard let key = try storedKey() else {
                throw AttachmentFileIOError(.other, detail: "pending attachment key unavailable")
            }
            let handle = try FileHandle(forReadingFrom: file)
            defer { try? handle.close() }
            let (plainSize, chunkSize) = try Self.readBlobHeader(handle)
            guard plainSize == expectedSizeBytes else {
                throw AttachmentFileIOError(.sourceChanged)
            }
            if length == 0 { return Data() }

            let totalChunks = Int((plainSize + Int64(chunkSize) - 1) / Int64(chunkSize))
            let firstChunk = Int(offset / Int64(chunkSize))
            let lastChunk = Int((offset + Int64(length) - 1) / Int64(chunkSize))
            let sealedStride = Int64(chunkSize) + Int64(Self.gcmOverhead)

            var out = Data()
            out.reserveCapacity(length)
            for idx in firstChunk...lastChunk {
                let chunkStart = Int64(idx) * Int64(chunkSize)
                let plainLen = idx == totalChunks - 1 ? Int(plainSize - chunkStart) : chunkSize
                let sealedLen = plainLen + Self.gcmOverhead
                let fileOffset = UInt64(Int64(Self.blobHeaderSize) + Int64(idx) * sealedStride)
                let slice = try autoreleasepool {
                    try handle.seek(toOffset: fileOffset)
                    guard let sealed = try handle.read(upToCount: sealedLen), sealed.count == sealedLen else {
                        throw AttachmentFileIOError(.corruptStore)
                    }
                    let plain = try AES.GCM.open(
                        AES.GCM.SealedBox(combined: sealed),
                        using: key,
                        authenticating: Self.blobAAD(ref: ref, chunkIndex: idx)
                    )
                    let lo = max(0, Int(offset - chunkStart))
                    let hi = min(plainLen, Int(offset + Int64(length) - chunkStart))
                    return plain[lo..<hi]
                }
                out.append(slice)
            }
            return out
        }
    }

    /// Streams `"blob:<id>"` to compute exact byte size and SHA-256 without loading the entire blob into memory.
    func hashBlob(_ ref: String, maxBytes: Int64) throws -> (sizeBytes: Int64, sha256: Data) {
        try lock.withLock {
            guard let id = Self.refID(ref, prefix: "blob:") else {
                throw AttachmentFileIOError(.sourceMissing)
            }
            if ephemeral {
                guard let data = memBlobs[ref] else { throw AttachmentFileIOError(.sourceMissing) }
                if maxBytes > 0 && Int64(data.count) > maxBytes { throw AttachmentFileIOError(.fileTooLarge) }
                return (Int64(data.count), Data(SHA256.hash(data: data)))
            }
            let file = blobsDir.appending(path: "\(id).blob")
            guard FileManager.default.fileExists(atPath: file.path) else {
                throw AttachmentFileIOError(.sourceMissing)
            }
            guard let key = try storedKey() else {
                throw AttachmentFileIOError(.other, detail: "pending attachment key unavailable")
            }
            let handle = try FileHandle(forReadingFrom: file)
            defer { try? handle.close() }
            let (plainSize, chunkSize) = try Self.readBlobHeader(handle)
            if maxBytes > 0 && plainSize > maxBytes { throw AttachmentFileIOError(.fileTooLarge) }
            var hasher = SHA256()
            let totalChunks = plainSize == 0 ? 0 : Int((plainSize + Int64(chunkSize) - 1) / Int64(chunkSize))
            for idx in 0..<totalChunks {
                let plainLen = idx == totalChunks - 1
                    ? Int(plainSize - Int64(idx) * Int64(chunkSize))
                    : chunkSize
                let sealedLen = plainLen + Self.gcmOverhead
                try autoreleasepool {
                    guard let sealed = try handle.read(upToCount: sealedLen), sealed.count == sealedLen else {
                        throw AttachmentFileIOError(.corruptStore)
                    }
                    let plain = try AES.GCM.open(
                        AES.GCM.SealedBox(combined: sealed),
                        using: key,
                        authenticating: Self.blobAAD(ref: ref, chunkIndex: idx)
                    )
                    hasher.update(data: plain)
                }
            }
            return (plainSize, Data(hasher.finalize()))
        }
    }

    /// Stores a security-scoped bookmark sealed on disk and returns `"bmk:<id>"`.
    func storeBookmark(_ bookmark: Data) throws -> String {
        try lock.withLock {
            let id = newRequestId()
            let ref = "bmk:\(id)"
            if ephemeral {
                memBookmarks[ref] = bookmark
                return ref
            }
            let sealed = try seal(bookmark)
            try ensureDir(blobsDir)
            guard ImageDiskPolicy.bytes(directory) + Int64(sealed.count) <= limit else {
                throw AttachmentFileIOError(.storageFull)
            }
            let file = blobsDir.appending(path: "\(id).bmk")
            try sealed.write(to: file, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            return ref
        }
    }

    func readBookmark(_ ref: String) throws -> Data {
        try lock.withLock {
            guard let id = Self.refID(ref, prefix: "bmk:") else {
                throw AttachmentFileIOError(.sourceMissing)
            }
            if ephemeral {
                guard let data = memBookmarks[ref] else { throw AttachmentFileIOError(.sourceMissing) }
                return data
            }
            let file = blobsDir.appending(path: "\(id).bmk")
            let sealed: Data
            do { sealed = try Data(contentsOf: file) }
            catch where Self.isMissing(error) { throw AttachmentFileIOError(.sourceMissing) }
            guard let key = try storedKey() else {
                throw AttachmentFileIOError(.other, detail: "pending attachment key unavailable")
            }
            return try AES.GCM.open(AES.GCM.SealedBox(combined: sealed), using: key)
        }
    }

    /// Refreshes a stale bookmark in place under the same `"bmk:<id>"` handle.
    func updateBookmark(_ ref: String, bookmark: Data) throws {
        try lock.withLock {
            guard let id = Self.refID(ref, prefix: "bmk:") else { return }
            if ephemeral {
                if memBookmarks[ref] != nil { memBookmarks[ref] = bookmark }
                return
            }
            let file = blobsDir.appending(path: "\(id).bmk")
            guard FileManager.default.fileExists(atPath: file.path) else { return }
            let sealed = try seal(bookmark)
            let replacing = Self.fileSize(file)
            guard ImageDiskPolicy.bytes(directory) - replacing + Int64(sealed.count) <= limit else {
                throw AttachmentFileIOError(.storageFull)
            }
            try sealed.write(to: file, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        }
    }

    /// Moves/copies a photo-library video file into protected pending storage and returns `"vid:<id>"`.
    func storeVideo(from sourceURL: URL, filename: String? = nil) throws -> (ref: String, url: URL) {
        try lock.withLock {
            let id = newRequestId()
            let ref = "vid:\(id)"
            let safeName: String = {
                let raw = (filename?.isEmpty == false ? filename! : sourceURL.lastPathComponent)
                return Self.isSafeFilename(raw) ? raw : "video.mov"
            }()
            let size = Self.fileSize(sourceURL)
            guard ImageDiskPolicy.bytes(directory) + size <= limit else {
                throw AttachmentFileIOError(.storageFull)
            }
            try ensureDir(videosDir)
            let folder = videosDir.appending(path: id, directoryHint: .isDirectory)
            try protectedDirectory(folder)
            let dst = folder.appending(path: safeName)
            do {
                try FileManager.default.copyItem(at: sourceURL, to: dst)
                try? FileManager.default.setAttributes(
                    [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
                    ofItemAtPath: dst.path
                )
            } catch {
                try? FileManager.default.removeItem(at: folder)
                throw error
            }
            if ephemeral {
                memVideos[ref] = dst
            }
            return (ref, dst)
        }
    }

    func videoURL(_ ref: String) throws -> URL {
        try lock.withLock {
            guard let id = Self.refID(ref, prefix: "vid:") else {
                throw AttachmentFileIOError(.sourceMissing)
            }
            if ephemeral {
                guard let url = memVideos[ref], FileManager.default.fileExists(atPath: url.path) else {
                    throw AttachmentFileIOError(.sourceMissing)
                }
                return url
            }
            let folder = videosDir.appending(path: id, directoryHint: .isDirectory)
            guard let first = Self.visibleFiles(in: folder).first else {
                throw AttachmentFileIOError(.sourceMissing)
            }
            return first
        }
    }

    func releaseLocal(refs: [String]) {
        lock.withLock {
            let fm = FileManager.default
            for ref in refs {
                if let id = Self.refID(ref, prefix: "blob:") {
                    if ephemeral { memBlobs.removeValue(forKey: ref) }
                    else { try? fm.removeItem(at: blobsDir.appending(path: "\(id).blob")) }
                } else if let id = Self.refID(ref, prefix: "bmk:") {
                    if ephemeral { memBookmarks.removeValue(forKey: ref) }
                    else { try? fm.removeItem(at: blobsDir.appending(path: "\(id).bmk")) }
                } else if let id = Self.refID(ref, prefix: "vid:") {
                    if ephemeral {
                        if let url = memVideos.removeValue(forKey: ref) {
                            try? fm.removeItem(at: url.deletingLastPathComponent())
                        }
                    } else {
                        try? fm.removeItem(at: videosDir.appending(path: id, directoryHint: .isDirectory))
                    }
                }
            }
        }
    }

    // MARK: - Paths & Framing

    private var v1Root: URL { directory.appending(path: "v1", directoryHint: .isDirectory) }
    private var draftsDir: URL { v1Root.appending(path: "drafts", directoryHint: .isDirectory) }
    private var outboxDir: URL { v1Root.appending(path: "outbox", directoryHint: .isDirectory) }
    private var blobsDir: URL { v1Root.appending(path: "blobs", directoryHint: .isDirectory) }
    private var videosDir: URL { v1Root.appending(path: "videos", directoryHint: .isDirectory) }

    private func recordURL(bucket: PBDraftStorageBucket, key: String) -> URL {
        let dir = bucket == .outbox ? outboxDir : draftsDir
        let name = sha256(Data(key.utf8)).hex + ".rec"
        return dir.appending(path: name)
    }

    private func ensureDir(_ dir: URL) throws {
        try protectedDirectory(directory)
        try protectedDirectory(v1Root)
        try protectedDirectory(dir)
    }

    private func hasEncryptedV1Files() -> Bool {
        !Self.visibleFiles(in: draftsDir).isEmpty
            || !Self.visibleFiles(in: outboxDir).isEmpty
            || !Self.visibleFiles(in: blobsDir).isEmpty
    }

    private static func visibleFiles(in dir: URL) -> [URL] {
        guard let list = try? FileManager.default.contentsOfDirectory(
            at: dir,
            includingPropertiesForKeys: [.isRegularFileKey]
        ) else { return [] }
        return list.filter {
            !$0.lastPathComponent.hasPrefix(".")
                && (try? $0.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true
        }
    }

    private static func fileSize(_ url: URL) -> Int64 {
        Int64((try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0)
    }

    private static func isSafeFilename(_ name: String) -> Bool {
        !name.isEmpty && !name.contains("/") && !name.contains("\\") && name != "." && name != ".."
    }

    private static func refID(_ ref: String, prefix: String) -> String? {
        guard ref.hasPrefix(prefix) else { return nil }
        let id = String(ref.dropFirst(prefix.count))
        guard isSafeFilename(id) else { return nil }
        return id
    }

    private static func encodeRecord(key: String, payload: Data) -> Data {
        let keyBytes = Data(key.utf8)
        var out = Data()
        out.reserveCapacity(4 + keyBytes.count + payload.count)
        var lenBE = UInt32(keyBytes.count).bigEndian
        withUnsafeBytes(of: &lenBE) { out.append(contentsOf: $0) }
        out.append(keyBytes)
        out.append(payload)
        return out
    }

    private static func decodeRecord(_ plain: Data) -> (key: String, payload: Data)? {
        guard plain.count >= 4 else { return nil }
        let len = plain.prefix(4).withUnsafeBytes { raw in
            UInt32(bigEndian: raw.load(as: UInt32.self))
        }
        let end = 4 + Int(len)
        guard end <= plain.count else { return nil }
        guard let key = String(data: plain[4..<end], encoding: .utf8), !key.isEmpty else { return nil }
        return (key, Data(plain[end...]))
    }

    private static func readBlobHeader(_ handle: FileHandle) throws -> (plainSize: Int64, chunkSize: Int) {
        guard let header = try handle.read(upToCount: blobHeaderSize),
              header.count == blobHeaderSize,
              header.prefix(8) == blobMagic
        else {
            throw AttachmentFileIOError(.corruptStore)
        }
        let plainSizeU64 = header[8..<16].withUnsafeBytes { UInt64(bigEndian: $0.loadUnaligned(as: UInt64.self)) }
        let chunkSizeU32 = header[16..<20].withUnsafeBytes { UInt32(bigEndian: $0.loadUnaligned(as: UInt32.self)) }
        guard plainSizeU64 <= UInt64(Int64.max), chunkSizeU32 > 0 else {
            throw AttachmentFileIOError(.corruptStore)
        }
        return (Int64(plainSizeU64), Int(chunkSizeU32))
    }

    private static func blobAAD(ref: String, chunkIndex: Int) -> Data {
        Data("\(ref):\(chunkIndex)".utf8)
    }

    private static func isMissing(_ error: any Error) -> Bool {
        let ns = error as NSError
        return (ns.domain == NSCocoaErrorDomain && (ns.code == NSFileReadNoSuchFileError || ns.code == NSFileNoSuchFileError))
            || (ns.domain == NSPOSIXErrorDomain && ns.code == Int(ENOENT))
    }

    // MARK: - Sealing

    private func seal(_ plain: Data) throws -> Data {
        guard let combined = try AES.GCM.seal(plain, using: key()).combined else {
            throw AttachmentFileIOError(.other, detail: "seal failed")
        }
        return combined
    }

    private func storedKey() throws -> SymmetricKey? {
        if let cachedKey { return cachedKey }
        guard let vault, let stored = try vault.read(.pendingKey), stored.count == keySize else { return nil }
        let k = SymmetricKey(data: stored)
        cachedKey = k
        return k
    }

    private func key() throws -> SymmetricKey {
        if let stored = try storedKey() { return stored }
        if hasEncryptedV1Files() {
            throw AttachmentFileIOError(.other, detail: "pending attachment key unavailable")
        }
        let fresh = randomBytes(keySize)
        try vault?.write(.pendingKey, fresh)
        let k = SymmetricKey(data: fresh)
        cachedKey = k
        return k
    }
}

/// Account-scoped handle into `PendingStore` (`PendingStore.scoped()`): once
/// `clearAll()` runs on logout, stale scopes become inert and never touch the next account.
struct PendingScope: Sendable {
    fileprivate let store: PendingStore
    fileprivate let epoch: Int

    var used: Int64 { store.current(epoch) { store.used } ?? 0 }

    func list(opID: UInt64) -> PBDraftStorageListedEvent {
        store.current(epoch) { store.list(opID: opID) } ?? .with { $0.opID = opID }
    }

    func read(opID: UInt64, bucket: PBDraftStorageBucket, key: String) -> PBDraftStorageReadResultEvent {
        store.current(epoch) { store.read(opID: opID, bucket: bucket, key: key) } ?? .with {
            $0.opID = opID; $0.bucket = bucket; $0.key = key; $0.found = false
        }
    }

    func write(opID: UInt64, bucket: PBDraftStorageBucket, key: String, data: Data) -> PBDraftStorageWriteResultEvent {
        store.current(epoch) { store.write(opID: opID, bucket: bucket, key: key, data: data) } ?? .with {
            $0.opID = opID; $0.bucket = bucket; $0.key = key
            $0.ok = false; $0.errorKind = .other; $0.errorDetail = "signed out"
        }
    }

    func delete(bucket: PBDraftStorageBucket, key: String) {
        store.current(epoch) { store.delete(bucket: bucket, key: key) }
    }

    func clearAll() {
        store.current(epoch) { try? store.clearAll() }
    }

    func storeBlob(_ data: Data, prefix: String = "b") throws -> String {
        guard let ref = try store.current(epoch, { try store.storeBlob(data, prefix: prefix) }) else {
            throw AttachmentFileIOError(.other, detail: "signed out")
        }
        return ref
    }

    func readBlob(_ ref: String) throws -> Data {
        guard let data = try store.current(epoch, { try store.readBlob(ref) }) else {
            throw AttachmentFileIOError(.sourceMissing, detail: "signed out")
        }
        return data
    }

    func readBlobChunk(_ ref: String, offset: Int64, length: Int, expectedSizeBytes: Int64) throws -> Data {
        guard let data = try store.current(epoch, { try store.readBlobChunk(ref, offset: offset, length: length, expectedSizeBytes: expectedSizeBytes) }) else {
            throw AttachmentFileIOError(.sourceMissing, detail: "signed out")
        }
        return data
    }

    func hashBlob(_ ref: String, maxBytes: Int64) throws -> (sizeBytes: Int64, sha256: Data) {
        guard let res = try store.current(epoch, { try store.hashBlob(ref, maxBytes: maxBytes) }) else {
            throw AttachmentFileIOError(.sourceMissing, detail: "signed out")
        }
        return res
    }

    func storeBookmark(_ bookmark: Data) throws -> String {
        guard let ref = try store.current(epoch, { try store.storeBookmark(bookmark) }) else {
            throw AttachmentFileIOError(.other, detail: "signed out")
        }
        return ref
    }

    func readBookmark(_ ref: String) throws -> Data {
        guard let data = try store.current(epoch, { try store.readBookmark(ref) }) else {
            throw AttachmentFileIOError(.sourceMissing, detail: "signed out")
        }
        return data
    }

    func updateBookmark(_ ref: String, bookmark: Data) throws {
        try store.current(epoch) { try store.updateBookmark(ref, bookmark: bookmark) }
    }

    func storeVideo(from sourceURL: URL, filename: String? = nil) throws -> (ref: String, url: URL) {
        guard let res = try store.current(epoch, { try store.storeVideo(from: sourceURL, filename: filename) }) else {
            throw AttachmentFileIOError(.other, detail: "signed out")
        }
        return res
    }

    func videoURL(_ ref: String) throws -> URL {
        guard let url = try store.current(epoch, { try store.videoURL(ref) }) else {
            throw AttachmentFileIOError(.sourceMissing, detail: "signed out")
        }
        return url
    }

    func releaseLocal(refs: [String]) {
        store.current(epoch) { store.releaseLocal(refs: refs) }
    }
}

/// Logging out: keys go first, then cache, pending store, and shared files.
enum LocalAccountData {
    static func erase(vault: Vault = Vault(), cacheRoot: URL = CipherStore.defaultRoot, sharedFiles: URL = SharedFileCenter.defaultRoot, pending: PendingStore) throws {
        try vault.clear()
        try everyStep([
            { try CipherStore.eraseAll(root: cacheRoot) },
            { try pending.clearAll() },
            { if FileManager.default.fileExists(atPath: sharedFiles.path) { try FileManager.default.removeItem(at: sharedFiles) } },
        ])
    }
}

extension PBDraftScope {
    static func session(_ id: String) -> PBDraftScope {
        .with { $0.kind = .session; $0.id = id }
    }

    static func newSession(_ projectKey: String) -> PBDraftScope {
        .with { $0.kind = .newSession; $0.id = projectKey }
    }

    var debugKey: String {
        switch kind {
        case .session: return "session:\(id)"
        case .newSession: return "new:\(id)"
        default: return id
        }
    }
}

private func everyStep(_ steps: [() throws -> Void]) throws {
    var first: (any Error)?
    for step in steps {
        do { try step() } catch { first = first ?? error }
    }
    if let first { throw first }
}
