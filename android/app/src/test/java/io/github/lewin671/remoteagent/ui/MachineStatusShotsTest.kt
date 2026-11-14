package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.graphics.Bitmap
import android.graphics.Canvas
import android.view.inspector.WindowInspector
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import org.junit.After
import org.junit.Assert.*
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File

/** Fabricated computers, sessions and samples; nothing from a real machine. */
internal object MachineFixtures {
    const val T0 = 1_760_000_000_000L
    private val cpuAnchors = listOf(21.0, 22, 20, 25, 27, 28, 35, 38, 42, 44, 41, 38, 42, 47, 49, 43, 36, 44, 50, 53, 46, 38, 30, 27, 30, 26, 24, 25, 23, 25).map { it.toDouble() }
    private val busyAnchors = listOf(22.0, 24, 30, 58, 82, 86, 78, 84, 80, 88, 83, 90, 85, 92, 86, 89, 87, 91, 88, 90, 86, 91, 87, 90, 88, 89, 90, 87, 89, 88).map { it.toDouble() }

    private fun curve(anchors: List<Double>, n: Int = 60): List<Double> = List(n) { i ->
        val x = i * (anchors.size - 1).toDouble() / (n - 1)
        val a = x.toInt().coerceAtMost(anchors.size - 2)
        anchors[a] + (anchors[a + 1] - anchors[a]) * (x - a) + ((i * 7) % 5 - 2) * 0.3
    }

    fun usage(id: String, cpu: Double, mem: Long, exe: String = id, session: String = "") =
        ResourceUsage.newBuilder().setId(id).setExecutable(exe).setCpuPercent(cpu).setMemoryBytes(mem).setSessionId(session).build()

    fun status(
        busy: Boolean = false, cpu: Double = if (busy) 88.0 else 23.0, load: Double = if (busy) 11.4 else 3.2,
        memUsed: Long = if (busy) 13_529_146_982 else 12_025_908_428, pressure: ResourcePressure = if (busy) ResourcePressure.RESOURCE_PRESSURE_WARNING else ResourcePressure.RESOURCE_PRESSURE_NORMAL,
        position: Double = if (busy) 0.72 else 0.34, swap: Long = if (busy) 5_153_960_755 else 1_395_864_371,
        avail: Long = if (busy) 1_200_000_000 else 42_000_000_000, diskPressure: ResourcePressure = if (busy) ResourcePressure.RESOURCE_PRESSURE_CRITICAL else ResourcePressure.RESOURCE_PRESSURE_NORMAL,
    ): MachineStatus.Builder {
        val b = MachineStatus.newBuilder().setObservedMs(T0 + 118_000).setCpuPercent(cpu).setCores(10).setLoadOne(load)
            .setMemoryUsedBytes(memUsed).setMemoryTotalBytes(17_179_869_184).setMemoryPressure(pressure).setMemoryPressurePosition(position).setSwapUsedBytes(swap)
            .setDisk(DiskStatus.newBuilder().setTotalBytes(460_000_000_000).setAvailableBytes(avail).setPressure(diskPressure).setEmergencyReserveBytes(268_435_456))
            .setAgentBytes(3_100_000_000).setUptimeSeconds(3 * 86400L + 4 * 3600 + 120)
            .setBattery(MachineBattery.newBuilder().setPercent(81.0).setCharging(true))
        val cpus = curve(if (busy) busyAnchors else cpuAnchors)
        cpus.forEachIndexed { i, v ->
            b.addSamples(MachineSample.newBuilder().setTimeMs(T0 + i * 2000L).setCpuPercent(v.coerceIn(0.0, 100.0))
                .setMemoryUsedBytes((memUsed * (0.94 + 0.06 * i / 59.0)).toLong() + ((i * 13) % 7) * 12_000_000L))
        }
        if (busy) {
            b.addSessions(usage("s1", 312.0, 2_254_857_830)).addSessions(usage("s2", 188.0, 1_288_490_189)).addSessions(usage("s3", 41.0, 600L * 1048576))
            b.addProcesses(usage("p1", 312.0, 2_254_857_830, "java", "s1")).addProcesses(usage("p2", 188.0, 1_288_490_189, "xcodebuild", "s1")).addProcesses(usage("p3", 41.0, 1_717_986_918, "Google Chrome"))
        } else {
            b.addSessions(usage("s1", 141.0, 2_576_980_378)).addSessions(usage("s2", 62.0, 1_181_116_006)).addSessions(usage("s3", 3.0, 380L * 1048576))
            b.addProcesses(usage("p1", 141.0, 2_576_980_378, "node", "s1")).addProcesses(usage("p2", 62.0, 1_181_116_006, "codex", "s2")).addProcesses(usage("p3", 3.0, 380L * 1048576, "Finder"))
        }
        return b
    }

