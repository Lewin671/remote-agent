package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

class MachineStatusModelTest {
    private fun text(locale: String = "zh-CN") = StatusText({ messageTextForTest(it, locale) }, locale)
    private fun status(
        cpu: Double = 23.0, used: Long = 12_025_908_428, total: Long = 17_179_869_184, pressure: ResourcePressure = ResourcePressure.RESOURCE_PRESSURE_NORMAL,
        avail: Long = 42_000_000_000, diskTotal: Long = 460_000_000_000, diskPressure: ResourcePressure = ResourcePressure.RESOURCE_PRESSURE_NORMAL,
    ): MachineStatus.Builder = MachineStatus.newBuilder().setCpuPercent(cpu).setMemoryUsedBytes(used).setMemoryTotalBytes(total).setMemoryPressure(pressure)
        .setDisk(DiskStatus.newBuilder().setTotalBytes(diskTotal).setAvailableBytes(avail).setPressure(diskPressure))

    @Test fun ringsSayWhatTheSpecSays() {
        val rings = machineRings(status(cpu = 23.0, used = 12_200_000_000).build(), true, text())
        assertEquals(listOf("23%", "71%", "42 GB"), rings.map { it.value })
        assertEquals(listOf("CPU，23%", "内存，71%", "磁盘可用，42 GB"), rings.map { it.spoken })
        // The disk ring draws what is used, the number says what is free.
        assertEquals(1f - 42f / 460f, rings[2].fraction!!, 0.001f)
        assertTrue(rings.all { it.tone == MachineTone.NORMAL })
    }

    @Test fun onlyTheStrainedRingChangesColourAndCpuNever() {
        val s = status(cpu = 99.0, pressure = ResourcePressure.RESOURCE_PRESSURE_WARNING, avail = 1_200_000_000, diskPressure = ResourcePressure.RESOURCE_PRESSURE_CRITICAL).build()
        val rings = machineRings(s, true, text())
        assertEquals(listOf(MachineTone.NORMAL, MachineTone.WARN, MachineTone.CRITICAL), rings.map { it.tone })
        assertEquals("1.2 GB", rings[2].value)
    }

    @Test fun offlineDrawsTracksAndDashes() {
        for (r in machineRings(status().build(), online = false, text = text()) + machineRings(null, true, text())) {
            assertNull(r.fraction); assertEquals("—", r.value); assertEquals(MachineTone.NORMAL, r.tone)
        }
    }

    @Test fun sizesAreDecimalAndLocaleAware() {
        val t = text()
        assertEquals("42 GB", t.size(42_000_000_000)); assertEquals("1.2 GB", t.size(1_200_000_000)); assertEquals("380 MB", t.size(380_000_000))
        assertEquals("11.2", t.gbNumber(11_200_000_000, oneDecimal = true)); assertEquals("8.0", t.gbNumber(8_000_000_000, oneDecimal = true))
        // Memory is counted in 1024s and still called GB / MB (a 16 GiB computer says 16 GB); disk in 1000s.
        assertEquals("16 GB", t.size(17_179_869_184, binary = true)); assertEquals("17 GB", t.size(17_179_869_184))
        assertEquals("11.2", t.gbNumber(12_025_908_428, oneDecimal = true, binary = true)); assertEquals("380 MB", t.size(380L * 1048576, binary = true))
        assertEquals("1 MB", t.size(1_000, binary = true))
        assertEquals("3 天 4 小时", t.duration(3 * 86400L + 4 * 3600 + 99)); assertEquals("5 小时 7 分", t.duration(5 * 3600L + 7 * 60))
        assertEquals("3 d 4 hr", text("en").duration(3 * 86400L + 4 * 3600))
        assertEquals("30 秒前的数据", t.stale(30_000)); assertEquals("2 分钟前的数据", t.stale(125_000))
        assertEquals("Data from 1 sec ago", text("en").stale(0))
        // Rounded to the 2 seconds of the sampling.
        assertEquals("4 秒前", t.lookback(3_100)); assertEquals("0 秒前", t.lookback(400))
    }

