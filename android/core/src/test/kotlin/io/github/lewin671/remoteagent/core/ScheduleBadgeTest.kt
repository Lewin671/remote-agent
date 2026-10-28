package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class ScheduleBadgeTest {
    private fun task(runs: String = "[]", status: String = "scheduled", reason: String = "", id: String = "t", machine: String = "m") = ScheduledTask(machine, Json.parseToJsonElement("""{"id":"$id","status":"$status","reason":"$reason","runs":$runs}""").jsonObject)
    private fun run(id: String, notified: Boolean = true) = """{"id":"$id","status":"running","notified":$notified}"""
    @Test fun historicalRunsAreSeenAndNewNotificationsCountTasksOnce() {
        val seen = ScheduleSeen()
        val historical = task("[${run("old")}]")
        seen.observe("m", listOf(historical)); assertEquals(0, seen.count(listOf(historical)))
        seen.observe("m", listOf(task("[${run("new", false)}]")))
        val current = task("[${run("new")},${run("another")}]")
        seen.observe("m", listOf(current)); assertEquals(1, seen.count(listOf(current)))
        val failed = task("[${run("new")}]", "failed")
        assertEquals(2, seen.count(listOf(failed, task(status="uncertain", id="other"))))
        seen.setOpen(true); assertEquals(0, seen.count(listOf(current)))
        val arriving = task("[${run("while-open")}]")
        seen.observe("m", listOf(arriving)); assertEquals(0, seen.count(listOf(arriving)))
        seen.setOpen(false)
        val later = task("[${run("later")}]")
        seen.observe("m", listOf(later)); assertEquals(1, seen.count(listOf(later)))
        assertEquals(0, seen.count(listOf(task("[${run("later")}]", "cancelled", "user_cancelled"))))
        assertEquals(0, seen.count(emptyList()))
    }
    @Test fun emptyBaselinePersistsAndKeysIsolateRelayClientAndMachine() {
        val data = mutableMapOf<String,String>()
        fun seen(server: String = "relay-a", account: String = "account-a") = ScheduleSeen(listOf(server,account), { data[it] }, { k,v -> data[k]=v })
        seen().observe("m", emptyList())
        val t = task("[${run("r")}]")
        val reload = seen(); reload.observe("m", listOf(t)); assertEquals(1,reload.count(listOf(t)))
        for (other in listOf(seen(server="relay-b"),seen(account="account-b"))) {
            other.observe("m",listOf(t)); assertEquals(0,other.count(listOf(t)))
        }
        reload.observe("other-machine",listOf(t.copy(machine="other-machine")))
        assertEquals(0,reload.count(listOf(t.copy(machine="other-machine"))))
        reload.setOpen(true)
        val opened = seen(); opened.observe("m",listOf(t)); assertEquals(0,opened.count(listOf(t)))
    }
    @Test fun scheduledCloseComesFromTheLatestStateAndResetsOnResumeOrManualStop() {
        fun state(status: SessionStatus, automatic: Boolean) = Event.newBuilder().setSessionState(SessionState.newBuilder().setStatus(status).setDetail("stopped").setScheduledClosed(automatic)).build()
        val auto = state(SessionStatus.SESSION_STATUS_EXITED,true)
        assertTrue(derive(listOf(auto)).scheduledClosed)
        assertTrue(currentActions(derive(listOf(auto)), null, listOf(auto)).scheduledClosed)
        assertFalse(derive(listOf(state(SessionStatus.SESSION_STATUS_EXITED,false))).scheduledClosed)
        assertFalse(derive(listOf(auto,state(SessionStatus.SESSION_STATUS_STARTING,false))).scheduledClosed)
        assertFalse(derive(listOf(auto,state(SessionStatus.SESSION_STATUS_EXITED,false))).scheduledClosed)
    }
}
