// The images of the account in use, between the picker and the CLI: the
// drafts of the message boxes, what was picked for them, their thumbnails,
// and the messages sent with images until the CLI has recorded them. The
// counterpart of Android's `ImageDrafts`, `ImageNotices`, `ViewerImages`
// and the image half of `Outbox` (ui/ImageDrafts.kt, ui/ImagePick.kt,
// ui/Actions.kt).
//
// What it holds and where:
//
//   - prepared bytes: in memory, and sealed in `PendingStore` so that a
//     reclaimed process does not lose a draft or a message on its way;
//   - what was picked (to prepare it again as an original): in memory for
//     this run only. Unlike Android there is no grant to keep: the system
//     picker hands over a copy and nothing that outlives the process;
//   - thumbnails and viewed display copies: in memory only.
//
// One exists per sync engine: made when the account comes into use and
// dropped with it, so nothing of one account is shown to the next.
import Observation
import CoreTransferable
import RACore
import RAProto
import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// Sealed files that are neither sealed nor files: for sample data, where nothing is stored.
final class VolatileFiles: RACore.SealedFiles, @unchecked Sendable {
    private let lock = NSLock()
    private var data: [String: Data] = [:]

    func put(folder: String, name: String, data: Data) {
        lock.withLock { self.data["\(folder)/\(name)"] = data }
    }
    func get(folder: String, name: String) -> Data? { lock.withLock { data["\(folder)/\(name)"] } }
    func remove(folder: String, name: String) { lock.withLock { data["\(folder)/\(name)"] = nil } }
    func clearFolder(_ folder: String) {
        lock.withLock { for k in data.keys where k.hasPrefix("\(folder)/") { data[k] = nil } }
    }
}

/// A photo picked from the library, taken as the file the picker hands over: its size is known before any
/// of it is read, so one too large is refused without coming into memory.
struct PickedPhoto: Transferable, Sendable {
    /// The file's bytes, or why they were not read.
    let read: Result<Data, ImageTooLarge>

