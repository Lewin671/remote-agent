import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// Messages with images, from the box to the CLI: Android's SendImagesTest and the image half of `Outbox`.
@MainActor
@Suite struct SessionImagesTests {
    private let remote = try! Remote(
        account: Account(server: "http://127.0.0.1:1", root: Data(count: 32), deviceSeed: Data(count: 32), deviceId: "00112233445566778899aabbccddeeff"),
        http: AppModel.http)
    private let files = VolatileFiles()
    private let store = SessionStore()
    private let computer = FakeCommands()
    private let sid = "s1"

    private func center() -> ImageCenter {
        ImageCenter.install(remote: remote, files: files)
        return ImageCenter.of(remote)
    }

    /// An image as it stands in a draft once it is on the computer.
    private func ready(_ id: String) -> PendingImage {
        let img = PendingImage(id: id, uri: "picked:\(id)")
        img.processed = Processed(bytes: Data([0x89]) + Data("PNG\r\n\u{1A}\n".utf8) + Data(id.utf8), format: .png, width: 4, height: 3)
        img.state = .ready
        img.machineId = "mac"
        img.imageId = "on-mac-\(id)"
        img.expiresMs = Int64.max
        return img
    }

    private func send(_ c: ImageCenter, _ text: String, keep: (@Sendable (String, OutgoingRecord, Bool) throws -> Void)? = nil) async -> Result<Void, Error>? {
        await withCheckedContinuation { done in
            let taken = c.send(sid: sid, machineId: "mac", store: store, text: text, mode: .steer, commands: computer, keep: keep) {
                done.resume(returning: $0)
            }
            if !taken { done.resume(returning: nil) }
        }
    }

    /// Waits for what runs after `done`, on other tasks.
    private func eventually(_ what: String, _ ok: @MainActor () -> Bool) async {
        for _ in 0..<500 where !ok() { try? await Task.sleep(nanoseconds: 2_000_000) }
        #expect(ok(), "\(what)")
    }

    @Test func aDraftWithoutImagesIsNotThisCodesToSend() async {
        let c = center()
        #expect(await send(c, "只有文字") == nil)
        #expect(store.outgoing(sid).isEmpty && computer.sent.isEmpty)
    }

    @Test func aMessageWhoseImagesCouldNotBeKeptIsNotSent() async {
        struct DiskFull: Error {}
        let c = center()
        let draft = c.draft(ImageCenter.sessionKey(sid))
        draft.append([ready("a")])
        let kept = await send(c, "看图") { _, _, _ in throw DiskFull() }
        guard case .failure(let error)? = kept else {
            Issue.record("keeping the images should have failed")
            return
        }
        #expect(error is DiskFull)
        // Nothing went out, nothing waits here as if it had, and the draft is as it was.
        #expect(store.outgoing(sid).isEmpty && computer.sent.isEmpty)
        #expect(draft.images.map(\.id) == ["a"])
    }

    /// Logging out does not wait for images that are being written; the message they were for is not sent after it.
    @Test func aMessageStillBeingKeptWhenTheAccountIsLeftIsNotSent() async {
        final class Gate: @unchecked Sendable {
            private let lock = NSLock()
            private var reached = false
            let open = DispatchSemaphore(value: 0)
            var entered: Bool {
                get { lock.withLock { reached } }
                set { lock.withLock { reached = newValue } }
            }
        }
        let gate = Gate()
        let c = center()
        let draft = c.draft(ImageCenter.sessionKey(sid))
        draft.append([ready("a")])
        async let kept = send(c, "看图") { _, _, _ in
            gate.entered = true
            gate.open.wait()
        }
        await eventually("the images are being written") { gate.entered }
        ImageCenter.remove(remote)
        gate.open.signal()
        guard case .failure(let error)? = await kept else {
            Issue.record("the message was taken as sent")
            return
        }
        #expect(errorText(error) == "已退出账户")
        try? await Task.sleep(nanoseconds: 50_000_000)
        #expect(computer.sent.isEmpty && store.outgoing(sid).isEmpty)
        #expect(remote.events(sid).followers == 0)
    }

