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
        val meta = MachineMeta.newBuilder().addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setDefaultMode(ApprovalMode.APPROVAL_MODE_PLAN)).build()
        val state = RemoteState(machines=mapOf("m" to MachineView("m",true,0,meta)))
        assertTrue(schedulePlanBlocked(false,config.copy(mode=null),state))
        assertEquals(Mode.PLAN,config.mode)
    }
}
