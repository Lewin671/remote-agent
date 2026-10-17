import Foundation
import RAProto
import Testing

@testable import RACore

/// Sealed files in memory, as the pending store keeps them.
final class MemoryFiles: SealedFiles, @unchecked Sendable {
    private let lock = NSLock()
    private var data = [String: Data]()
    /// Set to have every write fail, as a full disk would.
    var full = false
    /// How many more writes there is room for; nil for any number.
    var room: Int?
    /// Every write that went in, in order.
    private(set) var puts = [(key: String, data: Data)]()

    struct Full: Error, LocalizedError {
        var errorDescription: String? { "磁盘已满" }
    }

    var keys: [String] { lock.withLock { Array(data.keys) } }

    func put(folder: String, name: String, data: Data) throws {
        try lock.withLock {
            if full || room == 0 { throw Full() }
            room = room.map { $0 - 1 }
            self.data["\(folder)/\(name)"] = data
            puts.append(("\(folder)/\(name)", data))
        }
    }

    func get(folder: String, name: String) throws -> Data? { lock.withLock { data["\(folder)/\(name)"] } }

    func remove(folder: String, name: String) throws { lock.withLock { data["\(folder)/\(name)"] = nil } }

    func clearFolder(_ folder: String) throws {
        lock.withLock { for k in data.keys where k.hasPrefix("\(folder)/") { data[k] = nil } }
    }
}

private final class FailureCount: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    func add() { lock.withLock { n += 1 } }
    var count: Int { lock.withLock { n } }
}

/// Android's app AttachmentsTest.
@MainActor @Suite struct AttachmentsTests {
    private func img(_ id: String) -> PendingImage {
        let i = PendingImage(id: id, uri: "content://picker/\(id)")
        i.processed = Processed(bytes: Data([1, 2, UInt8(id.count)]), format: .jpeg, width: 10, height: 20)
        i.state = .ready
        i.machineId = "mac"
        i.imageId = "img-\(id)"
        i.expiresMs = Int64.max
        return i
    }

    private func draft(_ key: String, _ files: MemoryFiles) -> ImageDraft { ImageDraft(key: key, files: files, saves: nil) }

    @Test func aRestoredDraftKeepsThePickOrder() throws {
        let files = MemoryFiles()
        // Random-looking IDs in an order no listing would give back.
        let picked = [img("zz"), img("aa"), img("mm")]
        try saveImages(files, folder: "session:s", images: picked.map { StoredImage(id: $0.id, uri: $0.uri, processed: $0.processed) }, original: true)
        try saveOrder(files, folder: "session:s", ids: picked.map(\.id), original: true)
        let d = draft("session:s", files)
        try d.restore()
        #expect(d.images.map(\.id) == ["zz", "aa", "mm"])
        #expect(d.original)
    }

    @Test func messagesSavedAtTheSameTimeAreAllInTheIndex() throws {
        let files = MemoryFiles()
        let records = (0..<24).map { Outgoing(requestId: "r\($0)", text: "t", imageIds: [], images: [], original: false).record }
        let failed = FailureCount()
        // Saves and removals run off the main thread, each on its own.
        DispatchQueue.concurrentPerform(iterations: records.count) { i in
            do { try OutgoingRecords(files: files).save(sid: "s", records[i], original: false) } catch { failed.add() }
        }
        #expect(failed.count == 0)
        #expect(Set(try OutgoingRecords(files: files).load(sid: "s").map(\.requestId)) == Set(records.map(\.requestId)))
        DispatchQueue.concurrentPerform(iterations: records.count) { i in
            guard i % 2 == 0 else { return }
            do { try OutgoingRecords(files: files).remove(sid: "s", requestId: "r\(i)") } catch { failed.add() }
        }
        #expect(failed.count == 0)
        #expect(Set(try OutgoingRecords(files: files).load(sid: "s").map(\.requestId)) == Set((0..<24).filter { $0 % 2 == 1 }.map { "r\($0)" }))
    }

