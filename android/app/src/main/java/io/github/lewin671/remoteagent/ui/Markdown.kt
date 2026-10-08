package io.github.lewin671.remoteagent.ui

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.wrapContentWidth
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.selection.DisableSelection
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.layout
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.withAnnotation
import androidx.compose.ui.text.withLink
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.constrainHeight
import androidx.compose.ui.unit.constrainWidth
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.delay
import java.util.Locale
import java.util.regex.Pattern

// Markdown as the CLIs write it, for an agent's reply. Text is only ever
// rendered as text, and only http(s) links can be opened; other targets,
// mostly local file paths, show as monospace refs.
//
// The web client parses the same way (web/src/markdown.tsx): both run the
// cases in testdata/markdown, and how each element looks is
// docs/design-system.md §7. The first half of this file is the parser, a
// port of the web one kept in its order; the second half draws the result.
//
// CLI output can hold file content written by someone else, so every pass
// here is linear in the text: no pattern that can backtrack over a long run,
// and none whose matcher recurses once per repetition.

/** Inline Markdown as a tree, so that both clients can be held to the same reading of it. */
sealed interface Span {
    data class Text(val v: String) : Span
    data class Code(val v: String) : Span
    data class Bold(val c: List<Span>) : Span
    data class Italic(val c: List<Span>) : Span
    data class Strike(val c: List<Span>) : Span
    data class Link(val v: String, val href: String) : Span

    /** A link whose target cannot be opened: shown, not clickable. */
    data class Ref(val v: String, val target: String) : Span
}

enum class Task { DONE, TODO }

enum class TableAlign { LEFT, CENTER, RIGHT }

/** One item of a list: its own text, then whatever is nested under it. */
data class Item(val text: String, val task: Task?, val children: List<Block>)

sealed interface Block {
    data class Para(val text: String) : Block
    data class Code(val lang: String, val text: String) : Block
    data class Heading(val level: Int, val text: String) : Block
    data object Rule : Block
    data class Quote(val blocks: List<Block>) : Block
    data class ItemList(val ordered: Boolean, val start: Int, val items: List<Item>) : Block

    /** [align] has a column's alignment where the separator row gave one. */
    data class Table(val head: List<String>, val align: List<TableAlign?>, val rows: List<List<String>>) : Block
}

// White space as JavaScript's \s and trim() have it, which is what the web
// parser reads by: it includes the full-width space of Chinese text.
private const val WS = "\\t\\n\\u000B\\f\\r \\u00A0\\u1680\\u2000-\\u200A\\u2028\\u2029\\u202F\\u205F\\u3000\\uFEFF"

private fun Char.isSpace(): Boolean =
    this == ' ' || this in '\t'..'\r' || this == ' ' || this == ' ' || this in ' '..' ' ||
        this == ' ' || this == ' ' || this == ' ' || this == ' ' || this == '　' || this == '﻿'

private fun String.trimSpace(): String = trim { it.isSpace() }
private fun String.isBlankLine(): Boolean = all { it.isSpace() }
private fun indentOf(line: String): Int = line.indexOfFirst { !it.isSpace() }.let { if (it < 0) line.length else it }

/** What JavaScript's `.` stops at, besides the end of a line. */
private fun Char.isLineEnd(): Boolean = this == '\n' || this == '\r' || this == ' ' || this == ' '

/** Whether a link target may be opened: web pages only, so javascript:, data: and file paths never become clickable. */
fun safeHref(url: String): String? {
    val u = url.trimSpace()
    val rest = when {
        u.startsWith("https://", ignoreCase = true) -> 8
        u.startsWith("http://", ignoreCase = true) -> 7
        else -> return null
    }
    return if (u.length > rest && u.none { it.isSpace() }) u else null
}

private fun escapedAt(text: String, index: Int): Boolean {
    var at = index - 1
    var slashes = 0
    while (at >= 0 && text[at--] == '\\') slashes++
    return slashes % 2 == 1
}

private class LinkEnd(val close: Int, val end: Int, val auto: Boolean)

/** Index link delimiters once. A failed '[' or autolink opener must not rescan the rest of the text on every character. */
private fun inlineLinks(text: String): Map<Int, LinkEnd> {
    val links = HashMap<Int, LinkEnd>()
    var close = -1
    var end = -1
    var validTarget = false
    var paren = -1
    var angle = -1
    var space = text.length
    var newline = text.length
    for (i in text.indices.reversed()) {
        val c = text[i]
        if (c == '[') {
            if (close > i + 1 && close < newline && validTarget) links[i] = LinkEnd(close, end, false)
        } else if (c == '<' && angle < space) {
            val prefix = if (text.startsWith("https://", i + 1)) 8 else if (text.startsWith("http://", i + 1)) 7 else 0
            if (prefix > 0 && angle > i + 1 + prefix) links[i] = LinkEnd(angle, angle, true)
        }
        if (c == ']') {
            close = i
            end = paren
            validTarget = text.getOrNull(i + 1) == '(' && paren > i + 2 && paren < space
        }
        if (c == ')') paren = i
        if (c == '>') angle = i
        if (c.isSpace()) space = i
        if (c == '\n') newline = i
    }
    return links
}