    @Test fun memoryCardLines() {
        val s = status(pressure = ResourcePressure.RESOURCE_PRESSURE_WARNING).setSwapUsedBytes(1_395_864_371).setMemoryPressurePosition(0.72).build()
        val c = memoryCard(s, text())
        assertEquals("11.2", c.used); assertEquals("共 16 GB，交换 1.3 GB", c.detail); assertEquals(0.72f, c.position!!, 0.0001f); assertEquals(MachineTone.WARN, c.tone)
        // No swap in use: only the total. No pressure: no scale and no word.
        val bare = memoryCard(status(pressure = ResourcePressure.RESOURCE_PRESSURE_UNSPECIFIED).build(), text())
        assertEquals("共 16 GB", bare.detail); assertNull(bare.position); assertNull(bare.pressureWord)
        assertEquals("共 16 GB", memoryCard(status().setSwapUsedBytes(0).build(), text()).detail)
        // A level without a position: the word, no scale.
        val levelOnly = memoryCard(status(pressure = ResourcePressure.RESOURCE_PRESSURE_WARNING).build(), text())
        assertNull(levelOnly.position); assertEquals("压力偏高", messageTextForTest(levelOnly.pressureWord!!))
    }

    @Test fun diskCardLinesAndBar() {
        val ok = diskCard(status().setAgentBytes(3_100_000_000).build(), text())
        // Two lines of their own under the state word, never one that wraps into three.
        assertEquals("42", ok.free); assertEquals("共 460 GB", ok.detail); assertEquals("remote-agent 占 3.1 GB", ok.detail2)
        assertEquals("460 GB total", diskCard(status().setAgentBytes(3_100_000_000).build(), text("en")).detail); assertEquals("remote-agent uses 3.1 GB", diskCard(status().setAgentBytes(3_100_000_000).build(), text("en")).detail2)
        assertEquals(3.1f / 460f, ok.agentShare, 0.0001f); assertEquals((460f - 42f - 3.1f) / 460f, ok.otherShare, 0.0001f)
        assertEquals("空间充足", messageTextForTest(ok.word))
        val low = diskCard(status(avail = 8_000_000_000, diskPressure = ResourcePressure.RESOURCE_PRESSURE_WARNING).build(), text())
        assertEquals("空间不多了", messageTextForTest(low.word)); assertEquals(MachineTone.WARN, low.tone)
        val bad = diskCard(status(avail = 1_200_000_000, diskPressure = ResourcePressure.RESOURCE_PRESSURE_CRITICAL).setAgentBytes(1).build(), text())
        assertEquals("快满了", messageTextForTest(bad.word)); assertEquals("新会话可能启动不了", bad.detail); assertNull(bad.detail2); assertEquals("1.2", bad.free)
        // No figure for remote-agent's share: the line does not claim one.
        assertEquals("共 460 GB", diskCard(status().build(), text()).detail); assertNull(diskCard(status().build(), text()).detail2)
        assertFalse(showDiskActions(status().build())); assertTrue(showDiskActions(status(diskPressure = ResourcePressure.RESOURCE_PRESSURE_WARNING).build()))
    }

    @Test fun cpuAndFooterOmitWhatIsNotProvided() {
        val s = status().setCores(10).setLoadOne(3.2).setUptimeSeconds(3 * 86400L + 4 * 3600).setBattery(MachineBattery.newBuilder().setPercent(81.0).setCharging(true)).build()
        val f = cpuFigures(s, text())
        assertEquals(23, f.percent); assertEquals("3.2", f.load); assertEquals("10 核", f.cores)
        assertEquals(listOf("已开机 3 天 4 小时", "电池 81%，充电中"), footerParts(s, text()))
        assertEquals(listOf("已开机 3 天 4 小时", "电池 81%"), footerParts(s.toBuilder().setBattery(MachineBattery.newBuilder().setPercent(81.0)).build(), text()))
        val bare = status().build()
        assertNull(cpuFigures(bare, text()).load); assertNull(cpuFigures(bare, text()).cores); assertTrue(footerParts(bare, text()).isEmpty())
        assertEquals(listOf("已开机 3 天 4 小时"), footerParts(s.toBuilder().clearBattery().build(), text()))
    }