    @Test func aSecondSendWhileTheImagesAreBeingKeptSendsNothing() async throws {
        let c = center()
        let draft = c.draft(ImageCenter.sessionKey(sid))
        draft.append([ready("a")])
        // The message box as it is drawn, and as it is drawn again after a look at the terminal: each is
        // a new view, and what they share is the session's store.
        func box() -> SessionImageContext {
            SessionImageContext(sessionId: sid, machineId: "mac", remote: remote, store: store, blocked: nil, limit: 4)
        }
        let disk = DispatchSemaphore(value: 0)
        var outcomes = 0
        #expect(SessionImages.send(box(), text: "看图", mode: .steer, commands: computer, keep: { _, _, _ in disk.wait() }) { _ in outcomes += 1 })
        #expect(store.isKeeping(sid))
        // Taken over, and nothing done: the first message has these images.
        #expect(SessionImages.send(box(), text: "看图", mode: .steer, commands: computer, keep: { _, _, _ in }) { _ in outcomes += 1 })
        try await Task.sleep(nanoseconds: 50_000_000)
        #expect(store.outgoing(sid).isEmpty && outcomes == 0)
        disk.signal()
        await eventually("the first message is delivered") { store.outgoing(sid).first?.state == .sent }
        #expect(!store.isKeeping(sid) && outcomes == 1)
        #expect(store.outgoing(sid).count == 1 && computer.sent.count == 1 && draft.images.isEmpty)
        // The lock went with the message: the next one is sent.
        draft.append([ready("b")])
        #expect(SessionImages.send(box(), text: "再看", mode: .steer, commands: computer, keep: { _, _, _ in }) { _ in outcomes += 1 })
        await eventually("the second message is delivered") { computer.sent.count == 2 }
    }

    @Test func aMessageGoesWithItsImagesOnceTheyAreKept() async throws {
        let c = center()
        let draft = c.draft(ImageCenter.sessionKey(sid))
        draft.append([ready("a"), ready("b")])
        let kept = await send(c, "这两个页面底部的按钮都点不到")
        guard case .success? = kept else {
            Issue.record("the images should have been kept")
            return
        }
        // Out of the box, into the message, and sealed for a run that may end before the CLI has it.
        #expect(draft.images.isEmpty)
        let o = try #require(store.outgoing(sid).first)
        #expect(o.imageIds == ["on-mac-a", "on-mac-b"] && o.images.map(\.id) == ["a", "b"])
        #expect(try OutgoingRecords(files: files).load(sid: sid).map(\.requestId) == [o.requestId])
        await eventually("the message is delivered") { o.state == .sent }
        let sent = try #require(computer.sent.first?.send)
        #expect(sent.imageIds == ["on-mac-a", "on-mac-b"] && sent.requestID == o.requestId && sent.text == "这两个页面底部的按钮都点不到")
    }

    @Test(arguments: [PBInputStatus.submitted, .canceled])
    func itIsLetGoOfOnceSubmittedOrCanceled(_ status: PBInputStatus) async throws {
        let c = center()
        c.draft(ImageCenter.sessionKey(sid)).append([ready("a")])
        computer.answer(CommandError(CommandError.unknownOutcome, "不确定电脑有没有收到"))
        _ = await send(c, "看图")
        let o = try #require(store.outgoing(sid).first)
        await eventually("answer lost") { o.state == .unknown }
        func input(_ seq: UInt64, _ status: PBInputStatus) -> PBEvent {
            .with {
                $0.seq = seq
                $0.inputState = .with {
                    $0.requestID = o.requestId
                    $0.status = status
                }
            }
        }
        // Queued is not yet recorded: the images stay.
        await remote.showForTest(state: RemoteState(), events: [sid: [input(1, .queued)]])
        try await Task.sleep(nanoseconds: 50_000_000)
        #expect(store.find(sid, requestId: o.requestId) != nil && o.images[0].processed != nil)
        await remote.showForTest(state: RemoteState(), events: [sid: [input(1, .queued), input(2, status)]])
        await eventually("settled") { store.find(sid, requestId: o.requestId) == nil }
        #expect(o.images[0].processed == nil)
        await eventually("history released") { remote.events(sid).followers == 0 }
        await eventually("its record is gone") { (try? OutgoingRecords(files: files).load(sid: sid).isEmpty) == true }
    }

