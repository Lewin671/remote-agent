package io.github.lewin671.remoteagent.ui

import android.content.Context
import android.database.ContentObserver
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.view.HapticFeedbackConstants
import android.view.View
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.FiniteAnimationSpec
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.shrinkVertically
import androidx.compose.animation.slideInVertically
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment

/**
 * Motion (design-system §4 动效): three durations and one curve, used everywhere something comes,
 * goes or changes size; nothing else is animated.
 *
 * - [FAST] 120 ms: press feedback, colour and icon changes, a line or tool row fading in
 * - [BASE] 200 ms: an element appearing or going, a height change
 * - [SLOW] 280 ms: a whole screen coming or going (sheet, page)
 *
 * Coming and changing use [Decelerate]; going takes 0.8 of the same duration and [Accelerate], so it
 * leaves quicker than it came. No overshoot, no bounce, nothing moves more than [RISE_DP] dp, and
 * nothing scales below [PRESS_SCALE].
 *
 * With the system's animations removed ([reduced]: the animator duration scale is 0) or reduced,
 * every move, scale and height change is dropped and only fades of at most [FAST] ms are left;
 * the system itself then plays no animation at all, which is stricter and takes precedence.
 */
object Motion {
    const val FAST = 120
    const val BASE = 200
    const val SLOW = 280

    /** The one curve of §4: cubic-bezier(.2, .8, .2, 1). */
    val Decelerate = CubicBezierEasing(.2f, .8f, .2f, 1f)

    /** For what goes: ease-in. */
    val Accelerate = CubicBezierEasing(.42f, 0f, 1f, 1f)

    /** How far a new bubble, the dock or "回到最新" rises while it fades in, in dp (the most §4 allows is 12). */
    const val BUBBLE_RISE_DP = 8
    const val DOCK_RISE_DP = 12

    /** The smallest scale a pressed button shrinks to. */
    const val PRESS_SCALE = 0.96f

    /** The system's animations are off or reduced: no motion, only short fades. */
    var reduced by mutableStateOf(false)
        private set

    /** What a fade lasts: at most [FAST] when [reduced]. */
    private fun fadeMs(ms: Int) = if (reduced) minOf(ms, FAST) else ms

    /** Coming or changing, [ms] long. Moves and sizes snap when [reduced]. */
    fun <T> enter(ms: Int = BASE): FiniteAnimationSpec<T> = if (reduced) snap() else tween(ms, easing = Decelerate)

    /** Going: 0.8 of [ms], ease-in. */
    fun <T> exit(ms: Int = BASE): FiniteAnimationSpec<T> = if (reduced) snap() else tween((ms * 0.8f).toInt(), easing = Accelerate)

    /** A fade in: a fade alone is kept when [reduced], shorter if need be. */
    fun fadeInSpec(ms: Int = BASE): FiniteAnimationSpec<Float> = tween(fadeMs(ms), easing = Decelerate)

    /** A fade out. */
    fun fadeOutSpec(ms: Int = BASE): FiniteAnimationSpec<Float> = tween(fadeMs((ms * 0.8f).toInt()), easing = Accelerate)

    /** Something coming in: it fades in and, unless [reduced], rises [riseDp] and opens its height. */
    fun appear(ms: Int = BASE, riseDp: Int = 0, grow: Boolean = false): EnterTransition {
        var t: EnterTransition = fadeIn(fadeInSpec(ms))
        if (!reduced && riseDp > 0) t += slideInVertically(tween(ms, easing = Decelerate)) { (riseDp * pixel).toInt() }
        if (!reduced && grow) t += expandVertically(tween(ms, easing = Decelerate), Alignment.Top)
        return t
    }

    /** Something going: it fades out and, unless [reduced], closes its height. */
    fun vanish(ms: Int = BASE, shrink: Boolean = true): ExitTransition {
        var t: ExitTransition = fadeOut(fadeOutSpec(ms))
        if (!reduced && shrink) t += shrinkVertically(tween((ms * 0.8f).toInt(), easing = Accelerate), Alignment.Top)
        return t
    }

    // Pixels in a dp, to turn a rise into pixels where a transition is built outside a composition.
    private var pixel = 3f

    /** Follows the system's animator duration scale, and the screen's density the rise is measured in. */
    fun observe(context: Context) {
        pixel = context.resources.displayMetrics.density
        val resolver = context.contentResolver
        fun read() {
            reduced = Settings.Global.getFloat(resolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) == 0f
        }
        read()
        resolver.registerContentObserver(
            Settings.Global.getUriFor(Settings.Global.ANIMATOR_DURATION_SCALE), false,
            object : ContentObserver(Handler(Looper.getMainLooper())) { override fun onChange(selfChange: Boolean) = read() },
        )
    }

    /**
     * The light tap of §4 触感: a message going out, an approval allowed or denied. Nothing else vibrates.
     * The system's own touch-feedback setting still decides whether it is felt.
     */
    fun tap(view: View) {
        view.performHapticFeedback(HapticFeedbackConstants.CONTEXT_CLICK)
    }
}

/**
 * [androidx.compose.animation.AnimatedVisibility] with the transitions of [Motion]; a function of its own so
 * that it is the one for no scope, whatever [androidx.compose.foundation.layout.ColumnScope] or
 * [androidx.compose.foundation.layout.BoxScope] it is called from.
 */
@androidx.compose.runtime.Composable
fun Reveal(
    visible: Boolean,
    modifier: androidx.compose.ui.Modifier = androidx.compose.ui.Modifier,
    enter: EnterTransition = Motion.appear(),
    exit: ExitTransition = Motion.vanish(),
    content: @androidx.compose.runtime.Composable () -> Unit,
) {
    androidx.compose.animation.AnimatedVisibility(visible, modifier, enter, exit) { content() }
}