/** A bare URL without the sentence's punctuation after it; a closing bracket stays only while the URL has an opening one for it. Empty when nothing but the scheme is left. */
private fun bareUrl(raw: String): String {
    // How many closing brackets have no opening one: those end the sentence.
    var paren = 0
    var bracket = 0
    for (c in raw) when (c) {
        '(' -> paren--
        ')' -> paren++
        '[' -> bracket--
        ']' -> bracket++
    }
    var end = raw.length
    while (end > 0) {
        val c = raw[end - 1]
        if (c == ')' && paren > 0) paren--
        else if (c == ']' && bracket > 0) bracket--
        else if (c !in ".,;:!?'\"*_~") break
        end--
    }
    val url = raw.substring(0, end)
    return if (url.equals("https://", ignoreCase = true) || url.equals("http://", ignoreCase = true)) "" else url
}

// Groups: 1 a run of backticks, 2 bold, 3 *italic*, 4 _italic_, 5 struck,
// 6 '[', 7 an autolink's opener, 8 a bare URL. Word characters and digits
// are spelled out: Android's Pattern reads \\w and \\d as any script's, and
// the web client's are ASCII.
private val inlineRe: Pattern = Pattern.compile(
    "(`+)|\\*\\*([^*\\n]+)\\*\\*|(?<![A-Za-z0-9_*])\\*([^*$WS][^*\\n]*?)\\*(?![A-Za-z0-9_*])" +
        "|(?<![\\p{L}\\p{N}_])_([^_$WS][^_\\n]*?)_(?![\\p{L}\\p{N}_])|~~([^~\\n]+)~~|(\\[)|(<https?://)" +
        "|(?<![A-Za-z0-9_/])(https?://[^$WS<>\\u2010-\\u2027\\u3000-\\u303f\\uff00-\\uffef]+)",
)
private val ticksRe = Regex("`+")

/** Inline Markdown: `code`, **bold**, *italic*, _italic_, ~~struck~~, [links](target), <https://autolinks> and bare https:// URLs. */
fun inlineSpans(text: String): List<Span> {
    val out = ArrayList<Span>()
    val plain = StringBuilder()
    fun add(span: Span) {
        if (plain.isNotEmpty()) {
            out += Span.Text(plain.toString())
            plain.setLength(0)
        }
        out += span
    }
    val links = inlineLinks(text)
    // The next run of as many backticks closes a run, found without rescanning.
    val closers = HashMap<Int, Int>()
    val next = HashMap<Int, Int>()
    for (run in ticksRe.findAll(text).toList().asReversed()) {
        next[run.value.length]?.let { closers[run.range.first] = it }
        next[run.value.length] = run.range.first
    }
    val m = inlineRe.matcher(text)
    var last = 0
    var from = 0
    while (from <= text.length && m.find(from)) {
        val at = m.start()
        from = m.end()
        if (at < last) continue
        val ticks: String? = m.group(1)
        val bold: String? = m.group(2)
        val star: String? = m.group(3)
        val under: String? = m.group(4)
        val struck: String? = m.group(5)
        val bare: String? = m.group(8)
        val escapedTick = ticks != null && escapedAt(text, at)
        if (at > last) plain.append(text, last, if (escapedTick) at - 1 else at)
        // Where the text goes on when a branch below took more or less than the match.
        var resume = -1
        when {
            ticks != null -> {
                val end = if (escapedTick) null else closers[at]
                if (end != null) {
                    add(Span.Code(text.substring(at + ticks.length, end)))
                    resume = end + ticks.length
                } else plain.append(ticks)
            }
            // Emphasis may wrap code and links, so its content is inline Markdown too.
            bold != null -> add(Span.Bold(inlineSpans(bold)))
            star != null -> add(Span.Italic(inlineSpans(star)))
            under != null -> add(Span.Italic(inlineSpans(under)))
            struck != null -> add(Span.Strike(inlineSpans(struck)))
            bare != null -> {
                val url = bareUrl(bare)
                if (url.isNotEmpty()) {
                    add(Span.Link(url, url))
                    resume = at + url.length
                } else plain.append(m.group())
            }
            else -> {
                val link = links[at]
                if (link != null) {
                    val label = text.substring(at + 1, link.close)
                    val target = if (link.auto) label else text.substring(link.close + 2, link.end)
                    val href = safeHref(target)
                    add(if (href != null) Span.Link(label, href) else Span.Ref(label, target))
                    resume = link.end + 1
                } else plain.append(m.group())
            }
        }
        if (resume >= 0) {
            last = resume
            from = resume
        } else last = m.end()
    }
    if (last < text.length) plain.append(text, last, text.length)
    if (plain.isNotEmpty()) out += Span.Text(plain.toString())
    return out
}

private val blankCell = setOf("", "-", "–", "—", "N/A", "n/a")
private fun Char.isAsciiDigit(): Boolean = this in '0'..'9'
private fun Char.isUnitLetter(): Boolean = this in 'a'..'z' || this in 'A'..'Z' || this == 'µ'

/**
 * A whole cell that reads as a quantity: digits with a sign, a currency
 * mark or a short unit, or a few of them ("+84 −61", "5.2 s", "118 ms",
 * "1 / 3"). Scanned once from the left: a pattern for it backtracks.
 */
