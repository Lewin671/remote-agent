// Absolute paths shortened for display, exactly as shortenPaths in
// web/src/model.ts and Android's core/Model.kt: all three clients must show
// the same (the cases are in ShortPathsTests).
import Foundation

private let pathToken = Rx("(\\A|[\(Jre.s)=\\:(\\[\\{,])(?:([\"'])(/[^\"'\\n]*)\\2|(/[^\(Jre.s)\"'`]*))")
private let homeDir = Rx("\\A/(?:Users|home)/[^/:]+")
private let lineSuffix = Rx(":\(Jre.digit)+(?::\(Jre.digit)+)*")
private let privatePrefix = Rx("\\A/private(?=/(?:tmp|var|etc)(?:/|\(Jre.end)))")
private let usersHome = Rx("\\A/Users/[^/]+")
private let linuxHome = Rx("\\A/home/[^/]+")

/// macOS reports /tmp, /var and /etc under /private: the same place.
private func samePlace(_ p: String) -> String { privatePrefix.replace(p, first: true) { _ in "" } }

/// Absolute paths in a tool title, shortened for display. Paths are whole
/// tokens: a quoted one is the whole quoted text, spaces and all; an
/// unquoted one runs to whitespace or a quote; a token starts with / after
/// the start, a space, =, :, (, [, { or a comma. Under the session folder a
/// path becomes relative to it, the folder itself "."; else under the home
/// folder ~/…, and without a known home /Users/<name> and /home/<name> count
/// as one. A folder only matches when what follows it is /, nothing or a
/// :line suffix; anything else, or anything unsure, stays as written.
public func shortenPaths(_ text: String, cwd: String, home: String = "") -> String {
    pathToken.replace(text) { m in
        let before = m.text(1)
        let quoted = m.group(3)
        let path = quoted ?? m.text(4)
        let short = shortenPath(path, cwd: cwd, home: home)
        if short.jvmEquals(path) { return m.value }
        if quoted != nil { return before + m.text(2) + short + m.text(2) }
        return before + short
    }
}

/// One whole path, spaces and all (a file edit's title), shortened by the
/// rule of `shortenPaths`; unchanged when unsure. Same as shortenPath in
/// web/src/model.ts.
public func shortenPath(_ path: String, cwd: String, home: String = "") -> String {
    let slash = UInt8(ascii: "/")
    let base = samePlace(cwd).trimmingEnd(ascii: slash)
    let h = samePlace(home).trimmingEnd(ascii: slash)
    // What follows a folder in the path: a subpath, nothing, or :line[:col].
    func under(_ p: String, _ dir: String) -> String? {
        if dir.isEmpty || !p.jvmStartsWith(dir) { return nil }
        let rest = String(decoding: p.utf8.dropFirst(dir.utf8.count), as: UTF8.self)
        return rest.isEmpty || rest.utf8.first == slash || lineSuffix.matchEntire(rest) != nil ? rest : nil
    }
    let p = samePlace(path)
    if let r = under(p, base) {
        if r.isEmpty || r == "/" { return "." }
        return r.utf8.first == slash ? String(decoding: r.utf8.dropFirst(), as: UTF8.self) : ".\(r)"
    }
    let hm = h.ifEmpty(homeDir.find(p)?.value ?? "")
    if let r = under(p, hm) { return "~\(r)" }
    return path
}

/// A path with the home folder as ~, for a line that has no session folder to be relative to.
public func shortPath(_ p: String) -> String {
    linuxHome.replace(usersHome.replace(p, with: "~"), with: "~")
}
