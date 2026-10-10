// The images picked for a message and the messages sent with them, kept
// sealed on the phone until the CLI has them (architecture §17): the part of
// Android's ui/ImageDrafts.kt and ui/Actions.kt that needs neither a picker
// nor a connection. Preparing pixels and starting uploads is the app's; it
// drives these objects, which SwiftUI can watch.
import Foundation
import Observation
import RAProto

/// Sealed files by folder and name: the pending-image store, or memory in tests.
public protocol SealedFiles: Sendable {
    func put(folder: String, name: String, data: Data) throws
    func get(folder: String, name: String) throws -> Data?
    func remove(folder: String, name: String) throws
    func clearFolder(_ folder: String) throws
}

/// An image as it goes to the machine: prepared on the phone from what was picked.
public struct Processed: Equatable, Sendable {
    public var bytes: Data
    public var format: PBImageFormat
    public var width: Int
    public var height: Int
    public var note: String

    public init(bytes: Data, format: PBImageFormat, width: Int, height: Int, note: String = "") {
        self.bytes = bytes
        self.format = format
        self.width = width
        self.height = height
        self.note = note
    }
}

/// One image picked for a message. It is processed on the phone, then sent
/// to the machine the message goes to; changing machines sends it again.
/// The processed bytes stay (encrypted on disk) until the CLI has the
/// message, so nothing has to be picked again.
@MainActor @Observable
public final class PendingImage: Identifiable {
    public enum State: Equatable, Sendable {
        case processing
        case uploading(progress: Float)
        case ready
        case failed(error: String)
    }

    public nonisolated let id: String
    /// Where it was picked from, for preparing it again as an original; nil once that is not known.
    public nonisolated let uri: String?

    public var processed: Processed?
    public var state: State = .processing

    /// The machine it goes to (the latest asked for) and the image ID the daemon gave it there.
    public var machineId: String?
    public var imageId: String?
    public var transferId: String = newRequestId()
    /// What is preparing or uploading it now, to stop when that is overtaken.
    @ObservationIgnored public var job: Task<Void, Never>?

    public init(id: String, uri: String?) {
        self.id = id
        self.uri = uri
    }

    public func readyFor(_ machine: String) -> Bool { state == .ready && machineId == machine && imageId != nil }

    /// Lets go of the bytes and stops what works on it.
    public func release() {
        job?.cancel()
        job = nil
        processed = nil
    }

    /// Marks the image as on its way to `machineId`, unless it is there or on
    /// its way there already; true when an upload is to be started. An upload
    /// to another machine is dropped: the latest target wins, with a transfer
    /// of its own.
    public func startUpload(to machineId: String) -> Bool {
        if !needsUpload(self, machineId: machineId) { return false }
        job?.cancel()
        if self.machineId != machineId {
            transferId = newRequestId()
            imageId = nil
        }
        self.machineId = machineId
        state = .uploading(progress: 0)
        return true
    }
}

/// Whether `img` must (again) be sent to `machineId`: not when it is there
/// or already on its way there. An upload to another machine does not
/// count: the latest target wins and the old upload is dropped.
@MainActor
public func needsUpload(_ img: PendingImage, machineId: String) -> Bool {
    var uploadingThere = false
    if case .uploading = img.state { uploadingThere = img.machineId == machineId }
    return img.processed != nil && !img.readyFor(machineId) && !uploadingThere
}

/// Whether a machine takes images, and if not, why, in words.
public func uploadBlocked(_ m: MachineView?, online: Bool, name: String) -> String? {
    guard let m else { return "先选一台电脑" }
    guard let caps = capabilities(m) else { return "还没读到 \(name) 的信息，稍后才能添加图片" }
    // The daemon's folder has spaces in its path, which the CLIs do not take.
    if !caps.imageUpload { return "\(name) 上 remote-agent 的目录路径带空格，不能发图片" }
    if !m.online || !online { return "\(name) 离线，连上后才能添加图片" }
    return nil
}

/// The most images a message may carry to this machine: six, or fewer if the daemon says so.
public func imageLimit(for m: MachineView?) -> Int {
    guard let n = capabilities(m).map({ Int($0.maxImagesPerMessage) }), n >= 1, n < maxImages else { return maxImages }
    return n
}

private let orderName = "order"

/// An image as the store keeps it.
struct StoredImage: Sendable {
    var id: String
    var uri: String?
    var processed: Processed?
}

