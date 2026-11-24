import CryptoKit
import Foundation
import RAProto
import Testing
@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct FilesTests {
    private func makeFile(_ data: Data = Data(), name: String = "report.csv") throws -> (folder: URL, file: URL) {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent("ra-files-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let url = folder.appendingPathComponent(name)
        try data.write(to: url)
        return (folder, url)
    }

    @Test func inspectReads100MiBBoundaryAndDirectoriesWithoutLoadingBytes() throws {
        let boundary: Int64 = 100 * 1024 * 1024
        let (folder, url) = try makeFile()
        defer { try? FileManager.default.removeItem(at: folder) }

        let dirInfo = try PickedFile.inspect(url: folder)
        #expect(dirInfo.isDirectory && dirInfo.bookmark == nil)

        let handle = try FileHandle(forWritingTo: url)
        defer { try? handle.close() }
        try handle.truncate(atOffset: UInt64(boundary))
        let atLimit = try PickedFile.inspect(url: url)
        #expect(atLimit.sizeBytes == boundary && !atLimit.isDirectory && !atLimit.sizeUnknown && atLimit.bookmark != nil)

        try handle.truncate(atOffset: UInt64(boundary + 1))
        let overLimit = try PickedFile.inspect(url: url)
        #expect(overLimit.sizeBytes == boundary + 1)
    }

    @Test func streamingHashHandlesZeroByteMultiChunkAndLimitRejections() throws {
        let (emptyDir, emptyURL) = try makeFile(Data(), name: "empty.txt")
        defer { try? FileManager.default.removeItem(at: emptyDir) }
        let emptyHash = try PickedFile.hashFile(at: emptyURL, maxBytes: 1024)
        #expect(emptyHash.sizeBytes == 0)
        #expect(emptyHash.sha256 == Data(SHA256.hash(data: Data())))

        let payload = Data((0..<(defaultChunkBytes + 513)).map { UInt8($0 & 0xFF) })
        let (bigDir, bigURL) = try makeFile(payload, name: "payload.bin")
        defer { try? FileManager.default.removeItem(at: bigDir) }
        let bigHash = try PickedFile.hashFile(at: bigURL, maxBytes: Int64(payload.count))
        #expect(bigHash.sizeBytes == Int64(payload.count))
        #expect(bigHash.sha256 == Data(SHA256.hash(data: payload)))

        #expect(throws: AttachmentFileIOError(.fileTooLarge)) {
            _ = try PickedFile.hashFile(at: bigURL, maxBytes: Int64(payload.count - 1))
        }
        #expect(throws: AttachmentFileIOError(.directory)) {
            _ = try PickedFile.hashFile(at: bigDir, maxBytes: 1024)
        }
    }

    @Test func bookmarkHashAndChunkReadDetectSourceChangesAndMissingFiles() throws {
        let payload = Data("0123456789abcdefghijklmnopqrstuvwxyz".utf8)
        let (folder, url) = try makeFile(payload, name: "notes.txt")
        defer { try? FileManager.default.removeItem(at: folder) }

        let inspection = try PickedFile.inspect(url: url)
        let bookmark = try #require(inspection.bookmark)

        let hashed = try PickedFile.hashBookmark(bookmark, maxBytes: 1024)
        #expect(hashed.sizeBytes == Int64(payload.count))
        #expect(hashed.sha256 == Data(SHA256.hash(data: payload)))

        let slice = try PickedFile.readBookmarkChunk(bookmark, offset: 10, length: 6, expectedSizeBytes: Int64(payload.count))
        #expect(slice == Data("abcdef".utf8))

        // Mutating file size is reported as `.sourceChanged`.
        try Data("short".utf8).write(to: url)
        #expect(throws: AttachmentFileIOError(.sourceChanged)) {
            _ = try PickedFile.readBookmarkChunk(bookmark, offset: 0, length: 5, expectedSizeBytes: Int64(payload.count))
        }

        // Removing the file is reported as `.sourceMissing`.
        try FileManager.default.removeItem(at: url)
        do {
            _ = try PickedFile.hashBookmark(bookmark, maxBytes: 1024)
            Issue.record("expected .sourceMissing")
        } catch let error as AttachmentFileIOError {
            #expect(error.kind == .sourceMissing)
        }
    }
}
