package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class ScheduleBadgeTest {
    private fun task(runs: String = "[]", status: String = "scheduled", reason: String = "", id: String = "t", machine: String = "m") = ScheduledTask(machine, Json.parseToJsonElement("""{"id":"$id","status":"$status","reason":"$reason","runs":$runs}""").jsonObject)
    private fun run(id: String, notified: Boolean = true) = """{"id":"$id","status":"running","notified":$notified}"""
    @Test fun historicalRunsAreSeenAndBadgeOnlyCountsAttention() {
        val seen = ScheduleSeen()
        val historical = task("[${run("old")}]")
        seen.observe("m", listOf(historical)); assertEquals(0, seen.count(listOf(historical)))
        val current = task("[${run("old")},${run("new")},${run("another")},${run("quiet", false)}]")
        seen.observe("m", listOf(current))
        assertEquals(setOf("new", "another"), seen.unreadIds(current))
        assertEquals(0, seen.count(listOf(current))); assertTrue(seen.hasNewResult(current))
        // Repeated notifications within one run remain one unread message.
        val repeated = current.copy(json = JsonObject(current.json + ("active_run" to
            Json.parseToJsonElement("""{"id":"new","notified":true,"notification_count":7}"""))))
        seen.observe("m", listOf(repeated)); assertEquals(0, seen.count(listOf(repeated))); assertTrue(seen.hasNewResult(repeated))
        val failed = task("[${run("new")},${run("another")} ]", "failed")
        assertEquals(2, seen.count(listOf(failed, task(status="uncertain", id="other"))))
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
        assertEquals(0, seen.count(listOf(first,other))); assertTrue(seen.hasNewResult(other))
        assertEquals(setOf("first", "second"), seen.detailUnread)
        val arriving = task("[${run("first")},${run("second")},${run("arriving")}]")
        seen.observe("m",listOf(arriving,other))
        assertTrue(seen.unreadIds(arriving).isEmpty())
        assertEquals(setOf("first", "second"), seen.detailUnread)
        seen.closeTask("m", "t"); assertTrue(seen.detailUnread.isEmpty())
        seen.openTask("m", "t"); assertTrue(seen.detailUnread.isEmpty())
        seen.closeTask("m", "t")
        val later=task("[${run("later")}]")
        seen.observe("m",listOf(later,other)); assertTrue(seen.hasNewResult(later)); assertTrue(seen.hasNewResult(other))
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
        seen.observe("m",listOf(later));assertTrue(seen.hasNewResult(later))
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
        val reload = seen(); reload.observe("m", listOf(t)); assertTrue(reload.hasNewResult(t))
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
    @Test fun idleCloseComesFromTheLatestStateAndResetsOnResumeOrManualStop() {
        fun state(status: SessionStatus, idle: Boolean) = Event.newBuilder().setSessionState(SessionState.newBuilder().setStatus(status).setDetail("stopped").setIdleClosed(idle)).build()
        val idle = state(SessionStatus.SESSION_STATUS_EXITED,true)
        assertTrue(derive(listOf(idle)).idleClosed)
        assertFalse(derive(listOf(idle)).scheduledClosed)
        assertTrue(currentActions(derive(listOf(idle)), null, listOf(idle)).idleClosed)
        assertFalse(derive(listOf(state(SessionStatus.SESSION_STATUS_EXITED,false))).idleClosed)
        assertFalse(derive(listOf(idle,state(SessionStatus.SESSION_STATUS_STARTING,false))).idleClosed)
        assertFalse(derive(listOf(idle,state(SessionStatus.SESSION_STATUS_EXITED,false))).idleClosed)
    }
    @Test fun markingUnreadOnlyRemovesLatestResultAndReadDismissesAllNotices() {
        val dismissed = mutableListOf<String>()
        val seen = ScheduleSeen(onRead = { dismissed += it.map { r -> r.id } })
        val t = task("""[{"id":"old","notified":true,"ended_at":"2026-10-01T08:00:00Z"},{"id":"new","notified":true,"ended_at":"2026-10-02T08:00:00Z"},{"id":"quiet","notified":false}]""")
        seen.observe("m", listOf(t)); seen.markUnread(t)
        assertEquals(setOf("new"), seen.unreadIds(t))
        seen.markRead(t); assertFalse(seen.hasNewResult(t))
        assertEquals(setOf("old", "new"), dismissed.toSet())
        seen.markUnread(task()); assertFalse(seen.hasNewResult(task()))
    }
    @Test fun cachedSnapshotsCannotPruneReadHistoryButAuthoritativeListsDeleteIt() {
        val data = mutableMapOf<String, String>()
        fun store() = ScheduleSeen(read = { data[it] }, write = { key, value -> data[key] = value })
        val t = task("[${run("old")},${run("retained")}]")
        store().observe("m", listOf(t))
        val reload = store()
        reload.observe("m", listOf(task("[${run("old")}]")), authoritative = false)
        reload.observe("m", listOf(t))
        assertFalse(reload.hasNewResult(t))
        reload.observe("m", emptyList())
        reload.observe("m", listOf(t))
        assertTrue(reload.hasNewResult(t))
        reload.markRead(t)
        reload.observe("m", listOf(t.copy(json = JsonObject(t.json + mapOf("status" to JsonPrimitive("cancelled"), "reason" to JsonPrimitive("user_cancelled"))))))
        assertEquals("[]", data.values.single())
    }
    @Test fun badgeCapsVisibleAttentionAndNeverUsesResultCounts() {
        val seen = ScheduleSeen(read = { "[]" })
        val tasks = (1..120).map { task(status = "failed", id = "t$it") }
        assertEquals(120, seen.count(tasks + task(status = "completed") + task(status = "cancelled", reason = "user_cancelled")))
        assertEquals("99+", scheduleBadgeText(seen.count(tasks)))
        assertEquals("99", scheduleBadgeText(99))
    }

    @Test fun cachedDetailDoesNotCaptureDotsReadOrDismissUntilACompleteListArrives() {
        val dismissed = mutableListOf<String>()
        val seen = ScheduleSeen(read = { "[]" }, onRead = { dismissed += it.map { r -> r.id } })
        val t = task("[${run("r")}]")
        seen.observe("m", listOf(t), authoritative = false)
        seen.openTask("m", "t")
        assertEquals(setOf("r"), seen.unreadIds(t))
        assertTrue(seen.detailUnread.isEmpty()); assertNull(seen.capturedDetail); assertTrue(dismissed.isEmpty())
        seen.observe("m", listOf(t))
        assertTrue(seen.unreadIds(t).isEmpty()); assertEquals(setOf("r"), seen.detailUnread)
        assertEquals(ScheduleTaskKey("m", "t"), seen.capturedDetail)
        assertEquals(listOf("r"), dismissed)
    }
    @Test fun markingUnreadSurvivesReturningToTheSameSourceConversationAndRefreshing() {
        val seen = ScheduleSeen(read = { "[]" })
        val t = task("""[{"id":"r","session_id":"shared","notified":true,"ended_at":"2026-10-02T08:00:00Z"}]""")
        seen.observe("m", listOf(t)); seen.setVisibleSession("shared"); seen.openTask("m", "t")
        assertFalse(seen.hasNewResult(t))
        seen.markUnread(t); seen.hideTask("m", "t"); seen.closeTask("m", "t")
        seen.setVisibleSession("shared"); seen.observe("m", listOf(t))
        assertEquals(setOf("r"), seen.unreadIds(t))
        val newer = t.copy(json = JsonObject(t.json + ("runs" to buildJsonArray {
            add(t.runs.single().json); add(buildJsonObject { put("id", "later"); put("session_id", "shared"); put("notified", true); put("ended_at", "2026-10-03T08:00:00Z") })
        })))
        seen.observe("m", listOf(newer))
        assertEquals(setOf("r"), seen.unreadIds(newer)) // Only the explicit unread mark is protected.
        seen.setVisibleSession("elsewhere"); seen.setVisibleSession("shared")
        assertFalse(seen.hasNewResult(newer))
        seen.markUnread(newer); seen.setVisibleSession("shared"); assertTrue(seen.hasNewResult(newer))
        seen.setVisibleSession(null); seen.setVisibleSession("shared")
        assertFalse(seen.hasNewResult(newer)) // Background/foreground restores automatic reads too.
    }
    @Test fun repeatedDetailReadsPreserveExplicitUnreadUntilThePageChangesOrBackgrounds() {
        val seen = ScheduleSeen(read = { "[]" })
        val t = task("[${run("r")}]")
        seen.observe("m", listOf(t)); seen.openTask("m", "t"); seen.markUnread(t)
        seen.openTask("m", "t"); seen.observe("m", listOf(t))
        assertTrue(seen.hasNewResult(t))
        seen.hideTask("m", "t"); seen.openTask("m", "t"); assertFalse(seen.hasNewResult(t))
        seen.markUnread(t); seen.openTask("m", "different"); seen.openTask("m", "t")
        assertFalse(seen.hasNewResult(t))
        seen.markUnread(t); seen.markRead(t); assertFalse(seen.hasNewResult(t))
    }

    @Test fun readingTheActualCachedConversationKeepsItsExistingReadRule() {
        val seen = ScheduleSeen(read = { "[]" })
        val t = task("""[{"id":"r","session_id":"shared","notified":true}]""")
        seen.observe("m", listOf(t), authoritative = false)
        seen.openTask("m", "t"); assertTrue(seen.hasNewResult(t)); assertNull(seen.capturedDetail)
        seen.hideTask("m", "t"); seen.closeTask("m", "t"); seen.setVisibleSession("shared")
        assertFalse(seen.hasNewResult(t)); assertNull(seen.capturedDetail)
    }

}
