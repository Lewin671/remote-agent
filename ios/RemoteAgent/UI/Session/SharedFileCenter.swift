// Files the agent shared, on the phone: the downloads (`SharedFileStore` in
// the package does them), throttled local-state reporting to Go clientcore
// (`SharedFileLocalStateEvent`), thumbnails/duration probing, and system actions
// (Quick Look, Photos, Files, Share, Open With, delete).
import AVFoundation
import ImageIO
import Observation
import Photos
import QuickLook
import RACore
import RAProto
import SwiftUI
import UIKit

/// A line the session page shows for a moment after something was saved or did not go.
struct SharedFileNotice: Equatable, Identifiable {
    let id = UUID()
    let text: String
    let failed: Bool
    /// A check for what went through, a cross for what did not.
    var symbol: String { failed ? "xmark" : "checkmark" }
}

/// Builds the minimal `PBSharedFile` metadata needed by `SharedFileStore` from Core's `PBSessionSharedFileItem`.
func sharedFileDescriptor(_ item: PBSessionSharedFileItem) -> PBSharedFile {
    .with {
        $0.fileID = item.fileID
        $0.name = item.displayName
        $0.sizeBytes = item.sizeBytes
        $0.sha256 = item.sha256
    }
}

@MainActor
@Observable
final class SharedFileCenter {
    // MARK: One per account

    @ObservationIgnored private static var centers: [ObjectIdentifier: SharedFileCenter] = [:]

    /// Called when an account comes into use: its copies are kept under `root` and reported to Core (`replace_all = true`).
    static func install(remote: Remote, coreHost: CoreHost? = nil, root: URL) {
        centers[ObjectIdentifier(remote)]?.close()
        let report: @Sendable (PBSharedFileLocalStateEvent) async -> Void = { [weak coreHost] ev in
            await coreHost?.reportSharedFileLocalState(ev)
        }
        let center: SharedFileCenter
        #if DEBUG
        if SharedFileDebug.fake {
            center = SharedFileDebug.fakeCenter(root: root, report: report)
        } else {
            center = SharedFileCenter(remote: remote, root: root, report: report)
        }
        #else
        center = SharedFileCenter(remote: remote, root: root, report: report)
        #endif
        centers[ObjectIdentifier(remote)] = center
        #if DEBUG
        if SharedFileDebug.hasForcedStates { return }
        #endif
        Task { await center.syncAllToCore() }
    }

    /// Called when the account is left: downloads stop and what was held in memory goes.
    static func remove(_ remote: Remote) {
        centers.removeValue(forKey: ObjectIdentifier(remote))?.close()
    }

    /// The files of the account `remote` syncs; one that was never installed (a test's, the sample data's) keeps its copies in a temporary folder.
    static func of(_ remote: Remote) -> SharedFileCenter {
        if let c = centers[ObjectIdentifier(remote)] { return c }
        let root = FileManager.default.temporaryDirectory.appending(path: "shared-files-\(ObjectIdentifier(remote).hashValue)", directoryHint: .isDirectory)
        #if DEBUG
        let c = SharedFileDebug.fake ? SharedFileDebug.fakeCenter(root: root) : SharedFileCenter(remote: remote, root: root)
        #else
        let c = SharedFileCenter(remote: remote, root: root)
        #endif
        centers[ObjectIdentifier(remote)] = c
        return c
    }

    /// Where the copies of an account are: the system's cache, which it may take back (and the
    /// card then goes back to 可下载), outside what is backed up.
    nonisolated static var defaultRoot: URL {
        URL.cachesDirectory.appending(path: "RemoteAgent/shared-files", directoryHint: .isDirectory)
    }

    // MARK: State

    private struct ReportedMark {
        var timeMs: Int64
        var percent: Int
        var status: PBSharedFileTransferStatus
        var waitingReconnect: Bool
    }

    @ObservationIgnored let store: SharedFileStore
    @ObservationIgnored private let present: @MainActor (URL) -> Void
    @ObservationIgnored private let report: @Sendable (PBSharedFileLocalStateEvent) async -> Void
    @ObservationIgnored private let clock: @Sendable () -> Int64
    @ObservationIgnored private var follower: Task<Void, Never>?
    @ObservationIgnored private var reportTail: Task<Void, Never>?
    @ObservationIgnored private var lastReported: [SharedFileKey: ReportedMark] = [:]
    @ObservationIgnored private var peeked: [SharedFileKey: SharedFileTransfer] = [:]
    @ObservationIgnored private var loading = Set<SharedFileKey>()
    /// Downloads the user started on a page that is still open: the ones that open by themselves when done.
    @ObservationIgnored private var tickets = Set<SharedFileKey>()
    @ObservationIgnored private var noticeTask: Task<Void, Never>?

