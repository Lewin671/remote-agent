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
    await withTaskGroup(of: T?.self) { group in
        group.addTask { var it = stream.makeAsyncIterator(); return await it.next() }
        group.addTask { try? await Task.sleep(for: .seconds(5)); return nil }
        defer { group.cancelAll() }
        return await group.next() ?? nil
    }
}

/// In-process upload responder for real-Core attachment smoke tests.
private final class FakeUploadDaemon: @unchecked Sendable {
    private let lock = NSLock()
    private var transfers: [String: (machine: String, total: Int64, received: Data)] = [:]
    var sentCommands: [(machine: String, send: PBSendRequest)] = []

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
                sentCommands.append((effect.machineID, req))
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

        var state = RemoteState()
        state.conn = .online
        state.link = .online
        state.machines["m1"] = MachineView(
            id: "m1", online: true, lastSeenMs: nowMs(),
            meta: .with {
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
            },
            metaRev: 1
        )
        state.sessions["s1"] = SessionView(
            id: "s1", machineId: "m1",
            info: .with {
                $0.id = "s1"
                $0.cwd = "/Users/me/proj"
                $0.title = "Draft Smoke"
                $0.status = .idle
                $0.agent = .codex
            },
            metaRev: 1, updatedMs: nowMs()
        )
        await host.seedDemo(state: state)
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

    @Test func realCorePrepareUploadSendAndRestartPersistenceSmoke() async throws {
        let dir = FileManager.default.temporaryDirectory.appending(path: "att-smoke-\(UUID().uuidString)", directoryHint: .isDirectory)
        let vault = Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")
        defer {
            try? FileManager.default.removeItem(at: dir)
            try? vault.clear()
        }

        let daemon = FakeUploadDaemon()
        let store1 = PendingStore(directory: dir, vault: vault)
        let h1 = try await makeHarness(store: store1, daemon: daemon, persist: true)

        let scope = PBDraftScope.session("s1")
        await h1.host.openDraft(scope: scope, machineID: "m1")
        await h1.host.setDraftText(scope: scope, text: "persisted text")
        h1.center.addImageData(scope: scope, [samplePNG()])

        await eventually("image prepared and uploaded via Core") {
            let d = h1.screens.sessions["s1"]?.composer.draft
            return d?.text == "persisted text" && d?.images.count == 1 && d?.images.first?.state == .ready
        }
        await h1.host.drainDraftStorageQueue()

        // Close host 1 and start host 2 on the same encrypted PendingStore -> draft restores across restart.
        await h1.host.closeAsync()
        ImageCenter.remove(h1.remote)

        let store2 = PendingStore(directory: dir, vault: vault)
        let h2 = try await makeHarness(store: store2, daemon: daemon, persist: true)
        defer {
            Task {
                await h2.host.closeAsync()
                await MainActor.run { ImageCenter.remove(h2.remote) }
            }
        }
        await h2.host.openDraft(scope: scope, machineID: "m1")
        await eventually("draft text and image restored after restart") {
            let d = h2.screens.sessions["s1"]?.composer.draft
            return d?.text == "persisted text" && d?.images.count == 1 && d?.images.first?.state == .ready
        }

        // Send via Core -> DraftSendSessionAction clears draft and dispatches SendInput with image_ids.
        await h2.host.sendSessionDraft(sessionID: "s1", mode: .steer)
        await eventually("send dispatched with uploaded image id and draft cleared") {
            let d = h2.screens.sessions["s1"]?.composer.draft
            return d?.text.isEmpty == true && d?.images.isEmpty == true && daemon.sentCommands.count == 1
        }
        #expect(daemon.sentCommands.first?.send.imageIds.count == 1)
    }

