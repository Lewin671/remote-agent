package io.github.lewin671.remoteagent.term

import android.content.Context
import android.graphics.Paint
import android.graphics.Typeface
import android.graphics.fonts.Font
import android.graphics.fonts.FontFamily
import android.os.Build
import androidx.core.content.res.ResourcesCompat
import io.github.lewin671.remoteagent.R
import kotlin.math.ceil
import kotlin.math.floor

/**
 * JetBrains Mono, then the bundled Sarasa symbols for what it lacks (⏵ ⎿ ✻, Braille spinners),
 * then the system fonts, which supply Chinese. Before Android 10 a typeface cannot name its own
 * fallback, so symbols missing from the system fonts stay missing there.
 */
internal fun terminalTypeface(context: Context): Typeface {
    if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) return requireNotNull(ResourcesCompat.getFont(context, R.font.jetbrains_mono_regular))
    fun family(id: Int) = FontFamily.Builder(Font.Builder(context.resources, id).build()).build()
    return Typeface.CustomFallbackBuilder(family(R.font.jetbrains_mono_regular))
        .addCustomFallback(family(R.font.sarasa_fixed_sc_symbols))
        .setSystemFallback("monospace")
        .build()
}

/** Same pixel rounding and initial baseline offset as Termux's TerminalRenderer. */
internal data class TerminalGeometry(val cellWidth: Float, val lineHeight: Int, val topOffset: Int) {
    fun capacity(width: Int, height: Int): Pair<Int, Int> =
        floor(width.coerceAtLeast(0) / cellWidth).toInt() to
            ((height - topOffset).coerceAtLeast(0) / lineHeight)

    fun contains(width: Int, height: Int, cols: Int, rows: Int): Boolean =
        cols * cellWidth <= width && rows * lineHeight + topOffset <= height
}

internal fun terminalGeometry(typeface: Typeface, pixels: Int): TerminalGeometry {
    val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply { this.typeface = typeface; textSize = pixels.toFloat() }
    val spacing = ceil(paint.fontSpacing).toInt().coerceAtLeast(1)
    return TerminalGeometry(paint.measureText("X"), spacing, spacing + ceil(paint.ascent()).toInt())
}

/**
 * Keep the remote grid intact; only the explicit fit action changes its size. A window wider than
 * the screen shrinks the font to fit, but never below [minPx]: at that size the terminal is panned
 * sideways instead (architecture §11).
 */
internal fun terminalFontPixels(typeface: Typeface, preferred: Int, width: Int, height: Int, cols: Int, rows: Int, minPx: Int): Int {
    if (cols <= 0 || rows <= 0) return preferred
    return fitFontPixels(preferred, minPx) { terminalGeometry(typeface, it).contains(width, height, cols, rows) }
}

/**
 * The largest font from [preferred] down to [minPx] that [fits], or [minPx] when even that does not
 * fit: past the floor the terminal is panned, not shrunk further.
 */
internal fun fitFontPixels(preferred: Int, minPx: Int, fits: (Int) -> Boolean): Int =
    (preferred downTo minPx).firstOrNull(fits) ?: minPx
