// Platform media coordinator for the account in use:
//   - viewed images and event previews (decrypted in memory for the session viewer);
//   - async thumbnail and sharp-preview decoding from `PendingScope` local refs;
//   - OS picker / pasteboard presentation and ingestion into Core's
//     `DraftReserveSlots` / `DraftAddItems` / `DraftReleaseSlots` actions.
//
// Draft state, upload chunking/retries, capabilities, and outbox lifecycle live
// exclusively in Go clientcore (`PBAttachmentDraftView`, `CoreHost`).
import CoreTransferable
import ImageIO
import Observation
import RACore
import RAProto
import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// A photo picked from the library, taken as the file the picker hands over: its size is known before any
/// of it is read, so one too large is refused without coming into memory.
struct PickedPhoto: Transferable, Sendable {
    let read: Result<Data, ImageTooLarge>

    static var transferRepresentation: some TransferRepresentation {
        FileRepresentation(importedContentType: .image) { received in
            do {
                return PickedPhoto(read: .success(try readPicked(received.file)))
            } catch let refused as ImageTooLarge {
                return PickedPhoto(read: .failure(refused))
            }
        }
    }
}

/// A display copy decrypted for the viewer: in memory only, for as long as the account is in use.
struct ViewedImage: @unchecked Sendable {
    let image: UIImage
    let bytes: Data
    let format: PBImageFormat
}

@MainActor
@Observable
final class ImageCenter {
    /// What ＋ is showing for a draft.
    enum Pick: Equatable {
        case choose
        case photos
        case attachments
        case documents
    }

    struct Notice: Identifiable {
        let id = UUID()
        let text: String
    }

    // MARK: One per account

    @ObservationIgnored private static var centers: [ObjectIdentifier: ImageCenter] = [:]

    @discardableResult
    static func install(remote: Remote, coreHost: CoreHost, pending: PendingScope, live: Bool = true) -> ImageCenter {
        centers[ObjectIdentifier(remote)]?.close()
        let center = ImageCenter(remote: remote, coreHost: coreHost, pending: pending, live: live)
        centers[ObjectIdentifier(remote)] = center
        return center
    }

    static func remove(_ remote: Remote) {
        centers.removeValue(forKey: ObjectIdentifier(remote))?.close()
    }

    static func of(_ remote: Remote) -> ImageCenter {
        guard let c = centers[ObjectIdentifier(remote)] else {
            preconditionFailure("ImageCenter.install must be called before ImageCenter.of")
        }
        return c
    }

    // MARK: State

    @ObservationIgnored let remote: Remote
    @ObservationIgnored let coreHost: CoreHost
    @ObservationIgnored let pending: PendingScope
    @ObservationIgnored let live: Bool
    @ObservationIgnored private var closed = false

    /// Display copies decrypted while the session image viewer is open.
    @ObservationIgnored private let viewed = AccountCache<ViewedImage>(limit: 6)
    private var arrivals = 0
    @ObservationIgnored private let previews: NSCache<NSString, UIImage> = {
        let c = NSCache<NSString, UIImage>()
        c.countLimit = 60
        return c
    }()

    /// Bounded LRU cache of decoded thumbnails for Core's local attachment refs (`thumb_ref` / `prepared_ref`).
    static let maxLocalImages = 64
    /// Bounded negative cache so undecodable/missing/oversized blobs do not re-trigger reads on every SwiftUI render.
    static let maxFailedLocalRefs = 256
    /// Size cap for inline strip thumbnail decodes (normal `thumb_ref` is ~20 KiB; huge original blobs skip full decrypt here).
    static let maxThumbnailBlobBytes: Int64 = 8 * 1024 * 1024
    private var localImages: [String: UIImage] = [:]
    @ObservationIgnored private var localImageOrder: [String] = []
    @ObservationIgnored private var loadingRefs = Set<String>()
    @ObservationIgnored private var failedLocalRefs = Set<String>()
    @ObservationIgnored private var failedLocalRefOrder: [String] = []
    @ObservationIgnored private var loadTasks: [String: Task<Void, Never>] = [:]
    #if DEBUG
    @ObservationIgnored private var localImageLoadCount = 0
    #endif

