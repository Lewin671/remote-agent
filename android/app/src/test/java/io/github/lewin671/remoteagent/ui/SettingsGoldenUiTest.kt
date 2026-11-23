package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onLast
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.DeviceView
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.PEER_PROTOCOL_VERSION
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.proto.ThemePreference
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.DeviceMeta
import io.github.lewin671.remoteagent.proto.MachineMeta
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class SettingsGoldenUiTest {
    @get:Rule val resources = SessionMessageContextRule()
    @get:Rule val ui = createComposeRule()

    @Test
    fun settingsOverviewSettingsAndCheckingUpdateRenderGoldenAndDispatchActions() {
        val settingsVm = ScreenGoldenLoader.settingsView("settings_overview", "settings")
        val checkingVm = ScreenGoldenLoader.settingsView("settings_overview", "checkingUpdate")
        val vmState = mutableStateOf(settingsVm)
        var pickedTheme: ThemePreference? = null
        var openedNotifications = 0
        var openedClients = 0
        var openedConnCheck = 0
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalChartFrozen provides true) {
                    SettingsScreenContent(
                        vm = vmState.value,
                        serverDisplay = "example.com",
                        localCacheText = "12.0 MiB",
                        updaterEnabled = true,
                        onNotifications = { openedNotifications++ },
                        onDevices = { openedClients++ },
                        onTheme = { pickedTheme = it },
                        onOpenConnCheck = { openedConnCheck++ },
                    )
                }
            }
        }
        ui.onNodeWithText("电脑").assertIsDisplayed()
        ui.onNodeWithText("BuildBox").assertIsDisplayed()
        ui.onNodeWithText("BackupMac").assertIsDisplayed()
        ui.onNodeWithText("本机").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("本机缓存").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("12.0 MiB").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("账户与设备").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("关于").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("v0.32.4").performScrollTo().assertIsDisplayed()

        // No dual "全部设备 / 全部客户端" entries and no raw client rows on main Settings.
        ui.onAllNodesWithText("全部设备").assertCountEquals(0)
        ui.onAllNodesWithText("全部客户端").assertCountEquals(0)
        ui.onAllNodesWithText("iPad Pro").assertCountEquals(0)

        ui.onNodeWithText("通知").performScrollTo().performClick()
        assertEquals(1, openedNotifications)

        ui.onNodeWithText("客户端").performScrollTo().performClick()
        assertEquals(1, openedClients)

        ui.onNodeWithText("暗色").performScrollTo().performClick()
        assertEquals(ThemePreference.THEME_PREFERENCE_DARK, pickedTheme)

        ui.onNodeWithText("服务器与连接").performScrollTo().performClick()
        assertEquals(1, openedConnCheck)

        ui.runOnUiThread { vmState.value = checkingVm }
        ui.waitForIdle()
        ui.onNodeWithText("正在检查更新…").performScrollTo().assertIsDisplayed()
    }

    @Test
    fun settingsOverviewDevicesAndClientDetailsRenderWithoutDoubleEllipsis() {
        val devicesVm = ScreenGoldenLoader.devicesView("settings_overview", "devices")
        val authClientVm = ScreenGoldenLoader.clientDetailView("settings_overview", "authClient")
        val selfClientVm = ScreenGoldenLoader.clientDetailView("settings_overview", "selfClient")

        val showDevices = mutableStateOf(true)
        val clientState = mutableStateOf(authClientVm)
        var renamedTo: String? = null
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalNav provides nav) {
                    if (showDevices.value) {
                        DevicesScreenContent(vm = devicesVm)
                    } else {
                        ClientScreenContent(
                            vm = clientState.value,
                            onRename = { renamedTo = it },
                        )
                    }
                }
            }
        }
        // Pure Clients screen: no computer rows!
        ui.onAllNodesWithText("BuildBox").assertCountEquals(0)
        ui.onAllNodesWithText("BackupMac").assertCountEquals(0)
        ui.onNodeWithText("My Phone").assertIsDisplayed()
        ui.onNodeWithText("Pixel 9 · …self · 当前").assertIsDisplayed()

        ui.runOnUiThread { showDevices.value = false }
        ui.waitForIdle()
        ui.onAllNodesWithText("iPad Pro").onFirst().assertIsDisplayed()
        ui.onNodeWithText("…auth").assertIsDisplayed()
        ui.onAllNodesWithText("……auth", substring = true).assertCountEquals(0)

        ui.runOnUiThread { clientState.value = selfClientVm }
        ui.waitForIdle()
        ui.onAllNodesWithText("My Phone").onFirst().assertIsDisplayed()
        ui.onNodeWithText("…self").assertIsDisplayed()
        ui.onAllNodesWithText("……self", substring = true).assertCountEquals(0)
        ui.onNodeWithText("恢复默认名称").performClick()
        assertEquals("", renamedTo)
    }

    @Test
    fun machineSettingsEditGoldenRendersVariantsAndDispatchesPickers() {
        val initial = ScreenGoldenLoader.machineSettingsView("machine_settings_edit", "initialLoaded")
        val saving = ScreenGoldenLoader.machineSettingsView("machine_settings_edit", "savingClaudeAndClean")
        val unknown = ScreenGoldenLoader.machineSettingsView("machine_settings_edit", "unknownTriggersReread")
        val relayFailed = ScreenGoldenLoader.machineSettingsView("machine_settings_edit", "relayFailedDiag")

        val vmState = mutableStateOf(initial)
        var selectedAgent: Agent? = null
        var updatedTimeout: Int? = null
        var updatedModel: String? = null
        var toggledDirect: Boolean? = null
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalChartFrozen provides true, LocalNav provides nav) {
                    DeviceScreenContent(
                        vm = vmState.value,
                        onToggleDirect = { toggledDirect = it },
                        onSelectAgent = { selectedAgent = it },
                        onUpdateAgentDefaults = { _, model, _, _ -> updatedModel = model },
                        onUpdateDaemonSettings = { timeout, _, _, _ -> updatedTimeout = timeout },
                    )
                }
            }
        }
        ui.onNodeWithText("MacStudio").assertIsDisplayed()
        ui.onNodeWithText("电脑状态").assertIsDisplayed()
        ui.onNodeWithText("用量与速度").performScrollTo().assertIsDisplayed()
        ui.onAllNodesWithText("Claude Code").onLast().performScrollTo().performClick()
        assertEquals(Agent.AGENT_CLAUDE, selectedAgent)

        ui.onNodeWithText("模型").performClick()
        val claudeDefaults = initial.cliVersionsList.firstOrNull { it.agent == Agent.AGENT_CLAUDE }
            ?.takeIf { it.hasDefaults() }?.defaults ?: initial.agentDefaults
        val firstModelLabel = claudeDefaults.modelChoicesList.first().label.let {
            sessionText(io.github.lewin671.remoteagent.core.UiMessages.modelsDefault())
        }
        ui.onAllNodesWithText(firstModelLabel).onLast().performClick()
        assertEquals(claudeDefaults.modelChoicesList.first().id, updatedModel)
        ui.onNodeWithText("完成").performClick()

        ui.onNodeWithText("审批超时").performScrollTo().performClick()
        ui.onNodeWithText("10 分钟").performClick()
        assertEquals(600, updatedTimeout)

        ui.runOnUiThread { vmState.value = saving }
        ui.waitForIdle()
        ui.onNodeWithText("MacStudio").assertIsDisplayed()

        ui.runOnUiThread { vmState.value = unknown }
        ui.waitForIdle()
        ui.onNodeWithText("MacStudio").assertIsDisplayed()

        ui.runOnUiThread { vmState.value = relayFailed }
        ui.waitForIdle()
        ui.onNodeWithText("直连").performScrollTo().performClick()
        ui.onNodeWithText("允许直连").performClick()
        assertFalse(toggledDirect!!)
    }

    @Test
    fun machineSettingsRendersCompactQuotaFallbackAndSingleSeparatorForUnverifiedCli() {
        val base = ScreenGoldenLoader.machineSettingsView("machine_settings_edit", "initialLoaded")
        val longMsg = "尚未收到 Claude Code 的额度。仅从这台电脑上由 remote-agent 管理的 CLI 会话读取；安装 CLI 或在其他会话中使用不会触发这里更新。"
        val unvText = io.github.lewin671.remoteagent.core.proto.Text.newBuilder()
            .setKey("settings.unverified")
            .build()
        val vm = base.toBuilder()
            .clearQuotaRows()
            .addQuotaRows(
                io.github.lewin671.remoteagent.core.proto.QuotaCliSection.newBuilder()
                    .setAgent(Agent.AGENT_CLAUDE)
                    .setAgentName("Claude Code")
                    .setEmptyOrErrorText(
                        io.github.lewin671.remoteagent.core.proto.Text.newBuilder()
                            .setLiteral(longMsg)
                            .build(),
                    )
                    .build(),
            )
            .clearCliVersions()
            .addCliVersions(
                io.github.lewin671.remoteagent.core.proto.CliVersionView.newBuilder()
                    .setAgent(Agent.AGENT_CODEX)
                    .setName("Codex")
                    .setVersion("0.166.0")
                    .setVerified(false)
                    .setUnverifiedLabel(unvText)
                    .build(),
            )
            .build()
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalChartFrozen provides true, LocalNav provides nav) {
                    DeviceScreenContent(vm = vm)
                }
            }
        }
        val labelBounds = ui.onAllNodesWithText("Claude Code").onFirst().fetchSemanticsNode().boundsInRoot
        assertTrue("quota label squeezed vertically: $labelBounds", labelBounds.height < 80f)
        ui.onNodeWithText(longMsg).assertIsDisplayed()
        ui.onNodeWithText("未验证 · 0.166.0").performScrollTo().assertIsDisplayed()
        ui.onAllNodesWithText("·  ·", substring = true).assertCountEquals(0)
    }

    @Test
    fun coreHostDispatchesSettingsAppearanceDirectAndRenameEffects() = runBlocking {
        val remote = Remote(
            Account("https://example.com", ByteArray(32), ByteArray(32), "self-client"),
            Api.defaultHttp(),
        )
        try {
            var directPref: Pair<String, Boolean>? = null
            remote.coreHost.onDirectPreference = { eff -> directPref = eff.machineId to eff.enabled }
            var checkUpdatesCalled = 0
            remote.coreHost.onCheckUpdates = { checkUpdatesCalled++ }
            remote.showForTest(
                RemoteState(
                    conn = ConnState.ONLINE,
                    loaded = true,
                    authorityDeviceId = "self-client",
                    isAuthority = true,
                    devices = mapOf(
                        "self-client" to DeviceView(
                            "self-client",
                            DeviceKind.DEVICE_KIND_CLIENT,
                            1_000L,
                            DeviceMeta.newBuilder().setName("My Phone").setPlatform("android").build(),
                        ),
                        "m1" to DeviceView("m1", DeviceKind.DEVICE_KIND_MACHINE, 1_000L),
                    ),
                    machines = mapOf(
                        "m1" to MachineView(
                            "m1",
                            online = true,
                            lastSeenMs = 1_000L,
                            meta = MachineMeta.newBuilder().setHostname("BuildBox").setProtocolVersion(PEER_PROTOCOL_VERSION).build(),
                        ),
                    ),
                ),
            )
            remote.coreHost.openSettings()
            remote.coreHost.setAppearancePref(ThemePreference.THEME_PREFERENCE_DARK)
            withTimeout(3000) {
                while (remote.settingsView.value.appearance.theme != ThemePreference.THEME_PREFERENCE_DARK) {
                    kotlinx.coroutines.delay(15)
                }
            }
            assertEquals(ThemePreference.THEME_PREFERENCE_DARK, remote.settingsView.value.appearance.theme)

            remote.coreHost.checkForUpdates()
            withTimeout(3000) {
                while (checkUpdatesCalled == 0) {
                    kotlinx.coroutines.delay(15)
                }
            }
            assertTrue(checkUpdatesCalled >= 1)

            remote.coreHost.updateDirectPreference("m1", false)
            withTimeout(3000) {
                while (directPref == null) {
                    kotlinx.coroutines.delay(15)
                }
            }
            assertEquals("m1" to false, directPref)

            var renameEff: io.github.lewin671.remoteagent.core.proto.RenameClientEffect? = null
            remote.coreHost.onRenameClient = { renameEff = it }
            remote.coreHost.openClientDetail("self-client")
            remote.coreHost.renameClient("self-client", "Desk Phone")
            withTimeout(3000) {
                while (renameEff == null) {
                    kotlinx.coroutines.delay(15)
                }
            }
            assertEquals("self-client", renameEff!!.deviceId)
            assertEquals("Desk Phone", renameEff!!.name)
        } finally {
            remote.close()
        }
    }
}
