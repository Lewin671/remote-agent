// Files the agent hands to the user (the remote_agent MCP's send_file):
// what a file is, how its card reads in each state, and the rules of the
// blocks it is fetched in. Docs: tmp/ra-send-file/ui-spec.md is the lead's
// spec, Android's core/SharedFiles.kt the reference. The downloading itself
// is `SharedFileStore`.
import Foundation
import RAProto

/// What a card calls a file.
public enum SharedFileKind: Equatable, Sendable {
    case video, image, audio, archive, pdf, doc, other

    /// The type word of a subtitle: 视频 / 图片 / 音频 / 压缩包 / PDF / 文件.
    public var word: String {
        switch self {
        case .video: L10n.sharedFileTypeVideo()
        case .image: L10n.sharedFileTypeImage()
        case .audio: L10n.sharedFileTypeAudio()
        case .archive: L10n.sharedFileTypeArchive()
        case .pdf: L10n.sharedFileTypePdf()
        case .doc, .other: L10n.sharedFileTypeOther()
        }
    }

    /// Only pictures and videos go to the photo library.
    public var savesToPhotos: Bool { self == .image || self == .video }
    /// A duration is known (and shown) for these once the copy is on the phone.
    public var hasDuration: Bool { self == .video || self == .audio }
}

private let videoExtensions: Set<String> = ["mp4", "mov", "m4v", "mkv", "webm", "avi", "3gp"]
private let imageExtensions: Set<String> = ["png", "jpg", "jpeg", "webp", "gif", "heic", "heif", "bmp", "svg"]
private let audioExtensions: Set<String> = ["mp3", "m4a", "aac", "wav", "ogg", "opus", "flac"]
private let archiveExtensions: Set<String> = ["zip", "tar", "gz", "tgz", "bz2", "xz", "7z", "rar", "zst"]
private let docExtensions: Set<String> = [
    "doc", "docx", "xls", "xlsx", "ppt", "pptx", "txt", "md", "csv", "rtf", "pages", "numbers", "key",
]

/// By the name's extension, as Android's classifySharedFile.
public func classifySharedFile(_ name: String) -> SharedFileKind {
    let clean = sanitizeSharedFileName(name)
    guard let dot = clean.lastIndex(of: "."), dot != clean.startIndex else { return .other }
    let ext = clean[clean.index(after: dot)...].lowercased()
    if videoExtensions.contains(ext) { return .video }
    if imageExtensions.contains(ext) { return .image }
    if audioExtensions.contains(ext) { return .audio }
    if archiveExtensions.contains(ext) { return .archive }
    if ext == "pdf" { return .pdf }
    if docExtensions.contains(ext) { return .doc }
    return .other
}

/// A name from the computer made safe to keep: no path (either separator), control characters or
/// bidirectional marks, at most 200 characters with the extension kept, never empty
/// (ui-spec §8; the same rule as Android's sanitizeSharedFileName).
public func sanitizeSharedFileName(_ raw: String) -> String {
    let tail = raw.split(whereSeparator: { $0 == "/" || $0 == "\\" }).last.map(String.init) ?? ""
    var kept = String.UnicodeScalarView()
    for s in tail.unicodeScalars {
        let v = s.value
        if v < 0x20 || v == 0x7f || (0x80...0x9f).contains(v) { continue }
        if (0x202a...0x202e).contains(v) || (0x2066...0x2069).contains(v) || v == 0x200e || v == 0x200f || v == 0x061c { continue }
        kept.append(s)
    }
    let name = String(kept).trimmingCharacters(in: .whitespaces)
    if name.isEmpty || name == "." || name == ".." { return "file" }
    if name.count <= 200 { return name }
    if let dot = name.lastIndex(of: "."), dot != name.startIndex {
        let ext = name[dot...]
        if ext.count > 1 && ext.count <= 16 { return String(name.prefix(200 - ext.count)) + ext }
    }
    return String(name.prefix(200))
}

