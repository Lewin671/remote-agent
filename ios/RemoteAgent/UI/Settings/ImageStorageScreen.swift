import Observation
import RACore
import SwiftUI

/// 图片存储 · 这台手机 (Android's PhoneImageStoragePage): what the images
/// waiting to be sent take, and what the cache of viewed images takes, each
/// against its own limit. Clearing the cache keeps the images waiting.
struct ImageStorageScreen: View {
    @Environment(AppModel.self) private var model
    @State private var storage: ImageStorageModel?
    @State private var confirming = false

    var body: some View {
        let s = storage
        SettingsPage {
            GroupLabel("图片存储 · \(thisDeviceWord)")
            GroupCard {
                GRow("待发和待确认图片", value: s?.pending.map { "\(imageBytes($0)) / \(wholeBytes(PendingPolicy.draftLimit))" } ?? "读取中…")
                GRow("查看缓存", value: s?.cache.map { "\(imageBytes($0)) / \(wholeBytes(CipherStore.defaultImageLimit))" } ?? "读取中…")
                GRow(
                    s?.clearing == true ? "正在清理…" : "清理查看缓存", action: true,
                    enabled: s?.clearing == false && (s?.cache ?? 0) > 0
                ) { confirming = true }
            }
            GroupNote("草稿闲置 7 天后清理；待确认消息保留到确认或移除。容量不足时提醒，保留已有内容。查看缓存超过上限时先清最久没看的。")
            if let error = s?.error, !error.isEmpty { GroupBanner(error, tone: .error) }
        }
        .navBar("图片存储")
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
            isPresented: $confirming, title: "清理查看缓存？",
            text: "看过的大图会从\(thisDeviceWord)上清掉，再看时重新下载。待发的图片、草稿和会话历史不受影响。", confirm: "清理"
        ) {
            guard let storage else { return }
            Task { await storage.clear() }
        }
    }

    private var files: LocalImageFiles {
        #if DEBUG
        if model.isDemo { return .demo(SettingsDemo.state) }
        #endif
        return .stores(cache: model.imageCache, pending: model.pendingImages)
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

    /// The account's own stores, asked themselves: the cache of sealed
    /// envelopes the sync engine fills, and the sealed store of images
    /// waiting to be sent. Without an open cache (it did not open, and the
    /// app goes on from the server alone) there is nothing kept to count
    /// or to clear.
    static func stores(cache: CipherStore?, pending: PendingStore) -> LocalImageFiles {
        LocalImageFiles(
            pendingUsed: { pending.used },
            cacheUsed: { cache?.imageBytes ?? 0 },
            clearCache: { cache?.clearImages() })
    }

    #if DEBUG
    /// Fixed sizes for looking at the page with `-demo`, where nothing is stored.
    static func demo(_ state: String?) -> LocalImageFiles {
        let empty = state == "empty"
        return LocalImageFiles(pendingUsed: { empty ? 0 : 3_355_443 }, cacheUsed: { empty ? 0 : 33_554_432 }, clearCache: {})
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
    private(set) var clearing = false
    private(set) var error = ""

    @ObservationIgnored private let files: LocalImageFiles

    init(files: LocalImageFiles) {
        self.files = files
    }

    func load() async {
        let files = files
        let (pending, cache) = await Task.detached { (files.pendingUsed(), files.cacheUsed()) }.value
        self.pending = pending
        self.cache = cache
    }

    /// Empties the cache of viewed images and reads the sizes again, whether or not all of it went.
    func clear() async {
        guard !clearing else { return }
        clearing = true
        error = ""
        let files = files
        let result = await Task.detached { Result { try files.clearCache() } }.value
        if case .failure(let failure) = result { error = "没能清理完：\(failure.localizedDescription)" }
        await load()
        clearing = false
    }

    #if DEBUG
    func showClearing() { clearing = true }
    #endif
}