    static var transferRepresentation: some TransferRepresentation {
        FileRepresentation(importedContentType: .image) { received in
            do {
                return PickedPhoto(read: .success(try readPicked(received.file)))
            } catch let refused as ImageTooLarge {
                // Said as it is: an error thrown from here reaches the caller as the system's own.
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
    /// Where the bytes of a picked image come from.
    enum Source {
        case data(Data)
        case library(NSItemProvider)
        case provider(NSItemProvider)
    }

    /// What ＋ is showing for a draft.
    enum Pick: Equatable {
        /// The choice between the photo library and the clipboard.
        case choose
        case photos
        case attachments
        case documents
    }

    // MARK: One per account

    @ObservationIgnored private static var centers: [ObjectIdentifier: ImageCenter] = [:]

    /// Called when an account comes into use. `live` false is sample data: nothing is uploaded.
    static func install(remote: Remote, coreHost: CoreHost? = nil, files: any RACore.SealedFiles, live: Bool = true) {
        centers[ObjectIdentifier(remote)]?.close()
        centers[ObjectIdentifier(remote)] = ImageCenter(remote: remote, coreHost: coreHost, files: files, live: live)
    }

    /// Called when the account is left: jobs stop, and what was held in memory goes.
    static func remove(_ remote: Remote) {
        centers.removeValue(forKey: ObjectIdentifier(remote))?.close()
    }

    /// The images of the account `remote` syncs. One that was never installed (a test's) keeps nothing on disk.
    static func of(_ remote: Remote) -> ImageCenter {
        if let c = centers[ObjectIdentifier(remote)] { return c }
        let c = ImageCenter(remote: remote, coreHost: nil, files: VolatileFiles(), live: true)
        centers[ObjectIdentifier(remote)] = c
        return c
    }

    // MARK: State

    @ObservationIgnored let remote: Remote
    @ObservationIgnored let live: Bool
    @ObservationIgnored private let coreHost: CoreHost?
    @ObservationIgnored private var fallbackCore: CoreClient?
    @ObservationIgnored private var fallbackLocale = ""
    @ObservationIgnored private let files: any RACore.SealedFiles
    @ObservationIgnored private let records: OutgoingRecords
    @ObservationIgnored private(set) var uploads: ImageUploads!
    @ObservationIgnored private var drafts: [String: ImageDraft] = [:]
    @ObservationIgnored private var sources: [String: Source] = [:]
    /// A picked file is being read, and who waits to read theirs.
    @ObservationIgnored private var reading = false
    @ObservationIgnored private var readers: [CheckedContinuation<Void, Never>] = []
    /// The machine each draft's images go to now.
    @ObservationIgnored private var targets: [String: String] = [:]
    @ObservationIgnored private var fileCaps: [String: AttachmentCapabilities] = [:]
    @ObservationIgnored private var blockedKeys = Set<String>()
    @ObservationIgnored private var aimedKeys = Set<String>()
    /// Messages with images held for thumbnails and clean disk release while Core owns delivery.
    @ObservationIgnored private var kept: [String: [Outgoing]] = [:]
    /// Crash-restored records from an earlier run not yet handed to Core or dismissed.
    var restoredOutgoing: [String: [Outgoing]] = [:]
    @ObservationIgnored private var restored = Set<String>()
    @ObservationIgnored private var restorations: [String: Task<Void, Never>] = [:]
    /// The account was left: what was on its way when that happened must not go on for it.
    @ObservationIgnored private var forgottenSessions = Set<String>()
    @ObservationIgnored private var closed = false
    /// Display copies decrypted while the viewer is open, so paging back does not fetch again.
    @ObservationIgnored private let viewed = AccountCache<ViewedImage>(limit: 6)
    /// Counts the display copies that came in. Read with every look for one, so that a view that showed
    /// what can be done without a copy is drawn again when the copy is here.
    private var arrivals = 0
    /// The previews the events carry, decoded once for the viewer's pages.
    @ObservationIgnored private let previews: NSCache<NSString, UIImage> = {
        let c = NSCache<NSString, UIImage>()
        c.countLimit = 60
        return c
    }()

    #if DEBUG
        /// Sample images stay in the states they were shown in: nothing sends them.
        @ObservationIgnored var frozen = false
    #endif

    private var thumbs: [String: UIImage] = [:]
    /// Cached Core `AttachmentEval` outputs (never queried inside SwiftUI `body`).
    private(set) var evaluatedProblems: [String: PBImageProblemView] = [:]
    private(set) var failureSummaries: [String: String] = [:]
    private(set) var libraryRooms: [String: Int] = [:]
    struct Notice: Identifiable {
        let id = UUID()
        let message: AttachmentNotice
        var text: String { message.text }
    }
    private(set) var notices: [String: Notice] = [:]
    private(set) var outgoingRestoreErrors: [String: String] = [:]
    var picking: [String: Pick] = [:]

    private init(remote: Remote, coreHost: CoreHost?, files: any RACore.SealedFiles, live: Bool) {
        self.remote = remote
        self.coreHost = coreHost
        self.files = files
        self.live = live
        records = OutgoingRecords(files: files)
        viewed.use(remote)
        uploads = ImageUploads(
            prepare: { [weak self] uri, original in
                guard let data = try await self?.read(uri) else { throw AttachmentFailure(kind: .sourceMissing) }
                return try await prepareImage(data, original: original)
            },
            channel: { [remote] machine in
                #if DEBUG
                    if !live { return SampleUploads() }
                #endif
                return remote.uploadChannel(machineId: machine)
            },
            readable: { [weak self] uri in
                #if DEBUG
                    if self?.frozen == true { return true }
                #endif
                return self?.sources[uri] != nil
            })
        uploads.onPrepared = { [weak self] img in self?.makeThumb(img) }
        uploads.onFileChanged = { [weak self] img in
            guard let self, !closed, let draft = drafts.values.first(where: { $0.images.contains(where: { $0 === img }) }) else { throw CancellationError() }
            try await draft.persistFile(img)
        }
    }

    private func close() {
        closed = true
        for draft in drafts.values { for img in draft.images { img.release() } }
        for task in restorations.values { task.cancel() }
        restorations.removeAll()
        drafts.removeAll()
        sources.removeAll()
        kept.removeAll()
        restoredOutgoing.removeAll()
        thumbs.removeAll()
        evaluatedProblems.removeAll()
        failureSummaries.removeAll()
        libraryRooms.removeAll()
        fallbackCore?.close()
        fallbackCore = nil
        viewed.clear()
        previews.removeAllObjects()
    }

    func forgetSession(_ sid: String) {
        guard forgottenSessions.insert(sid).inserted else { return }
        let key = Self.sessionKey(sid)
        restorations.removeValue(forKey: key)?.cancel()
        restorations.removeValue(forKey: "out:" + sid)?.cancel()
        restoredOutgoing[sid] = nil
        let draft = drafts.removeValue(forKey: key)
        let images = (draft?.images ?? []) + (kept.removeValue(forKey: sid) ?? []).flatMap(\.images)
        draft?.clear()
        images.forEach { $0.release() }; forget(images)
        targets[key] = nil
        failureSummaries[key] = nil
        libraryRooms[key] = nil
        let files = files, records = records
        Task.detached { try? files.clearFolder(key); try? records.removeSession(sid) }
    }

    // MARK: Viewed images

    /// The display copy held for an image of a session, if it was loaded while the viewer was open.
    func viewedImage(_ key: String) -> ViewedImage? {
        _ = arrivals
        return viewed.get(remote, key: key)
    }

    /// Holds a display copy that was just decrypted. Not for an account that was left meanwhile.
    func keepViewed(_ key: String, _ image: ViewedImage) {
        if closed { return }
        viewed.put(remote, key: key, value: image)
        arrivals += 1
    }

    /// An event's preview as a picture, decoded once while the account is in use: it is decrypted
    /// content like the display copies, and goes with them.
    func preview(_ ref: PBImageRef) -> UIImage? {
        if ref.preview.isEmpty { return nil }
        let key = "\(ref.imageID)/\(ref.preview.count)/\(ref.path)" as NSString
        if let held = previews.object(forKey: key) { return held }
        guard let image = UIImage(data: ref.preview) else { return nil }
        if !closed { previews.setObject(image, forKey: key) }
        return image
    }

    // MARK: Drafts

    static func sessionKey(_ sid: String) -> String { "session:\(sid)" }
    static func projectKey(_ project: String) -> String { "new:\(project)" }
    static func sessionID(from key: String) -> String {
        key.hasPrefix("session:") ? String(key.dropFirst("session:".count)) : ""
    }

    // MARK: Core AttachmentEval

    func evaluateAttachments(_ request: PBAttachmentEvalScreenRequest) async -> PBAttachmentEvalViewModel {
        if let coreHost {
            return await coreHost.evaluateAttachments(request)
        }
        let locale = MessageLocale.current
        let client: CoreClient
        if let existing = fallbackCore {
            client = existing
        } else if let created = try? CoreClient() {
            fallbackCore = created
            client = created
        } else {
            return PBAttachmentEvalViewModel()
        }
        if fallbackLocale != locale {
            fallbackLocale = locale
            _ = try? client.dispatch(.with { $0.localeChanged.uiLocale = locale })
        }
        return (try? client.view(.with { $0.attachmentEval = request }).attachmentEval) ?? PBAttachmentEvalViewModel()
    }

    private func evalRequest(
        key: String,
        machineId: String? = nil,
        currentImages: Int32? = nil,
        currentFiles: Int32? = nil,
        candidates: [PBAttachmentCandidateInput] = [],
        problems: [PBImageProblemInput] = []
    ) -> PBAttachmentEvalScreenRequest {
        let d = draft(key)
        return .with {
            $0.machineID = machineId ?? targets[key] ?? ""
            $0.sessionID = Self.sessionID(from: key)
            $0.currentImages = currentImages ?? Int32(d.photos.count)
            $0.currentFiles = currentFiles ?? Int32(d.documents.count)
            $0.candidates = candidates
            $0.problems = problems
        }
    }

    static func candidates(for items: [PendingImage]) -> [PBAttachmentCandidateInput] {
        items.enumerated().map { idx, item in
            .with {
                if let file = item.file {
                    $0.name = file.name
                    $0.sizeBytes = file.size
                    $0.sizeUnknown = false
                    $0.isDirectory = false
                    $0.forceFile = true
                } else {
                    $0.name = "image-\(idx + 1).png"
                    $0.mimeType = "image/png"
                    $0.forceFile = false
                }
            }
        }
    }

    static func rejectionReason(from eval: PBAttachmentEvalViewModel) -> String {
        if eval.hasNotice {
            let text = eval.notice.rendered()
            if !text.isEmpty { return text }
        }
        if let first = eval.decisions.first(where: { !$0.accepted && $0.hasRejectReason }) {
            let text = first.rejectReason.rendered()
            if !text.isEmpty { return text }
        }
        if eval.hasFilesPickBlockedReason {
            let text = eval.filesPickBlockedReason.rendered()
            if !text.isEmpty { return text }
        }
        if eval.hasImagesPickBlockedReason {
            let text = eval.imagesPickBlockedReason.rendered()
            if !text.isEmpty { return text }
        }
        if eval.hasFileUploadBlockedReason {
            let text = eval.fileUploadBlockedReason.rendered()
            if !text.isEmpty { return text }
        }
        if eval.hasImageUploadBlockedReason {
            let text = eval.imageUploadBlockedReason.rendered()
            if !text.isEmpty { return text }
        }
        return ""
    }

    /// Re-evaluates all items currently in `key` against the target machine's Core limits.
    func evaluateDraft(_ key: String, machineId: String? = nil) async -> (allowed: Bool, eval: PBAttachmentEvalViewModel, reason: String) {
        let items = draft(key).images
        let cands = Self.candidates(for: items)
        let eval = await evaluateAttachments(
            evalRequest(key: key, machineId: machineId, currentImages: 0, currentFiles: 0, candidates: cands)
        )
        let allowed = items.isEmpty || (eval.decisions.count == cands.count && eval.decisions.allSatisfy(\.accepted))
        return (allowed, eval, allowed ? "" : Self.rejectionReason(from: eval))
    }

    /// Re-evaluates all document items currently in `key` against the target machine's Core limits.
    func evaluateDraftDocuments(_ key: String, machineId: String? = nil) async -> (allowed: Bool, eval: PBAttachmentEvalViewModel, reason: String) {
        let docs = draft(key).documents
        let cands = Self.candidates(for: docs)
        let eval = await evaluateAttachments(
            evalRequest(key: key, machineId: machineId, currentImages: 0, currentFiles: 0, candidates: cands)
        )
        let allowed = docs.isEmpty || (eval.decisions.count == cands.count && eval.decisions.allSatisfy(\.accepted))
        return (allowed, eval, allowed ? "" : Self.rejectionReason(from: eval))
    }

    /// Classifies all currently failed photos in `key` via Core and caches the per-image
    /// problems and single summary line (never called from SwiftUI `body`).
    func refreshProblems(_ key: String, machineId: String? = nil) async {
        let d = draft(key)
        var failedIDs: [String] = []
        var inputs: [PBImageProblemInput] = []
        for img in d.photos {
            if case .failed(let why) = img.state {
                failedIDs.append(img.id)
                inputs.append(why.input)
            } else {
                evaluatedProblems[img.id] = nil
            }
        }
        guard !inputs.isEmpty else {
            failureSummaries[key] = nil
            return
        }
        let eval = await evaluateAttachments(evalRequest(key: key, machineId: machineId, problems: inputs))
        guard !closed else { return }
        for (id, problem) in zip(failedIDs, eval.problems) {
            evaluatedProblems[id] = problem
        }
        failureSummaries[key] = eval.hasFailureSummary ? eval.failureSummary.rendered() : nil
    }

    private func hydrateRestoredFileMetadata(_ key: String) async {
        let d = draft(key)
        let missing = d.documents.filter { $0.file?.formattedSize == nil }
        guard !missing.isEmpty else { return }
        let candidates: [PBAttachmentCandidateInput] = missing.compactMap { item in
            guard let file = item.file else { return nil }
            return .with {
                $0.name = file.name
                $0.sizeBytes = file.size
                $0.forceFile = true
            }
        }
        let eval = await evaluateAttachments(evalRequest(key: key, candidates: candidates))
        guard !closed else { return }
        for (item, decision) in zip(missing, eval.decisions) {
            guard var file = item.file else { continue }
            if !decision.displayName.isEmpty { file.displayName = decision.displayName }
            if !decision.formattedSize.isEmpty { file.formattedSize = decision.formattedSize }
            item.file = file
        }
    }

    private static func imageMimeType(for provider: NSItemProvider) -> String {
        for identifier in provider.registeredTypeIdentifiers {
            if let type = UTType(identifier), type.conforms(to: .image), let mime = type.preferredMIMEType {
                return mime
            }
        }
        return "image/jpeg"
    }

    /// A draft's images, restored from the sealed store when first asked for.
    func draft(_ key: String) -> ImageDraft {
        if let d = drafts[key] { return d }
        let d = ImageDraft(key: key, files: files)
        drafts[key] = d
        restoreDraft(d)
        return d
    }

    func retryDraft(_ key: String) {
        let d = draft(key)
        guard !d.restoring else { return }
        restoreDraft(d)
    }

    private func restoreDraft(_ d: ImageDraft) {
        let key = d.key
        d.restoring = true
        restorations[key] = Task { [weak self] in
            do { try await d.restoreAsync() }
            catch { return } // ImageDraft retains the failure and blocks disk writes until retry.
            guard let self, !closed, !Task.isCancelled else { return }
            for img in d.images { makeThumb(img) }
            await hydrateRestoredFileMetadata(key)
        }
    }

    func restoredDraft(_ key: String) async {
        await restorations[key]?.value
    }

    func restoredOutgoing(_ sid: String) async {
        await restorations["out:" + sid]?.value
    }

    func thumb(_ img: PendingImage) -> UIImage? { thumbs[img.id] }

    private func makeThumb(_ img: PendingImage) {
        guard let bytes = img.processed?.bytes else { return }
        let id = img.id
        Task {
            let made = await Task.detached {
                thumbnailImage(bytes, maxLong: 256).map { UIImage(cgImage: $0) }
            }.value
            if let made, !closed { thumbs[id] = made }
        }
    }

    /// The machine a draft's images go to from now on; they are sent there when it can take them.
    func aim(_ key: String, at machineId: String?, blocked: Bool, caps: AttachmentCapabilities = AttachmentCapabilities()) {
        let known = aimedKeys.contains(key)
        let changed = known && targets[key] != machineId
        if machineId != nil { aimedKeys.insert(key) }
        targets[key] = machineId
        fileCaps[key] = caps
        if blocked { blockedKeys.insert(key) } else { blockedKeys.remove(key) }
        #if DEBUG
            if frozen { return }
        #endif
        let d = draft(key)
        for item in d.images {
            let initialDifferentTarget = !known && machineId != nil && item.historyMachineId != nil && item.historyMachineId != machineId
            if changed || initialDifferentTarget {
                uploads.retarget(item)
                if !item.isFile && item.needsProcessing {
                    uploads.process(d, item, machine: self.machine(key))
                }
            }
        }
        if !caps.supportsFiles {
            for item in d.documents where item.hasSource {
                item.invalidate()
                let detail = caps.fileUploadBlocked.map { LocalizedMessage(raw: $0) }
                item.state = .failed(error: AttachmentFailure(kind: .processingFailed, detail: detail))
            }
        }
        if let machineId, !blocked {
            for item in d.toSend(machineId: machineId) where !item.isFile {
                uploads.upload(item, machineId: machineId)
            }
            if !d.documents.isEmpty {
                Task { [weak self] in
                    guard let self, !closed, targets[key] == machineId, !blockedKeys.contains(key) else { return }
                    let check = await evaluateDraftDocuments(key, machineId: machineId)
                    guard !closed, targets[key] == machineId, !blockedKeys.contains(key) else { return }
                    let current = draft(key)
                    if !check.allowed {
                        let detail = check.reason.isEmpty ? nil : LocalizedMessage(raw: check.reason)
                        for item in current.documents where item.hasSource {
                            item.invalidate()
                            item.state = .failed(error: AttachmentFailure(kind: .processingFailed, detail: detail))
                        }
                        return
                    }
                    for item in current.toSend(machineId: machineId) where item.isFile {
                        uploads.upload(item, machineId: machineId)
                    }
                }
            }
        }
    }

    private func machine(_ key: String) -> @MainActor () -> String? {
        { [weak self] in
            guard let self, !closed, !blockedKeys.contains(key) else { return nil }
            return targets[key]
        }
    }

    /// ＋ was tapped.
    func plus(_ key: String, machineId: String? = nil, caps: AttachmentCapabilities = AttachmentCapabilities()) {
        if let blocked = caps.imageUploadBlocked {
            say(key, blocked)
            return
        }
        if caps.supportsFiles {
            picking[key] = .attachments
            return
        }
        Task { [weak self] in
            guard let self, !closed else { return }
            let eval = await evaluateAttachments(evalRequest(key: key, machineId: machineId))
            guard !closed else { return }
            libraryRooms[key] = max(Int(eval.libraryRoom), 1)
            guard eval.canPickImages else {
                if eval.hasImagesPickBlockedReason {
                    say(key, eval.imagesPickBlockedReason.rendered())
                }
                return
            }
            // Asking whether the clipboard has an image does not read it, so nothing is asked of the user yet.
            picking[key] = UIPasteboard.general.hasImages ? .choose : .photos
        }
    }

    /// 照片和视频 was chosen: evaluated via Core `AttachmentEval` for room and block reasons.
    func library(_ key: String, machineId: String? = nil, caps: AttachmentCapabilities = AttachmentCapabilities(), offerPaste: Bool = false) {
        Task { [weak self] in
            guard let self, !closed else { return }
            let eval = await evaluateAttachments(evalRequest(key: key, machineId: machineId))
            guard !closed else { return }
            libraryRooms[key] = max(Int(eval.libraryRoom), 1)
            guard eval.canPickGallery else {
                if eval.hasGalleryBlockedReason {
                    say(key, eval.galleryBlockedReason.rendered())
                }
                return
            }
            picking[key] = (offerPaste && UIPasteboard.general.hasImages) ? .choose : .photos
        }
    }

    /// 文件 was chosen: evaluated via Core `AttachmentEval` for file limits and block reasons.
    func documents(_ key: String, machineId: String? = nil, caps: AttachmentCapabilities = AttachmentCapabilities()) {
        Task { [weak self] in
            guard let self, !closed else { return }
            let eval = await evaluateAttachments(evalRequest(key: key, machineId: machineId))
            guard !closed else { return }
            guard eval.canPickFiles else {
                if eval.hasFilesPickBlockedReason {
                    say(key, eval.filesPickBlockedReason.rendered())
                }
                return
            }
            picking[key] = .documents
        }
    }

    /// Adds picked/pasted image sources after Core `AttachmentEval` routing and admission.
    func add(_ key: String, _ picked: [Source], machineId: String? = nil) {
        Task { [weak self] in
            guard let self, !closed else { return }
            await restoredDraft(key)
            guard !closed else { return }
            let candidates: [PBAttachmentCandidateInput] = picked.enumerated().map { idx, source in
                .with {
                    switch source {
                    case .library(let p), .provider(let p):
                        $0.name = p.suggestedName ?? "image-\(idx + 1).jpg"
                        $0.mimeType = Self.imageMimeType(for: p)
                    case .data:
                        $0.name = "image-\(idx + 1).png"
                        $0.mimeType = "image/png"
                    }
                    $0.forceFile = false
                }
            }
            let eval = await evaluateAttachments(evalRequest(key: key, machineId: machineId, candidates: candidates))
            guard !closed else { return }
            let d = draft(key)
            var accepted: [Source] = []
            for (source, decision) in zip(picked, eval.decisions) where decision.accepted && decision.route == .image {
                accepted.append(source)
            }
            d.notice = eval.hasNotice ? eval.notice.rendered() : ""
            guard !accepted.isEmpty else { return }
            let uris = accepted.map { _ in "picked:\(newRequestId())" }
            for (uri, source) in zip(uris, accepted) { sources[uri] = source }
            uploads.add(d, uris: uris, machine: machine(key))
        }
    }

    /// Mixed photo/video library selection: routes images and videos together via Core `AttachmentEval`
    /// (`force_file = false`), discarding any rejected video temp copy immediately.
    func addLibraryItems(_ key: String, _ providers: [NSItemProvider], machineId: String? = nil, caps: AttachmentCapabilities) {
        guard !providers.isEmpty else { return }
        Task { [weak self] in
            guard let self, !closed else { return }
            await restoredDraft(key)
            guard !closed else { return }
            enum PreparedItem {
                case image(NSItemProvider, PBAttachmentCandidateInput)
                case video(PickedFileInspection, PBAttachmentCandidateInput)
            }
            var items: [PreparedItem] = []
            var copyFailed = false
            for (idx, provider) in providers.enumerated() {
                if provider.hasItemConformingToTypeIdentifier(UTType.movie.identifier) {
                    do {
                        let video: LibraryVideo = try await withCheckedThrowingContinuation { done in
                            _ = provider.loadTransferable(type: LibraryVideo.self) { done.resume(with: $0) }
                        }
                        let inspection = try await Task.detached { try PickedFile.inspect(url: video.url) }.value
                        items.append(.video(inspection, inspection.candidate(forceFile: false)))
                    } catch {
                        copyFailed = true
                    }
                } else {
                    let candidate = PBAttachmentCandidateInput.with {
                        $0.name = provider.suggestedName ?? "image-\(idx + 1).jpg"
                        $0.mimeType = Self.imageMimeType(for: provider)
                        $0.forceFile = false
                    }
                    items.append(.image(provider, candidate))
                }
            }
            guard !closed else {
                for item in items { if case .video(let insp, _) = item { LibraryVideos.discard(insp.url) } }
                return
            }
            let candidates = items.map { item -> PBAttachmentCandidateInput in
                switch item {
                case .image(_, let c), .video(_, let c): return c
                }
            }
            let eval = await evaluateAttachments(evalRequest(key: key, machineId: machineId, candidates: candidates))
            guard !closed else {
                for item in items { if case .video(let insp, _) = item { LibraryVideos.discard(insp.url) } }
                return
            }
            let d = draft(key)
            var imageSources: [Source] = []
            var acceptedFiles: [PendingImage] = []
            for (item, decision) in zip(items, eval.decisions) {
                switch item {
                case .image(let provider, _):
                    if decision.accepted && decision.route == .image {
                        imageSources.append(.library(provider))
                    }
                case .video(let inspection, _):
                    if decision.accepted && (decision.route == .videoFile || decision.route == .file),
                       let picked = try? inspection.makePickedFile(displayName: decision.displayName, formattedSize: decision.formattedSize) {
                        let pending = PendingImage(id: newRequestId(), uri: nil)
                        pending.file = picked
                        pending.state = .failed(error: AttachmentFailure(kind: .uploadFailed, detail: L10nMessage.imageIosNeedsUploadingToTheComputerAgain()))
                        acceptedFiles.append(pending)
                    } else {
                        LibraryVideos.discard(inspection.url)
                    }
                }
            }
            if eval.hasNotice {
                say(key, eval.notice.rendered())
            } else if copyFailed {
                say(key, L10n.imageIosCannotReadThisVideoSelectItAgain())
            }
            if !imageSources.isEmpty {
                let uris = imageSources.map { _ in "picked:\(newRequestId())" }
                for (uri, source) in zip(uris, imageSources) { sources[uri] = source }
                uploads.add(d, uris: uris, machine: machine(key))
            }
            if !acceptedFiles.isEmpty {
                d.append(acceptedFiles)
                if let target = self.machine(key)() {
                    for item in acceptedFiles { uploads.upload(item, machineId: target) }
                }
            }
        }
    }

    /// Import documents from the explicit File picker (`force_file = true`) via Core `AttachmentEval`.
    /// Even images picked from Files are routed as files unchanged. Rejected copies are discarded at once.
    func addFiles(_ key: String, _ urls: [URL], machineId: String? = nil, caps: AttachmentCapabilities) {
        guard !urls.isEmpty else { return }
        if let blocked = caps.fileUploadBlocked {
            urls.forEach { LibraryVideos.discard($0) }
            say(key, blocked)
            return
        }
        Task { [weak self] in
            guard let self, !closed else {
                urls.forEach { LibraryVideos.discard($0) }
                return
            }
            await restoredDraft(key)
            guard !closed else {
                urls.forEach { LibraryVideos.discard($0) }
                return
            }
            var inspected: [PickedFileInspection] = []
            var firstReadError: Error?
            for url in urls {
                do {
                    let info = try await Task.detached { try PickedFile.inspect(url: url) }.value
                    inspected.append(info)
                } catch {
                    if firstReadError == nil { firstReadError = error }
                    LibraryVideos.discard(url)
                }
            }
            guard !closed else {
                inspected.forEach { LibraryVideos.discard($0.url) }
                return
            }
            guard !inspected.isEmpty else {
                if let firstReadError { say(key, errorText(firstReadError)) }
                return
            }
            let candidates = inspected.map { $0.candidate(forceFile: true) }
            let eval = await evaluateAttachments(evalRequest(key: key, machineId: machineId, candidates: candidates))
            guard !closed else {
                inspected.forEach { LibraryVideos.discard($0.url) }
                return
            }
            let d = draft(key)
            var accepted: [PendingImage] = []
            for (info, decision) in zip(inspected, eval.decisions) {
                if decision.accepted && (decision.route == .file || decision.route == .videoFile),
                   let file = try? info.makePickedFile(displayName: decision.displayName, formattedSize: decision.formattedSize) {
                    let item = PendingImage(id: newRequestId(), uri: nil)
                    item.file = file
                    item.state = .failed(error: AttachmentFailure(kind: .uploadFailed, detail: L10nMessage.imageIosNeedsUploadingToTheComputerAgain()))
                    accepted.append(item)
                } else {
                    LibraryVideos.discard(info.url)
                }
            }
            if eval.hasNotice {
                say(key, eval.notice.rendered())
            } else if let firstReadError {
                say(key, errorText(firstReadError))
            }
            if !accepted.isEmpty {
                d.append(accepted)
                if let target = self.machine(key)() {
                    for item in accepted { uploads.upload(item, machineId: target) }
                }
            }
        }
    }

    /// Adds the images on the clipboard via Core `AttachmentEval`.
    func paste(_ key: String, machineId: String? = nil) {
        let providers = UIPasteboard.general.itemProviders.filter {
            $0.hasItemConformingToTypeIdentifier(UTType.image.identifier)
        }
        if providers.isEmpty {
            say(key, .clipboardEmpty)
        } else {
            add(key, providers.map { .provider($0) }, machineId: machineId)
        }
    }

    func retry(_ key: String, _ img: PendingImage) {
        if img.isFile && !img.hasSource {
            img.invalidate()
            img.state = .failed(error: AttachmentFailure(kind: .sourceMissing, detail: L10nMessage.attachmentFileMissing()))
            return
        }
        if img.isFile {
            Task { [weak self] in
                guard let self, !closed else { return }
                let check = await evaluateDraftDocuments(key, machineId: targets[key])
                guard !closed else { return }
                if !check.allowed {
                    img.invalidate()
                    let detail = check.reason.isEmpty ? nil : LocalizedMessage(raw: check.reason)
                    img.state = .failed(error: AttachmentFailure(kind: .processingFailed, detail: detail))
                    return
                }
                uploads.retry(draft(key), img, machine: machine(key))
            }
            return
        }
        uploads.retry(draft(key), img, machine: machine(key))
    }

    func remove(_ key: String, _ img: PendingImage) {
        draft(key).remove(img)
        uploads.discard(img)
        forget([img])
    }

    func setOriginal(_ key: String, _ on: Bool) {
        uploads.setOriginal(draft(key), on, machine: machine(key))
    }

    /// Takes images out of a draft once a started session carries them.
    func used(_ key: String, _ taken: [PendingImage], machineId: String? = nil) {
        draft(key).detach(taken)
        for img in taken { uploads.discard(img, keeping: machineId ?? img.historyMachineId); img.release() }
        forget(taken)
    }

    /// Lets go of what is held for images that are gone for good.
    private func forget(_ images: [PendingImage]) {
        for img in images {
            if img.isFile { LibraryVideos.discard(img.file) }
            thumbs[img.id] = nil
            evaluatedProblems[img.id] = nil
            if let uri = img.uri { sources[uri] = nil }
        }
    }

    /// The bytes that were picked at `uri`, read once and then held for the run. One file is read at a
    /// time: several picked together would otherwise all be coming into memory at once.
    private func read(_ uri: String) async throws -> Data {
        if reading {
            await withCheckedContinuation { readers.append($0) }
        } else {
            reading = true
        }
        defer {
            if readers.isEmpty { reading = false } else { readers.removeFirst().resume() }
        }
        let unreadable = AttachmentFailure(kind: .decode)
        guard let source = sources[uri] else { throw AttachmentFailure(kind: .sourceMissing) }
        let data: Data
        switch source {
        case .data(let d): return d
        case .library(let item):
            do {
                let photo: PickedPhoto = try await withCheckedThrowingContinuation { done in
                    _ = item.loadTransferable(type: PickedPhoto.self) { done.resume(with: $0) }
                }
                data = try photo.read.get()
            } catch { throw AttachmentFailure.reading(error) }
        case .provider(let provider):
            // The clipboard hands over bytes, not a file: there is no size to ask for first.
            data = try await withCheckedThrowingContinuation { done in
                _ = provider.loadDataRepresentation(forTypeIdentifier: UTType.image.identifier) {
                    d, error in
                    if let d {
                        done.resume(returning: d)
                    } else {
                        done.resume(throwing: error.map(AttachmentFailure.reading) ?? unreadable)
                    }
                }
            }
        }
        // Not held when it is more than is ever read of one: preparing refuses it in words.
        if Int64(data.count) <= pickedReadLimit(original: false), sources[uri] != nil {
            sources[uri] = .data(data)
        }
        return data
    }

    // MARK: Notices

    /// A one-line reason shown by the strip, e.g. why ＋ does nothing.
    func say(_ key: String, _ text: String) {
        notices[key] = Notice(message: .raw(text))
    }

    func say(_ key: String, _ message: AttachmentNotice) {
        notices[key] = Notice(message: message)
    }

    func clearNotice(_ key: String, id: UUID) {
        if notices[key]?.id == id { notices[key] = nil }
    }

    // MARK: Messages with images

    /// Sends a message with the images of a session's draft; false when the
    /// draft has none. They are first kept on disk, sealed, off the main
    /// actor; only once that worked is the message sent and the images taken
    /// out of the draft. If keeping them failed nothing is sent, the draft
    /// stays as it is and `done` gets the reason.
    func send(
        sid: String, machineId: String, text: String, mode: PBSendMode,
        keep: (@Sendable (String, OutgoingRecord, Bool) throws -> Void)? = nil,
        submit: @escaping @MainActor @Sendable (Outgoing) -> Void,
        done: @escaping @MainActor (Result<Void, Error>) -> Void
    ) -> Bool {
        let draft = draft(Self.sessionKey(sid))
        let picked = draft.images
        if picked.isEmpty { return false }
        do { try remote.requireFileCapability(machineId: machineId, hasFiles: picked.contains(where: \.isFile)) }
        catch {
            Task { done(.failure(error)) }
            return true
        }
        uploads.sendAll(draft, machineId: machineId)
        guard draft.ready(machineId: machineId) else {
            Task { done(.failure(UserError(L10nMessage.imageIosAttachmentsNeedUploadingAgainCompleteTheUploadBeforeSending()))) }
            return true
        }
        let key = Self.sessionKey(sid)
        let original = draft.original
        let o = Outgoing(
            requestId: newRequestId(), text: text, imageIds: draft.ids(machineId: machineId), fileIds: draft.fileIds(machineId: machineId),
            images: picked, original: original, mode: mode)
        let record = o.record
        let records = records
        Task {
            if coreHost != nil {
                let check = await evaluateDraft(key, machineId: machineId)
                if !check.allowed {
                    let reason = check.reason
                    if !reason.isEmpty { say(key, reason) }
                    done(.failure(UserError(reason)))
                    return
                }
            }
            let kept: Result<Void, Error> = await Task.detached {
                Result {
                    try (keep ?? { try records.save(sid: $0, $1, original: $2) })(
                        sid, record, original)
                }
            }.value
            if case .failure = kept {
                done(kept)
                return
            }
            // Logged out while the images were being written: the message is not the next account's to
            // send, and logging out has taken what was written.
            if closed || forgottenSessions.contains(sid) {
                try? await Task.detached { try records.remove(sid: sid, requestId: o.requestId) }.value
                done(.failure(UserError(closed ? L10nMessage.attachmentIosSignedOutOfTheAccount() : L10nMessage.imageIosSessionRemoved())))
                return
            }
            if coreHost != nil {
                let check = await evaluateDraft(key, machineId: machineId)
                if !check.allowed {
                    try? await Task.detached { try records.remove(sid: sid, requestId: o.requestId) }.value
                    let reason = check.reason
                    if !reason.isEmpty { say(key, reason) }
                    done(.failure(UserError(reason)))
                    return
                }
            }
            do { try remote.requireFileCapability(machineId: machineId, hasFiles: !o.fileIds.isEmpty) }
            catch {
                try? await Task.detached { try records.remove(sid: sid, requestId: o.requestId) }.value
                done(.failure(error))
                return
            }
            // Saving originals can outlast an upload. Nothing has been sent yet.
            guard picked.filter({ !$0.isFile }).compactMap(\.imageId) == o.imageIds,
                picked.filter(\.isFile).compactMap(\.imageId) == o.fileIds,
                picked.allSatisfy({ $0.readyFor(machineId) })
            else {
                try? await Task.detached { try records.remove(sid: sid, requestId: o.requestId) }
                    .value
                uploads.sendAll(draft, machineId: machineId)
                done(.failure(UserError(L10nMessage.imageIosAttachmentsNeedUploadingAgainCompleteTheUploadBeforeSending())))
                return
            }
            // The immutable request owns these transports before the composer lets them go.
            for img in picked { img.protectUpload(machineId: machineId, transferId: img.transferId, requestId: o.requestId) }
            // Prepared/restored bytes may reach the outbox before their preview
            // has been made. Sending must not depend on the draft having drawn it.
            for img in picked where thumb(img) == nil { makeThumb(img) }
            draft.detach(picked)
            hold(sid, o)
            done(kept)
            submit(o)
        }
        return true
    }

    /// Re-uploads locally held attachments under `newRequestId` for promote / retry / undo,
    /// saves the new outgoing record, holds it in memory, and then invokes `submit`.
    func resend(
        sid: String,
        machineId: String,
        sourceRequestId: String,
        newRequestId: String,
        text: String,
        mode: PBSendMode,
        keep: (@Sendable (String, OutgoingRecord, Bool) throws -> Void)? = nil,
        submit: @escaping @MainActor @Sendable (Outgoing) -> Void,
        done: @escaping @MainActor (Result<Void, Error>) -> Void
    ) {
        Task { [weak self] in
            guard let self, !closed else { return }
            var base = heldOutgoing(sid, requestId: sourceRequestId)
            if base == nil {
                let rec = try? await Task.detached { [records = self.records] in
                    try records.loadRecords(sid: sid).first { $0.requestId == sourceRequestId }
                }.value
                base = rec.map(OutgoingRecords.revive)
            }
            guard let base else {
                done(.failure(UserError(L10nMessage.imageIosAttachmentsNeedUploadingAgainCompleteTheUploadBeforeSending())))
                return
            }
            do {
                try remote.requireFileCapability(machineId: machineId, hasFiles: base.images.contains(where: \.isFile))
                var newImageIds: [String] = []
                var newFileIds: [String] = []
                var newImages: [PendingImage] = []
                let ch: any UploadChannel = {
                    #if DEBUG
                    if !live { return SampleUploads() }
                    #endif
                    return remote.uploadChannel(machineId: machineId)
                }()
                for img in base.images {
                    let tid = RACore.newRequestId()
                    let clone = PendingImage(id: RACore.newRequestId(), uri: img.uri)
                    if img.isFile {
                        guard let file = img.file, file.hasSource else {
                            throw AttachmentFailure(kind: .sourceMissing, detail: L10nMessage.attachmentFileMissing())
                        }
                        let up = try await uploadFile(ch, transferId: tid, source: file)
                        var copied = file
                        copied.machineId = machineId
                        copied.transfers = [:]
                        clone.file = copied
                        clone.transferId = tid
                        clone.machineId = machineId
                        clone.imageId = up.imageId
                        clone.expiresMs = up.expiresMs
                        clone.state = .ready
                        clone.protectUpload(machineId: machineId, transferId: tid, requestId: newRequestId)
                        newFileIds.append(up.imageId)
                    } else {
                        guard let processed = img.processed else {
                            throw UserError(L10nMessage.imageIosAttachmentsNeedUploadingAgainCompleteTheUploadBeforeSending())
                        }
                        let up = try await uploadImage(ch, transferId: tid, data: processed.bytes)
                        clone.processed = processed
                        clone.transferId = tid
                        clone.machineId = machineId
                        clone.imageId = up.imageId
                        clone.expiresMs = up.expiresMs
                        clone.state = .ready
                        clone.protectUpload(machineId: machineId, transferId: tid, requestId: newRequestId)
                        newImageIds.append(up.imageId)
                    }
                    newImages.append(clone)
                }
                let o = Outgoing(
                    requestId: newRequestId, text: text, imageIds: newImageIds, fileIds: newFileIds,
                    images: newImages, original: base.original, mode: mode
                )
                let record = o.record
                let original = o.original
                try await Task.detached { [records = self.records] in
                    try (keep ?? { try records.save(sid: $0, $1, original: $2) })(sid, record, original)
                }.value
                guard !closed && !forgottenSessions.contains(sid) else {
                    try? await Task.detached { [records = self.records] in
                        try records.remove(sid: sid, requestId: o.requestId)
                    }.value
                    throw UserError(closed ? L10nMessage.attachmentIosSignedOutOfTheAccount() : L10nMessage.imageIosSessionRemoved())
                }
                for img in o.images where thumb(img) == nil { makeThumb(img) }
                hold(sid, o)
                done(.success(()))
                submit(o)
            } catch {
                done(.failure(error))
            }
        }
    }

    /// Brings back the messages with images a run that ended left behind, once per session and run.
    func restoreOutgoing(_ sid: String) {
        guard restored.insert(sid).inserted else { return }
        let records = records
        restorations["out:" + sid] = Task { [weak self] in
            do {
                let list = try await Task.detached { try records.loadRecords(sid: sid) }.value
                guard let self, !closed, !Task.isCancelled else { return }
                outgoingRestoreErrors[sid] = nil
                for record in list where self.heldOutgoing(sid, requestId: record.requestId) == nil {
                    let o = OutgoingRecords.revive(record)
                    for img in o.images { makeThumb(img) }
                    hold(sid, o)
                    var restoredList = self.restoredOutgoing[sid] ?? []
                    if !restoredList.contains(where: { $0.requestId == o.requestId }) {
                        restoredList.append(o)
                        self.restoredOutgoing[sid] = restoredList
                    }
                }
            } catch {
                guard let self, !closed, !Task.isCancelled else { return }
                restored.remove(sid)
                outgoingRestoreErrors[sid] = L10n.imageIosCouldNotRestorePendingRecordsRetry()
            }
        }
    }

    func heldOutgoing(_ sid: String, requestId: String) -> Outgoing? {
        kept[sid]?.first { $0.requestId == requestId }
    }

    func adoptRestored(_ sid: String, requestId: String) {
        restoredOutgoing[sid]?.removeAll { $0.requestId == requestId }
        if restoredOutgoing[sid]?.isEmpty == true { restoredOutgoing[sid] = nil }
    }

    func dismissRestored(_ sid: String, requestId: String) {
        adoptRestored(sid, requestId: requestId)
        if let o = heldOutgoing(sid, requestId: requestId) {
            release(sid, o)
        } else {
            dropRecord(sid, requestId)
        }
    }

    private func evaluatePutBack(sid: String, items: [PendingImage]) async -> Bool {
        if items.isEmpty { return true }
        let key = Self.sessionKey(sid)
        let d = draft(key)
        guard !d.restoring, d.restoreError == nil else {
            d.noticeMessage = .restoreUnavailable
            return false
        }
        let hasFiles = items.contains(where: \.isFile)
        if coreHost == nil {
            guard canRestoreFiles(sid, hasFiles: hasFiles) else { return false }
        }
        let machineId = remote.current.sessions[sid]?.machineId ?? targets[key] ?? ""
        let cands = Self.candidates(for: items)
        let eval = await evaluateAttachments(evalRequest(key: key, machineId: machineId, candidates: cands))
        guard eval.decisions.count == cands.count, eval.decisions.allSatisfy(\.accepted) else {
            let reason = Self.rejectionReason(from: eval)
            if !reason.isEmpty {
                d.notice = reason
                say(key, reason)
            }
            return false
        }
        return true
    }

    func canPutBack(sid: String, _ o: Outgoing) async -> Bool {
        let hasFiles = !o.fileIds.isEmpty || o.images.contains(where: \.isFile)
        if coreHost == nil {
            guard canRestoreFiles(sid, hasFiles: hasFiles) else { return false }
        }
        if o.images.isEmpty { return true }
        return await evaluatePutBack(sid: sid, items: o.images)
    }

    /// Puts a message's images back into its session's draft; false when they do not fit, and then nothing moved.
    func putBack(sid: String, _ o: Outgoing, releaseFailure: Bool = false) async -> Bool {
        let hasFiles = !o.fileIds.isEmpty || o.images.contains(where: \.isFile)
        if coreHost == nil {
            guard canRestoreFiles(sid, hasFiles: hasFiles) else { return false }
        }
        if o.images.isEmpty {
            adoptRestored(sid, requestId: o.requestId)
            release(sid, o)
            return true
        }
        guard await evaluatePutBack(sid: sid, items: o.images) else { return false }
        adoptRestored(sid, requestId: o.requestId)
        let draft = draft(Self.sessionKey(sid))
        if o.state == .failed || releaseFailure {
            for img in o.images { img.releaseUploadProtection(requestId: o.requestId) }
        }
        guard draft.attach(o.images, wereOriginal: o.original) else { return false }
        letGo(sid, o)
        // Its images live on in the draft. Its record goes once they are on disk there: until then it is the
        // only copy a reclaimed process would find.
        draft.whenKept(o.images.map(\.id)) { [weak self] in self?.dropRecord(sid, o.requestId) }
        return true
    }

    private func canRestoreFiles(_ sid: String, hasFiles: Bool) -> Bool {
        let machine = remote.current.sessions[sid]?.machineId ?? ""
        do { try remote.requireFileCapability(machineId: machine, hasFiles: hasFiles); return true }
        catch { say(Self.sessionKey(sid), errorText(error)); return false }
    }

    /// History can hold remote-only files. Evaluate via Core `AttachmentEval` before creating
    /// placeholders or appending any part of the message to the composer.
    @discardableResult
    func putBack(sid: String, text: String, files: [PBSentFile], store: SessionStore) async -> Bool {
        let picked = files.map { file in
            let item = PendingImage(id: newRequestId(), uri: nil)
            item.file = PickedFile(remote: file)
            item.state = .failed(error: AttachmentFailure(kind: .sourceMissing, detail: L10nMessage.attachmentFileMissing()))
            return item
        }
        guard await evaluatePutBack(sid: sid, items: picked) else { return false }
        guard draft(Self.sessionKey(sid)).attach(picked) else { return false }
        store.appendToDraft(sid, text)
        return true
    }

    /// Follows a message until Core settles it (`pruneSettled`) or the user dismisses it.
    private func hold(_ sid: String, _ o: Outgoing) {
        if !(kept[sid] ?? []).contains(where: { $0.requestId == o.requestId }) {
            kept[sid, default: []].append(o)
        }
    }

    private func letGo(_ sid: String, _ o: Outgoing) {
        kept[sid]?.removeAll { $0 === o || $0.requestId == o.requestId }
        if kept[sid]?.isEmpty == true {
            kept[sid] = nil
        }
    }

    /// Releases local image bytes/thumbnails and disk records once Core no longer lists `requestId`
    /// as in-flight (`outgoing` / `pendingInputs` / `unsentOnExit`), keeping crash-restored records
    /// until dismissed, put back, retried, or observed in Core's settled timeline.
    func pruneSettled(_ sid: String, activeRequestIDs: Set<String>, timelineSettledRequestIDs: Set<String> = []) {
        let restoredIDs = Set((restoredOutgoing[sid] ?? []).map(\.requestId))
        for o in kept[sid] ?? [] {
            if timelineSettledRequestIDs.contains(o.requestId) {
                adoptRestored(sid, requestId: o.requestId)
                release(sid, o, accepted: true)
            } else if !restoredIDs.contains(o.requestId), !activeRequestIDs.contains(o.requestId) {
                release(sid, o, accepted: true)
            }
        }
    }

    /// Releases a message whose upload failed or was dismissed directly.
    func release(_ sid: String, _ o: Outgoing, accepted: Bool = false) {
        adoptRestored(sid, requestId: o.requestId)
        letGo(sid, o)
        dropRecord(sid, o.requestId)
        for img in o.images {
            if o.state == .failed || !accepted { img.releaseUploadProtection(requestId: o.requestId) }
            uploads.discard(img, keeping: accepted ? img.historyMachineId : nil)
            img.release()
        }
        forget(o.images)
    }

    func release(sid: String, requestId: String, accepted: Bool = false) {
        if let o = heldOutgoing(sid, requestId: requestId) {
            release(sid, o, accepted: accepted)
        } else {
            adoptRestored(sid, requestId: requestId)
            dropRecord(sid, requestId)
        }
    }

    private func dropRecord(_ sid: String, _ requestId: String) {
        let records = records
        Task.detached { try? records.remove(sid: sid, requestId: requestId) }
    }

    #if DEBUG
        /// Resolve a debug pick when the signal arrives, after project settings have loaded.
        func debugAddFiles(_ key: String, _ urls: [URL]) {
            addFiles(key, urls, machineId: targets[key], caps: fileCaps[key] ?? AttachmentCapabilities())
        }

        /// Shows a message with images as it would stand after sending, for sample data.
        func debugShow(_ sid: String, _ o: Outgoing) {
            guard heldOutgoing(sid, requestId: o.requestId) == nil else { return }
            hold(sid, o)
            var list = restoredOutgoing[sid] ?? []
            list.append(o)
            restoredOutgoing[sid] = list
            for img in o.images { makeThumb(img) }
        }

        /// Puts images in a draft as they would stand, without preparing or sending them, for sample data.
        func debugShow(
            _ key: String, _ images: [PendingImage], original: Bool = false, notice: String = ""
        ) {
            let d = draft(key)
            guard d.images.isEmpty else { return }
            d.append(images)
            _ = d.setOriginal(original)
            d.notice = notice
            for img in images { makeThumb(img) }
        }
    #endif
}

#if DEBUG
    /// A computer that takes every upload, slowly enough to be seen: for sample data.
    private struct SampleUploads: UploadChannel {
        private func status(_ id: String, done: Bool, persisted: Int64 = 0) -> PBImageUploadStatus {
            .with {
                $0.transferID = id
                $0.persistedBytes = persisted
                $0.maxChunkBytes = 64 * 1024
                $0.state = done ? .complete : .receiving
                $0.expiresMs = Int64.max
                $0.imageID = done ? "sample-\(id.prefix(8))" : ""
            }
        }

        func begin(transferId: String, format: PBImageFormat, total: Int64, sha256: Data)
            async throws -> PBImageUploadStatus
        {
            status(transferId, done: false)
        }

        func beginFile(transferId: String, name: String, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
            status(transferId, done: false)
        }
        func abort(transferId: String) async throws {}

        func put(transferId: String, offset: Int64, data: Data) async throws -> PBImageUploadStatus
        {
            try await Task.sleep(for: .milliseconds(120))
            return status(transferId, done: false, persisted: offset + Int64(data.count))
        }

        func status(transferId: String) async throws -> PBImageUploadStatus {
            status(transferId, done: false)
        }
        func complete(transferId: String) async throws -> PBImageUploadStatus {
            status(transferId, done: true)
        }
    }
#endif

/// A video chosen in the library, as a file of this app's: the picker hands
/// over a copy that lives only while it is read, so it is copied again into
/// a folder of its own (`LibraryVideos`), named as it was.
struct LibraryVideo: Transferable {
    let url: URL

    static var transferRepresentation: some TransferRepresentation {
        FileRepresentation(importedContentType: .movie) { received in
            LibraryVideo(url: try LibraryVideos.copy(received.file))
        }
    }
}

/// The copies of videos from the library, one folder each under the
/// temporary directory. A copy lives as long as the draft or the message
/// that holds it: it is deleted when the video is taken out of the draft,
/// once the message that carried it is settled or dropped, when it could
/// not be added, and with the account's drafts.
enum LibraryVideos {
    static let root = FileManager.default.temporaryDirectory.appendingPathComponent("library-videos", isDirectory: true)

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

    /// Deletes the copy at `url` and its folder; a file that is not one of these copies is left alone.
    static func discard(_ url: URL, root: URL = root) {
        let folder = url.standardizedFileURL.resolvingSymlinksInPath().deletingLastPathComponent()
        let base = root.standardizedFileURL.resolvingSymlinksInPath()
        guard folder.deletingLastPathComponent().path == base.path else { return }
        try? FileManager.default.removeItem(at: folder)
    }

    /// The copy a picked file stands for, if it is one: its bookmark resolved.
    static func discard(_ file: PickedFile?, root: URL = root) {
        guard let file, file.hasSource, let url = try? PickedFile.resolve(file.bookmark).url else { return }
        discard(url, root: root)
    }

    /// Every copy, as the account's drafts go.
    static func discardAll(root: URL = root) {
        try? FileManager.default.removeItem(at: root)
    }
}
