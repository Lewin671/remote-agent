// Preparing and transferring attachments share synchronous invalidation: no await
// may leave a superseded completion visible to a composer (docs/session-files.md).
import Foundation
import RAProto

@MainActor
public final class ImageUploads {
    public typealias Prepare = @Sendable (_ uri: String, _ original: Bool) async throws -> Processed
    private let clock: @MainActor () -> Int64
    private let sleep: @Sendable (Int64) async throws -> Void
    private let prepare: Prepare
    private let channel: @Sendable (_ machineId: String) -> any UploadChannel
    private let readable: @MainActor (_ uri: String) -> Bool
    public var onPrepared: (@MainActor (PendingImage) -> Void)?
    public var onFileChanged: (@MainActor (PendingImage) async throws -> Void)?

    public init(
        prepare: @escaping Prepare, channel: @escaping @Sendable (_ machineId: String) -> any UploadChannel,
        readable: @escaping @MainActor (_ uri: String) -> Bool = { _ in true },
        clock: @escaping @MainActor () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
        sleep: @escaping @Sendable (Int64) async throws -> Void = { try await Task.sleep(for: .milliseconds($0)) }
    ) {
        self.clock = clock; self.sleep = sleep; self.prepare = prepare
        self.channel = channel; self.readable = readable
    }

    @discardableResult
    public func add(_ draft: ImageDraft, uris: [String], machine: @escaping @MainActor () -> String?) -> [PendingImage] {
        let added = uris.map { PendingImage(id: newRequestId(), uri: $0) }
        draft.append(added)
        for img in added { process(draft, img, machine: machine) }
        return added
    }

    private func check(_ img: PendingImage, _ token: Int) throws {
        try Task.checkCancellation()
        guard img.isCurrent(token) else { throw CancellationError() }
    }

    public func process(_ draft: ImageDraft, _ img: PendingImage, machine: @escaping @MainActor () -> String?) {
        draft.invalidate(img)
        let token = img.generation
        let cleanup = abort(img.takeRetired())
        guard let uri = img.uri else { img.state = .failed(error: "读不到这张图片"); return }
        let original = draft.original, prepare = self.prepare
        img.job = Task { [weak self] in
            guard let self else { return }
            do {
                await cleanup.value
                try check(img, token)
                let p = try await prepare(uri, original)
                try check(img, token)
                img.processed = p
                onPrepared?(img)
                draft.prepared(img)
                img.job = nil
                if let m = machine() { upload(img, machineId: m) }
                else { img.state = .failed(error: "先选一台电脑") }
            } catch {
                if Task.isCancelled || !img.isCurrent(token) || error is CancellationError { return }
                img.state = .failed(error: errorText(error))
            }
        }
    }

    /// A selected target changed even if it is offline, unsupported, or temporarily absent.
    public func retarget(_ img: PendingImage) {
        img.invalidate(retiring: true)
        _ = abort(img.takeRetired())
    }