/// A name in two parts for a line that cuts it in the middle: the head may be cut, the tail (the
/// extension and three characters before it) never is.
public func splitMiddleTruncateName(_ name: String) -> (head: String, tail: String) {
    let chars = Array(name)
    guard let dot = chars.lastIndex(of: "."), dot > 0, dot < chars.count - 1, chars.count - dot <= 12 else { return (name, "") }
    let keep = min(chars.count - dot + 3, chars.count - 1)
    return (String(chars.dropLast(keep)), String(chars.suffix(keep)))
}

/// The path on the computer as the menu shows it: the home folder as `~`.
public func displaySharedPath(_ path: String, home: String, fallbackName: String) -> String {
    let trimmed = path.trimmingCharacters(in: .whitespaces)
    if trimmed.isEmpty { return sanitizeSharedFileName(fallbackName) }
    return shortenPath(trimmed, cwd: "", home: home)
}

/// Sizes as the file chips of a message show them.
public func sharedFileSize(_ bytes: Int64) -> String { fileSize(max(0, bytes)) }

/// "8.1" and "24.3 MB": the downloaded part drops its unit when the total has the same one.
public func sharedFileProgress(done: Int64, total: Int64) -> (done: String, total: String) {
    let totalText = sharedFileSize(total)
    let doneText = sharedFileSize(min(done, total))
    let unit = totalText.split(separator: " ").last.map(String.init) ?? ""
    let doneUnit = doneText.split(separator: " ").last.map(String.init) ?? ""
    if !unit.isEmpty, unit == doneUnit, let number = doneText.split(separator: " ").first { return (String(number), totalText) }
    return (doneText, totalText)
}

/// `m:ss`, `h:mm:ss` past an hour.
public func sharedFileDuration(ms: Int64) -> String {
    let seconds = (max(0, ms) + 500) / 1000
    let (h, m, s) = (seconds / 3600, seconds % 3600 / 60, seconds % 60)
    return h > 0 ? String(format: "%lld:%02lld:%02lld", h, m, s) : String(format: "%lld:%02lld", m, s)
}

/// Seconds as 约 {…}: seconds under a minute, else minutes rounded up.
public func sharedFileEta(seconds: Int) -> String {
    seconds < 60 ? L10n.sharedFileEtaSeconds(count: max(1, seconds)) : L10n.sharedFileEtaMinutes(count: (seconds + 59) / 60)
}

// MARK: Blocks

/// The default block size, in step with `MachineCapabilities.shared_file_chunk_bytes` (768 KiB).
public let defaultSharedFileBlockBytes = 786_432
/// How long a download waits for the computer to come back before it gives up (once per download).
public let sharedFileReconnectWaitMs: Int64 = 30_000

/// The block size the machine asks for, else the protocol's.
public func sharedFileBlockBytes(_ machine: MachineView?) -> Int {
    let n = Int(capabilities(machine)?.sharedFileChunkBytes ?? 0)
    return n > 0 ? n : defaultSharedFileBlockBytes
}

/// Only a machine that says it can hands out files; one that does not keeps the call a plain tool row.
public func sharedFilesSupported(_ machine: MachineView?) -> Bool { capabilities(machine)?.sharedFiles == true }

public struct SharedFileBlockPlan: Equatable, Sendable {
    public var offsets: [Int64]
    /// How many bytes of the file the last block holds (0 for an empty file).
    public var lastBlockFileBytes: Int
}

/// The offsets a file is fetched at: one block for an empty file, else every multiple of the block size below the size.
public func planSharedFileBlocks(size: Int64, blockBytes: Int = defaultSharedFileBlockBytes) -> SharedFileBlockPlan {
    precondition(size >= 0 && blockBytes > 0)
    if size == 0 { return SharedFileBlockPlan(offsets: [0], lastBlockFileBytes: 0) }
    let block = Int64(blockBytes)
    let offsets = Array(stride(from: Int64(0), to: size, by: blockBytes))
    let rest = Int(size % block)
    return SharedFileBlockPlan(offsets: offsets, lastBlockFileBytes: rest == 0 ? blockBytes : rest)
}

public func isValidSharedFileOffset(_ offset: Int64, size: Int64, blockBytes: Int = defaultSharedFileBlockBytes) -> Bool {
    if blockBytes <= 0 || offset < 0 || offset % Int64(blockBytes) != 0 { return false }
    return size == 0 ? offset == 0 : offset < size
}

