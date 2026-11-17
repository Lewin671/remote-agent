package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.DeviceView
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.DiskReminderView
import io.github.lewin671.remoteagent.core.proto.MachinePollState
import io.github.lewin671.remoteagent.core.proto.MachineRingKind
import io.github.lewin671.remoteagent.core.proto.MachineRingTone
import io.github.lewin671.remoteagent.core.proto.MachineRingView
import io.github.lewin671.remoteagent.core.proto.MachineStatusDot
import io.github.lewin671.remoteagent.core.proto.MachineStatusViewModel
import io.github.lewin671.remoteagent.core.proto.MachineUsageTab
import io.github.lewin671.remoteagent.core.proto.Text
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.MachineMeta
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], qualifiers = "w412dp-h892dp-xxhdpi", application = Application::class)
class MachineStatusShotsTest {
    @get:Rule val ui = createComposeRule()
    @get:Rule val resources = SessionMessageContextRule()

    private val remote = Remote(
        Account("https://example.com", ByteArray(32), ByteArray(32), "d"),
        Api.defaultHttp(),
        dispatcher = Dispatchers.Unconfined,
    )
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private var dark by mutableStateOf(false)
    private var fontScale by mutableStateOf(1f)
    private var language by mutableStateOf("zh-CN")

    @After
    fun close() {
        remote.close()
        Drafts.clear()
        Outbox.clear()
        ImageDrafts.clear()
        CleanupDraft.clear()
    }

