// The phone's copies of files the agent shared, and the downloads that make
// them: fetched in blocks over `GetSharedFileChunk`, kept across pauses and
// a killed app, checked against the SHA-256 the event carries, and shared
// by every card that shows the same file_id (ui-spec §4, §7, §8).
//
// Layout under `root`, one folder per session and per file:
//
//   <session>/<file>/.part       the blocks so far
//   <session>/<file>/.meta       JSON: size, block size, completed offset, SHA-256, duration
//   <session>/<file>/file/<name> the finished copy, named as the sanitized name
//
// A pause, cancel or delete takes the download's token away: whatever its
// task does after that (a block that was already on its way) is dropped,
// never written and never shown (Android's pause race, device-report 1).
import CryptoKit
import Foundation
import RAProto

/// Fetches one block of a shared file; `Remote.sharedFileFetcher` is the real one.
public protocol SharedFileFetching: Sendable {
    func fetchChunk(sessionId: String, fileId: String, offset: Int64) async throws -> PBGetSharedFileChunkResponse
}

public enum SharedFileStoreEvent: Equatable, Sendable {
    case state(SharedFileKey, SharedFileTransfer)
    /// The finished copy has been checked and put in place.
    case completed(SharedFileKey, URL)
}

public actor SharedFileStore {
    private struct Meta: Codable, Sendable {
        var size: Int64
        var blockBytes: Int
        var completed: Int64
        var sha256: String
        var durationMs: Int64?
    }

    private struct Job {
        let token: Int
        let task: Task<Void, Never>
    }

    private nonisolated let root: URL
    private let fetcher: any SharedFileFetching
    private let isMachineOnline: @Sendable (String) -> Bool
    private let blockBytesFor: @Sendable (String) -> Int
    private let clock: @Sendable () -> Int64
    private let sleep: @Sendable (Int64) async -> Void

    private var jobs: [SharedFileKey: Job] = [:]
    private var states: [SharedFileKey: SharedFileTransfer] = [:]
    private var nextToken = 0
    private let continuation: AsyncStream<SharedFileStoreEvent>.Continuation
    /// What changed, in order. One reader.
    public nonisolated let events: AsyncStream<SharedFileStoreEvent>

    public init(
        root: URL, fetcher: any SharedFileFetching,
        isMachineOnline: @escaping @Sendable (String) -> Bool,
        blockBytes: @escaping @Sendable (String) -> Int = { _ in defaultSharedFileBlockBytes },
        clock: @escaping @Sendable () -> Int64 = { nowMs() },
        sleep: @escaping @Sendable (Int64) async -> Void = { try? await Task.sleep(nanoseconds: UInt64(max(0, $0)) * 1_000_000) }
    ) {
        self.root = root
        self.fetcher = fetcher
        self.isMachineOnline = isMachineOnline
        self.blockBytesFor = blockBytes
        self.clock = clock
        self.sleep = sleep
        (events, continuation) = AsyncStream.makeStream(of: SharedFileStoreEvent.self, bufferingPolicy: .unbounded)
    }

    deinit { continuation.finish() }

    // MARK: Paths

    private nonisolated func directory(_ key: SharedFileKey) -> URL {
        root.appending(path: sharedFileFolderName(key.sessionId), directoryHint: .isDirectory)
            .appending(path: sharedFileFolderName(key.fileId), directoryHint: .isDirectory)
    }

    private nonisolated func partURL(_ key: SharedFileKey) -> URL { directory(key).appending(path: ".part") }
    private nonisolated func metaURL(_ key: SharedFileKey) -> URL { directory(key).appending(path: ".meta") }

    /// Where the finished copy of `file` is, or would be.
    public nonisolated func localURL(_ key: SharedFileKey, name: String) -> URL {
        directory(key).appending(path: "file", directoryHint: .isDirectory)
            .appending(path: sanitizeSharedFileName(name), directoryHint: .notDirectory)
    }

    private nonisolated func readMeta(_ key: SharedFileKey) -> Meta? {
        guard let data = try? Data(contentsOf: metaURL(key)) else { return nil }
        return try? JSONDecoder().decode(Meta.self, from: data)
    }

    private nonisolated func writeMeta(_ key: SharedFileKey, _ meta: Meta) throws {
        try JSONEncoder().encode(meta).write(to: metaURL(key), options: .atomic)
    }

    private static func length(_ url: URL) -> Int64? {
        (try? FileManager.default.attributesOfItem(atPath: url.path)[.size] as? NSNumber)?.int64Value
    }

    // MARK: State

    private func set(_ key: SharedFileKey, _ state: SharedFileTransfer) {
        guard states[key] != state else { return }
        states[key] = state
        continuation.yield(.state(key, state))
    }

    /// The state of `key` as the disk says it, without starting anything.
    public nonisolated func inspect(_ key: SharedFileKey, file: PBSharedFile) -> SharedFileTransfer {
        let final = localURL(key, name: file.name)
        let meta = readMeta(key)
        if Self.length(final) == file.sizeBytes {
            return .downloaded(url: final, durationMs: meta?.durationMs)
        }
        if let meta, meta.size == file.sizeBytes, meta.sha256 == file.sha256.hexString, meta.blockBytes > 0,
           meta.completed > 0, meta.completed % Int64(meta.blockBytes) == 0,
           (Self.length(partURL(key)) ?? 0) >= meta.completed
        {
            return .paused(done: min(meta.completed, file.sizeBytes), total: file.sizeBytes)
        }
        return .idle
    }

    /// What is known of the file now: a download in flight, else what is on the disk. A copy the system
    /// took back (the cache is purged) is no longer a download.
    @discardableResult
    public func hydrate(_ key: SharedFileKey, file: PBSharedFile) -> SharedFileTransfer {
        if let held = states[key] {
            if jobs[key] != nil { return held }
            switch held {
            case .downloaded(let url, _): if FileManager.default.fileExists(atPath: url.path) { return held }
            case .paused, .failed, .cleanedUp: return held
            case .idle, .downloading: break
            }
        }
        let found = inspect(key, file: file)
        set(key, found)
        return found
    }

    public func state(_ key: SharedFileKey) -> SharedFileTransfer { states[key] ?? .idle }

    // MARK: Controls

    /// Starts, resumes or retries the download of `file`; a download already running is left alone.
    public func start(_ key: SharedFileKey, file: PBSharedFile) {
        if jobs[key] != nil { return }
        let found = inspect(key, file: file)
        if case .downloaded = found {
            set(key, found)
            return
        }
        nextToken += 1
        let token = nextToken
        let done: Int64 = if case .paused(let d, _) = found { d } else { 0 }
        set(key, .downloading(done: done, total: file.sizeBytes, etaSeconds: nil, waitingReconnect: false))
        jobs[key] = Job(token: token, task: Task { [self] in await run(key, file: file, token: token) })
    }

    /// Keeps what was fetched; nothing more is asked of the computer.
    public func pause(_ key: SharedFileKey, file: PBSharedFile) {
        guard let job = jobs.removeValue(forKey: key) else { return }
        job.task.cancel()
        let done = min(file.sizeBytes, readMeta(key)?.completed ?? 0)
        set(key, .paused(done: done, total: file.sizeBytes))
    }

    /// Drops the blocks fetched so far.
    public func cancel(_ key: SharedFileKey) {
        jobs.removeValue(forKey: key)?.task.cancel()
        try? FileManager.default.removeItem(at: partURL(key))
        try? FileManager.default.removeItem(at: metaURL(key))
        set(key, .idle)
    }

    /// 删除本机副本: the copy and anything partial.
    public func deleteLocal(_ key: SharedFileKey) {
        jobs.removeValue(forKey: key)?.task.cancel()
        try? FileManager.default.removeItem(at: directory(key))
        set(key, .idle)
    }

    /// The session is gone: its downloads stop and its folder goes.
    public func removeSession(_ sessionId: String) {
        for (key, job) in jobs where key.sessionId == sessionId {
            job.task.cancel()
            jobs[key] = nil
        }
        try? FileManager.default.removeItem(at: root.appending(path: sharedFileFolderName(sessionId), directoryHint: .isDirectory))
        for key in states.keys where key.sessionId == sessionId {
            states[key] = nil
            continuation.yield(.state(key, .idle))
        }
    }

    /// 清理: every copy and every partial download.
    public func clearAll() {
        for (_, job) in jobs { job.task.cancel() }
        jobs.removeAll()
        if let folders = try? FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil) {
            for folder in folders { try? FileManager.default.removeItem(at: folder) }
        }
        for key in states.keys {
            states[key] = nil
            continuation.yield(.state(key, .idle))
        }
    }

    /// The centre is closed: nothing more is fetched. What is on the disk stays, to be found again.
    public func stop() {
        for (_, job) in jobs { job.task.cancel() }
        jobs.removeAll()
    }

    /// What the copies and partial downloads take on disk.
    public func usedBytes() -> Int64 { Self.bytes(root) }

    nonisolated static func bytes(_ root: URL) -> Int64 { ImageDiskPolicy.bytes(root) }

    /// Notes the duration of a finished copy, which only the app can read (AVFoundation).
    public func recordDuration(_ key: SharedFileKey, ms: Int64) {
        guard case .downloaded(let url, _) = states[key], var meta = readMeta(key) else { return }
        meta.durationMs = ms
        try? writeMeta(key, meta)
        set(key, .downloaded(url: url, durationMs: ms))
    }

    // MARK: The download

    private func current(_ key: SharedFileKey, _ token: Int) -> Bool { jobs[key]?.token == token }

    private func finish(_ key: SharedFileKey, _ token: Int, _ state: SharedFileTransfer) {
        guard current(key, token) else { return }
        jobs[key] = nil
        set(key, state)
    }

    private func fail(_ key: SharedFileKey, _ token: Int, _ why: SharedFileFailure, dropBlocks: Bool = false) {
        guard current(key, token) else { return }
        if dropBlocks {
            try? FileManager.default.removeItem(at: partURL(key))
            try? FileManager.default.removeItem(at: metaURL(key))
        }
        finish(key, token, .failed(why))
    }

    private func run(_ key: SharedFileKey, file: PBSharedFile, token: Int) async {
        let fm = FileManager.default
        let blockBytes = max(1, blockBytesFor(key.sessionId))
        let size = file.sizeBytes
        let hashHex = file.sha256.hexString
        let part = partURL(key)
        var next: Int64 = 0
        do {
            try fm.createDirectory(at: directory(key), withIntermediateDirectories: true, attributes: Self.protection)
            if let meta = readMeta(key), meta.size == size, meta.blockBytes == blockBytes, meta.sha256 == hashHex,
               meta.completed > 0, meta.completed % Int64(blockBytes) == 0,
               (Self.length(part) ?? 0) >= min(meta.completed, size)
            {
                // Every block may already be in (the app went away before the check): only the check is left.
                next = min(meta.completed, size)
                let handle = try FileHandle(forWritingTo: part)
                try handle.truncate(atOffset: UInt64(next))
                try handle.close()
            } else {
                try? fm.removeItem(at: part)
                try writeMeta(key, Meta(size: size, blockBytes: blockBytes, completed: 0, sha256: hashHex, durationMs: nil))
                fm.createFile(atPath: part.path, contents: nil, attributes: Self.protection)
            }
        } catch {
            fail(key, token, Self.failure(for: error))
            return
        }
        set(key, .downloading(done: next, total: size, etaSeconds: nil, waitingReconnect: false))

        let plan = planSharedFileBlocks(size: size, blockBytes: blockBytes)
        var index = next >= size && size > 0 ? plan.offsets.count : (plan.offsets.firstIndex { $0 >= next } ?? 0)
        let started = clock()
        var fetched: Int64 = 0
        var retried = false
        while index < plan.offsets.count {
            guard current(key, token) else { return }
            let offset = plan.offsets[index]
            let reply: PBGetSharedFileChunkResponse
            do {
                reply = try await fetcher.fetchChunk(sessionId: key.sessionId, fileId: key.fileId, offset: offset)
            } catch {
                guard current(key, token), !Task.isCancelled else { return }
                if let e = error as? CommandError, e.fromDaemon, e.code == "not_found" {
                    // The computer dropped the file: nothing of it is kept.
                    try? fm.removeItem(at: part)
                    try? fm.removeItem(at: metaURL(key))
                    finish(key, token, .cleanedUp)
                    return
                }
                if Self.transient(error) {
                    if retried {
                        fail(key, token, .disconnected)
                        return
                    }
                    retried = true
                    set(key, .downloading(done: min(offset, size), total: size, etaSeconds: nil, waitingReconnect: true))
                    if await waitForComputer(key, token) { continue }
                    fail(key, token, .disconnected)
                    return
                }
                fail(key, token, .other((error as? CommandError)?.message ?? error.localizedDescription))
                return
            }
            // The block was on its way while a pause or cancel went through: it is not this download's any more.
            guard current(key, token) else { return }

            guard reply.hasTotalBytes, reply.totalBytes == UInt64(size),
                  reply.data.count == blockBytes || (size == 0 && reply.data.isEmpty)
            else {
                fail(key, token, .checksum, dropBlocks: true)
                return
            }
            let take = Int(min(Int64(blockBytes), max(0, size - offset)))
            do {
                let handle = try FileHandle(forUpdating: part)
                defer { try? handle.close() }
                try handle.seek(toOffset: UInt64(offset))
                if take > 0 { try handle.write(contentsOf: reply.data.prefix(take)) }
                try handle.truncate(atOffset: UInt64(offset + Int64(take)))
                try writeMeta(key, Meta(size: size, blockBytes: blockBytes, completed: offset + Int64(blockBytes), sha256: hashHex, durationMs: nil))
            } catch {
                fail(key, token, Self.failure(for: error))
                return
            }
            let done = min(size, offset + Int64(take))
            fetched += Int64(take)
            let elapsed = max(0, clock() - started)
            var eta: Int?
            if elapsed >= 3_000, fetched > 0, done < size {
                eta = max(1, Int((Double(size - done) / (Double(fetched) / (Double(elapsed) / 1000))).rounded(.down)))
            }
            set(key, .downloading(done: done, total: size, etaSeconds: eta, waitingReconnect: false))
            index += 1
        }

        guard current(key, token) else { return }
        guard let digest = Self.sha256(of: part, bytes: size), digest == file.sha256 else {
            fail(key, token, .checksum, dropBlocks: true)
            return
        }
        let final = localURL(key, name: file.name)
        do {
            try fm.createDirectory(at: final.deletingLastPathComponent(), withIntermediateDirectories: true, attributes: Self.protection)
            try? fm.removeItem(at: final)
            try fm.moveItem(at: part, to: final)
            try writeMeta(key, Meta(size: size, blockBytes: blockBytes, completed: size, sha256: hashHex, durationMs: nil))
        } catch {
            fail(key, token, Self.failure(for: error))
            return
        }
        finish(key, token, .downloaded(url: final, durationMs: nil))
        continuation.yield(.completed(key, final))
    }

    /// Waits for the computer to be reachable again, up to 30 seconds.
    private func waitForComputer(_ key: SharedFileKey, _ token: Int) async -> Bool {
        let deadline = clock() + sharedFileReconnectWaitMs
        await sleep(1_000)
        while clock() < deadline {
            guard current(key, token) else { return false }
            if isMachineOnline(key.sessionId) { return true }
            await sleep(min(1_000, max(1, deadline - clock())))
        }
        return current(key, token) && isMachineOnline(key.sessionId)
    }

    // MARK: Helpers

    nonisolated static var protection: [FileAttributeKey: Any] {
        #if os(iOS)
        [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication]
        #else
        [:]
        #endif
    }

    /// An answer that says nothing about the file: the link, or the computer being away.
    nonisolated static func transient(_ error: Error) -> Bool {
        if error is URLError { return true }
        guard let e = error as? CommandError else { return false }
        if e.fromDaemon { return false }
        return ["network", CommandError.unavailable, CommandError.unknownOutcome, "deadline_exceeded", "canceled"].contains(e.code)
    }

    nonisolated static func failure(for error: Error) -> SharedFileFailure {
        let e = error as NSError
        if e.domain == NSCocoaErrorDomain && e.code == NSFileWriteOutOfSpaceError { return .diskFull }
        if e.domain == NSPOSIXErrorDomain && e.code == Int(ENOSPC) { return .diskFull }
        if let under = e.userInfo[NSUnderlyingErrorKey] as? NSError, under.domain == NSPOSIXErrorDomain, under.code == Int(ENOSPC) { return .diskFull }
        return .other(e.localizedDescription)
    }

    /// SHA-256 of the first `bytes` bytes of the file, read in pieces.
    nonisolated static func sha256(of url: URL, bytes: Int64) -> Data? {
        guard let handle = try? FileHandle(forReadingFrom: url) else { return nil }
        defer { try? handle.close() }
        var hash = SHA256()
        var left = bytes
        while left > 0 {
            guard let piece = try? handle.read(upToCount: Int(min(left, 1 << 20))), !piece.isEmpty else { return nil }
            hash.update(data: piece)
            left -= Int64(piece.count)
        }
        return Data(hash.finalize())
    }
}

extension Data {
    var hexString: String { map { String(format: "%02x", $0) }.joined() }
}