// MARK: What a copy is doing

/// A file's place on one session, the unit that downloads are shared by: the same file_id shown twice is one download.
public struct SharedFileKey: Hashable, Sendable {
    public let sessionId: String
    public let fileId: String
    public init(sessionId: String, fileId: String) {
        self.sessionId = sessionId
        self.fileId = fileId
    }
}

public enum SharedFileFailure: Equatable, Sendable {
    case disconnected
    case checksum
    case diskFull
    /// The computer's own reason, as it gave it.
    case other(String)

    /// 连接断了 / 文件校验不一致 / 手机空间不足 / what the computer said.
    public var reason: String {
        switch self {
        case .disconnected: L10n.sharedFileReasonDisconnected()
        case .checksum: L10n.sharedFileReasonChecksumMismatch()
        case .diskFull: L10n.sharedFileReasonDiskFull()
        case .other(let text): text.isEmpty ? L10n.sharedFileReasonUnknown() : text
        }
    }
}

/// What the store knows about one file on this phone.
public enum SharedFileTransfer: Equatable, Sendable {
    /// Nothing on the phone.
    case idle
    case downloading(done: Int64, total: Int64, etaSeconds: Int?, waitingReconnect: Bool)
    /// Blocks are kept; nothing is coming.
    case paused(done: Int64, total: Int64)
    case downloaded(url: URL, durationMs: Int64?)
    case failed(SharedFileFailure)
    /// The computer no longer has it, and the phone never got it.
    case cleanedUp
}

/// What a card shows: the transfer, the computer and the session together (ui-spec §4).
public enum SharedFileCard: Equatable, Sendable {
    case available
    case downloading(done: Int64, total: Int64, etaSeconds: Int?)
    case paused(done: Int64, total: Int64)
    case downloaded(durationMs: Int64?)
    case failed(SharedFileFailure)
    case offline
    case cleaned
}

/// Order matters: a copy on the phone beats everything; a download in flight is shown as one;
/// a session that ended (or a computer that dropped the file) clears what was never fetched;
/// paused keeps its place even with the computer away; only then does the computer's absence count.
public func sharedFileCard(_ transfer: SharedFileTransfer, machineOnline: Bool, sessionExited: Bool) -> SharedFileCard {
    switch transfer {
    case .downloaded(_, let duration): return .downloaded(durationMs: duration)
    case .downloading(let done, let total, let eta, _): return .downloading(done: done, total: total, etaSeconds: eta)
    default: break
    }
    if sessionExited || transfer == .cleanedUp { return .cleaned }
    if case .paused(let done, let total) = transfer { return .paused(done: done, total: total) }
    if !machineOnline { return .offline }
    if case .failed(let why) = transfer { return .failed(why) }
    return .available
}

/// The second line of a card.
public func sharedFileSubtitle(_ card: SharedFileCard, name: String, size: Int64) -> String {
    let kind = classifySharedFile(name)
    switch card {
    case .available:
        return L10n.sharedFileSubtitleKindSize(kind: kind.word, size: sharedFileSize(size))
    case .downloading(let done, let total, let eta):
        let p = sharedFileProgress(done: done, total: total)
        if let eta { return L10n.sharedFileSubtitleDownloadingEta(downloaded: p.done, eta: sharedFileEta(seconds: eta), total: p.total) }
        return L10n.sharedFileSubtitleDownloading(downloaded: p.done, total: p.total)
    case .paused(let done, let total):
        let p = sharedFileProgress(done: done, total: total)
        return L10n.sharedFileSubtitlePaused(downloaded: p.done, total: p.total)
    case .downloaded(let duration):
        if kind.hasDuration, let duration {
            return L10n.sharedFileSubtitleMediaSize(duration: sharedFileDuration(ms: duration), size: sharedFileSize(size))
        }
        return L10n.sharedFileSubtitleKindSize(kind: kind.word, size: sharedFileSize(size))
    case .failed(let why):
        return L10n.sharedFileSubtitleFailed(reason: why.reason)
    case .offline:
        return L10n.sharedFileSubtitleOffline()
    case .cleaned:
        return L10n.sharedFileSubtitleCleaned()
    }
}

