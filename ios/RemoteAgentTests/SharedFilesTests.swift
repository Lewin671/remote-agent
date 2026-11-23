import CryptoKit
import Foundation
import RACore
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

/// In-process chunk responder backing `CoreHost.setDemoCommandResponder` for real-Core smoke tests.
private final class FakeChunkServer: @unchecked Sendable {
    let content: Data
    let blockBytes: Int
    private let lock = NSLock()
    private var calls = 0
    private var holdAt: Int?
    private var gate: CheckedContinuation<Void, Never>?
    private let held = AsyncStream<Bool>.makeStream()

    init(_ content: Data, blockBytes: Int = 1000) {
        self.content = content
        self.blockBytes = blockBytes
    }

    var count: Int { lock.withLock { calls } }
    func hold(at call: Int) { lock.withLock { holdAt = call } }
    func waitUntilHeld() async -> Bool { await nextEvent(held.stream) == true }
    func release() {
        lock.withLock { () -> CheckedContinuation<Void, Never>? in defer { gate = nil }; return gate }?.resume()
    }

    func action(for effect: PBCoreDispatchCommandEffect, request: PBGetSharedFileChunkRequest) -> CoreHost.DemoCommandAction {
        .asyncRespond { [self] in
            let n = lock.withLock { () -> Int in calls += 1; return calls - 1 }
            if lock.withLock({ holdAt }) == n {
                await withCheckedContinuation { c in
                    lock.withLock { gate = c }
                    held.continuation.yield(true)
                }
            }
            var block = Data(count: blockBytes)
            let from = Int(request.offset)
            let take = max(0, min(blockBytes, content.count - from))
            if take > 0 { block.replaceSubrange(0..<take, with: content[from..<from + take]) }
            return CoreHost.demoExecutedOutcome(for: effect) {
                $0.getSharedFileChunk = .with {
                    $0.data = block
                    $0.totalBytes = UInt64(content.count)
                }
            }
        }
    }
}

private func nextEvent<T: Sendable>(_ events: AsyncStream<T>) async -> T? {
    await withTaskGroup(of: T?.self) { group in
        group.addTask { var iterator = events.makeAsyncIterator(); return await iterator.next() }
        group.addTask { try? await Task.sleep(for: .seconds(5)); return nil }
        defer { group.cancelAll() }
        return await group.next() ?? nil
    }
}

@MainActor @Suite struct SharedFilesTests {
    private let content = Data((0..<2500).map { UInt8($0 % 251) })
    private let key = SharedFileKey(sessionId: "s1", fileId: "f1")

    @MainActor final class Opened {
        var urls: [URL] = []
        private let signal = AsyncStream<URL>.makeStream()
        func record(_ url: URL) { urls.append(url); signal.continuation.yield(url) }
        func next() async -> URL? { await nextEvent(signal.stream) }
    }

    private struct Harness {
        let host: CoreHost
        let screens: CoreScreens
        let center: SharedFileCenter
        let store: SharedFileStore
        let root: URL
        let opened: Opened

        @MainActor func item(sessionID: String = "s1", fileID: String = "f1") -> PBSessionSharedFileItem? {
            guard let items = screens.sessions[sessionID]?.timeline.items else { return nil }
            for row in items {
                if case .sharedFiles(let sf)? = row.kind,
                   let match = sf.files.first(where: { $0.fileID == fileID })
                {
                    return match
                }
            }
            return nil
        }

        func tearDown() async {
            await MainActor.run { center.close() }
            await host.closeAsync()
            try? FileManager.default.removeItem(at: root)
        }
    }

