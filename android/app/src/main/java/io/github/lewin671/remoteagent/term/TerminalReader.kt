package io.github.lewin671.remoteagent.term

import android.graphics.Paint
import android.graphics.Typeface
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.nestedscroll.NestedScrollConnection
import androidx.compose.ui.input.nestedscroll.NestedScrollSource
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.PlatformTextStyle
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.LineHeightStyle
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
import com.termux.terminal.TextStyle as TermStyle
import com.termux.terminal.WcWidth
import io.github.lewin671.remoteagent.ui.Ic
import io.github.lewin671.remoteagent.ui.Lucide
import io.github.lewin671.remoteagent.ui.T
import io.github.lewin671.remoteagent.ui.TermColors
import io.github.lewin671.remoteagent.ui.ts
import kotlin.math.abs

/**
 * The reader's items in the order its reverse list draws them: item 0 is the bottom of the
 * screen, so the newest line of the newest page comes first and every page's lines run from its
 * last to its first. Read back to front they are the capture's own order, top to bottom. Each
 * key names a line's place in its own capture, so appending an older page leaves the lines that
 * are already on screen where they are.
 */
internal fun historyItems(pages: List<TerminalHistory.Page>): List<Pair<String, List<SgrRun>>> =
    pages.flatMap { page -> page.lines.indices.reversed().map { i -> "${page.id}:$i" to page.lines[i] } }

/**
 * The reader of client-v40 §terminal-history: a capture of the terminal as read-only text
 * over the live screen, which keeps running behind it. It is drawn in the live terminal's own
 * font, size, line height and colours, so that entering it looks like the same picture moving
 * up. Lines come from the pane already at its width, so none of them wrap; each line is one
 * item, so an earlier page only ever adds items where the reader is not looking.
 */
@Composable
internal fun TerminalHistoryReader(
    history: TerminalHistory.State,
    typeface: Typeface,
    palette: IntArray,
    fontPx: Int,
    lineHeightPx: Int,
    cellWidth: Float,
    viewportPx: Int,
    entryRows: Int,
    onLoadOlder: () -> Unit,
    onRetry: () -> Unit,
    onLeave: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val density = LocalDensity.current
    val family = remember(typeface) { FontFamily(typeface) }
    val text = remember(family, palette, fontPx, lineHeightPx, density) {
        TextStyle(
            fontFamily = family,
            fontSize = with(density) { fontPx.toSp() },
            lineHeight = with(density) { lineHeightPx.toSp() },
            color = Color(palette[TermStyle.COLOR_INDEX_FOREGROUND]),
            lineHeightStyle = LineHeightStyle(LineHeightStyle.Alignment.Center, LineHeightStyle.Trim.None),
            // A line box is exactly one terminal row, padding and all.
            platformStyle = PlatformTextStyle(includeFontPadding = false),
        )
    }
    val paint = remember(typeface, fontPx) {
        Paint(Paint.ANTI_ALIAS_FLAG).apply { this.typeface = typeface; textSize = fontPx.toFloat() }
    }
    val list = rememberLazyListState()
    val items = remember(history.pages) { historyItems(history.pages) }
    val canLoadOlder = history.pending.isEmpty() && !history.failed && !history.expired && !history.disconnected
    val last = items.lastIndex
    val nearTop by remember(last) { derivedStateOf { list.layoutInfo.visibleItemsInfo.any { it.index >= last } } }
    LaunchedEffect(nearTop, last, canLoadOlder, history.cursor) {
        if (nearTop && canLoadOlder && history.cursor.isNotEmpty()) onLoadOlder()
    }
    // Opens where the gesture left off: the newest lines are at the bottom, [entryRows] rows of
    // them just below the fold.
    val rows = (viewportPx / lineHeightPx.coerceAtLeast(1)).coerceAtLeast(1)
    LaunchedEffect(Unit) { list.scrollToItem(entryRows.coerceIn(0, rows).coerceAtMost(last.coerceAtLeast(0)), 0) }

    Box(modifier.background(TermColors.bg).nestedScroll(leaveAtBottom(list, lineHeightPx, onLeave))) {
        SelectionContainer(Modifier.fillMaxSize()) {
            LazyColumn(Modifier.fillMaxSize(), state = list, reverseLayout = true) {
                items(items, key = { it.first }) { (_, line) ->
                    Text(
                        sgrText(line, paint, cellWidth, fontPx, palette),
                        style = text,
                        softWrap = false,
                        maxLines = 1,
                        modifier = Modifier.padding(horizontal = 12.dp),
                    )
                }
                // Last here is the top of the screen: the one line about what the reader is doing.
                item(key = "status") { HistoryStatusLine(history, waiting = false, onRetry = onRetry) }
            }
        }
    }
}