    /// What the store says, as it changes.
    private(set) var states: [SharedFileKey: SharedFileTransfer] = [:]
    /// Pictures and first frames of copies that are on the phone.
    private(set) var thumbnails: [SharedFileKey: UIImage] = [:]
    private(set) var notice: SharedFileNotice?

    /// `open` shows a finished copy (Quick Look); a test gives its own.
    init(
        store: SharedFileStore,
        open: @escaping @MainActor (URL) -> Void = { QuickLook.present($0) },
        report: @escaping @Sendable (PBSharedFileLocalStateEvent) async -> Void = { _ in },
        clock: @escaping @Sendable () -> Int64 = { nowMs() }
    ) {
        self.store = store
        self.present = open
        self.report = report
        self.clock = clock
        let events = store.events
        follower = Task { [weak self] in
            for await event in events {
                guard let self else { return }
                self.apply(event)
            }
        }
    }

    convenience init(
        remote: Remote,
        root: URL,
        report: @escaping @Sendable (PBSharedFileLocalStateEvent) async -> Void = { _ in }
    ) {
        self.init(
            store: SharedFileStore(
                root: root, fetcher: remote.sharedFileFetcher,
                isMachineOnline: { sid in
                    let state = remote.state.value
                    guard let machine = state.sessions[sid]?.machineId else { return false }
                    return state.peer(machine).available
                },
                blockBytes: { sid in
                    let state = remote.state.value
                    return sharedFileBlockBytes(state.sessions[sid].flatMap { state.machines[$0.machineId] })
                }),
            report: report
        )
    }

    func close() {
        follower?.cancel()
        follower = nil
        reportTail?.cancel()
        reportTail = nil
        noticeTask?.cancel()
        let store = store
        Task { await store.stop() }
    }

    /// Scans persisted local copies on disk and sends a full `replace_all = true` report to Core.
    func syncAllToCore() async {
        let entries = await store.scanPersisted()
        var updates: [PBSharedFileLocalStateUpdate] = []
        updates.reserveCapacity(entries.count)
        let now = clock()
        lastReported.removeAll()
        for (key, transfer) in entries {
            if states[key] == nil {
                states[key] = transfer
            }
            let u = sharedFileLocalStateUpdate(key: key, transfer: transfer)
            updates.append(u)
            lastReported[key] = ReportedMark(timeMs: now, percent: -1, status: u.status, waitingReconnect: u.waitingReconnect)
        }
        sendToCore(.with {
            $0.replaceAll = true
            $0.updates = updates
        })
    }

    /// Waits for all enqueued Core reports to complete (for tests).
    func flushReports() async {
        await reportTail?.value
    }

    private func sendToCore(_ event: PBSharedFileLocalStateEvent) {
        let prev = reportTail
        let report = self.report
        reportTail = Task {
            await prev?.value
            guard !Task.isCancelled else { return }
            await report(event)
        }
    }

    private func apply(_ event: SharedFileStoreEvent) {
        switch event {
        case .state(let key, let transfer):
            let previous = states[key]
            states[key] = transfer
            if case .downloaded = transfer {} else { thumbnails[key] = nil }
            reportTransfer(key: key, transfer: transfer, previous: previous)
        case .completed(let key, let url):
            Haptics.light()
            // Back on the page that asked, and only there: otherwise it is the user's to open.
            if tickets.remove(key) != nil { present(url) }
        }
    }

    private func reportTransfer(key: SharedFileKey, transfer: SharedFileTransfer, previous: SharedFileTransfer?) {
        #if DEBUG
        if SharedFileDebug.hasForcedStates { return }
        #endif
        if transfer == .idle && (previous == nil || previous == .idle) {
            return
        }
        // Duration-only update on an already-downloaded file uses `metadata_only = true`.
        if case .downloaded(let prevURL, let prevMs) = previous,
           case .downloaded(let nextURL, let nextMs) = transfer,
           prevURL == nextURL, let ms = nextMs, ms != prevMs
        {
            sendToCore(.with {
                $0.updates = [.with {
                    $0.sessionID = key.sessionId
                    $0.fileID = key.fileId
                    $0.metadataOnly = true
                    $0.hasDuration_p = true
                    $0.durationMs = ms
                }]
            })
            return
        }

        let update = sharedFileLocalStateUpdate(key: key, transfer: transfer)
        if case .downloading(let done, let total, _, let waitingReconnect) = transfer {
            let now = clock()
            let pct = total > 0 ? Int(min(100, max(0, (done * 100) / total))) : 0
            if let prev = lastReported[key],
               prev.status == .downloading,
               prev.waitingReconnect == waitingReconnect,
               prev.percent == pct,
               now - prev.timeMs < 250
            {
                return
            }
            lastReported[key] = ReportedMark(timeMs: now, percent: pct, status: .downloading, waitingReconnect: waitingReconnect)
        } else {
            lastReported[key] = ReportedMark(timeMs: clock(), percent: -1, status: update.status, waitingReconnect: false)
        }
        sendToCore(.with { $0.updates = [update] })
    }

