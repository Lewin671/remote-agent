package io.github.lewin671.remoteagent.ui

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
import io.github.lewin671.remoteagent.core.transport.DirectFailReason
import io.github.lewin671.remoteagent.core.transport.DirectStage
import io.github.lewin671.remoteagent.core.transport.DirectStatus
import io.github.lewin671.remoteagent.core.transport.PeerState
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
        val acct = Account("https://example.com", ByteArray(32), ByteArray(32), "2222222222222222")
        val remote = Remote(acct, Api.defaultHttp(), directPrefs = store)
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
                            linkState = PeerState(
                                reachable = true,
                                ready = true,
                                authorized = true,
                                directStatus = DirectStatus(
                                    enabled = true,
                                    stage = DirectStage.DIRECT_ACTIVE,
                                    readDirect = true,
                                    fileDirect = true,
                                    bulkDirectMibPerSec = 6.3,
                                    bulkRelayMibPerSec = 4.1,
                                    readDirectMs = 12.0,
                                    readRelayMs = 180.0,
                                ),
                            ),
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
            ui.waitUntil(3000) { remote.machineSettingsView.value.hasConnection() && remote.machineSettingsView.value.connection.toggleEnabled }
            ui.onNodeWithText("连接").performScrollTo().assertIsDisplayed()
            ui.onNodeWithText("直连").performScrollTo().assertIsDisplayed()
            ui.onNodeWithText("正在用直连").assertIsDisplayed()
            ui.onNodeWithText("文件传输").assertIsDisplayed()
            ui.onNodeWithText("直连 6.3 · 中继 4.1 MiB/s").assertIsDisplayed()
            ui.onNodeWithText("响应时间").assertIsDisplayed()
            ui.onNodeWithText("直连 12 · 中继 180 ms").assertIsDisplayed()
            ui.onNodeWithText("发消息和审批同时经直连和中继发出，以先到的为准；后台时只用中继。").performScrollTo().assertIsDisplayed()

            ui.onNodeWithText("直连").performScrollTo().performClick()
            ui.waitUntil(3000) { !remote.directEnabled(machineId) }
            assertFalse(remote.directEnabled(machineId))
            assertFalse(SharedPrefsDirectPreferenceStore(context).isEnabled("https://example.com", acct.deviceId, machineId))
            ui.waitUntil(3000) { !remote.machineSettingsView.value.connection.toggleChecked }
            ui.onAllNodesWithText("正在用直连").assertCountEquals(0)
            ui.onAllNodesWithText("文件传输").assertCountEquals(0)

            ui.onNodeWithText("直连").performScrollTo().performClick()
            ui.waitUntil(3000) { remote.directEnabled(machineId) }
            assertTrue(remote.directEnabled(machineId))
        } finally {
            remote.close()
        }
    }

    @Test
    fun directConnectionGroupCoversGoldenRelayDiagAndCoreStates() {
        val relayFailedVm = ScreenGoldenLoader.machineSettingsView("machine_settings_edit", "relayFailedDiag")
        val viewState = mutableStateOf(relayFailedVm.connection)
        var toggledTo: Boolean? = null
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                DirectConnectionGroup(viewState.value) { toggledTo = it }
            }
        }
        ui.onNodeWithText("连接").assertIsDisplayed()
        ui.onNodeWithText("直连").assertIsDisplayed()
        ui.onNodeWithText("直连").performClick()
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
                .setFileSpeedText(lit("下次传输时比较"))
                .build()
        }
        ui.waitForIdle()
        ui.onNodeWithText("直连已连上，正在测速").assertIsDisplayed()
        ui.onNodeWithText("下次传输时比较").assertIsDisplayed()

        ui.runOnUiThread {
            viewState.value = io.github.lewin671.remoteagent.core.proto.ConnectionGroupView.newBuilder()
                .setToggleVisible(true)
                .setToggleEnabled(true)
                .setToggleChecked(true)
                .setStage(io.github.lewin671.remoteagent.core.proto.ConnectionGroupStage.CONNECTION_GROUP_STAGE_DIRECT)
                .setTone(io.github.lewin671.remoteagent.core.proto.ConnectionGroupTone.CONNECTION_GROUP_TONE_LIVE)
                .setStatusText(lit("正在用直连"))
                .setDetailText(lit("用于加载"))
                .setFileSpeedText(lit("中继 4.1 MiB/s"))
                .build()
        }
        ui.waitForIdle()
        ui.onNodeWithText("正在用直连").assertIsDisplayed()
        ui.onNodeWithText("用于加载").assertIsDisplayed()
        ui.onNodeWithText("中继 4.1 MiB/s").assertIsDisplayed()

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
        val state = RemoteState(
            conn = ConnState.ONLINE,
            link = Link.ONLINE,
            loaded = true,
            machines = mapOf(
                "m1" to MachineView(
                    "m1",
                    online = true,
                    lastSeenMs = 0L,
                    linkState = PeerState(reachable = true, ready = true),
                    meta = MachineMeta.newBuilder().setHostname("alpha").build(),
                ),
                "m2" to MachineView(
                    "m2",
                    online = true,
                    lastSeenMs = 0L,
                    linkState = PeerState(
                        reachable = true,
                        ready = true,
                        directStatus = DirectStatus(supported = false),
                    ),
                    meta = MachineMeta.newBuilder().setHostname("beta").build(),
                ),
                "m3" to MachineView(
                    "m3",
                    online = false,
                    lastSeenMs = 0L,
                    linkState = PeerState(reachable = false),
                    meta = MachineMeta.newBuilder().setHostname("delta").build(),
                ),
            ),
        )
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ConnCheckSheet(
                    state = state,
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
        ui.onNodeWithText("这台设备不支持直连。", substring = true).performScrollTo().assertIsDisplayed()
    }

    @Test
    fun connCheckSheetShowsDirectRowWithLatencyAndReasonsAndDiagSheetRetries() {
        val m1State = RemoteState(
            conn = ConnState.ONLINE,
            link = Link.ONLINE,
            machines = mapOf(
                "m1" to MachineView(
                    "m1",
                    online = true,
                    lastSeenMs = 0L,
                    linkState = PeerState(
                        reachable = true,
                        ready = true,
                        directStatus = DirectStatus(
                            supported = true,
                            enabled = true,
                            stage = DirectStage.DIRECT_ACTIVE,
                            readDirect = true,
                            fileDirect = true,
                            readDirectMs = 18.0,
                        ),
                    ),
                    meta = MachineMeta.newBuilder().setHostname("alpha").build(),
                ),
                "m2" to MachineView(
                    "m2",
                    online = true,
                    lastSeenMs = 0L,
                    linkState = PeerState(
                        reachable = true,
                        ready = true,
                        directStatus = DirectStatus(
                            supported = true,
                            enabled = true,
                            stage = DirectStage.UNCONNECTED,
                            reason = DirectFailReason.NO_TRAVERSAL,
                        ),
                    ),
                    meta = MachineMeta.newBuilder().setHostname("beta").build(),
                ),
            ),
        )
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ConnCheckSheet(
                    state = m1State,
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
        ui.onNodeWithText("正在用直连 · 直连 18 ms").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("两边的网络不能直接连通").performScrollTo().assertIsDisplayed()
    }

    @Test
    fun directDiagEntryOpensSheetAndTriggersRetry() {
        val vm = ScreenGoldenLoader.machineSettingsView("machine_settings_edit", "relayFailedDiag")
        var openedDiag = false
        var retried = false
        val state = RemoteState(
            conn = ConnState.ONLINE,
            link = Link.ONLINE,
            machines = mapOf(
                "m1" to MachineView(
                    "m1",
                    online = true,
                    lastSeenMs = 0L,
                    linkState = PeerState(
                        reachable = true,
                        ready = true,
                        directStatus = DirectStatus(
                            supported = true,
                            enabled = true,
                            stage = DirectStage.UNCONNECTED,
                            reason = DirectFailReason.NO_TRAVERSAL,
                        ),
                    ),
                ),
            ),
        )
        val showDiag = androidx.compose.runtime.mutableStateOf(false)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                if (!showDiag.value) {
                    DirectConnectionGroup(
                        vm.connection,
                        onDiagnostics = {
                            openedDiag = true
                            showDiag.value = true
                        },
                    ) {}
                } else {
                    DirectDiagSheet(
                        machineId = "m1",
                        state = state,
                        onRetry = {
                            retried = true
                            true
                        },
                        onClose = { showDiag.value = false },
                    )
                }
            }
        }
        ui.onNodeWithText("诊断信息").performClick()
        assertTrue(openedDiag)
        ui.waitForIdle()
        ui.onNodeWithText("两边的网络不能直接连通").assertIsDisplayed()
        ui.onNodeWithText("重新尝试").performClick()
        assertTrue(retried)
    }

    @Test
    fun directDiagSheetShowsActiveDirectVerdictWhenActive() {
        val state = RemoteState(
            conn = ConnState.ONLINE,
            link = Link.ONLINE,
            machines = mapOf(
                "m1" to MachineView(
                    "m1",
                    online = true,
                    lastSeenMs = 0L,
                    linkState = PeerState(
                        reachable = true,
                        ready = true,
                        directStatus = DirectStatus(
                            supported = true,
                            enabled = true,
                            stage = DirectStage.DIRECT_ACTIVE,
                            reason = null,
                        ),
                    ),
                ),
            ),
        )
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                DirectDiagSheet(
                    machineId = "m1",
                    state = state,
                    onRetry = { true },
                    onClose = {},
                )
            }
        }
        ui.waitForIdle()
        ui.onNodeWithText("正在用直连").assertIsDisplayed()
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
