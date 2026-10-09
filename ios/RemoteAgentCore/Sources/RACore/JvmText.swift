// Text as the JVM handles it. The rules in Model.swift and beside it were
// written against Kotlin strings, and the web client, the daemon and the
// Android app must show the same: lengths and cuts count UTF-16 units,
// comparison is by code unit, whitespace is Java's, and a regular
// expression's \s, \d, ., ^ and $ are Java's ASCII-minded ones. Swift's own
// String works on grapheme clusters under canonical equivalence and ICU's
// classes are Unicode-wide, so each place that would differ goes through
// here.
import Foundation

/// The wall clock in milliseconds since the epoch, as System.currentTimeMillis.
public func nowMs() -> Int64 { Int64((Date().timeIntervalSince1970 * 1000).rounded(.down)) }

/// Kotlin's Char.isWhitespace: the Unicode separators (non-breaking ones included) and the ASCII controls Java counts.
func isJvmWhitespace(_ u: Unicode.Scalar) -> Bool {
    switch u.value {
    case 0x09...0x0D, 0x1C...0x1F: return true
    default:
        switch u.properties.generalCategory {
        case .spaceSeparator, .lineSeparator, .paragraphSeparator: return true
        default: return false
        }
    }
}

/// Kotlin's Char.isDigit for a UTF-16 unit: a decimal digit of any script in the BMP.
func isJvmDigit(_ unit: UInt16) -> Bool {
    guard let u = Unicode.Scalar(unit) else { return false }
    return u.properties.generalCategory == .decimalNumber
}

/// One UTF-16 unit's simple case mappings, as Character.toUpperCase and toLowerCase give them: half a surrogate pair maps to itself.
private func simpleCase(_ unit: UInt16) -> (upper: UInt16, lower: UInt16) {
    if unit < 0x80 {
        if unit >= 0x61 && unit <= 0x7A { return (unit - 0x20, unit) }
        if unit >= 0x41 && unit <= 0x5A { return (unit, unit + 0x20) }
        return (unit, unit)
    }
    guard let u = Unicode.Scalar(unit) else { return (unit, unit) }
    func one(_ s: String) -> UInt16 {
        // A mapping to several characters (ß to SS) is not a simple one: Java leaves the character.
        let units = Array(s.utf16)
        return units.count == 1 ? units[0] : unit
    }
    return (one(u.properties.uppercaseMapping), one(u.properties.lowercaseMapping))
}

extension String {
    /// The string of a slice of UTF-16 units. A cut through a surrogate pair,
    /// which Kotlin keeps as a lone surrogate, loses that half here: Swift
    /// strings hold no lone surrogates.
    fileprivate init(units: some Sequence<UInt16>) {
        var out = String.UnicodeScalarView()
        var it = units.makeIterator()
        var decoder = UTF16()
        loop: while true {
            switch decoder.decode(&it) {
            case .scalarValue(let s): out.append(s)
            case .emptyInput: break loop
            case .error: continue
            }
        }
        self.init(out)
    }

    /// Kotlin's String.length.
    var jvmLength: Int { utf16.count }

    /// Kotlin's take(n).
    func jvmTake(_ n: Int) -> String {
        if n >= utf16.count { return self }
        return String(units: utf16.prefix(max(0, n)))
    }

    /// Kotlin's takeLast(n).
    func jvmTakeLast(_ n: Int) -> String {
        if n >= utf16.count { return self }
        return String(units: utf16.suffix(max(0, n)))
    }

    /// Kotlin's substring(n).
    func jvmDrop(_ n: Int) -> String {
        if n <= 0 { return self }
        return String(units: utf16.dropFirst(n))
    }

    /// Kotlin's trim().
    func jvmTrim() -> String {
        let s = unicodeScalars
        guard let first = s.firstIndex(where: { !isJvmWhitespace($0) }) else { return "" }
        let last = s.lastIndex(where: { !isJvmWhitespace($0) })!
        return String(s[first...last])
    }

