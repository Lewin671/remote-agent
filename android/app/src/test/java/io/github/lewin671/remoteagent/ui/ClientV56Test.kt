package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import org.junit.After
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class ClientV56Test {
    @get:org.junit.Rule val batchCResources = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    @get:Rule val ui = createComposeRule()

    private val now = System.currentTimeMillis()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "test-client"), Api.defaultHttp())

    @After
    fun close() {
        remote.close()
    }

    @Test
    fun modeSheetLocksAllModesWhenModeLockedIsTrue() {
        val lockNote = io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey("modes.locked").build()
        fun choice(m: ApprovalMode, key: String, sel: Boolean = false) = io.github.lewin671.remoteagent.core.proto.SessionModeChoiceView.newBuilder()
            .setMode(m)
            .setLabel(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey(key))
            .setNote(lockNote)
            .setSelected(sel)
            .setAvailable(false)
            .build()
        val vm = io.github.lewin671.remoteagent.core.proto.SessionViewModel.newBuilder()
            .setSessionId("s1")
            .setHeader(io.github.lewin671.remoteagent.core.proto.SessionHeaderView.newBuilder().setFound(true).setMachineId("m1").setAgent(Agent.AGENT_CODEX))
            .setActions(
                io.github.lewin671.remoteagent.core.proto.SessionActionsView.newBuilder()
                    .setCanSwitchMode(false)
                    .setModeSwitchBlockedReason(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("以完全放行启动后本次会话内无法退回需审批的模式；要恢复审批请新建会话。"))
                    .addModeChoices(choice(ApprovalMode.APPROVAL_MODE_ASK, "modes.ask"))
                    .addModeChoices(choice(ApprovalMode.APPROVAL_MODE_AUTO, "modes.auto"))
                    .addModeChoices(choice(ApprovalMode.APPROVAL_MODE_PLAN, "modes.plan"))
                    .addModeChoices(choice(ApprovalMode.APPROVAL_MODE_FULL_ACCESS, "modes.full_access", sel = true)),
            )
            .build()
        remote.showForTest(RemoteState())
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote) {
                    ModeSheet("s1", vm) {}
                }
            }
        }

        ui.onNodeWithText("询问").assertIsNotEnabled()
        ui.onNodeWithText("自动").assertIsNotEnabled()
        ui.onNodeWithText("计划").assertIsNotEnabled()
        ui.onNodeWithText("完全放行").assertIsNotEnabled()
        ui.onAllNodesWithText("本会话以完全放行启动，不能切换审批模式").assertCountEquals(4)
        ui.onNodeWithText("以完全放行启动后本次会话内无法退回需审批的模式；要恢复审批请新建会话。").assertIsDisplayed()
    }

    @Test
    fun modeSheetDisablesAutoModeWhenAllAutoUnavailable() {
        val unavailable = io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey("modes.account_unsupported").build()
        val vm = io.github.lewin671.remoteagent.core.proto.SessionViewModel.newBuilder()
            .setSessionId("s2")
            .setHeader(io.github.lewin671.remoteagent.core.proto.SessionHeaderView.newBuilder().setFound(true).setMachineId("m1").setAgent(Agent.AGENT_ANTIGRAVITY))
            .setActions(
                io.github.lewin671.remoteagent.core.proto.SessionActionsView.newBuilder()
                    .setCanSwitchMode(true)
                    .addModeChoices(
                        io.github.lewin671.remoteagent.core.proto.SessionModeChoiceView.newBuilder()
                            .setMode(ApprovalMode.APPROVAL_MODE_ASK)
                            .setLabel(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey("modes.ask"))
                            .setSelected(true)
                            .setAvailable(true),
                    )
                    .addModeChoices(
                        io.github.lewin671.remoteagent.core.proto.SessionModeChoiceView.newBuilder()
                            .setMode(ApprovalMode.APPROVAL_MODE_AUTO)
                            .setLabel(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey("modes.auto"))
                            .setUnavailableReason(unavailable)
                            .setAvailable(false),
                    ),
            )
            .build()
        remote.showForTest(RemoteState())
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote) {
                    ModeSheet("s2", vm) {}
                }
            }
        }

        ui.onNodeWithText("自动").assertIsNotEnabled()
        ui.onNodeWithText("当前账户不支持自动模式").assertIsDisplayed()
    }

    @Test
    fun modelSheetShowsReadOnlyRowsForAntigravity() {
        val vm = io.github.lewin671.remoteagent.core.proto.SessionViewModel.newBuilder()
            .setSessionId("s3")
            .setHeader(
                io.github.lewin671.remoteagent.core.proto.SessionHeaderView.newBuilder()
                    .setFound(true)
                    .setMachineId("m1")
                    .setAgent(Agent.AGENT_ANTIGRAVITY)
                    .setModel("gemini-2.5-pro")
                    .setModelLabel("gemini-2.5-pro")
                    .setEffort("high"),
            )
            .setActions(
                io.github.lewin671.remoteagent.core.proto.SessionActionsView.newBuilder()
                    .setCanSwitchModel(false)
                    .setModelSwitchBlockedReason(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey("session.model_locked")),
            )
            .build()
        remote.showForTest(RemoteState())
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote) {
                    ModelSheet("s3", vm) {}
                }
            }
        }

        ui.onNodeWithText("运行中不能换模型，新建会话时可选").assertIsDisplayed()
        ui.onNodeWithText("运行中不能换推理强度").assertIsDisplayed()
    }

    @Test
    fun antigravityDoesNotSupportForking() {
        assertFalse(agentInfo(Agent.AGENT_ANTIGRAVITY).forks)
        assertTrue(agentInfo(Agent.AGENT_CODEX).forks)
        assertTrue(agentInfo(Agent.AGENT_CLAUDE).forks)
    }

    @Test
    fun machineQuotaGoldenViewRendersCodexAndAntigravitySections() {
        val allQuotas = ScreenGoldenLoader.quotaView("machine_quota", "allQuotas")
        val codexOnly = ScreenGoldenLoader.quotaView("machine_quota", "codexOnly")
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    QuotaScreenContent(allQuotas)
                }
            }
        }
        assertTrue(allQuotas.found)
        assertTrue(allQuotas.sectionsList.isNotEmpty())
        assertTrue(codexOnly.sectionsList.any { it.agent == Agent.AGENT_CODEX })
        ui.onNodeWithText(allQuotas.machineName, substring = true).assertExists()
    }
}
