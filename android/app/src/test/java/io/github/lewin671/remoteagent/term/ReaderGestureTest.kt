package io.github.lewin671.remoteagent.term

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** The reader's start-of-gesture axis rule and the width it lays its content out at. */
class ReaderGestureTest {
    @Test fun aDragIsOnItsDominantAxis() {
        assertEquals(DragAxis.Horizontal, dragAxis(40f, 5f))
        assertEquals(DragAxis.Horizontal, dragAxis(-30f, 12f))
        assertEquals(DragAxis.Vertical, dragAxis(5f, 40f))
        assertEquals(DragAxis.Vertical, dragAxis(-12f, 30f))
        // A tie keeps the vertical meaning, exactly as the live terminal decides it.
        assertEquals(DragAxis.Vertical, dragAxis(20f, 20f))
        assertEquals(DragAxis.Vertical, dragAxis(0f, 0f))
    }

    @Test fun theReaderIsAtLeastAsWideAsTheScreen() {
        // A grid narrower than the screen still fills the width, so a drag in the blank area works.
        assertEquals(1080, readerWidth(1080, 900))
        assertEquals(1080, readerWidth(1080, 1080))
        // A grid wider than the screen is laid out whole; the extra is panned.
        assertEquals(2200, readerWidth(1080, 2200))
    }

    @Test fun theReadersMaxOffsetMatchesTheLiveViewsMaxPan() {
        val viewportWidthPx = 1080
        val cols = 165
        val cellWidth = 9.9f
        // The live view pans by its grid width minus its viewport width (TerminalView.maxPan).
        val liveMaxPan = (Math.round(cols * cellWidth) - viewportWidthPx).coerceAtLeast(0)
        // The reader is handed that same grid width (TerminalView.getGridWidth) and derives the same.
        val grid = gridWidthPx(cols, cellWidth)
        assertEquals(liveMaxPan, maxPanPx(viewportWidthPx, grid))
        // And its content is the whole grid, so the right edge stops in the same place.
        assertEquals(grid, readerWidth(viewportWidthPx, grid))
        // A grid that fits (or is narrower) never pans in either.
        assertEquals(0, maxPanPx(1080, gridWidthPx(60, cellWidth)))
    }

    @Test fun theGridStartsAtTheLeftEdgeAndPansToTheRightEdge() {
        val viewportWidthPx = 1080
        val cols = 165
        val cellWidth = 9.9f
        val maxOffset = maxPanPx(viewportWidthPx, gridWidthPx(cols, cellWidth))
        // At offset 0 column 0 is at the viewport's left edge (the grid is start-aligned, not
        // centered, so nothing is hidden off the left).
        assertEquals(0, gridColumnX(0, 0, cellWidth))
        // At the maximum offset the grid's right edge (past the last column) is at the right edge.
        assertEquals(viewportWidthPx, gridColumnX(cols, maxOffset, cellWidth))
    }

    @Test fun aLongPressOrAFittedGridKeepsTheDragForSelection() {
        val timeout = 400L
        // A horizontal drag with somewhere to pan, started before a long press: ours to pan.
        assertTrue(claimsPan(DragAxis.Horizontal, 554f, 100L, timeout))
        // Held past the long press before moving: the selection's gesture, not ours.
        assertFalse(claimsPan(DragAxis.Horizontal, 554f, 500L, timeout))
        assertFalse(claimsPan(DragAxis.Horizontal, 554f, timeout, timeout))
        // A grid that fits has nowhere to pan, so a sideways drag is never taken.
        assertFalse(claimsPan(DragAxis.Horizontal, 0f, 100L, timeout))
        // Vertical drags belong to the list.
        assertFalse(claimsPan(DragAxis.Vertical, 554f, 100L, timeout))
    }

    @Test fun theExtentCoversACaptureWiderThanAShrunkenLiveGrid() {
        // A wide-character line counts its cells the way the reader draws them.
        assertEquals(3, lineCells(listOf(SgrRun("a中"))))
        // Captured when the terminal was 20 cells wide; the live grid has since shrunk to 12.
        val captured = widestLineCells(listOf(TerminalHistory.Page("p", listOf(listOf(SgrRun("x".repeat(20)))))))
        assertEquals(20, captured)
        val shrunkenGrid = gridWidthPx(12, 10f)
        // The live grid is only a minimum: the extent keeps the captured 20 cells reachable.
        assertEquals(200, readerExtentPx(shrunkenGrid, captured, 10f))
        // So the offset follows the captured extent, not the shrunken grid, and the lines stay whole.
        assertEquals(100, maxPanPx(100, readerExtentPx(shrunkenGrid, captured, 10f)))
        // A live grid wider than the capture still wins.
        assertEquals(300, readerExtentPx(gridWidthPx(30, 10f), captured, 10f))
    }

    @Test fun aFlingStopsAtTheBoundOfEveryFrame() {
        // At the end, the next frame clamps to the bound and leaves no speed.
        assertEquals(500f to 0f, flingStep(490f, 1000f, 0.016f, 500f))
        assertEquals(0f to 0f, flingStep(5f, -1000f, 0.016f, 500f))
        // A bound that shrank below the offset clamps it right away.
        assertEquals(120f to 0f, flingStep(300f, 900f, 0.016f, 120f))
        // Inside, it moves a frame's worth and keeps most of its speed.
        val (next, remaining) = flingStep(400f, 1000f, 0.016f, 500f)
        assertEquals(416.0, next.toDouble(), 0.01)
        assertTrue(remaining in 800f..1000f)
    }

    @Test fun aWheelPansTheReaderWithinItsBounds() {
        val perUnit = 64f
        // A wheel towards the right end moves the content as a drag does, by its own amount.
        assertEquals(128.0, readerWheelOffset(0f, 2f, perUnit, 500f).toDouble(), 0.001)
        assertEquals(32.0, readerWheelOffset(64f, -0.5f, perUnit, 500f).toDouble(), 0.001)
        // It never runs past either end of the pan.
        assertEquals(0.0, readerWheelOffset(0f, -3f, perUnit, 500f).toDouble(), 0.001)
        assertEquals(500.0, readerWheelOffset(480f, 5f, perUnit, 500f).toDouble(), 0.001)
        // A fitted grid has nowhere to pan: the offset stays put.
        assertEquals(0.0, readerWheelOffset(0f, 4f, perUnit, 0f).toDouble(), 0.001)
    }

    @Test fun theScrollbarThumbTracksTheOffsetAndStopsAtBothEnds() {
        val viewport = 1000
        val extent = 2000
        val track = 400
        // Half the content is in sight: the thumb is half the track, at the start.
        assertEquals(0f to 200f, scrollThumb(0, viewport, extent, track))
        // At the very end the thumb's right edge is the track's own.
        val (leftEnd, widthEnd) = scrollThumb(extent - viewport, viewport, extent, track)
        assertEquals(200.0, leftEnd.toDouble(), 0.001)
        assertEquals(400.0, (leftEnd + widthEnd).toDouble(), 0.001)
        // Half way: it sits in the middle of the track.
        assertEquals(100.0, scrollThumb(500, viewport, extent, track).first.toDouble(), 0.001)
        // A fitted extent fills the whole track (never drawn, but consistent).
        assertEquals(0f to 400f, scrollThumb(0, viewport, viewport, track))
        // A collapsed track draws nothing.
        assertEquals(0f to 0f, scrollThumb(0, viewport, extent, 0))
    }
}
