package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.Alignment
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.withLink
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.constrainWidth
import androidx.compose.ui.unit.constrainHeight
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.withAnnotation
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

// A small Markdown renderer for chat text: headings, paragraphs, lists,
// quotes, fenced code, tables and rules; inline code, bold, italic and
// links. Code stays unwrapped; tables wrap long cells and scroll sideways
// when needed. Text is only
// ever rendered as text.

sealed interface Block {
    data class Heading(val level: Int, val text: String) : Block
    data class Para(val text: String) : Block
    data class Code(val lang: String, val text: String) : Block
    data class Item(val ordered: Boolean, val marker: String, val depth: Int, val text: String) : Block
    data class Quote(val text: String) : Block
    data class Table(val header: List<String>, val rows: List<List<String>>, val align: List<TableAlign> = emptyList()) : Block
    data object Rule : Block
}

enum class TableAlign { START, CENTER, END }

private fun escapedAt(text: String, index: Int): Boolean {
    var at = index - 1
    var slashes = 0
    while (at >= 0 && text[at--] == '\\') slashes++
    return slashes % 2 == 1
}

/** Pipes escaped or inside a code span do not create extra columns. */
fun tableCells(line: String): List<String> {
    var s = line.trim().removePrefix("|")
    if (s.endsWith("|")) {
        var slashes = 0
        var at = s.lastIndex - 1
        while (at >= 0 && s[at--] == '\\') slashes++
        if (slashes % 2 == 0) s = s.dropLast(1)
    }
    val cells = mutableListOf<String>()
    val cell = StringBuilder()
    var codeFence = 0
    val ticks = Regex("`+").findAll(s).groupingBy { it.value.length }.eachCount().toMutableMap()
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
                if (codeFence == 0 && !escapedAt(s, i) && (ticks[n] ?: 0) > 0) codeFence = n else if (codeFence == n) codeFence = 0
                cell.append(s.substring(i, end)); i = end
            }
            s[i] == '|' && codeFence == 0 -> {
                cells += cell.toString().trim(); cell.clear(); i++
            }
            else -> { cell.append(s[i]); i++ }
        }
    }
    cells += cell.toString().trim()
    return cells
}

private val tableDivider = Regex(":?-{3,}:?")

fun parseMarkdown(src: String): List<Block> {
    val out = mutableListOf<Block>()
    val lines = src.replace("\r\n", "\n").split("\n")
    var i = 0
    val para = StringBuilder()
    fun flush() {
        if (para.isNotBlank()) out += Block.Para(para.toString().trim())
        para.clear()
    }
    val bullet = Regex("^(\\s*)([-*+])\\s+(.*)$")
    val ordered = Regex("^(\\s*)(\\d{1,3}[.)])\\s+(.*)$")
    while (i < lines.size) {
        val line = lines[i]
        val t = line.trim()
        val divider = if ('|' in t && i + 1 < lines.size) tableCells(lines[i + 1]) else emptyList()
        val table = divider.isNotEmpty() && divider.all { tableDivider.matches(it) } && tableCells(t).size == divider.size
        when {
            t.startsWith("```") || t.startsWith("~~~") -> {
                flush()
                val fence = t.take(3)
                val lang = t.drop(3).trim()
                val code = StringBuilder()
                i++
                while (i < lines.size && !lines[i].trim().startsWith(fence)) {
                    code.append(lines[i]).append('\n')
                    i++
                }
                out += Block.Code(lang, code.toString().trimEnd('\n'))
            }
            t.isEmpty() -> flush()
            Regex("^#{1,6}\\s").containsMatchIn(t) -> {
                flush()
                val level = t.takeWhile { it == '#' }.length
                out += Block.Heading(level, t.drop(level).trim())
            }
            Regex("^(-{3,}|\\*{3,}|_{3,})$").matches(t) -> {
                flush()
                out += Block.Rule
            }
            t.startsWith(">") -> {
                flush()
                val q = StringBuilder()
                while (i < lines.size && lines[i].trim().startsWith(">")) {
                    q.append(lines[i].trim().removePrefix(">").trim()).append('\n')
                    i++
                }
                out += Block.Quote(q.toString().trim())
                continue
            }
            table -> {
                flush()
                val header = tableCells(t)
                val align = divider.map {
                    when { it.startsWith(":") && it.endsWith(":") -> TableAlign.CENTER; it.endsWith(":") -> TableAlign.END; else -> TableAlign.START }
                }.takeUnless { values -> values.all { it == TableAlign.START } }.orEmpty()
                i += 2
                val rows = mutableListOf<List<String>>()
                while (i < lines.size && '|' in lines[i] && lines[i].isNotBlank()) {
                    rows += tableCells(lines[i]); i++
                }
                out += Block.Table(header, rows, align)
                continue
            }
            bullet.matches(line) || ordered.matches(line) -> {
                flush()
                val m = bullet.find(line) ?: ordered.find(line)!!
                val depth = (m.groupValues[1].length / 2).coerceAtMost(3)
                val isOrdered = ordered.matches(line)
                val text = StringBuilder(m.groupValues[3])
                // Continuation lines indented under the item.
                while (i + 1 < lines.size && lines[i + 1].startsWith("  ") && lines[i + 1].isNotBlank() &&
                    !bullet.matches(lines[i + 1]) && !ordered.matches(lines[i + 1])
                ) {
                    i++
                    text.append(' ').append(lines[i].trim())
                }
                out += Block.Item(isOrdered, if (isOrdered) m.groupValues[2] else "•", depth, text.toString())
            }
            else -> para.append(t).append('\n')
        }
        i++
    }
    flush()
    return out
}