    /// Transient picker / clipboard UX notices (e.g., empty clipboard or blocked picker tap).
    private(set) var notices: [String: Notice] = [:]
    var picking: [String: Pick] = [:]

    private init(remote: Remote, coreHost: CoreHost, pending: PendingScope, live: Bool) {
        self.remote = remote
        self.coreHost = coreHost
        self.pending = pending
        self.live = live
        viewed.use(remote)
    }

    func handlers() -> CoreHost.AttachmentHandlers {
        CoreHost.AttachmentHandlers(
            onReleaseLocalRefs: { [weak self] refs in
                self?.evictLocalRefs(refs)
            },
            onClearAllLocal: { [weak self] in
                self?.clearLocalCache()
            }
        )
    }

    private func close() {
        closed = true
        for task in loadTasks.values { task.cancel() }
        loadTasks.removeAll()
        loadingRefs.removeAll()
        failedLocalRefs.removeAll()
        failedLocalRefOrder.removeAll()
        localImages.removeAll()
        localImageOrder.removeAll()
        notices.removeAll()
        picking.removeAll()
        viewed.clear()
        previews.removeAllObjects()
    }

    private func evictLocalRefs(_ refs: [String]) {
        guard !refs.isEmpty else { return }
        let removed = Set(refs)
        for ref in refs {
            loadTasks.removeValue(forKey: ref)?.cancel()
            loadingRefs.remove(ref)
            failedLocalRefs.remove(ref)
            localImages.removeValue(forKey: ref)
        }
        localImageOrder.removeAll { removed.contains($0) }
        failedLocalRefOrder.removeAll { removed.contains($0) }
    }

    private func clearLocalCache() {
        for task in loadTasks.values { task.cancel() }
        loadTasks.removeAll()
        loadingRefs.removeAll()
        failedLocalRefs.removeAll()
        failedLocalRefOrder.removeAll()
        localImages.removeAll()
        localImageOrder.removeAll()
    }

    private func recordFailedLocalRef(_ ref: String) {
        guard failedLocalRefs.insert(ref).inserted else { return }
        failedLocalRefOrder.append(ref)
        while failedLocalRefOrder.count > Self.maxFailedLocalRefs {
            let evicted = failedLocalRefOrder.removeFirst()
            failedLocalRefs.remove(evicted)
        }
    }

    private func cacheLocalImage(_ image: UIImage, for ref: String) {
        failedLocalRefs.remove(ref)
        localImages[ref] = image
        if let idx = localImageOrder.firstIndex(of: ref) {
            localImageOrder.remove(at: idx)
        }
        localImageOrder.append(ref)
        while localImageOrder.count > Self.maxLocalImages {
            let evicted = localImageOrder.removeFirst()
            localImages.removeValue(forKey: evicted)
        }
    }

    #if DEBUG
    var localImageCacheCountForTesting: Int { localImages.count }
    var failedLocalRefCountForTesting: Int { failedLocalRefs.count }
    var localImageLoadCountForTesting: Int { localImageLoadCount }

    func drainLocalImageLoadsForTesting() async {
        while !loadTasks.isEmpty {
            let current = Array(loadTasks.values)
            for task in current {
                await task.value
            }
        }
    }
    #endif

    // MARK: Viewed images & timeline previews

    func viewedImage(_ key: String) -> ViewedImage? {
        _ = arrivals
        return viewed.get(remote, key: key)
    }

    func keepViewed(_ key: String, _ image: ViewedImage) {
        if closed { return }
        viewed.put(remote, key: key, value: image)
        arrivals += 1
    }

    func preview(imageID: String, displayName: String, data: Data) -> UIImage? {
        if data.isEmpty { return nil }
        let key = "\(imageID)/\(data.count)/\(displayName)" as NSString
        if let held = previews.object(forKey: key) { return held }
        guard let image = UIImage(data: data) else { return nil }
        if !closed { previews.setObject(image, forKey: key) }
        return image
    }

