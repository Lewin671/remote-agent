package io.github.lewin671.remoteagent.term

import org.junit.Assert.*
import org.junit.Test

class TerminalGeometryTest {
    @Test fun capacityAccountsForRoundedRowsAndInitialBaseline() {
        val g = TerminalGeometry(18.5f, 45, 8)
        val size = g.capacity(1059, 1673)
        assertEquals(57 to 37, size)
        assertTrue(g.contains(1059, 1673, size.first, size.second))
        assertFalse(g.contains(1059, 1673, size.first + 1, size.second))
        assertFalse(g.contains(1059, 1673, size.first, size.second + 1))
    }
    @Test fun keyboardSizedViewportDoesNotProduceNegativeCapacity() {
        assertEquals(0 to 0, TerminalGeometry(19f, 40, 8).capacity(-1, 3))
    }
}