/** The grey line at the top of the reader's content, with a retry after a failure. */
@Composable
internal fun HistoryStatusLine(
    history: TerminalHistory.State,
    waiting: Boolean,
    onRetry: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val status = historyStatus(history, waiting) ?: return
    Row(modifier.padding(start = 12.dp, end = 12.dp, top = 6.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(status, style = T.meta, color = TermColors.tool, softWrap = false, maxLines = 1)
        if (history.failed) Text(
            "重试", style = T.metaB, color = TermColors.link,
            modifier = Modifier.clickable(onClick = onRetry).padding(start = 8.dp, top = 12.dp, bottom = 12.dp),
        )
    }
}

/**
 * What the top line says, in the words of the design; null while there is nothing to say.
 * [waiting] is the first capture, which has no reader yet to put a line in.
 */
internal fun historyStatus(history: TerminalHistory.State, waiting: Boolean): String? = when {
    history.disconnected -> "终端断开了，已读到的内容还能看；重新连接后再往上滑读取更早的"
    history.expired -> "记录已过期，回到实时后重新往上滑"
    history.failed -> "读取失败"
    history.pending.isNotEmpty() -> if (waiting) "读取记录…" else "读取更早的记录…"
    !history.entered -> null
    history.cursor.isEmpty() -> "已经是保留的最早记录"
    else -> null
}

/** The floating way back to the live screen; it says when output has arrived since. */
@Composable
internal fun HistoryPill(newOutput: Boolean, onLeave: () -> Unit, modifier: Modifier = Modifier) {
    val shape = RoundedCornerShape(999.dp)
    Row(
        modifier.shadow(4.dp, shape).clip(shape).background(TermColors.key).clickable(onClick = onLeave).padding(horizontal = 14.dp, vertical = 9.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (newOutput) {
            Box(Modifier.size(7.dp).clip(CircleShape).background(TermColors.link))
            Spacer(Modifier.width(6.dp))
            Text("有新输出", style = pillText, color = TermColors.fg, maxLines = 1)
            Spacer(Modifier.width(6.dp))
            Ic(Lucide.ArrowDown, 15.dp, TermColors.fg)
        } else {
            Ic(Lucide.ArrowDown, 15.dp, TermColors.fg)
            Spacer(Modifier.width(6.dp))
            Text("回到实时", style = pillText, color = TermColors.fg, maxLines = 1)
        }
    }
}

private val pillText = ts(13.sp, FontWeight.SemiBold)

/**
 * Leaves reading when the scroll goes past the newest line, which is what a wheel down or a
 * drag up along the bottom edge is: the reverse list has nothing to take it, so the whole
 * delta is left to us and one line's worth of it is enough.
 */
@Composable
private fun leaveAtBottom(list: LazyListState, lineHeightPx: Int, onLeave: () -> Unit): NestedScrollConnection {
    val leave = rememberUpdatedState(onLeave)
    val threshold = lineHeightPx.coerceAtLeast(1).toFloat()
    val past = remember { Past() }
    return remember(list, threshold) {
        object : NestedScrollConnection {
            override fun onPostScroll(consumed: Offset, available: Offset, source: NestedScrollSource): Offset {
                if (available.y < 0f && !list.canScrollBackward) {
                    past.pixels += available.y
                    if (-past.pixels >= threshold) leave.value()
                } else {
                    past.pixels = 0f
                }
                return Offset.Zero
            }
        }
    }
}

/** How far past the newest line the reader was scrolled with nothing left to scroll. */
private class Past {
    var pixels = 0f
}

/** A line as styled runs that take the cells the live terminal gives them. */
private fun sgrText(line: List<SgrRun>, paint: Paint, cellWidth: Float, fontPx: Int, palette: IntArray): AnnotatedString =
    buildAnnotatedString {
        for (run in line) withStyle(styleOf(run, palette)) {
            var i = 0
            while (i < run.text.length) {
                val codePoint = run.text.codePointAt(i)
                val chars = Character.charCount(codePoint)
                val piece = run.text.substring(i, i + chars)
                // A glyph the font draws narrower or wider than its cells (a Chinese glyph from
                // a fallback font beside a 0.6 em Latin cell) gets the difference after it, so
                // the columns after it stay where the live terminal draws them.
                val extra = WcWidth.width(codePoint) * cellWidth - paint.measureText(piece)
                if (fontPx > 0 && cellWidth > 0f && abs(extra) > HALF_PIXEL) {
                    withStyle(SpanStyle(letterSpacing = (extra / fontPx).em)) { append(piece) }
                } else {
                    append(piece)
                }
                i += chars
            }
        }
    }

private const val HALF_PIXEL = 0.5f

/** A run's style, with its colours and attributes resolved as the live renderer has them. */
private fun styleOf(run: SgrRun, palette: IntArray): SpanStyle {
    val default = palette[TermStyle.COLOR_INDEX_BACKGROUND]
    var fg = color(run.fg, palette[TermStyle.COLOR_INDEX_FOREGROUND], run.bold, palette)
    var bg = color(run.bg, default, false, palette)
    if (run.inverse) {
        val swap = fg
        fg = bg
        bg = swap
    }
    if (run.dim) fg = Color(fg.red * 2f / 3f, fg.green * 2f / 3f, fg.blue * 2f / 3f, fg.alpha)
    return SpanStyle(
        color = fg,
        // The live terminal paints the default background as nothing, so the pane's own
        // colour stays behind the text.
        background = if (bg == Color(default)) Color.Unspecified else bg,
        fontWeight = if (run.bold) FontWeight.Bold else null,
        fontStyle = if (run.italic) FontStyle.Italic else null,
        textDecoration = if (run.underline) TextDecoration.Underline else null,
    )
}

/** The colour of a palette index or truecolor value; null is the terminal's own default. */
private fun color(c: SgrColor?, fallback: Int, bold: Boolean, palette: IntArray): Color = when (c) {
    null -> Color(fallback)
    is SgrColor.Rgb -> Color(0xff shl 24 or c.hex.removePrefix("#").toInt(16))
    is SgrColor.Palette -> {
        // Bold brightens the first eight colours, as the live renderer does.
        val index = if (bold && c.index < 8) c.index + 8 else c.index
        Color(if (index in palette.indices) palette[index] else fallback)
    }
}
