// Files the agent shared, on the phone: the downloads (`SharedFileStore` in
// the package does them), what each card shows of them, and what the user
// does with a copy: open it in Quick Look, put it in Photos or Files, share
// it, delete it (ui-spec §4-§6). One centre per account, like `ImageCenter`.
//
// Two rules that Android learned on a phone (device-report 1):
//
//   - a pause, cancel or delete that comes while a block is on its way wins:
//     that is the store's (a download's token), nothing here undoes it;
//   - a download finished on the page that started it opens by itself, but
//     not when the user has been away since: the ticket to open is taken
//     back when the session page goes, or the app is left (`leftPage`).
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

@MainActor
@Observable
final class SharedFileCenter {
    // MARK: One per account

    @ObservationIgnored private static var centers: [ObjectIdentifier: SharedFileCenter] = [:]

    /// Called when an account comes into use: its copies are kept under `root`.
    static func install(remote: Remote, root: URL) {
        centers[ObjectIdentifier(remote)]?.close()
        centers[ObjectIdentifier(remote)] = SharedFileCenter(remote: remote, root: root)
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

    @ObservationIgnored let store: SharedFileStore
    @ObservationIgnored private let present: @MainActor (URL) -> Void
    @ObservationIgnored private var follower: Task<Void, Never>?
    @ObservationIgnored private var peeked: [SharedFileKey: SharedFileTransfer] = [:]
    @ObservationIgnored private var known: [SharedFileKey: PBSharedFile] = [:]
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
    init(store: SharedFileStore, open: @escaping @MainActor (URL) -> Void = { QuickLook.present($0) }) {
        self.store = store
        present = open
        let events = store.events
        follower = Task { [weak self] in
            for await event in events {
                guard let self else { return }
                self.apply(event)
            }
        }
    }

    convenience init(remote: Remote, root: URL) {
        self.init(store: SharedFileStore(
            root: root, fetcher: remote.sharedFileFetcher,
            isMachineOnline: { sid in
                let state = remote.state.value
                guard state.conn == .online, let machine = state.sessions[sid]?.machineId else { return false }
                return state.machines[machine]?.online == true
            },
            blockBytes: { sid in
                let state = remote.state.value
                return sharedFileBlockBytes(state.sessions[sid].flatMap { state.machines[$0.machineId] })
            }))
    }

    func close() {
        follower?.cancel()
        follower = nil
        noticeTask?.cancel()
        let store = store
        Task { await store.stop() }
    }

    private func apply(_ event: SharedFileStoreEvent) {
        switch event {
        case .state(let key, let transfer):
            states[key] = transfer
            if case .downloaded = transfer {} else { thumbnails[key] = nil }
        case .completed(let key, let url):
            Haptics.light()
            // Back on the page that asked, and only there: otherwise it is the user's to open.
            if tickets.remove(key) != nil { present(url) }
        }
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

    /// Called when a card appears: asks the store to settle on what the disk says.
    func appeared(_ key: SharedFileKey, file: PBSharedFile) async {
        known[key] = file
        peeked[key] = nil
        await store.hydrate(key, file: file)
    }

    // MARK: Controls

    /// What a tap on the card, or on its control, does.
    func tap(_ card: SharedFileCard, key: SharedFileKey, file: PBSharedFile) {
        guard let control = sharedFileControl(card) else { return }
        switch control {
        case .download, .resume, .retry: begin(key, file: file)
        case .pause: pause(key, file: file)
        case .open: open(key, file: file)
        case .disabledDownload: break
        }
    }

    func begin(_ key: SharedFileKey, file: PBSharedFile) {
        known[key] = file
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
        let store = store
        Task { await store.removeSession(sessionId) }
    }

    /// 清理 in the storage settings.
    func clearAll() async {
        tickets.removeAll()
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

    func share(_ key: SharedFileKey, file: PBSharedFile) {
        if let url = copy(key, file: file) { SystemSheets.share(url) }
    }

    /// 存到「文件」: the system's export panel, which says itself how it went.
    func saveToFiles(_ key: SharedFileKey, file: PBSharedFile) {
        if let url = copy(key, file: file) { SystemSheets.export(url) }
    }

    func saveToPhotos(_ key: SharedFileKey, file: PBSharedFile) {
        guard let url = copy(key, file: file) else { return }
        let kind = classifySharedFile(file.name)
        Task {
            do {
                try await PhotoLibrary.add(url, video: kind == .video)
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
    func loadMedia(_ key: SharedFileKey, url: URL, kind: SharedFileKind) async {
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

    static func read(_ url: URL, kind: SharedFileKind) async -> SharedFileMedia {
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

@MainActor
enum SystemSheets {
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

    /// 存到「文件」: a copy of the file where the user chooses.
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
