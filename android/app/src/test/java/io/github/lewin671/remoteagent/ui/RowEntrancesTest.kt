package io.github.lewin671.remoteagent.ui

import android.app.Application
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class RowEntrancesTest {
    @Test fun acceptingLocalMessageDoesNotCollapseItsReplacementQueuedBubble() {
        val entrances = RowEntrances()
        entrances.outgoing(emptyList())
        entrances.outgoing(listOf("outrequest"))
        assertEquals(Entrance.RISE, entrances.take("outrequest"))
        entrances.outgoing(listOf("pending-request"))
        assertNull(entrances.take("pending-request"))
        entrances.outgoing(listOf("pending-request", "pending-another"))
        assertEquals(Entrance.RISE, entrances.take("pending-another"))
    }
}
