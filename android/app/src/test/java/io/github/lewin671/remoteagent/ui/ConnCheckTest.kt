package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.proto.ConnCheckSummaryView
import io.github.lewin671.remoteagent.core.proto.ConnState as CoreConnState
import io.github.lewin671.remoteagent.core.proto.MachineListCardView
import io.github.lewin671.remoteagent.core.proto.SettingsViewModel
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class ConnCheckTest {
    @get:org.junit.Rule val batchCResources = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    @get:Rule val ui = createComposeRule()
    private var serverCalls = 0
    private var machineCalls = 0
    private var reconnectCalls = 0
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
    ): SettingsViewModel {
        val cc = ConnCheckSummaryView.newBuilder()
            .setConnState(connState)
            .setNetworkAvailable(networkAvailable)
            .setLivePushConnected(connState == CoreConnState.CONN_STATE_CONNECTED)
            .setEverOnline(everOnline)
            .setConnStartedAtMs(connStartedAtMs)
            .setIsConnecting(connState == CoreConnState.CONN_STATE_CONNECTING || connState == CoreConnState.CONN_STATE_RECONNECTING)
        if (connStep.isNotEmpty()) cc.setConnStep(lit(connStep))
        if (lastFailure.isNotEmpty()) cc.setLastFailureText(lit(lastFailure))
        return SettingsViewModel.newBuilder()
            .setOnline(connState == CoreConnState.CONN_STATE_CONNECTED)
            .setConnCheck(cc)
            .addMachines(
                MachineListCardView.newBuilder()
                    .setMachineId(machine)
                    .setName(machine)
                    .setOnline(true)
                    .setMember(true)
                    .setDirectReady(directReady),
            )
            .build()
    }

    private fun show(vm: () -> SettingsViewModel) {
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ConnCheckSheet(
                    settingsView = vm(),
                    onClose = {},
                    serverAddress = "https://example.com",
                    onlineMachines = { listOf(machine) },
                    pingServer = { serverCalls++; 12L },
                    pingMachine = { id -> assertEquals(machine, id); machineCalls++; 34L },
                    reconnectNow = { reconnectCalls++ },
                    clock = { clockMs },
                )
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
            assertEquals(1, machineCalls)
        }
        val push = ui.onNodeWithText("实时推送").fetchSemanticsNode().boundsInRoot
        val network = ui.onNodeWithText("本机网络").fetchSemanticsNode().boundsInRoot
        assertTrue(push.top < network.top)
        ui.runOnIdle {
            assertEquals(3, serverCalls)
            assertEquals(1, machineCalls)
            vm = settingsVm(connState = CoreConnState.CONN_STATE_CONNECTED, lastFailure = "")
        }
        ui.onNodeWithText("已连接").assertExists()
        ui.onNodeWithText(hint, substring = true).assertDoesNotExist()
        ui.onNodeWithText("立即重连").assertDoesNotExist()
        ui.runOnIdle {
            assertEquals(3, serverCalls)
            assertEquals(1, machineCalls)
        }
        ui.onNodeWithText("再测一次").performScrollTo().performClick()
        ui.runOnIdle {
            assertEquals(6, serverCalls)
            assertEquals(2, machineCalls)
        }
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
}