private fun quantity(v: String): Boolean {
    val n = v.length
    var i = 0
    while (i < n && v[i] in "+-−~≈<>≤≥\$¥€£") i++
    while (true) {
        if (i >= n || !v[i].isAsciiDigit()) return false
        i++
        while (i < n && (v[i].isAsciiDigit() || v[i] in ",.:")) i++
        // A unit: one optional space, then a mark or up to five letters.
        val j = if (i < n && v[i].isSpace()) i + 1 else i
        if (j < n && v[j] in "%‰°") i = j + 1
        else {
            var k = j
            while (k < n && v[k].isUnitLetter()) k++
            if (k > j && k - j <= 5) i = k
        }
        if (i == n) return true
        if (!v[i].isSpace()) return false
        while (i < n && v[i].isSpace()) i++
        if (i < n && v[i] in "+-−/") {
            i++
            if (i < n && v[i].isSpace()) i++
        }
    }
}

/** Whether a column holds quantities, and so reads better right-aligned: every cell that says something is a quantity, and at least one does. */
/** Whether a diff's line names a file ("+++ b/x", "--- a/x"), not an added "++n;" or a removed "--n;". */
private fun fileHeader(l: String, sign: Char): Boolean =
    l.length >= 3 && l[0] == sign && l[1] == sign && l[2] == sign && (l.length == 3 || l[3].isSpace())

/** What a cell says once its marks are gone: `**42 ms**` reads "42 ms". */
private fun shownText(spans: List<Span>, out: StringBuilder) {
    for (s in spans) when (s) {
        is Span.Text -> out.append(s.v)
        is Span.Code -> out.append(s.v)
        is Span.Link -> out.append(s.v)
        is Span.Ref -> out.append(s.v)
        is Span.Bold -> shownText(s.c, out)
        is Span.Italic -> shownText(s.c, out)
        is Span.Strike -> shownText(s.c, out)
    }
}

fun numericColumn(cells: List<String>): Boolean {
    var n = 0
    for (c in cells) {
        // Cells are Markdown; what counts is the text they show.
        val v = buildString { shownText(inlineSpans(c), this) }.trimSpace()
        if (v in blankCell) continue
        if (!quantity(v)) return false
        n++
    }
    return n > 0
}

private val boldLine = Regex("\\*\\*[^*\\n]+\\*\\*[:：]?")

/** A paragraph that is one bold line, the way the CLIs often write a small heading: it gets a heading's space, not a paragraph's. */
fun boldHeading(text: String): Boolean = boldLine.matches(text.trimSpace())

/** Code longer than this is cut to [FOLD_SHOWN] lines until it is opened. */
const val FOLD_OVER = 16
const val FOLD_SHOWN = 12

// Any indent: the CLIs nest code blocks in list items.
private val fenceRe: Pattern = Pattern.compile("[$WS]*(`{3,}|~{3,})[$WS]*([^$WS`]*)")
private val listRe: Pattern = Pattern.compile("([$WS]*)([-*+]|[0-9]{1,9}[.)])[$WS]+([^\\n\\r\\u2028\\u2029]*)")
private val taskRe = Regex("\\[([ xX])\\][$WS]+(.*)", RegexOption.DOT_MATCHES_ALL)
private val sepCell = Regex(":?-{3,}:?")

/** A line that ends a fence opened by [n] of [mark]: the same character, at least as many. */
private fun closesFence(line: String, mark: Char, n: Int): Boolean {
    val t = line.trimSpace()
    return t.length >= n && t.all { it == mark }
}

/** The row under a table's head: cells of dashes, with optional colons and outer pipes. */
private fun tableSep(line: String): Boolean {
    var s = line.trimSpace()
    if (s.startsWith("|")) s = s.substring(1)
    if (s.endsWith("|")) s = s.substring(0, s.length - 1)
    return s.split('|').all { sepCell.matches(it.trimSpace()) }
}

/** `# text`, with up to three spaces before it and any closing hashes dropped; null when the line is not a heading. */
private fun headingOf(line: String): Block.Heading? {
    var i = 0
    while (i < 3 && i < line.length && line[i].isSpace()) i++
    val hashes = i
    while (i < line.length && line[i] == '#') i++
    val level = i - hashes
    if (level !in 1..6 || i >= line.length || !line[i].isSpace()) return null
    var end = line.length
    while (end > i && line[end - 1].isSpace()) end--
    while (end > i && line[end - 1] == '#') end--
    while (end > i && line[end - 1].isSpace()) end--
    while (i < end && line[i].isSpace()) i++
    val text = line.substring(i, end)
    return if (text.any { it.isLineEnd() }) null else Block.Heading(level, text)
}

/** Three or more of the same of - * _ and nothing else, up to three spaces in. */
private fun isRule(line: String): Boolean {
    val at = indentOf(line)
    if (at > 3 || at >= line.length) return false
    val mark = line[at]
    if (mark != '-' && mark != '*' && mark != '_') return false
    var n = 0
    for (k in at until line.length) {
        if (line[k] == mark) n++ else if (!line[k].isSpace()) return false
    }
    return n >= 3
}

/** What a quoted line says after its `>`; null when the line is not quoted. */
private fun quoteOf(line: String): String? {
    var i = 0
    while (i < 3 && i < line.length && line[i].isSpace()) i++
    if (i >= line.length || line[i] != '>') return null
    i++
    if (i < line.length && line[i].isSpace()) i++
    val rest = line.substring(i)
    return if (rest.any { it.isLineEnd() }) null else rest
}