private func utf8(_ s: String) -> Data { Data(s.utf8) }
private func text(_ d: Data) -> String { String(decoding: d, as: UTF8.self) }

/// Writes images under `folder`. Which of the folder's images belong to it, and in what order, is `saveOrder`'s to say.
func saveImages(_ files: any SealedFiles, folder: String, images: [StoredImage], original: Bool) throws {
    for img in images {
        guard let p = img.processed else { continue }
        try files.put(folder: folder, name: img.id + ".img", data: p.bytes)
        let meta = ["\(p.format.rawValue)", "\(p.width)", "\(p.height)", original ? "1" : "0", img.uri ?? "", p.note].joined(separator: "\n")
        try files.put(folder: folder, name: img.id + ".meta", data: utf8(meta))
    }
}

/// Writes the order file: the whole selection as picked, so a restore keeps it. Written once per save, after the
/// images it lists, so that it never names fewer than are kept.
func saveOrder(_ files: any SealedFiles, folder: String, ids: [String], original: Bool) throws {
    try files.put(folder: folder, name: orderName, data: utf8(([original ? "1" : "0"] + ids).joined(separator: "\n")))
}

/// Images kept under `folder`, in the order they were picked, and whether they were originals. One whose record does not read is left out.
func loadStored(_ files: any SealedFiles, folder: String) throws -> (images: [StoredImage], original: Bool) {
    guard let order = try files.get(folder: folder, name: orderName).map({ text($0).split(ascii: 0x0A) }) else { return ([], false) }
    let original = order.first == "1"
    var out = [StoredImage]()
    for id in order.dropFirst() where !id.isEmpty {
        guard let meta = try files.get(folder: folder, name: "\(id).meta").map({ text($0).split(ascii: 0x0A) }),
            let bytes = try files.get(folder: folder, name: "\(id).img"), meta.count >= 6,
            let number = Int(meta[0]), let width = Int(meta[1]), let height = Int(meta[2])
        else { continue }
        var format = PBImageFormat(rawValue: number) ?? .jpeg
        if case .UNRECOGNIZED = format { format = .jpeg }
        out.append(
            StoredImage(
                id: id, uri: meta[4].isEmpty ? nil : meta[4],
                processed: Processed(bytes: bytes, format: format, width: width, height: height, note: meta[5])))
    }
    return (out, original)
}

@MainActor
private func revive(_ stored: [StoredImage]) -> [PendingImage] {
    stored.map { s in
        let img = PendingImage(id: s.id, uri: s.uri)
        img.processed = s.processed
        // Uploads are the daemon's for a while only: send them again.
        img.state = .failed(error: "需要重新传到电脑")
        return img
    }
}

/// The images of one draft (a session's message box or a project's new-session card).
@MainActor @Observable
public final class ImageDraft {
    public nonisolated let key: String
    @ObservationIgnored private let files: any SealedFiles
    /// Where saves and removals run, one at a time for this draft; nil runs them in place.
    @ObservationIgnored private let saves: DispatchQueue?
    /// Images whose current bytes are on disk or on their way there, with the save that writes them; an image
    /// prepared again is written again.
    @ObservationIgnored private var saved = [String: Int]()
    @ObservationIgnored private var lastSave = 0
    /// Images being prepared again: `processed` still holds what they were before.
    @ObservationIgnored private var stale = Set<String>()
    /// Images a save has put on disk here.
    @ObservationIgnored private var landed = Set<String>()
    /// What waits for images to be on disk here or gone from the draft (`whenKept`).
    @ObservationIgnored private var waiting: [(ids: [String], then: @MainActor () -> Void)] = []

    public var images: [PendingImage] = []
    public private(set) var original = false

    /// Said once after a pick or a put back: how many did not fit.
    public var notice = ""

    /// `saves`: the queue the sealed files are written and removed on, serial
    /// so that an older snapshot never lands after a newer one nor after the
    /// removal of what it wrote; nil works in place, as tests want.
    public init(key: String, files: any SealedFiles, saves: DispatchQueue? = DispatchQueue(label: "remote-agent.image-draft")) {
        self.key = key
        self.files = files
        self.saves = saves
    }