/** Next matching whole backtick run, in linear time even for unmatched runs. */
private fun codeClosers(text: String): Map<Int, Int> {
    val closers = mutableMapOf<Int, Int>()
    val next = mutableMapOf<Int, Int>()
    val runs = Regex("`+").findAll(text).toList()
    for (run in runs.asReversed()) {
        next[run.value.length]?.let { closers[run.range.first] = it }
        next[run.value.length] = run.range.first
    }
    return closers
}

/** First closing delimiter for every opener, computed without repeated suffix scans. */
private fun linkClosers(text: String): Map<Int, Pair<Int, Int>> {
    val links = mutableMapOf<Int, Pair<Int, Int>>()
    var close = -1
    var end = -1
    var paren = -1
    for (i in text.indices.reversed()) {
        when (text[i]) {
            '[' -> if (close > i && end > close) links[i] = close to end
            ']' -> if (i + 1 < text.length && text[i + 1] == '(') { close = i; end = paren }
            ')' -> paren = i
        }
    }
    return links
}

/** Inline formatting: `code`, **bold**, *italic*, [text](url). */
fun inline(text: String, codeBg: androidx.compose.ui.graphics.Color, link: androidx.compose.ui.graphics.Color): AnnotatedString = buildAnnotatedString {
    var i = 0
    val s = text
    val closers = codeClosers(s)
    val links = linkClosers(s)
    while (i < s.length) {
        val c = s[i]
        when {
            c == '\\' && i + 1 < s.length && s[i + 1] == '`' && !escapedAt(s, i) -> { append('`'); i += 2 }
            c == '`' -> {
                var openingEnd = i + 1
                while (openingEnd < s.length && s[openingEnd] == '`') openingEnd++
                val end = closers[i]
                if (end != null) {
                    withAnnotation(CODE, "") { withStyle(SpanStyle(fontFamily = MonoFamily, fontSize = 13.sp)) { append(s.substring(openingEnd, end)) } }
                    i = end + (openingEnd - i)
                } else { append(s.substring(i, openingEnd)); i = openingEnd }
            }
            s.startsWith("**", i) -> {
                val end = s.indexOf("**", i + 2)
                if (end > i + 2) {
                    withStyle(SpanStyle(fontWeight = platformWeight(FontWeight.Bold))) { append(inline(s.substring(i + 2, end), codeBg, link)) }
                    i = end + 2
                } else {
                    append("**"); i += 2
                }
            }
            (c == '*' || c == '_') && i + 1 < s.length && s[i + 1] != ' ' && (i == 0 || !s[i - 1].isLetterOrDigit()) -> {
                val end = s.indexOf(c, i + 1)
                if (end > i + 1 && s[end - 1] != ' ') {
                    withStyle(SpanStyle(fontStyle = FontStyle.Italic)) { append(s.substring(i + 1, end)) }
                    i = end + 1
                } else {
                    append(c); i++
                }
            }
            c == '[' -> {
                val (close, end) = links[i] ?: (-1 to -1)
                if (close > i && end > close) {
                    val label = s.substring(i + 1, close)
                    val url = s.substring(close + 2, end)
                    if (url.startsWith("http://") || url.startsWith("https://")) {
                        withLink(LinkAnnotation.Url(url, TextLinkStyles(SpanStyle(color = link)))) { append(label) }
                    } else {
                        // File references read as code, not as links.
                        withStyle(SpanStyle(fontFamily = MonoFamily, fontSize = 13.sp)) { append(label) }
                    }
                    i = end + 1
                } else {
                    append(c); i++
                }
            }
            else -> {
                append(c); i++
            }
        }
    }
}

