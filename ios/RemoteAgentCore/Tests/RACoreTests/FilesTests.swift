import CryptoKit
import Foundation
import RAProto
import Testing
@testable import RACore

private actor FileReceiver: UploadChannel {
    func abort(transferId: String) async throws {}
    var received = Data()
    var name = ""
    var digest = Data()
    var total: Int64 = 0
    var biggest = 0
    var lostReply = true
    func begin(transferId: String, format: PBImageFormat, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
        Issue.record("Files must use the file begin command")
        return .init()
    }
    func beginFile(transferId: String, name: String, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
        self.name = name; self.total = total; digest = sha256
        return state()
    }
    func state(done: Bool = false) -> PBImageUploadStatus {
        .with {
            $0.state = done ? .complete : .receiving
            $0.persistedBytes = Int64(received.count)
            $0.maxChunkBytes = 65536
            $0.fileID = done ? "file-1" : ""
            $0.expiresMs = Int64.max
        }
    }
    func put(transferId: String, offset: Int64, data: Data) async throws -> PBImageUploadStatus {
        #expect(offset == received.count)
        biggest = max(biggest, data.count)
        received.append(data)
        if lostReply {
            lostReply = false
            throw UploadError(code: "network", message: "lost response after durable write")
        }
        return state()
    }
    func status(transferId: String) async throws -> PBImageUploadStatus { state() }
    func complete(transferId: String) async throws -> PBImageUploadStatus {
        #expect(received.count == total)
        #expect(Data(SHA256.hash(data: received)) == digest)
        return state(done: true)
    }
}