    /// Which of a pick go in, in the order picked: as many as fit under
    /// `max`, with `notice` saying how many did not.
    public func admit<T>(_ picked: [T], max: Int) -> [T] {
        let (taken, left) = takePicked(have: images.count, picked: picked, max: max)
        notice = left > 0 ? "一条消息最多 \(max) 张，多选的 \(left) 张没有加入" : ""
        return taken
    }

    /// Adds images just picked, still to be prepared, and keeps their order.
    public func append(_ picked: [PendingImage]) {
        images.append(contentsOf: picked)
        persist()
    }

    /// Saves the draft as it is now: every prepared image not yet on disk and
    /// the order of the whole selection.
    public func persist() {
        let now = images
        let fresh = now.filter { saved[$0.id] == nil && !stale.contains($0.id) && $0.processed != nil }
        let write = fresh.map { StoredImage(id: $0.id, uri: $0.uri, processed: $0.processed) }
        let ids = now.map(\.id)
        // Taken as written now; put back below if the write fails.
        lastSave += 1
        let save = lastSave
        for img in fresh { saved[img.id] = save }
        let files = self.files
        let key = self.key
        let original = self.original
        enqueue {
            try saveImages(files, folder: key, images: write, original: original)
            try saveOrder(files, folder: key, ids: ids, original: original)
        } then: { [weak self] failure in
            self?.wrote(write.map(\.id), save, failure)
        }
    }

    /// Runs `work` after the saves and removals asked for before it, then tells the main actor how it went.
    private func enqueue(_ work: @escaping @Sendable () throws -> Void, then: (@MainActor @Sendable (String?) -> Void)? = nil) {
        let run: @Sendable () -> String? = {
            do {
                try work()
                return nil
            } catch {
                return error.localizedDescription
            }
        }
        guard let saves else {
            let failure = run()
            then?(failure)
            return
        }
        saves.async {
            let failure = run()
            guard let then else { return }
            // In the order the work ran.
            DispatchQueue.main.async { MainActor.assumeIsolated { then(failure) } }
        }
    }

    /// A save ran. An image prepared again or taken out since is no longer that save's to answer for.
    private func wrote(_ ids: [String], _ save: Int, _ failure: String?) {
        let mine = ids.filter { saved[$0] == save }
        if let failure {
            for id in mine { saved[id] = nil }
            notice = "这张图未保存到手机：\(failure)。当前仍可发送，关闭应用后可能丢失"
        } else {
            landed.formUnion(mine)
            settle()
        }
    }

    /// Calls `then` once none of the images `ids` needs a copy kept elsewhere: each is on disk here, or gone
    /// from the draft. A save that fails keeps it waiting for the next one.
    public func whenKept(_ ids: [String], _ then: @escaping @MainActor () -> Void) {
        waiting.append((ids, then))
        settle()
    }

    private func settle() {
        var ready = [@MainActor () -> Void]()
        waiting.removeAll { wait in
            let kept = wait.ids.allSatisfy { id in
                // One without bytes has nothing a copy elsewhere could hold either.
                guard let img = images.first(where: { $0.id == id }), img.processed != nil else { return true }
                return landed.contains(id)
            }
            if kept { ready.append(wait.then) }
            return kept
        }
        for then in ready { then() }
    }

    /// An image is being prepared again: its bytes, in memory and on disk, are the old ones until `prepared`.
    public func invalidate(_ img: PendingImage) {
        saved[img.id] = nil
        stale.insert(img.id)
    }

    /// An image has the bytes it was prepared for: they are saved.
    public func prepared(_ img: PendingImage) {
        stale.remove(img.id)
        persist()
    }

    /// Originals or compressed, for this message; true when that changed and every image is to be prepared again from what was picked.
    public func setOriginal(_ v: Bool) -> Bool {
        if v == original { return false }
        original = v
        persist()
        return true
    }

    public func remove(_ img: PendingImage) {
        images.removeAll { $0 === img }
        forget(img)
        img.release()
        persist()
        settle()
    }

    /// Takes an image's files away, after any save of it that is still on its way.
    private func forget(_ img: PendingImage) {
        let id = img.id
        saved[id] = nil
        stale.remove(id)
        landed.remove(id)
        let files = self.files
        let key = self.key
        enqueue {
            try? files.remove(folder: key, name: id + ".img")
            try? files.remove(folder: key, name: id + ".meta")
        }
    }

    public func ready(machineId: String?) -> Bool {
        guard let machineId else { return false }
        return !images.isEmpty && images.allSatisfy { $0.readyFor(machineId) }
    }

