import Foundation
import GRDB
import RACore
import RAProto
import Testing

@testable import RemoteAgent

private struct Stop: Error {}

@Suite struct CipherStoreTests {
    let root = FileManager.default.temporaryDirectory.appending(path: "ra-cache-\(UUID().uuidString)", directoryHint: .isDirectory)
    let account = CipherStore.accountKey(server: "https://ra.example.com", deviceId: "device-1")

    private func open(account: String? = nil, imageLimit: Int64 = CipherStore.defaultImageLimit, clock: TestClock? = nil) throws -> CipherStore {
        let clock = clock ?? TestClock()
        return try CipherStore.open(root: root, account: account ?? self.account, imageLimit: imageLimit, now: { clock.now })
    }

    private func cleanUp() { try? FileManager.default.removeItem(at: root) }

    private func machine(_ id: String, key: UInt8 = 1) -> PBMachine {
        var m = PBMachine()
        m.id = id
        m.meta = envelope(key)
        return m
    }

    private func session(_ id: String, machine: String, rev: UInt8 = 1) -> PBRemoteSession {
        var s = PBRemoteSession()
        s.sessionID = id
        s.machineID = machine
        s.meta = envelope(rev)
        return s
    }

    private func event(_ seq: UInt64, fill: UInt8 = 0) -> PBStoredEvent {
        var e = PBStoredEvent()
        e.seq = seq
        e.envelope = envelope(fill == 0 ? UInt8(truncatingIfNeeded: seq) : fill)
        return e
    }

    private func envelope(_ fill: UInt8) -> PBEnvelope {
        var e = PBEnvelope()
        e.header = Data([fill, 1, 2])
        e.nonce = Data(repeating: fill, count: 12)
        e.ciphertext = Data(repeating: fill, count: 40)
        return e
    }

    @Test func machinesArePutReplacedAndRemoved() async throws {
        defer { cleanUp() }
        let store = try open()
        #expect(await store.machines().isEmpty)
        await store.putMachine(machine("a"))
        await store.putMachine(machine("b"))
        await store.putMachine(machine("a", key: 9))
        let all = await store.machines().sorted { $0.id < $1.id }
        #expect(all == [machine("a", key: 9), machine("b")])
        await store.removeMachine(id: "a")
        #expect(await store.machines() == [machine("b")])
    }

    @Test func removingAMachineTakesItsSessionsAndTheirEventsListedOrNot() async throws {
        defer { cleanUp() }
        let store = try open()
        await store.putMachine(machine("a"))
        await store.putMachine(machine("b"))
        await store.putSession(session("s1", machine: "a"))
        await store.keepOwner(session("s2", machine: "a"))
        await store.putSession(session("s3", machine: "b"))
        for sid in ["s1", "s2", "s3"] { await store.putEvents(sessionId: sid, events: [event(1), event(2)]) }
        await store.removeMachine(id: "a")
        #expect(await store.sessions() == [session("s3", machine: "b")])
        #expect(await store.events(sessionId: "s1").isEmpty)
        #expect(await store.events(sessionId: "s2").isEmpty)
        #expect(await store.cursor(sessionId: "s1") == 0)
        #expect(await store.cursor(sessionId: "s2") == 0)
        #expect(await store.events(sessionId: "s3").count == 2)
        #expect(await store.cursor(sessionId: "s3") == 2)
    }

    @Test func sessionsAreListedUnlistedAndRemoved() async throws {
        defer { cleanUp() }
        let store = try open()
        await store.putSession(session("s1", machine: "a"))
        await store.putSession(session("s2", machine: "a"))
        await store.putSession(session("s1", machine: "a", rev: 5))
        #expect(await store.sessions().sorted { $0.sessionID < $1.sessionID } == [session("s1", machine: "a", rev: 5), session("s2", machine: "a")])

        // Off the list, its events stay.
        await store.putEvents(sessionId: "s1", events: [event(1)])
        await store.unlistSession(id: "s1")
        #expect(await store.sessions() == [session("s2", machine: "a")])
        #expect(await store.events(sessionId: "s1") == [event(1)])
        // Listed again when the server lists it again.
        await store.putSession(session("s1", machine: "a", rev: 6))
        #expect(await store.sessions().count == 2)

        // A session that no longer exists goes with its events.
        await store.removeSession(id: "s1")
        #expect(await store.sessions() == [session("s2", machine: "a")])
        #expect(await store.events(sessionId: "s1").isEmpty)
        #expect(await store.cursor(sessionId: "s1") == 0)
    }

