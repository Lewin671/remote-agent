// Pure platform file I/O for picked attachments: security-scoped bookmarks,
// NSFileCoordinator materialization, metadata inspection, streaming SHA-256
// hashing, and bounded chunk reads. Upload sequencing, retries, and gates live
// in Go clientcore.
import CryptoKit
import Foundation
import RAProto
import UniformTypeIdentifiers

/// Structured platform file I/O error mapped directly to Core's `PBAttachmentIOErrorKind`.
public struct AttachmentFileIOError: Error, Equatable, Sendable {
    public let kind: PBAttachmentIOErrorKind
    public let detail: String

    public init(_ kind: PBAttachmentIOErrorKind, detail: String = "") {
        self.kind = kind
        self.detail = detail
    }
}

/// Metadata inspected from a picked OS URL before sending it to Core's `DraftAddItemsAction`.
public struct PickedFileInspection: Sendable {
    public let url: URL
    public let name: String
    public let mimeType: String
    public let sizeBytes: Int64
    public let sizeUnknown: Bool
    public let isDirectory: Bool
    public let bookmark: Data?

    public init(
        url: URL,
        name: String,
        mimeType: String,
        sizeBytes: Int64,
        sizeUnknown: Bool,
        isDirectory: Bool,
        bookmark: Data?
    ) {
        self.url = url
        self.name = name
        self.mimeType = mimeType
        self.sizeBytes = sizeBytes
        self.sizeUnknown = sizeUnknown
        self.isDirectory = isDirectory
        self.bookmark = bookmark
    }

    public func candidate(forceFile: Bool) -> PBAttachmentCandidateInput {
        .with {
            $0.name = name
            $0.mimeType = mimeType
            $0.sizeBytes = sizeBytes
            $0.sizeUnknown = sizeUnknown
            $0.isDirectory = isDirectory
            $0.forceFile = forceFile
        }
    }
}

/// Pure platform I/O helpers for security-scoped picked files and local videos.
public enum PickedFile {
    public static func bookmark(for url: URL) throws -> Data {
        #if os(macOS)
        return try url.bookmarkData(options: .withSecurityScope, includingResourceValuesForKeys: nil, relativeTo: nil)
        #else
        return try url.bookmarkData(options: .minimalBookmark, includingResourceValuesForKeys: nil, relativeTo: nil)
        #endif
    }

    public static func resolve(_ bookmark: Data) throws -> (url: URL, stale: Bool) {
        var stale = false
        #if os(macOS)
        let url = try URL(
            resolvingBookmarkData: bookmark,
            options: [.withSecurityScope, .withoutUI],
            relativeTo: nil,
            bookmarkDataIsStale: &stale
        )
        #else
        let url = try URL(
            resolvingBookmarkData: bookmark,
            options: .withoutUI,
            relativeTo: nil,
            bookmarkDataIsStale: &stale
        )
        #endif
        return (url, stale)
    }

    /// Coordinates reads so document providers materialize evicted documents before opening.
    public static func coordinated<T>(_ url: URL, _ work: (URL) throws -> T) throws -> T {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        let coordinator = NSFileCoordinator(filePresenter: nil)
        var coordinationError: NSError?
        var result: Result<T, Error>?
        coordinator.coordinate(readingItemAt: url, options: [], error: &coordinationError) { readable in
            result = Result {
                try Task.checkCancellation()
                return try work(readable)
            }
        }
        if let result {
            do { return try result.get() }
            catch is CancellationError { throw CancellationError() }
            catch let error as AttachmentFileIOError { throw error }
            catch {
                throw AttachmentFileIOError(
                    attachmentIOErrorKind(for: error) == .other ? .sourceMissing : attachmentIOErrorKind(for: error),
                    detail: error.localizedDescription
                )
            }
        }
        throw AttachmentFileIOError(.sourceMissing, detail: coordinationError?.localizedDescription ?? "")
    }

    /// Reads metadata and byte length via `seekToEnd` without loading contents into memory.
    public static func inspect(url: URL) throws -> PickedFileInspection {
        try coordinated(url) { readable in
            let values = try readable.resourceValues(forKeys: [.isRegularFileKey, .isDirectoryKey, .isPackageKey, .contentTypeKey])
            let mime = values.contentType?.preferredMIMEType ?? ""
            if values.isDirectory == true || values.isPackage == true {
                return PickedFileInspection(
                    url: url,
                    name: url.lastPathComponent,
                    mimeType: mime,
                    sizeBytes: 0,
                    sizeUnknown: false,
                    isDirectory: true,
                    bookmark: nil
                )
            }
            guard values.isRegularFile == true else {
                throw AttachmentFileIOError(.sourceMissing)
            }
            let handle = try FileHandle(forReadingFrom: readable)
            defer { try? handle.close() }
            let size = try handle.seekToEnd()
            let bookmark = try Self.bookmark(for: url)
            return PickedFileInspection(
                url: url,
                name: url.lastPathComponent,
                mimeType: mime,
                sizeBytes: Int64(size),
                sizeUnknown: false,
                isDirectory: false,
                bookmark: bookmark
            )
        }
    }