    @Test func aSentMessagesImagesStayOnDiskUntilTheCliHasThem() throws {
        let files = MemoryFiles()
        let o = Outgoing(requestId: "r1", text: "看这两张", imageIds: ["img-a", "img-b"], images: [img("a"), img("b")], original: false)
        try OutgoingRecords(files: files).save(sid: "s", o.record, original: false)
        // The process is gone; a new run finds it, images in order, outcome unknown.
        let loaded = try OutgoingRecords(files: files).load(sid: "s")
        let back = try #require(loaded.count == 1 ? loaded[0] : nil)
        #expect(back.requestId == "r1")
        #expect(back.text == "看这两张")
        #expect(back.imageIds == ["img-a", "img-b"])
        #expect(back.images.map(\.id) == ["a", "b"])
        #expect(back.state == .unknown)
        try OutgoingRecords(files: files).remove(sid: "s", requestId: "r1")
        #expect(try OutgoingRecords(files: files).load(sid: "s").isEmpty)
        #expect(!files.keys.contains { $0.hasPrefix("out:s:r1/") })
    }

    @Test func submittedAndCanceledMessagesAreLetGo() {
        let a = Outgoing(requestId: "a", text: "x", images: [img("1")])
        let b = Outgoing(requestId: "b", text: "y", images: [img("2")])
        let c = Outgoing(requestId: "c", text: "z")
        func state(_ status: PBInputStatus, _ reason: String = "") -> PBInputState {
            .with {
                $0.status = status
                $0.reason = reason
            }
        }
        let done = recorded([a, b, c], inputs: ["a": state(.submitted), "b": state(.canceled)])
        #expect(done.count == 2 && done[0] === a && done[1] === b)
        for status: PBInputStatus in [.queued, .heldByCli, .failed, .needsTerminal, .unconfirmed] {
            #expect(recorded([c], inputs: ["c": state(status)]).isEmpty)
        }
        // The session ended on it: the chat still offers to put it back, images included.
        #expect(recorded([b], inputs: ["b": state(.canceled, "exited")]).isEmpty)
    }

    @Test func sendingTakesOnlyItsImagesAndUntickesOriginal() {
        let d = draft("new:/p", MemoryFiles())
        let sent = [img("a"), img("b")]
        d.attach(sent, max: 6, wereOriginal: true)
        #expect(d.original)
        let later = img("c")
        d.images.append(later)
        d.detach(sent)
        #expect(d.images.map(\.id) == ["c"])
        // Still one image: 原图 stays for it; once the tray is empty it goes back to unticked.
        d.detach([later])
        #expect(!d.original)
    }

    @Test func puttingBackNeverGoesPastTheLimitNorDropsImages() {
        let d = draft("session:s", MemoryFiles())
        d.attach((1...4).map { img("have\($0)") }, max: 6)
        let back = (1...3).map { img("back\($0)") }
        #expect(!d.attach(back, max: 6))
        #expect(d.images.count == 4)
        #expect(d.notice.contains("超过 6 张"))
        #expect(d.attach(Array(back.prefix(2)), max: 6))
        #expect(d.images.count == 6)
    }

    @Test func anUploadToAnotherMachineIsReplacedByTheLatestTarget() {
        let i = img("a")
        i.state = .uploading(progress: 0.5)
        i.machineId = "old"
        #expect(needsUpload(i, machineId: "new"))
        #expect(!needsUpload(i, machineId: "old"))
        i.state = .ready
        i.machineId = "new"
        #expect(!needsUpload(i, machineId: "new"))
    }