    private func makeHarness(
        content: Data,
        fileName: String = "cover.png",
        server: FakeChunkServer,
        root existingRoot: URL? = nil,
        scanLocal: Bool = true
    ) async throws -> Harness {
        let root = existingRoot ?? FileManager.default.temporaryDirectory.appendingPathComponent("sf-smoke-\(UUID().uuidString)")
        let account = Account(
            server: "http://127.0.0.1:1",
            deviceId: "00112233445566778899aabbccddeeff",
            accountId: "11111111111111111111111111111111"
        )
        let remote = try Remote(account: account, http: HTTP(), allowPlainHttp: true)
        let screens = CoreScreens()
        let store = SharedFileStore(root: root)
        let host = try CoreHost.live(
            remote: remote,
            account: account,
            defaults: UserDefaults(suiteName: "sf-test-\(UUID().uuidString)")!,
            screens: screens,
            sharedFileStore: store,
            demoMode: true,
            onNavigate: { _ in }
        )
        let opened = Opened()
        let center = SharedFileCenter(store: store, coreHost: host, allowMissingReports: true, open: { opened.record($0) })
        await host.setSharedFileHandlers(center.handlers())
        await host.setDemoCommandResponder { eff, cmd in
            if case .getSharedFileChunk(let req)? = cmd.kind {
                return server.action(for: eff, request: req)
            }
            return nil
        }

        var state = RemoteState()
        state.conn = .online
        state.link = .online
        state.machines["m1"] = MachineView(
            id: "m1", online: true, lastSeenMs: nowMs(),
            meta: .with {
                $0.hostname = "MacBook"
                $0.home = "/Users/me"
                $0.os = "darwin"
                $0.protocolVersion = peerProtocolVersion
                $0.capabilities = .with {
                    $0.sharedFiles = true
                    $0.sharedFileChunkBytes = Int32(server.blockBytes)
                    $0.maxSharedFileBytes = 104_857_600
                }
            },
            metaRev: 1
        )
        state.sessions["s1"] = SessionView(
            id: "s1", machineId: "m1",
            info: .with {
                $0.id = "s1"
                $0.cwd = "/Users/me/proj"
                $0.title = "Files"
                $0.status = .idle
                $0.agent = .codex
            },
            metaRev: 1, updatedMs: nowMs()
        )
        let ev = PBEvent.with {
            $0.seq = 1
            $0.toolCall = .with {
                $0.title = "send_file"
                $0.callID = "send-f1"
                $0.kind = .other
                $0.status = "completed"
                $0.itemType = "remote_agent/send_file"
                $0.sharedFiles = [.with {
                    $0.fileID = "f1"
                    $0.name = fileName
                    $0.sizeBytes = Int64(content.count)
                    $0.sha256 = Data(SHA256.hash(data: content))
                    $0.path = "/Users/me/\(fileName)"
                }]
            }
        }
        await host.seedDemo(state: state, events: ["s1": [ev]])
        if scanLocal {
            await host.dispatch(PBCoreEvent.with {
                $0.sharedFileConfig = .with {
                    $0.localCacheMaxBytes = 0
                    $0.storageFullKind = .phoneStorageFull
                    $0.scanLocal = true
                }
            })
            await host.drainSharedFileQueue()
        }
        _ = await host.acquireSessionWatch("s1")
        return Harness(host: host, screens: screens, center: center, store: store, root: root, opened: opened)
    }

    private func cardVM(
        _ content: Data,
        id: String = "f1",
        name: String = "cover.png",
        status: PBSharedFileCardStatus = .available,
        action: PBSharedFilePrimaryAction = .download,
        enabled: Bool = true
    ) -> PBSessionSharedFileItem {
        .with {
            $0.fileID = id
            $0.displayName = name
            $0.nameHead = name
            $0.sizeBytes = Int64(content.count)
            $0.sha256 = Data(SHA256.hash(data: content))
            $0.displayPath = "~/\(name)"
            $0.status = status
            $0.primaryAction = action
            $0.primaryEnabled = enabled
            $0.primaryLabel = .with { $0.literal = "Action" }
            $0.subtitle = .with { $0.literal = "Subtitle" }
        }
    }

    // MARK: Real-Core Smoke Tests

