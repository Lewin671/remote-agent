// Inline Markdown: `code`, **bold**, *italic*, _italic_, ~~struck~~,
// [links](target), <https://autolinks> and bare https:// URLs.
//
// Android reads these with one regular expression; here each alternative of
// it is a function, tried in the same order at each position, so the two
// agree on which mark wins where several could start.

extension Markdown {
    /// Whether a link target may be opened: web pages only, so `javascript:`, `data:` and file paths never become tappable.
    public static func safeHref(_ url: String) -> String? {
        let u = url.scalars.trimmed()
        let rest: Int
        if hasPrefixIgnoringCase(u, "https://") {
            rest = 8
        } else if hasPrefixIgnoringCase(u, "http://") {
            rest = 7
        } else {
            return nil
        }
        return u.count > rest && !u.contains(where: isSpace) ? String(u) : nil
    }

    public static func inlineSpans(_ text: String) -> [MarkdownSpan] {
        spans(text.scalars)
    }

    /// What spans say once their marks are gone: `**42 ms**` reads "42 ms".
    public static func shownText(_ spans: [MarkdownSpan]) -> String {
        var out = ""
        appendShown(spans, to: &out)
        return out
    }
}

private func appendShown(_ spans: [MarkdownSpan], to out: inout String) {
    for s in spans {
        switch s {
        case .text(let v), .code(let v): out += v
        case .link(let v, _), .ref(let v, _): out += v
        case .bold(let c), .italic(let c), .strike(let c): appendShown(c, to: &out)
        }
    }
}

private func hasPrefixIgnoringCase(_ text: ArraySlice<Unicode.Scalar>, _ prefix: String) -> Bool {
    var i = text.startIndex
    for want in prefix.unicodeScalars {
        guard i < text.endIndex else { return false }
        // The prefixes are ASCII and lower case; only an ASCII capital may fold.
        let c = text[i].value
        let lower = c >= 0x41 && c <= 0x5A ? c + 0x20 : c
        guard lower == want.value else { return false }
        i += 1
    }
    return true
}

private struct LinkEnd {
    let close: Int
    let end: Int
    let auto: Bool
}

/// Index link delimiters once. A failed '[' or autolink opener must not rescan the rest of the text on every character.
private func inlineLinks(_ text: Scalars) -> [Int: LinkEnd] {
    var links: [Int: LinkEnd] = [:]
    var close = -1
    var end = -1
    var validTarget = false
    var paren = -1
    var angle = -1
    var space = text.count
    var newline = text.count
    for i in text.indices.reversed() {
        let c = text[i]
        if c == "[" {
            if close > i + 1, close < newline, validTarget { links[i] = LinkEnd(close: close, end: end, auto: false) }
        } else if c == "<", angle < space {
            let prefix = text.starts(with: "https://", at: i + 1) ? 8 : text.starts(with: "http://", at: i + 1) ? 7 : 0
            if prefix > 0, angle > i + 1 + prefix { links[i] = LinkEnd(close: angle, end: angle, auto: true) }
        }
        if c == "]" {
            close = i
            end = paren
            validTarget = i + 1 < text.count && text[i + 1] == "(" && paren > i + 2 && paren < space
        }
        if c == ")" { paren = i }
        if c == ">" { angle = i }
        if isSpace(c) { space = i }
        if c == "\n" { newline = i }
    }
    return links
}