/** A list line's marker and text; null when the line does not start an item. */
private fun listLine(line: String): Pair<String, String>? {
    val m = listRe.matcher(line)
    return if (m.matches()) m.group(2).orEmpty() to m.group(3).orEmpty() else null
}

private fun isList(line: String): Boolean = listRe.matcher(line).matches()
private fun isOrdered(line: String): Boolean = listLine(line)?.first?.get(0)?.isDigit() == true

/** Splits a table row into cells, at pipes outside `code` and not escaped. */
fun tableCells(line: String): List<String> {
    var s = line.trimSpace().removePrefix("|")
    // A pipe after an odd run of backslashes is escaped.
    if (s.endsWith("|") && !escapedAt(s, s.lastIndex)) s = s.dropLast(1)
    val cells = mutableListOf<String>()
    val cell = StringBuilder()
    var code = 0
    val ticks = ticksRe.findAll(s).groupingBy { it.value.length }.eachCount().toMutableMap()
    var i = 0
    while (i < s.length) {
        when {
            s[i] == '\\' && i + 1 < s.length && (s[i + 1] == '|' || s[i + 1] == '\\') -> {
                if (s[i + 1] == '|') cell.append('|') else cell.append("\\\\")
                i += 2
            }
            s[i] == '`' -> {
                var end = i + 1
                while (end < s.length && s[end] == '`') end++
                val n = end - i
                ticks[n] = (ticks[n] ?: 1) - 1
                if (code == 0 && !escapedAt(s, i) && (ticks[n] ?: 0) > 0) code = n else if (code == n) code = 0
                cell.append(s, i, end)
                i = end
            }
            s[i] == '|' && code == 0 -> {
                cells += cell.toString().trimSpace()
                cell.setLength(0)
                i++
            }
            else -> cell.append(s[i++])
        }
    }
    cells += cell.toString().trimSpace()
    return cells
}

/** Quotes and lists nest no deeper than this: past it the rest is one paragraph, so neither the parser nor the layout can be made to overflow. */
private const val MAX_DEPTH = 32

/** Splits Markdown into blocks: fenced code, tables, headings, rules, quotes, lists and paragraphs. */
fun parseMarkdown(md: String, depth: Int = 0): List<Block> {
    if (depth >= MAX_DEPTH) return listOf(Block.Para(md))
    val lines = md.replace("\r\n", "\n").replace('\r', '\n').split("\n")
    val nextNonBlank = IntArray(lines.size)
    var nextLine = lines.size
    for (i in lines.indices.reversed()) {
        nextNonBlank[i] = nextLine
        if (!lines[i].isBlankLine()) nextLine = i
    }
    val blocks = mutableListOf<Block>()
    val text = mutableListOf<String>()
    fun flush() {
        if (text.isNotEmpty()) blocks += Block.Para(text.joinToString("\n"))
        text.clear()
    }
    var i = 0
    while (i < lines.size) {
        val line = lines[i]
        if (line.isBlankLine()) {
            flush()
            i++
            continue
        }
        val f = fenceRe.matcher(line)
        if (f.lookingAt()) {
            val body = mutableListOf<String>()
            // Closed by a fence of the same character, at least as long.
            val opener = f.group(1).orEmpty()
            val mark = opener[0]
            val n = opener.length
            // The fence's own indent is the list's, not the code's.
            val indent = indentOf(line)
            var j = i + 1
            while (j < lines.size && !closesFence(lines[j], mark, n)) {
                body += lines[j].substring(minOf(indent, indentOf(lines[j])))
                j++
            }
            flush()
            blocks += Block.Code(f.group(2).orEmpty().lowercase(Locale.ROOT), body.joinToString("\n"))
            i = j + 1
            continue
        }
        if ('|' in line && i + 1 < lines.size && tableSep(lines[i + 1]) && tableCells(line).size == tableCells(lines[i + 1]).size) {
            val head = tableCells(line)
            val align = tableCells(lines[i + 1]).map {
                when {
                    it.startsWith(":") && it.endsWith(":") -> TableAlign.CENTER
                    it.endsWith(":") -> TableAlign.RIGHT
                    it.startsWith(":") -> TableAlign.LEFT
                    else -> null
                }
            }
            val rows = mutableListOf<List<String>>()
            var j = i + 2
            while (j < lines.size && '|' in lines[j] && !lines[j].isBlankLine()) rows += tableCells(lines[j++])
            flush()
            blocks += Block.Table(head, align, rows)
            i = j
            continue
        }
        val h = headingOf(line)
        if (h != null) {
            flush()
            blocks += h
            i++
            continue
        }
        if (isRule(line)) {
            flush()
            blocks += Block.Rule
            i++
            continue
        }
        if (quoteOf(line) != null) {
            flush()
            val body = mutableListOf<String>()
            var j = i
            while (j < lines.size) {
                body += quoteOf(lines[j]) ?: break
                j++
            }
            blocks += Block.Quote(parseMarkdown(body.joinToString("\n"), depth + 1))
            i = j
            continue
        }
        if (isList(line)) {
            flush()
            val base = indentOf(line)
            val kind = isOrdered(line)
            val body = mutableListOf<String>()
            var j = i
            // The list runs over its items, lines indented under them, and blank
            // lines between; it ends at a line back at the left edge that is not
            // an item.
            while (j < lines.size) {
                val l = lines[j]
                if (l.isBlankLine()) {
                    val next = lines.getOrNull(nextNonBlank[j])
                    if (next == null || (indentOf(next) <= base && !isList(next))) break
                    body += l
                    j++
                    continue
                }
                // An unindented line right after text is a lazy continuation of the item.
                val lazy = body.isNotEmpty() && !body.last().isBlankLine()
                if (indentOf(l) < base || (indentOf(l) == base && !isList(l) && body.isNotEmpty() && !lazy)) break
                // Bullets after numbers, or the other way round, start a new list.
                if (j > i && indentOf(l) <= base + 1 && isList(l) && isOrdered(l) != kind) break
                body += l
                j++
            }
            blocks += parseList(body, base, depth)
            i = j
            continue
        }
        text += line
        i++
    }
    flush()
    return blocks
}

