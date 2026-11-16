package io.github.lewin671.remoteagent.core.transport

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Read routing: warm-up, fast upgrade, fallback (docs/transport.md 「只读选路」).
 * The same cases as the web's direct-routing.test.ts. A fake clock and a
 * made-up network: the harness plays the engine's part (asks nextProbe,
 * completes probes later) and the page's part (a foreground read every
 * `readEvery` ms, on the chosen path).
 */
class DirectReadRoutingTest {
    private class Net(
        val direct: (Long) -> Double,
        val relay: (Long) -> Double,
        val readEvery: Long = 0L,
        val step: Long = 25L,
    )

    private class Due(val at: Long, val path: RouteName, val ms: Double, val probe: Boolean)
    private class Probe(val at: Long, val path: RouteName)
    private class Flip(val at: Long, val path: RouteName)

    private class Sim(val net: Net) {
        var now = 0L
        var nextRead = 0L
        var upgradedAt: Long? = null
        var fellBackAt: Long? = null
        val route = DirectRouting({ now })
        val probes = ArrayList<Probe>()
        val flips = ArrayList<Flip>()
        private val inflight = mutableMapOf(RouteName.RELAY to 0, RouteName.DIRECT to 0)
        var maxInflight = 0
        private var last = route.readPath()
        private var jitter = 0
        private val due = ArrayList<Due>()

        private fun lat(path: RouteName): Double {
            val base = if (path == RouteName.DIRECT) net.direct(now) else net.relay(now)
            return base + (jitter++ % 5) * 2
        }

        fun run(ms: Long) {
            val end = now + ms
            while (now < end) {
                now += net.step
                for (d in due.filter { it.at <= now }) {
                    due.remove(d)
                    if (d.probe) {
                        inflight[d.path] = inflight[d.path]!! - 1
                        route.probeDone(d.path)
                    }
                    route.observe(RouteHint.Read, d.path, 0, d.ms)
                }
                val probe = route.nextProbe()
                if (probe != null) {
                    probes.add(Probe(now, probe))
                    inflight[probe] = inflight[probe]!! + 1
                    maxInflight = maxOf(maxInflight, inflight[probe]!!)
                    val ms = lat(probe)
                    due.add(Due(now + ms.toLong(), probe, ms, true))
                }
                if (net.readEvery > 0 && now >= nextRead) {
                    nextRead = now + net.readEvery
                    val path = route.choose(RouteHint.Read, true)
                    val ms = lat(path)
                    due.add(Due(now + ms.toLong(), path, ms, false))
                }
                val path = route.readPath()
                if (path != last) {
                    flips.add(Flip(now, path))
                    if (path == RouteName.DIRECT) { if (upgradedAt == null) upgradedAt = now } else if (fellBackAt == null) fellBackAt = now
                    last = path
                }
            }
        }
    }

    private val clearWinDirect: (Long) -> Double = { 48.0 }
    private val clearWinRelay: (Long) -> Double = { 175.0 }

    private fun countOn(sim: Sim, path: RouteName) = sim.probes.count { it.path == path }

    @Test
    fun warmUpIsBoundedByCountRateAndInFlight() {
        val sim = Sim(Net({ 400.0 }, { 400.0 }))
        sim.route.arm()
        sim.run(20_000)
        assertEquals(10, countOn(sim, RouteName.DIRECT))
        assertEquals(10, countOn(sim, RouteName.RELAY))
        assertTrue(sim.probes.maxOf { it.at } <= 5000)
        assertEquals(1, sim.maxInflight)
        for (p in sim.probes) {
            assertTrue(sim.probes.count { it.at >= p.at && it.at < p.at + 1000 } <= 4)
        }
        for (i in 1 until sim.probes.size) assertTrue(sim.probes[i].path != sim.probes[i - 1].path)
    }

    @Test
    fun aPathThatNeverAnswersHoldsOneSlotAndTheOtherStopsAtTen() {
        val sim = Sim(Net({ 1e9 }, { 100.0 }))
        sim.route.arm()
        sim.run(20_000)
        assertEquals(1, countOn(sim, RouteName.DIRECT))
        assertEquals(10, countOn(sim, RouteName.RELAY))
        assertEquals(1, sim.maxInflight)
    }

