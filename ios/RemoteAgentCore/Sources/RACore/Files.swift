// Arbitrary files share the image transfer queue, but never its byte store.
import CryptoKit
import Foundation
import RAProto
import UniformTypeIdentifiers

public var fileCapabilityUpdate: String { L10n.attachmentIosThisComputerHasNotDeclaredFileUploadSupportUpdate() }

/// Core-supplied attachment capabilities for a session or new-session composer.
public struct AttachmentCapabilities: Equatable, Sendable {
    public var capabilitiesLoaded: Bool
    public var supportsImages: Bool
    public var supportsFiles: Bool
    public var maxImages: Int
    public var maxFiles: Int
    public var maxFileBytes: Int64
    public var maxFileNameBytes: Int
    public var maxFileSizeText: String
    public var originalImageMaxBytes: Int64
    public var galleryAllowsVideo: Bool
    public var imageUploadBlocked: String?
    public var fileUploadBlocked: String?
    public var originalImageHint: String
    public var fileMetadataNotice: String

    public init(
        capabilitiesLoaded: Bool = false,
        supportsImages: Bool = false,
        supportsFiles: Bool = false,
        maxImages: Int = 0,
        maxFiles: Int = 0,
        maxFileBytes: Int64 = 0,
        maxFileNameBytes: Int = 0,
        maxFileSizeText: String = "",
        originalImageMaxBytes: Int64 = 0,
        galleryAllowsVideo: Bool = false,
        imageUploadBlocked: String? = nil,
        fileUploadBlocked: String? = nil,
        originalImageHint: String = "",
        fileMetadataNotice: String = ""
    ) {
        self.capabilitiesLoaded = capabilitiesLoaded
        self.supportsImages = supportsImages
        self.supportsFiles = supportsFiles
        self.maxImages = maxImages
        self.maxFiles = maxFiles
        self.maxFileBytes = maxFileBytes
        self.maxFileNameBytes = maxFileNameBytes
        self.maxFileSizeText = maxFileSizeText
        self.originalImageMaxBytes = originalImageMaxBytes
        self.galleryAllowsVideo = galleryAllowsVideo
        self.imageUploadBlocked = imageUploadBlocked
        self.fileUploadBlocked = fileUploadBlocked
        self.originalImageHint = originalImageHint
        self.fileMetadataNotice = fileMetadataNotice
    }

    public init(_ caps: PBSessionCapabilitiesView) {
        let imgBlocked = caps.hasImageUploadBlockedReason ? caps.imageUploadBlockedReason.rendered() : ""
        let fileBlocked = caps.hasFileUploadBlockedReason ? caps.fileUploadBlockedReason.rendered() : ""
        self.init(
            capabilitiesLoaded: caps.capabilitiesLoaded,
            supportsImages: caps.supportsImages,
            supportsFiles: caps.supportsFiles,
            maxImages: Int(caps.maxImages),
            maxFiles: Int(caps.maxFiles),
            maxFileBytes: caps.maxFileBytes,
            maxFileNameBytes: Int(caps.maxFileNameBytes),
            maxFileSizeText: caps.maxFileSizeText,
            originalImageMaxBytes: caps.originalImageMaxBytes,
            galleryAllowsVideo: caps.galleryAllowsVideo,
            imageUploadBlocked: imgBlocked.isEmpty ? nil : imgBlocked,
            fileUploadBlocked: fileBlocked.isEmpty ? nil : fileBlocked,
            originalImageHint: caps.hasOriginalImageHint ? caps.originalImageHint.rendered() : "",
            fileMetadataNotice: caps.hasFileMetadataNotice ? caps.fileMetadataNotice.rendered() : ""
        )
    }

    public init(_ vm: PBCoreNewSessionViewModel) {
        let imgBlocked = vm.hasImageUploadBlockedReason ? vm.imageUploadBlockedReason.rendered() : ""
        let fileBlocked = vm.hasFileUploadBlockedReason ? vm.fileUploadBlockedReason.rendered() : ""
        self.init(
            capabilitiesLoaded: vm.capabilitiesLoaded,
            supportsImages: vm.supportsImages,
            supportsFiles: vm.supportsFiles,
            maxImages: Int(vm.maxImages),
            maxFiles: Int(vm.maxFiles),
            maxFileBytes: vm.maxFileBytes,
            maxFileNameBytes: Int(vm.maxFileNameBytes),
            maxFileSizeText: vm.maxFileSizeText,
            originalImageMaxBytes: vm.originalImageMaxBytes,
            galleryAllowsVideo: vm.galleryAllowsVideo,
            imageUploadBlocked: imgBlocked.isEmpty ? nil : imgBlocked,
            fileUploadBlocked: fileBlocked.isEmpty ? nil : fileBlocked,
            originalImageHint: vm.hasOriginalImageHint ? vm.originalImageHint.rendered() : "",
            fileMetadataNotice: vm.hasFileMetadataNotice ? vm.fileMetadataNotice.rendered() : ""
        )
    }
}