    @Test func originalToggleAndOriginalTotalSizeWorkWithRealCore() async throws {
        let store = PendingStore.ephemeral()
        let daemon = FakeUploadDaemon()
        let h = try await makeHarness(store: store, daemon: daemon, persist: false)
        defer {
            Task {
                await h.host.closeAsync()
                await MainActor.run { ImageCenter.remove(h.remote) }
            }
        }

        let scope = PBDraftScope.session("s1")
        await h.host.openDraft(scope: scope, machineID: "m1")
        h.center.addImageData(scope: scope, [samplePNG(.systemRed)])

        await eventually("picked image has original source enabled and becomes ready") {
            let d = h.screens.sessions["s1"]?.composer.draft
            return d?.images.count == 1 && d?.images.first?.state == .ready && d?.canToggleOriginal == true
        }

        await h.host.setDraftOriginal(scope: scope, original: true)
        await eventually("original toggle prepares original image and renders total size") {
            let d = h.screens.sessions["s1"]?.composer.draft
            return d?.original == true
                && d?.images.first?.state == .ready
                && d?.hasOriginalTotalSizeText == true
                && !(d?.originalTotalSizeText.rendered().isEmpty ?? true)
                && d?.canSend == true
        }
    }

    @Test func readChunkNeverFallsBackToRawSourceRefForImages() async throws {
        let store = PendingStore.ephemeral()
        let rawSourceRef = try store.storeBlob(Data("raw-exif-bytes".utf8), prefix: "src")
        let h = try await makeHarness(store: store, persist: false)
        defer {
            Task {
                await h.host.closeAsync()
                await MainActor.run { ImageCenter.remove(h.remote) }
            }
        }

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
    }

    @Test func inFlightPrepareCannotWriteFilesAfterClearAllDrafts() async throws {
        let dir = FileManager.default.temporaryDirectory.appending(path: "att-clear-\(UUID().uuidString)", directoryHint: .isDirectory)
        let vault = Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")
        defer {
            try? FileManager.default.removeItem(at: dir)
            try? vault.clear()
        }

        let store = PendingStore(directory: dir, vault: vault)
        let h = try await makeHarness(store: store, persist: true)
        defer {
            Task {
                await h.host.closeAsync()
                await MainActor.run { ImageCenter.remove(h.remote) }
            }
        }

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
        await h.host.drainDraftStorageQueue()
        #expect(!FileManager.default.fileExists(atPath: dir.path))
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
        while !input.isReadyForMoreMediaData { try await Task.sleep(for: .milliseconds(5)) }
        #expect(adaptor.append(buffer, withPresentationTime: .zero))
        while !input.isReadyForMoreMediaData { try await Task.sleep(for: .milliseconds(5)) }
        #expect(adaptor.append(buffer, withPresentationTime: CMTime(value: 1, timescale: 2)))
        input.markAsFinished()
        await writer.finishWriting()
        #expect(writer.status == .completed)

        let store = PendingStore.ephemeral()
        let bmk = try PickedFile.bookmark(for: videoURL)
        let bmkRef = try store.storeBookmark(bmk)
        let h = try await makeHarness(store: store, persist: false)
        defer {
            Task {
                await h.host.closeAsync()
                await MainActor.run { ImageCenter.remove(h.remote) }
            }
        }

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

    @Test func localImageCacheRespectsLRUBound() async throws {
        let store = PendingStore.ephemeral()
        let h = try await makeHarness(store: store, persist: false)
        defer {
            Task {
                await h.host.closeAsync()
                await MainActor.run { ImageCenter.remove(h.remote) }
            }
        }

        let png = samplePNG(.systemPurple)
        var refs: [String] = []
        for i in 0..<(ImageCenter.maxLocalImages + 6) {
            refs.append(try store.storeBlob(png, prefix: "t\(i)"))
        }
        for ref in refs {
            _ = h.center.localImage(for: ref)
        }
        await eventually("LRU thumbnail cache populated up to cap") {
            h.center.localImageCacheCountForTesting == ImageCenter.maxLocalImages
        }
        #expect(h.center.localImageCacheCountForTesting <= ImageCenter.maxLocalImages)
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
        defer { Task { await host.closeAsync() } }
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
        defer {
            Task {
                await host1.closeAsync()
                await host2.closeAsync()
            }
        }

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
    }

    @Test func draftViewsRenderFromCoreViewModelsWithoutState() throws {
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
        defer {
            Task { await host.closeAsync() }
            ImageCenter.remove(remote)
        }
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
    }
}