    @Test func aMessageTheSessionEndedOnKeepsItsImagesToBePutBack() async throws {
        let c = center()
        let draft = c.draft(ImageCenter.sessionKey(sid))
        draft.append([ready("a")])
        _ = await send(c, "看图")
        let o = try #require(store.outgoing(sid).first)
        await eventually("delivered") { o.state == .sent }
        func input(_ seq: UInt64, _ status: PBInputStatus, _ reason: String = "") -> PBEvent {
            .with {
                $0.seq = seq
                $0.inputState = .with {
                    $0.requestID = o.requestId
                    $0.status = status
                    $0.reason = reason
                }
            }
        }
        // The session ended while it waited: the chat offers to put it back.
        let events = [input(1, .queued), input(2, .canceled, "exited")]
        await remote.showForTest(state: RemoteState(), events: [sid: events])
        store.settle(sid, inputs: derive(events).inputDetails)
        try await Task.sleep(nanoseconds: 200_000_000)
        #expect(store.find(sid, requestId: o.requestId) === o && o.images[0].processed != nil)
        #expect(try OutgoingRecords(files: files).load(sid: sid).map(\.requestId) == [o.requestId])

        #expect(SessionImages.putBack(o, session: sid, state: RemoteState(), remote: remote, store: store))
        #expect(store.draft(sid) == "看图" && store.outgoing(sid).isEmpty)
        #expect(draft.images.map(\.id) == ["a"] && draft.images[0].processed != nil)
    }

    @Test func aMessageNotSubmittedGoesBackIntoTheBoxWithItsImages() async throws {
        let c = center()
        let draft = c.draft(ImageCenter.sessionKey(sid))
        draft.append([ready("a"), ready("b")])
        computer.answer(CommandError(CommandError.unavailable, "电脑不在线，指令没有发出"))
        _ = await send(c, "看图")
        let o = try #require(store.outgoing(sid).first)
        await eventually("failed") { o.state == .failed }
        store.setDraft(sid, "已经在写的")

        #expect(SessionImages.putBack(o, session: sid, state: RemoteState(), remote: remote, store: store))
        #expect(store.outgoing(sid).isEmpty)
        #expect(store.draft(sid) == "已经在写的\n看图")
        // The images are in the box again and go to the computer again; nothing of them was dropped.
        #expect(draft.images.map(\.id) == ["a", "b"])
        #expect(draft.images.allSatisfy { $0.state == .failed(error: "需要重新传到电脑") && $0.processed != nil })
        await eventually("its record is gone") { (try? OutgoingRecords(files: files).load(sid: sid).isEmpty) == true }
    }

    @Test func aMessagePutBackKeepsItsRecordUntilTheBoxHasItsImagesOnDisk() async throws {
        /// A store with no room for one folder while `full` names it.
        final class Cramped: RACore.SealedFiles, @unchecked Sendable {
            let inner = VolatileFiles()
            private let lock = NSLock()
            private var refused: String?
            var full: String? {
                get { lock.withLock { refused } }
                set { lock.withLock { refused = newValue } }
            }

            func put(folder: String, name: String, data: Data) throws {
                if folder == full { throw UserError("手机待发图片空间不足，请发送或移除旧草稿") }
                inner.put(folder: folder, name: name, data: data)
            }
            func get(folder: String, name: String) -> Data? { inner.get(folder: folder, name: name) }
            func remove(folder: String, name: String) { inner.remove(folder: folder, name: name) }
            func clearFolder(_ folder: String) { inner.clearFolder(folder) }
        }
        let files = Cramped()
        ImageCenter.install(remote: remote, files: files)
        let c = ImageCenter.of(remote)
        let key = ImageCenter.sessionKey(sid)
        let draft = c.draft(key)
        draft.append([ready("a")])
        computer.answer(CommandError(CommandError.unavailable, "电脑不在线，指令没有发出"))
        _ = await send(c, "看图")
        let o = try #require(store.outgoing(sid).first)
        await eventually("failed") { o.state == .failed }

        // The box cannot keep the image it gets back: the message's record is the only copy on disk.
        files.full = key
        #expect(SessionImages.putBack(o, session: sid, state: RemoteState(), remote: remote, store: store))
        await eventually("the box said so") { draft.notice.hasPrefix("这张图未保存到手机") }
        try await Task.sleep(nanoseconds: 50_000_000)
        #expect(try OutgoingRecords(files: files).load(sid: sid).map(\.requestId) == [o.requestId])
        #expect(files.get(folder: key, name: "a.img") == nil)
        // Room again: once the box has saved it the record goes.
        files.full = nil
        draft.persist()
        await eventually("its record is gone") { (try? OutgoingRecords(files: files).load(sid: sid).isEmpty) == true }
        #expect(files.get(folder: key, name: "a.img") != nil)
    }