/** An agent's reply (client-v7 .ai): 15 at 1.55, paragraphs 6 apart, list items 4. */
@Composable
fun Markdown(text: String, modifier: Modifier = Modifier) {
    val blocks = remember(text) { parseMarkdown(text) }
    val p = pal
    val body = T.chat.copy(color = p.fg)
    SelectionContainer(modifier) {
        Column {
            blocks.forEachIndexed { i, b ->
                val prev = blocks.getOrNull(i - 1)
                val gap = when {
                    i == 0 -> 0.dp
                    prev is Block.Item && b is Block.Item -> 4.dp
                    b is Block.Code || b is Block.Table || prev is Block.Code || prev is Block.Table -> 6.dp
                    else -> 6.dp
                }
                Box(Modifier.padding(top = gap)) {
                    when (b) {
                        is Block.Heading -> RichText(inline(b.text, p.chip, p.blue), style = (if (b.level <= 2) T.h17 else T.bodyB).copy(color = p.fg))
                        is Block.Para -> RichText(inline(b.text.replace("\n", " "), p.chip, p.blue), style = body)
                        is Block.Item -> Row(Modifier.padding(start = (b.depth * 20).dp)) {
                            // CJK fonts draw • as a small centered dot; draw the disc the mockup's list uses.
                            Box(Modifier.width(20.dp).height(with(LocalDensity.current) { body.lineHeight.toDp() }), contentAlignment = Alignment.Center) {
                                if (b.marker == "•") Box(Modifier.size(5.dp).background(p.fg, CircleShape)) else Text(b.marker, style = body, color = p.fg)
                            }
                            RichText(inline(b.text, p.chip, p.blue), style = body)
                        }
                        is Block.Quote -> Row {
                            Box(Modifier.width(3.dp).background(p.line).padding(vertical = 2.dp)) { Text(" ") }
                            RichText(inline(b.text, p.chip, p.blue), style = body.copy(color = p.muted), modifier = Modifier.padding(start = 10.dp))
                        }
                        is Block.Code -> CodeBlock(b.text)
                        is Block.Table -> TableBlock(b)
                        Block.Rule -> HorizontalDivider(color = p.line)
                    }
                }
            }
        }
    }
}

private const val CODE = "code"

/**
 * Text whose inline code sits on a rounded chip, as the mockup's `.ai code`
 * (padding 1×4, radius 4): a span background would fill the whole line
 * height with square corners.
 */
