package io.github.lewin671.remoteagent.ui

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class FollowTest {
    @Test
    fun onlyTheUserStopsFollowing() {
        val f = Follow()
        // Opening a long history: the app's own jump ends before the list
        // reaches the bottom. That must not stop following.
        f.observe(userDragging = false, atBottom = false)
        assertTrue(f.on)
        f.observe(userDragging = true, atBottom = false)
        assertFalse(f.on)
        // More messages arrive while scrolled up: still not following.
        f.observe(userDragging = false, atBottom = false)
        assertFalse(f.on)
        f.observe(userDragging = false, atBottom = true)
        assertTrue(f.on)
    }
}
