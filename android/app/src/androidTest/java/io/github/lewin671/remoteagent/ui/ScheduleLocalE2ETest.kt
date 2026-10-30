package io.github.lewin671.remoteagent.ui

import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.MainActivity
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.Dispatchers
import java.io.File
import org.junit.Assume.assumeTrue
import org.junit.Rule
import org.junit.Test

/** Opt-in local relay check. Arguments and the pairing handoff stay outside the repository. */
class ScheduleLocalE2ETest {
    @get:Rule val ui = createAndroidComposeRule<MainActivity>()
    private fun waitText(text: String, timeout: Long = 30_000) {
        try { ui.waitUntil(timeout) { ui.onAllNodesWithText(text, substring = true).fetchSemanticsNodes().isNotEmpty() } }
        catch (e: Throwable) {
            val dir=File(InstrumentationRegistry.getInstrumentation().targetContext.getExternalFilesDir(null),"schedule-e2e").apply { mkdirs() }
            File(dir,"failure-semantics.txt").writeText(ui.onRoot().printToString())
            File(dir,"failure.png").outputStream().use { requireNotNull(InstrumentationRegistry.getInstrumentation().uiAutomation.takeScreenshot()).compress(android.graphics.Bitmap.CompressFormat.PNG,100,it) }
            throw e
        }
    }
    @Test fun createRunOpenEditAndScheduleCurrentConversation() {
        val args = InstrumentationRegistry.getArguments()
        val server = args.getString("scheduleServer").orEmpty()
        assumeTrue("requires isolated dev-env", server.isNotEmpty())
        val output = File(InstrumentationRegistry.getInstrumentation().targetContext.getExternalFilesDir(null), "schedule-e2e").apply { mkdirs() }
        fun capture(name: String) {
            ui.waitForIdle()
            File(output,"$name.png").outputStream().use { requireNotNull(InstrumentationRegistry.getInstrumentation().uiAutomation.takeScreenshot()).compress(android.graphics.Bitmap.CompressFormat.PNG,100,it) }
        }
        ui.waitUntil(30_000) { RaApp.instance.remote.value != null || ui.onAllNodesWithText("新建账户").fetchSemanticsNodes().isNotEmpty() }
        if (RaApp.instance.remote.value == null) {
            ui.onNodeWithText("新建账户").performClick()
            ui.onAllNodes(hasSetTextAction())[0].performTextReplacement(server)
            ui.onAllNodes(hasSetTextAction())[1].performTextReplacement(args.getString("scheduleSetupToken").orEmpty())
            ui.onNodeWithText("创建账户").performScrollTo().performClick()
            waitText("我已保存，开始使用")
            ui.onNodeWithText("我已保存，开始使用").performScrollTo().performClick()
        }
        ui.waitUntil(30_000) { RaApp.instance.remote.value != null }
        check(RaApp.instance.remote.value?.account?.server == server) { "E2E may only use its isolated account" }
        if (args.getString("scheduleAlreadyPaired") != "true") {
        File(output,"host-confirmed").delete()
        waitText("设置")
        ui.onNodeWithText("设置").performClick()
        waitText("添加设备")
        ui.onNodeWithText("添加设备").performScrollTo().performClick()
        waitText("等待新设备")
        val code = ui.onAllNodes(hasText("",substring=true)).fetchSemanticsNodes().flatMap { it.config.getOrElse(androidx.compose.ui.semantics.SemanticsProperties.Text) { emptyList() } }.map { it.text }.first { Regex("[A-Z0-9]{4} [A-Z0-9]{4}").matches(it) }
        File(output,"pair-code").writeText(code.replace(" ",""))
        waitText("允许加入",120_000)
        val comparison=RaApp.instance.pairing(requireNotNull(RaApp.instance.remote.value),"").state.value as PairingUiState.Compare
        File(output,"pair-emoji").writeText(comparison.emoji.joinToString(" "))
        ui.waitUntil(120_000) { File(output,"host-confirmed").exists() }
        ui.onNodeWithText("允许加入").performClick()
        waitText("新设备已加入",60_000)
        ui.onNodeWithText("完成").performClick()
        }
        val remote = requireNotNull(RaApp.instance.remote.value)
        ui.waitUntil(60_000) { remote.state.value.machines.values.any { it.online } }
        val machine=remote.state.value.machines.values.first { it.online }.id
        // Seed a throwaway project preference; task creation and every task operation use the UI.
        runBlocking(Dispatchers.IO) {
            val project=requireNotNull(args.getString("scheduleProject"))
            remote.changePrefs(PrefsOp.SetSettings(project,machine,agent=Agent.AGENT_CLAUDE,sets=listOf(AgentSettings.newBuilder().setAgent(Agent.AGENT_CLAUDE).setModel("haiku").setMode(ApprovalMode.APPROVAL_MODE_AUTO).build())))
            remote.changePrefs(PrefsOp.Pin(project))
        }
        ui.onNodeWithText("定时",useUnmergedTree=true).performClick()
        waitText("定时任务")
        ui.onNodeWithContentDescription("新任务").performClick()
        val message = "只回复 SCHEDULE_V52_OK，不调用工具。\n" + (1..10).joinToString("\n") { "$it. 这是本地端到端验证，检查消息保持原文换行。" }
        ui.onAllNodes(hasSetTextAction())[0].performTextReplacement(message)
        ui.onAllNodes(hasSetTextAction())[1].performTextReplacement("本地定时验证")
        ui.onNodeWithText("选择项目").performScrollTo().performClick()
        waitText("schedule-fixture")
        ui.onNodeWithText("schedule-fixture",useUnmergedTree=true).performClick()
        capture("new-task")
        ui.onNodeWithText("创建").performScrollTo().performClick()
        ui.waitUntil(30_000) { ui.onAllNodesWithText("运行记录").fetchSemanticsNodes().isNotEmpty() || ui.onAllNodesWithText("重试创建").fetchSemanticsNodes().isNotEmpty() }
        if (ui.onAllNodesWithText("重试创建").fetchSemanticsNodes().isNotEmpty()) {
            File(output,"create-retry-semantics.txt").writeText(ui.onRoot().printToString(maxDepth=100))
            ui.onNodeWithText("重试创建").performScrollTo().performClick()
        }
        waitText("运行记录",60_000)
        capture("created-detail")
        ui.onNodeWithText("立即运行一次").performScrollTo().performClick()
        waitText("没有通知",120_000)
        ui.onNodeWithText("没有通知",substring=true).performScrollTo().performClick()
        ui.waitUntil(60_000) { ui.onAllNodesWithText("SCHEDULE_V52_OK").fetchSemanticsNodes().isNotEmpty() }
        File(output,"cli-reply-seen").writeText("SCHEDULE_V52_OK")
        capture("real-cli-reply")
        ui.onNodeWithContentDescription("返回").performClick()
        ui.onNodeWithText("编辑").performScrollTo().performClick()
        ui.onNode(hasSetTextAction()).performTextReplacement("更新后的描述。\n只回复 UPDATED_OK。")
        capture("edited-description")
        ui.onNodeWithText("保存").performClick()
        waitText("更新后的描述")
        File(output,"description-saved").writeText("true")
        capture("saved-description")
        ui.onNodeWithText("没有通知",substring=true).performScrollTo().performClick()
        ui.onNodeWithContentDescription("更多").performClick()
        ui.onNodeWithText("定时发送…").performClick()
        ui.onAllNodes(hasSetTextAction())[0].performTextReplacement("只回复 CURRENT_SCHEDULE_OK。")
        ui.onNodeWithText("创建").performScrollTo().performClick()
        waitText("发到",60_000)
        File(output,"current-task-created").writeText("true")
        capture("current-task-created")
    }
}
