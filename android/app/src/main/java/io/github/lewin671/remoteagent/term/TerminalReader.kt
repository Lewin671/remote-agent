package io.github.lewin671.remoteagent.term

import android.graphics.Paint
import android.graphics.Typeface
import android.view.ViewConfiguration
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.wrapContentWidth
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
import androidx.compose.runtime.MutableFloatState
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.nestedscroll.NestedScrollConnection
import androidx.compose.ui.input.nestedscroll.NestedScrollSource
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.PointerEventType
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.input.pointer.util.VelocityTracker
import androidx.compose.ui.platform.LocalContext
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
import androidx.compose.ui.unit.IntOffset
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
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlin.math.abs
import kotlin.math.exp
import kotlin.math.roundToInt

/**
 * The reader's items in the order its reverse list draws them: item 0 is the bottom of the
 * screen, so the newest line of the newest page comes first and every page's lines run from its
 * last to its first. Read back to front they are the capture's own order, top to bottom. Each
 * key names a line's place in its own capture, so appending an older page leaves the lines that
 * are already on screen where they are.
 */
internal fun historyItems(pages: List<TerminalHistory.Page>): List<Pair<String, List<SgrRun>>> =
    pages.flatMap { page -> page.lines.indices.reversed().map { i -> "${page.id}:$i" to page.lines[i] } }

/** The axis a reader drag is on. */
internal enum class DragAxis { Horizontal, Vertical }

/**
 * The axis of a drag, decided by the dominant one. The same rule as the live terminal
 * (TerminalView.horizontalDrag): a tie keeps the vertical meaning, so the pull into reading works.
 */
internal fun dragAxis(dx: Float, dy: Float): DragAxis =
    if (abs(dx) > abs(dy)) DragAxis.Horizontal else DragAxis.Vertical

/**
 * The grid's drawn width: columns times the cell width. The live view's horizontal scroll range
 * uses the same product, so reading it from there makes both stop at the same right edge.
 */
internal fun gridWidthPx(cols: Int, cellWidth: Float): Int = (cols * cellWidth).roundToInt()

/** How far a viewport can be panned over a grid: the part of it past the viewport, never negative. */
internal fun maxPanPx(viewportWidthPx: Int, gridWidthPx: Int): Int = (gridWidthPx - viewportWidthPx).coerceAtLeast(0)

/**
 * The reader's content width: at least as wide as the viewport, and the whole grid when that is
 * wider. Only the part past the viewport is panned, so a fitted grid leaves the whole width as the
 * vertical list's own drag target.
 */
internal fun readerWidth(viewportWidthPx: Int, gridWidthPx: Int): Int =
    viewportWidthPx.coerceAtLeast(gridWidthPx)

/**
 * Where a grid column's left edge is drawn in the viewport: the grid is placed at the start and the
 * content is panned left by [offset]. Mirrors the reader's [wrapContentWidth] + offset placement.
 */
internal fun gridColumnX(column: Int, offset: Int, cellWidth: Float): Int =
    (column * cellWidth).roundToInt() - offset

/** The cells one captured line takes, by the same wide-character rule the reader draws it with. */
internal fun lineCells(line: List<SgrRun>): Int {
    var cells = 0
    for (run in line) {
        var i = 0
        while (i < run.text.length) {
            val codePoint = run.text.codePointAt(i)
            cells += WcWidth.width(codePoint)
            i += Character.charCount(codePoint)
        }
    }
    return cells
}

/** The cells of the widest captured line; 0 when nothing has been captured. */
internal fun widestLineCells(pages: List<TerminalHistory.Page>): Int =
    pages.maxOfOrNull { page -> page.lines.maxOfOrNull { lineCells(it) } ?: 0 } ?: 0

/**
 * The reader's horizontal extent: wide enough for the captured lines, and never narrower than the
 * live grid. A terminal that got narrower since the capture must not clip what was captured wider.
 */
internal fun readerExtentPx(liveGridWidthPx: Int, capturedCells: Int, cellWidth: Float): Int =
    maxOf(liveGridWidthPx, gridWidthPx(capturedCells, cellWidth))

/**
 * Whether the reader takes a drag for its own sideways pan: a horizontal drag that has somewhere to
 * pan and crossed the touch slop before a long press could take it for text selection ([heldMillis]
 * under the long-press timeout). A drag the selection already owns is left to it.
 */