    val titles = mapOf("s1" to "三端措辞统一", "s2" to "发文件：电脑端搬到最新 main", "s3" to "稻城剪辑")
    val titlesEn = mapOf("s1" to "Unify wording across clients", "s2" to "Files: bring the computer side up to main", "s3" to "Daocheng edit")
    val agents = mapOf("s1" to Agent.AGENT_CLAUDE, "s2" to Agent.AGENT_CODEX, "s3" to Agent.AGENT_CLAUDE)

    fun live(s: MachineStatus, state: MachinePollState = MachinePollState.LIVE, receivedAt: Long = System.currentTimeMillis()) = MachineFeed(state, s, 0, receivedAt)
}

/** The 电脑状态 screens drawn for real (Robolectric native graphics), for review against 界面稿 v2 ② and as layout tests. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class, qualifiers = "w390dp-h2400dp-xhdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class MachineStatusShotsTest {
    @get:Rule(order = 0) val appFixture = io.github.lewin671.remoteagent.RaAppTestRule()
    @get:Rule(order = 1) val ui = createComposeRule()

    private val stamp = 1_760_000_000_000L
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "preview-client"), Api.defaultHttp())
    private var navigated = mutableListOf<String>()
    private val nav = Nav(
        { navigated += "back" }, { navigated += "session:$it" }, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {},
        machineStatus = { navigated += "status:$it" }, deviceStorage = { navigated += "storage:$it" }, cleanup = { navigated += "cleanup:$it" },
    )
    private var dark by mutableStateOf(false)
    private var scale by mutableStateOf(1f)
    private var language by mutableStateOf("zh-CN")

    @After fun close() { remote.close(); Drafts.clear(); Outbox.clear(); ImageDrafts.clear(); CleanupDraft.clear() }

    private fun text(message: UiMessage) = messageTextForTest(message, language)

    private fun machine(id: String, online: Boolean = true, status: Boolean = true, name: String = "MacBook Pro", disk: DiskStatus? = null): MachineView {
        val meta = MachineMeta.newBuilder().setHostname(name).setProtocolVersion(PROTOCOL_VERSION)
            .setCapabilities(MachineCapabilities.newBuilder().setMachineStatus(status)).addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE))
        if (disk != null) meta.setDiskStatus(disk)
        return MachineView(id, online, stamp, meta.build())
    }

    private fun stateOf(vararg machines: MachineView) = RemoteState(
        conn = ConnState.ONLINE, loaded = true, synced = true, link = Link.ONLINE, isAuthority = true, authorityDeviceId = "preview-client",
        leaseEndMs = stamp + 86_400_000, prefs = PrefsObject.EMPTY, machines = machines.associateBy { it.id },
        devices = machines.associate { it.id to DeviceView(it.id, DeviceKind.DEVICE_KIND_MACHINE, stamp, DeviceMeta.newBuilder().setName(it.meta?.hostname.orEmpty()).build()) } +
            ("preview-client" to DeviceView("preview-client", DeviceKind.DEVICE_KIND_CLIENT, stamp, DeviceMeta.newBuilder().setName("Example phone").setPlatform("android").build())),
    )

    private fun show(frozen: Boolean = true, content: @Composable () -> Unit) = ui.setContent {
        val density = LocalDensity.current
        RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
            CompositionLocalProvider(
                LocalRemote provides remote, LocalNav provides nav, LocalMessageLocale provides language,
                LocalDensity provides Density(density.density, scale), LocalChartFrozen provides frozen,
            ) { Box(Modifier.fillMaxSize().background(pal.paper)) { content() } }
        }
    }

    private fun save(name: String, trim: Boolean = false, topDp: Int = 0, settle: Boolean = true) {
        if (settle) ui.waitForIdle()
        val dir = System.getenv("RA_MACHINE_STATUS_SHOTS") ?: return
        if (settle) ui.mainClock.advanceTimeBy(600)
        ui.runOnIdle {
            val view = WindowInspector.getGlobalWindowViews().last()
            var bitmap = Bitmap.createBitmap(view.width, view.height, Bitmap.Config.ARGB_8888)
            view.draw(Canvas(bitmap))
            if (trim) {
                val bg = bitmap.getPixel(bitmap.width - 1, bitmap.height - 1)
                var last = bitmap.height - 1
                search@ while (last > 0) {
                    for (x in 0 until bitmap.width step 3) if (bitmap.getPixel(x, last) != bg) break@search
                    last--
                }
                val keep = minOf(bitmap.height, last + (28 * view.resources.displayMetrics.density).toInt())
                bitmap = Bitmap.createBitmap(bitmap, 0, 0, bitmap.width, keep)
            }
            if (topDp > 0) bitmap = Bitmap.createBitmap(bitmap, 0, 0, bitmap.width, minOf(bitmap.height, (topDp * view.resources.displayMetrics.density).toInt()))
            File(dir, "$name.png").apply { parentFile!!.mkdirs() }.outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
    }

    // ---- the entrance ---------------------------------------------------------------------------------------

    private fun settingsReply(reserve: Long = 268_435_456) = GetSettingsResponse.newBuilder().setSettings(DaemonSettings.newBuilder()
        .setApprovalTimeoutSeconds(1800).setIdleCloseMinutes(120).setImageUploadQuotaBytes(1L shl 30)
        .setClaude(AgentDefaults.newBuilder().setMode(ApprovalMode.APPROVAL_MODE_ASK).setModel("example-model")))
        .setImageStorage(ImageStorageUsage.newBuilder().setUploadBytes(14L shl 20).setReclaimableBytes(2L shl 20).setCacheBytes(3L shl 20).setEmergencyReserveBytes(reserve)).build()

    private fun entrance(status: MachineStatus.Builder?, online: Boolean = true, supported: Boolean = true) {
        remote.showForTest(stateOf(machine("m", online, supported)))
        val fetch: suspend (Remote, String) -> MachineStatus = { _, _ -> status?.build() ?: error("unreachable") }
        show { CompositionLocalProvider(LocalStatusFetch provides fetch) { DeviceScreen("m", { _, _ -> settingsReply() }) } }
        if (online && supported && status != null) ui.waitUntil(5_000) { ui.onAllNodesWithContentDescription(text(UiMessages.machineStatusA11YRing(text(UiMessages.machineStatusCpu()), "${status.cpuPercent.toInt()}%"))).fetchSemanticsNodes().isNotEmpty() }
    }

    @Test fun entranceNormal() {
        entrance(MachineFixtures.status().setMemoryUsedBytes(12_200_000_000))
        ui.onNodeWithContentDescription("CPU，23%", substring = true).assertExists()
        ui.onNodeWithContentDescription("内存，71%", substring = true).assertExists()
        ui.onNodeWithContentDescription("磁盘可用，42 GB", substring = true).assertExists()
        save("01-entry-normal", topDp = 700)
        ui.onNodeWithContentDescription("CPU，23%", substring = true).performClick()
        assertEquals(listOf("status:m"), navigated)
    }

    @Test fun ringsStackAtAccessibilityThreshold() {
        entrance(MachineFixtures.status())
        for (fontScale in listOf(1f, 1.49f, 1.5f, 2f)) {
            ui.runOnIdle { scale = fontScale }
            val bounds = listOf("CPU，", "内存，", "磁盘可用，").map {
                ui.onNodeWithContentDescription(it, substring = true, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            }
            if (fontScale >= 1.5f) {
                assertTrue("fontScale=$fontScale bounds=$bounds", bounds.zipWithNext().all { (a, b) -> b.top >= a.bottom && b.left == a.left })
            } else {
                assertTrue("fontScale=$fontScale bounds=$bounds", bounds.zipWithNext().all { (a, b) -> b.left >= a.right && b.top == a.top })
            }
            if (fontScale == 1f || fontScale == 2f) save("rulings-rings-$fontScale", trim = true)
        }
    }

    @Test fun entranceDiskTight() {
        entrance(MachineFixtures.status(busy = true).setMemoryUsedBytes(12_640_000_000).setMemoryPressure(ResourcePressure.RESOURCE_PRESSURE_NORMAL))
        ui.onNodeWithContentDescription("磁盘可用，1.2 GB", substring = true).assertExists()
        save("02-entry-disk-tight", topDp = 700)
    }

    @Test fun entranceOfflineIsDashesAndCannotBeOpened() {
        entrance(null, online = false)
        ui.waitForIdle()
        ui.onNodeWithContentDescription("CPU，—", substring = true).assertExists().performClick()
        assertTrue(navigated.isEmpty())
        save("03-entry-offline", topDp = 700)
    }

    @Test fun entranceIsAbsentWhenTheDaemonDoesNotSupportIt() {
        entrance(MachineFixtures.status(), supported = false)
        ui.onAllNodesWithContentDescription("CPU", substring = true).assertCountEquals(0)
    }

    @Test fun entranceEnglishAndDarkAndLargeText() {
        language = "en"; entrance(MachineFixtures.status(busy = true).setMemoryUsedBytes(12_640_000_000))
        ui.onNodeWithContentDescription("Disk free, 1.2 GB", substring = true).assertExists()
        save("20-entry-en", topDp = 700)
        ui.runOnIdle { dark = true; scale = 2f }
        ui.onNodeWithContentDescription("Disk free, 1.2 GB", substring = true).assertExists()
        save("18-entry-maxfont-dark-en", topDp = 844)
    }

    // ---- the page --------------------------------------------------------------------------------------------

    private var main by mutableStateOf(StatusMetric.CPU)
    private var sessions by mutableStateOf(true)

    private fun page(feed: MachineFeed, name: String = "MacBook Pro") = show {
        val titles = if (language == "en") MachineFixtures.titlesEn else MachineFixtures.titles
        MachineStatusContent(
            name, feed, MachineFixtures.T0 + 4 * 60_000L, main, { main = it }, sessions, { sessions = it }, { titles[it] }, { MachineFixtures.agents[it] },
            onBack = { navigated += "back" }, onSession = { navigated += "session:$it" }, onOpenStorage = { navigated += "storage" }, onCleanup = { navigated += "cleanup" }, onRetry = { navigated += "retry" },
        )
    }

    @Test fun pageNormal() {
        page(MachineFixtures.live(MachineFixtures.status().build()))
        ui.onNodeWithText("三端措辞统一").assertExists(); ui.onNodeWithText("实时").assertExists()
        ui.onNodeWithText("共 460 GB").assertExists(); ui.onNodeWithText("remote-agent 占 3.1 GB").assertExists(); ui.onNodeWithText("压力正常").assertExists()
        ui.onNodeWithText("已开机 3 天 4 小时 · 电池 81%，充电中").assertExists()
        // The disk is fine: no disk lines.
        ui.onAllNodesWithText("让 Agent 帮我清理").assertCountEquals(0)
        save("04-status-normal", trim = true)
    }

    @Test fun smallCardsStackAtAccessibilityThresholdAndAfterSwap() {
        page(MachineFixtures.live(MachineFixtures.status().build()))
        for (fontScale in listOf(1f, 1.49f, 1.5f, 2f)) {
            ui.runOnIdle { scale = fontScale }
            for (metric in listOf(StatusMetric.CPU, StatusMetric.MEMORY)) {
                ui.runOnIdle { main = metric }
                val other = if (metric == StatusMetric.CPU) "内存，" else "CPU，"
                val card = ui.onNodeWithContentDescription(other, substring = true).fetchSemanticsNode().boundsInRoot
                val disk = ui.onNodeWithContentDescription("磁盘，", substring = true).fetchSemanticsNode().boundsInRoot
                if (fontScale >= 1.5f) assertTrue(disk.top >= card.bottom && disk.left == card.left)
                else assertTrue(disk.left >= card.right && disk.top == card.top)
                if (metric == StatusMetric.CPU && (fontScale == 1f || fontScale == 2f)) save("rulings-status-$fontScale", trim = true)
            }
        }
    }

    @Test fun loadingCardsAlsoStackAtAccessibilitySize() {
        scale = 2f
        page(MachineFeed(MachinePollState.LOADING, null, 0, 0))
        val memory = ui.onNodeWithText("内存").fetchSemanticsNode().boundsInRoot
        val disk = ui.onNodeWithText("磁盘").fetchSemanticsNode().boundsInRoot
        assertTrue(disk.top > memory.bottom && disk.left == memory.left)
    }

    @Test fun pageDiskTightMemoryHighOnProcesses() {
        sessions = false
        page(MachineFixtures.live(MachineFixtures.status(busy = true).build()))
        ui.onNodeWithText("快满了").assertExists(); ui.onNodeWithText("新会话可能启动不了").assertExists(); ui.onNodeWithText("压力偏高").assertExists()
        ui.onNodeWithText("让 Agent 帮我清理").assertExists(); ui.onNodeWithText("3.1 GB").assertExists()
        ui.onNodeWithText("java").assertExists(); ui.onNodeWithText("显示会话").assertExists()
        save("05-status-tight-processes", trim = true)
        ui.onNodeWithText("让 Agent 帮我清理").performClick(); ui.onNodeWithText("3.1 GB").performClick()
        assertEquals(listOf("cleanup", "storage"), navigated)
    }

    @Test fun memoryCardTakesTheMainPlaceAndBack() {
        page(MachineFixtures.live(MachineFixtures.status().build()))
        ui.onNodeWithContentDescription("内存，11.2 GB 已用，压力正常，设为主图").performClick()
        ui.waitForIdle()
        assertEquals(StatusMetric.MEMORY, main)
        ui.onNodeWithContentDescription("CPU，23%，设为主图").assertExists()
        // The ranking follows the new metric: memory first.
        ui.onNodeWithText("2.4 GB").assertExists(); ui.onNodeWithText("共 16 GB").assertExists(); ui.onNodeWithText("交换 1.3 GB").assertExists()
        ui.onNodeWithText("负载 3.2").assertExists()
        save("06-status-memory-main", trim = true)
        ui.onNodeWithContentDescription("CPU，23%，设为主图").performClick()
        ui.waitForIdle()
        assertEquals(StatusMetric.CPU, main)
    }

    @Test fun reducedMotionDropsTheRollingDigitsAndSwapsAtOnce() {
        val context = androidx.test.core.app.ApplicationProvider.getApplicationContext<Application>()
        val resolver = context.contentResolver
        android.provider.Settings.Global.putFloat(resolver, android.provider.Settings.Global.ANIMATOR_DURATION_SCALE, 0f)
        Motion.observe(context)
        try {
            assertTrue(Motion.reduced)
            page(MachineFixtures.live(MachineFixtures.status().build()))
            // Whole numbers, not a column of digits that roll.
            ui.onNodeWithText("23").assertExists(); ui.onNodeWithText("11.2").assertExists()
            ui.onNodeWithContentDescription("内存，11.2 GB 已用，压力正常，设为主图").performClick()
            assertEquals(StatusMetric.MEMORY, main)
        } finally {
            android.provider.Settings.Global.putFloat(resolver, android.provider.Settings.Global.ANIMATOR_DURATION_SCALE, 1f)
            Motion.observe(context)
        }
        assertFalse(Motion.reduced)
    }

    @Test fun theSwapShowsOneSetOfFiguresAtATimeAndKeepsTheCardVisible() {
        page(MachineFixtures.live(MachineFixtures.status().build()))
        fun alpha(tag: String): Float? =
            ui.onAllNodesWithTag(tag, useUnmergedTree = true).fetchSemanticsNodes().firstOrNull()?.config?.getOrNull(SwapAlpha)
        ui.mainClock.autoAdvance = false
        ui.onNodeWithContentDescription("内存，11.2 GB 已用，压力正常，设为主图").performClick()
        val frames = mutableListOf<Triple<Int, Float, Float>>()
        for (ms in 16..368 step 16) {
            ui.mainClock.advanceTimeBy(16)
            // The card that is going to the main place: the memory one.
            frames += Triple(ms, alpha("swap-old-MEMORY") ?: 0f, alpha("swap-new-MEMORY") ?: 1f)
            if (ms == 48 || ms == 112 || ms == 176 || ms == 272) save("24-swap-frame-%03dms".format(ms), topDp = 900, settle = false)
        }
        // Never two sets of figures at once, in either card.
        assertTrue(frames.joinToString(), frames.all { (_, old, new) -> old == 0f || new == 0f })
        assertTrue("old figures fade out first", frames.any { (ms, old, _) -> ms in 16..96 && old > 0f && old < 1f })
        assertTrue("nothing in between", frames.any { (ms, old, new) -> ms in 112..224 && old == 0f && new == 0f })
        assertTrue("new figures fade in last", frames.any { (ms, _, new) -> ms in 240..336 && new > 0f && new < 1f })
        assertEquals(1f, frames.last().third, 0f)
        ui.mainClock.autoAdvance = true
        ui.waitForIdle()
        assertEquals(StatusMetric.MEMORY, main)
        ui.onNodeWithContentDescription("CPU，23%，设为主图").assertExists()
        assertNull(alpha("swap-old-MEMORY"))
    }

    @Test fun holdingTheCurveLooksBack() {
        page(MachineFixtures.live(MachineFixtures.status().build()))
        val chart = ui.onNode(SemanticsMatcher("chart") { n -> n.config.getOrNull(SemanticsProperties.ContentDescription)?.any { it.startsWith("最近 2 分钟") } == true })
        chart.assertExists()
        ui.onNodeWithText("实时").assertExists()
        chart.performTouchInput { down(Offset(width * 0.5f, height * 0.4f)) }
        ui.waitForIdle()
        // 59 samples back from the newest at 118 s; the middle of the 120 s window is ~58 s before the edge, which is a 60 s-old sample.
        ui.onNodeWithText("实时").assertDoesNotExist()
        ui.onNode(hasText("秒前", substring = true)).assertExists()
        save("07-status-hold-curve", trim = true)
        chart.performTouchInput { up() }
        ui.waitForIdle()
        ui.onNodeWithText("实时").assertExists()
    }

    @Test fun pageStale() {
        page(MachineFixtures.live(MachineFixtures.status().build(), MachinePollState.STALE, receivedAt = MachineFixtures.T0 + 4 * 60_000L - 120_000))
        ui.onNodeWithText("2 分钟前的数据").assertExists()
        ui.onAllNodesWithText("2 分钟前").assertCountEquals(0)
        save("08-status-stale", trim = true)
    }

    @Test fun pageBeforeTheFirstDataIsASkeletonWithoutASpinner() {
        page(MachineFeed.Empty)
        ui.onNodeWithText("正在读取").assertExists()
        ui.onAllNodesWithText("—").assertCountEquals(3)
        ui.onNodeWithText("CPU").assertExists(); ui.onNodeWithText("内存").assertExists(); ui.onNodeWithText("磁盘").assertExists()
        // No ranking, no curve, no live word, nothing to tap.
        ui.onAllNodesWithText("占用最多").assertCountEquals(0); ui.onAllNodesWithText("实时").assertCountEquals(0)
        ui.onAllNodesWithContentDescription("最近 2 分钟", substring = true).assertCountEquals(0)
        save("23-status-loading", trim = true)
    }

    @Test fun pageNothingToReadYet() {
        page(MachineFeed(MachinePollState.UNAVAILABLE, null, 0, 0))
        ui.onNodeWithText("读不到这台电脑的状态").assertExists()
        ui.onNodeWithText("重试").performClick()
        assertEquals(listOf("retry"), navigated)
        save("09-status-unavailable")
    }

    @Test fun pageWithMissingItemsDoesNotSaySo() {
        val s = MachineFixtures.status().clearLoadOne().setCores(0).clearBattery().setUptimeSeconds(0).clearSwapUsedBytes().clearAgentBytes().setMemoryPressure(ResourcePressure.RESOURCE_PRESSURE_UNSPECIFIED).clearMemoryPressurePosition().build()
        page(MachineFixtures.live(s))
        ui.onNodeWithText("共 16 GB").assertExists()
        ui.onNodeWithText("共 460 GB").assertExists()
        ui.onAllNodesWithText("压力", substring = true).assertCountEquals(0)
        ui.onAllNodesWithText("电池", substring = true).assertCountEquals(0)
        ui.onAllNodesWithText("已开机", substring = true).assertCountEquals(0)
        save("10-status-missing-items", trim = true)
    }

    @Test fun aLevelWithoutAPositionShowsTheWordAndNoScale() {
        val s = MachineFixtures.status().clearMemoryPressurePosition().build()
        page(MachineFixtures.live(s))
        ui.onNodeWithText("压力正常").assertExists()
        ui.onNodeWithContentDescription("内存，11.2 GB 已用，压力正常，设为主图").assertExists()
    }

    @Test fun pageDarkLargeAndEnglish() {
        dark = true
        page(MachineFixtures.live(MachineFixtures.status().build()))
        save("15-status-dark", trim = true)
        ui.runOnIdle { sessions = false; language = "en" }
        ui.waitForIdle()
        ui.onNodeWithText("Top usage").assertExists()
        ui.runOnIdle { scale = 2f; sessions = true }
        ui.waitForIdle()
        ui.onNodeWithText("Top usage").assertExists()
        save("17-status-maxfont-dark-en", trim = true)
    }

    @Test fun pageTightDark() {
        dark = true
        page(MachineFixtures.live(MachineFixtures.status(busy = true).build()))
        save("16-status-tight-dark", trim = true)
    }

    @Test fun pageEnglishLight() {
        language = "en"
        page(MachineFixtures.live(MachineFixtures.status(busy = true).build()))
        ui.onNodeWithText("Almost full").assertExists(); ui.onNodeWithText("New sessions may not start").assertExists()
        save("19-status-tight-en", trim = true)
    }

    // ---- the home, the failure card, the storage ----------------------------------------------------------------

    private fun homeLike(state: RemoteState) = show { Column(Modifier.fillMaxSize().background(pal.paper)) { Spacer(Modifier.height(24.dp)); DiskReminderCards(state) } }

    private fun disk(avail: Long) = DiskStatus.newBuilder().setAvailableBytes(avail).setTotalBytes(460_000_000_000).build()

    @Test fun homeReminders() {
        homeLike(stateOf(machine("m", disk = disk(8_200_000_000))))
        ui.onNodeWithText("磁盘空间不多了").assertExists(); ui.onNodeWithText("MacBook Pro 还剩 8.2 GB").assertExists()
        save("11-home-reminder-warn", trim = true)
        ui.onNodeWithText("磁盘空间不多了").performClick()
        assertEquals(listOf("status:m"), navigated)
    }

    @Test fun homeReminderCritical() {
        homeLike(stateOf(machine("m", disk = disk(1_200_000_000))))
        ui.onNodeWithText("磁盘快满了").assertExists(); ui.onNodeWithText("MacBook Pro 只剩 1.2 GB，会话可能启动不了").assertExists()
        save("12-home-reminder-critical", trim = true)
    }

    @Test fun homeRemindersOfSeveralComputersTightestFirstAndOnlyWhenOnline() {
        homeLike(stateOf(machine("a", name = "Mac mini", disk = disk(8_200_000_000)), machine("b", name = "MacBook Pro", disk = disk(1_200_000_000)), machine("c", online = false, name = "Studio", disk = disk(500_000_000)), machine("d", name = "Fine one", disk = disk(40_000_000_000))))
        val tops = ui.onAllNodes(hasText("磁盘", substring = true)).fetchSemanticsNodes().map { it.positionInRoot.y }
        assertEquals(2, tops.size); assertTrue(tops[0] < tops[1])
        ui.onNodeWithText("磁盘快满了").assertExists(); ui.onNodeWithText("磁盘空间不多了").assertExists()
        ui.onAllNodesWithText("Studio", substring = true).assertCountEquals(0)
        ui.onAllNodesWithText("Fine one", substring = true).assertCountEquals(0)
        save("13-home-reminders-two", trim = true)
    }

    @Test fun homeRemindersEnglishDark() {
        dark = true; language = "en"
        homeLike(stateOf(machine("m", disk = disk(1_200_000_000))))
        ui.onNodeWithText("Disk is almost full").assertExists()
        save("21-home-reminder-critical-dark-en", trim = true)
    }

    @Test fun launchFailedCard() {
        var retried = 0
        show { Column(Modifier.fillMaxSize().padding(16.dp)) { DiskFullCard({ navigated += "usage" }, { retried++ }) } }
        ui.onNodeWithText("这台电脑磁盘满了").assertExists(); ui.onNodeWithText("会话没能启动。腾出一些空间后再试。").assertExists()
        save("14-launch-failed-card", trim = true)
        ui.onNodeWithText("查看占用").performClick(); ui.onNodeWithText("重试").performClick()
        assertEquals(listOf("usage"), navigated); assertEquals(1, retried)
    }

    @Test fun launchFailedCardGreysRetryWhenItCannotRun() {
        show { Column(Modifier.fillMaxSize().padding(16.dp)) { DiskFullCard({}, null) } }
        ui.onNodeWithText("重试").assertIsNotEnabled()
        ui.onNodeWithText("查看占用").assertIsEnabled()
    }

    @Test fun storageShowsTheEmergencyReserve() {
        val settings = settingsReply().settings
        show { Column(Modifier.fillMaxSize().background(pal.paper)) { Spacer(Modifier.height(12.dp)); MachineImageStorage(settings, settingsReply().imageStorage, true) { _, _ -> } } }
        ui.onNodeWithText("应急预留").assertExists(); ui.onNodeWithText("256 MiB").assertExists()
        ui.onNodeWithText("磁盘写满时自动释放，让你还能开会话处理。").assertExists()
        save("22-storage-emergency-reserve", trim = true)
        ui.runOnIdle { language = "en" }
        ui.onNodeWithText("Emergency reserve").assertExists()
    }

    @Test fun storageHidesTheReserveWhenItIsGone() {
        val settings = settingsReply().settings
        show { MachineImageStorage(settings, settingsReply(reserve = 0).imageStorage, true) { _, _ -> } }
        ui.onAllNodesWithText("应急预留").assertCountEquals(0)
    }

    // ---- the whole screens, wired to a fetch ---------------------------------------------------------------------

    @Test fun theScreenPollsTheFetchAndShowsWhatItGets() {
        remote.showForTest(stateOf(machine("m")))
        var calls = 0
        val fetch: suspend (Remote, String) -> MachineStatus = { _, id -> calls++; assertEquals("m", id); MachineFixtures.status().build() }
        show { CompositionLocalProvider(LocalStatusFetch provides fetch) { MachineStatusScreen("m") } }
        ui.waitUntil(5_000) { ui.onAllNodesWithText("占用最多").fetchSemanticsNodes().isNotEmpty() }
        ui.onNodeWithText("MacBook Pro").assertExists()
        assertTrue(calls >= 1)
    }

    @Test fun theScreenOfAnUnreachableComputerOffersRetry() {
        remote.showForTest(stateOf(machine("m")))
        val fetch: suspend (Remote, String) -> MachineStatus = { _, _ -> error("unreachable") }
        show { CompositionLocalProvider(LocalStatusFetch provides fetch) { MachineStatusScreen("m") } }
        ui.waitUntil(5_000) { ui.onAllNodesWithText("读不到这台电脑的状态").fetchSemanticsNodes().isNotEmpty() }
        ui.onNodeWithText("重试").assertExists()
    }

    @Test fun cleanupDraftIsPrefilledWithoutSending() {
        val older = ProjectSummary.newBuilder().setPath("/example/old").setLatestEndedMs(1).build()
        val recent = ProjectSummary.newBuilder().setPath("/example/demo").setLatestCreatedMs(5).build()
        val m = MachineView("m", true, stamp, MachineMeta.newBuilder().setHostname("Mac").setProtocolVersion(PROTOCOL_VERSION).addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE)).addProjects(older).addProjects(recent).build())
        val state = stateOf(m)
        val prompt = text(UiMessages.machineStatusCleanupPrompt())
        assertEquals("/example/demo", prepareCleanupDraft(state, "m", prompt))
        val entry = Drafts.newSession("/example/demo", null)
        assertEquals(prompt, entry.text.value)
        assertEquals("m", entry.config.value?.machineId)
        // A second press adds nothing but the words, after what the user wrote meanwhile.
        entry.text.value = "先看看日志"
        prepareCleanupDraft(state, "m", prompt)
        assertEquals("先看看日志\n\n$prompt", entry.text.value)
    }

    @Test fun cleanupOnAComputerWithoutProjectsWaitsForTheChooser() {
        val state = stateOf(machine("m"))
        assertNull(prepareCleanupDraft(state, "m", "words"))
        assertNull(CleanupDraft.take("other"))
        assertEquals("words", CleanupDraft.take("m"))
        assertNull(CleanupDraft.take("m"))
    }
}
