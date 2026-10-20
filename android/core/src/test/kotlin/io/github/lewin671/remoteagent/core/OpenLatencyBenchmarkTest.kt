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
    companion object {
        private lateinit var fixture: ColdOpenFixture
        @JvmStatic @BeforeClass fun signOnce() { fixture = ColdOpenFixture() }
    }

    @Test fun newestVisibleAfterColdEngineStart() = runTest(timeout = 5.seconds) {
        val peer = ColdOpenPeer(fixture, this).apply { latencyMs = 50 }
        val r = peer.remote()
        try {
            val visible = async { r.events("long").first { it.events.lastOrNull()?.seq == 501L } }
            r.start()
            val page = withTimeout(15000) { visible.await() }
            assertEquals(500, page.events.size)
            assertEquals(listOf(1L), peer.after)
            assertEquals(0, peer.calls.count { it == "Login" || it == "GetChallenge" })
            // Upgrade, parallel lists, devices, then newest history: four dependent 50 ms waits.
            assertEquals(200L, testScheduler.currentTime)
        } finally { r.close(); runCurrent() }
    }
}