    /// Kotlin's trimStart().
    func jvmTrimStart() -> String {
        let s = unicodeScalars
        guard let first = s.firstIndex(where: { !isJvmWhitespace($0) }) else { return "" }
        return String(s[first...])
    }

    /// Kotlin's trimEnd().
    func jvmTrimEnd() -> String {
        let s = unicodeScalars
        guard let last = s.lastIndex(where: { !isJvmWhitespace($0) }) else { return "" }
        return String(s[...last])
    }

    /// Kotlin's isBlank().
    var jvmIsBlank: Bool { unicodeScalars.allSatisfy(isJvmWhitespace) }

    /// Kotlin's trim(char) for one ASCII character.
    func trimming(ascii c: UInt8) -> String {
        let u = Array(utf8)
        guard let first = u.firstIndex(where: { $0 != c }), let last = u.lastIndex(where: { $0 != c }) else { return "" }
        return String(decoding: u[first...last], as: UTF8.self)
    }

    /// Kotlin's trimEnd(char) for one ASCII character.
    func trimmingEnd(ascii c: UInt8) -> String {
        let u = Array(utf8)
        guard let last = u.lastIndex(where: { $0 != c }) else { return "" }
        return String(decoding: u[...last], as: UTF8.self)
    }

    /// Kotlin's startsWith: code units, where Swift's hasPrefix wants whole
    /// grapheme clusters and takes canonically equal text for the same.
    func jvmStartsWith(_ prefix: String) -> Bool { utf8.starts(with: prefix.utf8) }

    func jvmEndsWith(_ suffix: String) -> Bool { utf8.reversed().starts(with: suffix.utf8.reversed()) }

    /// Kotlin's removePrefix.
    func removingPrefix(_ prefix: String) -> String {
        jvmStartsWith(prefix) ? String(decoding: utf8.dropFirst(prefix.utf8.count), as: UTF8.self) : self
    }

    /// Kotlin's removeSuffix.
    func removingSuffix(_ suffix: String) -> String {
        jvmEndsWith(suffix) ? String(decoding: utf8.dropLast(suffix.utf8.count), as: UTF8.self) : self
    }

    /// Kotlin's == on strings: the same code units. Swift's == also holds for
    /// canonically equal text (a precomposed é and e with a combining accent).
    func jvmEquals(_ other: String) -> Bool { utf8.elementsEqual(other.utf8) }

    /// Kotlin's contains(other, ignoreCase = true): unit by unit, equal when
    /// their simple upper or lower case mappings are, with no locale and no
    /// mapping to several characters.
    func jvmContainsIgnoringCase(_ needle: String) -> Bool {
        let n = Array(needle.utf16)
        if n.isEmpty { return true }
        let h = Array(utf16)
        if n.count > h.count { return false }
        func same(_ a: UInt16, _ b: UInt16) -> Bool {
            if a == b { return true }
            let x = simpleCase(a), y = simpleCase(b)
            return x.upper == y.upper || x.lower == y.lower
        }
        outer: for start in 0...(h.count - n.count) {
            for i in 0..<n.count where !same(h[start + i], n[i]) { continue outer }
            return true
        }
        return false
    }

    /// The string, or `other` when it is empty: Kotlin's ifEmpty.
    func ifEmpty(_ other: @autoclosure () -> String) -> String { isEmpty ? other() : self }

    /// Kotlin's split(char) for one ASCII character, empty parts kept.
    func split(ascii c: UInt8) -> [String] {
        utf8.split(separator: c, omittingEmptySubsequences: false).map { String(decoding: $0, as: UTF8.self) }
    }

    /// Kotlin's lines(): split at \r\n, \n and \r.
    func jvmLines() -> [String] {
        var out = [String]()
        var cur = [UInt8]()
        var it = utf8.makeIterator()
        var pending = it.next()
        while let b = pending {
            pending = it.next()
            if b == 0x0D {
                if pending == 0x0A { pending = it.next() }
                out.append(String(decoding: cur, as: UTF8.self))
                cur.removeAll(keepingCapacity: true)
            } else if b == 0x0A {
                out.append(String(decoding: cur, as: UTF8.self))
                cur.removeAll(keepingCapacity: true)
            } else {
                cur.append(b)
            }
        }
        out.append(String(decoding: cur, as: UTF8.self))
        return out
    }
}