    @Test func keepOwnerNotesASessionWithoutListingItOrTouchingOneAlreadyNoted() async throws {
        defer { cleanUp() }
        let store = try open()
        await store.keepOwner(session("later", machine: "a"))
        #expect(await store.sessions().isEmpty)
        await store.putSession(session("listed", machine: "a", rev: 3))
        await store.keepOwner(session("listed", machine: "other", rev: 4))
        #expect(await store.sessions() == [session("listed", machine: "a", rev: 3)])
    }

    @Test func eventsComeBackInOrderAndAStoredOneIsNotReplaced() async throws {
        defer { cleanUp() }
        let store = try open()
        await store.putEvents(sessionId: "s", events: [event(3), event(1)])
        await store.putEvents(sessionId: "s", events: [event(2), event(3, fill: 99)])
        await store.putEvents(sessionId: "other", events: [event(7)])
        #expect(await store.events(sessionId: "s") == [event(1), event(2), event(3)])
        #expect(await store.events(sessionId: "other") == [event(7)])
        #expect(await store.events(sessionId: "none").isEmpty)
        // The cursor is the highest number stored, and an older batch does not take it back.
        #expect(await store.cursor(sessionId: "s") == 3)
        await store.putEvents(sessionId: "s", events: [event(2)])
        #expect(await store.cursor(sessionId: "s") == 3)
        #expect(await store.cursor(sessionId: "none") == 0)
        // Nothing to store is not an error and makes no cursor.
        await store.putEvents(sessionId: "empty", events: [])
        #expect(await store.cursor(sessionId: "empty") == 0)
    }

