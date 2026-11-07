// Conservative LaTeX fallback, mirrored by web/src/math.ts and Android ui/Math.kt.
private let mathSymbols: [String: String] = [
    "times": "×",
    "cdot": "·",
    "div": "÷",
    "pm": "±",
    "le": "≤",
    "leq": "≤",
    "ge": "≥",
    "geq": "≥",
    "ne": "≠",
    "neq": "≠",
    "approx": "≈",
    "to": "→",
    "rightarrow": "→",
    "Rightarrow": "⇒",
    "infty": "∞",
    "sum": "∑",
    "prod": "∏",
    "alpha": "α",
    "beta": "β",
    "gamma": "γ",
    "delta": "δ",
    "epsilon": "ε",
    "zeta": "ζ",
    "eta": "η",
    "theta": "θ",
    "iota": "ι",
    "kappa": "κ",
    "lambda": "λ",
    "mu": "μ",
    "nu": "ν",
    "xi": "ξ",
    "omicron": "ο",
    "pi": "π",
    "rho": "ρ",
    "sigma": "σ",
    "tau": "τ",
    "upsilon": "υ",
    "phi": "φ",
    "chi": "χ",
    "psi": "ψ",
    "omega": "ω",
    "Gamma": "Γ",
    "Delta": "Δ",
    "Theta": "Θ",
    "Lambda": "Λ",
    "Xi": "Ξ",
    "Pi": "Π",
    "Sigma": "Σ",
    "Upsilon": "Υ",
    "Phi": "Φ",
    "Psi": "Ψ",
    "Omega": "Ω",
    "varepsilon": "ϵ",
    "vartheta": "ϑ",
    "varpi": "ϖ",
    "varrho": "ϱ",
    "varsigma": "ς",
    "varphi": "ϕ",
    "%": "%",
    "{": "{",
    "}": "}",
    "_": "_",
    "$": "$",
    "!": "",
    ",": " ",
    ";": " ",
    ":": " ",
    " ": " ",
    "quad": " ",
    "qquad": " ",
    "enspace": " ",
    "thinspace": " ",
    "left": "",
    "right": "",
]
private let mathFormats: Set<String> = ["text", "textrm", "textnormal", "textbf", "textit", "mathrm", "mathbf", "mathit", "mathsf", "mathtt", "mathnormal", "operatorname"]
private let mathSuperscript: [String: String] = ["0": "⁰", "1": "¹", "2": "²", "3": "³", "4": "⁴", "5": "⁵", "6": "⁶", "7": "⁷", "8": "⁸", "9": "⁹", "+": "⁺", "-": "⁻", "=": "⁼", "(": "⁽", ")": "⁾", "i": "ⁱ", "n": "ⁿ", "a": "ᵃ", "b": "ᵇ", "c": "ᶜ", "d": "ᵈ", "e": "ᵉ", "f": "ᶠ", "g": "ᵍ", "h": "ʰ", "j": "ʲ", "k": "ᵏ", "l": "ˡ", "m": "ᵐ", "o": "ᵒ", "p": "ᵖ", "r": "ʳ", "s": "ˢ", "t": "ᵗ", "u": "ᵘ", "v": "ᵛ", "w": "ʷ", "x": "ˣ", "y": "ʸ", "z": "ᶻ", "A": "ᴬ", "B": "ᴮ", "D": "ᴰ", "E": "ᴱ", "G": "ᴳ", "H": "ᴴ", "I": "ᴵ", "J": "ᴶ", "K": "ᴷ", "L": "ᴸ", "M": "ᴹ", "N": "ᴺ", "O": "ᴼ", "P": "ᴾ", "R": "ᴿ", "T": "ᵀ", "U": "ᵁ", "V": "ⱽ", "W": "ᵂ", "−": "⁻"]
private let mathSubscript: [String: String] = ["0": "₀", "1": "₁", "2": "₂", "3": "₃", "4": "₄", "5": "₅", "6": "₆", "7": "₇", "8": "₈", "9": "₉", "+": "₊", "-": "₋", "=": "₌", "(": "₍", ")": "₎", "a": "ₐ", "e": "ₑ", "h": "ₕ", "i": "ᵢ", "j": "ⱼ", "k": "ₖ", "l": "ₗ", "m": "ₘ", "n": "ₙ", "o": "ₒ", "p": "ₚ", "r": "ᵣ", "s": "ₛ", "t": "ₜ", "u": "ᵤ", "v": "ᵥ", "x": "ₓ", "−": "₋"]

