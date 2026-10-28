// Arbitrary files share the image transfer queue, but never its byte store.
import CryptoKit
import Foundation
import RAProto

public let maxFiles = 6
public let maxFileBytes: Int64 = 100 * 1024 * 1024

public func fileSupported(_ machine: MachineView?) -> Bool { capabilities(machine)?.fileUpload == true }
public func fileLimit(for machine: MachineView?) -> Int {
    let n = Int(capabilities(machine)?.maxFilesPerMessage ?? 0)
    return n > 0 ? min(n, maxFiles) : maxFiles
}
public func fileByteLimit(for machine: MachineView?) -> Int64 {
    let n = capabilities(machine)?.maxFileUploadBytes ?? 0
    return n > 0 ? min(n, maxFileBytes) : maxFileBytes
}
public func fileUploadBlocked(_ machine: MachineView?, online: Bool) -> String? {
    guard let machine else { return "先选一台电脑" }
    guard fileSupported(machine) else { return "这台电脑不支持发送文件" }
    return machine.online && online ? nil : "电脑离线，连上后才能添加文件"
}
public func fileSize(_ bytes: Int64) -> String {
    if bytes < 1024 { return "\(bytes) B" }
    return String(format: "%.1f %@", Double(bytes) / (bytes < 1024 * 1024 ? 1024 : 1024 * 1024), bytes < 1024 * 1024 ? "KB" : "MB")
}

/// Middle truncation keeps a filename's extension, including Unicode names.
public func fileChipName(_ name: String, maxCharacters: Int = 40) -> String {
    guard name.count > maxCharacters else { return name }
    let room = max(2, maxCharacters - 1)
    let tail = min(room - 1, max(room / 2, (name as NSString).pathExtension.count + 1))
    return String(name.prefix(room - tail)) + "…" + String(name.suffix(tail))
}

/// Only whole reference lines attested by this event may be hidden.
public func withoutFileLines(_ text: String, files: [PBSentFile]) -> String {
    let paths = Set(files.map(\.path).filter { !$0.isEmpty })
    let lines = text.components(separatedBy: "\n")
    var hidden = Set<Int>()
    for (i, line) in lines.enumerated() {
        let path: String
        if line.hasPrefix("File: @\""), line.hasSuffix("\" (file)") {
            path = "@" + String(line.dropFirst(8).dropLast(8))

        } else if line.hasPrefix("File: ") {
            path = String(line.dropFirst(6))
        } else { path = line }
        if paths.contains(path) || (path.hasPrefix("@") && paths.contains(String(path.dropFirst()))) {
            hidden.insert(i)
            if i > 0 && lines[i - 1].isEmpty { hidden.insert(i - 1) }
        }
    }
    return lines.enumerated().filter { !hidden.contains($0.offset) }.map(\.element).joined(separator: "\n")
}

/// One durable transfer per target. Switching machines does not abandon reserved quota.
public struct FileTransfer: Codable, Equatable, Sendable {
    public var transferId: String
    public var fileId: String?
    public var expiresMs: Int64 = 0
    public init(transferId: String = newRequestId(), fileId: String? = nil, expiresMs: Int64 = 0) {
        self.transferId = transferId; self.fileId = fileId; self.expiresMs = expiresMs
    }
}

public struct PickedFile: Codable, Equatable, Sendable {
    public var bookmark: Data
    public var name: String
    public var size: Int64
    public var machineId: String?
    public var transfers: [String: FileTransfer] = [:]
    public var hasSource: Bool { !bookmark.isEmpty }