    // MARK: Local attachment thumbnails & sharp viewer decode

    private enum LocalThumbnailDecode: @unchecked Sendable {
        case image(UIImage)
        case transientFailure
        case permanentFailure
    }

    /// Returns a cached thumbnail for `ref` (`thumb_ref` or `prepared_ref`), or triggers an async decode.
    func localImage(for ref: String) -> UIImage? {
        guard !ref.isEmpty, !closed else { return nil }
        if let cached = localImages[ref] {
            if localImageOrder.last != ref, let idx = localImageOrder.firstIndex(of: ref) {
                localImageOrder.remove(at: idx)
                localImageOrder.append(ref)
            }
            return cached
        }
        guard !failedLocalRefs.contains(ref) else { return nil }
        guard loadingRefs.insert(ref).inserted else { return nil }
        #if DEBUG
        localImageLoadCount += 1
        #endif
        let store = pending
        let maxBytes = Self.maxThumbnailBlobBytes
        loadTasks[ref] = Task { [weak self] in
            let outcome: LocalThumbnailDecode = await Task.detached(priority: .userInitiated) {
                do {
                    let bytes = try store.readBlob(ref, maxBytes: maxBytes)
                    guard let cg = thumbnailImage(bytes, maxLong: 256) else {
                        return .permanentFailure
                    }
                    return .image(UIImage(cgImage: cg))
                } catch let v as VaultError where v.isLocked {
                    return .transientFailure
                } catch {
                    return .permanentFailure
                }
            }.value
            guard let self else { return }
            self.loadingRefs.remove(ref)
            self.loadTasks.removeValue(forKey: ref)
            guard !self.closed, !Task.isCancelled else { return }
            switch outcome {
            case .image(let decoded):
                self.cacheLocalImage(decoded, for: ref)
            case .permanentFailure:
                self.recordFailedLocalRef(ref)
            case .transientFailure:
                break
            }
        }
        return nil
    }

    /// Decodes a full-resolution (up to 2048px) preview from a local blob ref for `DraftImageViewer`.
    func loadSharpImage(for ref: String) async -> UIImage? {
        guard !ref.isEmpty, !closed else { return nil }
        let store = pending
        return await Task.detached(priority: .userInitiated) {
            guard let bytes = try? store.readBlob(ref) else { return nil }
            guard let source = CGImageSourceCreateWithData(bytes as CFData, nil),
                  let cg = CGImageSourceCreateThumbnailAtIndex(source, 0, [
                      kCGImageSourceCreateThumbnailFromImageAlways: true,
                      kCGImageSourceCreateThumbnailWithTransform: true,
                      kCGImageSourceThumbnailMaxPixelSize: 2048,
                  ] as CFDictionary)
            else { return nil }
            return UIImage(cgImage: cg)
        }.value
    }

    // MARK: Notices

    func say(_ scopeKey: String, _ text: String) {
        guard !text.isEmpty else { return }
        notices[scopeKey] = Notice(text: text)
    }

    func clearNotice(_ scopeKey: String, id: UUID) {
        if notices[scopeKey]?.id == id { notices[scopeKey] = nil }
    }

    // MARK: Picker triggers

    func plus(scope: PBDraftScope, draft: PBAttachmentDraftView) {
        let key = scope.debugKey
        if !draft.canPickGallery && !draft.canPickFiles {
            if draft.hasGalleryBlockedReason {
                say(key, draft.galleryBlockedReason.rendered())
            }
            return
        }
        if draft.galleryAllowsVideo {
            picking[key] = .attachments
            return
        }
        guard draft.canPickImages else {
            if draft.hasImagesPickBlockedReason {
                say(key, draft.imagesPickBlockedReason.rendered())
            }
            return
        }
        picking[key] = UIPasteboard.general.hasImages ? .choose : .photos
    }

