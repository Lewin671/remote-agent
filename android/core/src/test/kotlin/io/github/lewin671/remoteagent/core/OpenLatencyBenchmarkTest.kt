package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.BeforeClass
import kotlin.time.Duration.Companion.seconds
import org.junit.Test

/** Asserts the cold-open dependency count with 50 ms virtual I/O, without a wall-clock benchmark. */
@OptIn(ExperimentalCoroutinesApi::class)
class OpenLatencyBenchmarkTest {
    @get:org.junit.Rule val hangGuard = hangGuard()
    companion object {
        private lateinit var fixture: ColdOpenFixture
        @JvmStatic @BeforeClass fun signOnce() { fixture = ColdOpenFixture() }
    }

    @Test fun newestVisibleAfterColdEngineStart() = runTest(timeout = 5.seconds) {
        val peer = ColdOpenPeer(fixture, this).apply { latencyMs = 50 }
        val r = peer.remote()
        try {
            val win = io.github.lewin671.remoteagent.core.proto.SessionWindowRequest.newBuilder().setLimit(500).build()
            val visible = async { r.watchSession("long", win).first { it.timeline.windowLastSeq == 501L } }
            r.start()
            val page = withTimeout(15000) { visible.await() }
            assertEquals(500, page.timeline.itemsCount)
            assertEquals(listOf(1L), peer.after)
            assertEquals(0, peer.calls.count { it == "Login" || it == "GetChallenge" })
            // Upgrade, parallel lists/security, then newest history: three dependent 50 ms waits.
            assertEquals(150L, testScheduler.currentTime)
        } finally { r.close(); runCurrent() }
    }
}
