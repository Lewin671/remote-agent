package com.termux.view;

import org.junit.Test;
import static org.junit.Assert.assertEquals;

/** One continuous wheel or trackpad scroll keeps its first axis until it pauses. */
public class WheelAxisTest {
    @Test public void keepsAScrollsFirstAxisUntilItPauses() {
        WheelAxis axis = new WheelAxis(200);
        assertEquals(WheelAxis.Axis.HORIZONTAL, axis.axis(30f, -2f, 0));
        // Drifting up mid-pan stays a pan.
        assertEquals(WheelAxis.Axis.HORIZONTAL, axis.axis(1f, -20f, 16));
        assertEquals(WheelAxis.Axis.HORIZONTAL, axis.axis(0f, -40f, 150));
        // After a pause a new scroll decides afresh.
        assertEquals(WheelAxis.Axis.VERTICAL, axis.axis(0f, -40f, 400));
    }

    @Test public void waitsForAnEventThatMoves() {
        WheelAxis axis = new WheelAxis();
        assertEquals(WheelAxis.Axis.NONE, axis.axis(0f, 0f, 0));
        assertEquals(WheelAxis.Axis.VERTICAL, axis.axis(0f, 5f, 10));
    }

    /** The dominant axis decides; a tie keeps the vertical meaning the pull into reading needs. */
    @Test public void theDominantAxisDecidesAndATieIsVertical() {
        assertEquals(WheelAxis.Axis.HORIZONTAL, new WheelAxis().axis(40f, 5f, 0));
        assertEquals(WheelAxis.Axis.HORIZONTAL, new WheelAxis().axis(-30f, 12f, 0));
        assertEquals(WheelAxis.Axis.VERTICAL, new WheelAxis().axis(5f, 40f, 0));
        assertEquals(WheelAxis.Axis.VERTICAL, new WheelAxis().axis(20f, 20f, 0));
    }
}
