import AVFoundation
import CryptoKit
import Foundation
import RACore
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

private func nextSignal<T: Sendable>(_ stream: AsyncStream<T>) async -> T? {
    var it = stream.makeAsyncIterator()
    return await it.next()
}

/// In-process upload responder for real-Core attachment smoke tests.
private final class FakeUploadDaemon: @unchecked Sendable {
    private let lock = NSLock()
    private var transfers: [String: (machine: String, total: Int64, received: Data)] = [:]
    private var _sentCommands: [(machine: String, send: PBSendRequest)] = []

    var sentCommands: [(machine: String, send: PBSendRequest)] {
        lock.withLock { _sentCommands }
    }

    func action(for effect: PBCoreDispatchCommandEffect, cmd: PBCommand) -> CoreHost.DemoCommandAction? {
        switch cmd.kind {
        case .beginImageUpload(let req)?:
            return .respond(lock.withLock {
                transfers[req.transferID] = (effect.machineID, req.totalBytes, Data())
                return CoreHost.demoExecutedOutcome(for: effect) {
                    $0.beginImageUpload = .with {
                        $0.status = .with {
                            $0.transferID = req.transferID
                            $0.persistedBytes = 0
                            $0.maxChunkBytes = 64 * 1024
                            $0.state = .receiving
                            $0.expiresMs = nowMs() + 3_600_000
                        }
                    }
                }
            })
        case .putImageChunk(let req)?:
            return .respond(lock.withLock {
                var entry = transfers[req.transferID] ?? (effect.machineID, 0, Data())
                if Int64(entry.received.count) == req.offset {
                    entry.received.append(req.data)
                }
                transfers[req.transferID] = entry
                return CoreHost.demoExecutedOutcome(for: effect) {
                    $0.putImageChunk = .with {
                        $0.status = .with {
                            $0.transferID = req.transferID
                            $0.persistedBytes = Int64(entry.received.count)
                            $0.maxChunkBytes = 64 * 1024
                            $0.state = .receiving
                            $0.expiresMs = nowMs() + 3_600_000
                        }
                    }
                }
            })
        case .completeImageUpload(let req)?:
            return .respond(lock.withLock {
                let entry = transfers[req.transferID]
                let size = Int64(entry?.received.count ?? 0)
                return CoreHost.demoExecutedOutcome(for: effect) {
                    $0.completeImageUpload = .with {
                        $0.status = .with {
                            $0.transferID = req.transferID
                            $0.persistedBytes = size
                            $0.maxChunkBytes = 64 * 1024
                            $0.state = .complete
                            $0.imageID = "remote-\(effect.machineID)-\(req.transferID.prefix(6))"
                            $0.expiresMs = nowMs() + 3_600_000
                        }
                    }
                }
            })
        case .abortImageUpload?:
            return .respond(CoreHost.demoExecutedOutcome(for: effect))
        case .send(let req)?:
            return .respond(lock.withLock {
                _sentCommands.append((effect.machineID, req))
                return CoreHost.demoExecutedOutcome(for: effect)
            })
        default:
            return nil
        }
    }
}