    func library(scope: PBDraftScope, draft: PBAttachmentDraftView, offerPaste: Bool = false) {
        let key = scope.debugKey
        guard draft.canPickGallery else {
            if draft.hasGalleryBlockedReason {
                say(key, draft.galleryBlockedReason.rendered())
            }
            return
        }
        picking[key] = (offerPaste && UIPasteboard.general.hasImages) ? .choose : .photos
    }

    func documents(scope: PBDraftScope, draft: PBAttachmentDraftView) {
        let key = scope.debugKey
        guard draft.canPickFiles else {
            if draft.hasFilesPickBlockedReason {
                say(key, draft.filesPickBlockedReason.rendered())
            }
            return
        }
        picking[key] = .documents
    }

    // MARK: Ingestion into Core (`ReserveSlots` -> `AddItems` / `ReleaseSlots`)

    private static func imageMimeType(for provider: NSItemProvider) -> String {
        for identifier in provider.registeredTypeIdentifiers {
            if let type = UTType(identifier), type.conforms(to: .image), let mime = type.preferredMIMEType {
                return mime
            }
        }
        return "image/jpeg"
    }

    private static func mimeType(for imageData: Data) -> String {
        switch detectFormat(imageData) {
        case .png: return "image/png"
        case .webp: return "image/webp"
        case .gif: return "image/gif"
        default: return "image/jpeg"
        }
    }

    /// Adds raw image bytes (from debug signals or pasteboard data).
    @discardableResult
    func addImageData(scope: PBDraftScope, _ dataList: [Data]) -> Task<Void, Never>? {
        guard !dataList.isEmpty, !closed else { return nil }
        let reservationID = newRequestId()
        let store = pending
        let host = coreHost
        let scopeKey = scope.debugKey
        return Task { [weak self] in
            await host.reserveDraftSlots(
                scope: scope,
                reservationID: reservationID,
                imageSlots: Int32(dataList.count),
                fileSlots: 0
            )
            var picked: [PBDraftPickedItem] = []
            var createdRefs: [String] = []
            var firstError: Error?
            for data in dataList {
                do {
                    let ref = try await Task.detached(priority: .userInitiated) {
                        try store.storeBlob(data, prefix: "src")
                    }.value
                    createdRefs.append(ref)
                    picked.append(.with {
                        $0.candidate = .with {
                            $0.mimeType = Self.mimeType(for: data)
                            $0.sizeBytes = Int64(data.count)
                            $0.forceFile = false
                        }
                        $0.sourceRef = ref
                        $0.hasOriginalSource_p = true
                    })
                } catch {
                    if firstError == nil { firstError = error }
                }
            }
            guard let self, !self.closed else {
                store.releaseLocal(refs: createdRefs)
                await host.releaseDraftSlots(scope: scope, reservationID: reservationID)
                return
            }
            if picked.isEmpty {
                await host.releaseDraftSlots(scope: scope, reservationID: reservationID)
                if let firstError { self.say(scopeKey, errorText(firstError)) }
                return
            }
            await host.addDraftItems(scope: scope, reservationID: reservationID, items: picked)
            if let firstError { self.say(scopeKey, errorText(firstError)) }
        }
    }

