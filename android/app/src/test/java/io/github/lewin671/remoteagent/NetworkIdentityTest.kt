package io.github.lewin671.remoteagent

import org.junit.Assert.*
import org.junit.Test

class NetworkIdentityTest {
    @Test fun addressRouteAndCapabilityChangesAreNotHiddenByTheSameHandle() {
        fun snapshot(address: String = "192.0.2.1/24", route: String = "default", caps: List<Int> = listOf(1,2)) =
            networkIdentity(7,"wlan0",listOf(address),listOf(route),listOf("192.0.2.53"),listOf(1),caps)
        assertEquals(snapshot(),snapshot(caps=listOf(2,1)))
        assertNotEquals(snapshot(),snapshot(address="192.0.2.2/24"))
        assertNotEquals(snapshot(),snapshot(route="another-default"))
        assertNotEquals(snapshot(),snapshot(caps=listOf(1)))
    }
}