    @Test func eventsAndTheirCursorAreWrittenTogetherOrNotAtAll() async throws {
        defer { cleanUp() }
        let store = try open()
        await store.putEvents(sessionId: "s", events: [event(1), event(2)])
        await #expect(throws: Stop.self) {
            try await store.storeEvents(sessionId: "s", events: [self.event(3), self.event(4)]) { throw Stop() }
        }
        #expect(await store.events(sessionId: "s") == [event(1), event(2)])
        #expect(await store.cursor(sessionId: "s") == 2)
        // A first batch that fails leaves neither events nor a cursor.
        await #expect(throws: Stop.self) {
            try await store.storeEvents(sessionId: "new", events: [self.event(1)]) { throw Stop() }
        }
        #expect(await store.events(sessionId: "new").isEmpty)
        #expect(await store.cursor(sessionId: "new") == 0)
        // And the same batch goes in whole when nothing stops it.
        try await store.storeEvents(sessionId: "s", events: [event(3), event(4)])
        #expect(await store.events(sessionId: "s").map(\.seq) == [1, 2, 3, 4])
        #expect(await store.cursor(sessionId: "s") == 4)
    }

    @Test func blobsAreKeptByName() async throws {
        defer { cleanUp() }
        let store = try open()
        #expect(await store.blob(name: "project-prefs") == nil)
        await store.putBlob(name: "project-prefs", data: Data([1, 2, 3]))
        await store.putBlob(name: "other", data: Data())
        await store.putBlob(name: "project-prefs", data: Data([4]))
        #expect(await store.blob(name: "project-prefs") == Data([4]))
        #expect(await store.blob(name: "other") == Data())
    }

    @Test func imageEnvelopesAreKeptAndTheLeastRecentlyUsedGoFirst() async throws {
        defer { cleanUp() }
        let clock = TestClock(Date(timeIntervalSince1970: 1_800_000_000))
        let store = try open(imageLimit: 250, clock: clock)
        #expect(await store.image(key: "a") == nil)
        await store.putImage(key: "a", envelope: Data(repeating: 1, count: 100))
        clock.advance(10)
        await store.putImage(key: "b", envelope: Data(repeating: 2, count: 100))
        #expect(store.imageBytes == 200)
        clock.advance(10)
        // Showing the older one makes it the newer.
        #expect(await store.image(key: "a") == Data(repeating: 1, count: 100))
        clock.advance(10)
        await store.putImage(key: "c", envelope: Data(repeating: 3, count: 100))
        #expect(await store.image(key: "b") == nil)
        #expect(await store.image(key: "a") == Data(repeating: 1, count: 100))
        #expect(await store.image(key: "c") == Data(repeating: 3, count: 100))
        #expect(store.imageBytes == 200)
        // Putting one again replaces it rather than adding to the total.
        await store.putImage(key: "c", envelope: Data(repeating: 4, count: 50))
        #expect(store.imageBytes == 150)
        store.clearImages()
        #expect(store.imageBytes == 0)
        #expect(await store.image(key: "a") == nil)
    }

    @Test func aStoreOpenedAgainHasWhatWasStored() async throws {
        defer { cleanUp() }
        var store = try open()
        await store.putMachine(machine("a"))
        await store.putSession(session("s1", machine: "a"))
        await store.keepOwner(session("s2", machine: "a"))
        await store.putEvents(sessionId: "s1", events: [event(1), event(2)])
        await store.putBlob(name: "project-prefs", data: Data([7]))
        await store.putImage(key: "img", envelope: Data([8, 9]))
        try store.close()

        store = try open()
        #expect(await store.machines() == [machine("a")])
        #expect(await store.sessions() == [session("s1", machine: "a")])
        #expect(await store.events(sessionId: "s1") == [event(1), event(2)])
        #expect(await store.cursor(sessionId: "s1") == 2)
        #expect(await store.blob(name: "project-prefs") == Data([7]))
        #expect(await store.image(key: "img") == Data([8, 9]))
        // The unlisted owner is still known: its machine going takes its events.
        await store.putEvents(sessionId: "s2", events: [event(1)])
        await store.removeMachine(id: "a")
        #expect(await store.events(sessionId: "s2").isEmpty)
        try store.close()
    }

    @Test func aDatabaseOfAnotherSchemaVersionIsEmptiedNotMigrated() async throws {
        defer { cleanUp() }
        var store = try open()
        await store.putMachine(machine("a"))
        await store.putEvents(sessionId: "s", events: [event(1)])
        try store.close()
        let queue = try DatabaseQueue(path: store.directory.appending(path: "cache.sqlite").path)
        try await queue.write { db in try db.execute(sql: "PRAGMA user_version = \(CipherStore.schemaVersion + 1)") }
        try queue.close()

        store = try open()
        #expect(await store.machines().isEmpty)
        #expect(await store.events(sessionId: "s").isEmpty)
        #expect(await store.cursor(sessionId: "s") == 0)
        await store.putMachine(machine("b"))
        #expect(await store.machines() == [machine("b")])
        try store.close()
    }

    @Test func eachAccountHasItsOwnCacheAndLoggingOutRemovesThem() async throws {
        defer { cleanUp() }
        let other = CipherStore.accountKey(server: "https://ra.example.com", deviceId: "device-2")
        #expect(other != account)
        // The directory's name gives away neither the server nor the device.
        #expect(!account.contains("example") && !account.contains("device"))
        let a = try open()
        let b = try open(account: other)
        await a.putMachine(machine("only-a"))
        #expect(await b.machines().isEmpty)
        #expect(a.directory != b.directory)

        try b.erase()
        #expect(!FileManager.default.fileExists(atPath: b.directory.path))
        #expect(await a.machines() == [machine("only-a")])

        try a.close()
        try CipherStore.eraseAll(root: root)
        #expect(!FileManager.default.fileExists(atPath: root.path))
        // Nothing there is not an error.
        try CipherStore.eraseAll(root: root)
        #expect(try await open().machines().isEmpty)
    }

    @Test func theCacheIsLeftOutOfBackupsAndProtectedUntilFirstUnlock() async throws {
        defer { cleanUp() }
        let store = try open()
        await store.putEvents(sessionId: "s", events: [event(1)])
        await store.putImage(key: "img", envelope: Data([1]))
        for dir in [root, store.directory, store.directory.appending(path: "images")] {
            #expect(try dir.resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup == true)
        }
        // The database in write-ahead mode is three files; all of them are there and none is unprotected.
        var files = ["cache.sqlite", "cache.sqlite-wal", "cache.sqlite-shm"].map { store.directory.appending(path: $0) }
        files += try FileManager.default.contentsOfDirectory(at: store.directory.appending(path: "images"), includingPropertiesForKeys: nil)
        #expect(files.count == 4)
        for file in files + [store.directory] {
            #expect(FileManager.default.fileExists(atPath: file.path))
            let protection = try FileManager.default.attributesOfItem(atPath: file.path)[.protectionKey] as? FileProtectionType
            #if targetEnvironment(simulator)
            // The simulator has no data protection: it reports no class for any file, whatever was asked for.
            #expect(protection == nil || protection == .completeUntilFirstUserAuthentication, "\(file.lastPathComponent)")
            #else
            #expect(protection == .completeUntilFirstUserAuthentication, "\(file.lastPathComponent): \(String(describing: protection))")
            #endif
        }
    }

    @Test func onlyWhatItWasGivenIsOnDisk() async throws {
        defer { cleanUp() }
        let store = try open()
        var e = PBStoredEvent()
        e.seq = 1
        e.envelope.ciphertext = Data("CIPHERTEXT-MARKER".utf8)
        await store.putEvents(sessionId: "session-id", events: [e])
        try store.close()
        // The envelope is there as it was given (ciphertext from the server), with the routing ID beside it.
        let reopened = try open()
        #expect(await reopened.events(sessionId: "session-id") == [e])
        try reopened.close()
    }
}
