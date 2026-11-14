package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.transport.Recovery
import io.github.lewin671.remoteagent.core.transport.RecoveryBudget
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class RecoveryLifecycleTest {
    @Test fun schedulerGapTouchesSecurityAndReplacesTheNetworkGeneration() = runTest {
        val fixture = ColdOpenFixture(2)
        val peer = ColdOpenPeer(fixture, this)
        val base = TestSecurityStore(fixture.record)
        var touches = 0
        val storage = object : DeviceSecurity.Storage by base {
            override fun touchSecurity(expectedState: ByteArray, floor: ULong): DeviceSecurity.Record {
                touches++; return base.touchSecurity(expectedState,floor)
            }
        }
        var gap = 0L
        val monotonic = { testScheduler.currentTime + gap }
        val remote = Remote(fixture.account,Api.defaultHttp(),
            clock={fixture.now + testScheduler.currentTime}, monotonic=monotonic,
            securityStorage=storage, tokenStorage=peer.saved(), rpcTransport=peer,socketFactory=peer,
            dispatcher=StandardTestDispatcher(testScheduler))
        Remote::class.java.getDeclaredField("recovery").apply { isAccessible=true }.set(remote,Recovery(monotonic,{.5},RecoveryBudget()))
        fun generation()=Remote::class.java.getDeclaredField("networkGeneration").apply {isAccessible=true}.getLong(remote)
        try {
            remote.start();runCurrent();assertEquals(ConnState.ONLINE,remote.state.value.conn)
            val before=touches;val gen=generation()
            gap=10_000
            advanceTimeBy(1001);runCurrent()
            assertTrue(touches>before);assertTrue(generation()>gen)
            assertEquals(ConnState.ONLINE,remote.state.value.conn)
            assertTrue(peer.calls.contains("GetSecurityStates"))
        } finally {remote.close();runCurrent()}
    }
}
