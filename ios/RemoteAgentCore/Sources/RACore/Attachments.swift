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

/// Writes images and their order under `folder`; the order file lists them as picked, so a restore keeps it.
func saveImages(_ files: any SealedFiles, folder: String, images: [StoredImage], original: Bool) throws {
    for img in images {
        guard let p = img.processed else { continue }
        try files.put(folder: folder, name: img.id + ".img", data: p.bytes)
        let meta = ["\(p.format.rawValue)", "\(p.width)", "\(p.height)", original ? "1" : "0", img.uri ?? "", p.note].joined(separator: "\n")
        try files.put(folder: folder, name: img.id + ".meta", data: utf8(meta))
    }
    try saveOrder(files, folder: folder, ids: images.map(\.id), original: original)
}

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
    /// Where saves run, one at a time for this draft; nil runs them in place.
    @ObservationIgnored private let saves: DispatchQueue?
    /// Images whose current bytes are on disk; an image prepared again is written again.
    @ObservationIgnored private var saved = Set<String>()

    public var images: [PendingImage] = []
    public private(set) var original = false

    /// Said once after a pick or a put back: how many did not fit.
    public var notice = ""

    /// `saves`: the queue the sealed files are written on, serial so that an
    /// older snapshot never lands after a newer one; nil writes in place, as
    /// tests want.
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
    /// the order of the whole selection. Call it when an image was prepared.
    public func persist() {
        let now = images
        let fresh = now.filter { !saved.contains($0.id) && $0.processed != nil }
        let write = fresh.map { StoredImage(id: $0.id, uri: $0.uri, processed: $0.processed) }
        let ids = now.map(\.id)
        // Taken as written now; put back below if the write fails.
        for img in fresh { saved.insert(img.id) }
        let files = self.files
        let key = self.key
        let original = self.original
        let work: @Sendable () -> String? = {
            do {
                try saveImages(files, folder: key, images: write, original: original)
                try saveOrder(files, folder: key, ids: ids, original: original)
                return nil
            } catch {
                return error.localizedDescription
            }
        }
        if let saves {
            saves.async { [weak self] in
                guard let failure = work() else { return }
                Task { @MainActor in self?.saveFailed(write.map(\.id), failure) }
            }
        } else if let failure = work() {
            saveFailed(write.map(\.id), failure)
        }
    }

    private func saveFailed(_ ids: [String], _ why: String) {
        for id in ids { saved.remove(id) }
        notice = "这张图未保存到手机：\(why)。当前仍可发送，关闭应用后可能丢失"
    }

    /// An image is being prepared again: its bytes on disk are the old ones.
    public func invalidate(_ img: PendingImage) {
        saved.remove(img.id)
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
    }

    private func forget(_ img: PendingImage) {
        saved.remove(img.id)
        try? files.remove(folder: key, name: img.id + ".img")
        try? files.remove(folder: key, name: img.id + ".meta")
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
        try? files.clearFolder(key)
    }

    /// Images kept from before the process was reclaimed, in the order they were picked; they go to the machine again.
    public func restore() throws {
        let (list, orig) = try loadStored(files, folder: key)
        original = orig
        images.append(contentsOf: revive(list))
        for img in list { saved.insert(img.id) }
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
        try saveImages(files, folder: f, images: o.images, original: original)
        try files.put(folder: f, name: "text", data: utf8(o.text))
        try files.put(folder: f, name: "ids", data: utf8(o.imageIds.joined(separator: "\n")))
        try files.put(folder: f, name: "mode", data: utf8(modeName(o.mode)))
        let line = "\(sid)\t\(o.requestId)"
        try Self.indexLock.withLock {
            let idx = try index()
            if !idx.contains(line) { try writeIndex(idx + [line]) }
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
public func inputStates(_ events: some Sequence<PBEvent>) -> [String: PBInputStatus] {
    var out = [String: PBInputStatus]()
    for e in events { if case .inputState(let st) = e.kind { out[st.requestID] = st.status } }
    return out
}

/// The messages the CLI has recorded: the ones to let go of.
@MainActor
public func recorded(_ list: [Outgoing], inputs: [String: PBInputStatus]) -> [Outgoing] {
    list.filter { inputs[$0.requestId] == .submitted }
}
