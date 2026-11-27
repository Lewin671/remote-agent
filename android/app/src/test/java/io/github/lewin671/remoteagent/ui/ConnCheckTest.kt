package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.graphics.Bitmap
import android.graphics.Canvas
import android.view.inspector.WindowInspector
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.proto.ConnCheckSummaryView
import io.github.lewin671.remoteagent.core.proto.ConnProbeState
import io.github.lewin671.remoteagent.core.proto.ConnState as CoreConnState
import io.github.lewin671.remoteagent.core.proto.MachineLatencyState
import io.github.lewin671.remoteagent.core.proto.MachineLatencyView
import io.github.lewin671.remoteagent.core.proto.MachineListCardView
import io.github.lewin671.remoteagent.core.proto.SettingsViewModel
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.data.ThemeMode
import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], qualifiers = "w390dp-h844dp-xxhdpi", application = Application::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ConnCheckTest {
    @get:org.junit.Rule val batchCResources = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    @get:Rule val ui = createComposeRule()
    private var serverCalls = 0
    private var startCalls = 0
    private var stopCalls = 0
    private var reconnectCalls = 0
    private val events = mutableListOf<String>()
    private var clockMs = 10_000L
    private val machine = "test-machine"
    private val hint = "实时推送未连接，会话变化可能延迟。"

    private fun lit(s: String) = CoreText.newBuilder().setLiteral(s).build()

    private fun settingsVm(
        connState: CoreConnState = CoreConnState.CONN_STATE_OFFLINE,
        networkAvailable: Boolean = true,
        everOnline: Boolean = true,
        connStep: String = "",
        connStartedAtMs: Long = 0L,
        lastFailure: String = "",
        directReady: Boolean = false,
        machinesBusy: Boolean = false,
        machineLatency: MachineLatencyView? = MachineLatencyView.newBuilder()
            .setState(MachineLatencyState.MACHINE_LATENCY_STATE_DONE)
            .setValueText(lit("34 ms"))
            .build(),
    ): SettingsViewModel {
        val cc = ConnCheckSummaryView.newBuilder()
            .setConnState(connState)
            .setNetworkAvailable(networkAvailable)
            .setLivePushConnected(connState == CoreConnState.CONN_STATE_CONNECTED)
            .setEverOnline(everOnline)
            .setConnStartedAtMs(connStartedAtMs)
            .setIsConnecting(connState == CoreConnState.CONN_STATE_CONNECTING || connState == CoreConnState.CONN_STATE_RECONNECTING)
            .setMachinesBusy(machinesBusy)
        if (connStep.isNotEmpty()) cc.setConnStep(lit(connStep))
        if (lastFailure.isNotEmpty()) cc.setLastFailureText(lit(lastFailure))
        val m = MachineListCardView.newBuilder()
            .setMachineId(machine)
            .setName(machine)
            .setOnline(true)
            .setMember(true)
            .setDirectReady(directReady)
        if (machineLatency != null) m.setLatency(machineLatency)
        return SettingsViewModel.newBuilder()
            .setOnline(connState == CoreConnState.CONN_STATE_CONNECTED)
            .setConnCheck(cc)
            .addMachines(m)
            .build()
    }

    private fun show(
        theme: ThemeMode = ThemeMode.LIGHT,
        serverLatencyMs: Long = 12L,
        vm: () -> SettingsViewModel,
    ) {
        ui.setContent {
            RaTheme(theme) {
                Box(Modifier.fillMaxSize().background(pal.paper)) {
                    ConnCheckSheet(
                        settingsView = vm(),
                        onClose = {},
                        serverAddress = "https://example.com",
                        pingServer = { serverCalls++; serverLatencyMs },
                        reconnectNow = { reconnectCalls++ },
                        clock = { clockMs },
                        onProbeStatus = { s, ms, err -> events += "status:${s.name}:$ms:$err" },
                        onStartCheck = { startCalls++; events += "start" },
                        onStopCheck = { stopCalls++; events += "stop" },
                    )
                }
            }
        }
    }

    @Test fun disconnectedPushStaysVisibleWhenRoundTripsSucceedAndRecoveryDoesNotRerunProbes() {
        val failure = "同步设备状态超时，正在重连"
        var vm by mutableStateOf(settingsVm(connState = CoreConnState.CONN_STATE_RECONNECTING, lastFailure = failure))
        show { vm }
        ui.onNodeWithText("12 ms").assertExists()
        ui.onNodeWithText("34 ms").assertExists()
        ui.onNodeWithText("重连中").assertExists()
        ui.onNodeWithText(hint + "最近一次失败：$failure").assertExists()
        ui.onNodeWithText("立即重连").performScrollTo().performClick()
        ui.runOnIdle {
            assertEquals(1, reconnectCalls)
            assertEquals(3, serverCalls)
            assertEquals(1, startCalls)
            assertEquals(
                listOf(
                    "status:CONN_PROBE_STATE_CHECKING:0:",
                    "start",
                    "status:CONN_PROBE_STATE_WORKING:12:",
                ),
                events,
            )
        }
        val push = ui.onNodeWithText("实时推送").fetchSemanticsNode().boundsInRoot
        val network = ui.onNodeWithText("本机网络").fetchSemanticsNode().boundsInRoot
        assertTrue(push.top < network.top)
        ui.runOnIdle {
            assertEquals(3, serverCalls)
            assertEquals(1, startCalls)
            vm = settingsVm(connState = CoreConnState.CONN_STATE_CONNECTED, lastFailure = "")
        }
        ui.onNodeWithText("已连接").assertExists()
        ui.onNodeWithText(hint, substring = true).assertDoesNotExist()
        ui.onNodeWithText("立即重连").assertDoesNotExist()
        ui.runOnIdle {
            assertEquals(3, serverCalls)
            assertEquals(1, startCalls)
        }
        ui.onNodeWithText("再测一次").performScrollTo().performClick()
        ui.runOnIdle {
            assertEquals(6, serverCalls)
            assertEquals(2, startCalls)
            assertEquals("status:CONN_PROBE_STATE_CHECKING:0:", events[3])
            assertEquals("start", events[4])
            assertEquals("status:CONN_PROBE_STATE_WORKING:12:", events[5])
        }
    }

    @Test fun emitsStopOnCloseAndDisablesTestAgainWhileMachinesBusy() {
        var open by mutableStateOf(true)
        var vm by mutableStateOf(
            settingsVm(
                connState = CoreConnState.CONN_STATE_CONNECTED,
                machinesBusy = true,
                machineLatency = MachineLatencyView.newBuilder()
                    .setState(MachineLatencyState.MACHINE_LATENCY_STATE_MEASURING)
                    .setValueText(lit("检测中…"))
                    .build(),
            ),
        )
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                if (open) {
                    ConnCheckSheet(
                        settingsView = vm,
                        onClose = { open = false },
                        serverAddress = "https://example.com",
                        pingServer = { serverCalls++; 12L },
                        reconnectNow = {},
                        clock = { clockMs },
                        onProbeStatus = { s, ms, err -> events += "status:${s.name}:$ms:$err" },
                        onStartCheck = { startCalls++; events += "start" },
                        onStopCheck = { stopCalls++; events += "stop" },
                    )
                }
            }
        }
        ui.waitForIdle()
        ui.onNodeWithText("再测一次").performScrollTo().assertIsNotEnabled()
        assertEquals(0, stopCalls)

        ui.runOnIdle { vm = settingsVm(connState = CoreConnState.CONN_STATE_CONNECTED, machinesBusy = false) }
        ui.waitForIdle()
        ui.onNodeWithText("再测一次").performScrollTo().assertIsEnabled()

        ui.runOnIdle { open = false }
        ui.waitForIdle()
        assertEquals(1, stopCalls)
        assertEquals("stop", events.last())
    }

    @Test fun initialWaitingIsShownWithoutAnEmptyFailureSentence() {
        show { settingsVm(connState = CoreConnState.CONN_STATE_CONNECTING, everOnline = false, connStep = "登录", connStartedAtMs = 7000) }
        ui.onNodeWithText("实时推送").assertExists()
        ui.onNodeWithText("连接中").assertExists()
        ui.onNodeWithText("正在连接：登录，已 3 秒。").assertExists()
        ui.onNodeWithText(hint, substring = true).assertDoesNotExist()
        ui.onNodeWithText("最近一次失败：", substring = true).assertDoesNotExist()
    }

    @Test fun reconnectProgressTicksAndChangesStepUntilFailure() {
        var vm by mutableStateOf(settingsVm(connState = CoreConnState.CONN_STATE_CONNECTING, everOnline = true, connStep = "读取本机状态", connStartedAtMs = 7000))
        show { vm }
        ui.onNodeWithText("正在重连：读取本机状态，已 3 秒。").assertExists()
        ui.onNodeWithText(hint, substring = true).assertDoesNotExist()
        ui.runOnIdle { clockMs += 1000 }
        ui.mainClock.advanceTimeBy(1000)
        ui.onNodeWithText("正在重连：读取本机状态，已 4 秒。").assertExists()
        ui.runOnIdle { vm = settingsVm(connState = CoreConnState.CONN_STATE_CONNECTING, everOnline = true, connStep = "建立推送连接", connStartedAtMs = 7000) }
        ui.onNodeWithText("正在重连：建立推送连接，已 4 秒。").assertExists()
        ui.runOnIdle { vm = settingsVm(connState = CoreConnState.CONN_STATE_CONNECTING, everOnline = true, connStep = "建立推送连接", connStartedAtMs = clockMs) }
        ui.onNodeWithText("正在重连：建立推送连接，已 0 秒。").assertExists()
        ui.runOnIdle { vm = settingsVm(connState = CoreConnState.CONN_STATE_OFFLINE, everOnline = true, lastFailure = "没有回应") }
        ui.onNodeWithText(hint + "最近一次失败：没有回应").assertExists()
        ui.onNodeWithText("正在重连：", substring = true).assertDoesNotExist()
        ui.runOnIdle { vm = settingsVm(connState = CoreConnState.CONN_STATE_CONNECTING, everOnline = true, connStep = "登录", connStartedAtMs = clockMs, lastFailure = "没有回应") }
        ui.onNodeWithText("正在重连：登录，已 0 秒。上一次失败：没有回应").assertExists()
        ui.onNodeWithText(hint, substring = true).assertDoesNotExist()
    }

    @Test fun unreachableInitialStreamUsesExistingFailureWording() {
        show { settingsVm(connState = CoreConnState.CONN_STATE_OFFLINE, everOnline = false, lastFailure = "没有回应") }
        ui.onNodeWithText("连不上").assertExists()
        ui.onNodeWithText(hint + "最近一次失败：没有回应").assertExists()
        ui.onNodeWithText("12 ms").assertExists()
    }

    @Test fun onlineStreamHasNoDisconnectedHintEvenWithAnEarlierError() {
        show { settingsVm(connState = CoreConnState.CONN_STATE_CONNECTED, lastFailure = "earlier failure") }
        ui.onNodeWithText("实时推送").assertExists()
        ui.onNodeWithText("已连接").assertExists()
        ui.onNodeWithText(hint, substring = true).assertDoesNotExist()
        ui.onNodeWithText("earlier failure", substring = true).assertDoesNotExist()
    }

    @Test fun rendersCoreProvidedFooterTextAndMachineDisambiguator() {
        val footer = "延时是一次往返的总耗时。电脑一行下面分成网络和电脑处理两段，慢的那段会标出来。"
        val vm = SettingsViewModel.newBuilder()
            .setOnline(true)
            .setConnCheck(
                ConnCheckSummaryView.newBuilder()
                    .setConnState(CoreConnState.CONN_STATE_CONNECTED)
                    .setNetworkAvailable(true)
                    .setLivePushConnected(true)
                    .setFooterText(lit(footer)),
            )
            .addMachines(
                MachineListCardView.newBuilder()
                    .setMachineId(machine)
                    .setName("MacBook")
                    .setDisambiguator("74c7")
                    .setOnline(true)
                    .setMember(true)
                    .setDirectReady(true)
                    .setLatency(
                        MachineLatencyView.newBuilder()
                            .setState(MachineLatencyState.MACHINE_LATENCY_STATE_DONE)
                            .setValueText(lit("38 ms")),
                    ),
            )
            .build()
        show { vm }
        ui.onNodeWithText("MacBook").assertExists()
        ui.onNodeWithText("74c7").assertExists()
        ui.onNodeWithText(footer).performScrollTo().assertExists()
    }

    @Test fun rendersGoldenConnCheckBreakdownSnapshotInLightAndDark() {
        val goldenVm = ScreenGoldenLoader.settingsView("settings_overview", "connCheckBreakdown")
        var currentVm by mutableStateOf(goldenVm)
        var theme by mutableStateOf(ThemeMode.LIGHT)
        var locale by mutableStateOf("zh-CN")
        var srvMs by mutableStateOf(86L)
        ui.setContent {
            androidx.compose.runtime.CompositionLocalProvider(LocalMessageLocale provides locale) {
                RaTheme(theme) {
                    Box(Modifier.fillMaxSize().background(pal.paper)) {
                        ConnCheckSheet(
                            settingsView = currentVm,
                            onClose = {},
                            serverAddress = "https://relay.example.com",
                            pingServer = { srvMs },
                            reconnectNow = {},
                            clock = { clockMs },
                        )
                    }
                }
            }
        }
        ui.waitForIdle()

        // 1. MacBook: direct normal
        ui.onNodeWithText("MacBook").assertExists()
        ui.onNodeWithText("38 ms").assertExists()
        ui.onNodeWithText("6 ms").assertExists()
        ui.onNodeWithText("21 ms").assertExists()

        // 2. Studio: slow computer with queued detail
        ui.onNodeWithText("Studio").assertExists()
        ui.onNodeWithText("1,250 ms · 较慢").assertExists()
        ui.onNodeWithText("电脑处理 · 较慢").assertExists()
        ui.onNodeWithText("1,190 ms").assertExists()
        ui.onNodeWithText("主要在排队（1,150 ms），电脑正忙于别的任务").assertExists()

        // 3. Workstation: rerouted with wait-direct segment & summary override
        ui.onNodeWithText("Workstation").performScrollTo().assertExists()
        ui.onNodeWithText("直连没有及时回应，这一次改走了中继").assertExists()
        ui.onNodeWithText("3,420 ms · 较慢").assertExists()
        ui.onNodeWithText("等直连回应 · 较慢").assertExists()
        ui.onNodeWithText("3,046 ms").assertExists()

        // 4. Lab: outdated with note
        ui.onNodeWithText("Lab").performScrollTo().assertExists()
        ui.onNodeWithText("版本较旧").assertExists()
        ui.onNodeWithText("更新电脑上的 remote-agent 后，可显示网络往返和电脑处理的耗时").assertExists()

        // 5. VPS-dev: computer timeout with slow network & stuck computer segment
        ui.onNodeWithText("VPS-dev").performScrollTo().assertExists()
        ui.onNodeWithText("电脑没有及时回应").assertExists()
        ui.onNodeWithText("网络往返 · 较慢").assertExists()
        ui.onNodeWithText("520 ms").assertExists()
        ui.onNodeWithText("超过 15 秒").assertExists()

        // Footer
        ui.onNodeWithText("延时是一次往返的总耗时。电脑一行下面分成网络和电脑处理两段，慢的那段会标出来。", substring = true)
            .performScrollTo()
            .assertExists()

        // Merged accessibility description on a computer row
        val studio = goldenVm.machinesList.first { it.name == "Studio" }
        assertTrue(studio.latency.hasA11YText())
        ui.onNodeWithContentDescription("Studio", substring = true).assertExists()

        val shotDir = System.getenv("RA_CONNCHECK_SHOTS")
        if (!shotDir.isNullOrEmpty()) {
            fun capture(fileName: String) {
                ui.waitForIdle()
                ui.runOnIdle {
                    val view = WindowInspector.getGlobalWindowViews().last()
                    val bitmap = Bitmap.createBitmap(view.width, view.height, Bitmap.Config.ARGB_8888)
                    view.draw(Canvas(bitmap))
                    File(shotDir, fileName).apply { parentFile!!.mkdirs() }.outputStream().use {
                        assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
                    }
                }
            }
            // Capture light mode (top & scrolled bottom)
            ui.onNodeWithText("连接检测").performScrollTo()
            capture("conncheck-golden-light-top.png")
            ui.onNodeWithText("再测一次").performScrollTo()
            capture("conncheck-golden-light-bottom.png")

            // Capture dark mode (top & scrolled bottom)
            ui.runOnIdle { theme = ThemeMode.DARK }
            ui.onNodeWithText("连接检测").performScrollTo()
            capture("conncheck-golden-dark-top.png")
            ui.onNodeWithText("再测一次").performScrollTo()
            capture("conncheck-golden-dark-bottom.png")

            // Capture English light mode
            ui.runOnIdle {
                theme = ThemeMode.LIGHT
                locale = "en"
            }
            ui.onNodeWithText("Done").performScrollTo()
            capture("conncheck-golden-en-light-top.png")

            // Capture slow server (faint dot, NOT orange) + measuring (with placeholder segments & busy button) + offline
            val mixedVm = goldenVm.toBuilder()
                .setConnCheck(goldenVm.connCheck.toBuilder().setMachinesBusy(true))
                .clearMachines()
                .addMachines(goldenVm.machinesList.first { it.name == "Studio" })
                .addMachines(
                    MachineListCardView.newBuilder()
                        .setMachineId("m-measuring")
                        .setName("MacBook")
                        .setOnline(true)
                        .setMember(true)
                        .setDirectRow(goldenVm.machinesList.first { it.name == "MacBook" }.directRow)
                        .setLatency(
                            MachineLatencyView.newBuilder()
                                .setState(MachineLatencyState.MACHINE_LATENCY_STATE_MEASURING)
                                .setValueText(lit("检测中…"))
                                .addSegments(
                                    io.github.lewin671.remoteagent.core.proto.MachineLatencySegment.newBuilder()
                                        .setLabelText(lit("网络往返"))
                                        .setValueText(lit("6 ms")),
                                )
                                .addSegments(
                                    io.github.lewin671.remoteagent.core.proto.MachineLatencySegment.newBuilder()
                                        .setLabelText(lit("电脑处理"))
                                        .setValueText(lit("—")),
                                ),
                        ),
                )
                .addMachines(
                    MachineListCardView.newBuilder()
                        .setMachineId("m-offline")
                        .setName("Mac mini")
                        .setOnline(false)
                        .setMember(true),
                )
                .build()
            ui.runOnIdle {
                locale = "zh-CN"
                srvMs = 640L
                currentVm = mixedVm.toBuilder()
                    .setConnCheck(mixedVm.connCheck.toBuilder().setMachinesBusy(false))
                    .build()
            }
            ui.onNodeWithText("再测一次").performScrollTo().performClick()
            ui.runOnIdle { currentVm = mixedVm }
            ui.onNodeWithText("连接检测").performScrollTo()
            capture("conncheck-slow-server-measuring-light.png")
        }
    }
}