    @Test
    fun triggersWhileAWarmUpRunsAreAbsorbed() {
        val sim = Sim(Net({ 400.0 }, { 400.0 }))
        repeat(100) { sim.route.arm() }
        sim.run(2000)
        repeat(100) { sim.route.arm() }
        sim.run(8000)
        assertEquals(10, countOn(sim, RouteName.DIRECT))
        sim.route.arm()
        sim.run(6000)
        assertEquals(20, countOn(sim, RouteName.DIRECT))
        assertEquals(20, countOn(sim, RouteName.RELAY))
        for (p in sim.probes) {
            assertTrue(sim.probes.count { it.at >= p.at && it.at < p.at + 1000 } <= 4)
        }
    }

    @Test
    fun noSampleIsAskedForBeforeATriggerNorOnceTheWindowIsOver() {
        val sim = Sim(Net(clearWinDirect, clearWinRelay))
        sim.run(10_000)
        assertEquals(0, sim.probes.size)
        assertFalse(sim.route.warming())
        sim.route.arm()
        assertTrue(sim.route.warming())
        sim.run(5200)
        assertFalse(sim.route.warming())
    }

    @Test
    fun aClearWinUpgradesWithinFiveSecondsOfTheWarmUp() {
        val sim = Sim(Net(clearWinDirect, clearWinRelay, readEvery = 1000))
        sim.route.arm()
        sim.run(5000)
        assertNotNull(sim.upgradedAt)
        assertTrue(sim.upgradedAt!! <= 5000)
        assertEquals(RouteName.DIRECT, sim.route.readPath())
        val m = sim.route.measured()
        assertTrue(m.readDirectMs!! < 60)
        assertTrue(m.readRelayMs!! > 170)
    }

    @Test
    fun aCloseRaceNeverUpgradesAndNeverOscillates() {
        val sim = Sim(Net({ 160.0 }, { 175.0 }, readEvery = 500))
        sim.route.arm()
        sim.run(65_000)
        assertEquals(0, sim.flips.size)
        assertEquals(RouteName.RELAY, sim.route.readPath())
        repeat(6) { sim.route.arm(); sim.run(10_000) }
        assertEquals(0, sim.flips.size)
    }

    @Test
    fun theUpgradeLineIsSevenTenthsOfTheRelayMedian() {
        for ((direct, goes) in listOf(110.0 to true, 118.0 to true, 126.0 to false, 140.0 to false)) {
            val sim = Sim(Net({ direct }, { 175.0 }, readEvery = 1000))
            sim.route.arm()
            sim.run(5000)
            val m = sim.route.measured()
            assertEquals("direct=$direct", goes, sim.route.readPath() == RouteName.DIRECT)
            assertEquals("direct=$direct", goes, m.readDirectMs!! <= 0.7 * m.readRelayMs!!)
        }
    }

    @Test
    fun whatThePageShowsIsWhatTheRuleCompares() {
        var now = 0L
        val route = DirectRouting({ now })
        for (i in 0 until 9) {
            now = i * 300L
            route.observe(RouteHint.Read, RouteName.RELAY, 0, 100.0 + i * 10)
            route.observe(RouteHint.Read, RouteName.DIRECT, 0, 60.0 + i)
        }
        // Medians: relay 140, direct 64; direct is under 0.7 x relay, so the rule upgraded on exactly these numbers.
        val m = route.measured()
        assertEquals(140.0, m.readRelayMs!!, 0.0)
        assertEquals(64.0, m.readDirectMs!!, 0.0)
        assertEquals(RouteName.DIRECT, route.choose(RouteHint.Read, true))
        now += 61_000
        val kept = route.measured()
        assertEquals(140.0, kept.readRelayMs!!, 0.0)
        assertEquals(64.0, kept.readDirectMs!!, 0.0)
    }

    @Test
    fun theLastMeasuredMedianIsKeptReplacedAndNeverSteersADecision() {
        var now = 0L
        val route = DirectRouting({ now })
        for (i in 0 until 5) {
            now = i * 300L
            route.observe(RouteHint.Read, RouteName.RELAY, 0, 175.0)
            route.observe(RouteHint.Read, RouteName.DIRECT, 0, 160.0)
        }
        assertEquals(175.0, route.measured().readRelayMs!!, 0.0)
        now += 61_000
        assertEquals(175.0, route.measured().readRelayMs!!, 0.0)
        assertFalse(route.pending())
        assertTrue(route.sparse())
        route.observe(RouteHint.Read, RouteName.RELAY, 0, 90.0)
        route.observe(RouteHint.Read, RouteName.RELAY, 0, 90.0)
        assertEquals(175.0, route.measured().readRelayMs!!, 0.0)
        route.observe(RouteHint.Read, RouteName.RELAY, 0, 90.0)
        assertEquals(90.0, route.measured().readRelayMs!!, 0.0)
        now += 61_000
        assertEquals(RouteName.RELAY, route.choose(RouteHint.Read, true))
        assertEquals(RouteName.RELAY, route.readPath())
        route.fail()
        val after = route.measured()
        assertNull(after.readRelayMs)
        assertNull(after.readDirectMs)
    }