    static func bookmark(for url: URL) throws -> Data {
        #if os(macOS)
        return try url.bookmarkData(options: .withSecurityScope, includingResourceValuesForKeys: nil, relativeTo: nil)
        #else
        return try url.bookmarkData(options: .minimalBookmark, includingResourceValuesForKeys: nil, relativeTo: nil)
        #endif
    }
    static func resolve(_ bookmark: Data) throws -> (url: URL, stale: Bool) {
        var stale = false
        #if os(macOS)
        let url = try URL(resolvingBookmarkData: bookmark, options: [.withSecurityScope, .withoutUI], relativeTo: nil, bookmarkDataIsStale: &stale)
        #else
        let url = try URL(resolvingBookmarkData: bookmark, options: .withoutUI, relativeTo: nil, bookmarkDataIsStale: &stale)
        #endif
        return (url, stale)
    }
    /// The coordinator asks document providers to materialize evicted documents before opening them.
    private static func coordinated<T>(_ url: URL, _ work: (URL) throws -> T) throws -> T {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        let coordinator = NSFileCoordinator(filePresenter: nil)
        var coordinationError: NSError?
        var result: Result<T, Error>?
        coordinator.coordinate(readingItemAt: url, options: [], error: &coordinationError) { readable in
            result = Result { try Task.checkCancellation(); return try work(readable) }
        }
        if let result {
            do { return try result.get() }
            catch is CancellationError { throw CancellationError() }
            catch let error as UserError { throw error }
            catch { throw unreadable }
        }
        throw unreadable
    }
    public init(url: URL, maxBytes: Int64 = maxFileBytes) throws {
        self = try Self.coordinated(url) { readable in
            let values = try readable.resourceValues(forKeys: [.isRegularFileKey, .isDirectoryKey, .isPackageKey])
            if values.isDirectory == true || values.isPackage == true {
                throw UserError("文件夹不能直接发送，请压缩后选择")
            }
            guard values.isRegularFile == true else { throw Self.unreadable }
            let handle = try FileHandle(forReadingFrom: readable)
            defer { try? handle.close() }
            let size = try handle.seekToEnd()
            guard maxBytes >= 0, size <= UInt64(maxBytes) else { throw UserError("文件太大，单个文件最多 \(fileSize(maxBytes))") }
            guard !url.lastPathComponent.isEmpty, url.lastPathComponent.utf8.count <= 255 else {
                throw UserError("文件名不能超过 255 字节，请重命名后选择")
            }
            return PickedFile(bookmark: try Self.bookmark(for: url), name: url.lastPathComponent, size: Int64(size))
        }
    }
    init(bookmark: Data, name: String, size: Int64) {
        self.bookmark = bookmark; self.name = name; self.size = size
    }
    public init(remote file: PBSentFile) {
        bookmark = Data(); name = file.name; size = file.sizeBytes
    }
    public static var unreadable: UserError { UserError("读不到源文件，请移除后重新选择") }
    public static var modified: UserError { UserError("文件在选择后被修改，请重新选择") }

    /// The refreshed grant is returned to the draft and persisted before starting network work.
    func refreshed(resolve: (Data) throws -> (url: URL, stale: Bool) = Self.resolve) throws -> PickedFile {
        guard hasSource else { throw Self.unreadable }
        let resolved = try resolve(bookmark)
        guard resolved.stale else { return self }
        return try Self.coordinated(resolved.url) { url in
            var copy = self
            copy.bookmark = try Self.bookmark(for: url)
            return copy
        }
    }
    private func access<T>(_ work: (FileHandle) throws -> T) throws -> T {
        do {
            guard hasSource, size >= 0 else { throw Self.unreadable }
            return try Self.coordinated(Self.resolve(bookmark).url) { url in
                let handle = try FileHandle(forReadingFrom: url)
                defer { try? handle.close() }
                guard try handle.seekToEnd() == UInt64(size) else { throw Self.modified }
                try handle.seek(toOffset: 0)
                return try work(handle)
            }
        } catch is CancellationError { throw CancellationError() }
        catch let error as UserError { throw error }
        catch { throw Self.unreadable }
    }
    public func digest() throws -> Data {
        try access { handle in
            var hash = SHA256()
            while true {
                let more = try autoreleasepool {
                    try Task.checkCancellation()
                    let data = try handle.read(upToCount: defaultChunkBytes) ?? Data()
                    try Task.checkCancellation()
                    hash.update(data: data)
                    return !data.isEmpty
                }
                if !more { break }
            }
            return Data(hash.finalize())
        }
    }
    public func chunk(offset: Int64, count: Int) throws -> Data {
        try access { handle in
            try autoreleasepool {
                try Task.checkCancellation()
                try handle.seek(toOffset: UInt64(offset))
                let data = try handle.read(upToCount: count) ?? Data()
                try Task.checkCancellation()
                guard data.count == count else { throw Self.modified }
                return data
            }
        }
    }
}

/// A worker remains cancelable while the caller awaits it (detached tasks alone do not inherit cancellation).
func fileWork<T: Sendable>(_ work: @escaping @Sendable () throws -> T) async throws -> T {
    let worker = Task.detached { try Task.checkCancellation(); return try work() }
    return try await withTaskCancellationHandler {
        let result = try await worker.value
        try Task.checkCancellation()
        return result
    } onCancel: { worker.cancel() }
}

