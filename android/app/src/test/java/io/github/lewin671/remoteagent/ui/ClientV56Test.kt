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
    @get:Rule val ui = createComposeRule()

    private val now = System.currentTimeMillis()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "test-client"), Api.defaultHttp())

    @After
    fun close() {
        remote.close()
    }

    @Test
    fun modeSheetLocksAllModesWhenModeLockedIsTrue() {
        val session = SessionView(
            "s1", "m1",
            Session.newBuilder()
                .setId("s1")
                .setCwd("/work")
                .setAgent(Agent.AGENT_CODEX)
                .setMode(ApprovalMode.APPROVAL_MODE_FULL_ACCESS)
                .setModeLocked(true)
                .build(),
        )
        remote.showForTest(RemoteState())
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote) {
                    ModeSheet(session) {}
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
        val session = SessionView(
            "s2", "m1",
            Session.newBuilder()
                .setId("s2")
                .setCwd("/work")
                .setAgent(Agent.AGENT_ANTIGRAVITY)
                .setMode(ApprovalMode.APPROVAL_MODE_ASK)
                .build(),
        )
        val models = ListModelsResponse.newBuilder()
            .addModels(ModelInfo.newBuilder().setSlug("gemini-2.5-pro").setAutoUnavailable(true))
            .addModels(ModelInfo.newBuilder().setSlug("gemini-2.5-flash").setAutoUnavailable(true))
            .build()
        remote.showForTest(RemoteState(), modelLists = mapOf(("m1" to Agent.AGENT_ANTIGRAVITY) to models))
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote) {
                    ModeSheet(session) {}
                }
            }
        }

        ui.waitUntil(5000) {
            runCatching {
                ui.onNodeWithText("当前账户不支持自动模式").assertIsDisplayed()
                true
            }.getOrDefault(false)
        }
        ui.onNodeWithText("自动").assertIsNotEnabled()
        ui.onNodeWithText("当前账户不支持自动模式").assertIsDisplayed()
    }

    @Test
    fun modelSheetShowsReadOnlyRowsForAntigravity() {
        val session = SessionView(
            "s3", "m1",
            Session.newBuilder()
                .setId("s3")
                .setCwd("/work")
                .setAgent(Agent.AGENT_ANTIGRAVITY)
                .setModel("gemini-2.5-pro")
                .setEffort("high")
                .build(),
        )
        remote.showForTest(RemoteState())
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote) {
                    ModelSheet(RemoteState(), session, running = false) {}
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
    fun quotaSummaryLineFormatsTightestWindowWithMutedWindowName() {
        val qCodex = AgentQuota.newBuilder().setAgent(Agent.AGENT_CODEX).setState(QuotaState.QUOTA_STATE_OK).setObservedMs(now)
            .addWindows(QuotaWindow.newBuilder().setName("primary").setWindowMinutes(300).setUsedPercent(62.0).setResetsAtMs(now + 3 * 3600_000))
            .addWindows(QuotaWindow.newBuilder().setName("secondary").setWindowMinutes(10080).setUsedPercent(34.0).setResetsAtMs(now + 3 * 86400_000))
            .build()
        val m = MachineView("m1", true, now, MachineMeta.newBuilder().addQuotas(qCodex).addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX)).build())
        val g = quotaGroups(m, now).single { it.agent == Agent.AGENT_CODEX }
        assertEquals("已用 62% · 5 小时", quotaLine(g))

        val qAgy = AgentQuota.newBuilder().setAgent(Agent.AGENT_ANTIGRAVITY).setState(QuotaState.QUOTA_STATE_OK).setObservedMs(now)
            .addWindows(QuotaWindow.newBuilder().setName("gemini-5h").setWindowMinutes(300).setUsedPercent(24.0).setResetsAtMs(now + 4 * 3600_000))
            .addWindows(QuotaWindow.newBuilder().setName("gemini-weekly").setWindowMinutes(10080).setUsedPercent(12.0).setResetsAtMs(now + 6 * 86400_000))
            .addWindows(QuotaWindow.newBuilder().setName("3p-5h").setWindowMinutes(300).setUsedPercent(50.0).setResetsAtMs(now + 2 * 3600_000))
            .build()
        val mAgy = MachineView("m1", true, now, MachineMeta.newBuilder().addQuotas(qAgy).addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_ANTIGRAVITY)).build())
        val gAgy = quotaGroups(mAgy, now).single { it.agent == Agent.AGENT_ANTIGRAVITY }
        assertEquals("已用 50% · 5 小时", quotaLine(gAgy))
    }
}
