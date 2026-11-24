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

    @Test func confirmedMissingKeySelfHealsUndecryptableCiphertext() throws {
        defer { cleanUp() }
        let first = store()
        #expect(first.write(opID: 1, bucket: .draft, key: "draft:session:s1", data: Data("keep".utf8)).ok)
        let oldBlobRef = try first.storeBlob(Data("old-blob".utf8), prefix: "prep")
        try vault.delete(.pendingKey)

        // Keychain explicitly confirms errSecItemNotFound -> old ciphertext can never decrypt:
        // list() reports records as corrupt and enumerates blobs for Core GC with no error.
        let second = store()
        let listed = second.list(opID: 2)
        #expect(listed.errorKind == .unspecified)
        #expect(listed.entries.count == 1 && listed.entries.first?.corrupt == true)
        #expect(listed.localRefs == [oldBlobRef])

        let readRes = second.read(opID: 3, bucket: .draft, key: "draft:session:s1")
        #expect(!readRes.found && readRes.errorKind == .corruptStore)

        // Writing a new draft wipes undecryptable v1 remnants and generates a fresh key.
        let writeRes = second.write(opID: 4, bucket: .draft, key: "draft:session:s2", data: Data("healed".utf8))
        #expect(writeRes.ok)
        #expect(try vault.read(.pendingKey)?.count == 32)
        let readHealed = second.read(opID: 5, bucket: .draft, key: "draft:session:s2")
        #expect(readHealed.found && readHealed.data == Data("healed".utf8))
        #expect(second.list(opID: 6).localRefs.isEmpty)
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

    @Test func bookmarksAndStreamedVideosRoundTripEncryptedAndReleaseCleanly() throws {
        defer { cleanUp() }
        let s = store()
        let tmpFile = FileManager.default.temporaryDirectory.appending(path: "ra-bmk-\(UUID().uuidString).mp4")
        let videoBytes = Data("SECRET-VIDEO-MARKER-".utf8) + Data((0..<(300 * 1024)).map { UInt8(($0 * 17) & 0xFF) })
        try videoBytes.write(to: tmpFile)
        defer { try? FileManager.default.removeItem(at: tmpFile) }

        let bmkData = try PickedFile.bookmark(for: tmpFile)
        let bmkRef = try s.storeBookmark(bmkData)
        #expect(try s.readBookmark(bmkRef) == bmkData)
        try s.updateBookmark(bmkRef, bookmark: Data("refreshed".utf8))
        #expect(try s.readBookmark(bmkRef) == Data("refreshed".utf8))

        let vidRef = try s.storeBlob(from: tmpFile, prefix: "vid")
        #expect(vidRef.hasPrefix("blob:vid-"))
        #expect(try s.readBlob(vidRef) == videoBytes)
        let slice = try s.readBlobChunk(vidRef, offset: 256 * 1024 - 8, length: 32, expectedSizeBytes: Int64(videoBytes.count))
        #expect(slice == Data(videoBytes[(256 * 1024 - 8)..<(256 * 1024 + 24)]))
        let hashed = try s.hashBlob(vidRef, maxBytes: Int64(videoBytes.count))
        #expect(hashed.sizeBytes == Int64(videoBytes.count) && hashed.sha256 == Data(SHA256.hash(data: videoBytes)))

        // Zero plaintext video bytes stored on disk.
        for file in filesOnDisk() {
            let raw = try Data(contentsOf: file)
            #expect(raw.range(of: Data("SECRET-VIDEO-MARKER".utf8)) == nil)
        }

        let listed = s.list(opID: 1)
        #expect(Set(listed.localRefs) == Set([bmkRef, vidRef]))

        s.releaseLocal(refs: [bmkRef, vidRef])
        #expect(s.list(opID: 2).localRefs.isEmpty)
    }

    @Test func lockedKeychainAndCorruptKeyRefuseToDeleteOrOverwrite() throws {
        defer { cleanUp() }
        let first = store()
        #expect(first.write(opID: 1, bucket: .draft, key: "draft:session:s1", data: Data("keep-me".utf8)).ok)
        let recFile = try #require(filesOnDisk().first)
        let beforeBytes = try Data(contentsOf: recFile)
        let strayTmp = dir.appending(path: "v1/blobs/.pending.blob.tmp")
        try FileManager.default.createDirectory(at: strayTmp.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data("tmp".utf8).write(to: strayTmp)

        // Simulated locked Keychain (`errSecInteractionNotAllowed`): must return `.other`,
        // must NOT mark entries corrupt, must NOT delete files or overwrite key.
        let lockedVault = Vault(
            service: vault.service,
            keychain: KeychainOps(
                copyMatching: { _, _ in errSecInteractionNotAllowed },
                add: { _, _ in
                    Issue.record("locked Keychain must never add/overwrite pendingKey")
                    return errSecInteractionNotAllowed
                },
                update: { _, _ in
                    Issue.record("locked Keychain must never update pendingKey")
                    return errSecInteractionNotAllowed
                },
                delete: { _ in
                    Issue.record("locked Keychain must never delete pendingKey")
                    return errSecInteractionNotAllowed
                }
            )
        )
        let lockedStore = PendingStore(directory: dir, vault: lockedVault)
        let listed = lockedStore.list(opID: 2)
        #expect(listed.errorKind == .other && listed.entries.isEmpty)
        let readRes = lockedStore.read(opID: 3, bucket: .draft, key: "draft:session:s1")
        #expect(!readRes.found && readRes.errorKind == .other)
        let writeRes = lockedStore.write(opID: 4, bucket: .draft, key: "draft:session:s2", data: Data("new".utf8))
        #expect(!writeRes.ok && writeRes.errorKind == .other)
        #expect(try Data(contentsOf: recFile) == beforeBytes)
        #expect(FileManager.default.fileExists(atPath: strayTmp.path))

        // Corrupt key length (16 bytes instead of 32) must also fail safely without overwriting Keychain.
        try vault.writeRawKeychainItemForTest(account: Vault.Item.pendingKey.rawValue, data: Data(repeating: 0xAB, count: 16))
        let corruptStore = store()
        #expect(corruptStore.list(opID: 5).errorKind == .other)
        #expect(corruptStore.write(opID: 6, bucket: .draft, key: "draft:session:s3", data: Data("x".utf8)).errorKind == .other)
        #expect(try vault.read(.pendingKey) == Data(repeating: 0xAB, count: 16))
    }

    @Test func residualTempFilesAreCleanedOnStartupList() throws {
        defer { cleanUp() }
        let s = store()
        #expect(s.write(opID: 1, bucket: .draft, key: "draft:session:s1", data: Data("ok".utf8)).ok)
        let blobTmp = dir.appending(path: "v1/blobs/.orphan.blob.tmp")
        let draftTmp = dir.appending(path: "v1/drafts/.partial.rec.tmp")
        let outboxTmp = dir.appending(path: "v1/outbox/.dat.nosync1234")
        for url in [blobTmp, draftTmp, outboxTmp] {
            try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            try Data(repeating: 7, count: 4096).write(to: url)
        }
        let usedBefore = s.used

        let listed = s.list(opID: 2)
        #expect(listed.errorKind == .unspecified)
        #expect(listed.entries.count == 1 && listed.entries.first?.key == "draft:session:s1")
        for url in [blobTmp, draftTmp, outboxTmp] {
            #expect(!FileManager.default.fileExists(atPath: url.path))
        }
        #expect(s.used < usedBefore)
    }

    @Test func aadRejectsBucketSwapAndBlobTampering() throws {
        defer { cleanUp() }
        let s = store()
        #expect(s.write(opID: 1, bucket: .draft, key: "draft:session:s1", data: Data("payload".utf8)).ok)
        let draftFile = try #require(filesOnDisk().first { $0.path.contains("/drafts/") })
        let outboxDir = dir.appending(path: "v1/outbox", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: outboxDir, withIntermediateDirectories: true)
        let swapped = outboxDir.appending(path: draftFile.lastPathComponent)
        try FileManager.default.copyItem(at: draftFile, to: swapped)

        // Swapping a draft record into outbox fails bucket AAD verification.
        let listed = s.list(opID: 2)
        #expect(listed.errorKind == .unspecified)
        let outboxEntry = try #require(listed.entries.first { $0.bucket == .outbox })
        #expect(outboxEntry.corrupt)

        // Renaming a blob file or tampering its header plainSize fails blob AAD verification.
        let blobRef = try s.storeBlob(Data("hello-blob".utf8), prefix: "prep")
        let blobID = String(blobRef.dropFirst("blob:".count))
        let blobsDir = dir.appending(path: "v1/blobs", directoryHint: .isDirectory)
        let origBlob = blobsDir.appending(path: "\(blobID).blob")
        let renamedBlob = blobsDir.appending(path: "prep-renamed.blob")
        try FileManager.default.copyItem(at: origBlob, to: renamedBlob)
        #expect(throws: (any Error).self) {
            _ = try s.readBlob("blob:prep-renamed")
        }

        // Tamper plainSize in header (bytes 8..16) from 10 to 9.
        var tampered = try Data(contentsOf: origBlob)
        var fakeSize = UInt64(9).bigEndian
        withUnsafeBytes(of: &fakeSize) { tampered.replaceSubrange(8..<16, with: $0) }
        try tampered.write(to: origBlob)
        #expect(throws: (any Error).self) {
            _ = try s.readBlob(blobRef)
        }
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
