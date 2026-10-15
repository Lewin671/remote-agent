package com.termux.view;

import org.junit.Test;
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
}
