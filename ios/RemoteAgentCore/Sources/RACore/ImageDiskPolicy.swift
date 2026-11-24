import Foundation

/// File-only rules, shared by the encrypted stores on the phone and tested without it.
public enum ImageDiskPolicy {
    public static let draftLimit: Int64 = 256 * 1024 * 1024
    public static let defaultLimit: Int64 = 150 * 1024 * 1024

    private static let keys: Set<URLResourceKey> = [.isRegularFileKey, .isDirectoryKey, .fileSizeKey, .contentModificationDateKey]

    private static func isFile(_ u: URL) -> Bool { (try? u.resourceValues(forKeys: keys))?.isRegularFile == true }
    private static func isDirectory(_ u: URL) -> Bool { (try? u.resourceValues(forKeys: keys))?.isDirectory == true }

    /// A file's size, 0 when there is none.
    private static func length(_ u: URL) -> Int64 { Int64((try? u.resourceValues(forKeys: keys))?.fileSize ?? 0) }

    /// When it was last written, in milliseconds; 0 when there is none.
    private static func lastModified(_ u: URL) -> Int64 {
        guard let d = (try? u.resourceValues(forKeys: keys))?.contentModificationDate else { return 0 }
        return Int64((d.timeIntervalSince1970 * 1000).rounded(.down))
    }

    private static func list(_ dir: URL) -> [URL] {
        (try? FileManager.default.contentsOfDirectory(at: dir, includingPropertiesForKeys: Array(keys))) ?? []
    }

    /// The bytes of every file under `dir`, however deep.
    public static func bytes(_ dir: URL) -> Int64 {
        guard let walk = FileManager.default.enumerator(at: dir, includingPropertiesForKeys: Array(keys)) else { return 0 }
        var total: Int64 = 0
        for case let u as URL in walk where isFile(u) { total += length(u) }
        return total
    }

    /// Deletes the files of `dir` least recently written until they fit `limit`; returns what is left. A file that does not go still counts.
    public static func trimCache(_ dir: URL, limit: Int64) -> Int64 {
        let files = list(dir).filter(isFile).map { (url: $0, at: lastModified($0), size: length($0)) }
            .sorted { $0.at != $1.at ? $0.at < $1.at : $0.url.lastPathComponent < $1.url.lastPathComponent }
        var total = files.reduce(0) { $0 + $1.size }
        for f in files {
            if total <= limit { break }
            if (try? FileManager.default.removeItem(at: f.url)) != nil { total -= f.size }
        }
        return total
    }

    /// Throws when `incoming` bytes in place of `replacing` would take the store past `limit`.
    public static func requireRoom(_ dir: URL, replacing: URL, incoming: Int64, limit: Int64) throws {
        if bytes(dir) - length(replacing) + incoming > limit {
            throw UserError(L10nMessage.attachmentIosThisPhoneSPendingImageStorageIsFullSend(), problemKind: .storageFull)
        }
    }
}