/// A bare URL without the sentence's punctuation after it; a closing bracket stays only while the URL has an opening one for it. Empty when nothing but the scheme is left.
private func bareUrl(_ raw: ArraySlice<Unicode.Scalar>) -> ArraySlice<Unicode.Scalar> {
    // How many closing brackets have no opening one: those end the sentence.
    var paren = 0
    var bracket = 0
    for c in raw {
        switch c {
        case "(": paren -= 1
        case ")": paren += 1
        case "[": bracket -= 1
        case "]": bracket += 1
        default: break
        }
    }
    var end = raw.endIndex
    while end > raw.startIndex {
        let c = raw[end - 1]
        if c == ")", paren > 0 {
            paren -= 1
        } else if c == "]", bracket > 0 {
            bracket -= 1
        } else {
            switch c {
            case ".", ",", ";", ":", "!", "?", "'", "\"", "*", "_", "~": break
            default: return schemeOnly(raw[raw.startIndex..<end]) ? raw[raw.startIndex..<raw.startIndex] : raw[raw.startIndex..<end]
            }
        }
        end -= 1
    }
    return raw[raw.startIndex..<raw.startIndex]
}

private func schemeOnly(_ url: ArraySlice<Unicode.Scalar>) -> Bool {
    (url.count == 8 && hasPrefixIgnoringCase(url, "https://")) || (url.count == 7 && hasPrefixIgnoringCase(url, "http://"))
}

/// Where a bare URL stops: white space, angle brackets, and the punctuation of the sentence around it (dashes, curly quotes, full-width marks).
private func endsBareUrl(_ c: Unicode.Scalar) -> Bool {
    if isSpace(c) || c == "<" || c == ">" { return true }
    switch c.value {
    case 0x2010...0x2027, 0x3000...0x303F, 0xFF00...0xFFEF: return true
    default: return false
    }
}

/// A letter or a number of any script, or an underscore: what `_` must not touch to be emphasis.
private func isWordOfAnyScript(_ c: Unicode.Scalar) -> Bool {
    if c == "_" { return true }
    switch c.properties.generalCategory {
    case .uppercaseLetter, .lowercaseLetter, .titlecaseLetter, .modifierLetter, .otherLetter,
         .decimalNumber, .letterNumber, .otherNumber: return true
    default: return false
    }
}

private enum Mark {
    case ticks, bold, star, under, struck, bracket, autolink, bare
}

/// One mark found at `at`: the whole of it ends at `end`, and `inner` is what it wraps.
private struct Found {
    let mark: Mark
    let at: Int
    let end: Int
    let inner: Range<Int>
}

/// `open X+ open` for bold and strike: a doubled delimiter around at least one scalar that is neither the delimiter nor a line break.
private func doubled(_ t: Scalars, _ at: Int, _ d: Unicode.Scalar) -> Range<Int>? {
    guard at + 1 < t.count, t[at + 1] == d else { return nil }
    var j = at + 2
    while j < t.count, t[j] != d, t[j] != "\n" { j += 1 }
    guard j > at + 2, j + 1 < t.count, t[j] == d, t[j + 1] == d else { return nil }
    return at + 2..<j
}

/// `d X d` for italics: the first scalar inside is not white space, and the
/// delimiters touch no word on their outer sides. The content cannot hold
/// the delimiter, so the first one after it either closes or nothing does.
private func single(_ t: Scalars, _ at: Int, _ d: Unicode.Scalar, word: (Unicode.Scalar) -> Bool) -> Range<Int>? {
    if at > 0, word(t[at - 1]) { return nil }
    guard at + 1 < t.count, t[at + 1] != d, !isSpace(t[at + 1]) else { return nil }
    var j = at + 2
    while j < t.count, t[j] != d, t[j] != "\n" { j += 1 }
    guard j < t.count, t[j] == d else { return nil }
    if j + 1 < t.count, word(t[j + 1]) { return nil }
    return at + 1..<j
}