    @Test func imagesThatDoNotFitNextToTheOnesInTheBoxStayWithTheirMessage() async throws {
        let c = center()
        let draft = c.draft(ImageCenter.sessionKey(sid))
        draft.append((1...4).map { ready("m\($0)") })
        computer.answer(CommandError(CommandError.unavailable, "电脑不在线，指令没有发出"))
        _ = await send(c, "四张")
        let o = try #require(store.outgoing(sid).first)
        await eventually("failed") { o.state == .failed }
        draft.append((1...3).map { ready("n\($0)") })

        #expect(!SessionImages.putBack(o, session: sid, state: RemoteState(), remote: remote, store: store))
        // Nothing moved: the message is still there to retry or to put back later.
        #expect(store.find(sid, requestId: o.requestId) != nil && store.draft(sid).isEmpty)
        #expect(draft.images.count == 3)
        #expect(draft.notice == "输入框里已有 3 张图，放回这 4 张会超过 6 张。先移除一些再放回")
        #expect(try OutgoingRecords(files: files).load(sid: sid).count == 1)
    }

    /// A session that ended, or whose computer is offline, sends no more events: its history is not kept
    /// followed waiting for one after the last message with images is put back or removed.
    @Test func aSessionIsFollowedOnlyWhileAMessageOfItWaits() async throws {
        let c = center()
        c.draft(ImageCenter.sessionKey(sid)).append([ready("a")])
        let down = CommandError(CommandError.unavailable, "电脑不在线，指令没有发出")
        computer.answer(down, down)
        _ = await send(c, "看图")
        let o = try #require(store.outgoing(sid).first)
        await eventually("failed") { o.state == .failed }
        #expect(remote.events(sid).followers == 1)
        #expect(SessionImages.putBack(o, session: sid, state: RemoteState(), remote: remote, store: store))
        await eventually("let go of when put back") { remote.events(sid).followers == 0 }

        // The put-back image has to finish a fresh upload before another message can use it.
        let renewed = c.draft(ImageCenter.sessionKey(sid)).images[0]
        renewed.machineId = "mac"
        renewed.imageId = "renewed-a"
        renewed.expiresMs = Int64.max
        renewed.state = .ready
        // Sent again and followed again; then removed in the chat.
        _ = await send(c, "再看")
        let again = try #require(store.outgoing(sid).first)
        await eventually("failed") { again.state == .failed }
        #expect(remote.events(sid).followers == 1)
        store.remove(sid, again)
        await eventually("let go of when removed") { remote.events(sid).followers == 0 }
    }

    @Test func aMessageWithoutImagesGoesBackAsItsTextAlone() async {
        _ = center()
        await store.send(computer, sid: sid, text: "只有文字").value
        let o = store.outgoing(sid)[0]
        #expect(SessionImages.putBack(o, session: sid, state: RemoteState(), remote: remote, store: store))
        #expect(store.draft(sid) == "只有文字" && store.outgoing(sid).isEmpty)
    }

    @Test func aRunThatEndedLeavesItsMessagesToTheNext() async throws {
        let first = center()
        first.draft(ImageCenter.sessionKey(sid)).append([ready("a")])
        _ = await send(first, "看图")
        let sent = try #require(store.outgoing(sid).first)

        // The process was reclaimed: a new store, a new center, the same sealed files.
        let later = SessionStore()
        let c = center()
        c.restoreOutgoing(sid, store: later)
        await c.restoredOutgoing(sid)
        let o = try #require(later.outgoing(sid).first)
        #expect(o.requestId == sent.requestId && o.text == "看图" && o.imageIds == ["on-mac-a"])
        #expect(o.state == .unknown && o.error == "App 重启过，不确定这条有没有送到")
        // Asked again, it is not doubled.
        c.restoreOutgoing(sid, store: later)
        await c.restoredOutgoing(sid)
        #expect(later.outgoing(sid).count == 1)
    }

    @Test func aMessageTheUserRemovedDoesNotComeBack() async throws {
        let c = center()
        c.draft(ImageCenter.sessionKey(sid)).append([ready("a")])
        computer.answer(CommandError(CommandError.unavailable, "电脑不在线，指令没有发出"))
        _ = await send(c, "看图")
        let o = try #require(store.outgoing(sid).first)
        await eventually("failed") { o.state == .failed }
        // 移除此提示 in the chat.
        store.remove(sid, o)
        await eventually("its record is gone") { (try? OutgoingRecords(files: files).load(sid: sid).isEmpty) == true }
        #expect(o.images[0].processed == nil)
    }

