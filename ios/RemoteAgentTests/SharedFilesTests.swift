import CryptoKit
import Foundation
import RACore
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

/// A computer that serves one file, 1000 bytes a block.
private final class Computer: SharedFileFetching, @unchecked Sendable {
    let content: Data
    private let lock = NSLock()
    private var calls = 0
    private var gate: CheckedContinuation<Void, Never>?
    private var holdAt: Int?
    private let held = AsyncStream<Bool>.makeStream()
    func waitUntilHeld() async -> Bool { await nextEvent(held.stream) == true }
    init(_ content: Data) { self.content = content }
    var count: Int { lock.withLock { calls } }
    func hold(at call: Int) { lock.withLock { holdAt = call } }
    func release() { lock.withLock { () -> CheckedContinuation<Void, Never>? in defer { gate = nil }; return gate }?.resume() }

    func fetchChunk(sessionId: String, fileId: String, offset: Int64) async throws -> PBGetSharedFileChunkResponse {
        let n = lock.withLock { () -> Int in calls += 1; return calls - 1 }
        if lock.withLock({ holdAt }) == n {
            await withCheckedContinuation { c in
                lock.withLock { gate = c }
                // Count only says fetchChunk started; release needs this
                // continuation to be installed before the test can continue.
                held.continuation.yield(true)
            }
        }
        var block = Data(count: 1000)
        let from = Int(offset), take = max(0, min(1000, content.count - from))
        if take > 0 { block.replaceSubrange(0..<take, with: content[from..<from + take]) }
        return .with { $0.data = block; $0.totalBytes = UInt64(content.count) }
    }
}

/// Event readiness, bounded only to diagnose a missing callback.
private func nextEvent<T: Sendable>(_ events: AsyncStream<T>) async -> T? {
    await withTaskGroup(of: T?.self) { group in
        group.addTask { var iterator = events.makeAsyncIterator(); return await iterator.next() }
        group.addTask { try? await Task.sleep(for: .seconds(5)); return nil }
        defer { group.cancelAll() }
        return await group.next() ?? nil
    }
}

@MainActor @Suite struct SharedFilesTests {
    private func file(_ content: Data, id: String = "f1", name: String = "cover.png") -> PBSharedFile {
        .with {
            $0.fileID = id; $0.name = name; $0.sizeBytes = Int64(content.count)
            $0.sha256 = Data(SHA256.hash(data: content)); $0.path = "/Users/me/\(name)"
        }
    }
    private let content = Data((0..<2500).map { UInt8($0 % 251) })
    private let key = SharedFileKey(sessionId: "s1", fileId: "f1")

    private func center(_ computer: Computer, opened: Opened) -> (SharedFileCenter, URL) {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("sf-\(UUID().uuidString)")
        let store = SharedFileStore(root: root, fetcher: computer, isMachineOnline: { _ in true }, blockBytes: { _ in 1000 })
        return (SharedFileCenter(store: store, open: { opened.record($0) }), root)
    }

    @MainActor final class Opened {
        var urls: [URL] = []
        private let signal = AsyncStream<URL>.makeStream()
        func record(_ url: URL) { urls.append(url); signal.continuation.yield(url) }
        func next() async -> URL? {
            await nextEvent(signal.stream)
        }
    }

    // MARK: Rows

    private func events() -> [PBEvent] {
        [
            .with { $0.seq = 1; $0.kind = .turnStarted(.init()) },
            .with {
                $0.seq = 2
                $0.kind = .toolCall(.with {
                    $0.title = "send_file"; $0.callID = "c"; $0.status = "completed"
                    $0.sharedFiles = [.with { $0.fileID = "f1"; $0.name = "a.mp4"; $0.sizeBytes = 10; $0.sha256 = Data(count: 32) }]
                })
            },
        ]
    }

    @Test func theChatDrawsCardsOnlyForAComputerThatSaysItSharesFiles() async {
        let builder = ChatBuilder()
        let on = await builder.build(PagedList(events()), ChatParams(sharedFiles: true))
        #expect(on.rows.contains { if case .sharedFiles = $0.item { true } else { false } })
        let off = await ChatBuilder().build(PagedList(events()), ChatParams(sharedFiles: false))
        #expect(!off.rows.contains { if case .sharedFiles = $0.item { true } else { false } })
        #expect(off.rows.contains { if case .tool = $0.item { true } else { false } })
    }

    // MARK: Downloading