    private fun show(frozen: Boolean = true, content: @Composable () -> Unit) = ui.setContent {
        CompositionLocalProvider(
            LocalRemote provides remote,
            LocalNav provides nav,
            LocalDensity provides Density(LocalDensity.current.density, fontScale),
            LocalMessageLocale provides language,
            LocalChartFrozen provides frozen,
        ) { RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) { content() } }
    }

    @Test
    fun goldenLifecycleViewsRenderFromCore() {
        val views = listOf("loading", "liveCriticalDisk", "processesAndScrub", "staleAfter3Timeouts", "recoveredFreshCurve")
            .map { it to ScreenGoldenLoader.machineStatusView("machine_status_lifecycle", it) }
        var current by mutableStateOf(views.first().second)
        show {
            MachineStatusContent(
                view = current,
                main = StatusMetric.CPU,
                onMain = {},
                onScrub = { _, _, _ -> },
                onSelectTab = {},
                onBack = {},
                onRetry = {},
                onOpenStorage = {},
                onCleanup = {},
                onSession = {},
            )
        }
        for ((name, vm) in views) {
            ui.runOnIdle { current = vm }
            ui.waitForIdle()
            if (name == "liveCriticalDisk") {
                ui.onNodeWithText(vm.machineName).assertExists()
                ui.onNodeWithText(sessionText(UiMessages.machineStatusTopUsage())).performScrollTo().assertIsDisplayed()
            }
        }
    }

    @Test
    fun ringsEntranceOpensOnlyWhenOnlineAndStacksUnderLargeText() {
        val rings = listOf(
            MachineRingView.newBuilder()
                .setKind(MachineRingKind.MACHINE_RING_KIND_CPU)
                .setHasValue(true)
                .setFraction(0.32)
                .setValueText(Text.newBuilder().setLiteral("32%"))
                .setLabel(Text.newBuilder().setLiteral("CPU"))
                .setTone(MachineRingTone.MACHINE_RING_TONE_NORMAL)
                .build(),
            MachineRingView.newBuilder()
                .setKind(MachineRingKind.MACHINE_RING_KIND_MEMORY)
                .setHasValue(true)
                .setFraction(0.64)
                .setValueText(Text.newBuilder().setLiteral("64%"))
                .setLabel(Text.newBuilder().setLiteral("内存"))
                .setTone(MachineRingTone.MACHINE_RING_TONE_WARN)
                .build(),
            MachineRingView.newBuilder()
                .setKind(MachineRingKind.MACHINE_RING_KIND_DISK)
                .setHasValue(true)
                .setFraction(0.95)
                .setValueText(Text.newBuilder().setLiteral("2.1G"))
                .setLabel(Text.newBuilder().setLiteral("磁盘"))
                .setTone(MachineRingTone.MACHINE_RING_TONE_CRITICAL)
                .build(),
        )
        var online by mutableStateOf(true)
        var opened = 0
        fontScale = 1.6f
        show { MachineRingsCard(rings, online = online, onOpen = { opened++ }) }
        ui.onNodeWithText("32%").performClick()
        assertEquals(1, opened)

        ui.runOnIdle { online = false }
        ui.onNodeWithText("32%").performClick()
        assertEquals(1, opened)
    }

    @Test
    fun memoryCardTakesTheMainPlaceAndForwardsInteractions() {
        val vm = ScreenGoldenLoader.machineStatusView("machine_status_lifecycle", "liveCriticalDisk")
        var main by mutableStateOf(StatusMetric.CPU)
        var tabClicked: MachineUsageTab? = null
        var cleaned = 0
        var storage = 0
        show {
            MachineStatusContent(
                view = vm,
                main = main,
                onMain = { main = it },
                onScrub = { _, _, _ -> },
                onSelectTab = { tabClicked = it },
                onBack = {},
                onRetry = {},
                onOpenStorage = { storage++ },
                onCleanup = { cleaned++ },
                onSession = {},
            )
        }
        ui.onNodeWithText(sessionText(UiMessages.machineStatusMemory())).performClick()
        ui.waitForIdle()
        assertEquals(StatusMetric.MEMORY, main)

        val tabNode = ui.onNodeWithText(sessionText(UiMessages.machineStatusShowProcesses()))
        tabNode.performScrollTo().performClick()
        assertEquals(MachineUsageTab.MACHINE_USAGE_TAB_PROCESSES, tabClicked)

        val cleanupNodes = ui.onAllNodesWithText(sessionText(UiMessages.machineStatusCleanupAction()))
        if (cleanupNodes.fetchSemanticsNodes().isNotEmpty()) {
            cleanupNodes[0].performScrollTo().performClick()
            assertEquals(1, cleaned)
        }
        val usageNodes = ui.onAllNodesWithText(sessionText(UiMessages.machineStatusAgentUsage()))
        if (usageNodes.fetchSemanticsNodes().isNotEmpty()) {
            usageNodes[0].performScrollTo().performClick()
            assertEquals(1, storage)
        }
    }

    @Test
    fun theSwapShowsOneSetOfFiguresAtATimeAndKeepsTheCardVisible() {
        fun alpha(tag: String): Float? =
            ui.onAllNodesWithText("", useUnmergedTree = true).let {
                runCatching { ui.onNodeWithTag(tag, useUnmergedTree = true).fetchSemanticsNode().config[SwapAlpha] }.getOrNull()
            }

        val vm = ScreenGoldenLoader.machineStatusView("machine_status_lifecycle", "liveCriticalDisk")
        var main by mutableStateOf(StatusMetric.CPU)
        show {
            MachineStatusContent(
                view = vm,
                main = main,
                onMain = { main = it },
                onScrub = { _, _, _ -> },
                onSelectTab = {},
                onBack = {},
                onRetry = {},
                onOpenStorage = {},
                onCleanup = {},
                onSession = {},
            )
        }
        ui.mainClock.autoAdvance = false
        ui.onNodeWithText(sessionText(UiMessages.machineStatusMemory())).performClick()
        val frames = mutableListOf<Triple<Int, Float, Float>>()
        for (ms in 16..368 step 16) {
            ui.mainClock.advanceTimeBy(16)
            frames += Triple(ms, alpha("swap-old-MEMORY") ?: 0f, alpha("swap-new-MEMORY") ?: 1f)
        }
        assertTrue(frames.joinToString(), frames.all { (_, old, new) -> old == 0f || new == 0f })
        assertTrue("old figures fade out first", frames.any { (ms, old, _) -> ms in 16..96 && old > 0f && old < 1f })
        assertTrue("nothing in between", frames.any { (ms, old, new) -> ms in 112..224 && old == 0f && new == 0f })
        assertTrue("new figures fade in last", frames.any { (ms, _, new) -> ms in 240..336 && new > 0f && new < 1f })
        ui.mainClock.autoAdvance = true
        ui.waitForIdle()
        assertEquals(StatusMetric.MEMORY, main)
    }

    @Test
    fun unreachableComputerShowsUnavailableAndRetry() {
        var retried = 0
        val vm = MachineStatusViewModel.newBuilder()
            .setMachineId("m1")
            .setMachineName("desk")
            .setFound(true)
            .setSupported(true)
            .setPollState(MachinePollState.MACHINE_POLL_STATE_UNAVAILABLE)
            .setStatusDot(MachineStatusDot.MACHINE_STATUS_DOT_RED_STATIC)
            .setUnavailableTitle(Text.newBuilder().setLiteral("无法读取这台电脑的状态"))
            .setShowRetry(true)
            .build()
        show {
            MachineStatusContent(
                view = vm,
                main = StatusMetric.CPU,
                onMain = {},
                onScrub = { _, _, _ -> },
                onSelectTab = {},
                onBack = {},
                onRetry = { retried++ },
                onOpenStorage = {},
                onCleanup = {},
                onSession = {},
            )
        }
        ui.onNodeWithText(sessionText(UiMessages.commonRetry())).performClick()
        assertEquals(1, retried)
    }

    @Test
    fun homeRemindersAndDiskFullCardRenderAndRespectRetryState() {
        val reminders = listOf(
            DiskReminderView.newBuilder()
                .setMachineId("m1")
                .setCritical(false)
                .setAvailableBytes(6_400_000_000L)
                .setTitle(Text.newBuilder().setLiteral("MacBook Pro 空间偏紧"))
                .setDetail(Text.newBuilder().setLiteral("还有 6.4 GB 可用"))
                .build(),
            DiskReminderView.newBuilder()
                .setMachineId("m2")
                .setCritical(true)
                .setAvailableBytes(400_000_000L)
                .setTitle(Text.newBuilder().setLiteral("Linux Box 磁盘已严重不足"))
                .setDetail(Text.newBuilder().setLiteral("还有 0.4 GB 可用"))
                .build(),
        )
        var retried = 0
        var retryEnabled by mutableStateOf(true)
        show {
            Column(Modifier.fillMaxSize().background(pal.paper)) {
                Spacer(Modifier.height(16.dp))
                DiskReminderCards(reminders)
                Spacer(Modifier.height(16.dp))
                DiskFullCard(onUsage = {}, onRetry = if (retryEnabled) ({ retried++ }) else null)
            }
        }
        ui.onNodeWithText("MacBook Pro 空间偏紧").assertExists()
        ui.onNodeWithText("Linux Box 磁盘已严重不足").assertExists()
        ui.onNodeWithText(sessionText(UiMessages.commonRetry())).performClick()
        assertEquals(1, retried)

        ui.runOnIdle { retryEnabled = false }
        ui.onNodeWithText(sessionText(UiMessages.commonRetry())).performClick()
        assertEquals(1, retried)
    }

    @Test
    fun prepareCleanupSessionEmitsEffectWithRecentProjectOrPicker() {
        var lastEff: io.github.lewin671.remoteagent.core.proto.PrepareCleanupSessionEffect? = null
        remote.coreHost.onPrepareCleanupSession = { lastEff = it }
        val state = RemoteState(
            machines = mapOf(
                "m1" to MachineView(
                    "m1",
                    true,
                    0,
                    MachineMeta.newBuilder()
                        .addProjects(
                            io.github.lewin671.remoteagent.proto.ProjectSummary.newBuilder()
                                .setPath("/example/demo")
                                .setLatestEndedMs(500L),
                        )
                        .build(),
                ),
                "m2" to MachineView("m2", true, 0, null),
            ),
            devices = mapOf(
                "m1" to DeviceView("m1", DeviceKind.DEVICE_KIND_MACHINE, 0),
                "m2" to DeviceView("m2", DeviceKind.DEVICE_KIND_MACHINE, 0),
            ),
            conn = io.github.lewin671.remoteagent.core.ConnState.ONLINE,
            loaded = true,
        )
        remote.showForTest(state)

        remote.coreHost.prepareCleanupSession("m1")
        assertFalse(lastEff!!.needsDirectoryPicker)
        assertEquals("m1", lastEff!!.machineId)
        assertEquals("/example/demo", lastEff!!.cwd)

        remote.coreHost.prepareCleanupSession("m2")
        assertTrue(lastEff!!.needsDirectoryPicker)
        assertEquals("m2", lastEff!!.machineId)
    }
}
