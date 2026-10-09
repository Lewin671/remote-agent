// What the drawing needs to know about parsed Markdown and that every
// client must decide alike: which columns are numbers, which lists are
// loose, how much space goes between blocks, how wide a table's columns
// are. Kept beside the parser so that `swift test` checks it on the Mac.

private let blankCells: Set<String> = ["", "-", "–", "—", "N/A", "n/a"]

private func isUnitLetter(_ c: Unicode.Scalar) -> Bool {
    switch c {
    case "a"..."z", "A"..."Z", "µ": true
    default: false
    }
}

private func isAny(_ c: Unicode.Scalar, of set: String) -> Bool { set.unicodeScalars.contains(c) }

/// A whole cell that reads as a quantity: digits with a sign, a currency
/// mark or a short unit, or a few of them ("+84 −61", "5.2 s", "118 ms",
/// "1 / 3"). Scanned once from the left, never back.
private func quantity(_ v: ArraySlice<Unicode.Scalar>) -> Bool {
    let n = v.endIndex
    var i = v.startIndex
    while i < n, isAny(v[i], of: "+-−~≈<>≤≥$¥€£") { i += 1 }
    while true {
        guard i < n, isAsciiDigit(v[i]) else { return false }
        i += 1
        while i < n, isAsciiDigit(v[i]) || isAny(v[i], of: ",.:") { i += 1 }
        // A unit: one optional space, then a mark or up to five letters.
        let j = i < n && isSpace(v[i]) ? i + 1 : i
        if j < n, isAny(v[j], of: "%‰°") {
            i = j + 1
        } else {
            var k = j
            while k < n, isUnitLetter(v[k]) { k += 1 }
            if k > j, k - j <= 5 { i = k }
        }
        if i == n { return true }
        if !isSpace(v[i]) { return false }
        while i < n, isSpace(v[i]) { i += 1 }
        if i < n, isAny(v[i], of: "+-−/") {
            i += 1
            if i < n, isSpace(v[i]) { i += 1 }
        }
    }
}

/// Whether a diff's line names a file ("+++ b/x", "--- a/x"), not an added "++n;" or a removed "--n;".
private func fileHeader(_ l: Scalars, _ sign: Unicode.Scalar) -> Bool {
    l.count >= 3 && l[0] == sign && l[1] == sign && l[2] == sign && (l.count == 3 || isSpace(l[3]))
}

/// Where a run of blocks sits, which decides the space between them.
public enum MarkdownPlace: Hashable, Sendable {
    case reply, quote, item
}

/// A block to draw: where it was among its siblings, and the space above it.
public struct MarkdownSpaced: Hashable, Sendable {
    /// The block's position before rules were dropped. It does not change
    /// while a reply grows, so it identifies the block's view.
    public let index: Int
    public let gap: Int
    public let block: MarkdownBlock
}

public enum MarkdownDiffLine: Hashable, Sendable {
    case added, removed, context
}

extension Markdown {
    /// Whether a column holds quantities, and so reads better right-aligned: every cell that says something is a quantity, and at least one does.
    public static func numericColumn(_ cells: [String]) -> Bool {
        var n = 0
        for c in cells {
            // Cells are Markdown; what counts is the text they show.
            let v = shownText(inlineSpans(c)).scalars.trimmed()
            if blankCells.contains(String(v)) { continue }
            if !quantity(v) { return false }
            n += 1
        }
        return n > 0
    }

    /// Items longer than this many columns wrap on a phone: a wide character (CJK and the like) takes two.
    public static let looseOver = 40

    /// Whether a list's items need more room between them: once an item runs
    /// over a line, items set as close as lines read as one paragraph. Judged
    /// by what the items say, not by how they happen to wrap on this screen,
    /// so every client spaces a list alike.
    public static func looseList(_ items: [String]) -> Bool {
        for item in items {
            if item.unicodeScalars.contains("\n") { return true }
            var width = 0
            for c in shownText(inlineSpans(item)).unicodeScalars {
                // An estimate, not Unicode's table of widths: from the CJK blocks up, but
                // for the half-width forms. One column either way moves only where a
                // list is called long.
                let v = c.value
                let wide = v >= 0x2E80 && !(0xFF61...0xFFDC).contains(v) && !(0xFFE8...0xFFEE).contains(v)
                width += wide ? 2 : 1
                if width > looseOver { return true }
            }
        }
        return false
    }

