// Files the agent hands to the user (the remote_agent MCP's send_file):
// block download planning, local transfer state, and Core local-state mapping.
// Display rules (classification, sanitization, subtitles, actions, menus) live in Go clientcore.
import Foundation
import RAProto

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

// MARK: Local transfer state

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

/// Maps local transfer state into Core's `PBSharedFileLocalStateUpdate`.
public func sharedFileLocalStateUpdate(key: SharedFileKey, transfer: SharedFileTransfer) -> PBSharedFileLocalStateUpdate {
    .with {
        $0.sessionID = key.sessionId
        $0.fileID = key.fileId
        switch transfer {
        case .idle:
            $0.status = .unspecified
        case .downloading(let done, _, let eta, let waitingReconnect):
            $0.status = .downloading
            $0.downloadedBytes = done
            if let eta {
                $0.hasEta_p = true
                $0.etaSeconds = Int64(eta)
            }
            $0.waitingReconnect = waitingReconnect
        case .paused(let done, _):
            $0.status = .paused
            $0.downloadedBytes = done
        case .downloaded(_, let durationMs):
            $0.status = .downloaded
            if let durationMs {
                $0.hasDuration_p = true
                $0.durationMs = durationMs
            }
        case .failed(let failure):
            $0.status = .failed
            switch failure {
            case .disconnected:
                $0.failureKind = .disconnected
            case .checksum:
                $0.failureKind = .checksumMismatch
            case .diskFull:
                $0.failureKind = .phoneStorageFull
            case .other(let detail):
                $0.failureKind = .other
                $0.failureDetail = detail
            }
        case .cleanedUp:
            $0.status = .cleanedUp
        }
    }
}

/// The folder name a session or file is kept under: what the computer's IDs are made of, nothing else.
func sharedFileFolderName(_ id: String) -> String {
    String(id.unicodeScalars.map { s in
        (s.value < 128 && (CharacterSet.alphanumerics.contains(s) || s == "-" || s == "_")) ? Character(s) : "_"
    })
}

/// Minimal path-traversal guard for local disk filenames; Core provides the sanitized display name.
func safeLocalSharedFileName(_ raw: String) -> String {
    let tail: String
    if let sep = raw.lastIndex(where: { $0 == "/" || $0 == "\\" }) {
        tail = String(raw[raw.index(after: sep)...])
    } else {
        tail = raw
    }
    let clean = tail.trimmingCharacters(in: .whitespacesAndNewlines)
    return (clean.isEmpty || clean == "." || clean == "..") ? "file" : clean
}
