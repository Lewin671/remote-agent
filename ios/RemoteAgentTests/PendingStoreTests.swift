import Foundation
import RACore
import Testing

@testable import RemoteAgent

@Suite struct PendingStoreTests {
    let dir = FileManager.default.temporaryDirectory.appending(path: "ra-pending-\(UUID().uuidString)", directoryHint: .isDirectory)
    let vault = Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")
    let suite = "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)"
    let clock = TestClock()

    private func store(limit: Int64 = PendingPolicy.draftLimit) -> PendingStore {
        let clock = clock
        return PendingStore(directory: dir, vault: vault, defaults: UserDefaults(suiteName: suite)!, limit: limit, now: { clock.now })
    }

    private func cleanUp() {
        try? FileManager.default.removeItem(at: dir)
        try? vault.clear()
        UserDefaults(suiteName: suite)?.removePersistentDomain(forName: suite)
    }

    private func filesOnDisk() -> [URL] {
        guard let walk = FileManager.default.enumerator(at: dir, includingPropertiesForKeys: [.isRegularFileKey]) else { return [] }
        return walk.compactMap { $0 as? URL }.filter { (try? $0.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true }
    }

    @Test func whatIsPutComesBackAndIsNotReadableOnDisk() throws {
        defer { cleanUp() }
        let s = store()
        let plain = Data("PLAINTEXT-IMAGE-BYTES-PLAINTEXT-IMAGE-BYTES".utf8)
        try s.put(folder: "draft:/Users/me/Code/foo", name: "1.jpg", data: plain)
        #expect(s.get(folder: "draft:/Users/me/Code/foo", name: "1.jpg") == plain)
        #expect(s.get(folder: "draft:/Users/me/Code/foo", name: "2.jpg") == nil)
        #expect(s.get(folder: "another", name: "1.jpg") == nil)

        let files = filesOnDisk()
        #expect(files.count == 1)
        for file in files {
            let bytes = try Data(contentsOf: file)
            // Nonce, ciphertext, tag: longer than the image, and nothing of it readable.
            #expect(bytes.count == plain.count + 12 + 16)
            #expect(bytes.range(of: Data("PLAINTEXT".utf8)) == nil)
            // Neither the draft's key (a path) nor anything of it names the folder.
            #expect(!file.path.contains("foo") && !file.path.contains("draft"))
        }
        #expect(try dir.resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup == true)
        #expect(s.used == Int64(plain.count + 28))
        // The key is in the Keychain, and a store made later opens what this one sealed.
        #expect(try vault.read(.pendingKey)?.count == 32)
        #expect(store().get(folder: "draft:/Users/me/Code/foo", name: "1.jpg") == plain)
    }

    @Test func filesAreRemovedOneByOneAndByFolder() throws {
        defer { cleanUp() }
        let s = store()
        try s.put(folder: "a", name: "1", data: Data([1]))
        try s.put(folder: "a", name: "2", data: Data([2]))
        try s.put(folder: "b", name: "1", data: Data([3]))
        s.remove(folder: "a", name: "1")
        #expect(s.get(folder: "a", name: "1") == nil)
        #expect(s.get(folder: "a", name: "2") == Data([2]))
        s.clearFolder("a")
        #expect(s.get(folder: "a", name: "2") == nil)
        #expect(s.get(folder: "b", name: "1") == Data([3]))
        // Removing what is not there does nothing.
        s.remove(folder: "none", name: "1")
        s.clearFolder("none")
    }

    @Test func storingMoreThanTheLimitIsRefusedAndNothingIsEvicted() throws {
        defer { cleanUp() }
        let s = store(limit: 200)
        try s.put(folder: "a", name: "1", data: Data(repeating: 1, count: 100))
        #expect(throws: UserError.self) { try s.put(folder: "a", name: "2", data: Data(repeating: 2, count: 100)) }
        #expect(s.get(folder: "a", name: "1") == Data(repeating: 1, count: 100))
        #expect(s.get(folder: "a", name: "2") == nil)
        // Replacing a file counts only the difference.
        try s.put(folder: "a", name: "1", data: Data(repeating: 3, count: 120))
        #expect(s.get(folder: "a", name: "1") == Data(repeating: 3, count: 120))
    }

    @Test func draftsNothingTouchedForAWeekExpireAndAreCounted() throws {
        defer { cleanUp() }
        let s = store()
        try s.put(folder: "old", name: "1.jpg", data: Data([1]))
        try s.put(folder: "old", name: "1.meta", data: Data([1]))
        try s.put(folder: "old", name: "2.meta", data: Data([1]))
        clock.advance(PendingPolicy.draftTTL - 60)
        #expect(s.expire() == 0)
        // Reading a draft is touching it.
        #expect(s.get(folder: "old", name: "1.jpg") == Data([1]))
        clock.advance(120)
        #expect(s.expire() == 0)
        try s.put(folder: "fresh", name: "1.meta", data: Data([2]))
        clock.advance(PendingPolicy.draftTTL - 30)
        // One image is one .meta file: two expired.
        #expect(s.expire() == 2)
        #expect(s.expired == 2)
        #expect(s.get(folder: "old", name: "1.jpg") == nil)
        #expect(s.get(folder: "fresh", name: "1.meta") == Data([2]))
        s.clearExpiredCount()
        #expect(s.expired == 0)
    }

    @Test func whatTheOutboxIndexNamesDoesNotExpire() throws {
        defer { cleanUp() }
        let s = store()
        try s.put(folder: "out:session-1:req-1", name: "1.meta", data: Data([1]))
        try s.put(folder: "out:session-1:req-2", name: "1.meta", data: Data([2]))
        try s.put(folder: "outbox", name: "index", data: Data("session-1\treq-1\n".utf8))
        clock.advance(PendingPolicy.draftTTL + 60)
        #expect(s.expire() == 1)
        #expect(s.get(folder: "out:session-1:req-1", name: "1.meta") == Data([1]))
        #expect(s.get(folder: "out:session-1:req-2", name: "1.meta") == nil)
        #expect(s.get(folder: "outbox", name: "index") == Data("session-1\treq-1\n".utf8))
        // Once the index no longer names it, it expires like any draft.
        try s.put(folder: "outbox", name: "index", data: Data())
        clock.advance(PendingPolicy.draftTTL + 60)
        #expect(s.expire() == 1)
        #expect(s.get(folder: "out:session-1:req-1", name: "1.meta") == nil)
    }

    @Test func loggingOutRemovesTheFilesAndTheirKey() throws {
        defer { cleanUp() }
        let s = store()
        try s.put(folder: "a", name: "1", data: Data([1]))
        let key = try #require(try vault.read(.pendingKey))
        try s.clearAll()
        #expect(!FileManager.default.fileExists(atPath: dir.path))
        #expect(try vault.read(.pendingKey) == nil)
        #expect(s.get(folder: "a", name: "1") == nil)
        // The next account's images are sealed under a new key.
        try s.put(folder: "a", name: "1", data: Data([2]))
        #expect(try vault.read(.pendingKey) != key)
        #expect(s.get(folder: "a", name: "1") == Data([2]))
    }

    @Test func withoutItsKeyWhatWasStoredIsGone() throws {
        defer { cleanUp() }
        try store().put(folder: "a", name: "1", data: Data([1]))
        try vault.delete(.pendingKey)
        let s = store()
        #expect(s.get(folder: "a", name: "1") == nil)
        try s.put(folder: "b", name: "1", data: Data([2]))
        // Making the new key cleared what the lost one sealed.
        #expect(filesOnDisk().count == 1)
        #expect(s.get(folder: "b", name: "1") == Data([2]))
    }

    @Test func loggingOutErasesKeysCacheAndPendingImages() async throws {
        defer { cleanUp() }
        let cacheRoot = FileManager.default.temporaryDirectory.appending(path: "ra-cache-\(UUID().uuidString)", directoryHint: .isDirectory)
        defer { try? FileManager.default.removeItem(at: cacheRoot) }
        let s = store()
        try vault.save(VaultAccount(server: "https://ra.example.com", root: Data(repeating: 1, count: 32), deviceSeed: Data(repeating: 2, count: 32), deviceId: "d"), notifyKey: Data(repeating: 3, count: 32))
        try s.put(folder: "a", name: "1", data: Data([1]))
        let cache = try CipherStore.open(root: cacheRoot, account: "acct")
        await cache.putBlob(name: "x", data: Data([1]))
        try cache.close()

        try LocalAccountData.erase(vault: vault, cacheRoot: cacheRoot, pending: s)
        #expect(try !vault.hasAccount())
        #expect(try vault.notifyKey() == nil)
        #expect(!FileManager.default.fileExists(atPath: cacheRoot.path))
        #expect(!FileManager.default.fileExists(atPath: dir.path))
    }
}