/// Inspect the immutable command, including already prepared retries.
func requiresFileCapability(_ command: PBCommand) -> Bool {
    switch command.kind {
    case .send(let request): return !request.fileIds.isEmpty
    case .createSession(let request): return !request.fileIds.isEmpty
    case .beginImageUpload(let request): return !request.fileName.isEmpty
    default: return false
    }
}

extension Remote {
    /// Current advertised metadata is the only capability authority. No metadata
    /// means no file permission; never downgrade a file request to text.
    nonisolated func requireFileCapability(machineId: String, hasFiles: Bool) throws {
        if hasFiles && capabilities(state.value.machines[machineId])?.fileUpload != true {
            throw CommandError("file_upload_unsupported", fileCapabilityUpdate)
        }
    }
}

/// Metadata inspected from a picked OS URL before sending it to Core's `AttachmentEval`.
public struct PickedFileInspection: Sendable {
    public let url: URL
    public let name: String
    public let mimeType: String
    public let sizeBytes: Int64
    public let sizeUnknown: Bool
    public let isDirectory: Bool
    public let bookmark: Data?

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

    public func makePickedFile(displayName: String? = nil, formattedSize: String? = nil) throws -> PickedFile {
        guard let bookmark, !isDirectory, !sizeUnknown, sizeBytes >= 0 else { throw PickedFile.unreadable }
        return PickedFile(
            bookmark: bookmark,
            name: name,
            size: sizeBytes,
            displayName: displayName,
            formattedSize: formattedSize
        )
    }
}