/// The state word the card's VoiceOver label ends with (ui-spec §9).
public func sharedFileStateWord(_ card: SharedFileCard, name: String, size: Int64) -> String {
    switch card {
    case .available: L10n.sharedFileA11YStateAvailable()
    case .downloading: L10n.sharedFileA11YStateDownloading()
    case .paused: L10n.sharedFileA11YStatePaused()
    case .downloaded: L10n.sharedFileA11YStateDownloaded()
    case .failed, .offline, .cleaned: sharedFileSubtitle(card, name: name, size: size)
    }
}

/// 「{name}，{type}，{size}，{state}」
public func sharedFileA11yLabel(_ card: SharedFileCard, name: String, size: Int64) -> String {
    L10n.sharedFileA11YCard(
        kind: classifySharedFile(name).word, name: sanitizeSharedFileName(name), size: sharedFileSize(size),
        state: sharedFileStateWord(card, name: name, size: size))
}

/// What the control at the card's right says (and the card does when tapped), nil where there is none.
public enum SharedFileControl: Equatable, Sendable {
    case download, pause, resume, open, retry
    /// The grey arrow of a computer that is away: it can be seen, not pressed.
    case disabledDownload

    public var label: String {
        switch self {
        case .download, .disabledDownload: L10n.sharedFileActionDownload()
        case .pause: L10n.sharedFileActionPause()
        case .resume: L10n.sharedFileActionResume()
        case .open: L10n.sharedFileActionOpen()
        case .retry: L10n.sharedFileActionRetry()
        }
    }
}

public func sharedFileControl(_ card: SharedFileCard) -> SharedFileControl? {
    switch card {
    case .available: .download
    case .downloading: .pause
    case .paused: .resume
    case .downloaded: .open
    case .failed: .retry
    case .offline: .disabledDownload
    case .cleaned: nil
    }
}

/// The items of the long-press menu, in order, by what the card is (ui-spec §6). `.separator` stands
/// before the destructive one. Nothing for a computer that is away or a file that was cleaned.
public enum SharedFileMenuItem: Equatable, Sendable {
    case download, resume, pause, cancelDownload
    case saveToPhotos, saveToFiles, share
    case deleteLocal

    public var label: String {
        switch self {
        case .download: L10n.sharedFileActionDownload()
        case .resume: L10n.sharedFileActionResume()
        case .pause: L10n.sharedFileActionPause()
        case .cancelDownload: L10n.sharedFileActionCancelDownload()
        case .saveToPhotos: L10n.sharedFileMenuSaveToAlbum()
        case .saveToFiles: L10n.sharedFileMenuSaveToFiles()
        case .share: L10n.sharedFileMenuShare()
        case .deleteLocal: L10n.sharedFileMenuDeleteLocal()
        }
    }
}

public func sharedFileMenu(_ card: SharedFileCard, kind: SharedFileKind) -> [SharedFileMenuItem] {
    switch card {
    case .available, .failed: [.download]
    case .downloading: [.pause, .cancelDownload]
    case .paused: [.resume, .cancelDownload]
    case .downloaded: (kind.savesToPhotos ? [.saveToPhotos] : []) + [.saveToFiles, .share, .deleteLocal]
    case .offline, .cleaned: []
    }
}

/// The folder name a session or file is kept under: what the computer's IDs are made of, nothing else.
func sharedFileFolderName(_ id: String) -> String {
    String(id.unicodeScalars.map { s in
        (s.value < 128 && (CharacterSet.alphanumerics.contains(s) || s == "-" || s == "_")) ? Character(s) : "_"
    })
}

// MARK: Notifications

extension PBNotifyContent {
    /// The body of a turn-end notification with the number of files the round handed over added
    /// after the text, one space between; with no text only that sentence (ui-spec §10).
    public func turnEndBody(text: String) -> String {
        sharedFileNotificationBody(text: text, files: Int(sharedFileCount))
    }
}

public func sharedFileNotificationBody(text: String, files: Int) -> String {
    guard files > 0 else { return text }
    let sentence = L10n.sharedFileNotificationFiles(count: files)
    return text.isEmpty ? sentence : text + " " + sentence
}
