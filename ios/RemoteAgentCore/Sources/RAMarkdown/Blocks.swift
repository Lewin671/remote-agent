// Block Markdown: fenced code, tables, headings, rules, quotes, lists and
// paragraphs, tried in that order on each line.

extension Markdown {
    /// Splits Markdown into blocks. Linear in the text for each level of nesting, and nesting stops at `maxDepth`.
    public static func parse(_ md: String) -> [MarkdownBlock] {
        // "\r\n" and a lone "\r" are line breaks like "\n".
        var lines: [Scalars] = []
        var line = Scalars()
        var afterReturn = false
        for c in md.unicodeScalars {
            MarkdownWork.step()
            if c == "\n" && afterReturn {
                afterReturn = false
                continue
            }
            afterReturn = c == "\r"
            if c == "\n" || c == "\r" {
                lines.append(line)
                line.removeAll()
            } else {
                line.append(c)
            }
        }
        lines.append(line)
        return blocks(lines, depth: 0)
    }

    /// Splits a table row into cells, at pipes outside `code` and not escaped.
    public static func tableCells(_ line: String) -> [String] {
        cells(line.scalars)
    }
}

private func join(_ lines: [Scalars]) -> String {
    var out = Scalars()
    for (i, l) in lines.enumerated() {
        if i > 0 { out.append("\n") }
        out += l
    }
    return String(out)
}

private struct Fence {
    let mark: Unicode.Scalar
    let count: Int
    let lang: String
}

/// A fence's opener, at any indent: the CLIs nest code blocks in list items.
/// The language is the first word after it, lower case.
private func fenceOf(_ line: Scalars) -> Fence? {
    var i = line.indent
    guard i < line.count, line[i] == "`" || line[i] == "~" else { return nil }
    let mark = line[i]
    let start = i
    while i < line.count, line[i] == mark { i += 1 }
    let count = i - start
    guard count >= 3 else { return nil }
    while i < line.count, isSpace(line[i]) { i += 1 }
    let word = i
    while i < line.count, !isSpace(line[i]), line[i] != "`" { i += 1 }
    return Fence(mark: mark, count: count, lang: String(line[word..<i]).lowercased())
}

/// A line that ends a fence opened by `n` of `mark`: the same character, at least as many.
private func closesFence(_ line: Scalars, _ mark: Unicode.Scalar, _ n: Int) -> Bool {
    let t = line.trimmed()
    return t.count >= n && t.allSatisfy { $0 == mark }
}

/// The row under a table's head: cells of dashes, with optional colons and outer pipes.
private func tableSep(_ line: Scalars) -> Bool {
    var s = line.trimmed()
    if s.first == "|" { s = s.dropFirst() }
    if s.last == "|" { s = s.dropLast() }
    // Every cell is `:?-{3,}:?` once trimmed.
    for cell in s.split(separator: "|", omittingEmptySubsequences: false) {
        var lo = cell.startIndex
        var hi = cell.endIndex
        while lo < hi, isSpace(cell[lo]) { lo += 1 }
        while hi > lo, isSpace(cell[hi - 1]) { hi -= 1 }
        if lo < hi, cell[lo] == ":" { lo += 1 }
        if hi > lo, cell[hi - 1] == ":" { hi -= 1 }
        guard hi - lo >= 3, cell[lo..<hi].allSatisfy({ $0 == "-" }) else { return false }
    }
    return true
}

/// `# text`, with up to three spaces before it and any closing hashes dropped; nil when the line is not a heading.
private func headingOf(_ line: Scalars) -> MarkdownBlock? {
    var i = 0
    while i < 3, i < line.count, isSpace(line[i]) { i += 1 }
    let hashes = i
    while i < line.count, line[i] == "#" { i += 1 }
    let level = i - hashes
    guard (1...6).contains(level), i < line.count, isSpace(line[i]) else { return nil }
    var end = line.count
    while end > i, isSpace(line[end - 1]) { end -= 1 }
    while end > i, line[end - 1] == "#" { end -= 1 }
    while end > i, isSpace(line[end - 1]) { end -= 1 }
    while i < end, isSpace(line[i]) { i += 1 }
    let text = line[i..<end]
    return text.contains(where: isLineEnd) ? nil : .heading(level: level, text: String(text))
}

/// Three or more of the same of - * _ and nothing else, up to three spaces in.
private func isRule(_ line: Scalars) -> Bool {
    let at = line.indent
    guard at <= 3, at < line.count else { return false }
    let mark = line[at]
    guard mark == "-" || mark == "*" || mark == "_" else { return false }
    var n = 0
    for c in line[at...] {
        if c == mark {
            n += 1
        } else if !isSpace(c) {
            return false
        }
    }
    return n >= 3
}