@Composable
fun RichText(text: AnnotatedString, style: TextStyle, modifier: Modifier = Modifier) {
    val bg = pal.chip
    val codes = remember(text) { text.getStringAnnotations(CODE, 0, text.length) }
    var layout by remember { mutableStateOf<TextLayoutResult?>(null) }
    Text(
        text, style = style, onTextLayout = { layout = it },
        modifier = if (codes.isEmpty()) modifier else modifier.drawBehind {
            val l = layout ?: return@drawBehind
            val padX = 3.dp.toPx()
            val half = 13.sp.toPx() * 0.72f
            for (a in codes) {
                if (a.end <= a.start) continue
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

/** A code block in a reply: soft, a line around, radius 6, mono 12. */
@Composable
fun CodeBlock(text: String, maxLines: Int = Int.MAX_VALUE, modifier: Modifier = Modifier) {
    val p = pal
    Box(
        modifier.fillMaxWidth().background(p.soft, Radii.XS).border(1.dp, p.line, Radii.XS)
            .horizontalScroll(rememberScrollState()).padding(horizontal = 10.dp, vertical = 8.dp),
    ) {
        Text(text, style = T.mono, color = p.fg2, softWrap = false, maxLines = maxLines)
    }
}

/** Keep compact tables in the viewport without squeezing wide or large-font
 * tables below a readable width. Numeric columns keep their intrinsic width. */
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

private data class TableGrid(val widths: List<Int>, val heights: List<Int>)

@Composable
private fun TableBlock(t: Block.Table) {
    val p = pal
    val all = remember(t) { listOf(t.header) + t.rows }
    val columns = t.header.size
    if (columns == 0) return
    var grid by remember(t) { mutableStateOf<TableGrid?>(null) }
    androidx.compose.foundation.layout.BoxWithConstraints(Modifier.fillMaxWidth()) {
        val viewportWidth = constraints.maxWidth
        Box(Modifier.horizontalScroll(rememberScrollState())) {
            Layout(
                modifier = Modifier.border(1.dp, p.line).drawBehind {
                    val g = grid ?: return@drawBehind
                    drawRect(p.soft, size = Size(size.width, g.heights.first().toFloat()))
                    val stroke = 1.dp.toPx()
                    var y = 0f
                    for (height in g.heights.dropLast(1)) {
                        y += height
                        drawLine(p.line, Offset(0f, y), Offset(size.width, y), strokeWidth = stroke)
                    }
                    var x = 0f
                    for (width in g.widths.dropLast(1)) {
                        x += width
                        val at = if (layoutDirection == LayoutDirection.Rtl) size.width - x else x
                        drawLine(p.line, Offset(at, 0f), Offset(at, size.height), strokeWidth = stroke)
                    }
                },
                content = {
                    for ((ri, row) in all.withIndex()) for (ci in 0 until columns) {
                        val align = when (t.align.getOrNull(ci)) {
                            TableAlign.CENTER -> TextAlign.Center
                            TableAlign.END -> TextAlign.Right
                            else -> TextAlign.Left
                        }
                        RichText(
                            inline(row.getOrElse(ci) { "" }, p.chip, p.blue),
                            style = T.sm.copy(color = p.fg, textAlign = align,
                                fontWeight = if (ri == 0) platformWeight(FontWeight.SemiBold) else T.sm.fontWeight),
                            modifier = Modifier.padding(horizontal = 7.dp, vertical = 3.dp),
                        )
                    }
                },
            ) { cells, constraints ->
                // Intrinsic queries are allowed before measuring. Every cell is then
                // measured once with the same width as the rest of its column.
                val preferred = List(columns) { ci ->
                    all.indices.maxOf { ri -> cells[ri * columns + ci].maxIntrinsicWidth(Constraints.Infinity) }
                        .coerceIn(48.dp.roundToPx(), 240.dp.roundToPx())
                }
                val widths = tableColumnWidths(preferred, viewportWidth, 96.sp.roundToPx())
                val placed = cells.mapIndexed { i, cell -> cell.measure(Constraints.fixedWidth(widths[i % columns])) }
                val heights = all.indices.map { ri -> (0 until columns).maxOf { ci -> placed[ri * columns + ci].height } }
                grid = TableGrid(widths, heights)
                layout(constraints.constrainWidth(widths.sum()), constraints.constrainHeight(heights.sum())) {
                    var y = 0
                    for (ri in all.indices) {
                        var x = 0
                        for (ci in 0 until columns) {
                            val cell = placed[ri * columns + ci]
                            cell.placeRelative(x, y + (heights[ri] - cell.height) / 2)
                            x += widths[ci]
                        }
                        y += heights[ri]
                    }
                }
            }
        }
    }
}