internal fun claimsPan(axis: DragAxis, maxOffsetPx: Float, heldMillis: Long, longPressTimeoutMillis: Long): Boolean =
    axis == DragAxis.Horizontal && maxOffsetPx > 0f && heldMillis < longPressTimeoutMillis

/**
 * One frame of a sideways fling: where it moves next, and the speed left. It stops at either end of
 * [maxOffset], which is read every frame, so a bound that shrank cannot be run past.
 */
internal fun flingStep(offset: Float, velocity: Float, dt: Float, maxOffset: Float): Pair<Float, Float> {
    val bound = maxOffset.coerceAtLeast(0f)
    val next = (offset + velocity * dt).coerceIn(0f, bound)
    val stopped = (next <= 0f && velocity < 0f) || (next >= bound && velocity > 0f)
    return next to if (stopped) 0f else velocity * exp(-FLING_FRICTION * dt)
}

/**
 * The reader's sideways offset after a wheel or trackpad scroll: by the scroll's own amount
 * in pixels, stopped at the pan's bounds. The axis value is the one the live view pans by, so
 * a wheel towards the right end moves the content the way a sideways drag does.
 */
internal fun readerWheelOffset(offset: Float, scrollDeltaX: Float, pixelsPerUnit: Float, maxOffset: Float): Float =
    (offset + scrollDeltaX * pixelsPerUnit).coerceIn(0f, maxOffset.coerceAtLeast(0f))

/**
 * The reader's own thin sideways bar: the thumb's left edge and width within a track of
 * [trackPx], for content [extentPx] wide panned by [offsetPx] behind a [viewportPx]-wide view,
 * in the platform's own proportions — the thumb reaches the track's right edge exactly at the
 * end of the pan, and a fitted extent fills the whole track.
 */
internal fun scrollThumb(offsetPx: Int, viewportPx: Int, extentPx: Int, trackPx: Int): Pair<Float, Float> {
    if (trackPx <= 0) return 0f to 0f
    val track = trackPx.toFloat()
    if (extentPx <= viewportPx) return 0f to track
    val width = track * viewportPx / extentPx
    val left = track * offsetPx / extentPx
    return left.coerceIn(0f, track - width) to width
}

