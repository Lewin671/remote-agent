import Foundation
import Observation
import RACore
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

/// Messages with images, from the box to the CLI: Android's SendImagesTest and the image half of `Outbox`.
@MainActor
@Suite struct SessionImagesTests {
    @MainActor
    private final class SubmitSpy {
        var sent: [Outgoing] = []
    }

    private let remote = try! Remote(
        account: Account(server: "http://127.0.0.1:1", deviceId: "00112233445566778899aabbccddeeff"),
        http: AppModel.http)
    private let files = VolatileFiles()
    private let store = SessionStore()
    private let spy = SubmitSpy()
    private let sid = "s1"

    private func centerWithCore() async throws -> (ImageCenter, CoreHost) {
        var state = RemoteState()
        state.machines["mac"] = MachineView(id: "mac", online: true, lastSeenMs: 0, meta: .with {
            $0.hostname = "MacBook"
            $0.capabilities.imageUpload = true
            $0.capabilities.fileUpload = true
        })
        state.sessions[sid] = SessionView(id: sid, machineId: "mac")
        await remote.showForTest(state: state)
        let host = try CoreHost.live(
            remote: remote, account: remote.account, defaults: UserDefaults(suiteName: "ra-si-\(UUID())")!,
            screens: CoreScreens(), demoMode: true, onNavigate: { _ in }
        )
        await host.seedDemo(state: state)
        ImageCenter.install(remote: remote, coreHost: host, files: files)
        return (ImageCenter.of(remote), host)
    }

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
        await c.restoredDraft(ImageCenter.sessionKey(sid))
        let spy = spy
        return await withCheckedContinuation { done in
            let taken = SessionImages.send(remote, session: sid, store: store, machineId: "mac", text: text, mode: .steer, keep: keep, submit: {
                spy.sent.append($0)
            }) {
                done.resume(returning: $0)
            }
            if !taken { done.resume(returning: nil) }
        }
    }

    @Test func draftRemoveTargetOnlyCoversTheCornerAndStaysInsideItsCell() {
        for side: CGFloat in [56, 68, 80] {
            let layout = DraftThumbGeometry(side: side)
            let thumb = layout.thumbnail
            let remove = layout.remove
            let overlap = thumb.intersection(remove)
            #expect(remove.width == 44 && remove.height == 44)
            #expect(remove.midX == thumb.maxX && remove.midY == thumb.minY)
            #expect(overlap.width * overlap.height <= 22 * 22)
            #expect(!remove.contains(CGPoint(x: thumb.midX, y: thumb.midY)))
            let neighbour = thumb.offsetBy(dx: layout.cell.width, dy: 0)
            #expect(remove.intersection(neighbour).width == 0 || !remove.intersects(neighbour))
            #expect(CGRect(origin: .zero, size: layout.cell).contains(remove))
        }
    }

    @Test func aDraftWithoutImagesIsNotThisCodesToSend() async {
        let c = center()
        #expect(await send(c, "只有文字") == nil)
        #expect(c.heldOutgoing(sid, requestId: "any") == nil && spy.sent.isEmpty)
    }

    @Test func sendingPreparedBytesMakesAnObservableThumbnailAndKeepsItAfterFailure() async throws {
        let c = center()
        defer { ImageCenter.remove(remote) }
        let key = ImageCenter.sessionKey(sid)
        let draft = c.draft(key)
        await c.restoredDraft(key)
        let img = ready("photo")
        let png = UIGraphicsImageRenderer(size: CGSize(width: 4, height: 3)).image { ctx in
            UIColor.red.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 4, height: 3))
        }.pngData()!
        img.processed = Processed(bytes: png, format: .png, width: 4, height: 3)
        draft.append([img])
        // A prepared image need not have a cached thumbnail yet. The outbox
        // must make one, and a bubble drawn before it arrives must redraw.
        final class Heard: @unchecked Sendable { var yes = false }
        let heard = Heard()
        withObservationTracking {
            #expect(c.thumb(img) == nil)
        } onChange: { heard.yes = true }
        _ = await send(c, "看图")
        await eventually("sent and held") { !spy.sent.isEmpty }
        let submitted = try #require(spy.sent.first)
        let o = try #require(c.heldOutgoing(sid, requestId: submitted.requestId))
        await eventually("thumbnail decoded") { c.thumb(img) != nil }
        #expect(heard.yes)
        #expect(draft.images.isEmpty && o.images.first === img && img.processed?.bytes == png)
        #expect(o.imageIds == ["on-mac-photo"])

        // Both terminal failures and a lost command response leave the same
        // attachment visible, whether or not its thumbnail decoded.
        for state in [Outgoing.State.sending, .sent, .unknown, .failed] {
            o.state = state
            let rendered = ImageRenderer(content: OutgoingImageThumbs(center: c, message: o).frame(width: 320))
            #expect(try #require(rendered.uiImage).size.height >= 56)
        }

        let restoredCenter = center()
        restoredCenter.restoreOutgoing(sid)
        await restoredCenter.restoredOutgoing(sid)
        let restored = try #require(restoredCenter.restoredOutgoing[sid]?.first)
        await eventually("restart rebuilds the thumbnail from sealed bytes") { restoredCenter.thumb(restored.images[0]) != nil }
        #expect(restored.state == .unknown && restored.images[0].processed?.bytes == png)
    }

    @Test func picturesWithoutDecodedThumbnailsStillAppearInOutgoingAndPendingBubbles() async throws {
        let c = center()
        defer { ImageCenter.remove(remote) }
        // Invalid preview bytes exercise the decode-failure path, as well as
        // the interval before an asynchronous thumbnail has arrived.
        let img = ready("photo")
        c.draft(ImageCenter.sessionKey(sid)).append([img])
        _ = await send(c, "看图")
        await eventually("held outgoing ready") { !spy.sent.isEmpty }
        let o = try #require(c.heldOutgoing(sid, requestId: spy.sent[0].requestId))
        #expect(c.thumb(o.images[0]) == nil)
        for state in [Outgoing.State.sending, .sent, .unknown, .failed] {
            o.state = state
            let rendered = ImageRenderer(content: OutgoingImageThumbs(center: c, message: o).frame(width: 320))
            #expect(try #require(rendered.uiImage).size.height >= 56)
        }
        let account = Account(server: "http://127.0.0.1:1", deviceId: "00112233445566778899aabbccddeeff")
        let context = AccountContext(account: account, remote: remote)
        // PendingThumb draws the retained local attachment whenever Core shows a pending/queued item for requestId.
        let rendered = ImageRenderer(content:
            SessionImages.PendingThumbs(session: sid, requestId: o.requestId)
                .environment(context).frame(width: 320))
        #expect(try #require(rendered.uiImage).size.height >= 56)
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
        #expect(spy.sent.isEmpty)
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
        #expect(errorText(error) == L10n.attachmentIosSignedOutOfTheAccount())
        try? await Task.sleep(nanoseconds: 50_000_000)
        #expect(spy.sent.isEmpty)
    }

    @Test func aSecondSendWhileTheImagesAreBeingKeptSendsNothing() async throws {
        let c = center()
        let draft = c.draft(ImageCenter.sessionKey(sid))
        await c.restoredDraft(ImageCenter.sessionKey(sid))
        draft.append([ready("a")])
        let disk = DispatchSemaphore(value: 0)
        var outcomes = 0
        #expect(SessionImages.send(remote, session: sid, store: store, machineId: "mac", text: "看图", mode: .steer, keep: { _, _, _ in disk.wait() }, submit: {
            spy.sent.append($0)
        }) { _ in outcomes += 1 })
        #expect(store.isKeeping(sid))
        // Taken over, and nothing done: the first message has these images.
        #expect(SessionImages.send(remote, session: sid, store: store, machineId: "mac", text: "看图", mode: .steer, keep: { _, _, _ in }, submit: {
            spy.sent.append($0)
        }) { _ in outcomes += 1 })
        try await Task.sleep(nanoseconds: 50_000_000)
        #expect(spy.sent.isEmpty && outcomes == 0)
        disk.signal()
        await eventually("the first message is delivered") { !spy.sent.isEmpty }
        #expect(!store.isKeeping(sid) && outcomes == 1)
        #expect(spy.sent.count == 1 && draft.images.isEmpty)
        // The lock went with the message: the next one is sent.
        draft.append([ready("b")])
        #expect(SessionImages.send(remote, session: sid, store: store, machineId: "mac", text: "再看", mode: .steer, keep: { _, _, _ in }, submit: {
            spy.sent.append($0)
        }) { _ in outcomes += 1 })
        await eventually("the second message is delivered") { spy.sent.count == 2 }
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
        await eventually("submitted") { !spy.sent.isEmpty }
        let o = try #require(spy.sent.first)
        #expect(o.imageIds == ["on-mac-a", "on-mac-b"] && o.images.map(\.id) == ["a", "b"])
        #expect(try OutgoingRecords(files: files).load(sid: sid).map(\.requestId) == [o.requestId])
        #expect(c.heldOutgoing(sid, requestId: o.requestId) === o)
    }

    @Test func itIsLetGoOfOnceSettledOrCanceledInCore() async throws {
        let c = center()
        c.draft(ImageCenter.sessionKey(sid)).append([ready("a")])
        _ = await send(c, "看图")
        await eventually("submitted") { !spy.sent.isEmpty }
        let o = try #require(spy.sent.first)
        // While active in Core (pendingInputs / outgoing), the held images stay.
        c.pruneSettled(sid, activeRequestIDs: [o.requestId])
        #expect(c.heldOutgoing(sid, requestId: o.requestId) != nil && o.images[0].processed != nil)
        // Once settled in timeline or removed from Core active IDs, release local bytes and record.
        c.pruneSettled(sid, activeRequestIDs: [], timelineSettledRequestIDs: [o.requestId])
        #expect(c.heldOutgoing(sid, requestId: o.requestId) == nil && o.images[0].processed == nil)
        await eventually("its record is gone") { (try? OutgoingRecords(files: files).load(sid: sid).isEmpty) == true }
    }

    @Test func aMessageTheSessionEndedOnKeepsItsImagesToBePutBack() async throws {
        let (c, host) = try await centerWithCore(); defer { host.close() }
        let draft = c.draft(ImageCenter.sessionKey(sid))
        draft.append([ready("a")])
        _ = await send(c, "看图")
        await eventually("submitted") { !spy.sent.isEmpty }
        let o = try #require(spy.sent.first)
        // Still held if unsentOnExit/active in Core.
        c.pruneSettled(sid, activeRequestIDs: [o.requestId])
        #expect(c.heldOutgoing(sid, requestId: o.requestId) === o && o.images[0].processed != nil)
        #expect(try OutgoingRecords(files: files).load(sid: sid).map(\.requestId) == [o.requestId])

        let effect = PBSessionRestoreComposerEffect.with {
            $0.sessionID = sid
            $0.requestID = o.requestId
            $0.text = "看图"
        }
        #expect(await SessionImages.applyRestore(effect, session: sid, remote: remote, store: store))
        #expect(store.draft(sid) == "看图" && c.heldOutgoing(sid, requestId: o.requestId) == nil)
        #expect(draft.images.map(\.id) == ["a"] && draft.images[0].processed != nil)
    }

    @Test func aMessageNotSubmittedGoesBackIntoTheBoxWithItsImages() async throws {
        let (c, host) = try await centerWithCore(); defer { host.close() }
        let draft = c.draft(ImageCenter.sessionKey(sid))
        draft.append([ready("a"), ready("b")])
        _ = await send(c, "看图")
        await eventually("submitted") { !spy.sent.isEmpty }
        let o = try #require(spy.sent.first)
        o.state = .failed
        store.setDraft(sid, "已经在写的")

        #expect(await SessionImages.putBack(o, session: sid, remote: remote, store: store))
        #expect(c.heldOutgoing(sid, requestId: o.requestId) == nil)
        #expect(store.draft(sid) == "已经在写的\n看图")
        // The images are in the box again and go to the computer again; nothing of them was dropped.
        #expect(draft.images.map(\.id) == ["a", "b"])
        #expect(draft.images.allSatisfy { $0.state == .failed(error: AttachmentFailure(kind: .uploadFailed)) && $0.processed != nil })
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
        var state = RemoteState()
        state.machines["mac"] = MachineView(id: "mac", online: true, lastSeenMs: 0, meta: .with {
            $0.hostname = "MacBook"
            $0.capabilities.imageUpload = true
        })
        state.sessions[sid] = SessionView(id: sid, machineId: "mac")
        await remote.showForTest(state: state)
        let host = try CoreHost.live(
            remote: remote, account: remote.account, defaults: UserDefaults(suiteName: "ra-cramped-\(UUID())")!,
            screens: CoreScreens(), demoMode: true, onNavigate: { _ in }
        )
        defer { host.close() }
        await host.seedDemo(state: state)
        ImageCenter.install(remote: remote, coreHost: host, files: files)
        let c = ImageCenter.of(remote)
        let key = ImageCenter.sessionKey(sid)
        let draft = c.draft(key)
        draft.append([ready("a")])
        _ = await send(c, "看图")
        await eventually("submitted") { !spy.sent.isEmpty }
        let o = try #require(spy.sent.first)
        o.state = .failed

        // The box cannot keep the image it gets back: the message's record is the only copy on disk.
        files.full = key
        #expect(await SessionImages.putBack(o, session: sid, remote: remote, store: store))
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
        let (c, host) = try await centerWithCore(); defer { host.close() }
        let draft = c.draft(ImageCenter.sessionKey(sid))
        draft.append((1...4).map { ready("m\($0)") })
        _ = await send(c, "四张")
        await eventually("submitted") { !spy.sent.isEmpty }
        let o = try #require(spy.sent.first)
        o.state = .failed
        draft.append((1...3).map { ready("n\($0)") })

        #expect(await !SessionImages.canPutBack(requestId: o.requestId, session: sid, remote: remote))
        #expect(await !SessionImages.putBack(o, session: sid, remote: remote, store: store))
        // Nothing moved: the message is still there to retry or to put back later.
        #expect(c.heldOutgoing(sid, requestId: o.requestId) != nil && store.draft(sid).isEmpty)
        #expect(draft.images.count == 3)
        #expect(draft.notice.contains("一条消息最多 6 张"))
        #expect(try OutgoingRecords(files: files).load(sid: sid).count == 1)
    }

    @Test func aMessageWithoutImagesGoesBackAsItsTextAlone() async {
        _ = center()
        let effect = PBSessionRestoreComposerEffect.with {
            $0.sessionID = sid
            $0.requestID = "text-only"
            $0.text = "只有文字"
        }
        #expect(await SessionImages.applyRestore(effect, session: sid, remote: remote, store: store))
        #expect(store.draft(sid) == "只有文字")
    }

    @Test func aRunThatEndedLeavesItsMessagesToTheNext() async throws {
        let first = center()
        first.draft(ImageCenter.sessionKey(sid)).append([ready("a")])
        _ = await send(first, "看图")
        await eventually("submitted") { !spy.sent.isEmpty }
        let sent = try #require(spy.sent.first)

        // The process was reclaimed: a new center, the same sealed files.
        let c = center()
        c.restoreOutgoing(sid)
        await c.restoredOutgoing(sid)
        let o = try #require(c.restoredOutgoing[sid]?.first)
        #expect(o.requestId == sent.requestId && o.text == "看图" && o.imageIds == ["on-mac-a"])
        #expect(o.state == .unknown && o.error == L10n.attachmentIosTheAppRestartedDeliveryOfThisMessageIsUnconfirmed())
        // Asked again, it is not doubled.
        c.restoreOutgoing(sid)
        await c.restoredOutgoing(sid)
        #expect(c.restoredOutgoing[sid]?.count == 1)
    }

    @Test func aMessageTheUserRemovedDoesNotComeBack() async throws {
        let c = center()
        c.draft(ImageCenter.sessionKey(sid)).append([ready("a")])
        _ = await send(c, "看图")
        await eventually("submitted") { !spy.sent.isEmpty }
        let o = try #require(spy.sent.first)
        // Dismiss in the chat.
        c.release(sid, o)
        await eventually("its record is gone") { (try? OutgoingRecords(files: files).load(sid: sid).isEmpty) == true }
        #expect(o.images[0].processed == nil)
    }

    @Test(arguments: ["session:test", "project:/work/example"])
    func theButtonsFollowTheDraft(key: String) async {
        let c = center()
        let context = ComposerAttachments(remote: remote, key: key, machineId: "mac", caps: AttachmentCapabilities(supportsImages: true, maxImages: 6))
        _ = c.draft(key)
        await c.restoredDraft(key)
        #expect(!context.hasImages && context.ready)
        let waiting = PendingImage(id: "w", uri: nil)
        c.draft(key).append([ready("a"), waiting])
        // Never a message with some of its images.
        #expect(context.hasImages && !context.ready)
        c.remove(key, waiting)
        #expect(context.ready)
        // On another computer they are not there.
        let other = ComposerAttachments(remote: remote, key: key, machineId: "vps", caps: AttachmentCapabilities(supportsImages: true, maxImages: 6))
        #expect(!other.ready)
    }

    @Test func sendingOverLimitDraftIsBlockedByCoreAttachmentEval() async throws {
        let (c, host) = try await centerWithCore(); defer { host.close() }
        let key = ImageCenter.sessionKey(sid)
        let draft = c.draft(key)
        await c.restoredDraft(key)
        draft.append((1...7).map { ready("over-\($0)") })
        let result = await send(c, "七张")
        guard case .failure(let error)? = result else {
            Issue.record("expected over-limit draft send to fail via Core AttachmentEval")
            return
        }
        #expect(errorText(error).contains("一条消息最多 6 张"))
        #expect(c.notices[key]?.text.contains("一条消息最多 6 张") == true)
        #expect(spy.sent.isEmpty && draft.images.count == 7)
    }

    #if DEBUG
    @Test func plusSaysWhyItAddsNothing() async throws {
        var state = RemoteState()
        state.machines["mac"] = MachineView(id: "mac", online: true, lastSeenMs: 0, meta: .with {
            $0.hostname = "MacBook"
            $0.capabilities.imageUpload = true
            $0.capabilities.fileUpload = false
        })
        state.machines["mac-files"] = MachineView(id: "mac-files", online: true, lastSeenMs: 0, meta: .with {
            $0.hostname = "MacPro"
            $0.capabilities.imageUpload = true
            $0.capabilities.fileUpload = true
            $0.capabilities.maxFilesPerMessage = 6
        })
        state.sessions[sid] = SessionView(id: sid, machineId: "mac")
        await remote.showForTest(state: state)
        let host = try CoreHost.live(
            remote: remote, account: remote.account, defaults: UserDefaults(suiteName: "ra-plus-\(UUID())")!,
            screens: CoreScreens(), demoMode: true, onNavigate: { _ in }
        )
        defer { host.close() }
        await host.seedDemo(state: state)
        ImageCenter.install(remote: remote, coreHost: host, files: files, live: false)
        defer { ImageCenter.remove(remote) }

        let c = ImageCenter.of(remote)
        let key = ImageCenter.sessionKey(sid)
        c.plus(key, machineId: "mac", caps: AttachmentCapabilities(imageUploadBlocked: L10n.attachmentComputerOffline(name: "MacBook")))
        #expect(c.notices[key]?.text == L10n.attachmentComputerOffline(name: "MacBook") && c.picking[key] == nil)
        c.draft(key).append((1...6).map { ready("i\($0)") })
        c.plus(key, machineId: "mac", caps: AttachmentCapabilities(supportsFiles: false))
        await eventually("max images notice from Core imagesPickBlockedReason") {
            c.notices[key]?.text == L10n.attachmentMaxImages(count: 6) && c.picking[key] == nil
        }
        c.documents(key, machineId: "mac", caps: AttachmentCapabilities(supportsFiles: false))
        await eventually("files unsupported notice from Core filesPickBlockedReason") {
            c.notices[key]?.text == L10n.attachmentUpdateRequired() && c.picking[key] == nil
        }
        let fileItems = (1...6).map { idx -> PendingImage in
            let item = PendingImage(id: "doc-\(idx)", uri: nil)
            item.file = PickedFile(remote: .with { $0.name = "f\(idx).txt"; $0.sizeBytes = 10 })
            return item
        }
        c.draft(key).append(fileItems)
        c.documents(key, machineId: "mac-files", caps: AttachmentCapabilities(supportsFiles: true))
        await eventually("max files notice from Core filesPickBlockedReason") {
            c.notices[key]?.text == L10n.attachmentMaxFiles(count: 6) && c.picking[key] == nil
        }
        let noticeID = c.notices[key]!.id
        c.clearNotice(key, id: UUID())
        #expect(c.notices[key] != nil)
        c.clearNotice(key, id: noticeID)
        #expect(c.notices[key] == nil)
    }

    @Test func pickedFilesArePreparedAndTheExtraOnesAreLeftOut() async throws {
        let (c, host) = try await centerWithCore()
        defer { host.close(); ImageCenter.remove(remote) }
        let key = ImageCenter.sessionKey(sid)
        _ = c.draft(key)
        await c.restoredDraft(key)
        // Not pictures: Core admits 6 and sets notice for the extra 2; preparing fails with structured .decode.
        c.add(key, (1...8).map { .data(Data("not a picture \($0)".utf8)) }, machineId: "mac")
        let draft = c.draft(key)
        await eventually("6 admitted with Core notice") {
            draft.images.count == 6 && draft.notice == "一条消息最多 6 张，多选的 2 张没有加入"
        }
        await eventually("each says it could not be read") {
            draft.images.allSatisfy { $0.state == .failed(error: AttachmentFailure(kind: .decode)) }
        }
        await c.refreshProblems(key, machineId: "mac")
        #expect(c.failureSummaries[key] == L10n.imageCannotReadThisImageTryJpegOrPngInstead())
        #expect(draft.photos.allSatisfy { c.evaluatedProblems[$0.id]?.kind == .decode })
    }
    #endif

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
        #expect(try await picked(large).read == .failure(ImageTooLarge(L10nMessage.imageIosImageExceedsMb(count: 80), kind: .dimensions)))
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
        #expect(spy.sent.isEmpty)
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
    private var failing = false
    func failReads(_ value: Bool) { lock.withLock { failing = value } }
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
        if lock.withLock({ failing }) { throw UserError("read failed") }
        return base.get(folder: folder, name: name)
    }
    func remove(folder: String, name: String) throws { base.remove(folder: folder, name: name) }
    func clearFolder(_ folder: String) throws { base.clearFolder(folder) }
}

