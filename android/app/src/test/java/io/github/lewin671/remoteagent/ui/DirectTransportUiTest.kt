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
                        DeviceScreen(
                            id = machineId,
                            readSettings = { _, _ -> io.github.lewin671.remoteagent.proto.GetSettingsResponse.getDefaultInstance() },
                        )
                    }
                }
            }
            ui.onNodeWithText("连接").performScrollTo().assertIsDisplayed()
            ui.onNodeWithText("直连").performScrollTo().assertIsDisplayed()
            ui.onNodeWithText("正在用直连").assertIsDisplayed()
            ui.onNodeWithText("文件传输").assertIsDisplayed()
            ui.onNodeWithText("直连 6.3 · 中继 4.1 MiB/s").assertIsDisplayed()
            ui.onNodeWithText("响应时间").assertIsDisplayed()
            ui.onNodeWithText("直连 12 · 中继 180 ms").assertIsDisplayed()
            ui.onNodeWithText("直连只用于加载和文件，发消息和审批始终经中继。").performScrollTo().assertIsDisplayed()

            ui.onNodeWithText("直连").performScrollTo().performClick()
            ui.mainClock.advanceTimeBy(400)
            ui.waitForIdle()
            assertFalse(remote.directEnabled(machineId))
            assertFalse(SharedPrefsDirectPreferenceStore(context).isEnabled("https://example.com", acct.deviceId, machineId))
            ui.onAllNodesWithText("正在用直连").assertCountEquals(0)
            ui.onAllNodesWithText("文件传输").assertCountEquals(0)

            ui.onNodeWithText("直连").performScrollTo().performClick()
            ui.mainClock.advanceTimeBy(400)
            ui.waitForIdle()
            assertTrue(remote.directEnabled(machineId))
        } finally {
            remote.close()
        }
    }

    @Test
    fun directConnectionGroupCoversAllStatesReasonsAndOffline() {
        val statusState = mutableStateOf(DirectStatus(stage = DirectStage.MEASURING))
        val onlineState = mutableStateOf(true)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                DirectConnectionGroup(statusState.value, onlineState.value, null, 0L) {}
            }
        }
        ui.onNodeWithText("直连已连上，正在测速").assertIsDisplayed()
        ui.onNodeWithText("下次传输时比较").assertIsDisplayed()

        ui.runOnUiThread { statusState.value = DirectStatus(stage = DirectStage.CONNECTED_RELAY, relayNotSlower = false) }
        ui.waitForIdle()
        ui.onNodeWithText("正在用中继").assertIsDisplayed()
        ui.onNodeWithText("直连没有明显更快").assertIsDisplayed()

        ui.runOnUiThread {
            statusState.value = DirectStatus(stage = DirectStage.CONNECTED_RELAY, relayNotSlower = true, readDirectMs = 200.0, readRelayMs = 15.0)
        }
        ui.waitForIdle()
        ui.onNodeWithText("中继更快").assertIsDisplayed()
        ui.onNodeWithText("直连 200 · 中继 15 ms").assertIsDisplayed()

        ui.runOnUiThread { statusState.value = DirectStatus(stage = DirectStage.DIRECT_ACTIVE, readDirect = true, bulkRelayMibPerSec = 4.1) }
        ui.waitForIdle()
        ui.onNodeWithText("正在用直连").assertIsDisplayed()
        ui.onNodeWithText("用于加载").assertIsDisplayed()
        ui.onNodeWithText("中继 4.1 MiB/s").assertIsDisplayed()

        ui.runOnUiThread { statusState.value = DirectStatus(stage = DirectStage.TRYING) }
        ui.waitForIdle()
        ui.onNodeWithText("正在建立直连…").assertIsDisplayed()

        for ((reason, text) in listOf(
            DirectFailReason.PEER_UNSUPPORTED to "电脑上的版本太旧，更新后可用",
            DirectFailReason.NO_RESPONSE to "电脑没有回应，可能在电脑上关了直连",
            DirectFailReason.NO_TRAVERSAL to "两边的网络不能直接连通",
            DirectFailReason.FAILED to "没连上，稍后会自动再试",
        )) {
            ui.runOnUiThread { statusState.value = DirectStatus(stage = DirectStage.UNCONNECTED, reason = reason) }
            ui.waitForIdle()
            ui.onNodeWithText("正在用中继").assertIsDisplayed()
            ui.onNodeWithText("直连不可用：$text").assertIsDisplayed()
        }

        ui.runOnUiThread { onlineState.value = false }
        ui.waitForIdle()
        ui.onAllNodesWithText("正在用中继").assertCountEquals(0)

        ui.runOnUiThread { statusState.value = DirectStatus(supported = false, enabled = true); onlineState.value = true }
        ui.waitForIdle()
        ui.onNodeWithText("这台设备不支持直连").assertIsDisplayed()
    }

    @Test
    fun aSwitchJustTurnedOnSaysTryingUntilTheEngineReportsOrTenSecondsPass() {
        val atState = mutableStateOf<Long?>(1_000L)
        val nowState = mutableStateOf(2_000L)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                DirectConnectionGroup(DirectStatus(stage = DirectStage.UNCONNECTED), true, atState.value, nowState.value) {}
            }
        }
        ui.onNodeWithText("正在建立直连…").assertIsDisplayed()
        ui.runOnUiThread { nowState.value = 12_000L }
        ui.waitForIdle()
        ui.onNodeWithText("正在用中继").assertIsDisplayed()
    }

    @Test
    fun directViewWordsMatchTheWebForEveryState() {
        val copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText
        fun v(s: DirectStatus) = directView(s, copy)
        v(DirectStatus(stage = DirectStage.DIRECT_ACTIVE, readDirect = true, fileDirect = true)).also {
            assertEquals(DirectKind.DIRECT, it.kind); assertEquals("正在用直连", it.word); assertNull(it.sub)
            assertEquals("正在用直连", it.summary)
        }
        v(DirectStatus(stage = DirectStage.DIRECT_ACTIVE, fileDirect = true)).also {
            assertEquals("用于文件", it.sub); assertEquals("正在用直连 · 用于文件", it.summary)
        }
        v(DirectStatus(stage = DirectStage.MEASURING)).also {
            assertEquals(DirectKind.MEASURING, it.kind); assertEquals(MarkKind.RUNNING, it.mark); assertEquals("直连已连上，正在测速", it.word)
        }
        v(DirectStatus(stage = DirectStage.TRYING)).also {
            assertEquals(MarkKind.RUNNING, it.mark); assertEquals("正在建立直连…", it.word)
        }
        v(DirectStatus(stage = DirectStage.CONNECTED_RELAY)).also {
            assertEquals(MarkKind.OFF, it.mark); assertEquals("正在用中继 · 直连没有明显更快", it.summary)
        }
        v(DirectStatus(stage = DirectStage.UNCONNECTED, reason = DirectFailReason.NO_TRAVERSAL)).also {
            assertEquals("正在用中继 · 直连不可用：两边的网络不能直接连通", it.summary)
        }
        v(DirectStatus(stage = DirectStage.UNCONNECTED)).also { assertEquals("正在用中继", it.summary) }
        v(DirectStatus(enabled = false, stage = DirectStage.OFF)).also { assertEquals("直连已关闭", it.summary); assertEquals(DirectKind.OFF, it.kind) }
        // The hollow ring is never used.
        for (stage in DirectStage.values()) assertTrue(v(DirectStatus(stage = stage)).mark != MarkKind.IDLE)
        // Speeds: one row per purpose, only what was measured; files not yet measured say so only when connected.
        assertEquals(emptyList<Pair<String, String>>(), v(DirectStatus(stage = DirectStage.UNCONNECTED)).speeds)
        assertEquals(listOf("文件传输" to "下次传输时比较"), v(DirectStatus(stage = DirectStage.CONNECTED_RELAY)).speeds)
        assertEquals("直连 120 · 中继 99.9 MiB/s", v(DirectStatus(stage = DirectStage.CONNECTED_RELAY, bulkDirectMibPerSec = 119.96, bulkRelayMibPerSec = 99.9)).speeds[0].second)
        assertEquals("直连 12 ms", v(DirectStatus(stage = DirectStage.CONNECTED_RELAY, bulkDirectMibPerSec = 1.0, bulkRelayMibPerSec = 1.0, readDirectMs = 12.2)).speeds[1].second)
    }

    @Test
    fun connCheckDisplaysDirectSummaryPerComputer() {
        val state = RemoteState(
            conn = ConnState.ONLINE,
            link = Link.ONLINE,
            loaded = true,
            machines = mapOf(
                "m1" to MachineView(
                    "m1",
                    online = true,
                    lastSeenMs = 0L,
                    linkState = PeerState(
                        reachable = true,
                        ready = true,
                        directStatus = DirectStatus(stage = DirectStage.DIRECT_ACTIVE, fileDirect = true, readDirect = true),
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
                        directStatus = DirectStatus(enabled = false, stage = DirectStage.OFF),
                    ),
                    meta = MachineMeta.newBuilder().setHostname("beta").build(),
                ),
                "m3" to MachineView(
                    "m3",
                    online = true,
                    lastSeenMs = 0L,
                    linkState = PeerState(
                        reachable = true,
                        ready = true,
                        directStatus = DirectStatus(stage = DirectStage.UNCONNECTED, reason = DirectFailReason.NO_TRAVERSAL),
                    ),
                    meta = MachineMeta.newBuilder().setHostname("gamma").build(),
                ),
                "m4" to MachineView(
                    "m4",
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
                    onlineMachines = { listOf("m1", "m2", "m3") },
                    pingServer = { 20L },
                    pingMachine = { 40L },
                    reconnectNow = {},
                    clock = { 1000L },
                )
            }
        }
        ui.waitForIdle()
        ui.onNodeWithText("正在用直连").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("直连已关闭").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("正在用中继 · 直连不可用：两边的网络不能直接连通").performScrollTo().assertIsDisplayed()
        ui.onAllNodesWithText("这台设备不支持直连。", substring = true).assertCountEquals(0)
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
