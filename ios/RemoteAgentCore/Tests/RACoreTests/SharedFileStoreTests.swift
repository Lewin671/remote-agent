import CryptoKit
import Foundation
import RAProto
import Testing

@testable import RACore

/// Platform I/O executor tests for `SharedFileStore`.
/// Download state machine, block validation, resume, ETA, and reconnect rules are tested in Go `clientcore`.
@Suite(.timeLimit(.minutes(2))) struct SharedFileStoreTests {
    private func pattern(_ n: Int) -> Data { Data((0..<n).map { UInt8($0 % 251) }) }
    private func tempRoot() -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("shared-file-io-\(UUID().uuidString)")
    }
    private let key = SharedFileKey(sessionId: "s1", fileId: "f1")

    private func writeEffect(
        opID: UInt64,
        sessionID: String = "s1",
        fileID: String = "f1",
        name: String = "clip.mp4",
        offset: Int64,
        data: Data,
        total: Int64,
        chunkBytes: Int32 = 1000,
        committedOffset: Int64,
        sha256: Data,
        resetPartial: Bool
    ) -> PBSharedFileWriteChunkEffect {
        .with {
            $0.opID = opID
            $0.sessionID = sessionID
            $0.fileID = fileID
            $0.displayName = name
            $0.offset = offset
            $0.data = data
            $0.totalSizeBytes = total
            $0.chunkBytes = chunkBytes
            $0.committedOffset = committedOffset
            $0.expectedSha256 = sha256
            $0.resetPartial = resetPartial
        }
    }

    private func verifyEffect(
        opID: UInt64,
        sessionID: String = "s1",
        fileID: String = "f1",
        name: String = "clip.mp4",
        total: Int64,
        sha256: Data,
        deliverOnly: Bool = false
    ) -> PBSharedFileVerifyAndCommitEffect {
        .with {
            $0.opID = opID
            $0.sessionID = sessionID
            $0.fileID = fileID
            $0.displayName = name
            $0.totalSizeBytes = total
            $0.expectedSha256 = sha256
            $0.deliverOnly = deliverOnly
        }
    }

    @Test func writeChunksVerifyCommitAndScanCompletedWithDuration() throws {
        let dir = tempRoot(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = SharedFileStore(root: dir)
        let content = pattern(2500)
        let hash = Data(SHA256.hash(data: content))

        let r1 = store.writeChunk(writeEffect(
            opID: 1, name: "../evil/clip.mp4", offset: 0, data: content[0..<1000],
            total: 2500, committedOffset: 1000, sha256: hash, resetPartial: true))
        #expect(r1.ok && r1.opID == 1)

        let r2 = store.writeChunk(writeEffect(
            opID: 2, name: "../evil/clip.mp4", offset: 1000, data: content[1000..<2000],
            total: 2500, committedOffset: 2000, sha256: hash, resetPartial: false))
        #expect(r2.ok && r2.opID == 2)

        // Scan while partial should report completed=false with exact bytes and metadata.
        let midScan = store.scanLocal(opID: 10)
        #expect(midScan.opID == 10 && midScan.error.isEmpty)
        let partialEntry = try #require(midScan.entries.first)
        #expect(!partialEntry.completed)
        #expect(partialEntry.sessionID == "s1" && partialEntry.fileID == "f1")
        #expect(partialEntry.bytesOnDisk == 2000 && partialEntry.committedOffset == 2000)
        #expect(partialEntry.expectedSizeBytes == 2500 && partialEntry.chunkBytes == 1000)
        #expect(partialEntry.sha256 == hash)

        let r3 = store.writeChunk(writeEffect(
            opID: 3, name: "../evil/clip.mp4", offset: 2000, data: content[2000..<2500],
            total: 2500, committedOffset: 2500, sha256: hash, resetPartial: false))
        #expect(r3.ok)

        let verified = store.verifyAndCommit(verifyEffect(opID: 4, name: "../evil/clip.mp4", total: 2500, sha256: hash))
        #expect(verified.ok && verified.opID == 4)

        let local = try #require(store.existingLocalURL(key, name: "../evil/clip.mp4"))
        #expect(local.lastPathComponent == "clip.mp4")
        #expect(local.path.hasPrefix(dir.path))
        #expect(try Data(contentsOf: local) == content)
        #expect(store.usedBytes() >= 2500)

        store.recordDuration(key, ms: 102_000)
        let doneScan = store.scanLocal(opID: 11)
        let doneEntry = try #require(doneScan.entries.first)
        #expect(doneEntry.completed)
        #expect(doneEntry.bytesOnDisk == 2500 && doneEntry.expectedSizeBytes == 2500)
        #expect(doneEntry.hasDuration_p && doneEntry.durationMs == 102_000)
    }

    @Test func zeroByteFileCommitsAndScansCleanly() throws {
        let dir = tempRoot(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = SharedFileStore(root: dir)
        let hash = Data(SHA256.hash(data: Data()))

        let w = store.writeChunk(writeEffect(
            opID: 1, name: "empty.txt", offset: 0, data: Data(),
            total: 0, committedOffset: 0, sha256: hash, resetPartial: true))
        #expect(w.ok)

        let v = store.verifyAndCommit(verifyEffect(opID: 2, name: "empty.txt", total: 0, sha256: hash))
        #expect(v.ok)

        let local = try #require(store.existingLocalURL(key, name: "empty.txt"))
        #expect(try Data(contentsOf: local).isEmpty)

        let scan = store.scanLocal(opID: 3)
        let entry = try #require(scan.entries.first)
        #expect(entry.completed && entry.bytesOnDisk == 0 && entry.expectedSizeBytes == 0)
    }

    @Test func verifyRejectsMismatchAndDeletesPartial() {
        let dir = tempRoot(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = SharedFileStore(root: dir)
        let content = pattern(1200)
        var badHash = Data(SHA256.hash(data: content))
        badHash[0] ^= 0xff

        #expect(store.writeChunk(writeEffect(
            opID: 1, offset: 0, data: content, total: 1200,
            committedOffset: 1200, sha256: badHash, resetPartial: true)).ok)

        let v = store.verifyAndCommit(verifyEffect(opID: 2, total: 1200, sha256: badHash))
        #expect(!v.ok && v.errorKind == .checksumMismatch)
        #expect(store.existingLocalURL(key, name: "clip.mp4") == nil)
        #expect(store.scanLocal(opID: 3).entries.isEmpty)
    }

    @Test func legacyMetaFormatIsNotReadAndReportedMalformedForCleanup() throws {
        let dir = tempRoot(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = SharedFileStore(root: dir)
        let fm = FileManager.default

        // Legacy partial (.meta using old camelCase fields without schema_version).
        let legacyPartialDir = dir.appending(path: "s1/f-legacy-part", directoryHint: .isDirectory)
        try fm.createDirectory(at: legacyPartialDir, withIntermediateDirectories: true)
        try Data("{\"size\":2000,\"blockBytes\":1000,\"completed\":1000,\"sha256\":\"abcd\"}".utf8)
            .write(to: legacyPartialDir.appending(path: ".meta"))
        try pattern(1000).write(to: legacyPartialDir.appending(path: ".part"))

        // Legacy completed copy with old .meta.
        let legacyDoneDir = dir.appending(path: "s1/f-legacy-done", directoryHint: .isDirectory)
        let legacyFileFolder = legacyDoneDir.appending(path: "file", directoryHint: .isDirectory)
        try fm.createDirectory(at: legacyFileFolder, withIntermediateDirectories: true)
        try Data("{\"size\":500,\"blockBytes\":1000,\"completed\":500,\"sha256\":\"abcd\"}".utf8)
            .write(to: legacyDoneDir.appending(path: ".meta"))
        try pattern(500).write(to: legacyFileFolder.appending(path: "old.bin"))

        let scan = store.scanLocal(opID: 99)
        let byID = Dictionary(uniqueKeysWithValues: scan.entries.map { ($0.fileID, $0) })

        let partEntry = try #require(byID["f-legacy-part"])
        #expect(!partEntry.completed && partEntry.committedOffset == 0 && partEntry.sha256.isEmpty)

        let doneEntry = try #require(byID["f-legacy-done"])
        #expect(doneEntry.completed && doneEntry.bytesOnDisk == -1)
    }

    @Test func inspectDiskReportsKnownUnknownAndStorageFull() {
        let dir = tempRoot(); defer { try? FileManager.default.removeItem(at: dir) }
        let req = PBSharedFileInspectDiskEffect.with {
            $0.opID = 7; $0.sessionID = "s1"; $0.fileID = "f1"; $0.requiredBytes = 4096
        }

        let knownStore = SharedFileStore(root: dir, availableCapacity: { _ in 123_456 })
        let known = knownStore.inspectDisk(req)
        #expect(known.opID == 7 && known.known && known.availableBytes == 123_456 && known.errorKind == .unspecified)

        let unknownStore = SharedFileStore(root: dir, availableCapacity: { _ in nil })
        let unknown = unknownStore.inspectDisk(req)
        #expect(!unknown.known && unknown.errorKind == .unspecified)

        let fullStore = SharedFileStore(root: dir, availableCapacity: { _ in
            throw NSError(domain: NSCocoaErrorDomain, code: NSFileWriteOutOfSpaceError)
        })
        let full = fullStore.inspectDisk(req)
        #expect(!full.known && full.errorKind == .storageFull)
    }

    @Test func storageFullErrorsMapToStorageFullKind() {
        #expect(SharedFileStore.ioErrorKind(for: NSError(domain: NSCocoaErrorDomain, code: NSFileWriteOutOfSpaceError)) == .storageFull)
        #expect(SharedFileStore.ioErrorKind(for: NSError(domain: NSPOSIXErrorDomain, code: Int(ENOSPC))) == .storageFull)
        let wrapped = NSError(domain: NSCocoaErrorDomain, code: 512, userInfo: [
            NSUnderlyingErrorKey: NSError(domain: NSPOSIXErrorDomain, code: Int(ENOSPC)),
        ])
        #expect(SharedFileStore.ioErrorKind(for: wrapped) == .storageFull)
        #expect(SharedFileStore.ioErrorKind(for: NSError(domain: NSCocoaErrorDomain, code: 513)) == .other)
    }

    @Test func deletePartialDeleteLocalDeleteSessionAndClearAll() {
        let dir = tempRoot(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = SharedFileStore(root: dir)
        let content = pattern(1000)
        let hash = Data(SHA256.hash(data: content))

        // DeletePartial clears .part/.meta.
        #expect(store.writeChunk(writeEffect(opID: 1, sessionID: "s1", fileID: "p1", offset: 0, data: content, total: 2000, committedOffset: 1000, sha256: hash, resetPartial: true)).ok)
        store.deletePartial(sessionID: "s1", fileID: "p1")
        #expect(store.scanLocal(opID: 2).entries.isEmpty)

        // DeleteLocal clears committed copy.
        #expect(store.writeChunk(writeEffect(opID: 3, sessionID: "s1", fileID: "f1", offset: 0, data: content, total: 1000, committedOffset: 1000, sha256: hash, resetPartial: true)).ok)
        #expect(store.verifyAndCommit(verifyEffect(opID: 4, sessionID: "s1", fileID: "f1", total: 1000, sha256: hash)).ok)
        #expect(store.existingLocalURL(key, name: "clip.mp4") != nil)
        store.deleteLocal(sessionID: "s1", fileID: "f1")
        #expect(store.existingLocalURL(key, name: "clip.mp4") == nil)

        // DeleteSession & ClearAll.
        #expect(store.writeChunk(writeEffect(opID: 5, sessionID: "s1", fileID: "f1", offset: 0, data: content, total: 1000, committedOffset: 1000, sha256: hash, resetPartial: true)).ok)
        #expect(store.verifyAndCommit(verifyEffect(opID: 6, sessionID: "s1", fileID: "f1", total: 1000, sha256: hash)).ok)
        #expect(store.writeChunk(writeEffect(opID: 7, sessionID: "s2", fileID: "f2", offset: 0, data: content, total: 1000, committedOffset: 1000, sha256: hash, resetPartial: true)).ok)
        #expect(store.verifyAndCommit(verifyEffect(opID: 8, sessionID: "s2", fileID: "f2", total: 1000, sha256: hash)).ok)
        store.deleteSession(sessionID: "s1")
        #expect(store.scanLocal(opID: 9).entries.map(\.sessionID) == ["s2"])
        store.clearAll()
        #expect(store.scanLocal(opID: 10).entries.isEmpty)
        #expect(store.usedBytes() == 0)
    }

    @Test func writeChunkReportsIOFailureInsteadOfChecksumMismatchWhenPartialMissing() {
        let dir = tempRoot(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = SharedFileStore(root: dir)
        let content = pattern(1000)
        let hash = Data(SHA256.hash(data: content))

        let res = store.writeChunk(writeEffect(
            opID: 1, offset: 500, data: content[500..<1000], total: 1000,
            committedOffset: 1000, sha256: hash, resetPartial: false))
        #expect(!res.ok)
        #expect(res.errorKind == .other)
    }

    @Test func verifyReadIOErrorReportsOtherAndPreservesPartial() throws {
        let dir = tempRoot(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = SharedFileStore(root: dir)
        let content = pattern(1200)
        let hash = Data(SHA256.hash(data: content))

        #expect(store.writeChunk(writeEffect(
            opID: 1, offset: 0, data: content, total: 1200,
            committedOffset: 1200, sha256: hash, resetPartial: true)).ok)

        let part = dir.appending(path: "s1/f1/.part")
        try FileManager.default.setAttributes([.posixPermissions: 0o000], ofItemAtPath: part.path)
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: part.path) }

        let v = store.verifyAndCommit(verifyEffect(opID: 2, total: 1200, sha256: hash))
        #expect(!v.ok)
        #expect(v.errorKind == .other)
        #expect(FileManager.default.fileExists(atPath: part.path))

        // Once readable again, the preserved partial commits cleanly without redownloading.
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: part.path)
        let retry = store.verifyAndCommit(verifyEffect(opID: 3, total: 1200, sha256: hash))
        #expect(retry.ok)
    }

    @Test func verifyLeavesPartIntactWhenMetaWriteFails() throws {
        let dir = tempRoot(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = SharedFileStore(root: dir)
        let content = pattern(1000)
        let hash = Data(SHA256.hash(data: content))

        #expect(store.writeChunk(writeEffect(
            opID: 1, offset: 0, data: content, total: 1000,
            committedOffset: 1000, sha256: hash, resetPartial: true)).ok)

        let fm = FileManager.default
        let fileDir = dir.appending(path: "s1/f1", directoryHint: .isDirectory)
        let meta = fileDir.appending(path: ".meta")
        let part = fileDir.appending(path: ".part")
        // Replace .meta with a non-empty directory so atomic writeMeta fails deterministically.
        try fm.removeItem(at: meta)
        try fm.createDirectory(at: meta, withIntermediateDirectories: true)
        try Data("lock".utf8).write(to: meta.appending(path: "blocker"))

        let res = store.verifyAndCommit(verifyEffect(opID: 2, total: 1000, sha256: hash))
        #expect(!res.ok)
        #expect(res.errorKind == .other)
        #expect(fm.fileExists(atPath: part.path))
        #expect(store.existingLocalURL(key, name: "clip.mp4") == nil)
    }

    @Test func folderAndFileNamesStaySafe() {
        #expect(sharedFileFolderName("a1b2-c_3") == "a1b2-c_3")
        #expect(sharedFileFolderName("../x/y") == "___x_y")
        #expect(sharedFileFolderName("é") == "_")
        #expect(safeLocalSharedFileName("../evil/clip.mp4") == "clip.mp4")
        #expect(safeLocalSharedFileName("..") == "file")
    }
}