private fun parseList(lines: List<String>, base: Int, depth: Int): Block {
    class Raw(val text: MutableList<String>, val rest: MutableList<String> = mutableListOf())
    val first = listLine(lines[0])!!.first
    val ordered = first[0].isDigit()
    val items = mutableListOf<Raw>()
    for (l in lines) {
        val m = listLine(l)
        if (m != null && indentOf(l) <= base + 1) {
            items += Raw(mutableListOf(m.second))
            continue
        }
        val cur = items.last()
        // Text right under an item, before anything nested, continues it.
        if (cur.rest.isEmpty() && !l.isBlankLine() && m == null && !fenceRe.matcher(l).lookingAt()) cur.text += l.trimSpace()
        else cur.rest += l
    }
    fun strip(rest: List<String>): String {
        val ind = rest.filter { !it.isBlankLine() }.minOf { indentOf(it) }
        return rest.joinToString("\n") { it.substring(minOf(ind, indentOf(it))) }
    }
    return Block.ItemList(
        ordered,
        if (ordered) first.takeWhile { it.isDigit() }.toInt() else 1,
        items.map {
            val text = it.text.joinToString("\n")
            val task = taskRe.matchEntire(text)
            val children = if (it.rest.any { x -> !x.isBlankLine() }) parseMarkdown(strip(it.rest), depth + 1) else emptyList()
            if (task != null) Item(task.groupValues[2], if (task.groupValues[1] == " ") Task.TODO else Task.DONE, children)
            else Item(text, null, children)
        },
    )
}

// ---- Drawing: docs/design-system.md §7 ----

private const val CODE = "code"

/** A paragraph's inline tree as styled text; [codeSize] is 15 in the two large headings. */
fun richText(spans: List<Span>, p: Palette, codeSize: TextUnit = 13.sp): AnnotatedString = buildAnnotatedString { appendSpans(spans, p, codeSize) }

private fun AnnotatedString.Builder.appendSpans(spans: List<Span>, p: Palette, codeSize: TextUnit) {
    for (s in spans) when (s) {
        is Span.Text -> append(s.v)
        // The chip behind it is drawn by RichText, which finds it by this annotation.
        is Span.Code -> withAnnotation(CODE, "") { withStyle(SpanStyle(fontFamily = MonoFamily, fontSize = codeSize)) { append(s.v) } }
        is Span.Bold -> withStyle(SpanStyle(fontWeight = platformWeight(FontWeight.SemiBold))) { appendSpans(s.c, p, codeSize) }
        is Span.Italic -> withStyle(SpanStyle(fontStyle = FontStyle.Italic)) { appendSpans(s.c, p, codeSize) }
        is Span.Strike -> withStyle(SpanStyle(color = p.muted, textDecoration = TextDecoration.LineThrough)) { appendSpans(s.c, p, codeSize) }
        is Span.Link -> withLink(LinkAnnotation.Url(s.href, TextLinkStyles(SpanStyle(color = p.blue)))) { append(s.v) }
        is Span.Ref -> withStyle(SpanStyle(fontFamily = MonoFamily, fontSize = 13.sp, color = p.fg2)) { append(s.v) }
    }
}

/** A paragraph as styled text, keeping the single line breaks the CLIs use. */
private fun richLines(text: String, p: Palette, codeSize: TextUnit = 13.sp): AnnotatedString = buildAnnotatedString {
    text.split("\n").forEachIndexed { i, line ->
        if (i > 0) append('\n')
        appendSpans(inlineSpans(line), p, codeSize)
    }
}

/** Where a run of blocks sits, which decides the space between them. */
internal enum class Place { REPLY, QUOTE, ITEM }

private fun titled(b: Block): Boolean = b is Block.Heading || (b is Block.Para && boldHeading(b.text))

/**
 * The blocks to draw, each with the space in dp above it: the first row of
 * §7's table that fits. A rule is not drawn, it only widens the space where
 * it stood. Inside a quote and a list item headings get no space of their
 * own; [afterText] says an item has text above its first child.
 */