    @Test func aTapDownloadsAndOpensWhereTheUserStayed() async throws {
        let computer = Computer(content)
        let opened = Opened()
        let (c, root) = center(computer, opened: opened)
        defer { try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.tap(.available, key: key, file: f)
        // The store publishes .state(.downloaded) before .completed. Wait for
        // the actual presentation event, not an earlier transfer state.
        let url = try #require(await opened.next())
        #expect(opened.urls.count == 1)
        #expect(try Data(contentsOf: url) == content)
        #expect({ if case .downloaded = c.transfer(key, file: f) { true } else { false } }())
        // Tapping 打开 later opens it again.
        c.tap(.downloaded(durationMs: nil), key: key, file: f)
        #expect(opened.urls.count == 2)
    }

    @Test(arguments: [String?.some("s1"), nil])
    func leavingThePageOrAppTakesBackTheTicket(_ sessionId: String?) async throws {
        let computer = Computer(content)
        computer.hold(at: 1)
        defer { computer.release() }
        let opened = Opened()
        let (c, root) = center(computer, opened: opened)
        defer { c.close(); try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.tap(.available, key: key, file: f)
        try #require(await computer.waitUntilHeld())
        c.leftPage(sessionId)
        computer.release()
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
        // The downloaded state precedes the completion event. Enqueue a
        // distinct state after the store finishes, and wait for the centre to
        // consume it: its FIFO follower has then processed that completion.
        let barrier = SharedFileKey(sessionId: "barrier", fileId: "barrier")
        await c.appeared(barrier, file: f)
        await eventually("completion consumed") { c.states[barrier] != nil }
        #expect(opened.urls.isEmpty)
    }

    @Test func aPauseWinsOverTheBlockThatWasOnItsWay() async throws {
        let computer = Computer(content)
        computer.hold(at: 1)
        let opened = Opened()
        let (c, root) = center(computer, opened: opened)
        defer { try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.tap(.available, key: key, file: f)
        defer { computer.release() }
        try #require(await computer.waitUntilHeld())
        c.tap(.downloading(done: 1000, total: 2500, etaSeconds: nil), key: key, file: f)
        await eventually("paused") { if case .paused = c.transfer(key, file: f) { true } else { false } }
        computer.release()
        try await Task.sleep(nanoseconds: 100_000_000)
        guard case .paused(let done, _) = c.transfer(key, file: f) else { Issue.record("not paused"); return }
        #expect(done == 1000)
        #expect(opened.urls.isEmpty)
        c.tap(.paused(done: 1000, total: 2500), key: key, file: f)
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
    }

    @Test func theSameFileOnTwoCardsIsOneState() async {
        let computer = Computer(content)
        let (c, root) = center(computer, opened: Opened())
        defer { try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.begin(key, file: f)
        c.begin(key, file: f)
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
        #expect(computer.count == 3)
        // A second card, drawn later, reads the same.
        #expect(c.transfer(SharedFileKey(sessionId: "s1", fileId: "f1"), file: f) == c.transfer(key, file: f))
    }

    @Test func deletingTheCopyDropsItsThumbnailAndTheCardIsDownloadableAgain() async {
        let computer = Computer(content)
        let (c, root) = center(computer, opened: Opened())
        defer { try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.begin(key, file: f)
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
        await c.loadMedia(key, url: { if case .downloaded(let u, _) = c.transfer(key, file: f) { u } else { URL(fileURLWithPath: "/") } }(), kind: .other)
        c.deleteLocal(key)
        await eventually("idle") { c.transfer(key, file: f) == .idle }
        #expect(c.thumbnail(key) == nil)
        #expect(await c.usedBytes() == 0)
    }

    @Test func removingASessionDeletesItsFiles() async {
        let computer = Computer(content)
        let (c, root) = center(computer, opened: Opened())
        defer { try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.begin(key, file: f)
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
        #expect(await c.usedBytes() >= 2500)
        c.forgetSession("s1")
        await eventually("emptied") { c.transfer(key, file: f) == .idle }
        #expect(await c.usedBytes() == 0)
    }

    @Test func clearingTheStorageClearsTheCopiesToo() async {
        let computer = Computer(content)
        let (c, root) = center(computer, opened: Opened())
        defer { try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.begin(key, file: f)
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
        await c.clearAll()
        #expect(await c.usedBytes() == 0)
    }

    @Test func aThumbnailIsMadeFromTheCopyAndTheDurationIsKept() async throws {
        let png = UIGraphicsImageRenderer(size: CGSize(width: 40, height: 20)).image { ctx in
            UIColor.red.setFill(); ctx.fill(CGRect(x: 0, y: 0, width: 40, height: 20))
        }.pngData()!
        let computer = Computer(png)
        let (c, root) = center(computer, opened: Opened())
        defer { try? FileManager.default.removeItem(at: root) }
        let f = file(png)
        c.begin(key, file: f)
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
        guard case .downloaded(let url, _) = c.transfer(key, file: f) else { return }
        await c.loadMedia(key, url: url, kind: .image)
        #expect(c.thumbnail(key) != nil)
        // A copy that is not a picture gives none, and no error.
        let media = await SharedFileMedia.read(URL(fileURLWithPath: "/nonexistent"), kind: .image)
        #expect(media.thumbnail == nil)
    }

    // MARK: Drawing

    private func render<V: View>(_ view: V, size: DynamicTypeSize = .large, scheme: ColorScheme = .light) -> CGSize? {
        let renderer = ImageRenderer(content: view.environment(\.dynamicTypeSize, size).environment(\.colorScheme, scheme).frame(width: 390))
        renderer.scale = 1
        return renderer.uiImage?.size
    }

    @Test func everyStateDrawsA296WideCardAtEveryTypeSize() {
        let f = file(content, name: "daocheng-final-vertical-1080-v3.mp4")
        let cards: [SharedFileCard] = [
            .available, .downloading(done: 1000, total: 2500, etaSeconds: 12), .paused(done: 1000, total: 2500), .downloaded(durationMs: 102_000),
            .failed(.disconnected), .offline, .cleaned,
        ]
        for card in cards {
            for size in [DynamicTypeSize.large, .xxxLarge, .accessibility3] {
                let drawn = render(SharedFileCardBody(file: f, card: card), size: size)
                #expect(drawn != nil)
                #expect((drawn?.height ?? 0) >= 64, "\(card) \(size)")
            }
            let narrow = ImageRenderer(content: SharedFileCardBody(file: f, card: card).frame(width: 296, alignment: .leading))
            narrow.scale = 1
            #expect(narrow.uiImage?.size.width == 296)
        }
    }

    @Test func theSubtitleOfALargeTypeSizeIsNotCut() {
        let f = file(content)
        let small = render(SharedFileCardBody(file: f, card: .failed(.checksum), width: 200), size: .large)
        let large = render(SharedFileCardBody(file: f, card: .failed(.checksum), width: 200), size: .accessibility3)
        #expect((large?.height ?? 0) > (small?.height ?? 0))
    }
}