    @Test func theButtonsFollowTheDraft() async {
        let c = center()
        let context = SessionImageContext(sessionId: sid, machineId: "mac", remote: remote, store: store, blocked: nil, limit: 6)
        _ = c.draft(ImageCenter.sessionKey(sid))
        await c.restoredDraft(ImageCenter.sessionKey(sid))
        #expect(!SessionImages.hasImages(context) && SessionImages.ready(context))
        let waiting = PendingImage(id: "w", uri: nil)
        c.draft(ImageCenter.sessionKey(sid)).append([ready("a"), waiting])
        // Never a message with some of its images.
        #expect(SessionImages.hasImages(context) && !SessionImages.ready(context))
        c.remove(ImageCenter.sessionKey(sid), waiting)
        #expect(SessionImages.ready(context))
        // On another computer they are not there.
        let other = SessionImageContext(sessionId: sid, machineId: "vps", remote: remote, store: store, blocked: nil, limit: 6)
        #expect(!SessionImages.ready(other))
    }

    @Test func plusSaysWhyItAddsNothing() {
        let c = center()
        let key = ImageCenter.sessionKey(sid)
        c.plus(key, blocked: "MacBook 离线，连上后才能添加图片", max: 6)
        #expect(c.notices[key] == "MacBook 离线，连上后才能添加图片" && c.picking[key] == nil)
        c.draft(key).append((1...6).map { ready("i\($0)") })
        c.plus(key, blocked: nil, max: 6)
        #expect(c.notices[key] == "一条消息最多 6 张" && c.picking[key] == nil)
        c.clearNotice(key, was: "别的话")
        #expect(c.notices[key] != nil)
        c.clearNotice(key, was: "一条消息最多 6 张")
        #expect(c.notices[key] == nil)
    }

    @Test func pickedFilesArePreparedAndTheExtraOnesAreLeftOut() async throws {
        let c = center()
        let key = ImageCenter.sessionKey(sid)
        _ = c.draft(key)
        await c.restoredDraft(key)
        // Not pictures: what matters here is how many go in, and what is said of the rest and of each.
        c.add(key, (1...8).map { .data(Data("not a picture \($0)".utf8)) }, max: 6)
        let draft = c.draft(key)
        #expect(draft.images.count == 6)
        #expect(draft.notice == "一条消息最多 6 张，多选的 2 张没有加入")
        await eventually("each says it could not be read") { draft.images.allSatisfy { $0.state == .failed(error: "解不出这张图片") } }
    }

    /// What the photo picker hands over is a file: it is read whole unless it is too large, and then not at all.
    @Test func aPickedPhotoIsReadFromItsFileUnlessItIsTooLarge() async throws {
        func picked(_ file: URL) async throws -> PickedPhoto {
            let provider = try #require(NSItemProvider(contentsOf: file))
            return try await withCheckedThrowingContinuation { done in
                _ = provider.loadTransferable(type: PickedPhoto.self) { done.resume(with: $0) }
            }
        }
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("ra-picked-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let bytes = try #require(ready("a").processed?.bytes)
        let small = dir.appendingPathComponent("small.png")
        try bytes.write(to: small)
        #expect(try await picked(small).read.get() == bytes)
        // Larger than is ever read of one, as a scan or a long GIF can be: nothing of it is written here.
        let large = dir.appendingPathComponent("large.tiff")
        #expect(FileManager.default.createFile(atPath: large.path, contents: nil))
        let handle = try FileHandle(forWritingTo: large)
        try handle.truncate(atOffset: UInt64(pickedReadLimit(original: false)) + 1)
        try handle.close()
        #expect(try await picked(large).read == .failure(ImageTooLarge("图片超过 80 MB")))
    }

    @Test func theSealedStoreKeepsADraftWithoutItsPixelsInTheClear() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "ra-images-\(UUID().uuidString)", directoryHint: .isDirectory)
        defer { try? FileManager.default.removeItem(at: root) }
        let pending = PendingStore(
            directory: root, vault: Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)"),
            defaults: UserDefaults(suiteName: "ra-images-\(UUID().uuidString)")!)
        defer { try? pending.clearAll() }
        let image = ready("a")
        let draft = ImageDraft(key: "session:s1", files: pending, saves: nil)
        draft.append([image])
        // The draft comes back from the files alone, in a process that has nothing else.
        let again = ImageDraft(key: "session:s1", files: pending, saves: nil)
        try again.restore()
        #expect(again.images.map(\.id) == ["a"] && again.images[0].processed == image.processed)
        // Nothing on disk reads as the picture, or names the session.
        let stored = try FileManager.default.subpathsOfDirectory(atPath: root.path).map { root.appending(path: $0) }
            .filter { (try? $0.resourceValues(forKeys: [.isRegularFileKey]))?.isRegularFile == true }
        var seen = 0
        for url in stored {
            seen += 1
            let bytes = try Data(contentsOf: url)
            #expect(bytes.range(of: Data("PNG".utf8)) == nil)
            #expect(!url.path.contains("session:s1") && !url.path.contains("s1/"))
        }
        #expect(seen >= 3)
        // Logging out leaves none of it.
        try pending.clearAll()
        #expect(!FileManager.default.fileExists(atPath: root.path))
    }
}