internal fun spaced(blocks: List<Block>, place: Place, afterText: Boolean = false): List<Pair<Int, Block>> {
    val out = mutableListOf<Pair<Int, Block>>()
    var prev: Block? = null
    var ruled = false
    for (b in blocks) {
        if (b is Block.Rule) {
            ruled = true
            continue
        }
        val gap = when (place) {
            Place.ITEM -> if (prev == null && !afterText) 0 else if (b is Block.ItemList) 4 else 8
            Place.QUOTE -> if (prev == null) 0 else 12
            Place.REPLY -> when {
                prev == null -> 0
                ruled -> 24
                prev is Block.Heading && titled(b) -> 12
                b is Block.Heading && b.level <= 2 -> 24
                titled(b) -> 16
                titled(prev) -> 8
                prev is Block.Table -> 16
                prev is Block.Para && b is Block.ItemList -> 8
                else -> 12
            }
        }
        out += gap to b
        prev = b
        ruled = false
    }
    return out
}

/**
 * An agent's reply. [bleed] is the padding the caller keeps at the reply's
 * sides: a table too wide for the reply scrolls across it, to the edge of
 * the screen.
 */
@Composable
fun Markdown(text: String, modifier: Modifier = Modifier, bleed: Dp = 0.dp) {
    val blocks = remember(text) { parseMarkdown(text) }
    SelectionContainer(modifier) { Blocks(blocks, pal.fg, Place.REPLY, bleed = bleed) }
}

/** [color] is the text's: `fg` in a reply, `fg2` in a quote, `muted` under a finished task. [level] counts the lists around. */
@Composable
private fun Blocks(blocks: List<Block>, color: Color, place: Place, level: Int = 0, bleed: Dp = 0.dp, afterText: Boolean = false) {
    val p = pal
    val rows = remember(blocks, place, afterText) { spaced(blocks, place, afterText) }
    Column {
        for ((gap, b) in rows) {
            if (gap > 0) Spacer(Modifier.height(gap.dp))
            when (b) {
                is Block.Para -> RichText(remember(b, p) { richLines(b.text, p) }, T.chat.copy(color = color))
                is Block.Heading -> {
                    val style = when {
                        b.level <= 2 -> T.h17.copy(color = color)
                        b.level == 3 -> ts(15.sp, FontWeight.SemiBold, 1.35f).copy(color = color)
                        else -> ts(13.sp, FontWeight.SemiBold, 1.35f).copy(color = p.muted)
                    }
                    val codeSize = if (b.level <= 2) 15.sp else 13.sp
                    RichText(remember(b, p) { richText(inlineSpans(b.text), p, codeSize) }, style, codeSize = codeSize)
                }
                is Block.Code -> ReplyCode(b)
                is Block.Quote -> {
                    val bar = p.track
                    // The bar is drawn, not laid out, so it is as tall as what it quotes.
                    Box(Modifier.fillMaxWidth().drawBehind { drawRect(bar, size = Size(2.dp.toPx(), size.height)) }.padding(start = 14.dp)) {
                        Blocks(b.blocks, p.fg2, Place.QUOTE, level)
                    }
                }
                is Block.ItemList -> Items(b, color, level)
                is Block.Table -> TableBlock(b, color, if (place == Place.REPLY) bleed else 0.dp)
                Block.Rule -> {}
            }
        }
    }
}

@Composable
private fun Items(list: Block.ItemList, color: Color, level: Int) {
    val p = pal
    val body = T.chat
    // Two-digit numbers need a wider gutter, or they run into the text.
    val gutter = if (list.ordered && list.start + list.items.size - 1 >= 10) 28.dp else 20.dp
    val line = with(LocalDensity.current) { body.lineHeight.toDp() }
    Column {
        list.items.forEachIndexed { n, item ->
            if (n > 0) Spacer(Modifier.height(4.dp))
            // A finished task is muted with everything under it.
            val ink = if (item.task == Task.DONE) p.muted else color
            Row {
                DisableSelection {
                    Box(Modifier.width(gutter).height(line), contentAlignment = if (item.task != null) Alignment.CenterStart else Alignment.Center) {
                        when {
                            item.task != null -> Ic(if (item.task == Task.DONE) Lucide.BoxChecked else Lucide.Box, iconSize(body), p.muted)
                            list.ordered -> Text(
                                "${list.start + n}.", style = body.copy(fontFeatureSettings = "tnum"), color = p.muted, softWrap = false, maxLines = 1,
                                // At a large font the number grows to the left, away from the text.
                                modifier = Modifier.fillMaxWidth().padding(end = 6.dp).wrapContentWidth(Alignment.End, unbounded = true),
                            )
                            // CJK fonts draw • as a small centered dot, so the disc is drawn; from the second level it is a ring.
                            level == 0 -> Box(Modifier.size(5.dp).background(p.muted, CircleShape))
                            else -> Box(Modifier.size(5.dp).border(1.dp, p.muted, CircleShape))
                        }
                    }
                }
                Column(Modifier.weight(1f)) {
                    if (item.text.isNotEmpty()) RichText(remember(item.text, p) { richLines(item.text, p) }, body.copy(color = ink))
                    Blocks(item.children, ink, Place.ITEM, level + 1, afterText = item.text.isNotEmpty())
                }
            }
        }
    }
}

/**
 * Text whose inline code sits on a rounded chip (§7: `chip`, radius 4, 4 at
 * the sides): a span background would fill the whole line height with
 * square corners. The chip is drawn behind the text and takes no room of
 * its own, so its sides reach into the space around the code.
 */