/// What a quoted line says after its `>`; nil when the line is not quoted.
private func quoteOf(_ line: Scalars) -> Scalars? {
    var i = 0
    while i < 3, i < line.count, isSpace(line[i]) { i += 1 }
    guard i < line.count, line[i] == ">" else { return nil }
    i += 1
    if i < line.count, isSpace(line[i]) { i += 1 }
    let rest = line[i...]
    return rest.contains(where: isLineEnd) ? nil : Array(rest)
}

/// A list line's marker and text; nil when the line does not start an item.
/// A number has at most nine digits, so it always fits an Int.
private func listLine(_ line: Scalars) -> (marker: ArraySlice<Unicode.Scalar>, text: ArraySlice<Unicode.Scalar>)? {
    var i = line.indent
    let start = i
    guard i < line.count else { return nil }
    if line[i] == "-" || line[i] == "*" || line[i] == "+" {
        i += 1
    } else {
        while i < line.count, i - start < 9, isAsciiDigit(line[i]) { i += 1 }
        guard i > start, i < line.count, line[i] == "." || line[i] == ")" else { return nil }
        i += 1
    }
    let marker = line[start..<i]
    guard i < line.count, isSpace(line[i]) else { return nil }
    while i < line.count, isSpace(line[i]) { i += 1 }
    let text = line[i...]
    return text.contains(where: isLineEnd) ? nil : (marker, text)
}

private func isList(_ line: Scalars) -> Bool { listLine(line) != nil }

private func isOrdered(_ line: Scalars) -> Bool {
    listLine(line).map { isAsciiDigit($0.marker.first!) } ?? false
}

func cells(_ line: Scalars) -> [String] {
    var s = line.trimmed()
    if s.first == "|" { s = s.dropFirst() }
    let row = Array(s)
    var end = row.count
    // A pipe after an odd run of backslashes is escaped.
    if row.last == "|", !escapedAt(row, row.count - 1) { end -= 1 }
    // How many runs of each length are still ahead: a run opens code only if another can close it.
    var ticks: [Int: Int] = [:]
    var i = 0
    while i < end {
        guard row[i] == "`" else {
            i += 1
            continue
        }
        var j = i + 1
        while j < end, row[j] == "`" { j += 1 }
        ticks[j - i, default: 0] += 1
        i = j
    }
    var out: [String] = []
    var cell = Scalars()
    func push() {
        out.append(String(cell.trimmed()))
        cell.removeAll(keepingCapacity: true)
    }
    var code = 0
    i = 0
    while i < end {
        let c = row[i]
        if c == "\\", i + 1 < end, row[i + 1] == "|" || row[i + 1] == "\\" {
            if row[i + 1] == "|" {
                cell.append("|")
            } else {
                cell += ["\\", "\\"]
            }
            i += 2
        } else if c == "`" {
            var j = i + 1
            while j < end, row[j] == "`" { j += 1 }
            let n = j - i
            let left = (ticks[n] ?? 1) - 1
            ticks[n] = left
            if code == 0, !escapedAt(row, i), left > 0 {
                code = n
            } else if code == n {
                code = 0
            }
            cell += row[i..<j]
            i = j
        } else if c == "|", code == 0 {
            push()
            i += 1
        } else {
            cell.append(c)
            i += 1
        }
    }
    push()
    return out
}

