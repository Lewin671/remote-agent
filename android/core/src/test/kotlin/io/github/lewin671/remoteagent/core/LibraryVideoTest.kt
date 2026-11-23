package io.github.lewin671.remoteagent.core

import org.junit.Assert.assertEquals
import org.junit.Test

/** Local video thumbnail duration formatting for picked videos. */
class LibraryVideoTest {
    @Test fun lengthsReadLikeAClock() {
        assertEquals("0:00", videoLength(0))
        assertEquals("0:42", videoLength(42_300))
        assertEquals("12:05", videoLength(725_000))
        assertEquals("1:02:03", videoLength(3_723_000))
    }
}