    /// Imports mixed photos and videos from `PHPicker` (`force_file = false`).
    func addLibraryItems(scope: PBDraftScope, _ providers: [NSItemProvider]) {
        guard !providers.isEmpty, !closed else { return }
        let reservationID = newRequestId()
        let videoCount = providers.filter { $0.hasItemConformingToTypeIdentifier(UTType.movie.identifier) }.count
        let imageCount = providers.count - videoCount
        let store = pending
        let host = coreHost
        let scopeKey = scope.debugKey
        Task { [weak self] in
            await host.reserveDraftSlots(
                scope: scope,
                reservationID: reservationID,
                imageSlots: Int32(imageCount),
                fileSlots: Int32(videoCount)
            )
            let coreMaxFileBytes: Int64 = videoCount > 0 ? await host.maxFileBytes(for: scope) : 0
            var picked: [PBDraftPickedItem] = []
            var createdRefs: [String] = []
            var copyFailed = false
            var firstError: Error?
            for provider in providers {
                if provider.hasItemConformingToTypeIdentifier(UTType.movie.identifier) {
                    do {
                        let staged: StagedLibraryVideo = try await withCheckedThrowingContinuation { done in
                            _ = provider.loadFileRepresentation(forTypeIdentifier: UTType.movie.identifier) { url, error in
                                guard let url else {
                                    done.resume(throwing: error ?? AttachmentFileIOError(.sourceMissing))
                                    return
                                }
                                done.resume(with: Result {
                                    try LibraryVideos.stage(url, maxBytes: coreMaxFileBytes)
                                })
                            }
                        }
                        switch staged {
                        case .rejectedCandidate(let info):
                            // Hand the oversized/directory candidate straight to Core without copying or encrypting bytes.
                            picked.append(.with {
                                $0.candidate = info.candidate(forceFile: false)
                                $0.sourceRef = ""
                            })
                        case .copied(let info, let copiedURL):
                            defer { LibraryVideos.discard(copiedURL) }
                            let (ref, durationMs) = try await Task.detached(priority: .userInitiated) {
                                let dur = await CoreHost.probeVideoDurationMs(at: copiedURL, securityScoped: false)
                                let ref = try store.storeBlob(from: copiedURL, prefix: "vid")
                                return (ref, dur)
                            }.value
                            createdRefs.append(ref)
                            picked.append(.with {
                                $0.candidate = info.candidate(forceFile: false)
                                $0.sourceRef = ref
                                if durationMs > 0 {
                                    $0.hasDuration_p = true
                                    $0.durationMs = durationMs
                                }
                            })
                        }
                    } catch {
                        copyFailed = true
                        if firstError == nil { firstError = error }
                    }
                } else {
                    do {
                        let photo: PickedPhoto = try await withCheckedThrowingContinuation { done in
                            _ = provider.loadTransferable(type: PickedPhoto.self) { done.resume(with: $0) }
                        }
                        let data = try photo.read.get()
                        let ref = try await Task.detached(priority: .userInitiated) {
                            try store.storeBlob(data, prefix: "src")
                        }.value
                        createdRefs.append(ref)
                        let suggestedName = provider.suggestedName ?? ""
                        let mime = Self.imageMimeType(for: provider)
                        picked.append(.with {
                            $0.candidate = .with {
                                $0.name = suggestedName
                                $0.mimeType = mime
                                $0.sizeBytes = Int64(data.count)
                                $0.forceFile = false
                            }
                            $0.sourceRef = ref
                            $0.hasOriginalSource_p = true
                        })
                    } catch {
                        if firstError == nil { firstError = error }
                    }
                }
            }
            guard let self, !self.closed else {
                store.releaseLocal(refs: createdRefs)
                await host.releaseDraftSlots(scope: scope, reservationID: reservationID)
                return
            }
            if picked.isEmpty {
                await host.releaseDraftSlots(scope: scope, reservationID: reservationID)
                if copyFailed {
                    self.say(scopeKey, L10n.imageIosCannotReadThisVideoSelectItAgain())
                } else if let firstError {
                    self.say(scopeKey, errorText(firstError))
                }
                return
            }
            await host.addDraftItems(scope: scope, reservationID: reservationID, items: picked)
            if copyFailed {
                self.say(scopeKey, L10n.imageIosCannotReadThisVideoSelectItAgain())
            } else if let firstError {
                self.say(scopeKey, errorText(firstError))
            }
        }
    }

