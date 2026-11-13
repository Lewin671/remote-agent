import Observation
import RACore
import SwiftUI

/// 附件存储 · 这台手机 (Android's PhoneImageStoragePage): what the images
/// waiting to be sent take, and what the cache of viewed images takes, each
/// against its own limit. Clearing the cache keeps the images waiting.
struct ImageStorageScreen: View {
    @Environment(AppModel.self) private var model
    @State private var storage: ImageStorageModel?
    @State private var confirming = false

    var body: some View {
        let s = storage
        SettingsPage {
            GroupLabel(L10n.imageIosAttachmentStorage(value: String(describing: thisDeviceWord)))
            GroupCard {
                GRow(L10n.imageIosPendingAndUnconfirmedAttachments(), value: s?.pending.map { "\(imageBytes($0)) / \(wholeBytes(PendingPolicy.draftLimit))" } ?? L10n.projectsReadingDirectory())
                GRow(L10n.imageIosViewingCache(), value: s?.cache.map { "\(imageBytes($0)) / \(wholeBytes(CipherStore.defaultImageLimit))" } ?? L10n.projectsReadingDirectory())
                // The files the agent shared and this phone downloaded: counted here, cleared with the cache.
                GRow(L10n.sharedFileStorageReceivedFiles(), value: s?.received.map { imageBytes($0) } ?? L10n.projectsReadingDirectory())
                GRow(
                    s?.clearing == true ? L10n.imageIosClearing() : L10n.imageIosClearViewingCache(), action: true,
                    enabled: s?.clearing == false && ((s?.cache ?? 0) > 0 || (s?.received ?? 0) > 0)
                ) { confirming = true }
            }
            GroupNote(L10n.imageIosDraftsAreClearedAfter7IdleDaysUnconfirmedMessages())
            if let error = s?.error, !error.isEmpty { GroupBanner(error, tone: .error) }
        }
        .navBar(L10n.settingsAttachmentStorage())
        // What 清理 in the confirmation does (debug builds).
        .debugSignal("images.clear") {
            guard let storage else { return }
            Task { await storage.clear() }
        }
        .task {
            let made = storage ?? ImageStorageModel(files: files)
            storage = made
            await made.load()
            #if DEBUG
            if model.isDemo, SettingsDemo.state == "clearing" { made.showClearing() }
            #endif
        }
        .confirm(
            isPresented: $confirming, title: L10n.imageIosClearViewingCache200(),
            text: L10n.imageIosFullSizeImagesViewedOnWillBeClearedAnd(value: String(describing: thisDeviceWord)), confirm: L10n.settingsClearSettings()
        ) {
            guard let storage else { return }
            Task { await storage.clear() }
        }
    }

    private var files: LocalImageFiles {
        #if DEBUG
        if model.isDemo { return .demo(SettingsDemo.state) }
        #endif
        return .stores(cache: model.imageCache, pending: model.pendingImages, shared: model.context.map { SharedFileCenter.of($0.remote) })
    }
}

/// The image files on this device, as far as this page deals with them:
/// how much the two stores take, and emptying the cache of viewed images.
/// Everything blocks on the disk; `ImageStorageModel` calls it off the main actor.
struct LocalImageFiles: Sendable {
    /// Bytes the images waiting to be sent take.
    var pendingUsed: @Sendable () -> Int64
    /// Bytes the cache of viewed images takes.
    var cacheUsed: @Sendable () -> Int64
    var clearCache: @Sendable () throws -> Void
    /// Bytes the copies of files the agent shared take.
    var receivedUsed: @Sendable () -> Int64 = { 0 }
    var clearReceived: @Sendable () async -> Void = {}

    /// The account's own stores, asked themselves: the cache of sealed
    /// envelopes the sync engine fills, and the sealed store of images
    /// waiting to be sent, counted after the drafts left idle too long have
    /// gone. Without an open cache (it did not open, and the
    /// app goes on from the server alone) there is nothing kept to count
    /// or to clear.
    static func stores(cache: CipherStore?, pending: PendingStore, shared: SharedFileCenter? = nil) -> LocalImageFiles {
        let root = SharedFileCenter.defaultRoot
        return LocalImageFiles(
            pendingUsed: {
                // Drafts expire when one is read or written; none may have been since the app started.
                pending.expire()
                return pending.used
            },
            cacheUsed: { cache?.imageBytes ?? 0 },
            clearCache: { cache?.clearImages() },
            receivedUsed: { ImageDiskPolicy.bytes(root) },
            clearReceived: { await shared?.clearAll() })
    }

    #if DEBUG
    /// Fixed sizes for looking at the page with `-demo`, where nothing is stored.
    static func demo(_ state: String?) -> LocalImageFiles {
        let empty = state == "empty"
        return LocalImageFiles(pendingUsed: { empty ? 0 : 3_355_443 }, cacheUsed: { empty ? 0 : 33_554_432 }, clearCache: {}, receivedUsed: { empty ? 0 : 25_480_396 })
    }
    #endif
}

@MainActor
@Observable
final class ImageStorageModel {
    /// Bytes waiting to be sent; nil until read.
    private(set) var pending: Int64?
    /// Bytes of viewed images; nil until read.
    private(set) var cache: Int64?
    /// Bytes of the files the agent shared; nil until read.
    private(set) var received: Int64?
    private(set) var clearing = false
    private(set) var error = ""

    @ObservationIgnored private let files: LocalImageFiles

    init(files: LocalImageFiles) {
        self.files = files
    }

    func load() async {
        let files = files
        let (pending, cache, received) = await Task.detached { (files.pendingUsed(), files.cacheUsed(), files.receivedUsed()) }.value
        self.pending = pending
        self.cache = cache
        self.received = received
    }

    /// Empties the cache of viewed images and reads the sizes again, whether or not all of it went.
    func clear() async {
        guard !clearing else { return }
        clearing = true
        error = ""
        let files = files
        let result = await Task.detached { Result { try files.clearCache() } }.value
        await files.clearReceived()
        if case .failure(let failure) = result { error = L10n.imageIosCouldNotFinishClearing(value: String(describing: failure.localizedDescription)) }
        await load()
        clearing = false
    }

    #if DEBUG
    func showClearing() { clearing = true }
    #endif
}
