// Files the agent hands to the user (the remote_agent MCP's send_file):
// shared key and safe local filesystem path helpers.
// Download state machine, block planning, and display rules live in Go clientcore.
import Foundation

/// A file's place on one session, the unit that downloads are shared by: the same file_id shown twice is one download.
public struct SharedFileKey: Hashable, Sendable {
    public let sessionId: String
    public let fileId: String
    public init(sessionId: String, fileId: String) {
        self.sessionId = sessionId
        self.fileId = fileId
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