    /// Imports documents from `UIDocumentPicker` (`force_file = true`).
    func addFiles(scope: PBDraftScope, _ urls: [URL]) {
        guard !urls.isEmpty, !closed else { return }
        let reservationID = newRequestId()
        let store = pending
        let host = coreHost
        let scopeKey = scope.debugKey
        Task { [weak self] in
            await host.reserveDraftSlots(
                scope: scope,
                reservationID: reservationID,
                imageSlots: 0,
                fileSlots: Int32(urls.count)
            )
            var picked: [PBDraftPickedItem] = []
            var createdRefs: [String] = []
            var firstError: Error?
            for url in urls {
                do {
                    let item: (candidate: PBAttachmentCandidateInput, ref: String) = try await Task.detached(priority: .userInitiated) {
                        let info = try PickedFile.inspect(url: url)
                        if info.isDirectory {
                            return (info.candidate(forceFile: true), "")
                        }
                        guard let bookmark = info.bookmark else {
                            throw AttachmentFileIOError(.sourceMissing)
                        }
                        let ref = try store.storeBookmark(bookmark)
                        return (info.candidate(forceFile: true), ref)
                    }.value
                    if !item.ref.isEmpty { createdRefs.append(item.ref) }
                    picked.append(.with {
                        $0.candidate = item.candidate
                        $0.sourceRef = item.ref
                    })
                } catch {
                    if firstError == nil { firstError = error }
                }
            }
            guard let self, !self.closed else {
                store.releaseLocal(refs: createdRefs)
                await host.releaseDraftSlots(scope: scope, reservationID: reservationID)
                return
            }
            if picked.isEmpty {
                await host.releaseDraftSlots(scope: scope, reservationID: reservationID)
                if let firstError { self.say(scopeKey, errorText(firstError)) }
                return
            }
            await host.addDraftItems(scope: scope, reservationID: reservationID, items: picked)
            if let firstError { self.say(scopeKey, errorText(firstError)) }
        }
    }

    /// Picks candidate image UTIs registered on `provider` (concrete types first, then `public.image`)
    /// because `UIPasteboard` item providers register concrete types (`public.jpeg`, `public.png`, …)
    /// and reject `loadFileRepresentation(forTypeIdentifier: "public.image")`.
    static func imageTypeIdentifiers(for provider: NSItemProvider) -> [String] {
        var ids: [String] = []
        for id in provider.registeredTypeIdentifiers {
            if let type = UTType(id), type.conforms(to: .image), !ids.contains(id) {
                ids.append(id)
            }
        }
        if !ids.contains(UTType.image.identifier) {
            ids.append(UTType.image.identifier)
        }
        return ids
    }

    /// Loads one image `NSItemProvider` via `loadFileRepresentation` + `readPicked` so oversized clipboard
    /// images are refused by size before reading bytes into memory. Falls back to `loadDataRepresentation`
    /// only when the provider cannot serve a file representation for its concrete image UTI.
    static func loadBoundedImageData(from provider: NSItemProvider) async throws -> Data {
        let typeIDs = imageTypeIdentifiers(for: provider)
        var lastError: Error?
        for typeID in typeIDs {
            do {
                return try await withCheckedThrowingContinuation { done in
                    _ = provider.loadFileRepresentation(forTypeIdentifier: typeID) { url, error in
                        guard let url else {
                            done.resume(throwing: error ?? AttachmentFileIOError(.decode))
                            return
                        }
                        done.resume(with: Result { try readPicked(url) })
                    }
                }
            } catch let tooLarge as ImageTooLarge {
                throw tooLarge
            } catch {
                lastError = error
            }
        }
        for typeID in typeIDs {
            do {
                let data: Data = try await withCheckedThrowingContinuation { done in
                    _ = provider.loadDataRepresentation(forTypeIdentifier: typeID) { d, error in
                        if let d {
                            done.resume(returning: d)
                        } else {
                            done.resume(throwing: error ?? AttachmentFileIOError(.decode))
                        }
                    }
                }
                try checkPicked(bytes: Int64(data.count), original: false, originalMaxBytes: 0)
                return data
            } catch let tooLarge as ImageTooLarge {
                throw tooLarge
            } catch {
                lastError = error
            }
        }
        throw lastError ?? AttachmentFileIOError(.decode)
    }

