package io.github.lewin671.remoteagent.ui

// Mirrored by web/src/math.ts and RAMarkdown/Math.swift; shared fixtures check all three.
private val mathSymbols = mapOf(
    "times" to "×",
    "cdot" to "·",
    "div" to "÷",
    "pm" to "±",
    "le" to "≤",
    "leq" to "≤",
    "ge" to "≥",
    "geq" to "≥",
    "ne" to "≠",
    "neq" to "≠",
    "approx" to "≈",
    "to" to "→",
    "rightarrow" to "→",
    "Rightarrow" to "⇒",
    "infty" to "∞",
    "sum" to "∑",
    "prod" to "∏",
    "alpha" to "α",
    "beta" to "β",
    "gamma" to "γ",
    "delta" to "δ",
    "epsilon" to "ε",
    "zeta" to "ζ",
    "eta" to "η",
    "theta" to "θ",
    "iota" to "ι",
    "kappa" to "κ",
    "lambda" to "λ",
    "mu" to "μ",
    "nu" to "ν",
    "xi" to "ξ",
    "omicron" to "ο",
    "pi" to "π",
    "rho" to "ρ",
    "sigma" to "σ",
    "tau" to "τ",
    "upsilon" to "υ",
    "phi" to "φ",
    "chi" to "χ",
    "psi" to "ψ",
    "omega" to "ω",
    "Gamma" to "Γ",
    "Delta" to "Δ",
    "Theta" to "Θ",
    "Lambda" to "Λ",
    "Xi" to "Ξ",
    "Pi" to "Π",
    "Sigma" to "Σ",
    "Upsilon" to "Υ",
    "Phi" to "Φ",
    "Psi" to "Ψ",
    "Omega" to "Ω",
    "varepsilon" to "ϵ",
    "vartheta" to "ϑ",
    "varpi" to "ϖ",
    "varrho" to "ϱ",
    "varsigma" to "ς",
    "varphi" to "ϕ",
    "%" to "%",
    "{" to "{",
    "}" to "}",
    "_" to "_",
    "\$" to "\$",
    "!" to "",
    "," to " ",
    ";" to " ",
    ":" to " ",
    " " to " ",
    "quad" to " ",
    "qquad" to " ",
    "enspace" to " ",
    "thinspace" to " ",
    "left" to "",
    "right" to "",
)
private val mathFormats = setOf("text", "textrm", "textnormal", "textbf", "textit", "mathrm", "mathbf", "mathit", "mathsf", "mathtt", "mathnormal", "operatorname")
private val mathSuperscript = mapOf("0" to "⁰", "1" to "¹", "2" to "²", "3" to "³", "4" to "⁴", "5" to "⁵", "6" to "⁶", "7" to "⁷", "8" to "⁸", "9" to "⁹", "+" to "⁺", "-" to "⁻", "=" to "⁼", "(" to "⁽", ")" to "⁾", "i" to "ⁱ", "n" to "ⁿ", "a" to "ᵃ", "b" to "ᵇ", "c" to "ᶜ", "d" to "ᵈ", "e" to "ᵉ", "f" to "ᶠ", "g" to "ᵍ", "h" to "ʰ", "j" to "ʲ", "k" to "ᵏ", "l" to "ˡ", "m" to "ᵐ", "o" to "ᵒ", "p" to "ᵖ", "r" to "ʳ", "s" to "ˢ", "t" to "ᵗ", "u" to "ᵘ", "v" to "ᵛ", "w" to "ʷ", "x" to "ˣ", "y" to "ʸ", "z" to "ᶻ", "A" to "ᴬ", "B" to "ᴮ", "D" to "ᴰ", "E" to "ᴱ", "G" to "ᴳ", "H" to "ᴴ", "I" to "ᴵ", "J" to "ᴶ", "K" to "ᴷ", "L" to "ᴸ", "M" to "ᴹ", "N" to "ᴺ", "O" to "ᴼ", "P" to "ᴾ", "R" to "ᴿ", "T" to "ᵀ", "U" to "ᵁ", "V" to "ⱽ", "W" to "ᵂ", "−" to "⁻")
private val mathSubscript = mapOf("0" to "₀", "1" to "₁", "2" to "₂", "3" to "₃", "4" to "₄", "5" to "₅", "6" to "₆", "7" to "₇", "8" to "₈", "9" to "₉", "+" to "₊", "-" to "₋", "=" to "₌", "(" to "₍", ")" to "₎", "a" to "ₐ", "e" to "ₑ", "h" to "ₕ", "i" to "ᵢ", "j" to "ⱼ", "k" to "ₖ", "l" to "ₗ", "m" to "ₘ", "n" to "ₙ", "o" to "ₒ", "p" to "ₚ", "r" to "ᵣ", "s" to "ₛ", "t" to "ₜ", "u" to "ᵤ", "v" to "ᵥ", "x" to "ₓ", "−" to "₋")
private fun mathGrouped(v: String): String = if (v.any { it in "+-*/=<>×·÷±≤≥≠≈→⇒^_" || it.isSpace() }) "($v)" else v

