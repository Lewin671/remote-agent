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
        if line.hasPrefix("File: @"), line.hasSuffix(" (file)") {
            path = String(line.dropFirst(6).dropLast(7))
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

/// The picker grants access to the original document. Only the bookmark and metadata
/// enter the encrypted draft; neither a plaintext copy nor a 100 MiB Data is kept.
public struct PickedFile: Codable, Equatable, Sendable {
    public var bookmark: Data
    public var name: String
    public var size: Int64

    public init(url: URL, maxBytes: Int64 = maxFileBytes) throws {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        let values = try url.resourceValues(forKeys: [.isRegularFileKey])
        guard values.isRegularFile == true else { throw Self.unreadable }
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        let size = try handle.seekToEnd()
        guard size <= UInt64(maxBytes) else { throw UserError("文件太大，单个文件最多 \(fileSize(maxBytes))") }
        guard !url.lastPathComponent.isEmpty, url.lastPathComponent.utf8.count <= 255 else {
            throw UserError("文件名不能超过 255 字节，请重命名后选择")
        }
        self.name = url.lastPathComponent
        self.size = Int64(size)
        #if os(macOS)
        bookmark = try url.bookmarkData(options: .withSecurityScope, includingResourceValuesForKeys: nil, relativeTo: nil)
        #else
        bookmark = try url.bookmarkData(options: .minimalBookmark, includingResourceValuesForKeys: nil, relativeTo: nil)
        #endif
    }

    /// Metadata from another client's queue has no local grant; its chip asks for a new pick.
    public init(remote file: PBSentFile) {
        bookmark = Data()
        name = file.name
        size = file.sizeBytes
    }

    public static var unreadable: UserError { UserError("读不到源文件，请移除后重新选择") }

    private func access<T>(_ work: (FileHandle) throws -> T) throws -> T {
        do {
            var stale = false
            #if os(macOS)
            let url = try URL(resolvingBookmarkData: bookmark, options: [.withSecurityScope, .withoutUI], relativeTo: nil, bookmarkDataIsStale: &stale)
            #else
            let url = try URL(resolvingBookmarkData: bookmark, options: .withoutUI, relativeTo: nil, bookmarkDataIsStale: &stale)
            #endif
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            let handle = try FileHandle(forReadingFrom: url)
            defer { try? handle.close() }
            guard try handle.seekToEnd() == UInt64(size) else { throw Self.unreadable }
            try handle.seek(toOffset: 0)
            return try work(handle)
        } catch is CancellationError { throw CancellationError() }
        catch { throw Self.unreadable }
    }

    public func digest() throws -> Data {
        try access { handle in
            var hash = SHA256()
            while let data = try handle.read(upToCount: defaultChunkBytes), !data.isEmpty {
                try Task.checkCancellation()
                hash.update(data: data)
            }
            return Data(hash.finalize())
        }
    }

    public func chunk(offset: Int64, count: Int) throws -> Data {
        try access { handle in
            try handle.seek(toOffset: UInt64(offset))
            let data = try handle.read(upToCount: count) ?? Data()
            guard data.count == count else { throw Self.unreadable }
            return data
        }
    }
}

extension UploadChannel {
    public func beginFile(transferId: String, name: String, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
        throw UserError("这台电脑不支持发送文件")
    }
}

/// Hash and read on a worker; at most one chunk is resident, including during resume.
public func uploadFile(
    isolation: isolated (any Actor)? = #isolation,
    _ channel: some UploadChannel, transferId: String, source: PickedFile,
    onProgress: (Int64, Int64) -> Void = { _, _ in }
) async throws -> UploadedImage {
    let digest = try await Task.detached { try source.digest() }.value
    try Task.checkCancellation()
    var retries = 0
    func again(_ error: Error) -> Bool {
        guard let failure = error as? any UploadFailure,
            failure.unknown || failure.code == "network", retries < 3 else { return false }
        retries += 1
        return true
    }
    var status: PBImageUploadStatus
    while true {
        do {
            status = try await channel.beginFile(transferId: transferId, name: source.name, total: source.size, sha256: digest)
            break
        } catch where again(error) {}
    }
    let chunkSize = min(defaultChunkBytes, status.maxChunkBytes > 0 ? Int(status.maxChunkBytes) : defaultChunkBytes)
    while status.state == .receiving && status.persistedBytes < source.size {
        try Task.checkCancellation()
        let offset = status.persistedBytes
        guard offset >= 0 else { throw UserError("电脑报告的上传进度不对") }
        let count = Int(min(Int64(chunkSize), source.size - offset))
        let data = try await Task.detached { try source.chunk(offset: offset, count: count) }.value
        do {
            status = try await channel.put(transferId: transferId, offset: offset, data: data)
            guard status.persistedBytes > offset else { throw UserError("电脑报告的上传进度不对") }
        } catch where again(error) {
            status = try await channel.status(transferId: transferId)
        }
        onProgress(status.persistedBytes, source.size)
    }
    guard status.persistedBytes >= 0, status.persistedBytes <= source.size else { throw UserError("电脑报告的上传进度不对") }
    while status.state == .receiving {
        try Task.checkCancellation()
        do {
            status = try await channel.complete(transferId: transferId)
            guard status.state == .complete else { throw UserError("电脑没有确认文件上传完成，请重试") }
        }
        catch where again(error) { status = try await channel.status(transferId: transferId) }
    }
    guard status.state == .complete, !status.fileID.isEmpty else { throw UserError("电脑没有给出文件 ID") }
    onProgress(source.size, source.size)
    return UploadedImage(imageId: status.fileID, expiresMs: status.expiresMs)
}
