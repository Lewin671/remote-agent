// Thin platform I/O executor for Core-driven shared-file downloads:
// disk scanning, free-space inspection, chunk persistence, streaming SHA-256
// verification & atomic commit, and cache cleanup.
//
// All download state transitions, block planning, retries, and ETA live in Go clientcore.
// Callers must serialize all storage operations through a single global FIFO queue.
//
// Layout under `root`, one folder per session and per file:
//
//   <session>/<file>/.part       the unpadded blocks written so far
//   <session>/<file>/.meta       v2 JSON metadata (schema_version=2)
//   <session>/<file>/file/<name> the committed copy, named as the sanitized display name
import CryptoKit
import Foundation
import RAProto

public struct SharedFileStore: Sendable {
    private static let metaSchemaVersion = 2

    private struct Meta: Codable, Sendable {
        var schemaVersion: Int
        var sessionId: String
        var fileId: String
        var displayName: String
        var totalSizeBytes: Int64
        var chunkBytes: Int32
        var committedOffset: Int64
        var expectedSha256: Data
        var durationMs: Int64?

        enum CodingKeys: String, CodingKey {
            case schemaVersion = "schema_version"
            case sessionId = "session_id"
            case fileId = "file_id"
            case displayName = "display_name"
            case totalSizeBytes = "total_size_bytes"
            case chunkBytes = "chunk_bytes"
            case committedOffset = "committed_offset"
            case expectedSha256 = "expected_sha256"
            case durationMs = "duration_ms"
        }
    }

    public let root: URL
    private let availableCapacity: @Sendable (URL) throws -> Int64?

    public init(
        root: URL,
        availableCapacity: @escaping @Sendable (URL) throws -> Int64? = SharedFileStore.defaultAvailableCapacity
    ) {
        self.root = root
        self.availableCapacity = availableCapacity
    }

    // MARK: Paths

    private func directory(_ key: SharedFileKey) -> URL {
        root.appending(path: sharedFileFolderName(key.sessionId), directoryHint: .isDirectory)
            .appending(path: sharedFileFolderName(key.fileId), directoryHint: .isDirectory)
    }

    private func partURL(_ key: SharedFileKey) -> URL { directory(key).appending(path: ".part") }
    private func metaURL(_ key: SharedFileKey) -> URL { directory(key).appending(path: ".meta") }
    private func fileFolderURL(_ key: SharedFileKey) -> URL { directory(key).appending(path: "file", directoryHint: .isDirectory) }

    /// Where the finished copy of `name` is, or would be.
    public func localURL(_ key: SharedFileKey, name: String) -> URL {
        fileFolderURL(key).appending(path: safeLocalSharedFileName(name), directoryHint: .notDirectory)
    }

