import Foundation
import RAProto
import Testing
@testable import RACore

private actor FileMachine {
    struct Transfer { var data = Data(); var total: Int64; var done = false }
    var transfers: [String: Transfer] = [:]
    var begins: [(String, String)] = []
    var aborted: [(String, String)] = []
    var rejectFirst = false
    var holdFirst = false
    var loseComplete = false
    var completes = 0
    var waiting: CheckedContinuation<Void, Never>?
    var validation: (@Sendable (String, String) throws -> Bool)?
    func configure(reject: Bool = false, hold: Bool = false, loseComplete: Bool = false, validation: (@Sendable (String, String) throws -> Bool)? = nil) {
        rejectFirst = reject; holdFirst = hold; self.loseComplete = loseComplete; self.validation = validation
    }
    func begin(_ machine: String, _ id: String, total: Int64) async throws -> PBImageUploadStatus {
        if let validation { #expect(try validation(machine, id), "transfer must be on disk before Begin") }
        begins.append((machine, id))
        if rejectFirst { rejectFirst = false; throw UploadError(code: "already_exists", message: "different sha256") }
        if holdFirst {
            holdFirst = false
            await withCheckedContinuation { waiting = $0 }
        }
        if transfers[machine + id] == nil { transfers[machine + id] = Transfer(total: total) }
        return state(machine, id)
    }
    func release() { waiting?.resume(); waiting = nil }
    func state(_ machine: String, _ id: String) -> PBImageUploadStatus {
        let t = transfers[machine + id]
        return .with {
            $0.state = t?.done == true ? .complete : .receiving
            $0.persistedBytes = Int64(t?.data.count ?? 0)
            $0.maxChunkBytes = 1024
            $0.fileID = t?.done == true ? "file-" + id : ""
            $0.expiresMs = Int64.max
        }
    }
    func put(_ machine: String, _ id: String, offset: Int64, bytes: Data) -> PBImageUploadStatus {
        #expect(transfers[machine + id]?.data.count == Int(offset))
        transfers[machine + id]?.data.append(bytes)
        return state(machine, id)
    }
    func complete(_ machine: String, _ id: String) throws -> PBImageUploadStatus {
        completes += 1
        if loseComplete { loseComplete = false; throw UploadError(code: "network", message: "lost Complete response") }
        transfers[machine + id]?.done = true
        return state(machine, id)
    }
    func abort(_ machine: String, _ id: String) {
        aborted.append((machine, id)); transfers[machine + id] = nil
    }
    func seedPartial(_ machine: String, _ id: String, bytes: Data, total: Int64) {
        transfers[machine + id] = Transfer(data: bytes, total: total)
    }
}
private struct FileMachineChannel: UploadChannel {
    let server: FileMachine
    let machine: String
    func begin(transferId: String, format: PBImageFormat, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
        throw UserError("unexpected image upload")
    }
    func beginFile(transferId: String, name: String, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
        try await server.begin(machine, transferId, total: total)
    }
    func put(transferId: String, offset: Int64, data: Data) async throws -> PBImageUploadStatus { await server.put(machine, transferId, offset: offset, bytes: data) }
    func status(transferId: String) async throws -> PBImageUploadStatus { await server.state(machine, transferId) }
    func complete(transferId: String) async throws -> PBImageUploadStatus { try await server.complete(machine, transferId) }
    func abort(transferId: String) async throws { await server.abort(machine, transferId) }
}
private final class WorkerProgress: @unchecked Sendable {
    let lock = NSLock()
    var value = 0
    func tick() { lock.withLock { value += 1 } }
    var count: Int { lock.withLock { value } }
}

@MainActor @Suite struct FileLifecycleTests {
    private func picked() throws -> (URL, PendingImage) {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".txt")
        try Data(repeating: 65, count: 8192).write(to: url)
        let img = PendingImage(id: "picked", uri: nil)
        img.file = try PickedFile(url: url)
        img.state = .failed(error: "needs upload")
        return (url, img)
    }
    private func driver(_ server: FileMachine, _ draft: ImageDraft) -> ImageUploads {
        let u = ImageUploads(prepare: { _, _ in throw UserError("unexpected image preparation") }, channel: { FileMachineChannel(server: server, machine: $0) })
        u.onFileChanged = { try await draft.persistFile($0) }
        return u
    }
    private func until(_ condition: @MainActor () async -> Bool) async {
        for _ in 0..<300 {
            if await condition() { return }
            try? await Task.sleep(for: .milliseconds(5))
        }
        Issue.record("timed out waiting for file transfer")
    }

    @Test func completedTransfersSurviveRelaunchAndSwitchingBack() async throws {
        let (url, img) = try picked(); defer { try? FileManager.default.removeItem(at: url); img.release() }
        let disk = MemoryFiles(), server = FileMachine()
        let draft = ImageDraft(key: "draft", files: disk, saves: nil); draft.append([img])
        await server.configure(validation: { machine, transfer in
            let data = try disk.get(folder: "draft", name: "picked.file")!
            return try JSONDecoder().decode(PickedFile.self, from: data).transfers[machine]?.transferId == transfer
        })
        let u = driver(server, draft)
        u.upload(img, machineId: "a"); await img.job?.value
        let first = img.transferId
        #expect(img.readyFor("a"))
        u.upload(img, machineId: "b"); await img.job?.value
        u.upload(img, machineId: "a"); await img.job?.value
        #expect(img.transferId == first && img.readyFor("a"))
        let restored = ImageDraft(key: "draft", files: disk, saves: nil); try restored.restore()
        let revived = try #require(restored.documents.first)
        defer { revived.release() }
        #expect(revived.transferId == first && !revived.readyFor("a"))
        let next = driver(server, restored)
        next.upload(revived, machineId: "a"); await revived.job?.value
        #expect(revived.readyFor("a") && revived.transferId == first)
        #expect(await server.transfers.count == 2)
        #expect(await server.begins.filter { $0.0 == "a" }.allSatisfy { $0.1 == first })
        await next.discard(revived).value
        #expect(await server.transfers.isEmpty)
        #expect(await server.aborted.count == 2)
    }

    @Test func aRelaunchedPartialUploadUsesItsOriginalReservation() async throws {
        let (url, img) = try picked(); defer { try? FileManager.default.removeItem(at: url) }
        let disk = MemoryFiles(), server = FileMachine()
        let draft = ImageDraft(key: "partial", files: disk, saves: nil); draft.append([img])
        #expect(img.startUpload(to: "a"))
        let id = img.transferId
        try await draft.persistFile(img)
        await server.seedPartial("a", id, bytes: Data(repeating: 65, count: 2048), total: 8192)
        let restored = ImageDraft(key: "partial", files: disk, saves: nil); try restored.restore()
        let revived = try #require(restored.documents.first); defer { revived.release() }
        let u = driver(server, restored); u.upload(revived, machineId: "a"); await revived.job?.value
        #expect(revived.readyFor("a") && revived.transferId == id)
        #expect(await server.transfers["a" + id]?.data.count == 8192)
    }

    @Test func changedSameSizeSourceAbortsOldReservationBeforeRetry() async throws {
        let (url, img) = try picked(); defer { try? FileManager.default.removeItem(at: url); img.release() }
        let disk = MemoryFiles(), server = FileMachine()
        let draft = ImageDraft(key: "changed", files: disk, saves: nil); draft.append([img])
        await server.configure(reject: true)
        let u = driver(server, draft); u.upload(img, machineId: "a"); await img.job?.value
        let begins = await server.begins, aborted = await server.aborted
        #expect(begins.count == 2 && aborted.count == 1)
        #expect(begins[0].1 == aborted[0].1 && begins[1].1 != begins[0].1)
        #expect(img.readyFor("a"))
        #expect(fileUploadError(UploadError(code: "invalid_argument", message: "hash mismatch")).contains("校验失败"))
    }

    @Test func globalFileQueueDoesNotHashOrBeginASecondFileWhileOneIsUploading() async throws {
        let (url, img) = try picked(); defer { try? FileManager.default.removeItem(at: url) }
        let server = FileMachine(); await server.configure(hold: true)
        let source = try #require(img.file)
        let first = Task { try await uploadFile(FileMachineChannel(server: server, machine: "a"), transferId: "first", source: source) }
        await until { await server.waiting != nil }
        var secondHashed = false
        let second = Task { try await uploadFile(FileMachineChannel(server: server, machine: "b"), transferId: "second", source: source, onSource: { _ in secondHashed = true }) }
        try await Task.sleep(for: .milliseconds(40))
        #expect(await server.begins.count == 1 && !secondHashed)
        second.cancel()
        do { _ = try await second.value; Issue.record("queued file ignored cancellation") } catch { #expect(error is CancellationError) }
        await server.release()
        _ = try await first.value
    }

    @Test func allPersistedBytesStillNeedCompleteAfterAnUnknownReply() async throws {
        let (url, img) = try picked(); defer { try? FileManager.default.removeItem(at: url) }
        let server = FileMachine()
        await server.configure(loseComplete: true)
        await server.seedPartial("a", "all-bytes", bytes: Data(repeating: 65, count: 8192), total: 8192)
        let source = try #require(img.file)
        let uploaded = try await uploadFile(FileMachineChannel(server: server, machine: "a"), transferId: "all-bytes", source: source)
        #expect(uploaded.imageId == "file-all-bytes")
        #expect(await server.completes == 2)
    }

    @Test func cancelingTheCallerStopsTheDetachedWorker() async throws {
        let progress = WorkerProgress()
        let work = Task {
            try await fileWork {
                for _ in 0..<2000 { try Task.checkCancellation(); progress.tick(); Thread.sleep(forTimeInterval: 0.001) }
                return 1
            }
        }
        await until { progress.count > 0 }
        work.cancel()
        do { _ = try await work.value; Issue.record("worker ignored cancellation") } catch { #expect(error is CancellationError) }
        #expect(progress.count < 2000)
        let stopped = progress.count
        try await Task.sleep(for: .milliseconds(20))
        #expect(progress.count == stopped)
    }

    @Test func staleBookmarkIsRenewedAndPersistedWithTransferState() async throws {
        let (url, img) = try picked(); defer { try? FileManager.default.removeItem(at: url) }
        img.file?.bookmark = Data([0])
        img.file?.transfers["a"] = FileTransfer(transferId: "resume-me")
        let renewed = try #require(img.file).refreshed(resolve: { _ in (url, true) })
        #expect(renewed.bookmark != Data([0]) && renewed.transfers["a"]?.transferId == "resume-me")
        let disk = MemoryFiles(), draft = ImageDraft(key: "bookmark", files: MemoryFiles(), saves: nil)
        let saved = ImageDraft(key: "saved", files: disk, saves: nil)
        img.file = renewed; saved.append([img]); try await saved.persistFile(img)
        let restored = ImageDraft(key: "saved", files: disk, saves: nil); try restored.restore()
        #expect(restored.documents.first?.file?.bookmark == renewed.bookmark)
        #expect(try restored.documents.first?.file?.digest().count == 32)
        draft.restoring = true
        #expect(draft.attach([], max: 6), "text-only restore does not wait for attachment loading")
        let remote = PendingImage(id: "remote", uri: nil)
        remote.file = PickedFile(remote: .with { $0.name = "remote.txt"; $0.sizeBytes = 1 })
        #expect(!remote.hasSource)
        let emptyDisk = MemoryFiles(), placeholders = ImageDraft(key: "p", files: emptyDisk, saves: nil)
        #expect(placeholders.attach([remote], max: 6))
        #expect(placeholders.toSend(machineId: "a").isEmpty)
        #expect(!emptyDisk.keys.contains { $0.hasSuffix(".file") })
    }

    @Test func foldersAndChangedLengthsHaveSpecificMessagesAndChatHidesOnlyCurrentForms() throws {
        let (url, img) = try picked(); defer { try? FileManager.default.removeItem(at: url) }
        do { _ = try PickedFile(url: url.deletingLastPathComponent()); Issue.record("folder accepted") }
        catch { #expect(errorText(error) == "文件夹不能直接发送，请压缩后选择") }
        try Data([1]).write(to: url)
        do { _ = try img.file?.digest(); Issue.record("changed length accepted") }
        catch { #expect(errorText(error) == "文件在选择后被修改，请重新选择") }
        let file = PBSentFile.with { $0.name = "report.txt"; $0.path = "/uploads/session/files/id/report.txt" }
        let ordinary = "正文里提到 \(file.path)"
        let legacy = "File: @\(file.path) (file)"
        let event = PBEvent.with {
            $0.seq = 1
            $0.userMessage.text = ordinary + "\n\nFile: @\"\(file.path)\" (file)\nFile: \(file.path)\n" + legacy
            $0.userMessage.files = [file]
        }
        let chat = buildChat([event], d: Derived(), machine: "Test computer")
        guard case .user(let message) = chat.first else { Issue.record("missing user message"); return }
        #expect(message.text == ordinary + "\n" + legacy)
        #expect(message.files == [file])
    }
}