internal fun mathText(source: String): String = simplifyMath(source) ?: source

/** Null means unsupported syntax; an unchanged result can still be valid. */
internal fun simplifyMath(source: String): String? = MathTextParser(source).parse()

private class MathTextParser(private val source: String) {
    private var i = 0
    private var valid = true

    fun parse(): String? {
        val result = sequence(0)
        if (!valid || i != source.length) return null
        return buildString {
            var space = false
            for (c in result) {
                if (c.isSpace()) space = isNotEmpty()
                else {
                    if (space) append(' ')
                    append(c)
                    space = false
                }
            }
        }
    }

    private fun argument(depth: Int, required: Boolean = true): String {
        while (i < source.length && source[i].isSpace()) i++
        if (source.getOrNull(i) == '{') { i++; return sequence(depth + 1, true) }
        if (required || i >= source.length || source[i] in "}^_") { valid = false; return "" }
        return atom(depth + 1)
    }

    private fun atom(depth: Int): String {
        if (depth > 32) { valid = false; return "" }
        val c = source[i++]
        if (c == '{') return sequence(depth + 1, true)
        if (c == '\\') {
            val start = i
            while (i < source.length && (source[i] in 'a'..'z' || source[i] in 'A'..'Z')) i++
            if (i == start && i < source.length) i++
            val command = source.substring(start, i)
            if (command in mathFormats) return argument(depth)
            if (command in listOf("frac", "dfrac", "tfrac")) {
                val a = argument(depth)
                val b = argument(depth)
                return "${mathGrouped(a)}/${mathGrouped(b)}"
            }
            if (command == "sqrt") return "√" + mathGrouped(argument(depth))
            mathSymbols[command]?.let { return it }
            valid = false
            return ""
        }
        if (c in "}&#\$") valid = false
        return c.toString()
    }

    private fun sequence(depth: Int, group: Boolean = false): String = buildString {
        while (valid && i < source.length && source[i] != '}') {
            if (source[i] == '^' || source[i] == '_') {
                val mark = source[i++]
                val value = argument(depth, false)
                val map = if (mark == '^') mathSuperscript else mathSubscript
                val chars = value.map { it.toString() }
                append(if (chars.isNotEmpty() && chars.all { it in map }) chars.joinToString("") { map.getValue(it) } else mark + mathGrouped(value))
            } else append(atom(depth))
        }
        if (group) { if (source.getOrNull(i) == '}') i++ else valid = false }
    }
}

internal data class MathRange(val start: Int, val end: Int, val source: String)
private data class MathToken(val at: Int, val value: String, val escaped: Boolean, val line: Int)

