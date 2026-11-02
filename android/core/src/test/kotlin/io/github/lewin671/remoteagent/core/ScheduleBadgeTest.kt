package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class ScheduleBadgeTest {
    private fun task(runs: String = "[]", status: String = "scheduled", reason: String = "", id: String = "t", machine: String = "m") = ScheduledTask(machine, Json.parseToJsonElement("""{"id":"$id","status":"$status","reason":"$reason","runs":$runs}""").jsonObject)
    private fun run(id: String, notified: Boolean = true) = """{"id":"$id","status":"running","notified":$notified}"""
    @Test fun historicalRunsAreSeenAndUnreadCountsRunsRatherThanTasksOrEvents() {
        val seen = ScheduleSeen()
        val historical = task("[${run("old")}]")
        seen.observe("m", listOf(historical)); assertEquals(0, seen.count(listOf(historical)))
        val current = task("[${run("old")},${run("new")},${run("another")},${run("quiet", false)}]")
        seen.observe("m", listOf(current))
        assertEquals(setOf("new", "another"), seen.unreadIds(current))
        assertEquals(2, seen.count(listOf(current)))
        // Repeated notifications within one run remain one unread message.
        val repeated = current.copy(json = JsonObject(current.json + ("active_run" to
            Json.parseToJsonElement("""{"id":"new","notified":true,"notification_count":7}"""))))
        seen.observe("m", listOf(repeated)); assertEquals(2, seen.count(listOf(repeated)))
        val failed = task("[${run("new")},${run("another")} ]", "failed")
        assertEquals(3, seen.count(listOf(failed, task(status="uncertain", id="other"))))
        assertEquals(0, seen.count(listOf(task("[${run("new")}]", "cancelled", "user_cancelled"))))
        assertEquals(0, seen.count(emptyList()))
    }
    @Test fun openingDetailClearsOnlyThatTaskAndCapturesUnreadForThisVisit() {
        val seen = ScheduleSeen()
        seen.observe("m", emptyList())
        val first = task("[${run("first")},${run("second")}]")
        val other = task("[${run("third")} ]", id="other")
        seen.observe("m", listOf(first, other))
        seen.openTask("m", "t")
        assertEquals(1, seen.count(listOf(first,other)))
        assertEquals(setOf("first", "second"), seen.detailUnread)
        val arriving = task("[${run("first")},${run("second")},${run("arriving")}]")
        seen.observe("m",listOf(arriving,other))
        assertTrue(seen.unreadIds(arriving).isEmpty())
        assertEquals(setOf("first", "second"), seen.detailUnread)
        seen.closeTask("m", "t"); assertTrue(seen.detailUnread.isEmpty())
        seen.openTask("m", "t"); assertTrue(seen.detailUnread.isEmpty())
        seen.closeTask("m", "t")
        val later=task("[${run("later")}]")
        seen.observe("m",listOf(later,other)); assertEquals(2,seen.count(listOf(later,other)))
    }
    @Test fun backgroundingTheDetailKeepsItsDotsAndLeavesNewNotificationsUnreadUntilItReturns() {
        val dismissed = mutableListOf<String>()
        val seen = ScheduleSeen(onRead = { runs -> dismissed += runs.map { it.id } })
        seen.observe("m", emptyList())
        seen.observe("m", listOf(task("[${run("before")}]")))
        seen.openTask("m", "t")
        seen.hideTask("m", "t")
        val hidden = task("[${run("before")},${run("hidden")}]")
        seen.observe("m", listOf(hidden))
        assertEquals(setOf("hidden"), seen.unreadIds(hidden))
        assertEquals(setOf("before"), seen.detailUnread)
        assertEquals(listOf("before"), dismissed.distinct())
        seen.openTask("m", "t")
        assertTrue(seen.unreadIds(hidden).isEmpty())
        assertEquals(setOf("before", "hidden"), seen.detailUnread)
        assertEquals(listOf("before", "hidden"), dismissed.distinct())
        seen.closeTask("m", "t"); assertTrue(seen.detailUnread.isEmpty())
    }
    @Test fun detailOpenedBeforeTheFirstSnapshotCapturesPersistedUnreadWhenItLoads() {
        val seen=ScheduleSeen(read={ "[]" })
        seen.openTask("m","t")
        val t=task("[${run("r")}]")
        seen.observe("m",listOf(t))
        assertEquals(setOf("r"),seen.detailUnread)
        assertEquals(0,seen.count(listOf(t)))
    }
    @Test fun viewingASessionClearsEveryMatchingRunAcrossTasksAndMachines() {
        val dismissed=mutableListOf<String>()
        val seen=ScheduleSeen(onRead={ runs -> dismissed+=runs.map { it.id } })
        seen.observe("m",emptyList()); seen.observe("n",emptyList())
        fun sessionRun(id: String, session: String) = """{"id":"$id","session_id":"$session","notified":true}"""
        val first=task("[${sessionRun("r1","shared")},${sessionRun("r2","shared")},${sessionRun("r3","elsewhere")}]")
        val second=task("[${sessionRun("r4","shared")} ]",id="other",machine="n")
        seen.observe("m",listOf(first));seen.observe("n",listOf(second))
        seen.setVisibleSession("shared")
        assertEquals(setOf("r3"),seen.unreadIds(first)); assertTrue(seen.unreadIds(second).isEmpty())
        assertEquals(setOf("r1","r2","r4"),dismissed.toSet())
        val next=task("[${sessionRun("new","shared")}]")
        seen.observe("m",listOf(next));assertEquals(0,seen.count(listOf(next)))
        seen.setVisibleSession(null)
        val later=task("[${sessionRun("later","shared")}]")
        seen.observe("m",listOf(later));assertEquals(1,seen.count(listOf(later)))
    }
    @Test fun taskIdentityIncludesMachineAndAttentionIsNotDoubleCounted() {
        val seen=ScheduleSeen()
        seen.observe("m",emptyList());seen.observe("n",emptyList())
        val first=task("[${run("r")} ]", status="failed")
        val second=first.copy(machine="n")
        seen.observe("m",listOf(first));seen.observe("n",listOf(second))
        assertEquals(2,seen.count(listOf(first,second)))
        seen.openTask("m","t")
        assertTrue(seen.unreadIds(first).isEmpty());assertEquals(setOf("r"),seen.unreadIds(second))
        assertEquals(2,seen.count(listOf(first,second))) // attention survives reading
        seen.forget("n");assertEquals(1,seen.count(listOf(first)))
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
        reload.openTask("m","t")
        val opened = seen(); opened.observe("m",listOf(t)); assertEquals(0,opened.count(listOf(t)))
        assertEquals("[\"[\\\"t\\\",\\\"r\\\"]\"]",data["ra-schedule-seen:[\"relay-a\",\"account-a\",\"m\"]"])
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