    @Test func coreDrivenDownloadAutoOpensWhenSessionVisibleAndOpensOnLaterTap() async throws {
        let server = FakeChunkServer(content)
        let h = try await makeHarness(content: content, server: server)
        defer { Task { await h.tearDown() } }

        let visibleToken = await h.host.enterVisibleSession("s1")
        let initial = try #require(h.item())
        #expect(initial.status == .available)

        h.center.tap(initial, sessionId: "s1")
        let openedURL = try #require(await h.opened.next())
        #expect(try Data(contentsOf: openedURL) == content)
        #expect(h.opened.urls.count == 1)

        await eventually("downloaded in Core VM") { h.item()?.status == .downloaded }
        let downloaded = try #require(h.item())
        #expect(downloaded.primaryAction == .open)

        // Tapping 打开 later opens the committed copy again.
        h.center.tap(downloaded, sessionId: "s1")
        #expect(h.opened.urls.count == 2)

        // Leaving session clears visible session cleanly.
        await h.host.leaveVisibleSession(visibleToken)
    }

    @Test func leavingVisibleSessionSuppressesAutoOpenOnCompletion() async throws {
        let server = FakeChunkServer(content)
        server.hold(at: 1)
        defer { server.release() }
        let h = try await makeHarness(content: content, server: server)
        defer { Task { await h.tearDown() } }

        let token = await h.host.enterVisibleSession("s1")
        let initial = try #require(h.item())
        h.center.tap(initial, sessionId: "s1")
        try #require(await server.waitUntilHeld())

        // Leave session before download finishes -> Core revokes auto-open ticket.
        await h.host.leaveVisibleSession(token)
        server.release()

        await eventually("downloaded") { h.item()?.status == .downloaded }
        await h.host.drainSharedFileQueue()
        #expect(h.opened.urls.isEmpty)
    }

    @Test func pauseResumeDeleteClearAndMissingCopyWorkThroughCore() async throws {
        let server = FakeChunkServer(content)
        server.hold(at: 1)
        defer { server.release() }
        let h = try await makeHarness(content: content, fileName: "clip.mp4", server: server)
        defer { Task { await h.tearDown() } }

        let initial = try #require(h.item())
        h.center.tap(initial, sessionId: "s1")
        try #require(await server.waitUntilHeld())

        // Pause while chunk 1 is in flight.
        await eventually("downloading") { h.item()?.status == .downloading }
        h.center.pauseDownload(key)
        await eventually("paused") { h.item()?.status == .paused }
        server.release()

        // Resume and finish.
        h.center.startDownload(key, autoOpenIfStaying: false)
        await eventually("downloaded") { h.item()?.status == .downloaded }
        #expect(h.center.usedBytes() >= 2500)

        // Record duration -> persisted in .meta and visible in Core.
        await h.host.recordSharedFileDuration(sessionID: "s1", fileID: "f1", durationMs: 102_000)
        await h.host.drainSharedFileQueue()

        // Simulate OS reclaiming cache file -> opening reports SharedFileLocalMissingEvent -> reverts to available.
        if let local = h.store.existingLocalURL(key, name: "clip.mp4") {
            try FileManager.default.removeItem(at: local)
        }
        h.center.open(key, name: "clip.mp4")
        await eventually("reverted to available after OS purge") { h.item()?.status == .available }
        #expect(h.opened.urls.isEmpty)

        // Download again, then clearAll empties disk and resets state.
        h.center.startDownload(key, autoOpenIfStaying: false)
        await eventually("re-downloaded") { h.item()?.status == .downloaded }
        await h.center.clearAll()
        await eventually("cleared") { h.item()?.status == .available }
        #expect(h.center.usedBytes() == 0)
    }

