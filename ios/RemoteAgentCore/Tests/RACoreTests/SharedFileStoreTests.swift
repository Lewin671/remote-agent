import CryptoKit
import Foundation
import RAProto
import Testing

@testable import RACore

/// A computer that serves one file in blocks, and what goes wrong on the way, as the test says.
private final class Computer: SharedFileFetching, @unchecked Sendable {
    enum Fault: Sendable {
        case error(CommandError)
        /// Waits until `release()`, then answers.
        case hold
    }

    let lock = NSLock()
    var content: Data
    let blockBytes: Int
    private var served: [Int64] = []
    private var faults: [Int: Fault] = [:]
    private var held: [CheckedContinuation<Void, Never>] = []
    private var corruptTotal = false
    private var corruptData = false

    init(content: Data, blockBytes: Int) {
        self.content = content
        self.blockBytes = blockBytes
    }

    /// The nth request (from 0) fails or holds.
    func fault(at request: Int, _ fault: Fault) { lock.withLock { faults[request] = fault } }
    func corrupt(total: Bool = false, data: Bool = false) { lock.withLock { corruptTotal = total; corruptData = data } }
    var offsets: [Int64] { lock.withLock { served } }

    func release() {
        let waiting = lock.withLock { () -> [CheckedContinuation<Void, Never>] in defer { held = [] }; return held }
        for w in waiting { w.resume() }
    }

    func fetchChunk(sessionId: String, fileId: String, offset: Int64) async throws -> PBGetSharedFileChunkResponse {
        let (n, fault) = lock.withLock { () -> (Int, Fault?) in
            served.append(offset)
            return (served.count - 1, faults[served.count - 1])
        }
        if case .error(let e)? = fault { throw e }
        if case .hold? = fault { await withCheckedContinuation { c in lock.withLock { held.append(c) } } }
        _ = n
        var block = Data(repeating: 0, count: blockBytes)
        let from = Int(offset)
        let take = max(0, min(blockBytes, content.count - from))
        if take > 0 { block.replaceSubrange(0..<take, with: content[from..<from + take]) }
        let (badTotal, badData) = lock.withLock { (corruptTotal, corruptData) }
        if badData, take > 0 { block[0] ^= 0xff }
        return .with {
            $0.data = block
            $0.totalBytes = UInt64(content.count) + (badTotal ? 1 : 0)
        }
    }
}

private final class Ticker: @unchecked Sendable {
    private let lock = NSLock()
    private var now: Int64 = 1_000_000
    private var online = false
    private var onlineAfter: Int64 = .max
    var time: Int64 { lock.withLock { now } }
    func advance(_ ms: Int64) { lock.withLock { now += ms } }
    /// The computer is back once this many ms have passed from now.
    func comesBack(after ms: Int64) { lock.withLock { onlineAfter = now + ms } }
    var isOnline: Bool { lock.withLock { now >= onlineAfter } }
}