private func markAt(_ t: Scalars, _ at: Int) -> Found? {
    let c = t[at]
    switch c {
    case "`":
        var end = at + 1
        while end < t.count, t[end] == "`" { end += 1 }
        return Found(mark: .ticks, at: at, end: end, inner: at..<end)
    case "*":
        if let inner = doubled(t, at, "*") { return Found(mark: .bold, at: at, end: inner.upperBound + 2, inner: inner) }
        if let inner = single(t, at, "*", word: { isAsciiWord($0) || $0 == "*" }) {
            return Found(mark: .star, at: at, end: inner.upperBound + 1, inner: inner)
        }
    case "_":
        if let inner = single(t, at, "_", word: isWordOfAnyScript) { return Found(mark: .under, at: at, end: inner.upperBound + 1, inner: inner) }
    case "~":
        if let inner = doubled(t, at, "~") { return Found(mark: .struck, at: at, end: inner.upperBound + 2, inner: inner) }
    case "[":
        return Found(mark: .bracket, at: at, end: at + 1, inner: at..<at + 1)
    case "<":
        let n = t.starts(with: "<https://", at: at) ? 9 : t.starts(with: "<http://", at: at) ? 8 : 0
        if n > 0 { return Found(mark: .autolink, at: at, end: at + n, inner: at..<at + n) }
    case "h":
        let n = t.starts(with: "https://", at: at) ? 8 : t.starts(with: "http://", at: at) ? 7 : 0
        guard n > 0, at == 0 || !(isAsciiWord(t[at - 1]) || t[at - 1] == "/") else { return nil }
        var end = at + n
        while end < t.count, !endsBareUrl(t[end]) { end += 1 }
        if end > at + n { return Found(mark: .bare, at: at, end: end, inner: at..<end) }
    default:
        break
    }
    return nil
}

func spans(_ text: Scalars) -> [MarkdownSpan] {
    var out: [MarkdownSpan] = []
    var plain = Scalars()
    func add(_ span: MarkdownSpan) {
        if !plain.isEmpty {
            out.append(.text(String(plain)))
            plain.removeAll(keepingCapacity: true)
        }
        out.append(span)
    }
    let links = inlineLinks(text)
    // The next run of as many backticks closes a run, found without rescanning.
    var closers: [Int: Int] = [:]
    var next: [Int: Int] = [:]
    var i = text.count
    while i > 0 {
        i -= 1
        guard text[i] == "`" else { continue }
        var start = i
        while start > 0, text[start - 1] == "`" { start -= 1 }
        let length = i - start + 1
        if let later = next[length] { closers[start] = later }
        next[length] = start
        i = start
    }
    var last = 0
    var from = 0
    while from < text.count {
        guard let m = markAt(text, from) else {
            from += 1
            continue
        }
        let at = m.at
        from = m.end
        let escapedTick = m.mark == .ticks && escapedAt(text, at)
        if at > last { plain += text[last..<(escapedTick ? at - 1 : at)] }
        // Where the text goes on when a branch below took more or less than the match.
        var resume = -1
        switch m.mark {
        case .ticks:
            let n = m.end - at
            if !escapedTick, let end = closers[at] {
                add(.code(String(text[at + n..<end])))
                resume = end + n
            } else {
                plain += text[m.inner]
            }
        // Emphasis may wrap code and links, so its content is inline Markdown too.
        // It cannot hold its own delimiter, which is what bounds the nesting.
        case .bold: add(.bold(spans(Array(text[m.inner]))))
        case .star, .under: add(.italic(spans(Array(text[m.inner]))))
        case .struck: add(.strike(spans(Array(text[m.inner]))))
        case .bare:
            let url = bareUrl(text[m.inner])
            if !url.isEmpty {
                let s = String(url)
                add(.link(text: s, href: s))
                resume = at + url.count
            } else {
                plain += text[m.inner]
            }
        case .bracket, .autolink:
            if let link = links[at] {
                let label = String(text[at + 1..<link.close])
                let target = link.auto ? label : String(text[link.close + 2..<link.end])
                if let href = Markdown.safeHref(target) {
                    add(.link(text: label, href: href))
                } else {
                    add(.ref(text: label, target: target))
                }
                resume = link.end + 1
            } else {
                plain += text[m.inner]
            }
        }
        if resume >= 0 {
            last = resume
            from = resume
        } else {
            last = m.end
        }
    }
    if last < text.count { plain += text[last...] }
    if !plain.isEmpty { out.append(.text(String(plain))) }
    return out
}
