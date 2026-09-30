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
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.withAnnotation
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

// A small Markdown renderer for chat text: headings, paragraphs, lists,
// quotes, fenced code, tables and rules; inline code, bold, italic and
// links. Code and tables scroll sideways instead of wrapping. Text is only
// ever rendered as text.

sealed interface Block {
    data class Heading(val level: Int, val text: String) : Block
    data class Para(val text: String) : Block
    data class Code(val lang: String, val text: String) : Block
    data class Item(val ordered: Boolean, val marker: String, val depth: Int, val text: String) : Block
    data class Quote(val text: String) : Block
    data class Table(val header: List<String>, val rows: List<List<String>>) : Block
    data object Rule : Block
}

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
            t.startsWith("|") && i + 1 < lines.size && Regex("^\\|?\\s*:?-{2,}").containsMatchIn(lines[i + 1].trim()) -> {
                flush()
                fun cells(s: String) = s.trim().trim('|').split("|").map { it.trim() }
                val header = cells(t)
                i += 2
                val rows = mutableListOf<List<String>>()
                while (i < lines.size && lines[i].trim().startsWith("|")) {
                    rows += cells(lines[i])
                    i++
                }
                out += Block.Table(header, rows)
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

/** Inline formatting: `code`, **bold**, *italic*, [text](url). */
fun inline(text: String, codeBg: androidx.compose.ui.graphics.Color, link: androidx.compose.ui.graphics.Color): AnnotatedString = buildAnnotatedString {
    var i = 0
    val s = text
    while (i < s.length) {
        val c = s[i]
        when {
            c == '`' -> {
                val end = s.indexOf('`', i + 1)
                if (end > i) {
                    withAnnotation(CODE, "") { withStyle(SpanStyle(fontFamily = MonoFamily, fontSize = 13.sp)) { append(s.substring(i + 1, end)) } }
                    i = end + 1
                } else {
                    append(c); i++
                }
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
                val close = s.indexOf("](", i)
                val end = if (close > 0) s.indexOf(')', close) else -1
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

@Composable
private fun TableBlock(t: Block.Table) {
    val p = pal
    Box(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState())) {
        Column(Modifier.border(1.dp, p.line)) {
            val all = listOf(t.header) + t.rows
            val cols = all.maxOf { it.size }
            for ((ri, row) in all.withIndex()) {
                Row(if (ri == 0) Modifier.background(p.soft) else Modifier) {
                    for (ci in 0 until cols) {
                        Text(
                            inline(row.getOrElse(ci) { "" }, p.chip, p.blue),
                            Modifier.widthIn(min = 48.dp, max = 240.dp).padding(horizontal = 7.dp, vertical = 3.dp),
                            style = T.sm, color = p.fg,
                            fontWeight = if (ri == 0) platformWeight(FontWeight.SemiBold) else null,
                        )
                    }
                }
                if (ri < all.lastIndex) HorizontalDivider(color = p.line)
            }
        }
    }
}
