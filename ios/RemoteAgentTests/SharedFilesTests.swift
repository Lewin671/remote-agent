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
                held.continuation.yield(true)
            }
        }
        var block = Data(count: 1000)
        let from = Int(offset), take = max(0, min(1000, content.count - from))
        if take > 0 { block.replaceSubrange(0..<take, with: content[from..<from + take]) }
        return .with { $0.data = block; $0.totalBytes = UInt64(content.count) }
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

private final class ReportSpy: @unchecked Sendable {
    private let lock = NSLock()
    private var items: [PBSharedFileLocalStateEvent] = []
    func record(_ event: PBSharedFileLocalStateEvent) { lock.withLock { items.append(event) } }
    var events: [PBSharedFileLocalStateEvent] { lock.withLock { items } }
}

@MainActor @Suite struct SharedFilesTests {
    private func file(_ content: Data, id: String = "f1", name: String = "cover.png") -> PBSharedFile {
        .with {
            $0.fileID = id; $0.name = name; $0.sizeBytes = Int64(content.count)
            $0.sha256 = Data(SHA256.hash(data: content)); $0.path = "/Users/me/\(name)"
        }
    }

    private func item(
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

    private let content = Data((0..<2500).map { UInt8($0 % 251) })
    private let key = SharedFileKey(sessionId: "s1", fileId: "f1")

    private func center(
        _ computer: Computer,
        opened: Opened,
        spy: ReportSpy = ReportSpy(),
        clock: @escaping @Sendable () -> Int64 = { nowMs() }
    ) -> (SharedFileCenter, URL) {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("sf-\(UUID().uuidString)")
        let store = SharedFileStore(root: root, fetcher: computer, isMachineOnline: { _ in true }, blockBytes: { _ in 1000 })
        return (SharedFileCenter(store: store, open: { opened.record($0) }, report: { spy.record($0) }, clock: clock), root)
    }

    @MainActor final class Opened {
        var urls: [URL] = []
        private let signal = AsyncStream<URL>.makeStream()
        func record(_ url: URL) { urls.append(url); signal.continuation.yield(url) }
        func next() async -> URL? { await nextEvent(signal.stream) }
    }

    // MARK: Downloading & Core reporting

    @Test func aTapDownloadsOpensAndReportsLocalStateToCore() async throws {
        let computer = Computer(content)
        let opened = Opened()
        let spy = ReportSpy()
        let (c, root) = center(computer, opened: opened, spy: spy)
        defer { c.close(); try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.tap(item(content, status: .available, action: .download), sessionId: "s1")
        let url = try #require(await opened.next())
        #expect(opened.urls.count == 1)
        #expect(try Data(contentsOf: url) == content)
        #expect({ if case .downloaded = c.transfer(key, file: f) { true } else { false } }())
        await c.flushReports()
        let statuses = spy.events.flatMap(\.updates).map(\.status)
        #expect(statuses.contains(.downloading))
        #expect(statuses.last == .downloaded)
        // Tapping 打开 later opens it again.
        c.tap(item(content, status: .downloaded, action: .open), sessionId: "s1")
        #expect(opened.urls.count == 2)
        // Disabled primary action does nothing.
        c.tap(item(content, status: .offline, action: .download, enabled: false), sessionId: "s1")
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
        c.tap(item(content, status: .available, action: .download), sessionId: "s1")
        try #require(await computer.waitUntilHeld())
        c.leftPage(sessionId)
        computer.release()
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
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
        defer { c.close(); try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.tap(item(content, status: .available, action: .download), sessionId: "s1")
        defer { computer.release() }
        try #require(await computer.waitUntilHeld())
        c.tap(item(content, status: .downloading, action: .pause), sessionId: "s1")
        await eventually("paused") { if case .paused = c.transfer(key, file: f) { true } else { false } }
        computer.release()
        try await Task.sleep(nanoseconds: 100_000_000)
        guard case .paused(let done, _) = c.transfer(key, file: f) else { Issue.record("not paused"); return }
        #expect(done == 1000)
        #expect(opened.urls.isEmpty)
        c.tap(item(content, status: .paused, action: .resume), sessionId: "s1")
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
    }

    @Test func theSameFileOnTwoCardsIsOneState() async {
        let computer = Computer(content)
        let (c, root) = center(computer, opened: Opened())
        defer { c.close(); try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.begin(key, file: f)
        c.begin(key, file: f)
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
        #expect(computer.count == 3)
        #expect(c.transfer(SharedFileKey(sessionId: "s1", fileId: "f1"), file: f) == c.transfer(key, file: f))
    }

    @Test func deletingTheCopyDropsItsThumbnailAndReportsUnspecifiedToCore() async {
        let computer = Computer(content)
        let spy = ReportSpy()
        let (c, root) = center(computer, opened: Opened(), spy: spy)
        defer { c.close(); try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.begin(key, file: f)
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
        await c.loadMedia(key, url: { if case .downloaded(let u, _) = c.transfer(key, file: f) { u } else { URL(fileURLWithPath: "/") } }(), kind: .other)
        c.deleteLocal(key)
        await eventually("idle") { c.transfer(key, file: f) == .idle }
        await c.flushReports()
        #expect(spy.events.last?.updates.last?.status == .unspecified)
        #expect(c.thumbnail(key) == nil)
        #expect(await c.usedBytes() == 0)
    }

    @Test func removingASessionDeletesItsFilesAndSendsClearSessionId() async {
        let computer = Computer(content)
        let spy = ReportSpy()
        let (c, root) = center(computer, opened: Opened(), spy: spy)
        defer { c.close(); try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.begin(key, file: f)
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
        #expect(await c.usedBytes() >= 2500)
        c.forgetSession("s1")
        await eventually("emptied") { c.transfer(key, file: f) == .idle }
        await c.flushReports()
        #expect(spy.events.contains { $0.clearSessionID_p == "s1" })
        #expect(await c.usedBytes() == 0)
    }

    @Test func clearingAndStartupSyncReportReplaceAllToCore() async {
        let computer = Computer(content)
        let spy = ReportSpy()
        let (c, root) = center(computer, opened: Opened(), spy: spy)
        defer { c.close(); try? FileManager.default.removeItem(at: root) }
        let f = file(content)
        c.begin(key, file: f)
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }

        // A second center on the same root scans disk and sends replace_all = true.
        let spy2 = ReportSpy()
        let store2 = SharedFileStore(root: root, fetcher: computer, isMachineOnline: { _ in true }, blockBytes: { _ in 1000 })
        let c2 = SharedFileCenter(store: store2, open: { _ in }, report: { spy2.record($0) })
        defer { c2.close() }
        await c2.syncAllToCore()
        await c2.flushReports()
        let firstSync = spy2.events.first
        #expect(firstSync?.replaceAll == true)
        #expect(firstSync?.updates.first?.fileID == "f1")
        #expect(firstSync?.updates.first?.status == .downloaded)

        await c.clearAll()
        await c.flushReports()
        #expect(spy.events.contains { $0.replaceAll && $0.updates.isEmpty })
        #expect(await c.usedBytes() == 0)
    }

    @Test func aThumbnailIsMadeFromTheCopy() async throws {
        let png = UIGraphicsImageRenderer(size: CGSize(width: 40, height: 20)).image { ctx in
            UIColor.red.setFill(); ctx.fill(CGRect(x: 0, y: 0, width: 40, height: 20))
        }.pngData()!
        let computer = Computer(png)
        let (c, root) = center(computer, opened: Opened())
        defer { c.close(); try? FileManager.default.removeItem(at: root) }
        let f = file(png)
        c.begin(key, file: f)
        await eventually("downloaded") { if case .downloaded = c.transfer(key, file: f) { true } else { false } }
        guard case .downloaded(let url, _) = c.transfer(key, file: f) else { return }
        await c.loadMedia(key, url: url, kind: .image)
        #expect(c.thumbnail(key) != nil)
        let media = await SharedFileMedia.read(URL(fileURLWithPath: "/nonexistent"), kind: .image)
        #expect(media.thumbnail == nil)
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
            var vm = item(content, name: "daocheng-final-vertical-1080-v3.mp4", status: status, action: action, enabled: enabled)
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
        var vm = item(content, status: .failed, action: .retry)
        vm.subtitle = .with { $0.literal = "没下载下来，文件校验不一致" }
        vm.subtitleIsError = true
        let small = render(SharedFileCardBody(item: vm, width: 200), size: .large)
        let large = render(SharedFileCardBody(item: vm, width: 200), size: .accessibility3)
        #expect((large?.height ?? 0) > (small?.height ?? 0))
    }
}
