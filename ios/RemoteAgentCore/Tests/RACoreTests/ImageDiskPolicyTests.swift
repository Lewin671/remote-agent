import Foundation
import Testing

@testable import RACore

/// Android's core ImageDiskPolicyTest.
@Suite struct ImageDiskPolicyTests {
    private let fm = FileManager.default

    private func tempDir(_ name: String) throws -> URL {
        let dir = fm.temporaryDirectory.appendingPathComponent("\(name)-\(UUID().uuidString)", isDirectory: true)
        try fm.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    private func touch(_ url: URL, ms: Int64) throws {
        try fm.setAttributes([.modificationDate: Date(timeIntervalSince1970: Double(ms) / 1000)], ofItemAtPath: url.path)
    }

    private func exists(_ url: URL) -> Bool { fm.fileExists(atPath: url.path) }

    @Test func cacheEvictsOldestAndCountsOnlySuccessfulDeletes() throws {
        let dir = try tempDir("images")
        defer { try? fm.removeItem(at: dir) }
        let old = dir.appendingPathComponent("old")
        let recent = dir.appendingPathComponent("recent")
        try Data(count: 8).write(to: old)
        try touch(old, ms: 10_000)
        try Data(count: 8).write(to: recent)
        try touch(recent, ms: 20_000)
        #expect(ImageDiskPolicy.trimCache(dir, limit: 8) == 8)
        #expect(!exists(old))
        #expect(exists(recent))
    }

    @Test func expiredDraftsGoButUnconfirmedMessagesStay() throws {
        let dir = try tempDir("drafts")
        defer { try? fm.removeItem(at: dir) }
        let ttl = ImageDiskPolicy.draftTTL
        let expired = dir.appendingPathComponent("expired", isDirectory: true)
        try fm.createDirectory(at: expired, withIntermediateDirectories: true)
        try Data("sealed".utf8).write(to: expired.appendingPathComponent("image.meta"))
        try touch(expired.appendingPathComponent("image.meta"), ms: 1000)
        try touch(expired, ms: 1000)
        let pinned = dir.appendingPathComponent("outbox", isDirectory: true)
        try fm.createDirectory(at: pinned, withIntermediateDirectories: true)
        try Data().write(to: pinned.appendingPathComponent(".keep"))
        try touch(pinned.appendingPathComponent(".keep"), ms: 1000)
        try touch(pinned, ms: 1000)
        let fresh = dir.appendingPathComponent("fresh", isDirectory: true)
        try fm.createDirectory(at: fresh, withIntermediateDirectories: true)
        try touch(fresh, ms: ttl)
        #expect(ImageDiskPolicy.expireDrafts(dir, now: ttl + 1000) == 1)
        #expect(!exists(expired))
        #expect(exists(pinned))
        #expect(exists(fresh))
    }

    @Test func fullDraftStoreRefusesNewBytesWithoutEviction() throws {
        let dir = try tempDir("drafts")
        defer { try? fm.removeItem(at: dir) }
        let old = dir.appendingPathComponent("old")
        try Data(count: 8).write(to: old)
        #expect(throws: UserError("手机待发图片空间不足，请发送或移除旧草稿")) {
            try ImageDiskPolicy.requireRoom(dir, replacing: dir.appendingPathComponent("new"), incoming: 4, limit: 10)
        }
        #expect(exists(old))
        try ImageDiskPolicy.requireRoom(dir, replacing: old, incoming: 9, limit: 10)  // replacement counts once
    }

    // --- not in Android's tests ---

    @Test func bytesCountFilesHoweverDeepAndNothingForNoFolder() throws {
        let dir = try tempDir("deep")
        defer { try? fm.removeItem(at: dir) }
        let sub = dir.appendingPathComponent("a/b", isDirectory: true)
        try fm.createDirectory(at: sub, withIntermediateDirectories: true)
        try Data(count: 5).write(to: sub.appendingPathComponent("x.img"))
        try Data(count: 3).write(to: dir.appendingPathComponent("y"))
        #expect(ImageDiskPolicy.bytes(dir) == 8)
        #expect(ImageDiskPolicy.bytes(dir.appendingPathComponent("none")) == 0)
        #expect(ImageDiskPolicy.trimCache(dir.appendingPathComponent("none"), limit: 0) == 0)
        // Only the folder's own files are the cache's to trim.
        #expect(ImageDiskPolicy.trimCache(dir, limit: 0) == 0)
        #expect(exists(sub.appendingPathComponent("x.img")))
    }
}