    /// A paragraph that is one bold line, the way the CLIs often write a small heading: it gets a heading's space, not a paragraph's.
    public static func boldHeading(_ text: String) -> Bool {
        // `**X**` with an optional colon, where X holds no star and no line break.
        var t = text.scalars.trimmed()
        if t.last == ":" || t.last == "：" { t = t.dropLast() }
        guard t.count >= 5, t.prefix(2).allSatisfy({ $0 == "*" }), t.suffix(2).allSatisfy({ $0 == "*" }) else { return false }
        return !t.dropFirst(2).dropLast(2).contains { $0 == "*" || $0 == "\n" }
    }

    private static func titled(_ b: MarkdownBlock) -> Bool {
        switch b {
        case .heading: true
        case .paragraph(let text): boldHeading(text)
        default: false
        }
    }

    /// The blocks to draw, each with the space in points above it: the first
    /// row of §7's table that fits. A rule is not drawn, it only widens the
    /// space where it stood. Inside a quote and a list item headings get no
    /// space of their own; `afterText` says an item has text above its first
    /// child.
    public static func spaced(_ blocks: [MarkdownBlock], in place: MarkdownPlace, afterText: Bool = false) -> [MarkdownSpaced] {
        var out: [MarkdownSpaced] = []
        var prev: MarkdownBlock?
        var ruled = false
        for (index, b) in blocks.enumerated() {
            if case .rule = b {
                ruled = true
                continue
            }
            var isList = false
            if case .list = b { isList = true }
            let gap: Int
            switch place {
            case .item: gap = prev == nil && !afterText ? 0 : isList ? 4 : 8
            case .quote: gap = prev == nil ? 0 : 12
            case .reply:
                guard let prev else {
                    gap = 0
                    break
                }
                var afterHeading = false
                if case .heading = prev { afterHeading = true }
                var largeHeading = false
                if case .heading(let level, _) = b, level <= 2 { largeHeading = true }
                var afterTable = false
                if case .table = prev { afterTable = true }
                var afterParagraph = false
                if case .paragraph = prev { afterParagraph = true }
                if ruled {
                    gap = 24
                } else if afterHeading, titled(b) {
                    gap = 12
                } else if largeHeading {
                    gap = 24
                } else if titled(b) {
                    gap = 16
                } else if titled(prev) {
                    gap = 8
                } else if afterTable {
                    gap = 16
                } else if afterParagraph, isList {
                    gap = 8
                } else {
                    gap = 12
                }
            }
            out.append(MarkdownSpaced(index: index, gap: gap, block: b))
            prev = b
            ruled = false
        }
        return out
    }

    /// Fits a table that is a little too wide into the viewport by narrowing
    /// its long columns, never below a readable width; one that still does
    /// not fit keeps its widths and scrolls. Nothing is widened: a table is
    /// as wide as what it holds.
    public static func tableColumnWidths(preferred: [Int], viewport: Int, readable: Int) -> [Int] {
        let total = preferred.reduce(0, +)
        if total <= viewport { return preferred }
        let floors = preferred.map { min($0, readable) }
        let minimum = floors.reduce(0, +)
        if minimum > viewport { return preferred }
        let capacity = total - minimum
        let extra = viewport - minimum
        var widths = preferred.indices.map { floors[$0] + (preferred[$0] - floors[$0]) * extra / capacity }
        var remainder = viewport - widths.reduce(0, +)
        for i in widths.indices where remainder > 0 && widths[i] < preferred[i] {
            widths[i] += 1
            remainder -= 1
        }
        return widths
    }

    /// How a line of a `diff` or `patch` block is colored; the file headers are not.
    public static func diffLine(_ line: String) -> MarkdownDiffLine {
        let l = Array(line.unicodeScalars.prefix(4))
        if l.first == "+", !fileHeader(l, "+") { return .added }
        if l.first == "-", !fileHeader(l, "-") { return .removed }
        return .context
    }
}
