package io.github.lewin671.remoteagent.ui

import android.app.Application
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.*
import java.time.ZoneId
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class SchedulePresentationTest {
    @Test fun destinationIsTheSharedPlaceholderUntilInfoLoads() {
        val task=ScheduledTask("m",Json.parseToJsonElement("""{"spec":{"target":{"type":"resume_session","session":"s"}}}""").jsonObject)
        assertEquals("对话",scheduleDestination(task,RemoteState(sessions=mapOf("s" to SessionView("s","m")))))
        assertEquals("修复登录超时",scheduleDestination(task,RemoteState(sessions=mapOf("s" to SessionView("s","m",Session.newBuilder().setTitle("修复登录超时").build())))))
    }
    @Test fun defaultPermissionIsResolvedBeforeCreatingTheTaskTarget() {
        val meta=MachineMeta.newBuilder().addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setDefaultMode(ApprovalMode.APPROVAL_MODE_AUTO).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG)).build()
        val state=RemoteState(machines=mapOf("own" to MachineView("own",true,0,meta)))
        val config=NewConfig("own","/example",Agent.AGENT_CLAUDE)
        val concrete=resolvedScheduleConfig(config.copy(mode=null),state)
        assertEquals("auto",scheduleTarget(concrete).text("mode"))
        assertNull(resolvedScheduleConfig(config.copy(mode=null),RemoteState()).mode)
        assertEquals("full",scheduleTarget(resolvedScheduleConfig(config.copy(mode=Mode.FULL),state)).text("mode"))
    }
    @Test fun editingScopesDefaultsAndSessionsToTheOwningComputer() {
        val meta=MachineMeta.newBuilder().addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setDefaultMode(ApprovalMode.APPROVAL_MODE_AUTO).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG)).build()
        val prefs=ProjectPrefs.newBuilder().addSettings(ProjectSettings.newBuilder().setPath("/example").setMachineId("other").addAgents(AgentSettings.newBuilder().setAgent(Agent.AGENT_CLAUDE).setModel("other-model"))).build()
        val state=RemoteState(machines=mapOf("own" to MachineView("own",true,0,meta),"other" to MachineView("other",true,0,meta)),prefs=PrefsObject(1,prefs),sessions=mapOf("other" to SessionView("other","other",Session.newBuilder().setCwd("/example").setAgent(Agent.AGENT_CLAUDE).setModel("other-session-model").build())))
        val scoped=scheduleEditingState(state,"own")
        val config=projectStart(scoped,"/example",Agent.AGENT_CLAUDE,"own")!!.config
        assertEquals("",config.model);assertTrue(scoped.sessions.isEmpty());assertEquals(setOf("own"),scoped.machines.keys)
    }
    @Test fun canonicalLocalZonesStayImplicitAndOnlyForeignCalendarZonesAreShown() {
        val local = ZoneId.of("America/Los_Angeles")
        fun calendar(zone: String) = buildJsonObject { put("type", "calendar"); put("time", "09:49"); put("timezone", zone) }
        assertEquals("每天 09:49", scheduleTiming(calendar("US/Pacific"), local))
        assertEquals("每天 09:49 · Asia/Shanghai", scheduleTiming(calendar("Asia/Shanghai"), local))
        assertEquals("每小时", scheduleTiming(buildJsonObject { put("type", "interval"); put("interval_seconds", 3600); put("timezone", "Asia/Shanghai") }, local))
        assertTrue(scheduleTiming(buildJsonObject { put("type", "at"); put("at", "2026-10-24T13:00:00Z") }, local).endsWith("06:00，一次"))
    }
    @Test fun locationsDistinguishConversationFromNewProjectAndStripUsesActualState() {
        val task = ScheduledTask("m", Json.parseToJsonElement("""{"id":"t","status":"scheduled","spec":{"trigger":{"type":"interval","interval_seconds":3600},"target":{"type":"resume_session","session":"s"}}}""").jsonObject)
        val machine = MachineView("m", true, 0, MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true)).build())
        val state = RemoteState(conn=ConnState.ONLINE, machines=mapOf("m" to machine), sessions=mapOf("s" to SessionView("s", "m", Session.newBuilder().setTitle("修复登录超时").setMode(ApprovalMode.APPROVAL_MODE_AUTO).build())))
        assertEquals("在「修复登录超时」里", taskLocation(task,state))
        assertEquals("在对话里",taskLocation(task,state.copy(sessions=mapOf("s" to SessionView("s","m")))))
        assertEquals("自动", taskPermission(task,state))
        assertEquals("定时 · 每小时，未连接，显示的是上次的状态", sessionScheduleText(listOf(task),state.copy(conn=ConnState.OFFLINE)))
        val paused = ScheduledTask("m", JsonObject(task.json + ("status" to JsonPrimitive("paused")) + ("reason" to JsonPrimitive("user_paused"))))
        assertEquals("定时 · 每小时，已暂停", sessionScheduleText(listOf(paused),state))
    }
    @Test fun planBlocksNewExecutionWithoutChangingTheSelectedPermission() {
        val config = NewConfig("m","/example/project",Agent.AGENT_CLAUDE,"haiku",mode=Mode.PLAN)
        assertTrue(schedulePlanBlocked(false,config,RemoteState()))
        assertFalse(schedulePlanBlocked(true,config,RemoteState()))
        assertFalse(schedulePlanBlocked(false,config.copy(mode=Mode.AUTO),RemoteState()))
        val meta = MachineMeta.newBuilder().addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setDefaultMode(ApprovalMode.APPROVAL_MODE_PLAN).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG)).build()
        val state = RemoteState(machines=mapOf("m" to MachineView("m",true,0,meta)))
        assertTrue(schedulePlanBlocked(false,config.copy(mode=null),state))
        assertEquals(Mode.PLAN,config.mode)
    }
    @Test fun stoppedBannerCopyMatchesTheSharedRulesExactly() {
        fun task(status: String, reason: String, trigger: String = """{"type":"interval","interval_seconds":3600}""", target: String = """{"type":"new_session"}""") = ScheduledTask("m",buildJsonObject {
            put("status",status); put("reason",reason)
            put("spec",buildJsonObject { put("trigger",Json.parseToJsonElement(trigger));put("target",Json.parseToJsonElement(target)) })
        })
        val recurring="恢复或重新安排后从下一次自动时间开始，不重发上次消息。"
        assertEquals("中继上的会话数已满。$recurring",scheduleAttentionText(task("failed","relay_session_limit")))
        assertEquals("电脑重启前的发送结果无法确认。继续可能重复执行上次的消息，请先打开运行记录核实。",scheduleAttentionText(task("uncertain","restart_dispatching")))
        for (trigger in listOf("""{"type":"at"}""","""{"type":"after_turn","repeat":false}""")) {
            assertEquals("运行失败，任务已停下。可立即额外执行一次，或改时间重排。",scheduleAttentionText(task("failed","execution_failed",trigger)))
            assertEquals("运行失败，任务已停下。继续可能重复执行上次的消息，请先打开运行记录核实。",scheduleAttentionText(task("uncertain","execution_failed",trigger)))
        }
        assertEquals("运行失败，任务已停下。$recurring",scheduleAttentionText(task("failed","execution_failed","""{"type":"after_turn","repeat":true}""")))
        assertEquals("创建任务时的权限已改变，任务已停下。${recurring}继续会由你接管任务，按任务中显示的权限运行。",scheduleAttentionText(task("paused","agent_schedule_permissions_changed")))
        assertEquals("Agent 创建的任务间隔过短，任务已停下。$recurring",scheduleAttentionText(task("paused","agent_schedule_interval_below_minimum")))
        val missing=task("failed","execution_session_missing",target="""{"type":"resume_session","session":"gone"}""")
        val runs=buildJsonArray {
            add(buildJsonObject { put("id","earlier");put("started_at","2026-10-24T10:00:00Z") })
            add(buildJsonObject { put("id","failed");put("scheduled_at","2026-10-24T14:00:00Z");put("started_at","2026-10-24T15:02:00Z") })
        }
        assertEquals("今天 15:02 的运行失败，任务已停下：找不到运行的对话。这个任务只发到原来的对话，恢复后还会失败。",scheduleAttentionText(ScheduledTask("m",JsonObject(missing.json+("runs" to runs))),java.time.Instant.parse("2026-10-24T16:00:00Z"),ZoneId.of("UTC")))
        assertTrue(scheduleTargetMissing(missing))
        val exited=task("failed","execution_session_exited",target="""{"type":"resume_session","session":"gone"}""")
        assertFalse(scheduleTargetMissing(exited))
        assertEquals("运行中的对话已结束。$recurring",scheduleAttentionText(exited))
        assertFalse(scheduleTargetMissing(task("failed","execution_session_missing")))
    }

}
