// The cache between runs, the counterpart of Android's data/CacheDb.kt and
// data/ImageFiles.kt: what the server sent, as it was sent. Routing fields
// (IDs, which machine a session is of, sequence numbers) and sealed
// envelopes; nothing decrypted is written to disk.
//
// One SQLite database per account, in a directory of its own that is left
// out of backups and protected until the first unlock after a restart (the
// write-ahead log and its index too). Not "complete" protection: the app
// may still be finishing a write when the device locks, and what is stored
// is ciphertext whose keys the Keychain holds until the device is unlocked.
//
// There is no migration (AGENTS.md 「不写兼容逻辑」): a database of another
// schema version is emptied and filled again from the server.
import Foundation
import GRDB
import os
import RACore
import RAProto

/// A cache directory that could not be made or opened.
struct CacheError: Error, LocalizedError, CustomStringConvertible {
    let description: String
    var errorDescription: String? { description }
}

final class CipherStore: CipherCache {
    /// Bumped when a table changes; a database of another version is emptied.
    static let schemaVersion = 1
    /// Display copies of images are the one thing evicted, the least recently
    /// shown first, once they take more than this (Android's
    /// ImageDiskPolicy.DEFAULT_LIMIT). Events go only with their session or machine.
    static let defaultImageLimit: Int64 = 150 * 1024 * 1024

    /// This account's directory: the database and the image envelopes.
    let directory: URL
    private let pool: DatabasePool
    private let images: EnvelopeFiles
    private static let log = Logger(subsystem: "io.github.lewin671.remoteagent", category: "cache")

    /// Where every account's cache is: Application Support, which the system does not purge on its own.
    static var defaultRoot: URL {
        URL.applicationSupportDirectory.appending(path: "RemoteAgent/cache", directoryHint: .isDirectory)
    }

    /// The name an account's cache is kept under: not the server's address or the device ID themselves.
    static func accountKey(server: String, deviceId: String) -> String {
        sha256(Data("\(server)\n\(deviceId)".utf8)).hex.prefix(32).description
    }

    /// Opens the cache of `account` (an `accountKey`) under `root`, making it when it is not there.
    /// It touches the disk: call it off the main actor.
    static func open(
        root: URL = CipherStore.defaultRoot, account: String, imageLimit: Int64 = CipherStore.defaultImageLimit,
        now: @escaping @Sendable () -> Date = { Date() }
    ) throws -> CipherStore {
        let dir = root.appending(path: account, directoryHint: .isDirectory)
        try protectedDirectory(root)
        try protectedDirectory(dir)
        var config = Configuration()
        config.label = "ra.cache"
        let pool = try DatabasePool(path: dir.appending(path: "cache.sqlite").path, configuration: config)
        try pool.write { db in
            if try Int.fetchOne(db, sql: "PRAGMA user_version") != schemaVersion {
                for table in ["machines", "sessions", "events", "cursors", "blobs"] {
                    try db.execute(sql: "DROP TABLE IF EXISTS \(table)")
                }
                try db.execute(sql: """
                    CREATE TABLE machines (id TEXT PRIMARY KEY NOT NULL, data BLOB NOT NULL) WITHOUT ROWID;
                    CREATE TABLE sessions (
                        id TEXT PRIMARY KEY NOT NULL, machineId TEXT NOT NULL, data BLOB NOT NULL,
                        listed INTEGER NOT NULL DEFAULT 1) WITHOUT ROWID;
                    CREATE INDEX sessions_machine ON sessions (machineId);
                    CREATE TABLE events (
                        sessionId TEXT NOT NULL, seq INTEGER NOT NULL, data BLOB NOT NULL,
                        PRIMARY KEY (sessionId, seq)) WITHOUT ROWID;
                    CREATE TABLE cursors (sessionId TEXT PRIMARY KEY NOT NULL, seq INTEGER NOT NULL) WITHOUT ROWID;
                    CREATE TABLE blobs (name TEXT PRIMARY KEY NOT NULL, data BLOB NOT NULL) WITHOUT ROWID;
                    PRAGMA user_version = \(schemaVersion);
                    """)
            }
        }
        // SQLite makes the log and its index itself; they hold the same rows as the database.
        for suffix in ["", "-wal", "-shm"] {
            let path = dir.appending(path: "cache.sqlite").path + suffix
            if FileManager.default.fileExists(atPath: path) {
                try FileManager.default.setAttributes([.protectionKey: fileProtection], ofItemAtPath: path)
            }
        }
        let images = try EnvelopeFiles(directory: dir.appending(path: "images", directoryHint: .isDirectory), limit: imageLimit, now: now)
        return CipherStore(directory: dir, pool: pool, images: images)
    }