private func mathGrouped(_ v: String) -> String {
    v.unicodeScalars.contains { "+-*/=<>×·÷±≤≥≠≈→⇒^_".unicodeScalars.contains($0) || isSpace($0) } ? "(\(v))" : v
}

func mathText(_ source: String) -> String {
    simplifyMath(source) ?? source
}

/// Nil means unsupported syntax; an unchanged result can still be valid.
func simplifyMath(_ source: String) -> String? {
    var parser = MathTextParser(source: source.scalars)
    let result = parser.sequence(0)
    guard parser.valid, parser.i == parser.source.count else { return nil }
    // Collapse whitespace without changing unsupported source.
    var out = ""
    var space = false
    for c in result.unicodeScalars {
        if isSpace(c) { space = !out.isEmpty }
        else {
            if space { out += " " }
            out.unicodeScalars.append(c)
            space = false
        }
    }
    return out
}

private struct MathTextParser {
    let source: Scalars
    var i = 0
    var valid = true

    mutating func argument(_ depth: Int, required: Bool = true) -> String {
        while i < source.count, isSpace(source[i]) { i += 1 }
        if i < source.count, source[i] == "{" { i += 1; return sequence(depth + 1, group: true) }
        if required || i >= source.count || "}^_".unicodeScalars.contains(source[i]) { valid = false; return "" }
        return atom(depth + 1)
    }

    mutating func atom(_ depth: Int) -> String {
        if depth > 32 { valid = false; return "" }
        let c = source[i]
        i += 1
        if c == "{" { return sequence(depth + 1, group: true) }
        if c == "\\" {
            let start = i
            while i < source.count, (source[i] >= "a" && source[i] <= "z") || (source[i] >= "A" && source[i] <= "Z") { i += 1 }
            if i == start, i < source.count { i += 1 }
            let command = String(source[start..<i])
            if mathFormats.contains(command) { return argument(depth) }
            if ["frac", "dfrac", "tfrac"].contains(command) {
                let a = argument(depth)
                let b = argument(depth)
                return mathGrouped(a) + "/" + mathGrouped(b)
            }
            if command == "sqrt" { return "√" + mathGrouped(argument(depth)) }
            if let symbol = mathSymbols[command] { return symbol }
            valid = false
            return ""
        }
        if "}&#$".unicodeScalars.contains(c) { valid = false }
        return String(c)
    }

    mutating func sequence(_ depth: Int, group: Bool = false) -> String {
        var out = ""
        while valid, i < source.count, source[i] != "}" {
            if source[i] == "^" || source[i] == "_" {
                let mark = source[i]
                i += 1
                let value = argument(depth, required: false)
                let map = mark == "^" ? mathSuperscript : mathSubscript
                let chars = value.unicodeScalars.map { String($0) }
                if !chars.isEmpty, chars.allSatisfy({ map[$0] != nil }) { out += chars.map { map[$0]! }.joined() }
                else { out += String(mark) + mathGrouped(value) }
            } else { out += atom(depth) }
        }
        if group {
            if i < source.count, source[i] == "}" { i += 1 } else { valid = false }
        }
        return out
    }
}

struct MathRange {
    let start: Int
    let end: Int
    let source: String
}

private struct MathToken {
    let at: Int
    let value: String
    let escaped: Bool
    let line: Int
}