extension SessionImagesTests {
    @Test func anUploadThatExpiresWhileItsMessageIsSavedStaysInTheDraft() async throws {
        let c = center()
        let draft = c.draft(ImageCenter.sessionKey(sid))
        let img = ready("a")
        draft.append([img])
        let entered = DispatchSemaphore(value: 0)
        let release = DispatchSemaphore(value: 0)
        let sending = Task { await send(c, "看图") { _, _, _ in entered.signal(); release.wait() } }
        await Task.detached { waitForDisk(entered) }.value
        img.expiresMs = 0
        release.signal()
        let outcome = await sending.value
        guard case .failure? = outcome else { Issue.record("expired upload must not be sent"); return }
        #expect(computer.sent.isEmpty && store.outgoing(sid).isEmpty)
        #expect(draft.images.first === img && img.processed != nil)
        ImageCenter.remove(remote)
    }
}

/// Explicit synchronization around the real sealed-file restore. It refuses to
/// block the main thread, so a regression reports a failure instead of hanging.
private final class RestoreFiles: RACore.SealedFiles, @unchecked Sendable {
    let base = VolatileFiles()
    let entered = DispatchSemaphore(value: 0)
    let release = DispatchSemaphore(value: 0)
    private let lock = NSLock()
    private var held = false
    private(set) var mainRead = false
    func hold() { lock.withLock { held = true } }
    func put(folder: String, name: String, data: Data) throws { base.put(folder: folder, name: name, data: data) }
    func get(folder: String, name: String) throws -> Data? {
        let stop = lock.withLock { () -> Bool in
            guard held else { return false }
            held = false
            mainRead = Thread.isMainThread
            return true
        }
        if stop { entered.signal(); if !Thread.isMainThread { release.wait() } }
        return base.get(folder: folder, name: name)
    }
    func remove(folder: String, name: String) throws { base.remove(folder: folder, name: name) }
    func clearFolder(_ folder: String) throws { base.clearFolder(folder) }
}

extension SessionImagesTests {
    @Test func restoringDraftsReadsOffMainAndCannotRefillAnAccountAfterItWasClosed() async throws {
        let disk = RestoreFiles()
        let first = ImageDraft(key: "session:s1", files: disk, saves: nil)
        first.append([ready("old")])
        disk.hold()
        ImageCenter.install(remote: remote, files: disk)
        let c = ImageCenter.of(remote)
        let restored = c.draft("session:s1")
        let waiting = Task { await c.restoredDraft("session:s1") }
        await Task.detached { waitForDisk(disk.entered) }.value
        #expect(!disk.mainRead)
        #expect(restored.images.isEmpty && restored.restoring)
        ImageCenter.remove(remote)
        ImageCenter.install(remote: remote, files: VolatileFiles())
        disk.release.signal()
        await waiting.value
        #expect(restored.images.isEmpty)
        #expect(ImageCenter.of(remote).draft("session:s1").images.isEmpty)
        ImageCenter.remove(remote)
    }

    @Test func restoringPendingMessagesReadsOffMainAndCannotCrossAccounts() async throws {
        let disk = RestoreFiles()
        let o = Outgoing(requestId: "old", text: "旧账户", imageIds: ["old-image"], images: [ready("old")], original: true)
        try OutgoingRecords(files: disk).save(sid: sid, o.record, original: true)
        disk.hold()
        ImageCenter.install(remote: remote, files: disk)
        let c = ImageCenter.of(remote)
        c.restoreOutgoing(sid, store: store)
        let waiting = Task { await c.restoredOutgoing(sid) }
        await Task.detached { waitForDisk(disk.entered) }.value
        #expect(!disk.mainRead && store.outgoing(sid).isEmpty)
        ImageCenter.remove(remote)
        disk.release.signal()
        await waiting.value
        #expect(store.outgoing(sid).isEmpty)
    }
}

private func waitForDisk(_ semaphore: DispatchSemaphore) { semaphore.wait() }