    private init(directory: URL, pool: DatabasePool, images: EnvelopeFiles) {
        self.directory = directory
        self.pool = pool
        self.images = images
    }

    /// Closes the database. The store is not used afterwards.
    func close() throws { try pool.close() }

    /// Removes this account's cache from the disk.
    func erase() throws {
        try pool.close()
        try FileManager.default.removeItem(at: directory)
    }

    /// Logging out: removes every account's cache under `root`. Stores opened from it must be closed first.
    static func eraseAll(root: URL = CipherStore.defaultRoot) throws {
        guard FileManager.default.fileExists(atPath: root.path) else { return }
        try FileManager.default.removeItem(at: root)
    }

    // MARK: CipherCache

    func machines() async -> [PBMachine] {
        await read([]) { db in
            try Data.fetchAll(db, sql: "SELECT data FROM machines").compactMap { try? PBMachine(serializedBytes: $0) }
        }
    }

    func sessions() async -> [PBRemoteSession] {
        await read([]) { db in
            try Data.fetchAll(db, sql: "SELECT data FROM sessions WHERE listed = 1")
                .compactMap { try? PBRemoteSession(serializedBytes: $0) }
        }
    }

    func events(sessionId: String) async -> [PBStoredEvent] {
        await read([]) { db in
            try Data.fetchAll(db, sql: "SELECT data FROM events WHERE sessionId = ? ORDER BY seq", arguments: [sessionId])
                .compactMap { try? PBStoredEvent(serializedBytes: $0) }
        }
    }

    func putMachine(_ m: PBMachine) async {
        guard let data = try? m.serializedData() else { return }
        let id = m.id
        await write { db in
            try db.execute(sql: "INSERT OR REPLACE INTO machines (id, data) VALUES (?, ?)", arguments: [id, data])
        }
    }

    func putSession(_ s: PBRemoteSession) async {
        guard let data = try? s.serializedData() else { return }
        let (id, machine) = (s.sessionID, s.machineID)
        await write { db in
            try db.execute(
                sql: "INSERT OR REPLACE INTO sessions (id, machineId, data, listed) VALUES (?, ?, ?, 1)",
                arguments: [id, machine, data])
        }
    }

    func putEvents(sessionId: String, events: [PBStoredEvent]) async {
        do {
            try await storeEvents(sessionId: sessionId, events: events)
        } catch {
            Self.log.error("cache write failed: \(String(describing: error), privacy: .public)")
        }
    }

    func dropEvents(sessionId: String) async {
        await write { db in
            try db.execute(sql: "DELETE FROM events WHERE sessionId = ?", arguments: [sessionId])
            try db.execute(sql: "DELETE FROM cursors WHERE sessionId = ?", arguments: [sessionId])
        }
    }