    @Test
    fun theFastUpgradeNeedsSamplesSpanCalmTailAndStableQueue() {
        fun feed(n: Int = 8, gap: Long = 400, direct: (Int) -> Double = { 48.0 }, queued: (Int) -> Long = { 0L }): RouteName {
            var now = 0L
            val route = DirectRouting({ now })
            for (i in 0 until n) {
                now = i * gap
                route.observe(RouteHint.Read, RouteName.RELAY, 0, 175.0)
                route.observe(RouteHint.Read, RouteName.DIRECT, 0, direct(i), queued(i))
            }
            return route.readPath()
        }
        assertEquals(RouteName.DIRECT, feed())
        assertEquals(RouteName.RELAY, feed(n = 7))
        assertEquals(RouteName.RELAY, feed(gap = 200))
        assertEquals(RouteName.RELAY, feed(direct = { if (it < 5) 48.0 else 300.0 }))
        assertEquals(RouteName.RELAY, feed(queued = { it * 1000L }))
    }

    @Test
    fun directThatDegradesAfterAnUpgradeFallsBackWithinThirtySecondsThenHoldsRelay() {
        val sim = Sim(Net({ t -> if (t < 10_000) 48.0 else 400.0 }, { 175.0 }, readEvery = 500))
        sim.route.arm()
        sim.run(5000)
        assertEquals(RouteName.DIRECT, sim.route.readPath())
        sim.run(25_000)
        assertNotNull(sim.fellBackAt)
        assertTrue(sim.fellBackAt!! - 10_000 <= 30_000)
        assertEquals(RouteName.RELAY, sim.route.readPath())
        sim.run(60_000)
        assertEquals(listOf(RouteName.DIRECT, RouteName.RELAY), sim.flips.map { it.path })
    }

    @Test
    fun afterAFallbackDirectWaitsFiveSecondsAndMustQualifyAgain() {
        val sim = Sim(Net(clearWinDirect, clearWinRelay, readEvery = 500))
        sim.route.arm()
        sim.run(5000)
        assertEquals(RouteName.DIRECT, sim.route.readPath())
        val fell = sim.now
        sim.route.fail(RouteHint.Read)
        assertEquals(RouteName.RELAY, sim.route.readPath())
        sim.run(4900)
        assertEquals(RouteName.RELAY, sim.route.readPath())
        assertFalse(sim.route.warming())
        sim.run(2000)
        assertTrue(sim.route.warming() || sim.route.readPath() == RouteName.DIRECT)
        sim.run(5000)
        assertEquals(RouteName.DIRECT, sim.route.readPath())
        assertTrue(sim.flips.first { it.path == RouteName.DIRECT && it.at > fell }.at >= fell + 5000)
    }

    @Test
    fun aHardFailureIsImmediateAndDropsEverySample() {
        val sim = Sim(Net(clearWinDirect, clearWinRelay, readEvery = 500))
        sim.route.arm()
        sim.run(5000)
        assertEquals(RouteName.DIRECT, sim.route.readPath())
        sim.route.fail(RouteHint.Read)
        assertEquals(RouteName.RELAY, sim.route.readPath())
        assertEquals(RouteName.RELAY, sim.route.choose(RouteHint.Read, true))
        assertNull(sim.route.measured().readDirectMs)
    }

    @Test
    fun theGapBetweenSevenTenthsAndOnePointTwoHolds() {
        val same = Sim(Net({ t -> if (t < 10_000) 48.0 else 175.0 }, { 175.0 }, readEvery = 250))
        same.route.arm()
        same.run(70_000)
        assertEquals(listOf(RouteName.DIRECT), same.flips.map { it.path })
        val worse = Sim(Net({ t -> if (t < 10_000) 48.0 else 175.0 * 1.3 + 12 }, { 175.0 }, readEvery = 250))
        worse.route.arm()
        worse.run(70_000)
        assertEquals(listOf(RouteName.DIRECT, RouteName.RELAY), worse.flips.map { it.path })
    }