/// Durable metadata for resuming a current transfer and retiring superseded reservations.
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
    public var displayName: String?
    public var formattedSize: String?
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
    public static func resolve(_ bookmark: Data) throws -> (url: URL, stale: Bool) {
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
    /// Reads metadata and byte length (via seekToEnd, without loading contents) for Core `AttachmentEval`.
    public static func inspect(url: URL) throws -> PickedFileInspection {
        try Self.coordinated(url) { readable in
            let values = try readable.resourceValues(forKeys: [.isRegularFileKey, .isDirectoryKey, .isPackageKey, .contentTypeKey])
            let mime = values.contentType?.preferredMIMEType ?? ""
            if values.isDirectory == true || values.isPackage == true {
                return PickedFileInspection(
                    url: url, name: url.lastPathComponent, mimeType: mime,
                    sizeBytes: 0, sizeUnknown: false, isDirectory: true, bookmark: nil
                )
            }
            guard values.isRegularFile == true else { throw Self.unreadable }
            let handle = try FileHandle(forReadingFrom: readable)
            defer { try? handle.close() }
            let size = try handle.seekToEnd()
            let bookmark = try Self.bookmark(for: url)
            return PickedFileInspection(
                url: url, name: url.lastPathComponent, mimeType: mime,
                sizeBytes: Int64(size), sizeUnknown: false, isDirectory: false, bookmark: bookmark
            )
        }
    }
    public init(url: URL, displayName: String? = nil, formattedSize: String? = nil) throws {
        self = try Self.inspect(url: url).makePickedFile(displayName: displayName, formattedSize: formattedSize)
    }
    init(bookmark: Data, name: String, size: Int64, displayName: String? = nil, formattedSize: String? = nil) {
        self.bookmark = bookmark
        self.name = name
        self.size = size
        self.displayName = displayName
        self.formattedSize = formattedSize
    }
    public init(remote file: PBSentFile, displayName: String? = nil, formattedSize: String? = nil) {
        bookmark = Data()
        name = file.name
        size = file.sizeBytes
        self.displayName = displayName
        self.formattedSize = formattedSize
    }
    public static var unreadable: UserError {
        UserError(L10nMessage.attachmentIosCannotReadTheSourceFileRemoveItAndSelect(), problemKind: .sourceMissing)
    }
    public static var modified: UserError { UserError(L10nMessage.attachmentIosTheFileChangedAfterSelectionSelectItAgain()) }

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
    private var waiterWatchers: [(target: Int, cont: CheckedContinuation<Void, Never>)] = []
    func acquire() async throws {
        try Task.checkCancellation()
        if !busy { busy = true; return }
        let id = UUID()
        let acquired = await withTaskCancellationHandler {
            await withCheckedContinuation { continuation in
                if Task.isCancelled { continuation.resume(returning: false) }
                else {
                    waiting.append((id, continuation))
                    notifyWaiters()
                }
            }
        } onCancel: { Task { await self.cancel(id) } }
        guard acquired else { throw CancellationError() }
        if Task.isCancelled { release(); throw CancellationError() }
    }
    func waitForWaitersForTest(_ count: Int) async {
        if waiting.count >= count { return }
        await withCheckedContinuation { waiterWatchers.append((count, $0)) }
    }
    private func notifyWaiters() {
        var remaining: [(target: Int, cont: CheckedContinuation<Void, Never>)] = []
        for w in waiterWatchers {
            if waiting.count >= w.target { w.cont.resume() } else { remaining.append(w) }
        }
        waiterWatchers = remaining
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

public func fileUploadError(_ error: Error) -> String { fileUploadMessage(error).resolve() }

func fileUploadMessage(_ error: Error) -> LocalizedMessage {
    guard let failure = error as? any UploadFailure else {
        return (error as? UserError)?.localized ?? PickedFile.unreadable.localized
    }
    switch failure.code {
    case "already_exists": return L10nMessage.attachmentIosTheFileChangedAfterSelectionSelectItAgain()
    case "not_found": return L10nMessage.attachmentIosTheFileOnTheComputerExpiredRetryTheUpload()
    case "resource_exhausted": return L10nMessage.attachmentIosTheComputerSAttachmentStorageIsFullClearSpace()
    case "unavailable", "network", CommandError.unknownOutcome: return L10nMessage.attachmentIosCouldNotConnectToTheComputerRetryTheUpload()
    case "invalid_argument", "data_loss": return L10nMessage.attachmentIosFileVerificationFailedTheFileMayHaveChangedSelect()
    case "permission_denied", "unauthenticated": return L10nMessage.attachmentIosFileUploadIsNotAuthorizedCheckDeviceAuthorization()
    default: return L10nMessage.attachmentIosFileUploadFailedRetryOrSelectTheFileAgain()
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
            guard !Task.isCancelled, uploadRetryAllowed(error), retries < 3 else { return false }
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
            guard offset >= 0 else { throw UserError(L10nMessage.attachmentIosTheComputerReportedInvalidUploadProgress()) }
            let count = Int(min(Int64(chunkSize), refreshed.size - offset))
            let data = try await fileWork { try refreshed.chunk(offset: offset, count: count) }
            try Task.checkCancellation()
            do {
                status = try await channel.put(transferId: transferId, offset: offset, data: data)
                guard status.persistedBytes > offset else { throw UserError(L10nMessage.attachmentIosTheComputerReportedInvalidUploadProgress()) }
            } catch where again(error) { status = try await channel.status(transferId: transferId) }
            onProgress(status.persistedBytes, refreshed.size)
        }
        guard status.persistedBytes >= 0, status.persistedBytes <= refreshed.size else { throw UserError(L10nMessage.attachmentIosTheComputerReportedInvalidUploadProgress()) }
        // Hashing may still be in flight, even when Complete itself returns RECEIVING.
        var checking = 0
        while status.state == .receiving {
            try Task.checkCancellation()
            do {
                status = try await channel.complete(transferId: transferId)
                if status.state == .receiving && status.persistedBytes == refreshed.size {
                    checking += 1
                    guard checking < 3 else { throw UserError(L10nMessage.errorsTheComputerIsStillVerifyingAttachmentsRetryShortly()) }
                    continue
                }
                guard status.state == .complete else { throw UserError(L10nMessage.attachmentIosTheComputerDidNotConfirmUploadCompletionRetry()) }
            } catch where again(error) { status = try await channel.status(transferId: transferId) }
        }
        try Task.checkCancellation()
        guard status.state == .complete, !status.fileID.isEmpty else { throw UserError(L10nMessage.attachmentIosTheComputerDidNotProvideAFileId()) }
        onProgress(refreshed.size, refreshed.size)
        await FileUploadGate.shared.release()
        return UploadedImage(imageId: status.fileID, expiresMs: status.expiresMs)
    } catch {
        await FileUploadGate.shared.release()
        throw error
    }
}