/**
 * The reader of client-v40 §terminal-history: a capture of the terminal as read-only text
 * over the live screen, which keeps running behind it. It is drawn in the live terminal's own
 * font, size, line height and colours, so that entering it looks like the same picture moving
 * up. Lines come from the pane already at its width, so none of them wrap; each line is one
 * item, so an earlier page only ever adds items where the reader is not looking. Content wider
 * than the screen — the captured lines, never narrower than the live grid ([liveGridWidthPx]) — is
 * panned sideways, opening at the live screen's own [entryX] and clipping nothing.
 *
 * One gesture has one axis, decided the way the live terminal decides it: the first move past the
 * touch slop sets it, and a sideways drag is taken whole so the vertical list never claims it. The
 * bottom exit is a vertical gesture.
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
    viewportWidthPx: Int,
    liveGridWidthPx: Int,
    entryX: Int,
    entryRows: Int,
    onLoadOlder: () -> Unit,
    onRetry: () -> Unit,
    onLeave: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val density = LocalDensity.current
    // The pixels one wheel or trackpad notch moves: the platform's own factor, the same amount
    // the live view pans by, so a wheel moves both pictures alike.
    val context = LocalContext.current
    val wheelPixels = remember(context) { ViewConfiguration.get(context).getScaledHorizontalScrollFactor() }
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

    // The capture may have been taken when the terminal was wider than it is now: the extent covers
    // the widest captured line, with the live grid only as a minimum, so a narrower window never
    // clips lines that are already on screen. It is also at least the viewport, so a fitted grid
    // still fills the width for a vertical drag.
    val capturedCells = remember(history.pages) { widestLineCells(history.pages) }
    val extentPx = readerExtentPx(liveGridWidthPx, capturedCells, cellWidth)
    val widthPx = readerWidth(viewportWidthPx, extentPx)
    val width = with(density) { widthPx.toDp() }
    val maxOffset = maxPanPx(viewportWidthPx, extentPx).toFloat()
    val offset = remember { mutableFloatStateOf(entryX.toFloat().coerceIn(0f, maxOffset)) }
    // A viewport change (rotation) can leave the pan past the end of the grid, and must take a
    // running fling with it instead of letting it write past the new end.
    val fling = remember { Fling() }
    LaunchedEffect(maxOffset) {
        fling.job?.cancel()
        offset.floatValue = offset.floatValue.coerceIn(0f, maxOffset)
    }
    // The bound the fling reads every frame, so a viewport change re-bounds it mid-flight.
    val bound = rememberUpdatedState(maxOffset)
    val panning = remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    Box(modifier.background(TermColors.bg).nestedScroll(leaveAtBottom(list, lineHeightPx, panning, onLeave))) {
        Box(
            Modifier.fillMaxSize().clipToBounds()
                // A wheel or trackpad scroll sideways pans the reader like a drag, by the pan's
                // own bounds. It is taken whole, so it reaches neither the list (the pull out of
                // reading) nor the live screen behind it; a vertical wheel is left to the list,
                // which scrolls and leaves as it always has.
                .pointerInput(wheelPixels) {
                    awaitPointerEventScope {
                        while (true) {
                            val event = awaitPointerEvent(PointerEventPass.Initial)
                            if (event.type != PointerEventType.Scroll) continue
                            var dx = 0f
                            var dy = 0f
                            for (change in event.changes) {
                                dx += change.scrollDelta.x
                                dy += change.scrollDelta.y
                            }
                            if (dx == 0f || abs(dx) <= abs(dy)) continue
                            fling.job?.cancel()
                            event.changes.forEach { it.consume() }
                            offset.floatValue = readerWheelOffset(offset.floatValue, dx, wheelPixels, bound.value)
                        }
                    }
                }
                .pointerInput(widthPx, viewportWidthPx) {
                awaitEachGesture {
                    val down = awaitFirstDown(requireUnconsumed = false)
                    // A new touch takes the pan over from a fling still running.
                    fling.job?.cancel()
                    panning.value = false
                    var decided = false
                    var claimed = false
                    val tracker = VelocityTracker()
                    tracker.addPosition(down.uptimeMillis, down.position)
                    while (true) {
                        // Read before the list's own scrollable: a sideways gesture is taken here
                        // whole, so it can neither scroll rows nor leave reading.
                        val event = awaitPointerEvent(PointerEventPass.Initial)
                        val change = event.changes.firstOrNull { it.id == down.id } ?: break
                        if (!change.pressed) break
                        tracker.addPosition(change.uptimeMillis, change.position)
                        if (!decided) {
                            val total = change.position - down.position
                            if (total.getDistance() > viewConfiguration.touchSlop) {
                                decided = true
                                // A long press (or a grid with nowhere to pan) owns the drag: leave
                                // it to text selection rather than taking it for a pan.
                                claimed = claimsPan(
                                    dragAxis(total.x, total.y), maxOffset,
                                    change.uptimeMillis - down.uptimeMillis, viewConfiguration.longPressTimeoutMillis,
                                )
                                panning.value = claimed
                            }
                        }
                        if (claimed) {
                            change.consume()
                            val delta = change.position - change.previousPosition
                            offset.floatValue = (offset.floatValue - delta.x).coerceIn(0f, maxOffset)
                        }
                    }
                    // The gesture is over: a wheel or trackpad scroll is not on either axis of it.
                    panning.value = false
                    if (claimed) {
                        val velocity = tracker.calculateVelocity().x
                        fling.job = scope.launch { flingPan(offset, velocity) { bound.value } }
                    }
                }
            },
        ) {
            // Measured at the whole grid width but reported as the viewport and placed at its start:
            // width() alone is clamped by the viewport-sized box (every line laid out short), and
            // requiredWidth is centered by the layout system when it overflows (shifting the grid by
            // (width - viewport)/2, so column 0 sits off the left edge and the pan overruns). The
            // outer box clips the part past the viewport.
            SelectionContainer(
                Modifier
                    .offset { IntOffset(-offset.floatValue.roundToInt(), 0) }
                    .wrapContentWidth(Alignment.Start, unbounded = true)
                    .width(width)
                    .fillMaxHeight(),
            ) {
                LazyColumn(Modifier.fillMaxSize(), state = list, reverseLayout = true) {
                    items(items, key = { it.first }) { (_, line) ->
                        Text(
                            sgrText(line, paint, cellWidth, fontPx, palette),
                            style = text,
                            softWrap = false,
                            maxLines = 1,
                        )
                    }
                    // Last here is the top of the screen: the one line about what the reader is doing.
                    item(key = "status") { HistoryStatusLine(history, waiting = false, onRetry = onRetry) }
                }
            }
        }
        // The capture is wider than the viewport: the reader's own thin sideways bar sits at the
        // foot, in the platform's colours, showing how far along the pan is.
        if (extentPx > viewportWidthPx) {
            ReaderScrollbar({ offset.floatValue }, viewportWidthPx, extentPx, Modifier.align(Alignment.BottomStart))
        }
    }
}

/**
 * The reader's own thin sideways bar at the foot of the viewport, in the platform's own colours
 * and shape: the track is inset at both ends as the design has it, and the thumb is the part of
 * the wider capture that is in sight, at the pan's own position. [offset] is read when it draws,
 * not when it composes, so a pan does not recompose the reader.
 */
