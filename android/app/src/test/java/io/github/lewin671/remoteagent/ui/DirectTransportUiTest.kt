package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.showForTest

import android.app.Application
import android.content.Context
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.test.core.app.ApplicationProvider
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.DeviceView
import io.github.lewin671.remoteagent.core.Link
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.proto.ConnCheckDirectRowView
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupStage
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupTone
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupView
import io.github.lewin671.remoteagent.core.proto.DiagCheckMark
import io.github.lewin671.remoteagent.core.proto.DirectDiagnosticsViewModel
import io.github.lewin671.remoteagent.core.proto.MachineListCardView
import io.github.lewin671.remoteagent.core.proto.PlainDiagCheckView
import io.github.lewin671.remoteagent.core.proto.SettingsViewModel
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.core.transport.RtcFactory
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.transport.SharedPrefsDirectPreferenceStore
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class DirectTransportUiTest {
    @get:Rule val resources = SessionMessageContextRule()
    @get:Rule val ui = createComposeRule()

    @Test
    fun sharedPrefsDirectPreferenceStorePersistsPerComputerAndClearsOnLogout() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val store = SharedPrefsDirectPreferenceStore(context)
        store.clear()

        assertTrue(store.isEnabled("https://example.com", "client-1", "mac-1"))
        assertTrue(store.isEnabled("https://example.com", "client-1", "mac-2"))

        store.setEnabled("https://example.com", "client-1", "mac-1", false)
        val reopened = SharedPrefsDirectPreferenceStore(context)
        assertFalse(reopened.isEnabled("https://example.com", "client-1", "mac-1"))
        assertTrue(reopened.isEnabled("https://example.com", "client-1", "mac-2"))

        reopened.clear()
        assertTrue(SharedPrefsDirectPreferenceStore(context).isEnabled("https://example.com", "client-1", "mac-1"))
    }

    @Test
    fun deviceScreenConnectionGroupTogglesSwitchImmediately() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val store = SharedPrefsDirectPreferenceStore(context).apply { clear() }
        val machineId = "1111111111111111"
        val acct = Account("https://example.com", "2222222222222222")
        val remote = Remote(
            acct,
            Api.defaultHttp(),
            rtcFactory = RtcFactory { _, _ -> error("unused in unit test") },
            directPrefs = store,
        )
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        try {
            remote.showForTest(
                RemoteState(
                    conn = ConnState.ONLINE,
                    loaded = true,
                    authorityDeviceId = acct.deviceId,
                    devices = mapOf(machineId to DeviceView(machineId, DeviceKind.DEVICE_KIND_MACHINE, 0)),
                    machines = mapOf(
                        machineId to MachineView(
                            machineId,
                            online = true,
                            lastSeenMs = 0L,
                            meta = MachineMeta.newBuilder().setHostname("desk.example").setOs("darwin").build(),
                        ),
                    ),
                ),
            )
            ui.setContent {
                RaTheme(ThemeMode.LIGHT) {
                    CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                        DeviceScreen(id = machineId)
                    }
                }
            }
            ui.waitUntil(3000) {
                val vm = remote.machineSettingsView.value
                vm.found && vm.member && vm.hasConnection() && vm.connection.toggleEnabled &&
                    ui.onAllNodesWithText("直连").fetchSemanticsNodes().isNotEmpty()
            }
            ui.waitForIdle()
            ui.onNodeWithText("直连").performScrollTo().performClick()
            ui.waitUntil(3000) { ui.onAllNodesWithText("允许直连").fetchSemanticsNodes().isNotEmpty() }
            ui.onNodeWithText("允许直连").assertIsDisplayed()
            ui.onAllNodesWithText("文件传输").assertCountEquals(0)
            ui.onAllNodesWithText("响应时间").assertCountEquals(0)
            ui.onNodeWithText("发消息、审批和终端输入同时经直连和中继发出，以先到的为准；后台时只用中继。").performScrollTo().assertIsDisplayed()

            ui.onNodeWithText("允许直连").performClick()
            ui.waitUntil(3000) { !remote.directEnabled(machineId) }
            assertFalse(remote.directEnabled(machineId))
            assertFalse(SharedPrefsDirectPreferenceStore(context).isEnabled("https://example.com", acct.deviceId, machineId))
            ui.waitUntil(3000) {
                !remote.machineSettingsView.value.connection.toggleChecked &&
                    ui.onAllNodesWithText("已关闭").fetchSemanticsNodes().isNotEmpty()
            }
            ui.waitForIdle()
            ui.onAllNodesWithText("正在用直连").assertCountEquals(0)
            ui.onAllNodesWithText("文件传输").assertCountEquals(0)

            ui.onNodeWithText("允许直连").performClick()
            ui.waitUntil(3000) { remote.directEnabled(machineId) }
            assertTrue(remote.directEnabled(machineId))
        } finally {
            remote.close()
        }
    }

    @Test
    fun directConnectionSheetCoversGoldenRelayDiagAndCoreStates() {
        val relayFailedVm = ScreenGoldenLoader.machineSettingsView("machine_settings_edit", "relayFailedDiag")
        val viewState = mutableStateOf(relayFailedVm.connection)
        var toggledTo: Boolean? = null
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                DirectConnectionSheet(
                    machineId = "m1",
                    computerName = "m1",
                    view = viewState.value,
                    diagVm = DirectDiagnosticsViewModel.getDefaultInstance(),
                    onToggle = { toggledTo = it },
                    onRetry = {},
                    onClose = {},
                )
            }
        }
        ui.onNodeWithText("允许直连").assertIsDisplayed()
        ui.onNodeWithText("允许直连").performClick()
        assertFalse(toggledTo!!)

        fun lit(s: String) = io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral(s).build()
        ui.runOnUiThread {
            viewState.value = io.github.lewin671.remoteagent.core.proto.ConnectionGroupView.newBuilder()
                .setToggleVisible(true)
                .setToggleEnabled(true)
                .setToggleChecked(true)
                .setStage(io.github.lewin671.remoteagent.core.proto.ConnectionGroupStage.CONNECTION_GROUP_STAGE_MEASURING)
                .setTone(io.github.lewin671.remoteagent.core.proto.ConnectionGroupTone.CONNECTION_GROUP_TONE_MUTED)
                .setStatusText(lit("直连已连上，正在测速"))
                .setMetricText(lit("12 ms"))
                .build()
        }
        ui.waitForIdle()
        ui.onNodeWithText("直连已连上，正在测速").assertIsDisplayed()
        ui.onNodeWithText("12 ms").assertIsDisplayed()

        ui.runOnUiThread {
            viewState.value = io.github.lewin671.remoteagent.core.proto.ConnectionGroupView.newBuilder()
                .setToggleVisible(true)
                .setToggleEnabled(true)
                .setToggleChecked(true)
                .setStage(io.github.lewin671.remoteagent.core.proto.ConnectionGroupStage.CONNECTION_GROUP_STAGE_DIRECT)
                .setTone(io.github.lewin671.remoteagent.core.proto.ConnectionGroupTone.CONNECTION_GROUP_TONE_LIVE)
                .setStatusText(lit("正在用直连"))
                .setDetailText(lit("用于加载"))
                .setMetricText(lit("12 ms"))
                .build()
        }
        ui.waitForIdle()
        ui.onNodeWithText("正在用直连").assertIsDisplayed()
        ui.onNodeWithText("用于加载").assertIsDisplayed()
        ui.onNodeWithText("12 ms").assertIsDisplayed()

        ui.runOnUiThread {
            viewState.value = io.github.lewin671.remoteagent.core.proto.ConnectionGroupView.newBuilder()
                .setToggleVisible(true)
                .setToggleEnabled(true)
                .setToggleChecked(true)
                .setStage(io.github.lewin671.remoteagent.core.proto.ConnectionGroupStage.CONNECTION_GROUP_STAGE_OFFLINE)
                .setTone(io.github.lewin671.remoteagent.core.proto.ConnectionGroupTone.CONNECTION_GROUP_TONE_MUTED)
                .setStatusText(lit("设备离线"))
                .build()
        }
        ui.waitForIdle()
        ui.onAllNodesWithText("设备离线").assertCountEquals(0)

        ui.runOnUiThread {
            viewState.value = io.github.lewin671.remoteagent.core.proto.ConnectionGroupView.newBuilder()
                .setToggleVisible(false)
                .setStage(io.github.lewin671.remoteagent.core.proto.ConnectionGroupStage.CONNECTION_GROUP_STAGE_UNSUPPORTED)
                .setTone(io.github.lewin671.remoteagent.core.proto.ConnectionGroupTone.CONNECTION_GROUP_TONE_MUTED)
                .setStatusText(lit("这台设备不支持直连"))
                .build()
        }
        ui.waitForIdle()
        ui.onNodeWithText("这台设备不支持直连").assertIsDisplayed()
    }

    @Test
    fun connCheckDisplaysLatencyPerComputerAndUnsupportedFooter() {
        val settingsView = SettingsViewModel.newBuilder()
            .setOnline(true)
            .setConnCheck(
                io.github.lewin671.remoteagent.core.proto.ConnCheckSummaryView.newBuilder()
                    .setConnState(io.github.lewin671.remoteagent.core.proto.ConnState.CONN_STATE_CONNECTED)
                    .setNetworkAvailable(true)
                    .setLivePushConnected(true),
            )
            .addMachines(
                MachineListCardView.newBuilder()
                    .setMachineId("m1")
                    .setName("alpha")
                    .setOnline(true)
                    .setMember(true),
            )
            .addMachines(
                MachineListCardView.newBuilder()
                    .setMachineId("m2")
                    .setName("beta")
                    .setOnline(true)
                    .setMember(true)
                    .setDirectRow(
                        ConnCheckDirectRowView.newBuilder()
                            .setVisible(false)
                            .setStage(ConnectionGroupStage.CONNECTION_GROUP_STAGE_UNSUPPORTED),
                    ),
            )
            .addMachines(
                MachineListCardView.newBuilder()
                    .setMachineId("m3")
                    .setName("delta")
                    .setOnline(false)
                    .setMember(true),
            )
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ConnCheckSheet(
                    settingsView = settingsView,
                    onClose = {},
                    serverAddress = "https://example.com",
                    onlineMachines = { listOf("m1", "m2") },
                    pingServer = { 20L },
                    pingMachine = { 40L },
                    reconnectNow = {},
                    clock = { 1000L },
                )
            }
        }
        ui.waitForIdle()
        ui.onNodeWithText("alpha").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("beta").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("delta").performScrollTo().assertIsDisplayed()
        ui.onAllNodesWithText("40 ms").assertCountEquals(2)
        ui.onNodeWithText("离线").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("当前应用版本暂不支持直连，已全部经中继连接。", substring = true).performScrollTo().assertIsDisplayed()
    }

    @Test
    fun connCheckSheetShowsDirectRowWithLatencyAndReasonsAndDiagSheetRetries() {
        fun lit(s: String) = CoreText.newBuilder().setLiteral(s).build()
        val settingsView = SettingsViewModel.newBuilder()
            .setOnline(true)
            .setConnCheck(
                io.github.lewin671.remoteagent.core.proto.ConnCheckSummaryView.newBuilder()
                    .setConnState(io.github.lewin671.remoteagent.core.proto.ConnState.CONN_STATE_CONNECTED)
                    .setNetworkAvailable(true)
                    .setLivePushConnected(true),
            )
            .addMachines(
                MachineListCardView.newBuilder()
                    .setMachineId("m1")
                    .setName("alpha")
                    .setOnline(true)
                    .setMember(true)
                    .setDirectRow(
                        ConnCheckDirectRowView.newBuilder()
                            .setVisible(true)
                            .setStage(ConnectionGroupStage.CONNECTION_GROUP_STAGE_DIRECT)
                            .setTone(ConnectionGroupTone.CONNECTION_GROUP_TONE_LIVE)
                            .setStatusText(lit("正在用直连"))
                            .setMetricText(lit("18 ms")),
                    ),
            )
            .addMachines(
                MachineListCardView.newBuilder()
                    .setMachineId("m2")
                    .setName("beta")
                    .setOnline(true)
                    .setMember(true)
                    .setDirectRow(
                        ConnCheckDirectRowView.newBuilder()
                            .setVisible(true)
                            .setStage(ConnectionGroupStage.CONNECTION_GROUP_STAGE_RELAY)
                            .setTone(ConnectionGroupTone.CONNECTION_GROUP_TONE_WARN)
                            .setStatusText(lit("正在用中继"))
                            .setDetailText(lit("两边的网络隔开了，不能直接连通")),
                    ),
            )
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ConnCheckSheet(
                    settingsView = settingsView,
                    onClose = {},
                    serverAddress = "https://example.com",
                    onlineMachines = { listOf("m1", "m2") },
                    pingServer = { 20L },
                    pingMachine = { 40L },
                    reconnectNow = {},
                    clock = { 1000L },
                )
            }
        }
        ui.waitForIdle()
        ui.onNodeWithText("正在用直连").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("正在用中继 · 两边的网络隔开了，不能直接连通").performScrollTo().assertIsDisplayed()
    }

    @Test
    fun unifiedDirectSheetShowsInlineDiagnosticsAndTriggersRetry() {
        fun lit(s: String) = CoreText.newBuilder().setLiteral(s).build()
        val vm = ScreenGoldenLoader.machineSettingsView("machine_settings_edit", "relayFailedDiag")
        var retried = false
        val diagVm = DirectDiagnosticsViewModel.newBuilder()
            .setMachineId("m1")
            .setSupported(true)
            .setEnabled(true)
            .setCanRetry(true)
            .setTitleText(lit("两边的网络隔开了，不能直接连通"))
            .setBodyText(lit("已在后台继续尝试恢复直连。"))
            .addChecks(
                PlainDiagCheckView.newBuilder()
                    .setMark(DiagCheckMark.DIAG_CHECK_MARK_ONLINE)
                    .setLabelText(lit("电脑在线并响应"))
                    .setStatusText(lit("正常")),
            )
            .addChecks(
                PlainDiagCheckView.newBuilder()
                    .setMark(DiagCheckMark.DIAG_CHECK_MARK_OFF)
                    .setLabelText(lit("局域网或公网直通"))
                    .setDetailText(lit("当前 NAT 不支持直通"))
                    .setStatusText(lit("受阻")),
            )
            .addChecks(
                PlainDiagCheckView.newBuilder()
                    .setMark(DiagCheckMark.DIAG_CHECK_MARK_ONLINE)
                    .setLabelText(lit("后台自动重试"))
                    .setDetailText(lit("网络变化或切回前台时自动重试"))
                    .setStatusText(lit("已启用")),
            )
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                DirectConnectionSheet(
                    machineId = "m1",
                    computerName = "m1",
                    view = vm.connection,
                    diagVm = diagVm,
                    onToggle = {},
                    onRetry = { retried = true },
                    onClose = {},
                )
            }
        }
        ui.waitForIdle()
        ui.onNodeWithText("自动排障检查").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("电脑在线并响应").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("局域网或公网直通").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("后台自动重试").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("复制诊断给支持").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("重新尝试直连").performScrollTo().performClick()
        assertTrue(retried)
    }

    @Test
    fun unifiedDirectSheetShowsConnectedPathCheckWhenActive() {
        fun lit(s: String) = CoreText.newBuilder().setLiteral(s).build()
        val diagView = ConnectionGroupView.newBuilder()
            .setToggleVisible(true)
            .setToggleEnabled(true)
            .setToggleChecked(true)
            .setStage(ConnectionGroupStage.CONNECTION_GROUP_STAGE_DIRECT)
            .setTone(ConnectionGroupTone.CONNECTION_GROUP_TONE_LIVE)
            .setShowDiagnosticsEntry(true)
            .build()
        val diagVm = DirectDiagnosticsViewModel.newBuilder()
            .setMachineId("m1")
            .setSupported(true)
            .setEnabled(true)
            .setConnected(true)
            .setTitleText(lit("直连已连通"))
            .setBodyText(lit("正在通过直连通信"))
            .addChecks(
                PlainDiagCheckView.newBuilder()
                    .setMark(DiagCheckMark.DIAG_CHECK_MARK_ONLINE)
                    .setLabelText(lit("局域网或公网直通"))
                    .setStatusText(lit("已连通")),
            )
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                DirectConnectionSheet(
                    machineId = "m1",
                    computerName = "m1",
                    view = diagView,
                    diagVm = diagVm,
                    onToggle = {},
                    onRetry = {},
                    onClose = {},
                )
            }
        }
        ui.waitForIdle()
        ui.onNodeWithText("已连通").performScrollTo().assertIsDisplayed()
    }

    @Test
    fun machineStatusDoesNotDisplayTransportPaths() {
        val vm = ScreenGoldenLoader.machineStatusView("machine_status_lifecycle", "liveCriticalDisk")
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalChartFrozen provides true) {
                    MachineStatusContent(
                        view = vm,
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
            }
        }
        ui.onAllNodesWithText("交互：中继").assertCountEquals(0)
        ui.onAllNodesWithText("文件：直连").assertCountEquals(0)
        ui.onAllNodesWithText("文件：中继").assertCountEquals(0)
    }
}