    public func upload(_ img: PendingImage, machineId: String) {
        guard img.hasSource else {
            img.invalidate()
            img.state = .failed(error: img.isFile ? "这个文件没有本机副本，请移除后重新选择" : "读不到这张图片")
            return
        }
        guard img.startUpload(to: machineId, now: clock()) else { return }
        let token = img.generation
        let cleanup = abort(img.takeRetired())
        let ch = channel(machineId)
        var transfer = img.transferId
        img.job = Task {
            do {
                await cleanup.value
                try check(img, token)
                @MainActor func keep() async throws {
                    try check(img, token)
                    if img.isFile { try await self.onFileChanged?(img) }
                    try check(img, token)
                }
                @MainActor func send() async throws -> UploadedImage {
                    try await keep()
                    let progress: @MainActor (Int64, Int64) -> Void = { done, total in
                        guard img.isCurrent(token), !Task.isCancelled else { return }
                        img.state = .uploading(progress: total > 0 ? Float(done) / Float(total) : 0)
                    }
                    if let file = img.file {
                        return try await uploadFile(ch, transferId: transfer, source: file, onSource: { refreshed in
                            try self.check(img, token)
                            img.file?.bookmark = refreshed.bookmark
                            try await keep()
                        }, onProgress: progress)
                    }
                    guard let p = img.processed else { throw UserError("读不到这张图片") }
                    return try await uploadImage(ch, transferId: transfer, data: p.bytes, onProgress: progress)
                }
                let uploaded: UploadedImage
                do {
                    uploaded = try await send()
                } catch let error as any UploadFailure where error.code == "not_found" || (img.isFile && error.code == "already_exists") {
                    try check(img, token)
                    // Only this generation's transport can be replaced. A late abort must
                    // never target a newer generation's reservation on the same machine.
                    let abandoned = transfer
                    img.imageId = nil; img.machineId = nil; img.expiresMs = 0
                    img.state = .processing
                    if img.isFile { try await ch.abort(transferId: abandoned) }
                    try check(img, token)
                    transfer = newRequestId()
                    img.transferId = transfer
                    if img.isFile { img.file?.transfers[machineId] = FileTransfer(transferId: transfer) }
                    uploaded = try await send()
                }
                try check(img, token)
                if img.isFile {
                    img.file?.transfers[machineId] = FileTransfer(transferId: transfer, fileId: uploaded.imageId, expiresMs: uploaded.expiresMs)
                    try await keep()
                }
                try check(img, token)
                // Publish a completion only after the whole operation and its persistence succeeded.
                img.imageId = uploaded.imageId
                img.machineId = machineId
                img.expiresMs = uploaded.expiresMs
                img.state = .ready
                scheduleRefresh(img, machineId: machineId, expiresMs: uploaded.expiresMs, token: token)
            } catch {
                if Task.isCancelled || !img.isCurrent(token) || error is CancellationError { return }
                img.imageId = nil; img.machineId = nil; img.expiresMs = 0
                img.state = .failed(error: img.isFile ? fileUploadError(error) : errorText(error))
            }
        }
    }

    private func abort(_ references: [UploadReservation]) -> Task<Void, Never> {
        let unique = Set(references), channel = self.channel
        return Task {
            for reference in unique {
                let ch = channel(reference.machine)
                for attempt in 0..<3 {
                    do { try await ch.abort(transferId: reference.transfer); break }
                    catch {
                        guard let failure = error as? any UploadFailure,
                              failure.unknown || failure.code == "network", attempt < 2 else { break }
                    }
                }
            }
        }
    }

    /// Draft membership is removed by the caller before this cleanup. Accepted
    /// requests retain their immutable ID snapshot and their machine's reservation.
    @discardableResult
    public func discard(_ img: PendingImage, keeping machine: String? = nil) -> Task<Void, Never> {
        let unused = img.reservations.filter(img.canAbort)
        img.invalidate(cancelUpload: machine == nil)
        for reference in unused { img.file?.transfers[reference.machine] = nil }
        return abort(unused + img.takeRetired())
    }

    private func scheduleRefresh(_ img: PendingImage, machineId: String, expiresMs: Int64, token: Int) {
        guard expiresMs > clock() else { return }
        let delay = max(1, expiresMs - clock()), sleep = self.sleep
        img.refreshJob?.cancel()
        img.refreshJob = Task { [weak self, weak img] in
            do { try await sleep(delay) } catch { return }
            guard let self, let img, !Task.isCancelled, img.isCurrent(token) else { return }
            self.upload(img, machineId: machineId)
        }
    }

    public func sendAll(_ draft: ImageDraft, machineId: String) {
        for img in draft.toSend(machineId: machineId, now: clock()) { upload(img, machineId: machineId) }
    }

    public func retry(_ draft: ImageDraft, _ img: PendingImage, machine: @escaping @MainActor () -> String?) {
        img.invalidate(retiring: true)
        if img.isFile && !img.hasSource {
            _ = abort(img.takeRetired())
            img.state = .failed(error: "这个文件没有本机副本，请移除后重新选择")
        } else if !img.isFile && (img.needsProcessing || img.processed == nil) {
            process(draft, img, machine: machine)
        } else if let m = machine() { upload(img, machineId: m) }
        else {
            _ = abort(img.takeRetired())
            img.state = .failed(error: "先选一台电脑")
        }
    }

    public func canReprepare(_ draft: ImageDraft) -> Bool {
        !draft.photos.isEmpty && draft.photos.allSatisfy { img in img.uri.map(readable) ?? false }
    }

    public func setOriginal(_ draft: ImageDraft, _ v: Bool, machine: @escaping @MainActor () -> String?) {
        guard canReprepare(draft), draft.setOriginal(v) else { return }
        for img in draft.photos {
            if let uri = img.uri, readable(uri) { process(draft, img, machine: machine) }
        }
    }
}
