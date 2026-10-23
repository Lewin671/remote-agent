package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Link
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.RemoteState
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
    @get:Rule val ui = createComposeRule()
    private var serverCalls = 0
    private var machineCalls = 0
    private val machine = "test-machine"
    private val waiting = RemoteState(
        conn = ConnState.OFFLINE, link = Link.WAITING, synced = true, loaded = true,
        machines = mapOf(machine to MachineView(machine, true, 0)),
    )
    private val hint = "实时推送未连接，会话变化可能延迟。"

    private fun show(state: () -> RemoteState) {
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ConnCheckSheet(
                    state(), {}, "https://example.com", { listOf(machine) },
                    { serverCalls++; 12L },
                    { id -> assertEquals(machine, id); machineCalls++; 34L },
                )
            }
        }
    }

    @Test fun disconnectedPushStaysVisibleWhenRoundTripsSucceedAndRecoveryDoesNotRerunProbes() {
        val failure = "同步设备状态超时，正在重连"
        var state by mutableStateOf(waiting.copy(error = failure))
        show { state }
        ui.onNodeWithText("12 ms").assertExists()
        ui.onNodeWithText("34 ms").assertExists()
        ui.onNodeWithText("重连中").assertExists()
        ui.onNodeWithText(hint + "最近一次失败：$failure").assertExists()
        val push = ui.onNodeWithText("实时推送").fetchSemanticsNode().boundsInRoot
        val network = ui.onNodeWithText("本机网络").fetchSemanticsNode().boundsInRoot
        assertTrue(push.top < network.top)
        ui.runOnIdle {
            assertEquals(3, serverCalls)
            assertEquals(1, machineCalls)
            state = state.copy(conn = ConnState.ONLINE, link = Link.ONLINE, error = "")
        }
        ui.onNodeWithText("已连接").assertExists()
        ui.onNodeWithText(hint, substring = true).assertDoesNotExist()
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
        show { waiting.copy(conn = ConnState.CONNECTING, synced = false) }
        ui.onNodeWithText("实时推送").assertExists()
        ui.onNodeWithText("连接中").assertExists()
        ui.onNodeWithText(hint).assertExists()
        ui.onNodeWithText("最近一次失败：", substring = true).assertDoesNotExist()
    }

    @Test fun unreachableInitialStreamUsesExistingFailureWording() {
        show { waiting.copy(link = Link.NO_SERVER, synced = false, error = "没有回应") }
        ui.onNodeWithText("连不上").assertExists()
        ui.onNodeWithText(hint + "最近一次失败：没有回应").assertExists()
        ui.onNodeWithText("12 ms").assertExists()
    }

    @Test fun onlineStreamHasNoDisconnectedHintEvenWithAnEarlierError() {
        show { waiting.copy(conn = ConnState.ONLINE, link = Link.ONLINE, error = "earlier failure") }
        ui.onNodeWithText("实时推送").assertExists()
        ui.onNodeWithText("已连接").assertExists()
        ui.onNodeWithText(hint, substring = true).assertDoesNotExist()
        ui.onNodeWithText("earlier failure", substring = true).assertDoesNotExist()
    }
}