    private fun pts(vararg tv: Pair<Long, Double>) = tv.map { ChartPoint(it.first, it.second) }

    @Test fun chartPlacesPointsByTimeAndBreaksAtHoles() {
        val end = 200_000L
        val points = pts(100_000L to 10.0, 102_000L to 20.0, 104_000L to 30.0, 160_000L to 50.0, 162_000L to 60.0, 200_000L to 70.0)
        val runs = chartRuns(points, end, 100.0)
        // Two holes (104 s to 160 s, 162 s to 200 s): three runs, the middle and last with a point or two.
        assertEquals(3, runs.size)
        assertEquals(listOf(3, 2, 1), runs.map { it.size })
        // x is by time within the window that ends at 200 s.
        assertEquals((100_000 - (end - CHART_WINDOW_MS)).toDouble() / CHART_WINDOW_MS, runs[0][0].x, 1e-9)
        assertEquals(1.0, runs[2][0].x, 1e-9); assertEquals(0.7, runs[2][0].y, 1e-9)
    }

    @Test fun chartCutsAtBothEdges() {
        val points = pts(0L to 0.0, 4_000L to 40.0, 6_000L to 60.0)
        // Window [2 s - 120 s .. 2 s]: the line is cut on the right at 2 s (value 20) and has no point left of it.
        val runs = chartRuns(points, 2_000L, 100.0)
        assertEquals(1, runs.size); assertEquals(1.0, runs[0].last().x, 1e-9); assertEquals(0.2, runs[0].last().y, 1e-9)
        // And on the left: the window starts at 3 s, where the line between 0 and 4 s is 30.
        val left = chartRuns(pts(0L to 0.0, 4_000L to 40.0), 123_000L, 100.0, windowMs = 120_000)
        assertEquals(0.0, left[0][0].x, 1e-9); assertEquals(0.3, left[0][0].y, 1e-9)
        assertTrue(chartRuns(emptyList(), 1000, 100.0).isEmpty())
        assertEquals(1, chartRuns(pts(5_000L to 50.0), 5_000L, 100.0).size)
    }

    @Test fun valueAtInterpolatesButNotAcrossHoles() {
        val p = pts(0L to 0.0, 2_000L to 10.0, 30_000L to 90.0)
        assertEquals(5.0, valueAt(p, 1_000)!!, 1e-9); assertEquals(10.0, valueAt(p, 2_000)!!, 1e-9)
        assertNull(valueAt(p, 10_000)); assertNull(valueAt(p, 31_000)); assertNull(valueAt(emptyList(), 1))
    }

    @Test fun onTimeChartSamplesStayWithinTwoSeconds() {
        val clock = ChartTimeline()
        assertEquals(100_000.0, clock.advance(0.0, 100_000, 100_000), 0.0)
        for (now in 0..10_000 step 100) {
            val latest = 100_000L + now / 2000 * 2000
            val end = clock.advance(now.toDouble(), 98_000, latest)
            assertTrue(latest - end in 0.0..2000.0)
            if (now > 0 && now % 2000 == 0) assertEquals((latest - 2000).toDouble(), end, 0.0)
        }
        val history = ChartTimeline()
        assertEquals(98_000.0, history.advance(0.0, 98_000, 100_000), 0.0)
        assertEquals(99_000.0, history.advance(1000.0, 98_000, 100_000), 0.0)
    }

