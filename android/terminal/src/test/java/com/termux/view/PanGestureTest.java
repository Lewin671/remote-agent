package com.termux.view;

import org.junit.Test;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

/** A gesture's axis: sideways panning, or the vertical pull that scrolls and opens the reader. */
public class PanGestureTest {
    @Test public void sidewaysDragsPanAndVerticalDragsScroll() {
        assertTrue(TerminalView.horizontalDrag(40f, 5f));
        assertTrue(TerminalView.horizontalDrag(-30f, 12f));
        assertFalse(TerminalView.horizontalDrag(5f, 40f));
        assertFalse(TerminalView.horizontalDrag(-12f, 30f));
        // A tie keeps the vertical meaning, so the pull into reading still works.
        assertFalse(TerminalView.horizontalDrag(20f, 20f));
        assertFalse(TerminalView.horizontalDrag(0f, 0f));
    }

    /** A panned terminal: touches are viewport x, cells are content columns, and vice versa. */
    @Test public void touchColumnsAccountForThePanOffset() {
        // Panned 100px with 10px cells, a touch at the left edge is content column 10.
        assertEquals(10, TerminalView.columnAt(0f, 100, 10f));
        assertEquals(15, TerminalView.columnAt(50f, 100, 10f));
        // Not panned: unchanged.
        assertEquals(5, TerminalView.columnAt(50f, 0, 10f));
        // The inverse: a column sits further left as the terminal is panned right.
        assertEquals(-100, TerminalView.viewportX(0, 100, 10f));
        assertEquals(0, TerminalView.viewportX(10, 100, 10f));
        assertEquals(150, TerminalView.viewportX(15, 0, 10f));
    }

    /** The pan never parks past the end of the grid, however the grid width changes. */
    @Test public void thePanStaysInsideTheTerminal() {
        assertEquals(0, TerminalView.clampedPan(-5, 200));
        assertEquals(200, TerminalView.clampedPan(500, 200));
        assertEquals(120, TerminalView.clampedPan(120, 200));
        // A grid that shrank to fit (or is narrower than the view): the only offset is zero.
        assertEquals(0, TerminalView.clampedPan(300, 0));
        assertEquals(0, TerminalView.clampedPan(300, -10));
    }

    /** A wheel pans sideways by its own axis; a vertical one is left for the rows. */
    @Test public void aSidewaysWheelPansAndAVerticalOneDoesNot() {
        // A sideways wheel pans by its own axis value, either way.
        assertEquals(1f, TerminalView.horizontalWheel(1f, 0f, false), 0.001f);
        assertEquals(-2f, TerminalView.horizontalWheel(-2f, 0f, false), 0.001f);
        // A vertical wheel is not a pan: the rows take it, as before.
        assertEquals(0f, TerminalView.horizontalWheel(0f, 1f, false), 0.001f);
        assertEquals(0f, TerminalView.horizontalWheel(0f, -3f, false), 0.001f);
        // A diagonal goes by its dominant axis, so a little cross-axis drift cannot steal it.
        assertEquals(5f, TerminalView.horizontalWheel(5f, 2f, false), 0.001f);
        assertEquals(0f, TerminalView.horizontalWheel(2f, 5f, false), 0.001f);
        // Shift+vertical is sideways, the way the platform's own scroll views take it.
        assertEquals(-1f, TerminalView.horizontalWheel(0f, 1f, true), 0.001f);
        assertEquals(2f, TerminalView.horizontalWheel(0f, -2f, true), 0.001f);
        // Shift with no wheel movement stays no pan.
        assertEquals(0f, TerminalView.horizontalWheel(0f, 0f, true), 0.001f);
    }
}