extension SessionImagesTests {
    @Test func failedRestorationKeepsDraftAndOutgoingRecordsAndCanRetryInPlace() async throws {
        let disk = RestoreFiles()
        let first = ImageDraft(key: "session:s1", files: disk, saves: nil)
        first.append([ready("draft")])
        let record = Outgoing(requestId: "old", text: "keep", images: [ready("outgoing")])
        try OutgoingRecords(files: disk).save(sid: sid, record.record, original: false)
        disk.failReads(true)
        ImageCenter.install(remote: remote, files: disk)
        defer { ImageCenter.remove(remote) }
        let c = ImageCenter.of(remote)
        let d = c.draft("session:s1")
        c.restoreOutgoing(sid)
        await c.restoredDraft("session:s1")
        await c.restoredOutgoing(sid)
        #expect(d.restoreError != nil && c.outgoingRestoreErrors[sid] != nil)
        d.persist()
        #expect(c.restoredOutgoing[sid] == nil)
        disk.failReads(false)
        c.retryDraft("session:s1")
        c.restoreOutgoing(sid)
        await c.restoredDraft("session:s1")
        await c.restoredOutgoing(sid)
        #expect(d.restoreError == nil && c.outgoingRestoreErrors[sid] == nil)
        #expect(d.images.map(\.id) == ["draft"])
        #expect(c.restoredOutgoing[sid]?.map(\.requestId) == ["old"])
    }