    // MARK: What a card reads

    /// The transfer of a file. A card drawn before the store has said anything reads the disk once.
    func transfer(_ key: SharedFileKey, file: PBSharedFile) -> SharedFileTransfer {
        if let held = states[key] { return held }
        if let kept = peeked[key] { return kept }
        let found = store.inspect(key, file: file)
        peeked[key] = found
        return found
    }

    /// Called when a card appears: asks the store to settle on what the disk says and sync to Core.
    func appeared(_ key: SharedFileKey, file: PBSharedFile) async {
        peeked[key] = nil
        let found = await store.hydrate(key, file: file)
        // If store already had the same state in memory without emitting, ensure Core knows about it.
        if lastReported[key] == nil, found != .idle {
            reportTransfer(key: key, transfer: found, previous: nil)
        }
    }

    // MARK: Controls

    /// What a tap on the card, or on its control, does — driven by Core's `primary_action` and `primary_enabled`.
    func tap(_ item: PBSessionSharedFileItem, sessionId: String) {
        guard item.primaryEnabled else { return }
        let key = SharedFileKey(sessionId: sessionId, fileId: item.fileID)
        let file = sharedFileDescriptor(item)
        switch item.primaryAction {
        case .download, .resume, .retry: begin(key, file: file)
        case .pause: pause(key, file: file)
        case .open: open(key, file: file)
        case .unspecified, .UNRECOGNIZED: break
        }
    }

    /// Executes one menu action returned by Core's `SessionSharedFileItem.menu_items`.
    func runMenuAction(_ action: PBSharedFileMenuAction, item: PBSessionSharedFileItem, sessionId: String) {
        let key = SharedFileKey(sessionId: sessionId, fileId: item.fileID)
        let file = sharedFileDescriptor(item)
        switch action {
        case .download, .resume, .retry: begin(key, file: file)
        case .pause: pause(key, file: file)
        case .cancelDownload: cancel(key)
        case .saveToAlbum: saveToPhotos(key, item: item)
        case .save: saveToFiles(key, file: file)
        case .share: share(key, file: file)
        case .openWith: openWith(key, file: file)
        case .deleteLocal: deleteLocal(key)
        case .unspecified, .UNRECOGNIZED: break
        }
    }

    func begin(_ key: SharedFileKey, file: PBSharedFile) {
        tickets.insert(key)
        let store = store
        Task { await store.start(key, file: file) }
    }

    func pause(_ key: SharedFileKey, file: PBSharedFile) {
        tickets.remove(key)
        let store = store
        Task { await store.pause(key, file: file) }
    }

    func cancel(_ key: SharedFileKey) {
        tickets.remove(key)
        let store = store
        Task { await store.cancel(key) }
    }

    func deleteLocal(_ key: SharedFileKey) {
        tickets.remove(key)
        let store = store
        Task { await store.deleteLocal(key) }
    }

    /// The session page went, or the app left the front: nothing that finishes now opens by itself.
    func leftPage(_ sessionId: String? = nil) {
        if let sessionId { tickets = tickets.filter { $0.sessionId != sessionId } } else { tickets.removeAll() }
    }

    func forgetSession(_ sessionId: String) {
        leftPage(sessionId)
        for key in Array(lastReported.keys) where key.sessionId == sessionId {
            lastReported.removeValue(forKey: key)
        }
        sendToCore(.with { $0.clearSessionID_p = sessionId })
        let store = store
        Task { await store.removeSession(sessionId) }
    }

    /// 清理 in the storage settings.
    func clearAll() async {
        tickets.removeAll()
        lastReported.removeAll()
        sendToCore(.with { $0.replaceAll = true })
        await store.clearAll()
    }

    func usedBytes() async -> Int64 { await store.usedBytes() }

    // MARK: Using a copy