actor FileUploadGate {
    static let shared = FileUploadGate()
    private var busy = false
    private var waiting: [(UUID, CheckedContinuation<Bool, Never>)] = []
    func acquire() async throws {
        try Task.checkCancellation()
        if !busy { busy = true; return }
        let id = UUID()
        let acquired = await withTaskCancellationHandler {
            await withCheckedContinuation { continuation in
                if Task.isCancelled { continuation.resume(returning: false) }
                else { waiting.append((id, continuation)) }
            }
        } onCancel: { Task { await self.cancel(id) } }
        guard acquired else { throw CancellationError() }
        if Task.isCancelled { release(); throw CancellationError() }
    }
    private func cancel(_ id: UUID) {
        guard let i = waiting.firstIndex(where: { $0.0 == id }) else { return }
        waiting.remove(at: i).1.resume(returning: false)
    }
    func release() {
        if waiting.isEmpty { busy = false }
        else { waiting.removeFirst().1.resume(returning: true) }
    }
}

public func fileUploadError(_ error: Error) -> String {
    guard let failure = error as? any UploadFailure else {
        return error is UserError ? errorText(error) : errorText(PickedFile.unreadable)
    }
    switch failure.code {
    case "already_exists": return "文件在选择后被修改，请重新选择"
    case "not_found": return "电脑上的文件已过期，请重试上传"
    case "resource_exhausted": return "电脑的附件存储空间不足，请清理后重试"
    case "unavailable", "network", CommandError.unknownOutcome: return "没能连上电脑，请重试上传"
    case "invalid_argument", "data_loss": return "文件校验失败，文件可能已被修改，请重新选择"
    case "permission_denied", "unauthenticated": return "没有上传文件的权限，请检查设备授权"
    default: return "文件上传失败，请重试或重新选择"
    }
}

/// One file at a time across every draft and ImageUploads instance.
public func uploadFile(
    isolation: isolated (any Actor)? = #isolation,
    _ channel: some UploadChannel, transferId: String, source: PickedFile,
    onSource: (PickedFile) async throws -> Void = { _ in },
    onProgress: (Int64, Int64) -> Void = { _, _ in }
) async throws -> UploadedImage {
    try await FileUploadGate.shared.acquire()
    do {
        let refreshed = try await fileWork { try source.refreshed() }
        try await onSource(refreshed)
        let digest = try await fileWork { try refreshed.digest() }
        try Task.checkCancellation()
        var retries = 0
        func again(_ error: Error) -> Bool {
            guard !Task.isCancelled, let failure = error as? any UploadFailure,
                failure.unknown || failure.code == "network", retries < 3 else { return false }
            retries += 1
            return true
        }
        var status: PBImageUploadStatus
        while true {
            try Task.checkCancellation()
            do {
                status = try await channel.beginFile(transferId: transferId, name: refreshed.name, total: refreshed.size, sha256: digest)
                break
            } catch where again(error) {}
        }
        let chunkSize = min(defaultChunkBytes, status.maxChunkBytes > 0 ? Int(status.maxChunkBytes) : defaultChunkBytes)
        while status.state == .receiving && status.persistedBytes < refreshed.size {
            try Task.checkCancellation()
            let offset = status.persistedBytes
            guard offset >= 0 else { throw UserError("电脑报告的上传进度不对") }
            let count = Int(min(Int64(chunkSize), refreshed.size - offset))
            let data = try await fileWork { try refreshed.chunk(offset: offset, count: count) }
            try Task.checkCancellation()
            do {
                status = try await channel.put(transferId: transferId, offset: offset, data: data)
                guard status.persistedBytes > offset else { throw UserError("电脑报告的上传进度不对") }
            } catch where again(error) { status = try await channel.status(transferId: transferId) }
            onProgress(status.persistedBytes, refreshed.size)
        }
        guard status.persistedBytes >= 0, status.persistedBytes <= refreshed.size else { throw UserError("电脑报告的上传进度不对") }
        // RECEIVING with all bytes persisted still needs Complete, including after an unknown Complete response.
        while status.state == .receiving {
            try Task.checkCancellation()
            do {
                status = try await channel.complete(transferId: transferId)
                guard status.state == .complete else { throw UserError("电脑没有确认文件上传完成，请重试") }
            } catch where again(error) { status = try await channel.status(transferId: transferId) }
        }
        try Task.checkCancellation()
        guard status.state == .complete, !status.fileID.isEmpty else { throw UserError("电脑没有给出文件 ID") }
        onProgress(refreshed.size, refreshed.size)
        await FileUploadGate.shared.release()
        return UploadedImage(imageId: status.fileID, expiresMs: status.expiresMs)
    } catch {
        await FileUploadGate.shared.release()
        throw error
    }
}
