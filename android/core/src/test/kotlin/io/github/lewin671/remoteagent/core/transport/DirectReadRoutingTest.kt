package io.github.lewin671.remoteagent.core.transport

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Direct-first routing rules: ready -> immediate direct, 1/8 relay control sample,
 * real-sample fallback, exponential wait (60s -> 600s), and reset on rebuild/network change. */
class DirectReadRoutingTest {
    private class Sim(var now: Long = 10_000L) {
        val route = DirectRouting({ now })
        fun advance(ms: Long) { now += ms }
        fun observeReads(directN: Int, relayN: Int, directMs: Double, relayMs: Double, stepMs: Long = 500L) {
            repeat(directN) {
                route.observe(RouteHint.Read, RouteName.DIRECT, 0, directMs)
                advance(stepMs)
            }
            repeat(relayN) {
                route.observe(RouteHint.Read, RouteName.RELAY, 0, relayMs)
                advance(stepMs)
            }
        }
    }

    @Test
    fun directIsPreferredImmediatelyOnReadyWithOneEighthRelayControlSampling() {
        val sim = Sim()
        assertEquals(RouteName.DIRECT, sim.route.readPath())
        assertEquals(RouteName.DIRECT, sim.route.filePath)

        var readDirect = 0
        var readRelay = 0
        repeat(64) {
            if (sim.route.choose(RouteHint.Read, ready = true) == RouteName.DIRECT) readDirect++ else readRelay++
        }
        assertEquals(56, readDirect)
        assertEquals(8, readRelay)

        var bulkDirect = 0
        var bulkRelay = 0
        val bulkHint = RouteHint.Bulk("f")
        repeat(64) {
            if (sim.route.choose(bulkHint, ready = true) == RouteName.DIRECT) bulkDirect++ else bulkRelay++
        }
        assertEquals(56, bulkDirect)
        assertEquals(8, bulkRelay)

        assertEquals(RouteName.RELAY, sim.route.choose(RouteHint.Read, ready = false))
        assertEquals(RouteName.RELAY, sim.route.choose(bulkHint, ready = false))
    }

    @Test
    fun readFallbackRequiresWindowCountsAndMargin() {
        data class Case(val directN: Int, val relayN: Int, val directMs: Double, val relayMs: Double, val expected: RouteName)
        val cases = listOf(
            Case(7, 4, 200.0, 40.0, RouteName.DIRECT),   // <8 direct
            Case(8, 3, 200.0, 40.0, RouteName.DIRECT),   // <4 relay
            Case(8, 4, 80.0, 40.0, RouteName.DIRECT),    // 80 <= 1.5*40 + 20
            Case(8, 4, 81.0, 40.0, RouteName.RELAY),     // 81 > 80 -> fallback
        )
        for (c in cases) {
            val sim = Sim()
            sim.observeReads(c.directN, c.relayN, c.directMs, c.relayMs)
            assertEquals(c.expected, sim.route.readPath())
            assertEquals(c.expected == RouteName.RELAY, sim.route.measured().readSlow)
            assertEquals(RouteName.DIRECT, sim.route.filePath)
        }
    }

    @Test
    fun readFallbackIgnoresSamplesOlderThanThirtySeconds() {
        val sim = Sim()
        sim.observeReads(directN = 4, relayN = 4, directMs = 500.0, relayMs = 20.0)
        assertEquals(RouteName.DIRECT, sim.route.readPath())

        sim.advance(DirectRule.FALLBACK_WINDOW_MS + 1)
        sim.observeReads(directN = 4, relayN = 4, directMs = 500.0, relayMs = 20.0)
        assertEquals(RouteName.DIRECT, sim.route.readPath())

        sim.observeReads(directN = 4, relayN = 0, directMs = 500.0, relayMs = 20.0)
        assertEquals(RouteName.RELAY, sim.route.readPath())
    }

    @Test
    fun bulkSegmentFallbackTriggersBelowSeventyPercentThroughput() {
        val keepDirect = Sim()
        val hint = RouteHint.Bulk("archive.tar")
        keepDirect.route.observe(hint, RouteName.RELAY, 4_000_000, 1000.0)
        keepDirect.route.observe(hint, RouteName.DIRECT, 2_800_000, 1000.0)
        assertEquals(RouteName.DIRECT, keepDirect.route.filePath)
        assertFalse(keepDirect.route.measured().fileSlow)

        val fallback = Sim()
        fallback.route.observe(hint, RouteName.RELAY, 4_000_000, 1000.0)
        fallback.route.observe(hint, RouteName.DIRECT, 2_700_000, 1000.0)
        assertEquals(RouteName.RELAY, fallback.route.filePath)
        assertTrue(fallback.route.measured().fileSlow)
        assertEquals(RouteName.DIRECT, fallback.route.readPath())
    }