/// Index closing delimiters and matching code runs once, including failed openers.
func mathRanges(_ text: Scalars, singleLineCode: Bool = false) -> [MathRange] {
    var tokens: [MathToken] = []
    var slashes = 0
    var line = 0
    var i = 0
    while i < text.count {
        let pair = String(text[i..<min(i + 2, text.count)])
        if text[i] == "`" {
            var end = i + 1
            while end < text.count, text[end] == "`" { end += 1 }
            tokens.append(MathToken(at: i, value: String(text[i..<end]), escaped: slashes % 2 == 1, line: line))
            i = end - 1
        } else if ["$$", "\\[", "\\]", "\\(", "\\)"].contains(pair) {
            tokens.append(MathToken(at: i, value: pair, escaped: slashes % 2 == 1, line: line))
            i += 1
        } else if text[i] == "$", i == 0 || text[i - 1] != "$" {
            tokens.append(MathToken(at: i, value: "$", escaped: slashes % 2 == 1, line: line))
        }
        if text[i] == "\n" { line += 1 }
        slashes = text[i] == "\\" ? slashes + 1 : 0
        i += 1
    }
    var next: [String: Int] = [:]
    var close: [Int: Int] = [:]
    for t in tokens.reversed() {
        let key = t.value == "\\[" ? "\\]" : t.value == "\\(" ? "\\)" : t.value
        let lineBound = t.value == "$" || (singleLineCode && t.value.first == "`")
        if let end = next[lineBound ? "\(t.line):\(key)" : key] { close[t.at] = end }
        if !t.escaped || t.value.first == "`" { next[lineBound ? "\(t.line):\(t.value)" : t.value] = t.at }
    }
    var out: [MathRange] = []
    var until = 0
    for t in tokens {
        if t.at < until || t.escaped { continue }
        guard let end = close[t.at] else { continue }
        if t.value.first == "`" { until = end + t.value.count }
        else if t.value == "$" {
            // Convert only adjacent single-dollar gaps, even after rejection.
            if t.at + 1 == end || isSpace(text[t.at + 1]) || isSpace(text[end - 1]) { continue }
            if end + 1 < text.count, text[end + 1] >= "0", text[end + 1] <= "9" { continue }
            let content = text[t.at + 1..<end]
            guard content.contains(where: { $0 == "\\" || $0 == "^" || $0 == "_" }) else { continue }
            let source = String(content)
            guard simplifyMath(source) != nil else { continue }
            out.append(MathRange(start: t.at, end: end + 1, source: source))
            until = end + 1
        }
        else if ["$$", "\\[", "\\("].contains(t.value) {
            out.append(MathRange(start: t.at, end: end + 2, source: String(text[t.at + 2..<end])))
            until = end + 2
        }
    }
    return out
}

func maskMath(_ text: Scalars, _ ranges: [MathRange]) -> Scalars {
    var chars = text
    for r in ranges {
        let width = text[r.start] == "$" && text[r.start + 1] != "$" ? 1 : 2
        for i in r.start + width..<r.end where chars[i] != "\n" { chars[i] = "a" }
    }
    return chars
}

func mathParagraphs(_ lines: [Scalars]) -> [Int: Int] {
    var text = Scalars()
    var starts: [Int] = [], ends: [Int] = [], numbers: [Int] = []
    var offset = 0
    for (i, line) in lines.enumerated() {
        if i > 0 { text.append("\n") }
        text += line
        let end = offset + line.count
        let first = offset + line.indent
        let last = offset + (line.lastIndex(where: { !isSpace($0) }).map { $0 + 1 } ?? 0)
        for _ in offset...end { starts.append(first); ends.append(last); numbers.append(i) }
        offset = end + 1
    }
    var out: [Int: Int] = [:]
    var fenceMark: Unicode.Scalar?
    var fenceCount = 0
    let outsideCode: [Scalars] = lines.map { line in
        let trimmed = line.trimmed()
        if let mark = fenceMark {
            if trimmed.count >= fenceCount, trimmed.allSatisfy({ $0 == mark }) { fenceMark = nil }
            return Scalars(repeating: " ", count: line.count)
        }
        if let first = trimmed.first, first == "`" || first == "~" {
            let count = trimmed.prefix(while: { $0 == first }).count
            if count >= 3 {
                fenceMark = first
                fenceCount = count
                return Scalars(repeating: " ", count: line.count)
            }
        }
        return line
    }
    var masked = Scalars()
    for (i, line) in outsideCode.enumerated() {
        if i > 0 { masked.append("\n") }
        masked += line
    }
    for r in mathRanges(masked, singleLineCode: true) {
        if !(text.starts(with: "$$", at: r.start) || text.starts(with: "\\[", at: r.start)) || r.start != starts[r.start] || r.end != ends[r.end - 1] { continue }
        out[numbers[r.start]] = numbers[r.end - 1]
    }
    return out
}

extension Markdown {
    /// Keep paragraph line boundaries except those inside a formula.
    public static func mathLines(_ text: String) -> [String] {
        let source = text.scalars
        let ranges = Dictionary(uniqueKeysWithValues: mathRanges(source, singleLineCode: true).map { ($0.start, $0) })
        var lines: [String] = []
        var start = 0
        var i = 0
        while i < source.count {
            if let formula = ranges[i] { i = formula.end }
            else {
                if source[i] == "\n" { lines.append(String(source[start..<i])); start = i + 1 }
                i += 1
            }
        }
        lines.append(String(source[start...]))
        return lines
    }
}