    func removeMachine(id: String) async {
        await write { db in
            // Rows that are not listed stay to say which machine their events are of, so that they go here.
            try db.execute(
                sql: "DELETE FROM events WHERE sessionId IN (SELECT id FROM sessions WHERE machineId = ?)", arguments: [id])
            try db.execute(
                sql: "DELETE FROM cursors WHERE sessionId IN (SELECT id FROM sessions WHERE machineId = ?)", arguments: [id])
            try db.execute(sql: "DELETE FROM sessions WHERE machineId = ?", arguments: [id])
            try db.execute(sql: "DELETE FROM machines WHERE id = ?", arguments: [id])
            try db.execute(sql: "DELETE FROM blobs WHERE name = ?", arguments: ["schedules/" + id])
        }
    }

    func removeSession(id: String) async {
        await write { db in
            try db.execute(sql: "DELETE FROM events WHERE sessionId = ?", arguments: [id])
            try db.execute(sql: "DELETE FROM cursors WHERE sessionId = ?", arguments: [id])
            try db.execute(sql: "DELETE FROM sessions WHERE id = ?", arguments: [id])
        }
    }

    func unlistSession(id: String) async {
        await write { db in try db.execute(sql: "UPDATE sessions SET listed = 0 WHERE id = ?", arguments: [id]) }
    }

    func keepOwner(_ s: PBRemoteSession) async {
        guard let data = try? s.serializedData() else { return }
        let (id, machine) = (s.sessionID, s.machineID)
        await write { db in
            try db.execute(
                sql: "INSERT OR IGNORE INTO sessions (id, machineId, data, listed) VALUES (?, ?, ?, 0)",
                arguments: [id, machine, data])
        }
    }

    func image(key: String) async -> Data? { images.get(key) }

    func putImage(key: String, envelope: Data) async {
        do {
            try images.put(key, envelope)
        } catch {
            Self.log.error("image cache write failed: \(String(describing: error), privacy: .public)")
        }
    }

    func blob(name: String) async -> Data? {
        await read(nil) { db in try Data.fetchOne(db, sql: "SELECT data FROM blobs WHERE name = ?", arguments: [name]) }
    }

    func putBlob(name: String, data: Data) async {
        await write { db in
            try db.execute(sql: "INSERT OR REPLACE INTO blobs (name, data) VALUES (?, ?)", arguments: [name, data])
        }
    }

    func removeBlob(name: String) async {
        await write { db in try db.execute(sql: "DELETE FROM blobs WHERE name = ?", arguments: [name]) }
    }

    // MARK: Beyond the protocol

    /// The highest sequence number stored for a session, 0 when it has no events: where fetching resumes.
    func cursor(sessionId: String) async -> UInt64 {
        await read(0) { db in
            let seq = try Int64.fetchOne(db, sql: "SELECT seq FROM cursors WHERE sessionId = ?", arguments: [sessionId])
            return UInt64(bitPattern: seq ?? 0)
        }
    }

    /// Bytes the image envelopes take on disk.
    var imageBytes: Int64 { images.used }

    /// Removes the image envelopes (设置 › 清理查看缓存); sessions and events stay.
    func clearImages() { images.clear() }

    /// Stores events and moves the session's cursor in one transaction:
    /// after a crash or a failure there are both or neither. An event
    /// already stored is left as it is. `beforeCommit` is run inside the
    /// transaction, last; what it throws undoes the whole of it.
    func storeEvents(sessionId: String, events: [PBStoredEvent], beforeCommit: (@Sendable () throws -> Void)? = nil) async throws {
        // A sequence number is a uint64 and SQLite's integers are signed: it
        // is stored by its bits, which keeps the order for every number
        // below 2^63, far more than a session will ever have.
        let rows = try events.map { (Int64(bitPattern: $0.seq), try $0.serializedData()) }
        guard let last = rows.map(\.0).max() else { return }
        try await pool.write { db in
            for (seq, data) in rows {
                try db.execute(
                    sql: "INSERT OR IGNORE INTO events (sessionId, seq, data) VALUES (?, ?, ?)",
                    arguments: [sessionId, seq, data])
            }
            try db.execute(
                sql: """
                    INSERT INTO cursors (sessionId, seq) VALUES (?, ?)
                    ON CONFLICT (sessionId) DO UPDATE SET seq = max(seq, excluded.seq)
                    """,
                arguments: [sessionId, last])
            try beforeCommit?()
        }
    }