    @Test
    fun slowFallbackBacksOffExponentiallyAndResetsOnRebuildOrNetworkChange() {
        val sim = Sim()
        fun triggerSlowRead() {
            sim.observeReads(directN = 8, relayN = 3, directMs = 250.0, relayMs = 30.0, stepMs = 100L)
            sim.route.observe(RouteHint.Read, RouteName.RELAY, 0, 30.0)
        }
        fun triggerSlowBulk() {
            val f = RouteHint.Bulk("f-${sim.now}")
            sim.route.observe(f, RouteName.RELAY, 8 shl 20, 1000.0)
            sim.route.observe(f, RouteName.DIRECT, 2 shl 20, 1000.0)
        }

        for (wait in listOf(60_000L, 120_000L, 240_000L, 480_000L, 600_000L, 600_000L)) {
            triggerSlowRead()
            assertEquals(RouteName.RELAY, sim.route.readPath())
            sim.advance(wait - 1)
            assertEquals(RouteName.RELAY, sim.route.readPath())
            sim.advance(1)
            assertEquals(RouteName.DIRECT, sim.route.readPath())
        }
        // Once the wait elapses, re-preference stays DIRECT (no oscillation before new evidence).
        assertEquals(RouteName.DIRECT, sim.route.choose(RouteHint.Read, ready = true))

        // Direct read rebuild resets backoff back to 60s on next fallback.
        sim.route.reset(read = true, bulk = false)
        triggerSlowRead()
        sim.advance(59_999L)
        assertEquals(RouteName.RELAY, sim.route.readPath())
        sim.advance(1)
        assertEquals(RouteName.DIRECT, sim.route.readPath())

        // Bulk backoff is independent and resets on full network reset().
        triggerSlowBulk()
        assertEquals(RouteName.RELAY, sim.route.filePath)
        sim.advance(60_000L)
        assertEquals(RouteName.DIRECT, sim.route.filePath)
        triggerSlowBulk()
        assertEquals(RouteName.RELAY, sim.route.filePath)
        sim.route.reset()
        assertEquals(RouteName.DIRECT, sim.route.filePath)
        triggerSlowBulk()
        sim.advance(60_000L)
        assertEquals(RouteName.DIRECT, sim.route.filePath)
    }

    @Test
    fun measuredDisplayValuesRequireThreeSamplesAndClearOnFallbackOrHardFail() {
        val sim = Sim()
        repeat(2) {
            sim.route.observe(RouteHint.Read, RouteName.DIRECT, 0, 18.0)
            sim.route.observe(RouteHint.Read, RouteName.RELAY, 0, 150.0)
        }
        assertNull(sim.route.measured().readDirectMs)
        assertNull(sim.route.measured().readRelayMs)

        sim.route.observe(RouteHint.Read, RouteName.DIRECT, 0, 18.0)
        sim.route.observe(RouteHint.Read, RouteName.RELAY, 0, 150.0)
        assertEquals(18.0, sim.route.measured().readDirectMs!!, 0.01)
        assertEquals(150.0, sim.route.measured().readRelayMs!!, 0.01)

        sim.route.fail(RouteHint.Read)
        assertTrue(sim.route.measured().readSlow)
        assertNull(sim.route.measured().readDirectMs)
        assertNull(sim.route.measured().readRelayMs)

        sim.route.observe(RouteHint.Bulk("f"), RouteName.DIRECT, 4 shl 20, 500.0)
        sim.route.observe(RouteHint.Bulk("f"), RouteName.RELAY, 4 shl 20, 1000.0)
        assertEquals(8.0, sim.route.measured().bulkDirectMibPerSec!!, 0.1)
        assertEquals(4.0, sim.route.measured().bulkRelayMibPerSec!!, 0.1)
        sim.route.hardFail(read = false, bulk = true)
        assertNull(sim.route.measured().bulkDirectMibPerSec)
        assertNull(sim.route.measured().bulkRelayMibPerSec)
    }
}