/// Kotlin's String.compareTo as a "comes before": by UTF-16 unit. Swift's <
/// orders by Unicode scalar under canonical equivalence, which puts an emoji
/// after U+FFxx where UTF-16 puts it before.
func jvmLess(_ a: String, _ b: String) -> Bool { a.utf16.lexicographicallyPrecedes(b.utf16) }

extension Sequence {
    /// A stable sort, as Kotlin's sortedWith is: elements that compare equal keep their order. Swift documents no such promise for sorted(by:).
    func stableSorted(by before: (Element, Element) -> Bool) -> [Element] {
        enumerated().sorted { a, b in
            if before(a.element, b.element) { return true }
            if before(b.element, a.element) { return false }
            return a.offset < b.offset
        }.map(\.element)
    }

    /// Kotlin's distinct(): the first of each, in order.
    func distinct<K: Hashable>(by key: (Element) -> K) -> [Element] {
        var seen = Set<K>()
        return filter { seen.insert(key($0)).inserted }
    }
}

extension Sequence where Element: Hashable {
    func distinct() -> [Element] { distinct(by: { $0 }) }
}

// --- regular expressions ---

/// Pieces that spell Java's classes in an ICU pattern.
enum Jre {
    /// \s inside a character class: Java's is ASCII only, ICU's every Unicode space.
    static let s = " \\t\\n\\x0B\\f\\r"
    /// \s on its own.
    static let space = "[ \\t\\n\\x0B\\f\\r]"
    /// \d: ASCII digits, where ICU takes every script's.
    static let digit = "[0-9]"
    /// The dot: anything but Java's line terminators (ICU adds \v and \f to them).
    static let dot = "[^\\n\\r\\u0085\\u2028\\u2029]"
    /// $ without MULTILINE: the end, or before a line terminator that ends the input.
    static let end = "(?=(?:\\r\\n|[\\n\\r\\u0085\\u2028\\u2029])?\\z)"
    /// ^ with MULTILINE: the start, or after a line terminator (\r\n being one).
    static let lineStart = "(?:\\A|(?<=[\\n\\u0085\\u2028\\u2029])|(?<=\\r)(?!\\n))"
}

/// A compiled pattern with the few operations Kotlin's Regex is used for. It
/// works on UTF-16 like the JVM's, so ranges mean the same.
struct Rx: @unchecked Sendable {
    // NSRegularExpression is immutable and safe to use from several threads.
    private let re: NSRegularExpression
    private let whole: NSRegularExpression

    struct Match {
        fileprivate let source: NSString
        fileprivate let result: NSTextCheckingResult

        var range: NSRange { result.range }
        var value: String { source.substring(with: result.range) }

        /// A group's text, nil when the group took no part in the match.
        func group(_ i: Int) -> String? {
            let r = result.range(at: i)
            return r.location == NSNotFound ? nil : source.substring(with: r)
        }

        /// Kotlin's groupValues[i]: empty for a group that took no part.
        func text(_ i: Int) -> String { group(i) ?? "" }
    }

    init(_ pattern: String, ignoreCase: Bool = false) {
        let options: NSRegularExpression.Options = ignoreCase ? [.caseInsensitive] : []
        // The patterns are constants of this module: one that does not compile is a programming error.
        re = try! NSRegularExpression(pattern: pattern, options: options)
        whole = try! NSRegularExpression(pattern: "\\A(?:" + pattern + ")\\z", options: options)
    }

    private func all(_ s: NSString) -> NSRange { NSRange(location: 0, length: s.length) }

    /// Kotlin's find.
    func find(_ s: String) -> Match? {
        let ns = s as NSString
        return re.firstMatch(in: s, range: all(ns)).map { Match(source: ns, result: $0) }
    }