    @Test fun missedPollCatchesUpWithin500msWithoutJumpOrRewind() {
        val clock = ChartTimeline()
        clock.advance(0.0, 98_000, 100_000)
        assertEquals(100_000.0, clock.advance(2000.0, 98_000, 100_000), 0.0)
        assertEquals(100_000.0, clock.advance(4000.0, 98_000, 104_000), 0.0)
        val times = listOf(0, 100, 250, 400, 500)
        val frames = times.map { clock.advance(4000.0 + it, 98_000, 104_000) }
        assertEquals(listOf(100_000.0, 101_952.0, 103_500.0, 103_968.0, 104_000.0), frames)
        val speeds = frames.zipWithNext().mapIndexed { i, (a, b) -> (b - a) / (times[i + 1] - times[i]) }
        assertTrue(speeds.all { it > 0 } && speeds.zipWithNext().all { (a, b) -> a > b })
        assertEquals(104_000.0, clock.advance(6000.0, 98_000, 106_000), 0.0)
        assertEquals(105_000.0, clock.advance(7000.0, 98_000, 106_000), 0.0)
    }

    @Test fun chartCatchupRetargetsWithinItsOriginalDeadline() {
        val clock = ChartTimeline()
        clock.advance(0.0, 100_000, 100_000)
        assertEquals(100_000.0, clock.advance(4000.0, 98_000, 104_000), 0.0)
        val middle = clock.advance(4250.0, 98_000, 104_000)
        assertEquals(middle, clock.advance(4250.0, 98_000, 108_000), 0.0)
        assertEquals(108_000.0, clock.advance(4500.0, 98_000, 108_000), 0.0)
        assertEquals(108_000.0, clock.advance(4516.0, 98_000, 106_000), 0.0)
        assertEquals(108_000.0, clock.advance(4400.0, 98_000, 108_000), 0.0)
        assertEquals(112_000.0, clock.advance(5000.0, 98_000, 112_000, false), 0.0)
        assertEquals(112_000.0, clock.advance(5000.0, 98_000, 112_000), 0.0)
    }

    @Test fun theWindowAlwaysStartsOnDataOnceTheBufferIsFull() {
        // 60 samples span 118 s; the edge trails the newest by 0..2 s; a 116 s window starts inside the data in every case.
        val latest = 118_000L
        for (lag in listOf(0L, 1_000L, 2_000L)) {
            val end = latest - lag
            assertTrue(end - CHART_WINDOW_MS >= 0)
        }
        assertEquals(116_000L, CHART_WINDOW_MS)
    }

    @Test fun samplesMoreThanTenSecondsApartAreNotJoined() {
        val p = pts(0L to 10.0, 10_000L to 20.0, 20_001L to 30.0)
        val runs = chartRuns(p, 30_000L, 100.0)
        assertEquals(listOf(2, 1), runs.map { it.size })
        assertEquals(10_000L, CHART_GAP_MS)
    }

    @Test fun theSwapNeverShowsTwoSetsOfFiguresAtOnce() {
        assertEquals(1f, swapOldAlpha(0.0), 0f); assertEquals(0f, swapNewAlpha(0.0), 0f)
        assertEquals(0.5f, swapOldAlpha(50.0), 0.001f); assertEquals(0f, swapOldAlpha(100.0), 0f)
        assertEquals(0f, swapNewAlpha(230.0), 0f); assertEquals(0.5f, swapNewAlpha(290.0), 0.001f); assertEquals(1f, swapNewAlpha(350.0), 0f)
        for (ms in 0..350) assertTrue("at $ms", swapOldAlpha(ms.toDouble()) == 0f || swapNewAlpha(ms.toDouble()) == 0f)
        // Between the two nothing shows at all.
        assertEquals(0f, swapOldAlpha(150.0) + swapNewAlpha(150.0), 0f)
    }