    @Test func textDraftReadFailureBlocksReplacementAndRetriesWithoutLosingEdits() {
        let disk = RestoreFiles()
        let first = SessionStore(draftFiles: disk, draftQueue: nil)
        first.setDraft(sid, "old text")
        disk.failReads(true)
        let next = SessionStore(draftFiles: disk, draftQueue: nil)
        #expect(next.draft(sid).isEmpty)
        next.setDraft(sid, "typed during failure")
        #expect(next.draftErrors[sid] != nil)
        disk.failReads(false)
        #expect(SessionStore(draftFiles: disk, draftQueue: nil).draft(sid) == "old text")
        #expect(next.draft(sid) == "typed during failure")
        #expect(SessionStore(draftFiles: disk, draftQueue: nil).draft(sid) == "typed during failure")
    }

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
        c.restoreOutgoing(sid)
        let waiting = Task { await c.restoredOutgoing(sid) }
        await Task.detached { waitForDisk(disk.entered) }.value
        #expect(!disk.mainRead && c.restoredOutgoing[sid] == nil)
        ImageCenter.remove(remote)
        disk.release.signal()
        await waiting.value
        #expect(c.restoredOutgoing[sid] == nil)
    }
}

private func waitForDisk(_ semaphore: DispatchSemaphore) { semaphore.wait() }
