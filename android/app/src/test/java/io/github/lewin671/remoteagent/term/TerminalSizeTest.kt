package io.github.lewin671.remoteagent.term

import org.junit.Assert.assertEquals
import org.junit.Test

/** The 字号 range and the floor a terminal wider than the screen shrinks to. */
class TerminalSizeTest {
    @Test fun theFontSizeRangeIsSixToTwentyTwo() {
        assertEquals(14, FONT_DEFAULT_DP)
        assertEquals(FONT_MIN_DP, clampFontDp(6))
        // Below and above the range snap to its ends; the default stays where it is.
        assertEquals(FONT_MIN_DP, clampFontDp(3))
        assertEquals(FONT_MIN_DP, clampFontDp(0))
        assertEquals(FONT_MAX_DP, clampFontDp(22))
        assertEquals(FONT_MAX_DP, clampFontDp(40))
        assertEquals(14, clampFontDp(14))
    }

    @Test fun theLargestFontThatFitsIsChosen() {
        // Down from the preferred size, the first that fits.
        assertEquals(42, fitFontPixels(preferred = 42, minPx = 18) { true })
        assertEquals(30, fitFontPixels(preferred = 42, minPx = 18) { it <= 30 })
    }

    @Test fun theFontNeverGoesBelowTheMinimum() {
        // Nothing fits down to the floor: it keeps the floor, and the terminal is panned instead.
        assertEquals(18, fitFontPixels(preferred = 42, minPx = 18) { false })
        assertEquals(18, fitFontPixels(preferred = 42, minPx = 18) { it <= 5 })
        // The floor itself is the answer when it is the only size that fits.
        assertEquals(18, fitFontPixels(preferred = 42, minPx = 18) { it <= 18 })
    }
}
