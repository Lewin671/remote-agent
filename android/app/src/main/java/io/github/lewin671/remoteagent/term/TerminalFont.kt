package io.github.lewin671.remoteagent.term

import android.graphics.Paint
import android.graphics.Typeface
import kotlin.math.ceil
import kotlin.math.floor

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

/** Keep the remote grid intact; only the explicit fit action changes its size. */
internal fun terminalFontPixels(typeface: Typeface, preferred: Int, width: Int, height: Int, cols: Int, rows: Int): Int {
    if (cols <= 0 || rows <= 0) return preferred
    return (preferred downTo 5).firstOrNull {
        terminalGeometry(typeface, it).contains(width, height, cols, rows)
    } ?: 5
}