    private func copy(_ key: SharedFileKey, file: PBSharedFile) -> URL? {
        guard case .downloaded(let url, _) = transfer(key, file: file), FileManager.default.fileExists(atPath: url.path) else { return nil }
        return url
    }

    func open(_ key: SharedFileKey, file: PBSharedFile) {
        if let url = copy(key, file: file) { present(url) }
    }

    func openWith(_ key: SharedFileKey, file: PBSharedFile) {
        if let url = copy(key, file: file) { SystemSheets.openWith(url) }
    }

    func share(_ key: SharedFileKey, file: PBSharedFile) {
        if let url = copy(key, file: file) { SystemSheets.share(url) }
    }

    /// 存到「文件」 / 保存到本机: the system's export panel, which says itself how it went.
    func saveToFiles(_ key: SharedFileKey, file: PBSharedFile) {
        if let url = copy(key, file: file) { SystemSheets.export(url) }
    }

    func saveToPhotos(_ key: SharedFileKey, item: PBSessionSharedFileItem) {
        guard let url = copy(key, file: sharedFileDescriptor(item)) else { return }
        let isVideo = item.kind == .video
        Task {
            do {
                try await PhotoLibrary.add(url, video: isVideo)
                show(SharedFileNotice(text: L10n.sharedFileToastSavedToAlbum(), failed: false))
                Haptics.confirm()
            } catch is PhotoAccessDenied {
                show(SharedFileNotice(text: L10n.imageIosSavePermission(), failed: true))
            } catch {
                show(SharedFileNotice(text: L10n.commonIosSaveFailed(reason: errorText(error)), failed: true))
            }
        }
    }

    private func show(_ next: SharedFileNotice) {
        notice = next
        noticeTask?.cancel()
        noticeTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 2_400_000_000)
            guard !Task.isCancelled else { return }
            self?.notice = nil
        }
    }

    // MARK: From the copy only

    /// A picture's or video's first frame, and a video's or recording's length, read from the copy on the phone (never asked of the computer).
    func loadMedia(_ key: SharedFileKey, url: URL, kind: PBSharedFileKind) async {
        guard kind == .image || kind == .video || kind == .audio, !loading.contains(key) else { return }
        if kind == .audio || thumbnails[key] == nil {
            loading.insert(key)
            defer { loading.remove(key) }
            let media = await Task.detached(priority: .utility) { await SharedFileMedia.read(url, kind: kind) }.value
            guard case .downloaded(let current, _) = states[key] ?? .idle, current == url else { return }
            if let image = media.thumbnail { thumbnails[key] = image }
            if let ms = media.durationMs { await store.recordDuration(key, ms: ms) }
        }
    }

    func thumbnail(_ key: SharedFileKey) -> UIImage? { thumbnails[key] }
}

/// The photo library, written to with the access that only adds. Off the main actor: the library runs its change block on
/// a queue of its own.
enum PhotoLibrary {
    static func add(_ url: URL, video: Bool) async throws {
        let status = await PHPhotoLibrary.requestAuthorization(for: .addOnly)
        guard status == .authorized || status == .limited else { throw PhotoAccessDenied() }
        let name = url.lastPathComponent
        try await PHPhotoLibrary.shared().performChanges {
            let options = PHAssetResourceCreationOptions()
            options.originalFilename = name
            // The file as it is: the bytes the computer had, not a copy re-encoded here.
            PHAssetCreationRequest.forAsset().addResource(with: video ? .video : .photo, fileURL: url, options: options)
        }
    }
}

/// What reading a copy gives.
struct SharedFileMedia: Sendable {
    var thumbnail: UIImage?
    var durationMs: Int64?

    /// 44 points at up to 3x.
    static let side: CGFloat = 132

    static func read(_ url: URL, kind: PBSharedFileKind) async -> SharedFileMedia {
        switch kind {
        case .image:
            return SharedFileMedia(thumbnail: image(url), durationMs: nil)
        case .video:
            let asset = AVURLAsset(url: url)
            let duration = try? await asset.load(.duration)
            let generator = AVAssetImageGenerator(asset: asset)
            generator.appliesPreferredTrackTransform = true
            generator.maximumSize = CGSize(width: side * 2, height: side * 2)
            // A moment in, so that a fade from black is not the picture.
            let at = CMTime(seconds: min(0.5, max(0, (duration?.seconds ?? 0) / 2)), preferredTimescale: 600)
            let frame = (try? await generator.image(at: at))?.image
            return SharedFileMedia(thumbnail: frame.map { UIImage(cgImage: $0) }, durationMs: millis(duration))
        case .audio:
            let duration = try? await AVURLAsset(url: url).load(.duration)
            return SharedFileMedia(thumbnail: nil, durationMs: millis(duration))
        default:
            return SharedFileMedia()
        }
    }

