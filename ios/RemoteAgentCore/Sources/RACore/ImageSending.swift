// What happens to a picked image between the picker and the message it
// goes with: it is prepared, then sent to the machine the message goes to,
// and both can be asked for again (architecture §17). The part of Android's
// `ImageDraft` (ui/ImageDrafts.kt) that drives jobs: `add`, `process`,
// `upload`, `sendAllTo`, `retry` and `setOriginal`. Reading the picked
// bytes and the machine's upload commands are handed in, so the rules are
// tested without a picker or a daemon.
import Foundation
import RAProto

@MainActor
public final class ImageUploads {
    /// Reads what was picked at `uri` and prepares it, compressed or as an original. Runs off the main actor.
    public typealias Prepare = @Sendable (_ uri: String, _ original: Bool) async throws -> Processed

    private let prepare: Prepare
    private let channel: @Sendable (_ machineId: String) -> any UploadChannel
    /// Whether what was picked at a URI can still be read: not after the run that picked it.
    private let readable: @MainActor (_ uri: String) -> Bool
    /// Told when an image has new bytes, for whoever draws its thumbnail.
    public var onPrepared: (@MainActor (PendingImage) -> Void)?

    public init(
        prepare: @escaping Prepare, channel: @escaping @Sendable (_ machineId: String) -> any UploadChannel,
        readable: @escaping @MainActor (_ uri: String) -> Bool = { _ in true }
    ) {
        self.prepare = prepare
        self.channel = channel
        self.readable = readable
    }

    /// Adds what was picked at `uris` (already cut to what fits, `ImageDraft.admit`) and starts preparing each.
    @discardableResult
    public func add(_ draft: ImageDraft, uris: [String], machine: @escaping @MainActor () -> String?) -> [PendingImage] {
        let added = uris.map { PendingImage(id: newRequestId(), uri: $0) }
        draft.append(added)
        for img in added { process(draft, img, machine: machine) }
        return added
    }

    /// Prepares an image from what was picked, again if need be, and sends it to the machine `machine` names then.
    public func process(_ draft: ImageDraft, _ img: PendingImage, machine: @escaping @MainActor () -> String?) {
        img.job?.cancel()
        draft.invalidate(img)
        img.state = .processing
        img.machineId = nil
        img.imageId = nil
        img.transferId = newRequestId()
        guard let uri = img.uri else {
            img.state = .failed(error: "读不到这张图片")
            return
        }
        let original = draft.original
        let prepare = self.prepare
        img.job = Task { [weak self] in
            do {
                let p = try await prepare(uri, original)
                // Overtaken: removed, or being prepared again.
                if Task.isCancelled { return }
                img.processed = p
                self?.onPrepared?(img)
                // Kept for a reclaimed process; sending does not depend on it.
                draft.prepared(img)
                img.job = nil
                if let m = machine() { self?.upload(img, machineId: m) } else { img.state = .failed(error: "先选一台电脑") }
            } catch {
                if Task.isCancelled || error is CancellationError { return }
                img.state = .failed(error: errorText(error))
            }
        }
    }

    /// Sends the image to `machineId` unless it is there or on its way there.
    /// An upload to another machine is dropped: the latest target wins.
    public func upload(_ img: PendingImage, machineId: String) {
        guard let p = img.processed, img.startUpload(to: machineId) else { return }
        let ch = channel(machineId)
        img.job = Task {
            do {
                let id: String
                do {
                    id = try await uploadImage(ch, transferId: img.transferId, data: p.bytes) { done, total in
                        if !Task.isCancelled, img.machineId == machineId {
                            img.state = .uploading(progress: total > 0 ? Float(done) / Float(total) : 0)
                        }
                    }
                } catch let e as any UploadFailure where e.code == "not_found" && !Task.isCancelled {
                    // The transfer expired on the daemon: start a new one.
                    img.transferId = newRequestId()
                    id = try await uploadImage(ch, transferId: img.transferId, data: p.bytes)
                }
                if Task.isCancelled { return }
                if img.machineId == machineId {
                    img.imageId = id
                    img.state = .ready
                }
            } catch {
                if Task.isCancelled || error is CancellationError { return }
                if img.machineId == machineId { img.state = .failed(error: errorText(error)) }
            }
        }
    }

    /// Makes sure every image is on `machineId`, e.g. after the new-session card changed machines.
    public func sendAll(_ draft: ImageDraft, machineId: String) {
        for img in draft.toSend(machineId: machineId) { upload(img, machineId: machineId) }
    }

    /// What a tap on a failed image does: prepares it if that is what failed, sends it otherwise.
    public func retry(_ draft: ImageDraft, _ img: PendingImage, machine: @escaping @MainActor () -> String?) {
        if img.processed == nil, img.uri != nil {
            process(draft, img, machine: machine)
        } else if let m = machine() {
            upload(img, machineId: m)
        }
    }

    /// Whether 原图 can still change what a draft sends: some image of it can be read again.
    public func canReprepare(_ draft: ImageDraft) -> Bool {
        draft.images.contains { img in img.uri.map(readable) ?? false }
    }

    /// Originals or compressed, for this message: every image that can still be read is prepared again from what was picked.
    public func setOriginal(_ draft: ImageDraft, _ v: Bool, machine: @escaping @MainActor () -> String?) {
        guard draft.setOriginal(v) else { return }
        for img in draft.images {
            if let uri = img.uri, readable(uri) { process(draft, img, machine: machine) }
        }
    }
}