    /// Kotlin's findAll.
    func findAll(_ s: String) -> [Match] {
        let ns = s as NSString
        return re.matches(in: s, range: all(ns)).map { Match(source: ns, result: $0) }
    }

    /// Kotlin's matchEntire: the pattern must take the whole text.
    func matchEntire(_ s: String) -> Match? {
        let ns = s as NSString
        return whole.firstMatch(in: s, range: all(ns)).map { Match(source: ns, result: $0) }
    }

    func containsMatch(in s: String) -> Bool { find(s) != nil }

    /// Kotlin's replace with a function of the match.
    func replace(_ s: String, first: Bool = false, _ with: (Match) -> String) -> String {
        let ns = s as NSString
        var results = re.matches(in: s, range: all(ns))
        if results.isEmpty { return s }
        if first { results = [results[0]] }
        let out = NSMutableString()
        var at = 0
        for r in results {
            out.append(ns.substring(with: NSRange(location: at, length: r.range.location - at)))
            out.append(with(Match(source: ns, result: r)))
            at = r.range.location + r.range.length
        }
        out.append(ns.substring(from: at))
        return out as String
    }

    /// Kotlin's replace with fixed text (no group references).
    func replace(_ s: String, with text: String) -> String { replace(s) { _ in text } }

    /// Kotlin's replace with "$1".
    func replaceWithGroup1(_ s: String) -> String { replace(s) { $0.text(1) } }

    /// Kotlin's Regex.split: the parts between matches, empty ones kept.
    func split(_ s: String) -> [String] {
        let ns = s as NSString
        var out = [String]()
        var at = 0
        for r in re.matches(in: s, range: all(ns)) where r.range.length > 0 {
            out.append(ns.substring(with: NSRange(location: at, length: r.range.location - at)))
            at = r.range.location + r.range.length
        }
        out.append(ns.substring(from: at))
        return out
    }
}

/// %02d.
func pad2(_ n: Int) -> String { n < 10 && n >= 0 ? "0\(n)" : "\(n)" }

// --- local dates ---

/// A calendar day in some time zone, with what java.time's LocalDate is asked for.
struct LocalDay: Equatable {
    var year: Int
    var month: Int
    var day: Int
    var hour: Int
    var minute: Int

    init(ms: Int64, zone: TimeZone) {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = zone
        let c = cal.dateComponents([.year, .month, .day, .hour, .minute], from: Date(timeIntervalSince1970: Double(ms) / 1000))
        year = c.year ?? 1970
        month = c.month ?? 1
        day = c.day ?? 1
        hour = c.hour ?? 0
        minute = c.minute ?? 0
    }

    /// Days since 1970-01-01, by the proleptic Gregorian calendar.
    var epochDay: Int {
        let y = month <= 2 ? year - 1 : year
        let era = (y >= 0 ? y : y - 399) / 400
        let yoe = y - era * 400
        let doy = (153 * (month > 2 ? month - 3 : month + 9) + 2) / 5 + day - 1
        let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy
        return era * 146097 + doe - 719468
    }

    /// 0 for Monday up to 6 for Sunday.
    var weekdayIndex: Int { (((epochDay + 3) % 7) + 7) % 7 }

    func sameDay(_ o: LocalDay) -> Bool { year == o.year && month == o.month && day == o.day }

    /// 9:05: the hour as it is, the minute in two digits.
    var hhmm: String { "\(hour):\(pad2(minute))" }
}

let weekdayNames = ["周一", "周二", "周三", "周四", "周五", "周六", "周日"]

/// Kotlin's Double.toInt(): toward zero, the ends of Int32 for what lies past them, 0 for NaN. Swift's Int(_:) traps on those.
func jvmInt(_ d: Double) -> Int {
    if d.isNaN { return 0 }
    if d >= Double(Int32.max) { return Int(Int32.max) }
    if d <= Double(Int32.min) { return Int(Int32.min) }
    return Int(d.rounded(.towardZero))
}

/// Math.round for a value that fits: halves go up.
func jvmRound(_ d: Double) -> Int { jvmInt((d + 0.5).rounded(.down)) }
