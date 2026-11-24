// Platform UI coordinator for files the agent shared: dispatches user actions
// to Go clientcore (`CoreHost`), responds to `SharedFileCompletedEffect`
// (haptics, VoiceOver announcement, auto-open Quick Look), generates local
// thumbnails & probes media duration (`AVFoundation`), reports OS-reclaimed
// local copies (`SharedFileLocalMissingEvent`), and opens system sheets
// (Quick Look, Photos, Files, Share, Open With).
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

    /// Called when an account comes into use: pairs this center with `coreHost` and its shared-file root.
    @discardableResult
    static func install(
        remote: Remote,
        coreHost: CoreHost?,
        root: URL,
        allowMissingReports: Bool = true
    ) -> SharedFileCenter {
        centers[ObjectIdentifier(remote)]?.close()
        let center = SharedFileCenter(
            store: SharedFileStore(root: root),
            coreHost: coreHost,
            allowMissingReports: allowMissingReports
        )
        centers[ObjectIdentifier(remote)] = center
        return center
    }

    /// Called when the account is left: drops in-memory thumbnails and notices.
    static func remove(_ remote: Remote) {
        centers.removeValue(forKey: ObjectIdentifier(remote))?.close()
    }

    /// The files of the account `remote` syncs; one that was never installed (a test's, the sample data's) keeps its copies in a temporary folder.
    static func of(_ remote: Remote) -> SharedFileCenter {
        if let c = centers[ObjectIdentifier(remote)] { return c }
        let root = FileManager.default.temporaryDirectory.appending(path: "shared-files-\(ObjectIdentifier(remote).hashValue)", directoryHint: .isDirectory)
        let c = SharedFileCenter(store: SharedFileStore(root: root), coreHost: nil, allowMissingReports: false)
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
    @ObservationIgnored private weak var coreHost: CoreHost?
    @ObservationIgnored private let present: @MainActor (URL) -> Void
    @ObservationIgnored private let readMedia: @Sendable (URL, PBSharedFileKind) async -> SharedFileMedia
    @ObservationIgnored private let allowMissingReports: Bool
    @ObservationIgnored private var loadingTasks: [SharedFileKey: (id: UUID, task: Task<Void, Never>)] = [:]
    @ObservationIgnored private var probedDurations = Set<SharedFileKey>()
    @ObservationIgnored private var noticeTask: Task<Void, Never>?

    /// Pictures and first frames of copies that are on the phone.
    private(set) var thumbnails: [SharedFileKey: UIImage] = [:]
    private(set) var notice: SharedFileNotice?

    /// `open` shows a finished copy (Quick Look); a test gives its own.
    init(
        store: SharedFileStore,
        coreHost: CoreHost? = nil,
        allowMissingReports: Bool = true,
        open: @escaping @MainActor (URL) -> Void = { QuickLook.present($0) },
        readMedia: @escaping @Sendable (URL, PBSharedFileKind) async -> SharedFileMedia = { url, kind in
            await Task.detached(priority: .utility) { await SharedFileMedia.read(url, kind: kind) }.value
        }
    ) {
        self.store = store
        self.coreHost = coreHost
        self.allowMissingReports = allowMissingReports
        self.present = open
        self.readMedia = readMedia
    }

    func handlers() -> CoreHost.SharedFileHandlers {
        CoreHost.SharedFileHandlers(
            onCompleted: { [weak self] eff, isVisibleSession in
                self?.handleCompleted(eff, isVisibleSession: isVisibleSession)
            },
            onClearThumbnail: { [weak self] key in self?.clearThumbnail(key) },
            onClearSessionThumbnails: { [weak self] sid in self?.clearThumbnails(sessionID: sid) },
            onClearAllThumbnails: { [weak self] in self?.clearAllThumbnails() }
        )
    }

    func close() {
        noticeTask?.cancel()
        noticeTask = nil
        for entry in loadingTasks.values { entry.task.cancel() }
        loadingTasks.removeAll()
        thumbnails.removeAll()
        probedDurations.removeAll()
    }

    // MARK: Core Effects

    /// Handles `SharedFileCompletedEffect` emitted by Core after verification and commit.
    func handleCompleted(_ effect: PBSharedFileCompletedEffect, isVisibleSession: Bool) {
        guard !effect.deliverOnly else { return }
        if isVisibleSession {
            Haptics.light()
            if UIAccessibility.isVoiceOverRunning {
                AccessibilityNotification.Announcement(L10n.sharedFileA11YDownloadedAnnounce(name: effect.displayName)).post()
            }
        }
        let key = SharedFileKey(sessionId: effect.sessionID, fileId: effect.fileID)
        if effect.autoOpen {
            open(key, name: effect.displayName)
        }
        let hasMediaDuration = effect.kind == .video || effect.kind == .audio
        if effect.kind == .image || hasMediaDuration {
            Task {
                await loadMedia(
                    key,
                    name: effect.displayName,
                    kind: effect.kind,
                    hasMediaDuration: hasMediaDuration,
                    hasDurationText: false
                )
            }
        }
    }

    func clearThumbnail(_ key: SharedFileKey) {
        loadingTasks.removeValue(forKey: key)?.task.cancel()
        thumbnails.removeValue(forKey: key)
        probedDurations.remove(key)
    }

    func clearThumbnails(sessionID: String) {
        for key in Array(loadingTasks.keys) where key.sessionId == sessionID {
            loadingTasks.removeValue(forKey: key)?.task.cancel()
        }
        for key in Array(thumbnails.keys) where key.sessionId == sessionID {
            thumbnails.removeValue(forKey: key)
        }
        probedDurations = probedDurations.filter { $0.sessionId != sessionID }
    }

    func clearAllThumbnails() {
        for entry in loadingTasks.values { entry.task.cancel() }
        loadingTasks.removeAll()
        thumbnails.removeAll()
        probedDurations.removeAll()
    }

    // MARK: Controls (User -> Core Actions)

    /// What a tap on the card, or on its control, does — driven by Core's `primary_action` and `primary_enabled`.
    func tap(_ item: PBSessionSharedFileItem, sessionId: String) {
        guard item.primaryEnabled else { return }
        let key = SharedFileKey(sessionId: sessionId, fileId: item.fileID)
        switch item.primaryAction {
        case .download, .resume, .retry: startDownload(key, autoOpenIfStaying: true)
        case .pause: pauseDownload(key)
        case .open: open(key, name: item.displayName)
        case .unspecified, .UNRECOGNIZED: break
        }
    }

    /// Executes one menu action returned by Core's `SessionSharedFileItem.menu_items`.
    func runMenuAction(_ action: PBSharedFileMenuAction, item: PBSessionSharedFileItem, sessionId: String) {
        let key = SharedFileKey(sessionId: sessionId, fileId: item.fileID)
        switch action {
        case .download, .resume, .retry: startDownload(key, autoOpenIfStaying: true)
        case .pause: pauseDownload(key)
        case .cancelDownload: cancelDownload(key)
        case .saveToAlbum: saveToPhotos(key, item: item)
        case .save: saveToFiles(key, name: item.displayName)
        case .share: share(key, name: item.displayName)
        case .openWith: openWith(key, name: item.displayName)
        case .deleteLocal: deleteLocal(key)
        case .unspecified, .UNRECOGNIZED: break
        }
    }

    func startDownload(_ key: SharedFileKey, autoOpenIfStaying: Bool) {
        guard let host = coreHost else { return }
        Task { await host.startSharedFileDownload(sessionID: key.sessionId, fileID: key.fileId, autoOpenIfStaying: autoOpenIfStaying) }
    }

    func pauseDownload(_ key: SharedFileKey) {
        guard let host = coreHost else { return }
        Task { await host.pauseSharedFileDownload(sessionID: key.sessionId, fileID: key.fileId) }
    }

    func cancelDownload(_ key: SharedFileKey) {
        guard let host = coreHost else { return }
        Task { await host.cancelSharedFileDownload(sessionID: key.sessionId, fileID: key.fileId) }
    }

    func deleteLocal(_ key: SharedFileKey) {
        clearThumbnail(key)
        guard let host = coreHost else { return }
        Task { await host.deleteSharedFileLocal(sessionID: key.sessionId, fileID: key.fileId) }
    }

    func forgetSession(_ sessionId: String) {
        clearThumbnails(sessionID: sessionId)
        guard let host = coreHost else { return }
        Task { await host.forgetSharedFileSession(sessionId) }
    }

    /// 清理 in the storage settings.
    func clearAll() async {
        thumbnails.removeAll()
        if let host = coreHost {
            await host.clearAllSharedFiles()
        } else {
            let store = store
            await Task.detached(priority: .utility) { store.clearAll() }.value
        }
    }

    func usedBytes() -> Int64 { store.usedBytes() }

    // MARK: Using a copy

    private func existingCopy(_ key: SharedFileKey, name: String) -> URL? {
        if let url = store.existingLocalURL(key, name: name) {
            return url
        }
        reportMissingIfNeeded(key)
        return nil
    }

    private func reportMissingIfNeeded(_ key: SharedFileKey) {
        clearThumbnail(key)
        guard allowMissingReports, let host = coreHost else { return }
        Task { await host.reportSharedFileLocalMissing(sessionID: key.sessionId, fileID: key.fileId) }
    }

    func open(_ key: SharedFileKey, name: String) {
        if let url = existingCopy(key, name: name) { present(url) }
    }

    func openWith(_ key: SharedFileKey, name: String) {
        if let url = existingCopy(key, name: name) { SystemSheets.openWith(url) }
    }

    func share(_ key: SharedFileKey, name: String) {
        if let url = existingCopy(key, name: name) { SystemSheets.share(url) }
    }

    /// 存到「文件」 / 保存到本机: the system's export panel, which says itself how it went.
    func saveToFiles(_ key: SharedFileKey, name: String) {
        if let url = existingCopy(key, name: name) { SystemSheets.export(url) }
    }

    func saveToPhotos(_ key: SharedFileKey, item: PBSessionSharedFileItem) {
        guard let url = existingCopy(key, name: item.displayName) else { return }
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
    func loadMedia(
        _ key: SharedFileKey,
        name: String,
        kind: PBSharedFileKind,
        hasMediaDuration: Bool = false,
        hasDurationText: Bool = false
    ) async {
        while let existing = loadingTasks[key] {
            await existing.task.value
            if Task.isCancelled { return }
        }
        let needsThumbnail = (kind == .image || kind == .video) && thumbnails[key] == nil
        let needsDuration = hasMediaDuration && !hasDurationText && !probedDurations.contains(key)
        guard needsThumbnail || needsDuration else { return }
        #if DEBUG
        if SharedFileDebug.hasForcedStates { return }
        #endif
        guard let url = existingCopy(key, name: name) else { return }
        let loadID = UUID()
        let reader = self.readMedia
        let task = Task { [weak self] in
            defer {
                if let self, self.loadingTasks[key]?.id == loadID {
                    self.loadingTasks.removeValue(forKey: key)
                }
            }
            let media = await reader(url, kind)
            guard let self, !Task.isCancelled else { return }
            guard self.store.existingLocalURL(key, name: name) != nil else { return }
            if needsThumbnail, let image = media.thumbnail { self.thumbnails[key] = image }
            if needsDuration {
                self.probedDurations.insert(key)
                if let ms = media.durationMs, ms > 0, let host = self.coreHost {
                    await host.recordSharedFileDuration(sessionID: key.sessionId, fileID: key.fileId, durationMs: ms)
                }
            }
        }
        loadingTasks[key] = (loadID, task)
        await task.value
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