@Suite struct SharedFileStoreTests {
    private let block = 1000
    private func pattern(_ n: Int) -> Data { Data((0..<n).map { UInt8($0 % 251) }) }
    private func file(_ content: Data, id: String = "f1", name: String = "clip.mp4") -> PBSharedFile {
        .with {
            $0.fileID = id; $0.name = name; $0.sizeBytes = Int64(content.count)
            $0.sha256 = Data(SHA256.hash(data: content)); $0.path = "/Users/me/\(name)"
        }
    }
    private func root() -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("shared-files-\(UUID().uuidString)")
    }
    private func store(_ root: URL, _ computer: Computer, ticker: Ticker = Ticker(), online: Bool = true) -> SharedFileStore {
        SharedFileStore(
            root: root, fetcher: computer, isMachineOnline: { _ in online || ticker.isOnline }, blockBytes: { _ in block },
            clock: { ticker.time }, sleep: { ticker.advance($0); await Task.yield() })
    }
    private let key = SharedFileKey(sessionId: "s1", fileId: "f1")

    private func wait(_ store: SharedFileStore, _ what: (SharedFileTransfer) -> Bool) async -> SharedFileTransfer {
        for _ in 0..<2000 {
            let s = await store.state(key)
            if what(s) { return s }
            try? await Task.sleep(nanoseconds: 2_000_000)
        }
        return await store.state(key)
    }
    private func done(_ t: SharedFileTransfer) -> Bool { if case .downloaded = t { return true } else { return false } }
    private func failed(_ t: SharedFileTransfer) -> Bool { if case .failed = t { return true } else { return false } }

    // MARK: A whole download

    @Test func fetchesEveryBlockChecksAndKeepsTheCopyUnderItsSanitizedName() async throws {
        let content = pattern(2500)
        let computer = Computer(content: content, blockBytes: block)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        let f = file(content, name: "../evil\u{202E}/clip.mp4")
        await s.start(key, file: f)
        let end = await wait(s) { done($0) }
        guard case .downloaded(let url, _) = end else { Issue.record("\(end)"); return }
        #expect(computer.offsets == [0, 1000, 2000])
        #expect(try Data(contentsOf: url) == content)
        #expect(url.lastPathComponent == "clip.mp4")
        #expect(url.path.hasPrefix(dir.path))
        #expect(await s.usedBytes() >= 2500)
    }

    @Test func tellsTheProgressAsItGoesAndWhenItIsDone() async throws {
        let content = pattern(2500)
        let computer = Computer(content: content, blockBytes: block)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        await s.start(key, file: file(content))
        var seen: [SharedFileTransfer] = []
        var completed: URL?
        for await e in s.events {
            switch e {
            case .state(_, let t): seen.append(t)
            case .completed(_, let url): completed = url
            }
            if completed != nil { break }
        }
        let dones = seen.compactMap { t -> Int64? in if case .downloading(let d, _, _, _) = t { d } else { nil } }
        #expect(dones == [0, 1000, 2000, 2500])
        #expect(completed != nil)
    }

    @Test func anEmptyFileIsOneBlockAndAnEmptyCopy() async throws {
        let computer = Computer(content: Data(), blockBytes: block)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        await s.start(key, file: file(Data(), name: "empty.txt"))
        guard case .downloaded(let url, _) = await wait(s, { done($0) }) else { Issue.record("not downloaded"); return }
        #expect(computer.offsets == [0])
        #expect(try Data(contentsOf: url).isEmpty)
    }

    @Test func theSameFileIdIsOneDownloadHoweverManyCardsAskForIt() async {
        let content = pattern(2500)
        let computer = Computer(content: content, blockBytes: block)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        let f = file(content)
        await s.start(key, file: f)
        await s.start(key, file: f)
        await s.start(key, file: f)
        _ = await wait(s) { done($0) }
        #expect(computer.offsets == [0, 1000, 2000])
        // A card that appears later finds the same state without asking again.
        #expect(done(await s.hydrate(key, file: f)))
        await s.start(key, file: f)
        #expect(computer.offsets.count == 3)
    }

    // MARK: Pause, resume, cancel

    @Test func aPauseKeepsTheBlocksAndResumeAsksOnlyForTheRest() async {
        let content = pattern(3500)
        let computer = Computer(content: content, blockBytes: block)
        computer.fault(at: 2, .hold)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        let f = file(content)
        await s.start(key, file: f)
        for _ in 0..<2000 where computer.offsets.count < 3 { try? await Task.sleep(nanoseconds: 2_000_000) }
        await s.pause(key, file: f)
        #expect(await s.state(key) == .paused(done: 2000, total: 3500))
        computer.release()
        try? await Task.sleep(nanoseconds: 50_000_000)
        // The block that was on its way is not written, and the card does not go back to downloading.
        #expect(await s.state(key) == .paused(done: 2000, total: 3500))
        #expect(computer.offsets == [0, 1000, 2000])
        await s.start(key, file: f)
        guard case .downloaded(let url, _) = await wait(s, { done($0) }) else { Issue.record("not downloaded"); return }
        #expect(computer.offsets == [0, 1000, 2000, 2000, 3000])
        #expect(try! Data(contentsOf: url) == content)
    }

    @Test func aKilledAppComesBackPausedWithItsBlocks() async {
        let content = pattern(3500)
        let computer = Computer(content: content, blockBytes: block)
        computer.fault(at: 2, .hold)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let f = file(content)
        let first = store(dir, computer)
        await first.start(key, file: f)
        for _ in 0..<2000 where computer.offsets.count < 3 { try? await Task.sleep(nanoseconds: 2_000_000) }
        // The first process is gone: a second one reads what the disk says.
        let second = store(dir, Computer(content: content, blockBytes: block))
        #expect(await second.hydrate(key, file: f) == .paused(done: 2000, total: 3500))
        computer.release()
        await first.cancel(key)
    }

    @Test func blocksOfAnotherFileOrSizeAreNotResumedFrom() async {
        let content = pattern(3500)
        let computer = Computer(content: content, blockBytes: block)
        computer.fault(at: 2, .hold)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let first = store(dir, computer)
        await first.start(key, file: file(content))
        for _ in 0..<2000 where computer.offsets.count < 3 { try? await Task.sleep(nanoseconds: 2_000_000) }
        let other = pattern(3500).reversed().map { $0 } as [UInt8]
        let changed = file(Data(other))
        let second = store(dir, Computer(content: Data(other), blockBytes: block))
        #expect(await second.hydrate(key, file: changed) == .idle)
        computer.release()
        await first.cancel(key)
    }

    @Test func cancelDropsTheBlocks() async {
        let content = pattern(3500)
        let computer = Computer(content: content, blockBytes: block)
        computer.fault(at: 2, .hold)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        let f = file(content)
        await s.start(key, file: f)
        for _ in 0..<2000 where computer.offsets.count < 3 { try? await Task.sleep(nanoseconds: 2_000_000) }
        await s.cancel(key)
        computer.release()
        try? await Task.sleep(nanoseconds: 50_000_000)
        #expect(await s.state(key) == .idle)
        #expect(await s.inspect(key, file: f) == .idle)
    }

    // MARK: The link

    @Test func aDroppedLinkWaitsForTheComputerAndGoesOnOnce() async {
        let content = pattern(3500)
        let computer = Computer(content: content, blockBytes: block)
        computer.fault(at: 1, .error(CommandError("network", "down")))
        let ticker = Ticker()
        ticker.comesBack(after: 5_000)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer, ticker: ticker, online: false)
        await s.start(key, file: file(content))
        guard case .downloaded(let url, _) = await wait(s, { done($0) }) else { Issue.record("not downloaded"); return }
        #expect(try! Data(contentsOf: url) == content)
        // The block that failed is asked for again, from where it was.
        #expect(computer.offsets == [0, 1000, 1000, 2000, 3000])
    }

    @Test func aComputerThatDoesNotComeBackWithin30SecondsFailsTheDownloadKeepingItsBlocks() async {
        let content = pattern(3500)
        let computer = Computer(content: content, blockBytes: block)
        computer.fault(at: 2, .error(CommandError("unavailable", "offline")))
        let ticker = Ticker()
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer, ticker: ticker, online: false)
        let f = file(content)
        let started = ticker.time
        await s.start(key, file: f)
        #expect(await wait(s, { failed($0) }) == .failed(.disconnected))
        #expect(ticker.time - started >= 30_000)
        #expect(await s.inspect(key, file: f) == .paused(done: 2000, total: 3500))
        // 重试 goes on from the blocks that are there.
        await s.start(key, file: f)
        _ = await wait(s) { done($0) }
        #expect(computer.offsets == [0, 1000, 2000, 2000, 3000])
    }

    @Test func aSecondDropFailsAtOnceRatherThanWaitingAgain() async {
        let content = pattern(3500)
        let computer = Computer(content: content, blockBytes: block)
        computer.fault(at: 1, .error(CommandError("network", "down")))
        computer.fault(at: 2, .error(CommandError("network", "down")))
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer, online: true)
        await s.start(key, file: file(content))
        #expect(await wait(s, { failed($0) }) == .failed(.disconnected))
    }

    @Test func theComputerSayingItHasNoSuchFileEndsItAsCleanedUp() async {
        let content = pattern(2500)
        let computer = Computer(content: content, blockBytes: block)
        computer.fault(at: 1, .error(CommandError("not_found", "no such file", fromDaemon: true, readOnly: true)))
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        let f = file(content)
        await s.start(key, file: f)
        #expect(await wait(s, { $0 == .cleanedUp }) == .cleanedUp)
        #expect(await s.inspect(key, file: f) == .idle)
    }

    @Test func anUnknownSessionOfThisPhoneIsNotTheComputerCleaningUp() async {
        let content = pattern(2500)
        let computer = Computer(content: content, blockBytes: block)
        computer.fault(at: 0, .error(CommandError("not_found", "unknown session")))
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        await s.start(key, file: file(content))
        #expect(await wait(s, { failed($0) }) == .failed(.other("unknown session")))
    }

    @Test func theComputersOwnReasonIsPassedOn() async {
        let content = pattern(2500)
        let computer = Computer(content: content, blockBytes: block)
        computer.fault(at: 0, .error(CommandError("internal", "disk read error", fromDaemon: true, readOnly: true)))
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        await s.start(key, file: file(content))
        #expect(await wait(s, { failed($0) }) == .failed(.other("disk read error")))
    }

    // MARK: Checks

    @Test func aBlockThatCarriesTheWrongTotalMakesTheDownloadFail() async {
        let content = pattern(2500)
        let computer = Computer(content: content, blockBytes: block)
        computer.corrupt(total: true)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        let f = file(content)
        await s.start(key, file: f)
        #expect(await wait(s, { failed($0) }) == .failed(.checksum))
        #expect(await s.inspect(key, file: f) == .idle)
    }

    @Test func aHashThatDoesNotMatchThrowsTheWholeCopyAway() async {
        let content = pattern(2500)
        let computer = Computer(content: content, blockBytes: block)
        computer.corrupt(data: true)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        let f = file(content)
        await s.start(key, file: f)
        #expect(await wait(s, { failed($0) }) == .failed(.checksum))
        #expect(await s.inspect(key, file: f) == .idle)
        // 重试 begins from nothing.
        computer.corrupt()
        await s.start(key, file: f)
        _ = await wait(s) { done($0) }
        #expect(computer.offsets.suffix(3) == [0, 1000, 2000])
    }

    @Test func aMissingHashIsNotTrusted() async {
        let content = pattern(1500)
        let computer = Computer(content: content, blockBytes: block)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        var f = file(content)
        f.sha256 = Data()
        await s.start(key, file: f)
        #expect(await wait(s, { failed($0) }) == .failed(.checksum))
    }

    @Test func aFullPhoneIsSaidSoInTheCocoaAndPosixWays() {
        #expect(SharedFileStore.failure(for: NSError(domain: NSCocoaErrorDomain, code: NSFileWriteOutOfSpaceError)) == .diskFull)
        #expect(SharedFileStore.failure(for: NSError(domain: NSPOSIXErrorDomain, code: Int(ENOSPC))) == .diskFull)
        let wrapped = NSError(domain: NSCocoaErrorDomain, code: 512, userInfo: [NSUnderlyingErrorKey: NSError(domain: NSPOSIXErrorDomain, code: Int(ENOSPC))])
        #expect(SharedFileStore.failure(for: wrapped) == .diskFull)
        if case .other = SharedFileStore.failure(for: NSError(domain: NSCocoaErrorDomain, code: 513)) {} else { Issue.record("other") }
    }

    // MARK: The copies

    @Test func deletingTheCopyGoesBackToDownloadable() async {
        let content = pattern(1500)
        let computer = Computer(content: content, blockBytes: block)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        let f = file(content)
        await s.start(key, file: f)
        guard case .downloaded(let url, _) = await wait(s, { done($0) }) else { Issue.record("not downloaded"); return }
        await s.deleteLocal(key)
        #expect(await s.state(key) == .idle)
        #expect(!FileManager.default.fileExists(atPath: url.path))
        #expect(await s.usedBytes() == 0)
    }

    @Test func aCopyTheSystemTookBackIsDownloadableAgain() async {
        let content = pattern(1500)
        let computer = Computer(content: content, blockBytes: block)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        let f = file(content)
        await s.start(key, file: f)
        guard case .downloaded(let url, _) = await wait(s, { done($0) }) else { Issue.record("not downloaded"); return }
        try? FileManager.default.removeItem(at: url.deletingLastPathComponent())
        #expect(await s.hydrate(key, file: f) == .idle)
    }

    @Test func aCopyOnTheDiskIsFoundAgainByAnotherProcessWithItsDuration() async {
        let content = pattern(1500)
        let computer = Computer(content: content, blockBytes: block)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        let f = file(content)
        await s.start(key, file: f)
        _ = await wait(s) { done($0) }
        await s.recordDuration(key, ms: 102_000)
        let again = store(dir, computer)
        guard case .downloaded(_, let ms) = await again.hydrate(key, file: f) else { Issue.record("not found"); return }
        #expect(ms == 102_000)
    }

    @Test func removingASessionTakesItsFilesAndStopsItsDownloads() async {
        let content = pattern(3500)
        let computer = Computer(content: content, blockBytes: block)
        computer.fault(at: 1, .hold)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        await s.start(key, file: file(content))
        for _ in 0..<2000 where computer.offsets.count < 2 { try? await Task.sleep(nanoseconds: 2_000_000) }
        await s.removeSession("s1")
        computer.release()
        try? await Task.sleep(nanoseconds: 50_000_000)
        #expect(await s.state(key) == .idle)
        #expect(await s.usedBytes() == 0)
        #expect(computer.offsets == [0, 1000])
    }

    @Test func clearingEverythingEmptiesTheFolderAndCountsNothing() async {
        let content = pattern(1500)
        let computer = Computer(content: content, blockBytes: block)
        let dir = root(); defer { try? FileManager.default.removeItem(at: dir) }
        let s = store(dir, computer)
        await s.start(key, file: file(content))
        _ = await wait(s) { done($0) }
        let other = SharedFileKey(sessionId: "s2", fileId: "f9")
        await s.start(other, file: file(content, id: "f9", name: "b.zip"))
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(await s.usedBytes() > 0)
        await s.clearAll()
        #expect(await s.usedBytes() == 0)
        #expect(await s.state(key) == .idle)
    }

    @Test func theFolderNamesAreMadeOfSafeCharactersOnly() {
        #expect(sharedFileFolderName("a1b2-c_3") == "a1b2-c_3")
        #expect(sharedFileFolderName("../x/y") == "___x_y")
        #expect(sharedFileFolderName("é") == "_")
    }
}