    @Test fun nearestSampleAndSummary() {
        val p = pts(0L to 10.0, 2_000L to 40.0, 4_000L to 20.0)
        assertEquals(1, nearestSample(p, 2_700.0)); assertEquals(-1, nearestSample(emptyList(), 1.0))
        assertEquals("最近 2 分钟，最高 40%，现在 20%", chartSummary(p, StatusMetric.CPU, text()))
        assertEquals("最近 2 分钟，最高 38 MB，现在 19 MB", chartSummary(pts(0L to 1e6 * 10, 2L to 4e7, 4L to 2e7), StatusMetric.MEMORY, text()))
    }

    @Test fun chartPointsFilterTheRecoveryPointAndPickTheMetric() {
        val s = status().addSamples(MachineSample.newBuilder().setTimeMs(3).setCpuPercent(30.0).setMemoryUsedBytes(300))
            .addSamples(MachineSample.newBuilder().setTimeMs(1).setCpuPercent(10.0).setMemoryUsedBytes(100)).build()
        assertEquals(listOf(1L, 3L), chartPoints(s, StatusMetric.CPU).map { it.timeMs })
        assertEquals(listOf(3L), chartPoints(s, StatusMetric.MEMORY, sinceMs = 2).map { it.timeMs })
        assertEquals(300.0, chartPoints(s, StatusMetric.MEMORY, 2)[0].value, 0.0)
        assertEquals(100.0, chartCeiling(s, StatusMetric.CPU), 0.0); assertEquals(17_179_869_184.0, chartCeiling(s, StatusMetric.MEMORY), 0.0)
    }

    private fun usage(id: String, cpu: Double, mem: Long, exe: String = id, session: String = "") =
        ResourceUsage.newBuilder().setId(id).setExecutable(exe).setCpuPercent(cpu).setMemoryBytes(mem).setSessionId(session).build()

    @Test fun rankingRowsFollowTheMetricAndStayPut() {
        val order = UsageOrder()
        val s1 = status().addSessions(usage("a", 141.0, 2_576_980_378)).addSessions(usage("b", 62.0, 1_181_116_006)).addSessions(usage("c", 3.0, 380L * 1048576)).build()
        val rows = usageRows(s1, true, StatusMetric.CPU, order, text(), { mapOf("a" to "三端措辞统一")[it] }, { Agent.AGENT_CLAUDE })
        assertEquals(listOf("a", "b", "c"), rows.map { it.id })
        assertEquals(listOf("141%", "62%", "3%"), rows.map { it.primary }); assertEquals(listOf("2.4 GB", "1.1 GB", "380 MB"), rows.map { it.secondary })
        assertEquals("三端措辞统一", rows[0].title); assertTrue(rows[0].top); assertFalse(rows[1].top); assertEquals(1f, rows[0].bar, 0f)
        assertEquals(62f / 141f, rows[1].bar, 0.0001f)
        // 1.5 points ahead is not enough to overtake.
        val s2 = status().addSessions(usage("a", 60.0, 1)).addSessions(usage("b", 61.5, 1)).addSessions(usage("c", 3.0, 1)).build()
        assertEquals(listOf("a", "b", "c"), usageRows(s2, true, StatusMetric.CPU, order, text(), { null }, { null }).map { it.id })
        // On memory the list starts over by memory.
        val mem = usageRows(s1, true, StatusMetric.MEMORY, order, text(), { null }, { null })
        assertEquals("2.4 GB", mem[0].primary); assertEquals("141%", mem[0].secondary)
    }

    @Test fun processRowsNameTheirSessionAndOnlyEightShow() {
        val order = UsageOrder()
        val b = status()
        for (i in 1..12) b.addProcesses(usage("p$i", 100.0 - i, 1_000_000L * i, exe = "exe$i", session = if (i == 1) "s1" else ""))
        val rows = usageRows(b.build(), false, StatusMetric.CPU, order, text(), { mapOf("s1" to "三端措辞统一")[it] }, { null })
        assertEquals(8, rows.size); assertEquals("exe1", rows[0].title); assertEquals("1 MB · 三端措辞统一", rows[0].secondary); assertEquals("2 MB", rows[1].secondary)
        assertFalse(rows[0].isSession)
        assertTrue(usageRows(status().build(), true, StatusMetric.CPU, order, text(), { null }, { null }).isEmpty())
    }