    /// Pastes images from the system clipboard.
    func paste(scope: PBDraftScope) {
        let providers = UIPasteboard.general.itemProviders.filter {
            $0.hasItemConformingToTypeIdentifier(UTType.image.identifier)
        }
        guard !providers.isEmpty else {
            say(scope.debugKey, L10n.attachmentIosClipboardEmpty())
            return
        }
        let reservationID = newRequestId()
        let store = pending
        let host = coreHost
        let scopeKey = scope.debugKey
        Task { [weak self] in
            await host.reserveDraftSlots(
                scope: scope,
                reservationID: reservationID,
                imageSlots: Int32(providers.count),
                fileSlots: 0
            )
            var picked: [PBDraftPickedItem] = []
            var createdRefs: [String] = []
            var firstError: Error?
            for provider in providers {
                do {
                    let data = try await Self.loadBoundedImageData(from: provider)
                    let ref = try await Task.detached(priority: .userInitiated) {
                        try store.storeBlob(data, prefix: "src")
                    }.value
                    createdRefs.append(ref)
                    let suggestedName = provider.suggestedName ?? ""
                    let mime = Self.imageMimeType(for: provider)
                    picked.append(.with {
                        $0.candidate = .with {
                            $0.name = suggestedName
                            $0.mimeType = mime
                            $0.sizeBytes = Int64(data.count)
                            $0.forceFile = false
                        }
                        $0.sourceRef = ref
                        $0.hasOriginalSource_p = true
                    })
                } catch {
                    if firstError == nil { firstError = error }
                }
            }
            guard let self, !self.closed else {
                store.releaseLocal(refs: createdRefs)
                await host.releaseDraftSlots(scope: scope, reservationID: reservationID)
                return
            }
            if picked.isEmpty {
                await host.releaseDraftSlots(scope: scope, reservationID: reservationID)
                if let firstError { self.say(scopeKey, errorText(firstError)) }
                return
            }
            await host.addDraftItems(scope: scope, reservationID: reservationID, items: picked)
            if let firstError { self.say(scopeKey, errorText(firstError)) }
        }
    }
}

/// Result of inspecting a picked library movie before deciding whether to copy and encrypt it.
enum StagedLibraryVideo: Sendable {
    /// Video exceeds Core's `maxFileBytes` (or is a directory); pass candidate straight to Core to reject without copying bytes.
    case rejectedCandidate(PickedFileInspection)
    /// Video is within Core's limit and copied into transient staging for duration probe + streaming encryption.
    case copied(PickedFileInspection, URL)
}

/// Transient staging folder for `PHPicker` movie transfers before they are copied into `PendingStore`.
/// Cleared on app startup and logout so interrupted transfers never leave plaintext videos behind.
enum LibraryVideos {
    static let root = FileManager.default.temporaryDirectory.appendingPathComponent("library-videos", isDirectory: true)

    /// Inspects `file` in place first; only copies into `root` when within Core's `maxBytes` limit.
    static func stage(_ file: URL, maxBytes: Int64, root: URL = root) throws -> StagedLibraryVideo {
        let info = try PickedFile.inspect(url: file)
        if info.isDirectory || (maxBytes > 0 && info.sizeBytes > maxBytes) {
            return .rejectedCandidate(info)
        }
        return .copied(info, try copy(file, root: root))
    }

    static func copy(_ file: URL, root: URL = root) throws -> URL {
        let folder = root.appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let copy = folder.appendingPathComponent(file.lastPathComponent)
        do { try FileManager.default.copyItem(at: file, to: copy) } catch {
            try? FileManager.default.removeItem(at: folder)
            throw error
        }
        return copy
    }

    static func discard(_ url: URL, root: URL = root) {
        let folder = url.standardizedFileURL.resolvingSymlinksInPath().deletingLastPathComponent()
        let base = root.standardizedFileURL.resolvingSymlinksInPath()
        guard folder.deletingLastPathComponent().path == base.path else { return }
        try? FileManager.default.removeItem(at: folder)
    }

    static func discardAll(root: URL = root) {
        try? FileManager.default.removeItem(at: root)
    }
}