@Composable
fun RichText(text: AnnotatedString, style: TextStyle, modifier: Modifier = Modifier, codeSize: TextUnit = 13.sp) {
    val bg = pal.chip
    val codes = remember(text) { text.getStringAnnotations(CODE, 0, text.length) }
    var layout by remember { mutableStateOf<TextLayoutResult?>(null) }
    Text(
        text, style = style, onTextLayout = { layout = it },
        modifier = if (codes.isEmpty()) modifier else modifier.drawBehind {
            val l = layout ?: return@drawBehind
            val padX = 4.dp.toPx()
            val half = codeSize.toPx() * 0.72f
            for (a in codes) {
                if (a.end <= a.start || a.end > l.layoutInput.text.length) continue
                for (line in l.getLineForOffset(a.start)..l.getLineForOffset(a.end - 1)) {
                    val s = maxOf(a.start, l.getLineStart(line))
                    val e = minOf(a.end, l.getLineEnd(line, visibleEnd = true))
                    if (e <= s) continue
                    val x0 = l.getBoundingBox(s).left
                    val x1 = l.getBoundingBox(e - 1).right
                    val mid = (l.getLineTop(line) + l.getLineBottom(line)) / 2
                    drawRoundRect(bg, Offset(x0 - padX, mid - half), Size(x1 - x0 + padX * 2, half * 2), CornerRadius(4.dp.toPx()))
                }
            }
        },
    )
}

/** Lines of code as one text; in a diff the added and removed lines are colored, the file headers are not. */
internal fun codeText(lines: List<String>, diff: Boolean, added: Color, removed: Color): AnnotatedString = buildAnnotatedString {
    lines.forEachIndexed { i, l ->
        if (i > 0) append('\n')
        val color = when {
            !diff -> null
            l.startsWith("+") && !fileHeader(l, '+') -> added
            l.startsWith("-") && !fileHeader(l, '-') -> removed
            else -> null
        }
        if (color != null) withStyle(SpanStyle(color = color)) { append(l) } else append(l)
    }
}

/**
 * A code block in a reply: soft, no line around it, radius 6, mono 12, never
 * wrapped. A long one shows its first lines until it is opened in place.
 */
@Composable
private fun ReplyCode(b: Block.Code) {
    val p = pal
    val lines = remember(b.text) { b.text.split("\n") }
    val long = lines.size > FOLD_OVER
    var open by rememberSaveable { mutableStateOf(false) }
    // Only a diff is colored: added and removed lines mean something.
    val diff = b.lang == "diff" || b.lang == "patch"
    val shown = remember(lines, open, diff, p) { codeText(if (long && !open) lines.take(FOLD_SHOWN) else lines, diff, p.greenInk, p.red) }
    Box(Modifier.fillMaxWidth().clip(Radii.XS).background(p.soft)) {
        Column {
            Text(
                shown, style = T.mono, color = p.fg2, softWrap = false,
                // Room at the end for the copy button: scrolled all the way, no code is left under it.
                modifier = Modifier.horizontalScroll(rememberScrollState()).padding(start = 12.dp, end = 44.dp, top = 8.dp, bottom = 8.dp),
            )
            if (long) Row(
                Modifier.fillMaxWidth().heightIn(min = 44.dp).tap { open = !open }.padding(horizontal = 12.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                ArrowText(if (open) "收起" else "展开全文", p.blue, T.smB, down = !open, up = open)
                Spacer(Modifier.weight(1f))
                Text("共 ${lines.size} 行", style = T.meta, color = p.muted, maxLines = 1)
            }
        }
        // Over the corner without adding to the block's height: one line of code is lower than the button.
        Box(Modifier.matchParentSize(), contentAlignment = Alignment.TopEnd) { CopyButton(b.text) }
    }
}

private enum class Copied { NO, OK, FAILED }

private fun copyText(context: Context, text: String): Boolean = try {
    context.getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText("code", text))
    true
} catch (_: RuntimeException) {
    // Too large for the system's clipboard, or refused by it.
    false
}

/** Copies a code block; the icon says for a moment how it went. */
@Composable
private fun CopyButton(text: String) {
    val p = pal
    val context = LocalContext.current
    var state by remember { mutableStateOf(Copied.NO) }
    LaunchedEffect(state) {
        if (state != Copied.NO) {
            delay(1500)
            state = Copied.NO
        }
    }
    Box(Modifier.size(44.dp).tap { state = if (copyText(context, text)) Copied.OK else Copied.FAILED }, contentAlignment = Alignment.TopEnd) {
        // Code that runs under the icon fades out before it.
        Box(Modifier.fillMaxWidth().height(34.dp).background(Brush.horizontalGradient(0f to p.soft.copy(alpha = 0f), 0.4f to p.soft)))
        Ic(
            when (state) { Copied.OK -> Lucide.Check; Copied.FAILED -> Lucide.X; Copied.NO -> Lucide.Copy }, 16.dp, p.muted,
            Modifier.padding(top = 9.dp, end = 12.dp),
            contentDescription = when (state) { Copied.OK -> "已复制"; Copied.FAILED -> "复制失败"; Copied.NO -> "复制代码" },
        )
    }
}

/**
 * Fits a table that is a little too wide into the viewport by narrowing its
 * long columns, never below a readable width; one that still does not fit
 * keeps its widths and scrolls. Nothing is widened: a table is as wide as
 * what it holds.
 */
