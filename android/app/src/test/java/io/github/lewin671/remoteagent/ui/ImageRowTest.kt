package io.github.lewin671.remoteagent.ui

import org.junit.Assert.*
import org.junit.Test

class ImageRowTest {
    @Test fun fourLandscapeImagesFitInAPhoneWithoutClippingTheLastOne() {
        val row = imageRowGeometry(List(4) { 1.5f }, 360, 88, 180, 6)
        assertTrue(row.widths.sum() + row.gap * 3 <= 360)
        assertEquals(4, row.widths.size)
        assertTrue(row.height < 88)
        assertTrue(row.widths.all { kotlin.math.abs(it - row.height * 1.5f) <= 2 })
        val single = imageRowGeometry(listOf(1.5f), 360, 88, 180, 6)
        assertEquals(listOf(132), single.widths)
        assertEquals(88, single.height)
    }
}