    public func ids(machineId: String) -> [String] { images.compactMap { $0.readyFor(machineId) ? $0.imageId : nil } }

    public var totalBytes: Int64 { images.reduce(0) { $0 + Int64($1.processed?.bytes.count ?? 0) } }

    /// The images to send to `machineId` now, e.g. after the new-session card changed machines: those prepared and not there.
    public func toSend(machineId: String) -> [PendingImage] {
        images.filter { $0.processed != nil && !$0.readyFor(machineId) && $0.state != .processing }
    }

    /// Takes `taken` out of the draft once a message carries them; images
    /// picked since stay. 原图 goes back to unticked: it is for one message.
    /// The caller keeps the images until the CLI has them.
    public func detach(_ taken: [PendingImage]) {
        images.removeAll { img in taken.contains { $0 === img } }
        for img in taken { forget(img) }
        if images.isEmpty { original = false }
        persist()
        settle()
    }

    /// Puts images back, e.g. from a message that was not submitted. Nothing
    /// is dropped: when they do not fit with what is here already, nothing
    /// is put back and `notice` says why. Returns whether they went in.
    /// `wereOriginal` nil keeps what the draft has.
    @discardableResult
    public func attach(_ back: [PendingImage], max: Int, wereOriginal: Bool? = nil) -> Bool {
        if images.count + back.count > max {
            notice = "输入框里已有 \(images.count) 张图，放回这 \(back.count) 张会超过 \(max) 张。先移除一些再放回"
            return false
        }
        if images.isEmpty { original = wereOriginal ?? original }
        for img in back {
            img.imageId = nil
            img.machineId = nil
            img.state = .failed(error: "需要重新传到电脑")
            images.append(img)
        }
        persist()
        return true
    }

    /// Forgets every image, e.g. when the draft is dropped.
    public func clear() {
        for img in images { remove(img) }
        notice = ""
        original = false
        let files = self.files
        let key = self.key
        enqueue { try? files.clearFolder(key) }
    }

    /// Images kept from before the process was reclaimed, in the order they were picked; they go to the machine again.
    public func restore() throws {
        let (list, orig) = try loadStored(files, folder: key)
        original = orig
        images.append(contentsOf: revive(list))
        for img in list {
            saved[img.id] = lastSave
            landed.insert(img.id)
        }
    }
}

/// A message sent from this device. Its text stays in memory only (the disk
/// never holds plaintext); the daemon dedupes by `requestId`, so a retry
/// never submits it twice.
@MainActor @Observable
public final class Outgoing: Identifiable {
    public enum State: Sendable { case sending, sent, failed, unknown }

    public nonisolated let requestId: String
    public nonisolated let text: String
    /// Image IDs on the machine, sent with the message; the same on every retry.
    public nonisolated let imageIds: [String]
    /// The picked images, for their thumbnails while sending and to put them back.
    public let images: [PendingImage]
    /// They were sent as originals.
    public nonisolated let original: Bool
    public nonisolated let mode: PBSendMode

    public var state = State.sending
    public var error = ""

    public nonisolated var id: String { requestId }

    public init(
        requestId: String, text: String, imageIds: [String] = [], images: [PendingImage] = [], original: Bool = false,
        mode: PBSendMode = .steer
    ) {
        self.requestId = requestId
        self.text = text
        self.imageIds = imageIds
        self.images = images
        self.original = original
        self.mode = mode
    }

    /// The message as `OutgoingRecords` keeps it.
    public var record: OutgoingRecord {
        OutgoingRecord(
            requestId: requestId, text: text, imageIds: imageIds,
            images: images.map { StoredImage(id: $0.id, uri: $0.uri, processed: $0.processed) }, original: original, mode: mode)
    }
}

/// A message with its images as it is written to the sealed store: a value, so that writing can leave the main thread.
public struct OutgoingRecord: Sendable {
    public var requestId: String
    public var text: String
    public var imageIds: [String]
    var images: [StoredImage]
    public var original: Bool
    public var mode: PBSendMode
}

/// The names Android's enum gives the modes, which the store's files hold.
private func modeName(_ m: PBSendMode) -> String {
    switch m {
    case .steer: return "SEND_MODE_STEER"
    case .queue: return "SEND_MODE_QUEUE"
    case .unspecified: return "SEND_MODE_UNSPECIFIED"
    case .UNRECOGNIZED: return "UNRECOGNIZED"
    }
}

