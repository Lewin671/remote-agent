package io.github.lewin671.remoteagent.term

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class KeysTest {
    @Test
    fun arrowsFollowTheCursorKeyMode() {
        val left = keyRows[0].first { it.label == "左" }
        val right = keyRows[0].first { it.label == "右" }
        assertEquals("\u001b[D", keySequence(left, appCursor = false))
        assertEquals("\u001bOD", keySequence(left, appCursor = true))
        assertEquals("\u001b[C", keySequence(right, appCursor = false))
    }

    @Test
    fun theFirstRowHasEscArrowsAndEnter() {
        assertEquals(listOf("Esc", "左", "上", "下", "右", "回车"), keyRows[0].map { it.label })
        assertEquals(listOf("Tab", "Ctrl+C"), keyRows[1].map { it.label })
    }

    @Test
    fun fittedOnlyWhileTheWindowKeepsTheRequestedSize() {
        val f = FitTracker()
        assertFalse(f.onSnapshot(120 to 40))
        f.request(42 to 30)
        assertTrue(f.fitted)
        // The old size still in flight does not undo the request.
        assertFalse(f.onSnapshot(120 to 40))
        assertTrue(f.fitted)
        assertFalse(f.onSnapshot(42 to 30))
        assertTrue(f.fitted)
        // Someone else resized it: no longer fitted, and it says so.
        assertTrue(f.onSnapshot(80 to 30))
        assertFalse(f.fitted)
    }

    @Test
    fun releasingIsNotAChangeFromElsewhere() {
        val f = FitTracker()
        f.onSnapshot(120 to 40)
        f.request(42 to 30)
        f.onSnapshot(42 to 30)
        f.release()
        assertFalse(f.fitted)
        assertFalse(f.onSnapshot(120 to 40))
        assertTrue(f.onSnapshot(100 to 40))
    }
}