    @Test func theOrderFileAlwaysListsTheWholeSelection() throws {
        let files = MemoryFiles()
        let d = draft("session:s", files)
        let ready = img("a")
        let preparing = PendingImage(id: "b", uri: "content://picker/b")
        d.attach([ready], max: 6)
        d.images.append(preparing)
        // A save that runs while b is still being prepared keeps b in the selection.
        d.detach([])
        let order = String(decoding: try #require(try files.get(folder: "session:s", name: "order")), as: UTF8.self).split(separator: "\n").map(String.init)
        #expect(Array(order.dropFirst()) == ["a", "b"])
    }

    @Test func everyOrderWrittenListsTheImagesAlreadyKept() throws {
        let files = MemoryFiles()
        let d = draft("session:s", files)
        d.attach([img("a")], max: 6)
        // A second image is prepared: only it is written, and the order still names the first at every moment.
        d.images.append(img("b"))
        d.persist()
        let orders = files.puts.filter { $0.key == "session:s/order" }.map { String(decoding: $0.data, as: UTF8.self) }
        #expect(orders == ["0\na", "0\na\nb"])
        #expect(files.puts.filter { $0.key == "session:s/a.img" }.count == 1)
    }

    @Test func anImageRemovedBeforeItsSaveRanLeavesNoFiles() throws {
        let files = MemoryFiles()
        let saves = DispatchQueue(label: "test.image-draft")
        let d = ImageDraft(key: "session:s", files: files, saves: saves)
        // The save waits its turn; the image is removed meanwhile.
        saves.suspend()
        let a = img("a")
        d.attach([a, img("b")], max: 6)
        d.remove(a)
        saves.resume()
        saves.sync {}
        #expect(Set(files.keys) == ["session:s/b.img", "session:s/b.meta", "session:s/order"])
        #expect(String(decoding: try #require(try files.get(folder: "session:s", name: "order")), as: UTF8.self) == "0\nb")
        // The same for a draft dropped whole.
        saves.suspend()
        d.attach([img("c")], max: 6)
        d.clear()
        saves.resume()
        saves.sync {}
        #expect(files.keys.isEmpty)
    }

    @Test func aMessageThatCouldNotBeKeptWholeLeavesNothingBehind() throws {
        let files = MemoryFiles()
        let o = Outgoing(requestId: "r1", text: "看这两张", imageIds: ["img-a", "img-b"], images: [img("a"), img("b")])
        // Room for the first image and half of the second, as near the store's limit.
        files.room = 3
        #expect(throws: MemoryFiles.Full.self) { try OutgoingRecords(files: files).save(sid: "s", o.record, original: false) }
        #expect(files.keys.isEmpty)
        // Everything but the index went in: the same.
        files.room = 8
        #expect(throws: MemoryFiles.Full.self) { try OutgoingRecords(files: files).save(sid: "s", o.record, original: false) }
        #expect(files.keys.isEmpty)
        files.room = nil
        try OutgoingRecords(files: files).save(sid: "s", o.record, original: false)
        #expect(try OutgoingRecords(files: files).load(sid: "s").map(\.requestId) == ["r1"])
    }

    @Test func whatWaitsForImagesToBeKeptHearsOnceTheyAreOnDiskOrGone() throws {
        let files = MemoryFiles()
        let d = draft("session:s", files)
        var heard: [String] = []
        // No room: the images are in the draft but not on disk, so their copy elsewhere must stay.
        files.full = true
        let (a, b) = (img("a"), img("b"))
        d.attach([a, b], max: 6)
        d.whenKept(["a", "b"]) { heard.append("both") }
        d.whenKept(["a"]) { heard.append("a") }
        #expect(heard.isEmpty)
        // Removed: nothing of it is left to keep.
        d.remove(a)
        #expect(heard == ["a"])
        files.full = false
        d.persist()
        #expect(heard == ["a", "both"])
        #expect(try files.get(folder: "session:s", name: "b.img") != nil)
        // Already there: said at once.
        d.whenKept(["b"]) { heard.append("again") }
        #expect(heard == ["a", "both", "again"])
    }

    // --- not in Android's tests ---

    @Test func whatIsPutBackGoesToTheComputerAgainUnderANewTransfer() {
        let d = draft("session:s", MemoryFiles())
        let i = img("a")
        d.attach([i], max: 6)
        // Put back: the daemon may have used the old upload up.
        #expect(i.state == .failed(error: "需要重新传到电脑"))
        #expect(i.imageId == nil && i.machineId == nil)
        #expect(!d.ready(machineId: "mac"))
        #expect(d.toSend(machineId: "mac").map(\.id) == ["a"])
        let transfer = i.transferId
        #expect(i.startUpload(to: "mac"))
        #expect(i.state == .uploading(progress: 0) && i.machineId == "mac")
        #expect(i.transferId != transfer)
        // On its way there: nothing to start again. Another machine takes over with a transfer of its own.
        #expect(!i.startUpload(to: "mac"))
        let second = i.transferId
        #expect(i.startUpload(to: "vps"))
        #expect(i.transferId != second && i.machineId == "vps")
        i.imageId = "img-9"
        i.expiresMs = Int64.max
        i.state = .ready
        #expect(d.ready(machineId: "vps") && d.ids(machineId: "vps") == ["img-9"])
        #expect(d.totalBytes == 3)
        // More than fit: the first ones in the order picked, and the box says how many were left.
        #expect(d.admit(["p", "q", "r"], max: 3) == ["p", "q"])
        #expect(d.notice == "一条消息最多 3 张，多选的 1 张没有加入")
        #expect(d.setOriginal(true) && !d.setOriginal(true))
    }

    @Test func aDraftThatCannotBeKeptSaysSoAndTriesAgainNextTime() throws {
        let files = MemoryFiles()
        let d = draft("session:s", files)
        files.full = true
        d.attach([img("a")], max: 6)
        #expect(d.notice == "这张图未保存到手机：磁盘已满。当前仍可发送，关闭应用后可能丢失")
        #expect(files.keys.isEmpty)
        // Room again: the next save writes the image it could not write before.
        files.full = false
        d.persist()
        #expect(Set(files.keys) == ["session:s/a.img", "session:s/a.meta", "session:s/order"])
        // An image removed takes its files with it; a cleared draft leaves nothing.
        d.remove(d.images[0])
        #expect(files.keys == ["session:s/order"])
        d.clear()
        #expect(files.keys.isEmpty)
    }

    @Test func aRecordThatDoesNotReadIsLeftOutAndTheRestRestored() throws {
        let files = MemoryFiles()
        try saveImages(files, folder: "f", images: [img("a"), img("b")].map { StoredImage(id: $0.id, uri: nil, processed: $0.processed) }, original: false)
        try saveOrder(files, folder: "f", ids: ["a", "b"], original: false)
        try files.put(folder: "f", name: "a.meta", data: Data("not a record".utf8))
        let (got, original) = try loadStored(files, folder: "f")
        #expect(got.map(\.id) == ["b"] && !original)
        #expect(got[0].uri == nil && got[0].processed?.format == .jpeg && got[0].processed?.width == 10)
        #expect(try loadStored(files, folder: "none").images.isEmpty)
        // A message's mode is kept under the name Android gives it.
        let o = Outgoing(requestId: "r", text: "t", mode: .queue)
        try OutgoingRecords(files: files).save(sid: "s", o.record, original: false)
        #expect(String(decoding: try #require(try files.get(folder: "out:s:r", name: "mode")), as: UTF8.self) == "SEND_MODE_QUEUE")
        #expect(try OutgoingRecords(files: files).load(sid: "s").first?.mode == .queue)
        #expect(try OutgoingRecords(files: files).load(sid: "other").isEmpty)
    }

    @Test func aMachineSaysWhetherAndHowManyImagesItTakes() {
        func m(online: Bool = true, _ caps: PBMachineCapabilities?) -> MachineView {
            MachineView(id: "m", online: online, lastSeenMs: 0, meta: .with { if let caps { $0.capabilities = caps } })
        }
        let ok = PBMachineCapabilities.with { $0.imageUpload = true; $0.maxImagesPerMessage = 4 }
        #expect(uploadBlocked(nil, online: true, name: "mac") == "先选一台电脑")
        #expect(uploadBlocked(m(nil), online: true, name: "mac") == "还没读到 mac 的信息，稍后才能添加图片")
        #expect(uploadBlocked(m(PBMachineCapabilities()), online: true, name: "mac") == "mac 上 remote-agent 的目录路径带空格，不能发图片")
        #expect(uploadBlocked(m(online: false, ok), online: true, name: "mac") == "mac 离线，连上后才能添加图片")
        #expect(uploadBlocked(m(ok), online: false, name: "mac") == "mac 离线，连上后才能添加图片")
        #expect(uploadBlocked(m(ok), online: true, name: "mac") == nil)
        #expect(imageLimit(for: m(ok)) == 4)
        #expect(imageLimit(for: m(.with { $0.maxImagesPerMessage = 9 })) == 6)
        #expect(imageLimit(for: m(PBMachineCapabilities())) == 6)
        #expect(imageLimit(for: nil) == 6)
    }
}

/// From Android's app SendImagesTest: the case that needs no connection.
@Suite struct SendImagesTests {
    @Test func theLatestStateOfEachMessageComesFromTheEvents() {
        let m = inputStates([Mk.ev(1, Mk.inputState("a", .queued)), Mk.ev(2, Mk.inputState("a", .submitted)), Mk.ev(3, Mk.inputState("b", .queued))])
        #expect(m["a"]?.status == .submitted)
        #expect(m["b"]?.status == .queued)
    }
}