    private fun machine(id: String, online: Boolean, avail: Long?, supported: Boolean = true): MachineView {
        val meta = MachineMeta.newBuilder().setCapabilities(MachineCapabilities.newBuilder().setMachineStatus(supported))
        if (avail != null) meta.setDiskStatus(DiskStatus.newBuilder().setAvailableBytes(avail))
        return MachineView(id, online, 0, meta.build())
    }

    @Test fun reminderCardsTightestFirstAndOnlyForOnlineSupportedLowDisks() {
        val state = RemoteState(machines = mapOf(
            "m1" to machine("m1", true, 8_200_000_000), "m2" to machine("m2", true, 1_200_000_000), "m3" to machine("m3", false, 1_000_000_000),
            "m4" to machine("m4", true, 50_000_000_000), "m5" to machine("m5", true, 5_000_000_000, supported = false), "m6" to machine("m6", true, null),
        ))
        val cards = diskReminders(state, text()) { "电脑 $it" }
        assertEquals(listOf("m2", "m1"), cards.map { it.machineId }); assertEquals(listOf(true, false), cards.map { it.critical })
        assertEquals("磁盘快满了", messageTextForTest(cards[0].title)); assertEquals("电脑 m2 只剩 1.2 GB，会话可能启动不了", messageTextForTest(cards[0].detail))
        assertEquals("磁盘空间不多了", messageTextForTest(cards[1].title)); assertEquals("电脑 m1 还剩 8.2 GB", messageTextForTest(cards[1].detail))
        assertEquals("m1 has 8.2 GB left", messageTextForTest(diskReminders(state, text("en")) { it }[1].detail, "en"))
    }

    @Test fun viewFollowsControllerState() {
        assertEquals(StatusView.LOADING, statusView(MachinePollState.IDLE, null)); assertEquals(StatusView.LOADING, statusView(MachinePollState.LOADING, null))
        assertEquals(StatusView.UNAVAILABLE, statusView(MachinePollState.UNAVAILABLE, null))
        val s = MachineStatus.getDefaultInstance()
        assertEquals(StatusView.LIVE, statusView(MachinePollState.LIVE, s)); assertEquals(StatusView.STALE, statusView(MachinePollState.STALE, s))
        assertEquals(StatusView.STALE, statusView(MachinePollState.UNAVAILABLE, s)); assertEquals(StatusView.LIVE, statusView(MachinePollState.LOADING, s))
    }

    @Test fun cleanupDraftKeepsWhatWasWritten() {
        val prompt = messageTextForTest(UiMessages.machineStatusCleanupPrompt())
        assertEquals(prompt, cleanupDraft("", prompt)); assertEquals(prompt, cleanupDraft("  \n", prompt))
        assertEquals("先看看日志\n\n$prompt", cleanupDraft("先看看日志\n", prompt))
        // Already in the draft: not added again.
        assertEquals("先看看日志\n\n$prompt", cleanupDraft("先看看日志\n\n$prompt", prompt)); assertEquals(prompt, cleanupDraft(prompt, prompt))
        assertEquals("这台电脑磁盘空间不够了。帮我看看是什么占的，按占用从大到小列出来，说明哪些可以安全清理。先不要删除任何东西，等我确认。", prompt)
    }

    @Test fun diskFullErrorsSayItInPlainWords() {
        val e = CommandException("disk_full", "mkdir /Users/x: no space left on device")
        assertTrue(isDiskFull(e))
        assertEquals("这台电脑磁盘满了", e.display.testText())
        assertFalse(e.display.testText().contains("mkdir"))
        assertEquals("This computer’s disk is full", e.display.testText("en"))
        assertEquals("存储配额已用尽，或写入目标的空间不足", CommandException("storage_exhausted", "x").display.testText())
        assertFalse(isDiskFull(CommandException("storage_exhausted", "x")))
    }
}
