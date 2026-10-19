// Markdown as the CLIs write it, for an agent's reply. Text is only ever
// rendered as text, and only http(s) links can be opened; other targets,
// mostly local file paths, show as monospace refs.
//
// The web client (web/src/markdown.tsx) and Android (ui/Markdown.kt) parse
// the same way: all three run the cases in testdata/markdown, and how each
// element looks is docs/design-system.md §7. This target is the parser, a
// port of Android's kept in its order; RAMarkdownUI draws the result.
//
// CLI output can hold file content written by someone else, so every pass
// here is linear in the text: each scan stops at the next delimiter of its
// own kind and none looks back over what another already read.

/// Inline Markdown as a tree, so that every client can be held to the same reading of it.
public indirect enum MarkdownSpan: Hashable, Sendable {
    case text(String)
    case code(String)
    case bold([MarkdownSpan])
    case italic([MarkdownSpan])
    case strike([MarkdownSpan])
    /// A link that may be opened: `href` has passed `Markdown.safeHref`.
    case link(text: String, href: String)
    /// A link whose target cannot be opened: shown, not tappable.
    case ref(text: String, target: String)
}

public enum MarkdownTask: Hashable, Sendable {
    case done, todo
}

public enum MarkdownTableAlign: Hashable, Sendable {
    case left, center, right
}

/// One item of a list: its own text, then whatever is nested under it.
public struct MarkdownItem: Hashable, Sendable {
    public var text: String
    public var task: MarkdownTask?
    public var children: [MarkdownBlock]

    public init(text: String, task: MarkdownTask? = nil, children: [MarkdownBlock] = []) {
        self.text = text
        self.task = task
        self.children = children
    }
}

/// A block's text is still inline Markdown: `Markdown.inlineSpans` reads it when it is drawn.
public indirect enum MarkdownBlock: Hashable, Sendable {
    /// Keeps the single line breaks the CLIs use.
    case paragraph(String)
    case code(lang: String, text: String)
    case heading(level: Int, text: String)
    case rule
    case quote([MarkdownBlock])
    case list(ordered: Bool, start: Int, items: [MarkdownItem])
    /// `align` has a column's alignment where the separator row gave one.
    case table(head: [String], align: [MarkdownTableAlign?], rows: [[String]])
}

/// The parser's entry points; see Blocks.swift, Inline.swift and Reading.swift.
public enum Markdown {
    /// Quotes and lists nest no deeper than this: past it the rest is one paragraph, so neither the parser nor the layout can be made to overflow.
    public static let maxDepth = 32

    /// Code longer than this is cut to `foldShown` lines until it is opened.
    public static let foldOver = 16
    public static let foldShown = 12
}

// The parser reads Unicode scalars in arrays: an index is then a constant
// time step, and "\r\n" is two of them, not the one Character Swift makes it.
typealias Scalars = [Unicode.Scalar]

extension String {
    var scalars: Scalars { Array(unicodeScalars) }

    init(_ scalars: Scalars) {
        self.init(String.UnicodeScalarView(scalars))
    }

    init(_ scalars: ArraySlice<Unicode.Scalar>) {
        self.init(String.UnicodeScalarView(scalars))
    }
}

// Debug-only operation observer for deterministic complexity tests. TaskLocal
// keeps concurrent parses isolated; optimized builds contain no observer calls.
enum MarkdownWork {
    #if DEBUG
    @TaskLocal static var record: (@Sendable () -> Void)?
    #endif

    @inline(__always) static func step() {
        #if DEBUG
        record?()
        #endif
    }
}

/// White space as JavaScript's `\s` and `trim()` have it, which is what the
/// web parser reads by: it includes the full-width space of Chinese text.
func isSpace(_ c: Unicode.Scalar) -> Bool {
    MarkdownWork.step()
    return switch c.value {
    case 0x20, 0x09...0x0D, 0xA0, 0x1680, 0x2000...0x200A, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF: true
    default: false
    }
}

/// What JavaScript's `.` stops at, besides the end of a line.
func isLineEnd(_ c: Unicode.Scalar) -> Bool {
    c == "\n" || c == "\r" || c.value == 0x2028 || c.value == 0x2029
}

extension Array where Element == Unicode.Scalar {
    func trimmed() -> ArraySlice<Unicode.Scalar> {
        var lo = startIndex
        var hi = endIndex
        while lo < hi, isSpace(self[lo]) { lo += 1 }
        while hi > lo, isSpace(self[hi - 1]) { hi -= 1 }
        return self[lo..<hi]
    }

    var isBlankLine: Bool { allSatisfy(isSpace) }

    /// How many white space scalars the line starts with; its length when it has nothing else.
    var indent: Int { firstIndex { !isSpace($0) } ?? count }

    func starts(with prefix: String, at index: Int) -> Bool {
        var i = index
        for c in prefix.unicodeScalars {
            guard i < count, self[i] == c else { return false }
            i += 1
        }
        return true
    }
}

/// Whether the scalar at `index` follows an odd run of backslashes.
func escapedAt(_ text: Scalars, _ index: Int) -> Bool {
    var at = index - 1
    var slashes = 0
    while at >= 0, text[at] == "\\" {
        slashes += 1
        at -= 1
    }
    return slashes % 2 == 1
}

func isAsciiDigit(_ c: Unicode.Scalar) -> Bool { c.value >= 0x30 && c.value <= 0x39 }

/// `[A-Za-z0-9_]`: the web client's `\w`, which is ASCII.
func isAsciiWord(_ c: Unicode.Scalar) -> Bool {
    switch c {
    case "a"..."z", "A"..."Z", "0"..."9", "_": true
    default: false
    }
}
