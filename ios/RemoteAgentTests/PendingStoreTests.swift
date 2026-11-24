import CryptoKit
import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

@Suite struct PendingStoreTests {
    let dir = FileManager.default.temporaryDirectory.appending(path: "ra-pending-\(UUID().uuidString)", directoryHint: .isDirectory)
    let vault = Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")

    private func store(limit: Int64 = PendingPolicy.draftLimit) -> PendingStore {
        PendingStore(directory: dir, vault: vault, limit: limit)
    }

    private func cleanUp() {
        try? FileManager.default.removeItem(at: dir)
        try? vault.clear()
    }

    private func filesOnDisk() -> [URL] {
        guard let walk = FileManager.default.enumerator(at: dir, includingPropertiesForKeys: [.isRegularFileKey]) else { return [] }
        return walk.compactMap { $0 as? URL }.filter { (try? $0.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true }
    }

    @Test func recordsAndBlobsRoundTripEncryptedWithNoPlaintextOnDisk() throws {
        defer { cleanUp() }
        let s = store()
        let draftPayload = Data("SECRET-DRAFT-TEXT-PAYLOAD".utf8)
        let outboxPayload = Data("SECRET-OUTBOX-PAYLOAD".utf8)
        #expect(s.write(opID: 1, bucket: .draft, key: "draft:new:/Users/me/secret-proj", data: draftPayload).ok)
        #expect(s.write(opID: 2, bucket: .outbox, key: "outbox:s1:req1", data: outboxPayload).ok)

        let readDraft = s.read(opID: 3, bucket: .draft, key: "draft:new:/Users/me/secret-proj")
        #expect(readDraft.found && readDraft.data == draftPayload && readDraft.errorKind == .unspecified)
        let readOutbox = s.read(opID: 4, bucket: .outbox, key: "outbox:s1:req1")
        #expect(readOutbox.found && readOutbox.data == outboxPayload)
        let missing = s.read(opID: 5, bucket: .draft, key: "draft:session:none")
        #expect(!missing.found && missing.errorKind == .unspecified)

        // Chunked encrypted blob > 256 KiB spans multiple GCM chunks.
        let bigBlob = Data((0..<(300 * 1024)).map { UInt8(($0 * 31) & 0xFF) })
        let blobRef = try s.storeBlob(bigBlob, prefix: "prep")
        #expect(blobRef.hasPrefix("blob:prep-"))
        #expect(try s.readBlob(blobRef) == bigBlob)
        let slice = try s.readBlobChunk(blobRef, offset: 256 * 1024 - 10, length: 25, expectedSizeBytes: Int64(bigBlob.count))
        #expect(slice == Data(bigBlob[(256 * 1024 - 10)..<(256 * 1024 + 15)]))
        let hashed = try s.hashBlob(blobRef, maxBytes: Int64(bigBlob.count))
        #expect(hashed.sizeBytes == Int64(bigBlob.count) && hashed.sha256 == Data(SHA256.hash(data: bigBlob)))

        #expect(throws: AttachmentFileIOError(.sourceChanged)) {
            _ = try s.readBlobChunk(blobRef, offset: 0, length: 4, expectedSizeBytes: Int64(bigBlob.count - 1))
        }

        // Nothing on disk reveals plaintext payload or project path.
        let files = filesOnDisk()
        #expect(files.count == 3)
        for file in files {
            let raw = try Data(contentsOf: file)
            #expect(raw.range(of: Data("SECRET-DRAFT".utf8)) == nil)
            #expect(raw.range(of: Data("SECRET-OUTBOX".utf8)) == nil)
            #expect(!file.path.contains("secret-proj"))
        }
        #expect(try dir.resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup == true)
        #expect(try vault.read(.pendingKey)?.count == 32)

        // Fresh store instance opens records and enumerates local_refs.
        let listed = store().list(opID: 6)
        #expect(listed.errorKind == .unspecified)
        #expect(listed.entries.count == 2)
        #expect(listed.localRefs == [blobRef])
    }

    @Test func corruptRecordsAndLegacyEntriesAreReportedAndDeletedCleanly() throws {
        defer { cleanUp() }
        let s = store()
        #expect(s.write(opID: 1, bucket: .draft, key: "draft:session:s1", data: Data("ok".utf8)).ok)
        let rec = try #require(filesOnDisk().first)
        try Data("damaged-ciphertext".utf8).write(to: rec)

        // Plant a legacy pre-v1 folder at root.
        let legacyFolder = dir.appending(path: "abcdef0123456789", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: legacyFolder, withIntermediateDirectories: true)
        try Data("old".utf8).write(to: legacyFolder.appending(path: "order"))

        let listed = s.list(opID: 2)
        #expect(listed.errorKind == .unspecified)
        #expect(listed.entries.count == 2)
        let allCorrupt = listed.entries.allSatisfy { $0.corrupt }
        #expect(allCorrupt)

        for entry in listed.entries {
            s.delete(bucket: entry.bucket, key: entry.key)
        }
        let after = s.list(opID: 3)
        #expect(after.entries.isEmpty && after.localRefs.isEmpty)
    }