    private static func millis(_ time: CMTime?) -> Int64? {
        guard let time, time.isNumeric, time.seconds.isFinite, time.seconds > 0 else { return nil }
        return Int64((time.seconds * 1000).rounded())
    }

    private static func image(_ url: URL) -> UIImage? {
        guard let source = CGImageSourceCreateWithURL(url as CFURL, nil) else { return nil }
        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true, kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceThumbnailMaxPixelSize: side * 2,
        ]
        return CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary).map { UIImage(cgImage: $0) }
    }
}

extension Haptics {
    /// A download finished.
    static func light() { UIImpactFeedbackGenerator(style: .light).impactOccurred() }
}

// MARK: Quick Look and the system's sheets

/// Quick Look for a copy: the system's viewer for pictures, video, audio, PDF and text, and its own page (which can
/// share) for a type it cannot show.
@MainActor
private final class QuickLookSource: NSObject, QLPreviewControllerDataSource, @preconcurrency QLPreviewControllerDelegate {
    final class Item: NSObject, QLPreviewItem {
        let url: URL
        init(_ url: URL) { self.url = url }
        var previewItemURL: URL? { url }
        var previewItemTitle: String? { url.lastPathComponent }
    }

    let item: Item
    init(_ url: URL) { item = Item(url) }
    func numberOfPreviewItems(in controller: QLPreviewController) -> Int { 1 }
    func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> any QLPreviewItem { item }
    func previewControllerDidDismiss(_ controller: QLPreviewController) { QuickLook.held = nil }
}

@MainActor
enum QuickLook {
    fileprivate static var held: QuickLookSource?

    static func present(_ url: URL) {
        guard let top = SystemSheets.top() else { return }
        // One at a time: a viewer that is open already is replaced.
        if top is QLPreviewController { return }
        let source = QuickLookSource(url)
        held = source
        let controller = QLPreviewController()
        controller.dataSource = source
        controller.delegate = source
        top.present(controller, animated: true)
    }
}

private final class DocumentOpener: NSObject, UIDocumentInteractionControllerDelegate {
    let controller: UIDocumentInteractionController
    init(controller: UIDocumentInteractionController) { self.controller = controller }
    func documentInteractionControllerDidDismissOpenInMenu(_ controller: UIDocumentInteractionController) {
        MainActor.assumeIsolated { SystemSheets.opener = nil }
    }
}

@MainActor
enum SystemSheets {
    fileprivate static var opener: DocumentOpener?

    /// The view controller in front, whatever is presented over the app's own.
    static func top() -> UIViewController? {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let window = scenes.first { $0.activationState == .foregroundActive }?.windows.first { $0.isKeyWindow } ?? scenes.first?.windows.first
        var top = window?.rootViewController
        while let next = top?.presentedViewController, !next.isBeingDismissed { top = next }
        return top
    }

    static func share(_ url: URL) {
        guard let top = top() else { return }
        let sheet = UIActivityViewController(activityItems: [url], applicationActivities: nil)
        anchor(sheet, in: top)
        top.present(sheet, animated: true)
    }

    /// 用其他应用打开: system Open-In menu, falling back to share sheet when no direct handler claims the type.
    static func openWith(_ url: URL) {
        guard let top = top() else { return }
        let controller = UIDocumentInteractionController(url: url)
        let delegate = DocumentOpener(controller: controller)
        opener = delegate
        controller.delegate = delegate
        let rect = CGRect(x: top.view.bounds.midX, y: top.view.bounds.midY, width: 0, height: 0)
        if !controller.presentOpenInMenu(from: rect, in: top.view, animated: true) {
            opener = nil
            share(url)
        }
    }

    /// 保存到本机: a copy of the file where the user chooses.
    static func export(_ url: URL) {
        guard let top = top() else { return }
        let picker = UIDocumentPickerViewController(forExporting: [url], asCopy: true)
        top.present(picker, animated: true)
    }

    private static func anchor(_ sheet: UIViewController, in top: UIViewController) {
        guard let popover = sheet.popoverPresentationController else { return }
        popover.sourceView = top.view
        popover.sourceRect = CGRect(x: top.view.bounds.midX, y: top.view.bounds.midY, width: 0, height: 0)
        popover.permittedArrowDirections = []
    }
}