@MainActor @Suite struct AttachmentExecutorTests {
    private func samplePNG(_ color: UIColor = .systemBlue) -> Data {
        UIGraphicsImageRenderer(size: CGSize(width: 24, height: 18)).image { ctx in
            color.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 24, height: 18))
        }.pngData()!
    }

    private func makeHarness(
        store: PendingStore,
        queue: DraftStorageQueue = DraftStorageQueue(),
        daemon: FakeUploadDaemon = FakeUploadDaemon(),
        persist: Bool = true
    ) async throws -> (host: CoreHost, screens: CoreScreens, center: ImageCenter, remote: Remote) {
        let account = Account(
            server: "http://127.0.0.1:1",
            deviceId: "00112233445566778899aabbccddeeff",
            accountId: "11111111111111111111111111111111"
        )
        let remote = try Remote(account: account, http: HTTP(), allowPlainHttp: true)
        let screens = CoreScreens()
        let scope = store.scoped()
        let host = try CoreHost.live(
            remote: remote,
            account: account,
            defaults: UserDefaults(suiteName: "att-test-\(UUID().uuidString)")!,
            screens: screens,
            pendingStore: scope,
            draftStorageQueue: queue,
            demoMode: true,
            onNavigate: { _ in }
        )
        let center = ImageCenter.install(remote: remote, coreHost: host, pending: scope)
        await host.setAttachmentHandlers(center.handlers())
        await host.setDemoCommandResponder { eff, cmd in
            daemon.action(for: eff, cmd: cmd)
        }

        var world = DemoWorld()
        world.machines["m1"] = .with {
            $0.machineID = "m1"
            $0.online = true
            $0.lastSeenMs = nowMs()
            $0.meta = .with {
                $0.hostname = "Mac-A"
                $0.home = "/Users/me"
                $0.os = "darwin"
                $0.protocolVersion = peerProtocolVersion
                $0.capabilities = .with {
                    $0.imageUpload = true
                    $0.maxImagesPerMessage = 6
                    $0.maxUploadBytes = 20 * 1024 * 1024
                    $0.fileUpload = true
                    $0.maxFilesPerMessage = 5
                    $0.maxFileUploadBytes = 100 * 1024 * 1024
                }
            }
            $0.metaRev = 1
        }
        world.sessions["s1"] = .with {
            $0.sessionID = "s1"
            $0.machineID = "m1"
            $0.meta = .with {
                $0.id = "s1"
                $0.cwd = "/Users/me/proj"
                $0.title = "Draft Smoke"
                $0.status = .idle
                $0.agent = .codex
            }
            $0.metaRev = 1
            $0.updatedMs = nowMs()
        }
        await host.seedDemo(world: world)
        if persist {
            // Re-configure with persistence enabled so startup scan & disk persistence run.
            await host.dispatch(PBCoreEvent.with {
                $0.attachmentConfig = .with {
                    $0.persistDrafts = true
                    $0.persistOutbox = true
                    $0.scanStorage = true
                    $0.maxChunkBytes = 0
                    $0.hasPersistedPreparedImages_p = true
                }
            })
            await host.drainDraftStorageQueue()
        }
        _ = await host.acquireSessionWatch("s1")
        return (host, screens, center, remote)
    }

    private typealias Harness = (host: CoreHost, screens: CoreScreens, center: ImageCenter, remote: Remote)

    private func withHarness(
        store: PendingStore,
        queue: DraftStorageQueue = DraftStorageQueue(),
        daemon: FakeUploadDaemon = FakeUploadDaemon(),
        persist: Bool = true,
        _ body: (Harness) async throws -> Void
    ) async throws {
        let h = try await makeHarness(store: store, queue: queue, daemon: daemon, persist: persist)
        do {
            try await body(h)
        } catch {
            await tearDown(h)
            throw error
        }
        await tearDown(h)
    }

    private func tearDown(_ h: Harness) async {
        await h.host.drainAttachmentWorkForTesting()
        await h.host.closeAsync()
        ImageCenter.remove(h.remote)
    }

    @Test func realCorePrepareUploadSendAndRestartPersistenceSmoke() async throws {
        let dir = FileManager.default.temporaryDirectory.appending(path: "att-smoke-\(UUID().uuidString)", directoryHint: .isDirectory)
        let vault = Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")
        defer {
            try? FileManager.default.removeItem(at: dir)
            try? vault.clear()
        }

        let daemon = FakeUploadDaemon()
        let sharedQueue = DraftStorageQueue()
        let scope = PBDraftScope.session("s1")

        try await withHarness(
            store: PendingStore(directory: dir, vault: vault),
            queue: sharedQueue,
            daemon: daemon,
            persist: true
        ) { h1 in
            await h1.host.openDraft(scope: scope, machineID: "m1")
            // Wait for the initial DraftStorageRead to settle before picking images,
            // matching the UI which disables picking while `draft.restoring` is true.
            await h1.host.drainDraftStorageQueue()
            await h1.host.setDraftText(scope: scope, text: "persisted text")
            await h1.center.addImageData(scope: scope, [samplePNG()])?.value
            await h1.host.drainAttachmentWorkForTesting()

            let d1 = h1.screens.sessions["s1"]?.composer.draft
            #expect(d1?.text == "persisted text")
            #expect(d1?.images.count == 1)
            #expect(d1?.images.first?.state == .ready)
        }

        // Host 1 is closed and its storage queue fully drained; start host 2 on the same encrypted PendingStore.
        try await withHarness(
            store: PendingStore(directory: dir, vault: vault),
            queue: sharedQueue,
            daemon: daemon,
            persist: true
        ) { h2 in
            await h2.host.openDraft(scope: scope, machineID: "m1")
            await h2.host.drainAttachmentWorkForTesting()

            let d2 = h2.screens.sessions["s1"]?.composer.draft
            #expect(d2?.text == "persisted text")
            #expect(d2?.images.count == 1)
            #expect(d2?.images.first?.state == .ready)

            // Send via Core -> DraftSendSessionAction clears draft and dispatches SendInput with image_ids.
            await h2.host.sendSessionDraft(sessionID: "s1", mode: .steer)
            await h2.host.drainAttachmentWorkForTesting()

            let d3 = h2.screens.sessions["s1"]?.composer.draft
            #expect(d3?.text.isEmpty == true)
            #expect(d3?.images.isEmpty == true)
            let sent = daemon.sentCommands
            #expect(sent.count == 1)
            #expect(sent.first?.send.imageIds.count == 1)
        }
    }

    @Test func originalToggleAndOriginalTotalSizeWorkWithRealCore() async throws {
        let store = PendingStore.ephemeral()
        let daemon = FakeUploadDaemon()
        try await withHarness(store: store, daemon: daemon, persist: false) { h in
            let scope = PBDraftScope.session("s1")
            await h.host.openDraft(scope: scope, machineID: "m1")
            await h.center.addImageData(scope: scope, [samplePNG(.systemRed)])?.value
            await h.host.drainAttachmentWorkForTesting()

            let d1 = h.screens.sessions["s1"]?.composer.draft
            #expect(d1?.images.count == 1)
            #expect(d1?.images.first?.state == .ready)
            #expect(d1?.canToggleOriginal == true)

            await h.host.setDraftOriginal(scope: scope, original: true)
            await h.host.drainAttachmentWorkForTesting()

            let d2 = h.screens.sessions["s1"]?.composer.draft
            #expect(d2?.original == true)
            #expect(d2?.images.first?.state == .ready)
            #expect(d2?.hasOriginalTotalSizeText == true)
            #expect(!(d2?.originalTotalSizeText.rendered().isEmpty ?? true))
            #expect(d2?.canSend == true)
        }
    }

    @Test func readChunkNeverFallsBackToRawSourceRefForImages() async throws {
        let store = PendingStore.ephemeral()
        let rawSourceRef = try store.storeBlob(Data("raw-exif-bytes".utf8), prefix: "src")
        try await withHarness(store: store, persist: false) { h in
            let res = await h.host.readChunkForTesting(.with {
                $0.opID = 77
                $0.scope = .session("s1")
                $0.itemID = "img-1"
                $0.kind = .image
                $0.sourceRef = rawSourceRef
                $0.preparedRef = ""
                $0.offset = 0
                $0.length = 14
                $0.expectedSizeBytes = 14
            })
            #expect(res.opID == 77)
            #expect(!res.ok)
            #expect(res.errorKind == .sourceMissing)
            #expect(res.data.isEmpty)

            // Unspecified kind must never read sourceRef or preparedRef either.
            let unspecRes = await h.host.readChunkForTesting(.with {
                $0.opID = 78
                $0.scope = .session("s1")
                $0.itemID = "img-unspec"
                $0.kind = .unspecified
                $0.sourceRef = rawSourceRef
                $0.preparedRef = rawSourceRef
                $0.offset = 0
                $0.length = 14
                $0.expectedSizeBytes = 14
            })
            #expect(unspecRes.opID == 78)
            #expect(!unspecRes.ok)
            #expect(unspecRes.errorKind == .sourceMissing)
            #expect(unspecRes.data.isEmpty)
        }
    }

    @Test func preparePropagatesCancellationAndReportsVaultLockedWithoutDecodeMisclassification() async throws {
        let dir = FileManager.default.temporaryDirectory.appending(path: "att-l3-\(UUID().uuidString)", directoryHint: .isDirectory)
        let service = "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)"
        let liveVault = Vault(service: service)
        defer {
            try? FileManager.default.removeItem(at: dir)
            try? liveVault.clear()
        }

        // Write a real encrypted source blob first.
        let writerStore = PendingStore(directory: dir, vault: liveVault)
        let srcRef = try writerStore.storeBlob(samplePNG(.systemTeal), prefix: "src")

        // 1) Locked Keychain Vault must NOT set isReadingSource = true (which Core would misclassify as DECODE)
        //    and must return an ASCII diagnostic token rather than localized UI prose.
        let lockedOps = KeychainOps(
            copyMatching: { _, _ in errSecInteractionNotAllowed },
            add: { _, _ in errSecInteractionNotAllowed },
            update: { _, _ in errSecInteractionNotAllowed },
            delete: { _ in errSecInteractionNotAllowed }
        )
        let lockedStore = PendingStore(directory: dir, vault: Vault(service: service, keychain: lockedOps))
        try await withHarness(store: lockedStore, persist: false) { hLocked in
            let lockedRes = await hLocked.host.prepareImageForTesting(.with {
                $0.opID = 101
                $0.scope = .session("s1")
                $0.itemID = "img-locked"
                $0.sourceRef = srcRef
                $0.maxOriginalBytes = 20 * 1024 * 1024
                $0.maxDimensionPx = 2048
                $0.maxCompressedBytes = 900 * 1024
            })
            #expect(!lockedRes.ok)
            #expect(lockedRes.errorKind == .other)
            #expect(!lockedRes.isReadingSource)
            #expect(lockedRes.errorDetail == "vault_locked")
        }

        // 2) Cancelling the parent task must propagate into the detached worker (`canceled` instead of proceeding).
        try await withHarness(store: writerStore, persist: false) { hCancel in
            let gate = AsyncStream<Void>.makeStream()
            let cancelledTask = Task {
                var it = gate.stream.makeAsyncIterator()
                _ = await it.next()
                return await hCancel.host.prepareImageForTesting(.with {
                    $0.opID = 102
                    $0.scope = .session("s1")
                    $0.itemID = "img-cancel"
                    $0.sourceRef = srcRef
                    $0.maxOriginalBytes = 20 * 1024 * 1024
                    $0.maxDimensionPx = 2048
                    $0.maxCompressedBytes = 900 * 1024
                })
            }
            cancelledTask.cancel()
            gate.continuation.finish()
            let cancelRes = await cancelledTask.value
            #expect(cancelRes.opID == 102)
            #expect(!cancelRes.ok)
            #expect(cancelRes.errorKind == .other)
            #expect(cancelRes.errorDetail == "canceled")
        }
    }

    @Test func inFlightPrepareCannotWriteFilesAfterClearAllDrafts() async throws {
        let dir = FileManager.default.temporaryDirectory.appending(path: "att-clear-\(UUID().uuidString)", directoryHint: .isDirectory)
        let vault = Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")
        defer {
            try? FileManager.default.removeItem(at: dir)
            try? vault.clear()
        }

        let store = PendingStore(directory: dir, vault: vault)
        try await withHarness(store: store, persist: true) { h in
            let srcRef = try store.storeBlob(samplePNG(.systemOrange), prefix: "src")
            await h.host.clearAllDrafts()
            #expect(!FileManager.default.fileExists(atPath: dir.path))

            // Late prepare effect executing on the pre-logout host's stale PendingScope must write nothing to disk.
            await h.host.processEffectBatchForTesting([
                .with {
                    $0.attachmentPrepareImage = .with {
                        $0.opID = 88
                        $0.scope = .session("s1")
                        $0.itemID = "late-1"
                        $0.sourceRef = srcRef
                        $0.original = false
                        $0.maxOriginalBytes = 20 * 1024 * 1024
                        $0.maxDimensionPx = 2048
                        $0.maxCompressedBytes = 900 * 1024
                    }
                },
            ])
            await h.host.drainAttachmentWorkForTesting()
            #expect(!FileManager.default.fileExists(atPath: dir.path))
        }
    }

    @Test func hashFileBookmarkProbesVideoDurationWithHasDurationFlag() async throws {
        let dir = FileManager.default.temporaryDirectory.appending(path: "att-vid-\(UUID().uuidString)", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }

        let videoURL = dir.appending(path: "sample.mov")
        let writer = try AVAssetWriter(outputURL: videoURL, fileType: .mov)
        let settings: [String: Any] = [
            AVVideoCodecKey: AVVideoCodecType.h264,
            AVVideoWidthKey: 32,
            AVVideoHeightKey: 32,
        ]
        let input = AVAssetWriterInput(mediaType: .video, outputSettings: settings)
        let adaptor = AVAssetWriterInputPixelBufferAdaptor(
            assetWriterInput: input,
            sourcePixelBufferAttributes: [
                kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32ARGB,
                kCVPixelBufferWidthKey as String: 32,
                kCVPixelBufferHeightKey as String: 32,
            ]
        )
        writer.add(input)
        #expect(writer.startWriting())
        writer.startSession(atSourceTime: .zero)
        var pb: CVPixelBuffer?
        CVPixelBufferCreate(kCFAllocatorDefault, 32, 32, kCVPixelFormatType_32ARGB, nil, &pb)
        let buffer = try #require(pb)
        let writerQueue = DispatchQueue(label: "att-vid-test-writer")
        let frames: [CMTime] = [.zero, CMTime(value: 1, timescale: 2)]
        final class FrameFeed: @unchecked Sendable {
            var index = 0
            var appended = 0
            var finished = false
        }
        let feed = FrameFeed()
        nonisolated(unsafe) let uInput = input
        nonisolated(unsafe) let uAdaptor = adaptor
        nonisolated(unsafe) let uBuffer = buffer
        await withCheckedContinuation { (done: CheckedContinuation<Void, Never>) in
            uInput.requestMediaDataWhenReady(on: writerQueue) {
                while uInput.isReadyForMoreMediaData && feed.index < frames.count {
                    if uAdaptor.append(uBuffer, withPresentationTime: frames[feed.index]) {
                        feed.appended += 1
                    }
                    feed.index += 1
                }
                if feed.index >= frames.count && !feed.finished {
                    feed.finished = true
                    uInput.markAsFinished()
                    done.resume()
                }
            }
        }
        #expect(feed.appended == frames.count)
        await writer.finishWriting()
        #expect(writer.status == .completed)

        let store = PendingStore.ephemeral()
        let bmk = try PickedFile.bookmark(for: videoURL)
        let bmkRef = try store.storeBookmark(bmk)
        try await withHarness(store: store, persist: false) { h in
            let res = await h.host.hashFileForTesting(.with {
                $0.opID = 91
                $0.scope = .session("s1")
                $0.itemID = "vid-1"
                $0.sourceRef = bmkRef
                $0.maxBytes = 100 * 1024 * 1024
                $0.isVideo = true
            })
            #expect(res.ok)
            #expect(res.sizeBytes > 0 && res.sha256.count == 32)
            #expect(res.hasDuration_p)
            #expect(res.durationMs > 0)
        }
    }

    @Test func localImageCacheRespectsLRUBound() async throws {
        let store = PendingStore.ephemeral()
        try await withHarness(store: store, persist: false) { h in
            let png = samplePNG(.systemPurple)
            var refs: [String] = []
            for i in 0..<(ImageCenter.maxLocalImages + 6) {
                refs.append(try store.storeBlob(png, prefix: "t\(i)"))
            }
            for ref in refs {
                _ = h.center.localImage(for: ref)
            }
            await h.center.drainLocalImageLoadsForTesting()
            #expect(h.center.localImageCacheCountForTesting == ImageCenter.maxLocalImages)
        }
    }

    @Test func localImageNegativeCachesDecodeFailuresAndBoundsBlobSize() async throws {
        let store = PendingStore.ephemeral()
        try await withHarness(store: store, persist: false) { h in
            let badRef = try store.storeBlob(Data("not-an-image".utf8), prefix: "bad")
            let hugeRef = try store.storeBlob(Data(count: Int(ImageCenter.maxThumbnailBlobBytes) + 1), prefix: "huge")

            #expect(h.center.localImage(for: badRef) == nil)
            await h.center.drainLocalImageLoadsForTesting()
            #expect(h.center.failedLocalRefCountForTesting == 1)
            let loadsAfterBad = h.center.localImageLoadCountForTesting
            for _ in 0..<5 {
                #expect(h.center.localImage(for: badRef) == nil)
            }
            #expect(h.center.localImageLoadCountForTesting == loadsAfterBad)

            #expect(h.center.localImage(for: hugeRef) == nil)
            await h.center.drainLocalImageLoadsForTesting()
            #expect(h.center.failedLocalRefCountForTesting == 2)
            let loadsAfterHuge = h.center.localImageLoadCountForTesting
            #expect(h.center.localImage(for: hugeRef) == nil)
            #expect(h.center.localImageLoadCountForTesting == loadsAfterHuge)

            // Evicting a ref clears its negative-cache entry.
            h.center.handlers().onReleaseLocalRefs([badRef])
            #expect(h.center.failedLocalRefCountForTesting == 1)
        }
    }

    @Test func pastedOversizedImageRefusedBeforeLoadingIntoMemory() async throws {
        let tmpDir = FileManager.default.temporaryDirectory.appending(path: "att-paste-\(UUID().uuidString)", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: tmpDir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: tmpDir) }

        let hugeURL = tmpDir.appending(path: "huge.tiff")
        FileManager.default.createFile(atPath: hugeURL.path, contents: nil)
        let fh = try FileHandle(forWritingTo: hugeURL)
        try fh.truncate(atOffset: UInt64(pickedReadLimit(original: false, originalMaxBytes: 0)) + 1)
        try fh.close()

        let hugeProvider = NSItemProvider()
        hugeProvider.registerFileRepresentation(forTypeIdentifier: "public.image", fileOptions: [], visibility: .all) { handler in
            handler(hugeURL, false, nil)
            return nil
        }
        await #expect(throws: ImageTooLarge.self) {
            _ = try await ImageCenter.loadBoundedImageData(from: hugeProvider)
        }

        let smallURL = tmpDir.appending(path: "small.png")
        let png = samplePNG(.systemIndigo)
        try png.write(to: smallURL)
        let smallProvider = NSItemProvider()
        smallProvider.registerFileRepresentation(forTypeIdentifier: "public.image", fileOptions: [], visibility: .all) { handler in
            handler(smallURL, false, nil)
            return nil
        }
        let loaded = try await ImageCenter.loadBoundedImageData(from: smallProvider)
        #expect(loaded == png)

        // Concrete UTI registered only as data representation (as UIPasteboard does for JPEG/PNG):
        // must succeed without failing on abstract `public.image`.
        let concreteDataProvider = NSItemProvider()
        concreteDataProvider.registerDataRepresentation(forTypeIdentifier: UTType.png.identifier, visibility: .all) { handler in
            handler(png, nil)
            return nil
        }
        let concreteLoaded = try await ImageCenter.loadBoundedImageData(from: concreteDataProvider)
        #expect(concreteLoaded == png)

        // Real UIPasteboard item with concrete JPEG data (as copied from Photos/Safari/pbcopy).
        let pb = UIPasteboard.withUniqueName()
        pb.setData(png, forPasteboardType: UTType.png.identifier)
        let pbProvider = try #require(pb.itemProviders.first)
        let pbLoaded = try await ImageCenter.loadBoundedImageData(from: pbProvider)
        #expect(pbLoaded == png)

        // Concrete UTI oversized file must still throw ImageTooLarge without falling back to data load.
        let concreteHuge = NSItemProvider()
        concreteHuge.registerFileRepresentation(forTypeIdentifier: UTType.jpeg.identifier, fileOptions: [], visibility: .all) { handler in
            handler(hugeURL, false, nil)
            return nil
        }
        await #expect(throws: ImageTooLarge.self) {
            _ = try await ImageCenter.loadBoundedImageData(from: concreteHuge)
        }
    }

    @Test func draftTextRelayDeliversEditsAndSubmitInStrictFIFOOrder() async {
        final class Recorder: @unchecked Sendable {
            private let lock = NSLock()
            var events: [String] = []
            func append(_ s: String) { lock.withLock { events.append(s) } }
        }
        let recorder = Recorder()
        let firstStarted = AsyncStream<Void>.makeStream()
        let releaseFirst = AsyncStream<Void>.makeStream()
        let relay = DraftTextRelay()

        relay.enqueue {
            firstStarted.continuation.yield(())
            var it = releaseFirst.stream.makeAsyncIterator()
            _ = await it.next()
            recorder.append("edit-1")
        }
        relay.enqueue { recorder.append("edit-2") }
        relay.enqueue { recorder.append("edit-3") }
        relay.flushAndRun { recorder.append("submit") }

        #expect(relay.pendingCount == 3)
        _ = await nextSignal(firstStarted.stream)
        // Even while edit-1 is suspended, edit-2/edit-3/submit must not jump ahead.
        #expect(recorder.events.isEmpty)
        releaseFirst.continuation.yield(())
        await relay.flush()
        #expect(relay.pendingCount == 0)
        #expect(recorder.events == ["edit-1", "edit-2", "edit-3", "submit"])
    }

    @Test func oversizedLibraryVideoSkipsCopyAndIsRejectedByCore() async throws {
        let store = PendingStore.ephemeral()
        try await withHarness(store: store, persist: false) { h in
            let scope = PBDraftScope.session("s1")
            let maxBytes = await h.host.maxFileBytes(for: scope)
            #expect(maxBytes == 100 * 1024 * 1024)

            let tmpDir = FileManager.default.temporaryDirectory.appending(path: "att-bigvid-\(UUID().uuidString)", directoryHint: .isDirectory)
            let stagingRoot = tmpDir.appending(path: "library-videos", directoryHint: .isDirectory)
            try FileManager.default.createDirectory(at: tmpDir, withIntermediateDirectories: true)
            defer { try? FileManager.default.removeItem(at: tmpDir) }

            let hugeFile = tmpDir.appending(path: "huge.mp4")
            FileManager.default.createFile(atPath: hugeFile.path, contents: nil)
            let fh = try FileHandle(forWritingTo: hugeFile)
            try fh.truncate(atOffset: UInt64(maxBytes) + 1)
            try fh.close()

            let staged = try LibraryVideos.stage(hugeFile, maxBytes: maxBytes, root: stagingRoot)
            guard case .rejectedCandidate(let info) = staged else {
                Issue.record("expected oversized video to skip staging copy")
                return
            }
            #expect(!FileManager.default.fileExists(atPath: stagingRoot.path))
            #expect(store.used == 0)

            await h.host.openDraft(scope: scope, machineID: "m1")
            await h.host.addDraftItems(scope: scope, items: [.with {
                $0.candidate = info.candidate(forceFile: false)
                $0.sourceRef = ""
            }])
            await h.host.drainAttachmentWorkForTesting()
            let d = h.screens.sessions["s1"]?.composer.draft
            #expect(d?.files.isEmpty == true)
            #expect(d?.hasNotice == true)
            #expect(!(d?.notice.rendered().isEmpty ?? true))

            // Startup cleanup removes any residual staged library videos.
            let leftover = try LibraryVideos.copy(hugeFile, root: stagingRoot)
            #expect(FileManager.default.fileExists(atPath: leftover.path))
            LibraryVideos.discardAll(root: stagingRoot)
            #expect(!FileManager.default.fileExists(atPath: stagingRoot.path))
        }
    }

    @Test func draftStorageQueueDrainWaitsForFollowUpOpsEnqueuedDuringExecution() async {
        final class Recorder: @unchecked Sendable {
            private let lock = NSLock()
            private var _steps: [String] = []
            var steps: [String] { lock.withLock { _steps } }
            func append(_ step: String) { lock.withLock { _steps.append(step) } }
        }

        let queue = DraftStorageQueue()
        let recorder = Recorder()
        let firstStarted = AsyncStream<Void>.makeStream()
        let releaseFirst = AsyncStream<Void>.makeStream()

        queue.enqueue {
            firstStarted.continuation.yield(())
            var it = releaseFirst.stream.makeAsyncIterator()
            _ = await it.next()
            recorder.append("first")
            // Mirrors a storage callback (e.g., draftStorageWriteResult with dirtyWrite)
            // synchronously enqueuing a follow-up write after drain() was already called.
            queue.enqueue {
                recorder.append("follow-up")
            }
        }

        _ = await nextSignal(firstStarted.stream)
        let drainTask = Task { await queue.drain() }
        releaseFirst.continuation.yield(())
        await drainTask.value
        #expect(recorder.steps == ["first", "follow-up"])
    }

    @Test func draftStorageEffectsEnqueueInBatchOrderAcrossActorReentrancy() async throws {
        let dir = FileManager.default.temporaryDirectory.appending(path: "att-order-\(UUID().uuidString)", directoryHint: .isDirectory)
        let vault = Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")
        defer {
            try? FileManager.default.removeItem(at: dir)
            try? vault.clear()
        }

        let store = PendingStore(directory: dir, vault: vault)
        let queue = DraftStorageQueue()
        let account = Account(
            server: "http://127.0.0.1:1",
            deviceId: "00112233445566778899aabbccddeeff",
            accountId: "11111111111111111111111111111111"
        )
        let remote = try Remote(account: account, http: HTTP(), allowPlainHttp: true)
        let navEntered = AsyncStream<Void>.makeStream()
        let resumeBatch1 = AsyncStream<Void>.makeStream()

        let host = try CoreHost.live(
            remote: remote,
            account: account,
            defaults: UserDefaults(suiteName: "att-order-\(UUID().uuidString)")!,
            screens: CoreScreens(),
            pendingStore: store.scoped(),
            draftStorageQueue: queue,
            demoMode: true,
            onNavigate: { _ in }
        )
        await host.setNavigateSuspendHookForTesting {
            navEntered.continuation.yield(())
            var it = resumeBatch1.stream.makeAsyncIterator()
            _ = await it.next()
        }

        // Seed an initial record so Batch 1's Delete removes stale data.
        #expect(store.write(opID: 1, bucket: .draft, key: "draft:session:s1", data: Data("stale".utf8)).ok)

        // Batch 1 suspends at .navigate BEFORE .draftStorageDelete.
        let batch1: [PBCoreEffect] = [
            .with { $0.navigate = .with { $0.route = .session; $0.sessionID = "s1" } },
            .with { $0.draftStorageDelete = .with { $0.bucket = .draft; $0.key = "draft:session:s1" } },
        ]
        let batch2: [PBCoreEffect] = [
            .with {
                $0.draftStorageWrite = .with {
                    $0.opID = 2
                    $0.bucket = .draft
                    $0.key = "draft:session:s1"
                    $0.data = Data("fresh-second-batch".utf8)
                }
            },
        ]

        // Start Batch 1, wait until suspended at .navigate, run Batch 2, then resume Batch 1.
        // Because Batch 1 synchronously enqueued Delete before awaiting .navigate,
        // Delete runs BEFORE Batch 2's Write and the fresh data survives!
        let task1 = Task { await host.processEffectBatchForTesting(batch1) }
        _ = await nextSignal(navEntered.stream)
        await host.processEffectBatchForTesting(batch2)
        resumeBatch1.continuation.yield(())
        await task1.value
        await host.drainDraftStorageQueue()

        let read = store.read(opID: 3, bucket: .draft, key: "draft:session:s1")
        #expect(read.found && read.data == Data("fresh-second-batch".utf8))
        await host.closeAsync()
    }

    @Test func sharedDraftQueueSerializesLogoutClearAllBeforeNextHostList() async throws {
        let dir = FileManager.default.temporaryDirectory.appending(path: "att-cross-\(UUID().uuidString)", directoryHint: .isDirectory)
        let vault = Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")
        defer {
            try? FileManager.default.removeItem(at: dir)
            try? vault.clear()
        }

        let store = PendingStore(directory: dir, vault: vault)
        let sharedQueue = DraftStorageQueue()
        #expect(store.write(opID: 1, bucket: .draft, key: "draft:session:s1", data: Data("old-account".utf8)).ok)

        let makeHost: () throws -> CoreHost = {
            let account = Account(
                server: "http://127.0.0.1:1",
                deviceId: "00112233445566778899aabbccddeeff",
                accountId: "11111111111111111111111111111111"
            )
            let remote = try Remote(account: account, http: HTTP(), allowPlainHttp: true)
            return try CoreHost.live(
                remote: remote,
                account: account,
                defaults: UserDefaults(suiteName: "att-cross-\(UUID().uuidString)")!,
                screens: CoreScreens(),
                pendingStore: store.scoped(),
                draftStorageQueue: sharedQueue,
                demoMode: true,
                onNavigate: { _ in }
            )
        }

        let host1 = try makeHost()
        let host2 = try makeHost()

        // Hold queue with a blocker so host1's ClearAll queues behind it.
        let gateStarted = AsyncStream<Void>.makeStream()
        let gateRelease = AsyncStream<Void>.makeStream()
        sharedQueue.enqueue {
            gateStarted.continuation.yield(())
            var it = gateRelease.stream.makeAsyncIterator()
            _ = await it.next()
        }
        _ = await nextSignal(gateStarted.stream)

        await host1.dispatch(PBCoreEvent.with { $0.draftClearAll = PBDraftClearAllAction() })
        await host1.closeAsync()

        let listCount = AsyncStream<Int>.makeStream()
        sharedQueue.enqueue {
            listCount.continuation.yield(store.list(opID: 99).entries.count)
        }

        gateRelease.continuation.yield(())
        await host2.drainDraftStorageQueue()
        let count = await nextSignal(listCount.stream)
        #expect(count == 0)
        await host2.closeAsync()
    }

    @Test func draftViewsRenderFromCoreViewModelsWithoutState() async throws {
        let store = PendingStore.ephemeral()
        let account = Account(
            server: "http://127.0.0.1:1",
            deviceId: "00112233445566778899aabbccddeeff",
            accountId: "11111111111111111111111111111111"
        )
        let remote = try Remote(account: account, http: HTTP(), allowPlainHttp: true)
        let host = try CoreHost.live(
            remote: remote,
            account: account,
            defaults: UserDefaults(suiteName: "att-render-\(UUID().uuidString)")!,
            screens: CoreScreens(),
            pendingStore: store.scoped(),
            demoMode: true,
            onNavigate: { _ in }
        )
        let center = ImageCenter.install(remote: remote, coreHost: host, pending: store.scoped())
        let thumbRef = try store.storeBlob(samplePNG(.systemGreen), prefix: "thumb")

        let draft = PBAttachmentDraftView.with {
            $0.scope = .session("s1")
            $0.original = true
            $0.canToggleOriginal = true
            $0.originalTotalSizeText = .with { $0.literal = " · 共 4.2 MB" }
            $0.imageCount = 1
            $0.maxImages = 6
            $0.imageCountText = .with { $0.literal = "1 / 6" }
            $0.fileCount = 1
            $0.maxFiles = 5
            $0.fileCountText = .with { $0.literal = "1 / 5" }
            $0.images = [
                .with {
                    $0.itemID = "img-1"
                    $0.kind = .image
                    $0.state = .ready
                    $0.thumbRef = thumbRef
                    $0.canRemove = true
                    $0.positionText = .with { $0.literal = "1 / 3" }
                    $0.positionA11Y = .with { $0.literal = "第 1 张，共 3 张" }
                    $0.removeA11Y = .with { $0.literal = "移除第 1 张图片" }
                },
                .with {
                    $0.itemID = "img-2"
                    $0.kind = .image
                    $0.state = .paused
                    $0.thumbRef = thumbRef
                    $0.canResume = true
                    $0.canRemove = true
                    $0.badgeText = .with { $0.literal = "继续" }
                    $0.positionText = .with { $0.literal = "2 / 3" }
                    $0.positionA11Y = .with { $0.literal = "第 2 张，共 3 张" }
                    $0.removeA11Y = .with { $0.literal = "移除第 2 张图片" }
                },
                .with {
                    $0.itemID = "img-3"
                    $0.kind = .image
                    $0.state = .failed
                    $0.thumbRef = thumbRef
                    $0.canRetry = true
                    $0.canRemove = true
                    $0.badgeText = .with { $0.literal = "失败" }
                    $0.positionText = .with { $0.literal = "3 / 3" }
                    $0.positionA11Y = .with { $0.literal = "第 3 张，共 3 张" }
                    $0.removeA11Y = .with { $0.literal = "移除第 3 张图片" }
                },
            ]
            $0.files = [.with {
                $0.itemID = "file-1"
                $0.kind = .file
                $0.state = .uploading
                $0.progress = 0.5
                $0.displayName = "demo.mp4"
                $0.sizeText = .with { $0.literal = "12.4 MB" }
                $0.durationText = "0:15"
                $0.statusText = .with { $0.literal = "50%" }
                $0.canPause = true
                $0.canRemove = true
            }]
        }
        let strip = ImageRenderer(content: ImageStrip(center: center, host: host, scope: .session("s1"), draft: draft).frame(width: 360))
        #expect(strip.uiImage != nil)

        let outbox = OutboxAttachmentPreviews(center: center, attachments: [
            .with { $0.itemID = "o1"; $0.kind = .image; $0.thumbRef = thumbRef },
            .with { $0.itemID = "o2"; $0.kind = .file; $0.displayName = "spec.pdf"; $0.sizeText = .with { $0.literal = "1.2 MB" } },
        ])
        let outboxImg = ImageRenderer(content: outbox.frame(width: 320))
        #expect(outboxImg.uiImage != nil)
        await host.closeAsync()
        ImageCenter.remove(remote)
    }
}