fun tableColumnWidths(preferred: List<Int>, viewport: Int, readable: Int): List<Int> {
    if (preferred.sum() <= viewport) return preferred
    val floors = preferred.map { minOf(it, readable) }
    val minimum = floors.sum()
    if (minimum > viewport) return preferred
    val capacity = preferred.sum() - minimum
    val extra = viewport - minimum
    val widths = preferred.indices.map { i -> floors[i] + ((preferred[i] - floors[i]).toLong() * extra / capacity).toInt() }.toMutableList()
    var remainder = viewport - widths.sum()
    for (i in widths.indices) if (remainder > 0 && widths[i] < preferred[i]) { widths[i]++; remainder-- }
    return widths
}

/** Lets the content run [by] past both sides, as a negative margin would. */
private fun Modifier.bleed(by: Dp): Modifier = if (by <= 0.dp) this else layout { measurable, constraints ->
    val x = by.roundToPx()
    val placed = measurable.measure(constraints.copy(minWidth = 0, maxWidth = if (constraints.hasBoundedWidth) constraints.maxWidth + 2 * x else constraints.maxWidth))
    layout((placed.width - 2 * x).coerceAtLeast(0), placed.height) { placed.place(-x, 0) }
}

/** Fades the last 32 dp out, whatever is behind: there is more to scroll to. */
private fun Modifier.fadeEnd(on: Boolean): Modifier = if (!on) this else graphicsLayer { compositingStrategy = CompositingStrategy.Offscreen }.drawWithContent {
    drawContent()
    val w = 32.dp.toPx()
    drawRect(
        Brush.horizontalGradient(listOf(Color.Black, Color.Transparent), startX = size.width - w, endX = size.width),
        topLeft = Offset(size.width - w, 0f), size = Size(w, size.height), blendMode = BlendMode.DstIn,
    )
}

/**
 * A table: lines between the rows and nothing else, as wide as what it
 * holds. One too wide for the reply scrolls sideways, across [bleed] to the
 * edge of the screen, its right edge fading while there is more.
 */
@Composable
private fun TableBlock(t: Block.Table, color: Color, bleed: Dp) {
    val p = pal
    val all = remember(t) { listOf(t.head) + t.rows }
    val columns = t.head.size
    // The separator row's alignment wins; without one, quantities go right.
    val numeric = remember(t) { List(columns) { k -> t.align.getOrNull(k) == null && numericColumn(t.rows.map { r -> r.getOrElse(k) { "" } }) } }
    var heights by remember(t) { mutableStateOf(emptyList<Int>()) }
    val scroll = rememberScrollState()
    val headStyle = T.meta.copy(color = p.muted)
    val cellStyle = ts(13.sp, lineHeight = 1.45f).copy(color = color)
    BoxWithConstraints(Modifier.fillMaxWidth()) {
        val viewport = constraints.maxWidth
        Layout(
            modifier = Modifier.bleed(bleed).fadeEnd(scroll.canScrollForward).horizontalScroll(scroll).padding(horizontal = bleed).drawBehind {
                var y = 0f
                for (h in heights.dropLast(1)) {
                    y += h
                    drawLine(p.line, Offset(0f, y - 0.5.dp.toPx()), Offset(size.width, y - 0.5.dp.toPx()), 1.dp.toPx())
                }
            },
            content = {
                for ((ri, row) in all.withIndex()) for (ci in 0 until columns) {
                    val cell = row.getOrElse(ci) { "" }
                    val align = when (t.align.getOrNull(ci)) {
                        TableAlign.CENTER -> TextAlign.Center
                        TableAlign.RIGHT -> TextAlign.Right
                        TableAlign.LEFT -> TextAlign.Left
                        null -> if (numeric[ci]) TextAlign.Right else TextAlign.Left
                    }
                    RichText(
                        remember(cell, p) { richText(inlineSpans(cell), p) },
                        (if (ri == 0) headStyle else cellStyle).copy(textAlign = align, fontFeatureSettings = if (numeric[ci]) "tnum" else null),
                    )
                }
            },
        ) { cells, constraints ->
            val gap = 24.dp.roundToPx()
            val pad = 8.dp.roundToPx()
            // Intrinsic queries are allowed before measuring. Every cell is then
            // measured once with the same width as the rest of its column.
            val preferred = List(columns) { ci ->
                all.indices.maxOf { ri -> cells[ri * columns + ci].maxIntrinsicWidth(Constraints.Infinity) }.coerceIn(34.dp.roundToPx(), 240.dp.roundToPx())
            }
            val widths = tableColumnWidths(preferred, viewport - gap * (columns - 1), 96.sp.roundToPx())
            val placed = cells.mapIndexed { i, cell -> cell.measure(Constraints.fixedWidth(widths[i % columns])) }
            // 8 above and below a row's text; the head has nothing above it.
            val rowHeights = all.indices.map { ri -> (0 until columns).maxOf { ci -> placed[ri * columns + ci].height } + if (ri == 0) pad else 2 * pad }
            heights = rowHeights
            layout(constraints.constrainWidth(widths.sum() + gap * (columns - 1)), constraints.constrainHeight(rowHeights.sum())) {
                var y = 0
                for (ri in all.indices) {
                    var x = 0
                    for (ci in 0 until columns) {
                        placed[ri * columns + ci].placeRelative(x, y + if (ri == 0) 0 else pad)
                        x += widths[ci] + gap
                    }
                    y += rowHeights[ri]
                }
            }
        }
    }
}