@MainActor @Suite(.timeLimit(.minutes(2))) struct FilesTests {
    private func source(_ count: Int = 0, name: String = "report.csv") throws -> (URL, PickedFile) {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let url = folder.appendingPathComponent(name)
        try Data(repeating: 65, count: count).write(to: url)
        return (folder, try PickedFile(url: url))
    }
    private func caps(supported: Bool = true) -> AttachmentCapabilities {
        AttachmentCapabilities(supportsFiles: supported, maxFiles: 2, maxFileBytes: 10)
    }
    private func pending(_ source: PickedFile) -> PendingImage {
        let p = PendingImage(id: newRequestId(), uri: nil)
        p.file = source
        p.state = .failed(error: AttachmentFailure(kind: .uploadFailed))
        return p
    }

    @Test func supportIsExplicitAndLimitsUseCoreCapabilities() throws {
        let pbCaps = PBSessionCapabilitiesView.with {
            $0.capabilitiesLoaded = true
            $0.supportsImages = true
            $0.supportsFiles = false
            $0.maxImages = 6
            $0.maxFiles = 2
            $0.maxFileBytes = 10
            $0.maxFileNameBytes = 255
            $0.maxFileSizeText = "10 B"
            $0.galleryAllowsVideo = false
            $0.fileUploadBlockedReason = .with { $0.key = "attachment.update_required" }
            $0.originalImageHint = .with { $0.key = "image.original_hint" }
            $0.fileMetadataNotice = .with { $0.key = "attachment.metadata" }
        }
        let converted = AttachmentCapabilities(pbCaps)
        #expect(!converted.supportsFiles && !converted.galleryAllowsVideo)
        #expect(converted.fileUploadBlocked == L10n.attachmentUpdateRequired())
        #expect(converted.originalImageHint == L10n.imageOriginalHint())
        #expect(converted.fileMetadataNotice == L10n.attachmentMetadata())
        #expect(converted.maxFiles == 2 && converted.maxFileBytes == 10 && converted.maxFileSizeText == "10 B")
    }

    @Test func the100MiBBoundaryIsInspectedWithoutReadingBytes() throws {
        let boundary: Int64 = 100 * 1024 * 1024
        let (folder, source) = try source()
        defer { try? FileManager.default.removeItem(at: folder) }
        let url = folder.appendingPathComponent(source.name)
        let handle = try FileHandle(forWritingTo: url)
        defer { try? handle.close() }
        try handle.truncate(atOffset: UInt64(boundary))
        let atLimit = try PickedFile.inspect(url: url)
        #expect(atLimit.sizeBytes == boundary && !atLimit.isDirectory && !atLimit.sizeUnknown)
        try handle.truncate(atOffset: UInt64(boundary + 1))
        let overLimit = try PickedFile.inspect(url: url)
        #expect(overLimit.sizeBytes == boundary + 1)
    }

    @Test func documentsRestoreWithoutCopyingTheirContentsToTheImageStore() throws {
        let (folder, file) = try source(1024 * 1024)
        defer { try? FileManager.default.removeItem(at: folder) }
        let store = MemoryFiles()
        let d = ImageDraft(key: "session:s", files: store, saves: nil)
        d.append([pending(file)])
        #expect(!store.keys.contains { $0.hasSuffix(".img") })
        #expect(store.puts.allSatisfy { $0.data.count < 16 * 1024 })
        let restored = ImageDraft(key: d.key, files: store, saves: nil)
        try restored.restore()
        #expect(restored.documents.first?.file == file)
        #expect(restored.photos.isEmpty)
        #expect(!restored.ready(machineId: "mac"))
        let item = try #require(restored.documents.first)
        #expect(item.startUpload(to: "mac", now: 1))
        item.machineId = "mac"; item.imageId = "file-1"; item.state = .ready; item.expiresMs = 5
        #expect(item.readyFor("mac", now: 2))
        #expect(!item.startUpload(to: "mac", now: 2))
        #expect(item.startUpload(to: "other", now: 2))
        #expect(item.imageId == nil)
        item.machineId = "other"; item.imageId = "file-2"; item.state = .ready; item.expiresMs = 3
        let transfer = item.transferId
        #expect(item.startUpload(to: "other", now: 4))
        #expect(item.transferId != transfer && item.imageId == nil && item.machineId == nil, "expiry invalidates the old completion synchronously")
    }

    @Test func restoreForcesUploadWhileUnknownRequestsKeepIDs() throws {
        let (folder, file) = try source()
        defer { try? FileManager.default.removeItem(at: folder) }
        let store = MemoryFiles()
        let p = pending(file)
        p.imageId = "first-file-id"; p.machineId = "mac"; p.state = .ready; p.expiresMs = Int64.max
        let o = Outgoing(requestId: "request", text: "", fileIds: ["first-file-id"], images: [p], mode: .queue)
        let records = OutgoingRecords(files: store)
        try records.save(sid: "session", o.record, original: false)
        let r = try #require(records.load(sid: "session").first)
        #expect(r.state == .unknown && r.fileIds == ["first-file-id"] && r.mode == .queue)
        let input = ImageDraft(key: "back", files: store, saves: nil)
        #expect(input.attach(r.images))
        #expect(r.images.first?.imageId == nil)
        #expect(r.fileIds == ["first-file-id"])
        input.remove(try #require(input.documents.first))
        let again = ImageDraft(key: "back", files: store, saves: nil)
        try again.restore()
        #expect(again.images.isEmpty)
    }

    @Test func streamedUploadResumesAfterALostReplyAndPreservesAllBytes() async throws {
        let (folder, source) = try source(defaultChunkBytes * 2 + 17)
        defer { try? FileManager.default.removeItem(at: folder) }
        let receiver = FileReceiver()
        let result = try await uploadFile(receiver, transferId: "transfer", source: source)
        #expect(result.imageId == "file-1")
        #expect(await receiver.biggest <= 65536)
        #expect(await receiver.received.count == source.size)
        #expect(await receiver.name == "report.csv")
    }

    @Test func emptyFilesAreValidAndLostSourcesRequirePickingAgain() async throws {
        let (folder, source) = try source()
        defer { try? FileManager.default.removeItem(at: folder) }
        let receiver = FileReceiver()
        #expect(try await uploadFile(receiver, transferId: "empty", source: source).imageId == "file-1")
        try FileManager.default.removeItem(at: folder.appendingPathComponent(source.name))
        do { _ = try source.digest(); Issue.record("Deleted source was readable") }
        catch { #expect(errorText(error) == L10n.attachmentIosCannotReadTheSourceFileRemoveItAndSelect()) }
    }
}