    @Test func startupScanRestoresCompletedCopyAndDurationAcrossRestart() async throws {
        let sharedRoot = FileManager.default.temporaryDirectory.appendingPathComponent("sf-restart-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: sharedRoot) }

        let server1 = FakeChunkServer(content)
        let h1 = try await makeHarness(content: content, fileName: "clip.mp4", server: server1, root: sharedRoot)
        h1.center.startDownload(key, autoOpenIfStaying: false)
        await eventually("downloaded") { h1.item()?.status == .downloaded }
        await h1.host.recordSharedFileDuration(sessionID: "s1", fileID: "f1", durationMs: 65_000)
        await h1.host.drainSharedFileQueue()
        await h1.host.closeAsync()
        h1.center.close()

        // Second CoreHost on the same root scans local storage and restores DOWNLOADED + duration.
        let server2 = FakeChunkServer(content)
        let h2 = try await makeHarness(content: content, fileName: "clip.mp4", server: server2, root: sharedRoot)
        defer { Task { await h2.host.closeAsync(); await MainActor.run { h2.center.close() } } }
        await eventually("restored downloaded") { h2.item()?.status == .downloaded }
        #expect(server2.count == 0)
    }

    @Test func aThumbnailIsMadeFromTheCopy() async throws {
        let png = UIGraphicsImageRenderer(size: CGSize(width: 40, height: 20)).image { ctx in
            UIColor.red.setFill(); ctx.fill(CGRect(x: 0, y: 0, width: 40, height: 20))
        }.pngData()!
        let server = FakeChunkServer(png)
        let h = try await makeHarness(content: png, fileName: "cover.png", server: server)
        defer { Task { await h.tearDown() } }

        h.center.startDownload(key, autoOpenIfStaying: false)
        await eventually("downloaded") { h.item()?.status == .downloaded }
        await h.center.loadMedia(key, name: "cover.png", kind: .image)
        #expect(h.center.thumbnail(key) != nil)
        let missing = await SharedFileMedia.read(URL(fileURLWithPath: "/nonexistent"), kind: .image)
        #expect(missing.thumbnail == nil)

        // Deleting local copy clears thumbnail and disk.
        h.center.deleteLocal(key)
        await eventually("deleted") { h.item()?.status == .available }
        await h.host.drainSharedFileQueue()
        #expect(h.center.thumbnail(key) == nil)
        #expect(h.center.usedBytes() == 0)
    }

    // MARK: Drawing from Core ViewModel

    private func render<V: View>(_ view: V, size: DynamicTypeSize = .large, scheme: ColorScheme = .light) -> CGSize? {
        let renderer = ImageRenderer(content: view.environment(\.dynamicTypeSize, size).environment(\.colorScheme, scheme).frame(width: 390))
        renderer.scale = 1
        return renderer.uiImage?.size
    }

    @Test func everyStateDrawsA296WideCardAtEveryTypeSize() {
        let cases: [(PBSharedFileCardStatus, PBSharedFilePrimaryAction, Bool, Double)] = [
            (.available, .download, true, 0),
            (.downloading, .pause, true, 0.4),
            (.paused, .resume, true, 0.4),
            (.downloaded, .open, true, 1.0),
            (.failed, .retry, true, 0),
            (.offline, .download, false, 0),
            (.cleaned, .unspecified, false, 0),
        ]
        for (status, action, enabled, progress) in cases {
            var vm = cardVM(content, name: "daocheng-final-vertical-1080-v3.mp4", status: status, action: action, enabled: enabled)
            vm.kind = .video
            vm.nameHead = "daocheng-final-vertical-1080-"
            vm.nameTail = "-v3.mp4"
            vm.progress = progress
            vm.subtitleIsError = status == .failed
            for size in [DynamicTypeSize.large, .xxxLarge, .accessibility3] {
                let drawn = render(SharedFileCardBody(item: vm), size: size)
                #expect(drawn != nil)
                #expect((drawn?.height ?? 0) >= 64, "\(status) \(size)")
            }
            let narrow = ImageRenderer(content: SharedFileCardBody(item: vm).frame(width: 296, alignment: .leading))
            narrow.scale = 1
            #expect(narrow.uiImage?.size.width == 296)
        }
    }

    @Test func theSubtitleOfALargeTypeSizeIsNotCut() {
        var vm = cardVM(content, status: .failed, action: .retry)
        vm.subtitle = .with { $0.literal = "没下载下来，文件校验不一致" }
        vm.subtitleIsError = true
        let small = render(SharedFileCardBody(item: vm, width: 200), size: .large)
        let large = render(SharedFileCardBody(item: vm, width: 200), size: .accessibility3)
        #expect((large?.height ?? 0) > (small?.height ?? 0))
    }
}
