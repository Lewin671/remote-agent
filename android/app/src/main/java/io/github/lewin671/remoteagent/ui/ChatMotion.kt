package io.github.lewin671.remoteagent.ui

import android.os.SystemClock
import androidx.compose.animation.core.Animatable
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.layout
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.unit.dp
import kotlin.math.roundToInt

/** How a row that has just come into the chat arrives (design-system §4 动效). */
internal enum class Entrance {
    /** A bubble: it rises 8 dp, fades in and opens its height (200 ms). */
    RISE,

    /** A tool row, a line of the turn's time: it only fades in (120 ms). */
    FADE,

    /** The message the chat shows in place of the one this device just sent: no entrance, its status line closes. */
    SETTLE,
}

/**
 * Which rows of a chat are new at this moment, and so play their entrance. Everything else must
 * not: the history of a session just opened, history joining in front, rows scrolled into view, a
 * row that is built again. A row is new when it comes into the list at its end after the list was
 * first shown; its entrance is kept for [LIFE_MS] only, so that a row composed later, when the
 * reader scrolls to it, does not play it.
 */
internal class RowEntrances {
    private val known = HashSet<String>()
    private val outKnown = HashSet<String>()
    private val fresh = HashMap<String, Pair<Entrance, Long>>()
    private var armed = false
    private var outArmed = false

    /** When a message was last sent from this device, for telling its replacement from a new message. */
    private var sentAt = 0L

    /** The rows of the chat now; [joined]: rows came in front, which are history, not new. */
    fun rows(items: List<ChatItem>, joined: Boolean) {
        val now = SystemClock.uptimeMillis()
        val added = items.filter { known.add(it.key) }
        if (!armed) {
            armed = true
            return
        }
        // A catch-up of many rows at once is not a row arriving.
        if (joined || added.size > MOST_AT_ONCE) return
        for (item in added) {
            val kind = when (item) {
                is ChatItem.User ->
                    if (item.from != null) Entrance.RISE
                    else if (now - sentAt < SENT_MS) Entrance.SETTLE
                    else null
                is ChatItem.Tool, is ChatItem.Tools, is ChatItem.Running, is ChatItem.Note, is ChatItem.TurnEnd, is ChatItem.Generated -> Entrance.FADE
                else -> null
            }
            if (kind != null) fresh[item.key] = kind to now
        }
    }

    /** The keys of what is under the chat now (messages from this device, queued messages); new ones rise. */
    fun outgoing(keys: Collection<String>) {
        val now = SystemClock.uptimeMillis()
        if (!outArmed) {
            outArmed = true
            outKnown += keys
            return
        }
        for (k in keys) if (outKnown.add(k)) {
            fresh[k] = Entrance.RISE to now
            if (k.startsWith("out")) sentAt = now
        }
    }

    /** Takes the entrance of a row coming into composition, once. */
    fun take(key: String): Entrance? {
        val e = fresh.remove(key) ?: return null
        return if (SystemClock.uptimeMillis() - e.second < LIFE_MS) e.first else null
    }

    private companion object {
        const val LIFE_MS = 600L
        const val SENT_MS = 15_000L
        const val MOST_AT_ONCE = 4
    }
}

/**
 * A row's entrance, played once when it is composed: [Entrance.RISE] fades in, rises
 * [Motion.BUBBLE_RISE_DP] and opens its height, the list following the bottom meanwhile
 * ([pinBottom]); [Entrance.FADE] fades in. With animations reduced only the fade is left.
 */
@Composable
internal fun Modifier.entrance(kind: Entrance?): Modifier {
    if (kind != Entrance.RISE && kind != Entrance.FADE) return this
    val progress = remember { Animatable(0f) }
    val rise = kind == Entrance.RISE
    val spec = if (rise) Motion.fadeInSpec(Motion.BASE) else Motion.fadeInSpec(Motion.FAST)
    LaunchedEffect(Unit) {
        progress.animateTo(1f, if (rise && !Motion.reduced) Motion.enter(Motion.BASE) else spec)
    }
    val px = with(LocalDensity.current) { Motion.BUBBLE_RISE_DP.dp.toPx() }
    val move = rise && !Motion.reduced
    return this
        .graphicsLayer {
            alpha = progress.value
            if (move) translationY = (1f - progress.value) * px
        }
        .then(
            if (!move) Modifier
            else Modifier.layout { measurable, constraints ->
                val placeable = measurable.measure(constraints)
                layout(placeable.width, (placeable.height * progress.value).roundToInt()) { placeable.place(0, 0) }
            }.clipToBounds(),
        )
}

/**
 * Keeps a list that follows its end at its end while the end moves, frame by frame: a bubble
 * opening, the message box growing, the dock coming. A scroll asked for after the layout has changed
 * would show the end a frame late; asked at the start of each frame, it is applied by the same
 * frame's layout. Ends when the reader takes the list, or after [settleFrames] frames in which
 * nothing moved.
 */
internal suspend fun LazyListState.pinBottom(index: () -> Int, following: () -> Boolean, moved: () -> Int, settleFrames: Int = 4) {
    var last = moved()
    var still = 0
    while (still < settleFrames && following()) {
        requestScrollToItem(index())
        withFrameNanos { }
        val now = moved()
        still = if (now == last) still + 1 else 0
        last = now
    }
}

/** A number that changes whenever what the list shows grows, shrinks or moves: for telling when the end has settled. */
internal fun LazyListState.moved(): Int = layoutInfo.viewportSize.height + layoutInfo.visibleItemsInfo.sumOf { it.size }