    @Test
    fun theFallbackNeedsEightDirectAndFourRelaySamplesInThirtySeconds() {
        var now = 0L
        val route = DirectRouting({ now })
        for (i in 0 until 10) {
            now = i * 300L
            route.observe(RouteHint.Read, RouteName.RELAY, 0, 175.0)
            route.observe(RouteHint.Read, RouteName.DIRECT, 0, 48.0)
        }
        assertEquals(RouteName.DIRECT, route.readPath())
        now = 40_000
        repeat(3) { route.observe(RouteHint.Read, RouteName.RELAY, 0, 175.0) }
        repeat(12) { now += 100; route.observe(RouteHint.Read, RouteName.DIRECT, 0, 400.0) }
        assertEquals(RouteName.DIRECT, route.readPath())
        now += 100
        route.observe(RouteHint.Read, RouteName.RELAY, 0, 175.0)
        assertEquals(RouteName.RELAY, route.readPath())
    }

    @Test
    fun measuredSpeedsAppearOnlyFromRealSamplesAndAreForgottenOnDiscard() {
        var now = 0L
        val route = DirectRouting({ now })
        val file = RouteHint.Bulk(file = "one")
        var m = route.measured()
        assertNull(m.bulkRelayMibPerSec); assertNull(m.bulkDirectMibPerSec); assertNull(m.readRelayMs)
        route.observe(file, RouteName.RELAY, 2 shl 20, 100.0)
        assertNull(route.measured().bulkRelayMibPerSec)
        route.observe(file, RouteName.RELAY, 2 shl 20, 100.0)
        assertEquals(20.0, route.measured().bulkRelayMibPerSec!!, 1e-9)
        route.observe(file, RouteName.DIRECT, 4 shl 20, 100.0)
        m = route.measured()
        assertEquals(20.0, m.bulkRelayMibPerSec!!, 1e-9)
        assertEquals(40.0, m.bulkDirectMibPerSec!!, 1e-9)
        for (ms in listOf(300.0, 100.0, 200.0)) route.observe(RouteHint.Read, RouteName.RELAY, 0, ms)
        route.observe(RouteHint.Read, RouteName.DIRECT, 0, 20.0)
        route.observe(RouteHint.Read, RouteName.DIRECT, 0, 30.0)
        m = route.measured()
        assertEquals(200.0, m.readRelayMs!!, 0.0)
        assertNull(m.readDirectMs)
        route.fail()
        m = route.measured()
        assertNull(m.bulkRelayMibPerSec); assertNull(m.bulkDirectMibPerSec); assertNull(m.readRelayMs)
    }

    @Test
    fun relayNotSlowerNeedsAPurposeMeasuredOnBothPaths() {
        fun m(fd: Double? = null, fr: Double? = null, rd: Double? = null, rr: Double? = null) =
            DirectRouting.Measured(fd, fr, rd, rr, false, false, false)
        assertFalse(m().relayNotSlower)
        assertFalse(m(fd = 5.0).relayNotSlower)
        assertTrue(m(fd = 3.2, fr = 4.1, rd = 220.0, rr = 180.0).relayNotSlower)
        assertFalse(m(fd = 6.3, fr = 4.1, rd = 220.0, rr = 180.0).relayNotSlower)
        assertFalse(m(rd = 48.0, rr = 175.0).relayNotSlower)
        assertTrue(m(rd = 220.0, rr = 180.0).relayNotSlower)
    }

    @Test
    fun theStrictTierIsKeptForSmallGaps() {
        var now = 0L
        val route = DirectRouting({ now })
        for (i in 0 until 100) {
            now = i * 50L
            route.observe(RouteHint.Read, RouteName.RELAY, 1, 150.0)
            route.observe(RouteHint.Read, RouteName.DIRECT, 1, 140.0)
        }
        // Median 140 is above 0.7 x 150, so the fast tier says no; the strict one still needs its five seconds.
        assertEquals(RouteName.RELAY, route.choose(RouteHint.Read, true))
        now = 5000
        route.observe(RouteHint.Read, RouteName.DIRECT, 1, 140.0)
        assertEquals(RouteName.DIRECT, route.choose(RouteHint.Read, true))
        assertEquals(RouteName.DIRECT, route.choose(RouteHint.Read, true))
    }

    @Test
    fun theStrictTierStillRefusesAP95OutsideItsTolerance() {
        var now = 0L
        val route = DirectRouting({ now })
        for (i in 0 until 120) {
            now = i * 50L
            route.observe(RouteHint.Read, RouteName.RELAY, 1, 150.0)
            route.observe(RouteHint.Read, RouteName.DIRECT, 1, if (i % 10 == 0) 400.0 else 140.0)
        }
        now = 6000
        route.observe(RouteHint.Read, RouteName.DIRECT, 1, 140.0)
        assertEquals(RouteName.RELAY, route.choose(RouteHint.Read, true))
    }
}