    // The protocol's methods cannot fail: a cache that cannot be read is an
    // empty one, and one that cannot be written is fetched again next time.
    // Errors are logged without their arguments, which GRDB leaves out.

    private func read<T: Sendable>(_ fallback: T, _ body: @escaping @Sendable (Database) throws -> T) async -> T {
        do {
            return try await pool.read(body)
        } catch {
            Self.log.error("cache read failed: \(String(describing: error), privacy: .public)")
            return fallback
        }
    }

    private func write(_ body: @escaping @Sendable (Database) throws -> Void) async {
        do {
            try await pool.write(body)
        } catch {
            Self.log.error("cache write failed: \(String(describing: error), privacy: .public)")
        }
    }
}

/// What files of the cache and of pending images are protected with.
let fileProtection = FileProtectionType.completeUntilFirstUserAuthentication

/// Makes `url` a directory whose files are protected as above and left out of iCloud and device backups.
func protectedDirectory(_ url: URL) throws {
    do {
        try FileManager.default.createDirectory(
            at: url, withIntermediateDirectories: true, attributes: [.protectionKey: fileProtection])
        // A directory that was already there keeps the attributes it was made with unless they are set again.
        try FileManager.default.setAttributes([.protectionKey: fileProtection], ofItemAtPath: url.path)
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        var u = url
        try u.setResourceValues(values)
    } catch {
        throw CacheError(description: "没能建立本机缓存目录：\(error.localizedDescription)")
    }
}

/// Display copies of images as fetched: sealed envelopes only, one file
/// each, so a viewed image opens offline and nothing decrypted touches the
/// disk. The least recently used go once they take more than `limit` bytes.
private final class EnvelopeFiles: @unchecked Sendable {
    private let directory: URL
    private let limit: Int64
    private let now: @Sendable () -> Date
    private let lock = NSLock()

    init(directory: URL, limit: Int64, now: @escaping @Sendable () -> Date) throws {
        self.directory = directory
        self.limit = limit
        self.now = now
        try protectedDirectory(directory)
        trim()
    }

    private func file(_ key: String) -> URL { directory.appending(path: sha256(Data(key.utf8)).hex) }

    func get(_ key: String) -> Data? {
        lock.withLock {
            let f = file(key)
            guard let data = try? Data(contentsOf: f) else { return nil }
            try? FileManager.default.setAttributes([.modificationDate: now()], ofItemAtPath: f.path)
            return data
        }
    }

    func put(_ key: String, _ envelope: Data) throws {
        try lock.withLock {
            let f = file(key)
            try envelope.write(to: f, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            try FileManager.default.setAttributes([.modificationDate: now()], ofItemAtPath: f.path)
            trim()
        }
    }

    var used: Int64 { lock.withLock { entries().reduce(0) { $0 + $1.size } } }

    func clear() {
        lock.withLock { for e in entries() { try? FileManager.default.removeItem(at: e.url) } }
    }

    private func entries() -> [(url: URL, size: Int64, used: Date)] {
        let keys: Set<URLResourceKey> = [.fileSizeKey, .contentModificationDateKey, .isRegularFileKey]
        let files = (try? FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: Array(keys))) ?? []
        return files.compactMap { url in
            guard let v = try? url.resourceValues(forKeys: keys), v.isRegularFile == true else { return nil }
            return (url, Int64(v.fileSize ?? 0), v.contentModificationDate ?? .distantPast)
        }
    }

    private func trim() {
        let all = entries().sorted { $0.used < $1.used }
        var total = all.reduce(0) { $0 + $1.size }
        for e in all {
            if total <= limit { break }
            if (try? FileManager.default.removeItem(at: e.url)) != nil { total -= e.size }
        }
    }
}