@Composable
private fun ReaderScrollbar(offset: () -> Float, viewportPx: Int, extentPx: Int, modifier: Modifier = Modifier) {
    val trackPx = (viewportPx - with(LocalDensity.current) { SCROLLBAR_LEFT.dp.roundToPx() + SCROLLBAR_RIGHT.dp.roundToPx() }).coerceAtLeast(1)
    Box(
        modifier.padding(start = SCROLLBAR_LEFT.dp, end = SCROLLBAR_RIGHT.dp, bottom = 4.dp)
            .fillMaxWidth()
            .height(3.dp)
            .clip(RoundedCornerShape(2.dp))
            .drawBehind {
                val (thumbLeft, thumbWidth) = scrollThumb(offset().roundToInt(), viewportPx, extentPx, trackPx)
                drawRect(TermColors.line)
                drawRect(TermColors.tool, topLeft = Offset(thumbLeft, 0f), size = Size(thumbWidth, size.height))
            },
    )
}

/** The reader's bar insets, as the design draws them: clear on the left, clear of the pill on the right. */
private const val SCROLLBAR_LEFT = 12
private const val SCROLLBAR_RIGHT = 120

/** The running sideways fling, so a new touch can take the pan back over. */
private class Fling {
    var job: Job? = null
}

/**
 * Carries a sideways pan on with the finger's momentum, losing speed to friction and stopping at
 * the ends of the grid. The offset moves opposite the finger, so the fling runs opposite its x
 * velocity.
 */
private suspend fun flingPan(offset: MutableFloatState, velocityX: Float, maxOffset: () -> Float) {
    var velocity = -velocityX
    if (abs(velocity) < FLING_STOP_VELOCITY) return
    var last = withFrameNanos { it }
    while (abs(velocity) > FLING_STOP_VELOCITY) {
        val now = withFrameNanos { it }
        val dt = ((now - last) / 1_000_000_000f).coerceIn(0f, 0.05f)
        last = now
        val (next, remaining) = flingStep(offset.floatValue, velocity, dt, maxOffset())
        offset.floatValue = next
        velocity = remaining
    }
}

/** Below this speed the fling has stopped; a slower flick is just a drag that ended. */
private const val FLING_STOP_VELOCITY = 50f

/** How fast a fling loses speed, per second: its velocity e-folds in about a sixth of one. */
private const val FLING_FRICTION = 6f

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
 * delta is left to us and one line's worth of it is enough. Only a vertical gesture counts: while
 * the reader is panning sideways it must never leave reading.
 */
@Composable
private fun leaveAtBottom(list: LazyListState, lineHeightPx: Int, panning: MutableState<Boolean>, onLeave: () -> Unit): NestedScrollConnection {
    val leave = rememberUpdatedState(onLeave)
    val threshold = lineHeightPx.coerceAtLeast(1).toFloat()
    val past = remember { Past() }
    return remember(list, threshold) {
        object : NestedScrollConnection {
            override fun onPostScroll(consumed: Offset, available: Offset, source: NestedScrollSource): Offset {
                if (available.y < 0f && !list.canScrollBackward && !panning.value) {
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