private func modeNamed(_ s: String) -> PBSendMode? {
    switch s {
    case "SEND_MODE_STEER": return .steer
    case "SEND_MODE_QUEUE": return .queue
    case "SEND_MODE_UNSPECIFIED": return .unspecified
    default: return nil
    }
}

/// Messages with images kept on disk, sealed, from send until the CLI has
/// them (architecture §17): a reclaimed process can then still retry the
/// same request or put the message back. An index lists them by session.
public struct OutgoingRecords: Sendable {
    private let files: any SealedFiles
    /// The index is read, changed and written back: one change at a time,
    /// whichever thread it comes from, or the later write undoes the other.
    private static let indexLock = NSLock()

    public init(files: any SealedFiles) {
        self.files = files
    }

    private func folder(_ sid: String, _ requestId: String) -> String { "out:\(sid):\(requestId)" }

    private func index() throws -> [String] {
        try files.get(folder: "outbox", name: "index").map { text($0).split(ascii: 0x0A).filter { !$0.isEmpty } } ?? []
    }

    private func writeIndex(_ lines: [String]) throws {
        try files.put(folder: "outbox", name: "index", data: utf8(lines.joined(separator: "\n")))
    }

    /// Keeps a message; it may run off the main thread.
    public func save(sid: String, _ o: OutgoingRecord, original: Bool) throws {
        let f = folder(sid, o.requestId)
        do {
            try saveImages(files, folder: f, images: o.images, original: original)
            try saveOrder(files, folder: f, ids: o.images.map(\.id), original: original)
            try files.put(folder: f, name: "text", data: utf8(o.text))
            try files.put(folder: f, name: "ids", data: utf8(o.imageIds.joined(separator: "\n")))
            try files.put(folder: f, name: "mode", data: utf8(modeName(o.mode)))
            let line = "\(sid)\t\(o.requestId)"
            try Self.indexLock.withLock {
                let idx = try index()
                if !idx.contains(line) { try writeIndex(idx + [line]) }
            }
        } catch {
            // Not in the index: nothing would find what was written, and it would take up room unseen.
            try? files.clearFolder(f)
            throw error
        }
    }

    /// The messages kept for a session, as a run that ended left them: whether they reached the computer is not known.
    @MainActor
    public func load(sid: String) throws -> [Outgoing] {
        try index().compactMap { line -> Outgoing? in
            let parts = line.split(ascii: 0x09)
            guard parts.count == 2, parts[0] == sid else { return nil }
            let f = folder(sid, parts[1])
            guard let body = try files.get(folder: f, name: "text") else { return nil }
            let ids = try files.get(folder: f, name: "ids").map { text($0).split(ascii: 0x0A).filter { !$0.isEmpty } } ?? []
            let (images, original) = try loadStored(files, folder: f)
            let mode = try files.get(folder: f, name: "mode").flatMap { modeNamed(text($0)) } ?? .steer
            let o = Outgoing(requestId: parts[1], text: text(body), imageIds: ids, images: revive(images), original: original, mode: mode)
            // From a run that ended: whether it reached the computer is not known.
            o.state = .unknown
            o.error = "App 重启过，不确定这条有没有送到"
            return o
        }
    }

    public func remove(sid: String, requestId: String) throws {
        try files.clearFolder(folder(sid, requestId))
        try Self.indexLock.withLock {
            try writeIndex(try index().filter { $0 != "\(sid)\t\(requestId)" })
        }
    }
}

/// The latest state of each message by request ID, from a session's events.
public func inputStates(_ events: some Sequence<PBEvent>) -> [String: PBInputState] {
    var out = [String: PBInputState]()
    for e in events { if case .inputState(let st) = e.kind { out[st.requestID] = st } }
    return out
}

/// Whether a message in this state is let go of: submitted, or canceled by the user. One the session
/// ended on (canceled, reason "exited") is kept: the chat offers to put it back into the message box,
/// text and images.
public func letsGo(_ state: PBInputState?) -> Bool {
    guard let state else { return false }
    return state.status == .submitted || (state.status == .canceled && state.reason != "exited")
}

/// The messages to let go of (`letsGo`).
@MainActor
public func recorded(_ list: [Outgoing], inputs: [String: PBInputState]) -> [Outgoing] {
    list.filter { letsGo(inputs[$0.requestId]) }
}