    @Test func unavailableKeyReportsErrorWithoutDeletingOrOverwritingCiphertext() throws {
        defer { cleanUp() }
        let first = store()
        #expect(first.write(opID: 1, bucket: .draft, key: "draft:session:s1", data: Data("keep".utf8)).ok)
        try vault.delete(.pendingKey)

        let second = store()
        let before = try Data(contentsOf: #require(filesOnDisk().first))
        let listed = second.list(opID: 2)
        #expect(listed.errorKind == .other)
        let readRes = second.read(opID: 3, bucket: .draft, key: "draft:session:s1")
        #expect(!readRes.found && readRes.errorKind == .other)
        let writeRes = second.write(opID: 4, bucket: .draft, key: "draft:session:s2", data: Data("new".utf8))
        #expect(!writeRes.ok && writeRes.errorKind == .other)
        #expect(filesOnDisk().count == 1)
        #expect(try Data(contentsOf: #require(filesOnDisk().first)) == before)
    }

    @Test func quotaLimitRefusesOverflowWithStorageFull() throws {
        defer { cleanUp() }
        let s = store(limit: 200)
        #expect(s.write(opID: 1, bucket: .draft, key: "a", data: Data(repeating: 1, count: 80)).ok)
        let overflow = s.write(opID: 2, bucket: .draft, key: "b", data: Data(repeating: 2, count: 160))
        #expect(!overflow.ok && overflow.errorKind == .storageFull)
        #expect(s.read(opID: 3, bucket: .draft, key: "a").data == Data(repeating: 1, count: 80))
        #expect(!s.read(opID: 4, bucket: .draft, key: "b").found)
    }

    @Test func bookmarksAndVideosRoundTripAndReleaseCleanly() throws {
        defer { cleanUp() }
        let s = store()
        let tmpFile = FileManager.default.temporaryDirectory.appending(path: "ra-bmk-\(UUID().uuidString).mp4")
        try Data("video-bytes".utf8).write(to: tmpFile)
        defer { try? FileManager.default.removeItem(at: tmpFile) }

        let bmkData = try PickedFile.bookmark(for: tmpFile)
        let bmkRef = try s.storeBookmark(bmkData)
        #expect(try s.readBookmark(bmkRef) == bmkData)
        try s.updateBookmark(bmkRef, bookmark: Data("refreshed".utf8))
        #expect(try s.readBookmark(bmkRef) == Data("refreshed".utf8))

        let vid = try s.storeVideo(from: tmpFile, filename: "clip.mp4")
        #expect(vid.ref.hasPrefix("vid:"))
        #expect(try Data(contentsOf: try s.videoURL(vid.ref)) == Data("video-bytes".utf8))

        let listed = s.list(opID: 1)
        #expect(Set(listed.localRefs) == Set([bmkRef, vid.ref]))

        s.releaseLocal(refs: [bmkRef, vid.ref])
        #expect(s.list(opID: 2).localRefs.isEmpty)
    }

    @Test func staleScopeCannotTouchNextAccountAfterClearAll() throws {
        defer { cleanUp() }
        let s = store()
        let first = s.scoped()
        #expect(first.write(opID: 1, bucket: .draft, key: "k", data: Data("one".utf8)).ok)
        try s.clearAll()

        #expect(!first.write(opID: 2, bucket: .draft, key: "k", data: Data("two".utf8)).ok)
        #expect(filesOnDisk().isEmpty)
        #expect(try vault.read(.pendingKey) == nil)

        let second = s.scoped()
        #expect(!second.read(opID: 3, bucket: .draft, key: "k").found)
        #expect(second.write(opID: 4, bucket: .draft, key: "k", data: Data("new".utf8)).ok)
        first.delete(bucket: .draft, key: "k")
        #expect(second.read(opID: 5, bucket: .draft, key: "k").data == Data("new".utf8))
    }

    @Test func loggingOutErasesKeysCacheAndPendingStore() async throws {
        defer { cleanUp() }
        let cacheRoot = FileManager.default.temporaryDirectory.appending(path: "ra-cache-\(UUID().uuidString)", directoryHint: .isDirectory)
        defer { try? FileManager.default.removeItem(at: cacheRoot) }
        let s = store()
        _ = vault.executeStorage(.with {
            $0.op = .writeCas
            $0.bucket = .record
            $0.key = "record"
            $0.createOnly = true
            $0.nextRevision = "r1"
            $0.data = Data("rec".utf8)
        })
        #expect(s.write(opID: 1, bucket: .draft, key: "k", data: Data([1, 2, 3])).ok)
        let cache = try CipherStore.open(root: cacheRoot, account: "acct")
        await cache.putBlob(name: "x", data: Data([1]))
        try cache.close()

        try LocalAccountData.erase(vault: vault, cacheRoot: cacheRoot, pending: s)
        #expect(!vault.hasPersistedState())
        #expect(!FileManager.default.fileExists(atPath: cacheRoot.path))
        #expect(!FileManager.default.fileExists(atPath: dir.path))
    }
}
