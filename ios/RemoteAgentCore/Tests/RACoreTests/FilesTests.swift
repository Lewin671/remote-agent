import CryptoKit
import Foundation
import RAProto
import Testing
@testable import RACore

private actor FileReceiver: UploadChannel {
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

@MainActor @Suite struct FilesTests {
    private func source(_ count: Int = 0, name: String = "report.csv") throws -> (URL, PickedFile) {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let url = folder.appendingPathComponent(name)
        try Data(repeating: 65, count: count).write(to: url)
        return (folder, try PickedFile(url: url))
    }
    private func machine(supported: Bool = true) -> MachineView {
        var m = MachineView(id: "mac", online: true, lastSeenMs: 0, meta: PBMachineMeta())
        m.meta?.capabilities = .with { $0.fileUpload = supported; $0.maxFilesPerMessage = 2; $0.maxFileUploadBytes = 10 }
        return m
    }
    private func pending(_ source: PickedFile) -> PendingImage {
        let p = PendingImage(id: newRequestId(), uri: nil)
        p.file = source
        p.state = .failed(error: "needs upload")
        return p
    }

    @Test func supportIsExplicitAndLimitsUseTheMachinesAdvertisement() throws {
        #expect(!fileSupported(nil))
        #expect(!fileSupported(machine(supported: false)))
        #expect(fileUploadBlocked(machine(supported: false), online: true) == "这台电脑不支持发送文件")
        #expect(fileLimit(for: machine()) == 2)
        #expect(fileByteLimit(for: machine()) == 10)
        let (folder, file) = try source(11)
        defer { try? FileManager.default.removeItem(at: folder) }
        let d = ImageDraft(key: "new:project", files: MemoryFiles(), saves: nil)
        d.append([pending(file)])
        #expect(!d.filesAllowed(for: machine()))
        #expect(!d.filesAllowed(for: machine(supported: false)))
        #expect(throws: (any Error).self) { try PickedFile(url: folder.appendingPathComponent("report.csv"), maxBytes: 10) }
    }

    @Test func the100MiBBoundaryIsCheckedBeforeReadingBytes() throws {
        let (folder, source) = try source()
        defer { try? FileManager.default.removeItem(at: folder) }
        let url = folder.appendingPathComponent(source.name)
        let handle = try FileHandle(forWritingTo: url)
        defer { try? handle.close() }
        try handle.truncate(atOffset: UInt64(maxFileBytes))
        #expect(try PickedFile(url: url).size == maxFileBytes)
        try handle.truncate(atOffset: UInt64(maxFileBytes + 1))
        #expect(throws: (any Error).self) { try PickedFile(url: url) }
    }

    @Test func namesKeepExtensionsAndUnicodeAndSizesAreReadable() {
        #expect(fileChipName("数据.csv") == "数据.csv")
        let name = String(repeating: "报表", count: 40) + ".csv"
        let short = fileChipName(name, maxCharacters: 20)
        #expect(short.count == 20 && short.contains("…") && short.hasSuffix(".csv"))
        #expect(fileSize(0) == "0 B")
        #expect(fileSize(2048) == "2.0 KB")
    }

    @Test func onlyExactAttestedLinesAndTheirEmptySeparatorDisappear() {
        let f = PBSentFile.with { $0.path = "/uploads/s/files/f/report.csv" }
        #expect(withoutFileLines("看看\n\n@\(f.path)", files: [f]) == "看看")
        #expect(withoutFileLines(f.path, files: [f]).isEmpty)
        #expect(withoutFileLines("看看\n\nFile: \(f.path)", files: [f]) == "看看")
        #expect(withoutFileLines("File: @\(f.path) (file)", files: [f]).isEmpty)
        #expect(withoutFileLines("看看\n\nFile: @\"\(f.path)\" (file)", files: [f]) == "看看")
        #expect(withoutFileLines("File: @\(f.path) (file) more", files: [f]) == "File: @\(f.path) (file) more")
        let ordinary = "提到 \(f.path)\n\(f.path)  \n/elsewhere/file\n\n尾巴"
        #expect(withoutFileLines(ordinary, files: [f]) == ordinary)
        #expect(withoutFileLines("\nabc", files: []) == "\nabc")
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
        item.imageId = "file-1"; item.state = .ready; item.expiresMs = 5
        #expect(item.readyFor("mac", now: 2))
        #expect(!item.startUpload(to: "mac", now: 2))
        #expect(item.startUpload(to: "other", now: 2))
        #expect(item.imageId == nil)
        item.imageId = "file-2"; item.state = .ready; item.expiresMs = 3
        let transfer = item.transferId
        #expect(item.startUpload(to: "other", now: 4))
        #expect(item.transferId != transfer && item.imageId == nil)
    }

    @Test func filesCountSeparatelyAndRestoreForcesUploadWhileUnknownRequestsKeepIDs() throws {
        let (folder, file) = try source()
        defer { try? FileManager.default.removeItem(at: folder) }
        let store = MemoryFiles()
        let d = ImageDraft(key: "s", files: store, saves: nil)
        d.append((0..<6).map { _ in pending(file) })
        #expect(d.admit([1, 2, 3], max: 2) == [1, 2])
        #expect(!d.attach([pending(file)], max: 6))
        let p = pending(file)
        p.imageId = "first-file-id"; p.machineId = "mac"; p.state = .ready; p.expiresMs = Int64.max
        let o = Outgoing(requestId: "request", text: "", fileIds: ["first-file-id"], images: [p], mode: .queue)
        let records = OutgoingRecords(files: store)
        try records.save(sid: "session", o.record, original: false)
        let r = try #require(records.load(sid: "session").first)
        #expect(r.state == .unknown && r.fileIds == ["first-file-id"] && r.mode == .queue)
        let input = ImageDraft(key: "back", files: store, saves: nil)
        #expect(input.attach(r.images, max: 6))
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
        catch { #expect(errorText(error) == "读不到源文件，请移除后重新选择") }
    }

    @Test func queueProjectionKeepsFileMetadata() {
        let file = PBSentFile.with { $0.name = "data.csv"; $0.fileID = "file"; $0.sizeBytes = 42 }
        var derived = Derived()
        derived.inputDetails["r"] = .with { $0.requestID = "r"; $0.status = .queued; $0.files = [file] }
        #expect(pendingInputs(derived).first?.files == [file])
    }
}
