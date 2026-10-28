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
import PhotosUI
import RACore
import RAProto
import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// The app's sealed store is what the package keeps drafts and messages in.
extension PendingStore: RACore.SealedFiles {}

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
    var unverifiedLegacy = false
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
        case item(PhotosPickerItem)
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
    static func install(remote: Remote, files: any RACore.SealedFiles, live: Bool = true) {
        centers[ObjectIdentifier(remote)]?.close()
        centers[ObjectIdentifier(remote)] = ImageCenter(remote: remote, files: files, live: live)
    }

    /// Called when the account is left: jobs stop, and what was held in memory goes.
    static func remove(_ remote: Remote) {
        centers.removeValue(forKey: ObjectIdentifier(remote))?.close()
    }

    /// The images of the account `remote` syncs. One that was never installed (a test's) keeps nothing on disk.
    static func of(_ remote: Remote) -> ImageCenter {
        if let c = centers[ObjectIdentifier(remote)] { return c }
        let c = ImageCenter(remote: remote, files: VolatileFiles(), live: true)
        centers[ObjectIdentifier(remote)] = c
        return c
    }

    // MARK: State

    @ObservationIgnored let remote: Remote
    @ObservationIgnored let live: Bool
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
    @ObservationIgnored private var fileMachines: [String: MachineView] = [:]
    /// Messages with images sent from here, by session, until the CLI has them.
    @ObservationIgnored private var kept: [String: [Outgoing]] = [:]
    @ObservationIgnored private var restored = Set<String>()
    @ObservationIgnored private var restorations: [String: Task<Void, Never>] = [:]
    @ObservationIgnored private var watchers: [String: Task<Void, Never>] = [:]
    /// The account was left: what was on its way when that happened must not go on for it.
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
    private(set) var notices: [String: String] = [:]
    var picking: [String: Pick] = [:]

    private init(remote: Remote, files: any RACore.SealedFiles, live: Bool) {
        self.remote = remote
        self.files = files
        self.live = live
        records = OutgoingRecords(files: files)
        viewed.use(remote)
        uploads = ImageUploads(
            prepare: { [weak self] uri, original in
                guard let data = try await self?.read(uri) else { throw UserError("读不到这张图片") }
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
    }

    private func close() {
        closed = true
        for draft in drafts.values { for img in draft.images { img.release() } }
        for task in watchers.values { task.cancel() }
        for task in restorations.values { task.cancel() }
        restorations.removeAll()
        watchers.removeAll()
        drafts.removeAll()
        sources.removeAll()
        kept.removeAll()
        thumbs.removeAll()
        viewed.clear()
        previews.removeAllObjects()
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

    /// A draft's images, restored from the sealed store when first asked for.
    func draft(_ key: String) -> ImageDraft {
        if let d = drafts[key] { return d }
        let d = ImageDraft(key: key, files: files)
        drafts[key] = d
        d.restoring = true
        restorations[key] = Task { [weak self] in
            try? await d.restoreAsync()
            d.restoring = false
            guard let self, !closed, !Task.isCancelled else { return }
            for img in d.images { makeThumb(img) }
        }
        return d
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
    func aim(_ key: String, at machineId: String?, blocked: Bool, machine: MachineView? = nil) {
        targets[key] = machineId
        fileMachines[key] = machine
        #if DEBUG
            if frozen { return }
        #endif
        let d = draft(key)
        let allowed = d.filesAllowed(for: machine)
        if !allowed {
            for item in d.documents {
                item.job?.cancel()
                item.refreshJob?.cancel()
                item.state = .failed(error: fileSupported(machine) ? "附件超过这台电脑的文件数量或大小上限，请移除后重新选择" : "这台电脑不支持发送文件")
            }
        }
        if let machineId, !blocked {
            for item in d.toSend(machineId: machineId) where !item.isFile || allowed {
                uploads.upload(item, machineId: machineId)
            }
        }
    }

    private func machine(_ key: String) -> @MainActor () -> String? {
        { [weak self] in self?.targets[key] }
    }

    /// ＋ was tapped.
    func plus(_ key: String, blocked: String?, max: Int, filesSupported: Bool = false) {
        if let blocked {
            say(key, blocked)
        } else if filesSupported {
            picking[key] = .attachments
        } else if draft(key).photos.count >= max {
            say(key, "一条消息最多 \(max) 张")
        } else {
            // Asking whether the clipboard has an image does not read it, so nothing is asked of the user yet.
            picking[key] = UIPasteboard.general.hasImages ? .choose : .photos
        }
    }

    /// Adds what was picked, as many as fit, and starts preparing them.
    func add(_ key: String, _ picked: [Source], max: Int) {
        let draft = draft(key)
        if draft.restoring {
            Task { [weak self] in
                await self?.restoredDraft(key)
                guard let self, !closed else { return }
                add(key, picked, max: max)
            }
            return
        }
        let taken = draft.admit(picked, max: max)
        let uris = taken.map { _ in "picked:\(newRequestId())" }
        for (uri, source) in zip(uris, taken) { sources[uri] = source }
        uploads.add(draft, uris: uris, machine: machine(key))
    }

    /// Import a document's persistent grant, off the UI thread. Its bytes stay at the source.
    func addFiles(_ key: String, _ urls: [URL], machine: MachineView?, online: Bool) {
        guard let blocked = fileUploadBlocked(machine, online: online) else {
            let limit = fileLimit(for: machine)
            let bytes = fileByteLimit(for: machine)
            let d = draft(key)
            Task {
                await restoredDraft(key)
                guard !closed else { return }
                for url in urls {
                    guard !closed else { return }
                    if d.documents.count >= limit {
                        say(key, "一条消息最多 \(limit) 个文件，多选的文件没有加入")
                        break
                    }
                    do {
                        let file = try await Task.detached { try PickedFile(url: url, maxBytes: bytes) }.value
                        guard !closed else { return }
                        guard d.documents.count < limit else {
                            say(key, "一条消息最多 \(limit) 个文件")
                            break
                        }
                        let item = PendingImage(id: newRequestId(), uri: nil)
                        item.file = file
                        item.state = .failed(error: "需要重新传到电脑")
                        d.append([item])
                        if let target = targets[key], d.filesAllowed(for: fileMachines[key]) { uploads.upload(item, machineId: target) }
                    } catch { say(key, errorText(error)) }
                }
            }
            return
        }
        say(key, blocked)
    }

    /// Adds the images on the clipboard.
    func paste(_ key: String, max: Int) {
        let providers = UIPasteboard.general.itemProviders.filter {
            $0.hasItemConformingToTypeIdentifier(UTType.image.identifier)
        }
        if providers.isEmpty {
            say(key, "剪贴板里没有图片")
        } else {
            add(key, providers.map { .provider($0) }, max: max)
        }
    }

    func retry(_ key: String, _ img: PendingImage) {
        if img.isFile, !draft(key).filesAllowed(for: fileMachines[key]) {
            img.state = .failed(error: "这台电脑不支持这些文件，请检查文件数量和大小或换一台电脑")
            return
        }
        uploads.retry(draft(key), img, machine: machine(key))
    }

    func remove(_ key: String, _ img: PendingImage) {
        draft(key).remove(img)
        forget([img])
    }

    func setOriginal(_ key: String, _ on: Bool) {
        uploads.setOriginal(draft(key), on, machine: machine(key))
    }

    /// Takes images out of a draft once a started session carries them.
    func used(_ key: String, _ taken: [PendingImage]) {
        draft(key).detach(taken)
        for img in taken { img.release() }
        forget(taken)
    }

    /// Lets go of what is held for images that are gone for good.
    private func forget(_ images: [PendingImage]) {
        for img in images {
            thumbs[img.id] = nil
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
        let unreadable = UserError("读不到这张图片")
        guard let source = sources[uri] else { throw unreadable }
        let data: Data
        switch source {
        case .data(let d): return d
        case .item(let item):
            guard let photo = try await item.loadTransferable(type: PickedPhoto.self) else {
                throw unreadable
            }
            data = try photo.read.get()
        case .provider(let provider):
            // The clipboard hands over bytes, not a file: there is no size to ask for first.
            data = try await withCheckedThrowingContinuation { done in
                _ = provider.loadDataRepresentation(forTypeIdentifier: UTType.image.identifier) {
                    d, error in
                    if let d {
                        done.resume(returning: d)
                    } else {
                        done.resume(throwing: error ?? unreadable)
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
        notices[key] = text
    }

    func clearNotice(_ key: String, was text: String) {
        if notices[key] == text { notices[key] = nil }
    }

    // MARK: Messages with images

    /// Sends a message with the images of a session's draft; false when the
    /// draft has none. They are first kept on disk, sealed, off the main
    /// actor; only once that worked is the message sent and the images taken
    /// out of the draft. If keeping them failed nothing is sent, the draft
    /// stays as it is and `done` gets the reason.
    func send(
        sid: String, machineId: String, store: SessionStore, text: String, mode: PBSendMode,
        commands: (any SessionCommands)? = nil,
        keep: (@Sendable (String, OutgoingRecord, Bool) throws -> Void)? = nil,
        done: @escaping @MainActor (Result<Void, Error>) -> Void
    ) -> Bool {
        let draft = draft(Self.sessionKey(sid))
        let picked = draft.images
        if picked.isEmpty { return false }
        uploads.sendAll(draft, machineId: machineId)
        guard draft.ready(machineId: machineId) else {
            Task { done(.failure(UserError("附件需要重新传到电脑，请上传完成后再发送"))) }
            return true
        }
        let original = draft.original
        let o = Outgoing(
            requestId: newRequestId(), text: text, imageIds: draft.ids(machineId: machineId), fileIds: draft.fileIds(machineId: machineId),
            images: picked, original: original, mode: mode)
        let record = o.record
        let records = records
        let commands: any SessionCommands = commands ?? remote
        Task {
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
            if closed {
                done(.failure(UserError("已退出账户")))
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
                done(.failure(UserError("附件需要重新传到电脑，请上传完成后再发送")))
                return
            }
            // The message has them now; images picked meanwhile stay in the box.
            draft.detach(picked)
            store.add(sid, o)
            hold(sid, o, store)
            done(kept)
            store.deliver(commands, sid: sid, o)
        }
        return true
    }

    /// Brings back the messages with images a run that ended left behind, once per session and run.
    func restoreOutgoing(_ sid: String, store: SessionStore) {
        guard restored.insert(sid).inserted else { return }
        let records = records
        restorations["out:" + sid] = Task { [weak self] in
            let list = (try? await Task.detached { try records.loadRecords(sid: sid) }.value) ?? []
            guard let self, !closed, !Task.isCancelled else { return }
            for record in list where store.find(sid, requestId: record.requestId) == nil {
                let o = OutgoingRecords.revive(record)
                store.add(sid, o)
                for img in o.images { makeThumb(img) }
                hold(sid, o, store)
            }
        }
    }

    /// Puts a message's images back into its session's draft; false when they do not fit, and then nothing moved.
    func putBack(sid: String, max: Int, maxFiles: Int = 6, _ o: Outgoing) -> Bool {
        if o.images.isEmpty { return true }
        let draft = draft(Self.sessionKey(sid))
        guard draft.attach(o.images, max: max, maxFiles: maxFiles, wereOriginal: o.original) else { return false }
        letGo(sid, o)
        // Its images live on in the draft. Its record goes once they are on disk there: until then it is the
        // only copy a reclaimed process would find.
        draft.whenKept(o.images.map(\.id)) { [weak self] in self?.dropRecord(sid, o.requestId) }
        return true
    }

    /// Follows a message until it is let go of (`settle`) or removed, whether or not the chat is on screen.
    private func hold(_ sid: String, _ o: Outgoing, _ store: SessionStore) {
        kept[sid, default: []].append(o)
        observe(sid, store)
        guard watchers[sid] == nil else { return }
        let feed = remote.events(sid)
        watchers[sid] = Task { [weak self] in
            for await events in feed.updates() {
                guard let self, !Task.isCancelled else { return }
                let inputs = await Task.detached { inputStates(events.events) }.value
                if Task.isCancelled { return }
                self.settle(sid, inputs, store)
            }
        }
    }

    /// A message is no longer followed. With the session's last one its events are let go of too: waiting
    /// for the next batch to notice would keep the whole history of a session that sends none, one that
    /// ended or whose computer is offline.
    private func letGo(_ sid: String, _ o: Outgoing) {
        kept[sid]?.removeAll { $0 === o }
        if kept[sid]?.isEmpty == true {
            kept[sid] = nil
            watchers.removeValue(forKey: sid)?.cancel()
        }
    }

    /// Lets go of messages submitted or canceled by the user (`letsGo`): their images' bytes, thumbnails
    /// and the copies on disk.
    private func settle(_ sid: String, _ inputs: [String: PBInputState], _ store: SessionStore) {
        for o in recorded(kept[sid] ?? [], inputs: inputs) {
            store.remove(sid, o)
            release(sid, o)
        }
    }

    /// Notices a message the user removed from the chat: its record must not come back with the next launch.
    private func observe(_ sid: String, _ store: SessionStore) {
        withObservationTracking {
            _ = store.outgoing(sid)
        } onChange: { [weak self] in
            Task { @MainActor in
                guard let self else { return }
                for o in self.kept[sid] ?? [] where store.find(sid, requestId: o.requestId) == nil {
                    self.release(sid, o)
                }
                if self.kept[sid]?.isEmpty == false { self.observe(sid, store) }
            }
        }
    }

    private func release(_ sid: String, _ o: Outgoing) {
        letGo(sid, o)
        dropRecord(sid, o.requestId)
        for img in o.images { img.release() }
        forget(o.images)
    }

    private func dropRecord(_ sid: String, _ requestId: String) {
        let records = records
        Task.detached { try? records.remove(sid: sid, requestId: requestId) }
    }

    #if DEBUG
        /// Resolve a debug pick when the signal arrives, after project settings have loaded.
        func debugAddFiles(_ key: String, _ urls: [URL]) {
            addFiles(key, urls, machine: fileMachines[key], online: remote.current.conn == .online)
        }

        /// Shows a message with images as it would stand after sending, for sample data.
        func debugShow(_ sid: String, _ o: Outgoing, store: SessionStore) {
            if store.find(sid, requestId: o.requestId) == nil { store.add(sid, o) }
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