    /// Returns the existing committed local copy on disk, or `nil` if absent (e.g. reclaimed by the OS cache cleaner).
    public func existingLocalURL(_ key: SharedFileKey, name: String) -> URL? {
        let fm = FileManager.default
        if !name.isEmpty {
            let candidate = localURL(key, name: name)
            var isDir: ObjCBool = false
            if fm.fileExists(atPath: candidate.path, isDirectory: &isDir), !isDir.boolValue {
                return candidate
            }
        }
        let folder = fileFolderURL(key)
        guard let items = try? fm.contentsOfDirectory(at: folder, includingPropertiesForKeys: [.isRegularFileKey], options: .skipsHiddenFiles) else {
            return nil
        }
        return items.first { url in
            (try? url.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true
        }
    }

    /// What the copies and partial downloads take on disk.
    public func usedBytes() -> Int64 { ImageDiskPolicy.bytes(root) }

    // MARK: Core Storage Effects

    /// Executes `SharedFileScanLocalEffect`: enumerates persisted completed copies and partial downloads.
    /// Unrecognized / legacy `.meta` formats are never migrated and are reported as malformed so Core cleans them up.
    public func scanLocal(opID: UInt64) -> PBSharedFileLocalScannedEvent {
        let fm = FileManager.default
        var isDir: ObjCBool = false
        guard fm.fileExists(atPath: root.path, isDirectory: &isDir), isDir.boolValue else {
            return .with { $0.opID = opID }
        }
        let sessionDirs: [URL]
        do {
            sessionDirs = try fm.contentsOfDirectory(at: root, includingPropertiesForKeys: [.isDirectoryKey], options: .skipsHiddenFiles)
        } catch {
            return .with {
                $0.opID = opID
                $0.error = error.localizedDescription
            }
        }

        var entries: [PBSharedFileLocalEntry] = []
        for sessionDir in sessionDirs {
            guard (try? sessionDir.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true,
                  let fileDirs = try? fm.contentsOfDirectory(at: sessionDir, includingPropertiesForKeys: [.isDirectoryKey], options: .skipsHiddenFiles)
            else { continue }

            for fileDir in fileDirs {
                guard (try? fileDir.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true else { continue }
                let metaFile = fileDir.appending(path: ".meta")
                let partFile = fileDir.appending(path: ".part")
                let fileFolder = fileDir.appending(path: "file", directoryHint: .isDirectory)
                let fallbackSid = sessionDir.lastPathComponent
                let fallbackFid = fileDir.lastPathComponent

                guard let meta = Self.readMeta(at: metaFile),
                      meta.schemaVersion == Self.metaSchemaVersion,
                      !meta.sessionId.isEmpty,
                      !meta.fileId.isEmpty
                else {
                    // Legacy or corrupt metadata: never read/migrate; report as malformed so Core emits DeleteLocal / DeletePartial.
                    if Self.firstCommittedFile(in: fileFolder, preferredName: nil) != nil {
                        entries.append(.with {
                            $0.sessionID = fallbackSid
                            $0.fileID = fallbackFid
                            $0.completed = true
                            $0.bytesOnDisk = -1
                        })
                    } else if fm.fileExists(atPath: partFile.path) || fm.fileExists(atPath: metaFile.path) {
                        entries.append(.with {
                            $0.sessionID = fallbackSid
                            $0.fileID = fallbackFid
                            $0.completed = false
                            $0.bytesOnDisk = Self.fileLength(partFile) ?? 0
                        })
                    }
                    continue
                }

                if let finalURL = Self.firstCommittedFile(in: fileFolder, preferredName: meta.displayName),
                   let actualBytes = Self.fileLength(finalURL)
                {
                    entries.append(.with {
                        $0.sessionID = meta.sessionId
                        $0.fileID = meta.fileId
                        $0.completed = true
                        $0.bytesOnDisk = actualBytes
                        $0.expectedSizeBytes = meta.totalSizeBytes
                        $0.chunkBytes = meta.chunkBytes
                        $0.committedOffset = meta.committedOffset
                        $0.sha256 = meta.expectedSha256
                        if let duration = meta.durationMs, duration > 0 {
                            $0.hasDuration_p = true
                            $0.durationMs = duration
                        }
                    })
                } else if fm.fileExists(atPath: partFile.path), let partBytes = Self.fileLength(partFile) {
                    entries.append(.with {
                        $0.sessionID = meta.sessionId
                        $0.fileID = meta.fileId
                        $0.completed = false
                        $0.bytesOnDisk = partBytes
                        $0.expectedSizeBytes = meta.totalSizeBytes
                        $0.chunkBytes = meta.chunkBytes
                        $0.committedOffset = meta.committedOffset
                        $0.sha256 = meta.expectedSha256
                    })
                } else {
                    // Orphaned .meta with neither final copy nor .part: report malformed partial so Core cleans it up.
                    entries.append(.with {
                        $0.sessionID = meta.sessionId
                        $0.fileID = meta.fileId
                        $0.completed = false
                        $0.bytesOnDisk = 0
                    })
                }
            }
        }
        return .with {
            $0.opID = opID
            $0.entries = entries
        }
    }

    /// Executes `SharedFileInspectDiskEffect`: reports free volume capacity for important usage.
    public func inspectDisk(_ effect: PBSharedFileInspectDiskEffect) -> PBSharedFileDiskInspectedEvent {
        let fm = FileManager.default
        do {
            try fm.createDirectory(at: root, withIntermediateDirectories: true, attributes: Self.protection)
            if let available = try availableCapacity(root) {
                return .with {
                    $0.opID = effect.opID
                    $0.sessionID = effect.sessionID
                    $0.fileID = effect.fileID
                    $0.known = true
                    $0.availableBytes = max(0, available)
                }
            }
            return .with {
                $0.opID = effect.opID
                $0.sessionID = effect.sessionID
                $0.fileID = effect.fileID
                $0.known = false
            }
        } catch {
            let kind = Self.ioErrorKind(for: error)
            return .with {
                $0.opID = effect.opID
                $0.sessionID = effect.sessionID
                $0.fileID = effect.fileID
                $0.known = false
                if kind == .storageFull {
                    $0.errorKind = .storageFull
                    $0.errorDetail = error.localizedDescription
                }
            }
        }
    }

    /// Executes `SharedFileWriteChunkEffect`: writes validated unpadded chunk bytes to `.part` and atomically updates `.meta`.
    public func writeChunk(_ effect: PBSharedFileWriteChunkEffect) -> PBSharedFileWriteChunkResultEvent {
        let key = SharedFileKey(sessionId: effect.sessionID, fileId: effect.fileID)
        let dir = directory(key)
        let part = partURL(key)
        let fm = FileManager.default

        do {
            try fm.createDirectory(at: dir, withIntermediateDirectories: true, attributes: Self.protection)
            if effect.resetPartial {
                try? fm.removeItem(at: part)
                guard fm.createFile(atPath: part.path, contents: nil, attributes: Self.protection) else {
                    throw NSError(domain: NSPOSIXErrorDomain, code: Int(errno != 0 ? errno : EIO))
                }
            } else if !fm.fileExists(atPath: part.path) {
                if effect.offset > 0 {
                    return .with {
                        $0.opID = effect.opID
                        $0.sessionID = effect.sessionID
                        $0.fileID = effect.fileID
                        $0.ok = false
                        $0.errorKind = .checksumMismatch
                        $0.errorDetail = "partial file missing"
                    }
                }
                guard fm.createFile(atPath: part.path, contents: nil, attributes: Self.protection) else {
                    throw NSError(domain: NSPOSIXErrorDomain, code: Int(errno != 0 ? errno : EIO))
                }
            } else if effect.offset > 0, (Self.fileLength(part) ?? 0) < effect.offset {
                return .with {
                    $0.opID = effect.opID
                    $0.sessionID = effect.sessionID
                    $0.fileID = effect.fileID
                    $0.ok = false
                    $0.errorKind = .checksumMismatch
                    $0.errorDetail = "partial file truncated"
                }
            }

            let offset = max(0, effect.offset)
            let handle = try FileHandle(forUpdating: part)
            var closed = false
            defer { if !closed { try? handle.close() } }
            try handle.seek(toOffset: UInt64(offset))
            if !effect.data.isEmpty {
                try handle.write(contentsOf: effect.data)
            }
            try handle.truncate(atOffset: UInt64(offset + Int64(effect.data.count)))
            try handle.close()
            closed = true

            let meta = Meta(
                schemaVersion: Self.metaSchemaVersion,
                sessionId: effect.sessionID,
                fileId: effect.fileID,
                displayName: safeLocalSharedFileName(effect.displayName),
                totalSizeBytes: effect.totalSizeBytes,
                chunkBytes: effect.chunkBytes,
                committedOffset: effect.committedOffset,
                expectedSha256: effect.expectedSha256,
                durationMs: nil
            )
            try Self.writeMeta(meta, to: metaURL(key))
            return .with {
                $0.opID = effect.opID
                $0.sessionID = effect.sessionID
                $0.fileID = effect.fileID
                $0.ok = true
            }
        } catch {
            return .with {
                $0.opID = effect.opID
                $0.sessionID = effect.sessionID
                $0.fileID = effect.fileID
                $0.ok = false
                $0.errorKind = Self.ioErrorKind(for: error)
                $0.errorDetail = error.localizedDescription
            }
        }
    }

    /// Executes `SharedFileVerifyAndCommitEffect`: streams SHA-256 over the first `total_size_bytes` of `.part`
    /// and atomically commits to `file/<display_name>` (or removes `.part`/`.meta` on `deliver_only`).
    public func verifyAndCommit(_ effect: PBSharedFileVerifyAndCommitEffect) -> PBSharedFileVerifyAndCommitResultEvent {
        let key = SharedFileKey(sessionId: effect.sessionID, fileId: effect.fileID)
        let part = partURL(key)
        let metaFile = metaURL(key)
        let fm = FileManager.default

        func checksumFailure(_ detail: String) -> PBSharedFileVerifyAndCommitResultEvent {
            try? fm.removeItem(at: part)
            try? fm.removeItem(at: metaFile)
            return .with {
                $0.opID = effect.opID
                $0.sessionID = effect.sessionID
                $0.fileID = effect.fileID
                $0.ok = false
                $0.errorKind = .checksumMismatch
                $0.errorDetail = detail
            }
        }

        guard effect.totalSizeBytes >= 0, effect.expectedSha256.count == 32 else {
            return checksumFailure("invalid expected digest or size")
        }

        let digest: Data?
        if effect.totalSizeBytes == 0 {
            digest = Data(SHA256.hash(data: Data()))
        } else {
            digest = Self.sha256(of: part, bytes: effect.totalSizeBytes)
        }
        guard let digest, digest == effect.expectedSha256 else {
            return checksumFailure("sha256 mismatch")
        }

        if effect.deliverOnly {
            try? fm.removeItem(at: part)
            try? fm.removeItem(at: metaFile)
            return .with {
                $0.opID = effect.opID
                $0.sessionID = effect.sessionID
                $0.fileID = effect.fileID
                $0.ok = true
            }
        }

        let existingMeta = Self.readMeta(at: metaFile)
        let fileName = safeLocalSharedFileName(effect.displayName)
        let folder = fileFolderURL(key)
        let final = folder.appending(path: fileName, directoryHint: .notDirectory)

        do {
            try fm.createDirectory(at: folder, withIntermediateDirectories: true, attributes: Self.protection)
            if !fm.fileExists(atPath: part.path) {
                guard effect.totalSizeBytes == 0,
                      fm.createFile(atPath: part.path, contents: nil, attributes: Self.protection)
                else {
                    return checksumFailure("partial file missing")
                }
            } else if let partLen = Self.fileLength(part), partLen != effect.totalSizeBytes {
                let handle = try FileHandle(forUpdating: part)
                var closed = false
                defer { if !closed { try? handle.close() } }
                try handle.truncate(atOffset: UInt64(effect.totalSizeBytes))
                try handle.close()
                closed = true
            }

            if let stale = try? fm.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil) {
                for item in stale { try? fm.removeItem(at: item) }
            }
            try fm.moveItem(at: part, to: final)

            let meta = Meta(
                schemaVersion: Self.metaSchemaVersion,
                sessionId: effect.sessionID,
                fileId: effect.fileID,
                displayName: fileName,
                totalSizeBytes: effect.totalSizeBytes,
                chunkBytes: existingMeta?.chunkBytes ?? 0,
                committedOffset: effect.totalSizeBytes,
                expectedSha256: effect.expectedSha256,
                durationMs: existingMeta?.durationMs
            )
            try Self.writeMeta(meta, to: metaFile)

            return .with {
                $0.opID = effect.opID
                $0.sessionID = effect.sessionID
                $0.fileID = effect.fileID
                $0.ok = true
            }
        } catch {
            return .with {
                $0.opID = effect.opID
                $0.sessionID = effect.sessionID
                $0.fileID = effect.fileID
                $0.ok = false
                $0.errorKind = Self.ioErrorKind(for: error)
                $0.errorDetail = error.localizedDescription
            }
        }
    }

    /// Executes `SharedFileDeletePartialEffect`: removes `.part` and partial `.meta`, preserving any committed copy.
    public func deletePartial(sessionID: String, fileID: String) {
        let key = SharedFileKey(sessionId: sessionID, fileId: fileID)
        let fm = FileManager.default
        try? fm.removeItem(at: partURL(key))
        let hasCommitted = Self.firstCommittedFile(in: fileFolderURL(key), preferredName: nil) != nil
        if !hasCommitted {
            try? fm.removeItem(at: metaURL(key))
            let dir = directory(key)
            if let items = try? fm.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil), items.isEmpty {
                try? fm.removeItem(at: dir)
            }
        }
    }

    /// Executes `SharedFileDeleteLocalEffect`: removes the committed copy, `.part`, and `.meta`.
    public func deleteLocal(sessionID: String, fileID: String) {
        let key = SharedFileKey(sessionId: sessionID, fileId: fileID)
        try? FileManager.default.removeItem(at: directory(key))
    }

    /// Executes `SharedFileDeleteSessionEffect`: removes the entire session directory under `root`.
    public func deleteSession(sessionID: String) {
        try? FileManager.default.removeItem(at: root.appending(path: sharedFileFolderName(sessionID), directoryHint: .isDirectory))
    }

    /// Executes `SharedFileClearAllEffect`: empties `root`.
    public func clearAll() {
        let fm = FileManager.default
        if let folders = try? fm.contentsOfDirectory(at: root, includingPropertiesForKeys: nil) {
            for folder in folders { try? fm.removeItem(at: folder) }
        }
    }

    /// Persists probed video/audio duration (in ms) in `.meta` when a committed copy exists on disk.
    public func recordDuration(_ key: SharedFileKey, ms: Int64) {
        guard ms > 0 else { return }
        let metaFile = metaURL(key)
        guard Self.firstCommittedFile(in: fileFolderURL(key), preferredName: nil) != nil,
              var meta = Self.readMeta(at: metaFile),
              meta.schemaVersion == Self.metaSchemaVersion,
              meta.durationMs != ms
        else { return }
        meta.durationMs = ms
        try? Self.writeMeta(meta, to: metaFile)
    }

    // MARK: Platform Helpers

    public static func defaultAvailableCapacity(for url: URL) throws -> Int64? {
        let keys: Set<URLResourceKey> = [.volumeAvailableCapacityForImportantUsageKey, .volumeAvailableCapacityKey]
        let values = try url.resourceValues(forKeys: keys)
        if let important = values.volumeAvailableCapacityForImportantUsage {
            return important
        }
        if let standard = values.volumeAvailableCapacity {
            return Int64(standard)
        }
        return nil
    }

    public static func ioErrorKind(for error: Error) -> PBSharedFileIOErrorKind {
        isStorageFullError(error as NSError) ? .storageFull : .other
    }

    private static func isStorageFullError(_ error: NSError) -> Bool {
        if error.domain == NSCocoaErrorDomain && error.code == NSFileWriteOutOfSpaceError {
            return true
        }
        if error.domain == NSPOSIXErrorDomain && error.code == Int(ENOSPC) {
            return true
        }
        if let underlying = error.userInfo[NSUnderlyingErrorKey] as? NSError, isStorageFullError(underlying) {
            return true
        }
        if let multi = error.userInfo[NSMultipleUnderlyingErrorsKey] as? [NSError], multi.contains(where: isStorageFullError) {
            return true
        }
        return false
    }

    /// SHA-256 of the first `bytes` bytes of the file, streamed in 1 MiB pieces.
    static func sha256(of url: URL, bytes: Int64) -> Data? {
        guard bytes >= 0 else { return nil }
        if bytes == 0 { return Data(SHA256.hash(data: Data())) }
        guard let actual = fileLength(url), actual >= bytes,
              let handle = try? FileHandle(forReadingFrom: url)
        else { return nil }
        defer { try? handle.close() }
        var hash = SHA256()
        var left = bytes
        while left > 0 {
            guard let piece = try? handle.read(upToCount: Int(min(left, 1 << 20))), !piece.isEmpty else { return nil }
            hash.update(data: piece)
            left -= Int64(piece.count)
        }
        return Data(hash.finalize())
    }

    private static var protection: [FileAttributeKey: Any] {
        #if os(iOS)
        [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication]
        #else
        [:]
        #endif
    }

    private static var writeOptions: Data.WritingOptions {
        #if os(iOS)
        [.atomic, .completeFileProtectionUntilFirstUserAuthentication]
        #else
        [.atomic]
        #endif
    }

    private static func readMeta(at url: URL) -> Meta? {
        guard let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(Meta.self, from: data)
    }

    private static func writeMeta(_ meta: Meta, to url: URL) throws {
        try JSONEncoder().encode(meta).write(to: url, options: writeOptions)
    }

    private static func fileLength(_ url: URL) -> Int64? {
        (try? FileManager.default.attributesOfItem(atPath: url.path)[.size] as? NSNumber)?.int64Value
    }

    private static func firstCommittedFile(in folder: URL, preferredName: String?) -> URL? {
        let fm = FileManager.default
        if let name = preferredName, !name.isEmpty {
            let candidate = folder.appending(path: safeLocalSharedFileName(name), directoryHint: .notDirectory)
            var isDir: ObjCBool = false
            if fm.fileExists(atPath: candidate.path, isDirectory: &isDir), !isDir.boolValue {
                return candidate
            }
        }
        guard let items = try? fm.contentsOfDirectory(at: folder, includingPropertiesForKeys: [.isRegularFileKey], options: .skipsHiddenFiles) else {
            return nil
        }
        return items.first { url in
            (try? url.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true
        }
    }
}