func blocks(_ lines: [Scalars], depth: Int) -> [MarkdownBlock] {
    if depth >= Markdown.maxDepth { return [.paragraph(join(lines))] }
    // The next line that says something, so that a blank line in a list is not followed by a scan.
    var nextNonBlank = [Int](repeating: lines.count, count: lines.count)
    var nextLine = lines.count
    for i in lines.indices.reversed() {
        nextNonBlank[i] = nextLine
        if !lines[i].isBlankLine { nextLine = i }
    }
    var out: [MarkdownBlock] = []
    var text: [Scalars] = []
    func flush() {
        if !text.isEmpty { out.append(.paragraph(join(text))) }
        text.removeAll()
    }
    var i = 0
    while i < lines.count {
        MarkdownWork.step()
        let line = lines[i]
        if line.isBlankLine {
            flush()
            i += 1
            continue
        }
        if let f = fenceOf(line) {
            var body: [Scalars] = []
            // The fence's own indent is the list's, not the code's.
            let indent = line.indent
            var j = i + 1
            // Closed by a fence of the same character, at least as long.
            while j < lines.count, !closesFence(lines[j], f.mark, f.count) {
                body.append(Array(lines[j][min(indent, lines[j].indent)...]))
                j += 1
            }
            flush()
            out.append(.code(lang: f.lang, text: join(body)))
            i = j + 1
            continue
        }
        if line.contains("|"), i + 1 < lines.count, tableSep(lines[i + 1]) {
            let head = cells(line)
            let sep = cells(lines[i + 1])
            // The header owns the columns; ignore surplus separator cells in CLI replies.
            if head.count <= sep.count {
                let align: [MarkdownTableAlign?] = sep.prefix(head.count).map {
                    switch ($0.hasPrefix(":"), $0.hasSuffix(":")) {
                    case (true, true): .center
                    case (false, true): .right
                    case (true, false): .left
                    case (false, false): nil
                    }
                }
                var rows: [[String]] = []
                var j = i + 2
                while j < lines.count, lines[j].contains("|"), !lines[j].isBlankLine {
                    rows.append(cells(lines[j]))
                    j += 1
                }
                flush()
                out.append(.table(head: head, align: align, rows: rows))
                i = j
                continue
            }
        }
        if let h = headingOf(line) {
            flush()
            out.append(h)
            i += 1
            continue
        }
        if isRule(line) {
            flush()
            out.append(.rule)
            i += 1
            continue
        }
        if quoteOf(line) != nil {
            flush()
            var body: [Scalars] = []
            var j = i
            while j < lines.count, let q = quoteOf(lines[j]) {
                body.append(q)
                j += 1
            }
            out.append(.quote(blocks(body, depth: depth + 1)))
            i = j
            continue
        }
        if isList(line) {
            flush()
            let base = line.indent
            let kind = isOrdered(line)
            var body: [Scalars] = []
            var j = i
            // The list runs over its items, lines indented under them, and blank
            // lines between; it ends at a line back at the left edge that is not
            // an item.
            while j < lines.count {
                let l = lines[j]
                if l.isBlankLine {
                    guard nextNonBlank[j] < lines.count else { break }
                    let next = lines[nextNonBlank[j]]
                    if next.indent <= base, !isList(next) { break }
                    body.append(l)
                    j += 1
                    continue
                }
                // An unindented line right after text is a lazy continuation of the item.
                let lazy = !body.isEmpty && !body[body.count - 1].isBlankLine
                let indent = l.indent
                if indent < base || (indent == base && !isList(l) && !body.isEmpty && !lazy) { break }
                // Bullets after numbers, or the other way round, start a new list.
                if j > i, indent <= base + 1, isList(l), isOrdered(l) != kind { break }
                body.append(l)
                j += 1
            }
            out.append(list(body, base: base, depth: depth))
            i = j
            continue
        }
        text.append(line)
        i += 1
    }
    flush()
    return out
}

/// `[ ] text` or `[x] text` at the start of an item.
private func taskOf(_ text: Scalars) -> (task: MarkdownTask, text: ArraySlice<Unicode.Scalar>)? {
    guard text.count >= 4, text[0] == "[", text[2] == "]", isSpace(text[3]) else { return nil }
    let task: MarkdownTask
    switch text[1] {
    case " ": task = .todo
    case "x", "X": task = .done
    default: return nil
    }
    var i = 3
    while i < text.count, isSpace(text[i]) { i += 1 }
    return (task, text[i...])
}

private func list(_ lines: [Scalars], base: Int, depth: Int) -> MarkdownBlock {
    struct Raw {
        var text: [Scalars]
        var rest: [Scalars] = []
    }
    let first = listLine(lines[0])!.marker
    let ordered = isAsciiDigit(first.first!)
    var items: [Raw] = []
    for l in lines {
        let m = listLine(l)
        if let m, l.indent <= base + 1 {
            items.append(Raw(text: [Array(m.text)]))
            continue
        }
        let cur = items.count - 1
        // Text right under an item, before anything nested, continues it.
        if items[cur].rest.isEmpty, !l.isBlankLine, m == nil, fenceOf(l) == nil {
            items[cur].text.append(Array(l.trimmed()))
        } else {
            items[cur].rest.append(l)
        }
    }
    /// Takes off the indent the nested lines share.
    func strip(_ rest: [Scalars]) -> [Scalars] {
        let shared = rest.lazy.filter { !$0.isBlankLine }.map(\.indent).min() ?? 0
        return rest.map { Array($0[min(shared, $0.indent)...]) }
    }
    return .list(
        ordered: ordered,
        start: ordered ? Int(String(first.prefix(while: isAsciiDigit)))! : 1,
        items: items.map { raw in
            var text = Scalars()
            for (i, l) in raw.text.enumerated() {
                if i > 0 { text.append("\n") }
                text += l
            }
            let children = raw.rest.contains { !$0.isBlankLine } ? blocks(strip(raw.rest), depth: depth + 1) : []
            if let task = taskOf(text) { return MarkdownItem(text: String(task.text), task: task.task, children: children) }
            return MarkdownItem(text: String(text), task: nil, children: children)
        })
}