/** Closing delimiters and code runs are indexed once, even for unclosed input. */
internal fun mathRanges(text: String, singleLineCode: Boolean = false): List<MathRange> {
    val tokens = ArrayList<MathToken>()
    var slashes = 0
    var line = 0
    var i = 0
    while (i < text.length) {
        val pair = text.substring(i, minOf(i + 2, text.length))
        if (text[i] == '`') {
            var end = i + 1
            while (text.getOrNull(end) == '`') end++
            tokens += MathToken(i, text.substring(i, end), slashes % 2 == 1, line)
            i = end - 1
        } else if (pair in listOf("\$\$", "\\[", "\\]", "\\(", "\\)")) {
            tokens += MathToken(i, pair, slashes % 2 == 1, line)
            i++
        } else if (text[i] == '$' && text.getOrNull(i - 1) != '$') {
            tokens += MathToken(i, "\$", slashes % 2 == 1, line)
        }
        if (text[i] == '\n') line++
        slashes = if (text[i] == '\\') slashes + 1 else 0
        i++
    }
    val next = HashMap<String, Int>()
    val close = HashMap<Int, Int>()
    for (t in tokens.asReversed()) {
        val key = when (t.value) { "\\[" -> "\\]"; "\\(" -> "\\)"; else -> t.value }
        val lineBound = t.value == "\$" || (singleLineCode && t.value[0] == '`')
        next[if (lineBound) "${t.line}:$key" else key]?.let { close[t.at] = it }
        if (!t.escaped || t.value[0] == '`') next[if (lineBound) "${t.line}:${t.value}" else t.value] = t.at
    }
    val out = ArrayList<MathRange>()
    var until = 0
    for (t in tokens) {
        if (t.at < until || t.escaped) continue
        val end = close[t.at] ?: continue
        if (t.value[0] == '`') until = end + t.value.length
        else if (t.value == "\$") {
            // Convert only adjacent single-dollar gaps, even after rejection.
            if (t.at + 1 == end || text[t.at + 1].isSpace() || text[end - 1].isSpace() || text.getOrNull(end + 1) in '0'..'9') continue
            val source = text.substring(t.at + 1, end)
            if (source.none { it == '\\' || it == '^' || it == '_' } || simplifyMath(source) == null) continue
            out += MathRange(t.at, end + 1, source)
            until = end + 1
        }
        else if (t.value in listOf("\$\$", "\\[", "\\(")) {
            out += MathRange(t.at, end + 2, text.substring(t.at + 2, end))
            until = end + 2
        }
    }
    return out
}

internal fun maskMath(text: String, ranges: List<MathRange>): String {
    if (ranges.isEmpty()) return text
    val chars = text.toCharArray()
    for (r in ranges) {
        val width = if (text[r.start] == '$' && text.getOrNull(r.start + 1) != '$') 1 else 2
        for (i in r.start + width until r.end) if (chars[i] != '\n') chars[i] = 'a'
    }
    return String(chars)
}

internal fun mathParagraphs(lines: List<String>): Map<Int, Int> {
    val text = lines.joinToString("\n")
    val starts = IntArray(text.length + 1)
    val ends = IntArray(text.length + 1)
    val numbers = IntArray(text.length + 1)
    var offset = 0
    for ((i, line) in lines.withIndex()) {
        val end = offset + line.length
        val first = offset + line.length - line.trimStart { it.isSpace() }.length
        val last = offset + line.trimEnd { it.isSpace() }.length
        for (j in offset..end) { starts[j] = first; ends[j] = last; numbers[j] = i }
        offset = end + 1
    }
    val out = HashMap<Int, Int>()
    var fenceMark: Char? = null
    var fenceCount = 0
    val outsideCode = lines.map { line ->
        val trimmed = line.trim { it.isSpace() }
        val mark = fenceMark
        if (mark != null) {
            if (trimmed.length >= fenceCount && trimmed.all { it == mark }) fenceMark = null
            " ".repeat(line.length)
        } else {
            val first = trimmed.firstOrNull()
            val count = if (first == '`' || first == '~') trimmed.takeWhile { it == first }.length else 0
            if (count < 3) line else {
                fenceMark = first
                fenceCount = count
                " ".repeat(line.length)
            }
        }
    }.joinToString("\n")
    for (r in mathRanges(outsideCode, singleLineCode = true)) {
        if (!(text.startsWith("\$\$", r.start) || text.startsWith("\\[", r.start)) || r.start != starts[r.start] || r.end != ends[r.end - 1]) continue
        out[numbers[r.start]] = numbers[r.end - 1]
    }
    return out
}

/** Keep paragraph line boundaries except those inside a formula. */
internal fun mathLines(text: String): List<String> {
    val ranges = mathRanges(text, singleLineCode = true).associateBy { it.start }
    val lines = ArrayList<String>()
    var start = 0
    var i = 0
    while (i < text.length) {
        val formula = ranges[i]
        if (formula != null) i = formula.end
        else {
            if (text[i] == '\n') { lines += text.substring(start, i); start = i + 1 }
            i++
        }
    }
    lines += text.substring(start)
    return lines
}