    /// Streams `url` in bounded `768 KiB` chunks to compute its exact size and 32-byte SHA-256.
    /// Zero-byte regular files are valid. Directories and files exceeding `maxBytes` fail with structured kinds.
    public static func hashFile(at url: URL, maxBytes: Int64) throws -> (sizeBytes: Int64, sha256: Data) {
        try coordinated(url) { readable in
            let values = try readable.resourceValues(forKeys: [.isRegularFileKey, .isDirectoryKey, .isPackageKey])
            if values.isDirectory == true || values.isPackage == true {
                throw AttachmentFileIOError(.directory)
            }
            guard values.isRegularFile == true else {
                throw AttachmentFileIOError(.sourceMissing)
            }
            let handle = try FileHandle(forReadingFrom: readable)
            defer { try? handle.close() }
            let total = Int64(try handle.seekToEnd())
            if maxBytes > 0 && total > maxBytes {
                throw AttachmentFileIOError(.fileTooLarge)
            }
            try handle.seek(toOffset: 0)
            var hasher = SHA256()
            var hashed: Int64 = 0
            while true {
                let more = try autoreleasepool {
                    try Task.checkCancellation()
                    let chunk = try handle.read(upToCount: defaultChunkBytes) ?? Data()
                    try Task.checkCancellation()
                    if chunk.isEmpty { return false }
                    hashed += Int64(chunk.count)
                    if maxBytes > 0 && hashed > maxBytes {
                        throw AttachmentFileIOError(.fileTooLarge)
                    }
                    hasher.update(data: chunk)
                    return true
                }
                if !more { break }
            }
            return (hashed, Data(hasher.finalize()))
        }
    }

    /// Resolves a bookmark (refreshing it if stale) and computes its streaming SHA-256 and exact byte size.
    public static func hashBookmark(
        _ bookmark: Data,
        maxBytes: Int64,
        resolve: (Data) throws -> (url: URL, stale: Bool) = Self.resolve
    ) throws -> (url: URL, sizeBytes: Int64, sha256: Data, refreshedBookmark: Data?) {
        guard !bookmark.isEmpty else { throw AttachmentFileIOError(.sourceMissing) }
        let resolved: (url: URL, stale: Bool)
        do {
            resolved = try resolve(bookmark)
        } catch {
            throw AttachmentFileIOError(.sourceMissing, detail: error.localizedDescription)
        }
        var refreshed: Data?
        let (size, digest) = try coordinated(resolved.url) { readable in
            if resolved.stale {
                refreshed = try? Self.bookmark(for: readable)
            }
            let values = try readable.resourceValues(forKeys: [.isRegularFileKey, .isDirectoryKey, .isPackageKey])
            if values.isDirectory == true || values.isPackage == true {
                throw AttachmentFileIOError(.directory)
            }
            guard values.isRegularFile == true else {
                throw AttachmentFileIOError(.sourceMissing)
            }
            let handle = try FileHandle(forReadingFrom: readable)
            defer { try? handle.close() }
            let total = Int64(try handle.seekToEnd())
            if maxBytes > 0 && total > maxBytes {
                throw AttachmentFileIOError(.fileTooLarge)
            }
            try handle.seek(toOffset: 0)
            var hasher = SHA256()
            var hashed: Int64 = 0
            while true {
                let more = try autoreleasepool {
                    try Task.checkCancellation()
                    let chunk = try handle.read(upToCount: defaultChunkBytes) ?? Data()
                    try Task.checkCancellation()
                    if chunk.isEmpty { return false }
                    hashed += Int64(chunk.count)
                    if maxBytes > 0 && hashed > maxBytes {
                        throw AttachmentFileIOError(.fileTooLarge)
                    }
                    hasher.update(data: chunk)
                    return true
                }
                if !more { break }
            }
            return (hashed, Data(hasher.finalize()))
        }
        return (resolved.url, size, digest, refreshed)
    }

    /// Reads an exact `[offset, offset + length)` byte slice from `url` without loading the whole file,
    /// verifying that the current length still equals `expectedSizeBytes`.
    public static func readChunk(at url: URL, offset: Int64, length: Int, expectedSizeBytes: Int64) throws -> Data {
        guard offset >= 0, length >= 0, expectedSizeBytes >= 0, offset + Int64(length) <= expectedSizeBytes else {
            throw AttachmentFileIOError(.sourceChanged)
        }
        return try coordinated(url) { readable in
            let values = try readable.resourceValues(forKeys: [.isRegularFileKey])
            guard values.isRegularFile == true else { throw AttachmentFileIOError(.sourceMissing) }
            let handle = try FileHandle(forReadingFrom: readable)
            defer { try? handle.close() }
            let currentSize = Int64(try handle.seekToEnd())
            guard currentSize == expectedSizeBytes else {
                throw AttachmentFileIOError(.sourceChanged)
            }
            if length == 0 { return Data() }
            try handle.seek(toOffset: UInt64(offset))
            let data = try autoreleasepool {
                try Task.checkCancellation()
                return try handle.read(upToCount: length) ?? Data()
            }
            guard data.count == length else {
                throw AttachmentFileIOError(.sourceChanged)
            }
            return data
        }
    }

    /// Resolves `bookmark` and reads an exact `[offset, offset + length)` byte slice.
    public static func readBookmarkChunk(
        _ bookmark: Data,
        offset: Int64,
        length: Int,
        expectedSizeBytes: Int64,
        resolve: (Data) throws -> (url: URL, stale: Bool) = Self.resolve
    ) throws -> Data {
        guard !bookmark.isEmpty else { throw AttachmentFileIOError(.sourceMissing) }
        let url: URL
        do {
            url = try resolve(bookmark).url
        } catch {
            throw AttachmentFileIOError(.sourceMissing, detail: error.localizedDescription)
        }
        return try readChunk(at: url, offset: offset, length: length, expectedSizeBytes: expectedSizeBytes)
    }
}
