package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.content.Context
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ApplicationProvider
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.DeviceView
import io.github.lewin671.remoteagent.core.MachinePollState
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.StatusMetric
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.transport.SharedPrefsDirectPreferenceStore
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
    fun deviceScreenTogglesDirectPreferencePerMachine() {
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
            ui.onNodeWithText("直连").assertIsDisplayed().performClick()
            ui.onNodeWithText("关闭").assertIsDisplayed().performClick()
            ui.mainClock.advanceTimeBy(500)
            ui.waitForIdle()
            assertFalse(remote.directEnabled(machineId))
            assertFalse(SharedPrefsDirectPreferenceStore(context).isEnabled("https://example.com", acct.deviceId, machineId))
            ui.onNodeWithText("关闭", useUnmergedTree = true).assertIsDisplayed()
        } finally {
            remote.close()
        }
    }

    @Test
    fun machineStatusContentShowsPerPurposePaths() {
        val sampleStatus = MachineFixtures.status().build()
        val feed = MachineFeed(MachinePollState.LIVE, sampleStatus, MachineFixtures.T0, MachineFixtures.T0 + 118_000L) {}
        val state = androidx.compose.runtime.mutableStateOf(false)
        ui.setContent {
            val fileDirect = state.value
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalChartFrozen provides true) {
                    MachineStatusContent(
                        name = "desk.example",
                        feed = feed,
                        nowMs = MachineFixtures.T0 + 118_000L,
                        main = StatusMetric.CPU,
                        onMain = {},
                        showSessions = true,
                        onShowSessions = {},
                        titleOf = { null },
                        agentOf = { null },
                        onBack = {},
                        onSession = {},
                        onOpenStorage = {},
                        onCleanup = {},
                        onRetry = {},
                        fileDirect = fileDirect,
                    )
                }
            }
        }
        ui.onNodeWithText("交互：中继").assertIsDisplayed()
        ui.onNodeWithText("文件：中继").assertIsDisplayed()

        ui.runOnUiThread { state.value = true }
        ui.waitForIdle()
        ui.onNodeWithText("交互：中继").assertIsDisplayed()
        ui.onNodeWithText("文件：直连").assertIsDisplayed()
    }
}
