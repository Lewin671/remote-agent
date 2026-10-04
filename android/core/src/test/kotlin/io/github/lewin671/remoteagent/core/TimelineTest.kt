package io.github.lewin671.remoteagent.core

import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ProjectPref
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneId
import java.time.ZonedDateTime

/** The home page's scope and timeline (client-v27, architecture §8 「主页的范围」). */
class TimelineTest {
    private val zone = ZoneId.of("Asia/Shanghai")
    private fun at(y: Int, mo: Int, d: Int, h: Int = 12, mi: Int = 0) = ZonedDateTime.of(y, mo, d, h, mi, 0, 0, zone).toInstant().toEpochMilli()
    // A Sunday.
    private val now = at(2026, 10, 4, 9, 41)

    private fun ts(ms: Long) = Timestamp.newBuilder().setSeconds(ms / 1000).build()

    private fun session(
        id: String, cwd: String, title: String = id, created: Long = now - 3600_000, ended: Long? = null, machine: String = "m",
        agent: Agent = Agent.AGENT_CODEX, approvals: Int = 0, forkedFrom: String = "",
    ) = SessionView(
        id, machine,
        Session.newBuilder().setCwd(cwd).setTitle(title).setAgent(agent).setCreatedAt(ts(created)).setPendingApprovals(approvals).setForkedFrom(forkedFrom)
            .setStatus(if (ended != null) SessionStatus.SESSION_STATUS_EXITED else SessionStatus.SESSION_STATUS_IDLE)
            .also { if (ended != null) it.setEndedAt(ts(ended)) }.build(),
        updatedMs = ended ?: created,
    )

    private fun state(vararg sessions: SessionView, hidden: List<String> = emptyList(), pinned: List<String> = emptyList(), machines: List<String> = listOf("m")) = RemoteState(
        sessions = sessions.associateBy { it.id },
        machines = machines.associateWith { MachineView(it, online = true, lastSeenMs = 0) },
        prefs = PrefsObject(1, ProjectPrefs.newBuilder().addAllPinned(pinned.map { ProjectPref.newBuilder().setPath(it).build() })
            .addAllHidden(hidden.map { ProjectPref.newBuilder().setPath(it).build() }).build()),
    )

    @Test
    fun theOnlyListedProjectIsTheDefaultScopeElseEveryProject() {
        assertEquals(HomeScope.All, defaultScope(state()))
        assertEquals(HomeScope.Of("/a"), defaultScope(state(session("1", "/a"))))
        assertEquals(HomeScope.All, defaultScope(state(session("1", "/a"), session("2", "/b"))))
        // A removed project does not count.
        assertEquals(HomeScope.Of("/a"), defaultScope(state(session("1", "/a"), session("2", "/b"), hidden = listOf("/b"))))
    }

    @Test
    fun theRememberedScopeHoldsWhileItsProjectIsListed() {
        val two = state(session("1", "/a"), session("2", "/b"), session("3", "/c"))
        assertEquals(HomeScope.Of("/b"), resolveScope(HomeScope.Of("/b"), two))
        assertEquals(HomeScope.All, resolveScope(HomeScope.All, two))
        assertEquals(HomeScope.All, resolveScope(null, two))
        // Gone or removed: the default again.
        assertEquals(HomeScope.All, resolveScope(HomeScope.Of("/gone"), two))
        assertEquals(HomeScope.All, resolveScope(HomeScope.Of("/b"), state(session("1", "/a"), session("2", "/b"), session("3", "/c"), hidden = listOf("/b"))))
        assertEquals(HomeScope.Of("/a"), resolveScope(HomeScope.Of("/b"), state(session("1", "/a"), session("2", "/b"), hidden = listOf("/b"))))
        // 全部项目 is not offered for a single project.
        assertEquals(HomeScope.Of("/a"), resolveScope(HomeScope.All, state(session("1", "/a"))))
    }

    @Test
    fun theScopeSurvivesTheDeviceSettings() {
        for (s in listOf(HomeScope.All, HomeScope.Of("/Users/me/Code/foo"), HomeScope.Of(""), HomeScope.Of("/a:b/project:c"))) assertEquals(s, decodeScope(encodeScope(s)))
        assertNull(decodeScope(null))
        assertNull(decodeScope("something else"))
    }

    @Test
    fun endedSessionsGroupByLocalCalendarDay() {
        fun g(ms: Long) = endedGroup(ms, now, zone)
        assertEquals(EndedGroup.Today, g(at(2026, 10, 4, 0, 0)))
        assertEquals(EndedGroup.Today, g(now + 3600_000))
        // One minute apart across midnight is another day.
        assertEquals(EndedGroup.Yesterday, g(at(2026, 10, 3, 23, 59)))
        assertEquals(EndedGroup.Yesterday, g(at(2026, 10, 3, 0, 0)))
        assertEquals(EndedGroup.Week, g(at(2026, 10, 2, 23, 59)))
        assertEquals(EndedGroup.Week, g(at(2026, 9, 28, 0, 0)))
        assertEquals(EndedGroup.Month(2026, 9), g(at(2026, 9, 27, 23, 59)))
        assertEquals(EndedGroup.Month(2025, 12), g(at(2025, 12, 31)))

        assertEquals("今天", groupTitle(EndedGroup.Today, now, zone))
        assertEquals("昨天", groupTitle(EndedGroup.Yesterday, now, zone))
        assertEquals("近 7 天", groupTitle(EndedGroup.Week, now, zone))
        assertEquals("9 月", groupTitle(EndedGroup.Month(2026, 9), now, zone))
        assertEquals("2025 年 12 月", groupTitle(EndedGroup.Month(2025, 12), now, zone))
    }

    @Test
    fun aMonthOfThisYearEarlierThanAWeekAgoCanBeTheCurrentMonth() {
        val late = at(2026, 10, 20)
        assertEquals(EndedGroup.Month(2026, 10), endedGroup(at(2026, 10, 2), late, zone))
        assertEquals("10 月", groupTitle(EndedGroup.Month(2026, 10), late, zone))
    }

    @Test
    fun anEndedRowWritesTheTimeItsGroupCalls() {
        assertEquals("7:10", endedTime(at(2026, 10, 4, 7, 10), now, zone))
        assertEquals("18:44", endedTime(at(2026, 10, 3, 18, 44), now, zone))
        assertEquals("周五", endedTime(at(2026, 10, 2, 8, 0), now, zone))
        assertEquals("周一", endedTime(at(2026, 9, 28, 8, 0), now, zone))
        assertEquals("9月27日", endedTime(at(2026, 9, 27, 8, 0), now, zone))
        assertEquals("12月31日", endedTime(at(2025, 12, 31), now, zone))
        assertEquals("", endedTime(0, now, zone))
    }

    @Test
    fun groupsKeepTheOrderOfTheList() {
        val list = listOf(
            session("a", "/a", ended = at(2026, 10, 4, 7, 50)), session("b", "/a", ended = at(2026, 10, 4, 7, 10)),
            session("c", "/a", ended = at(2026, 10, 3, 18, 44)), session("d", "/a", ended = at(2026, 9, 30)),
            session("e", "/a", ended = at(2026, 9, 27)), session("f", "/a", ended = at(2026, 9, 1)), session("g", "/a", ended = at(2025, 9, 1)),
        )
        assertEquals(
            listOf(EndedGroup.Today to listOf("a", "b"), EndedGroup.Yesterday to listOf("c"), EndedGroup.Week to listOf("d"),
                EndedGroup.Month(2026, 9) to listOf("e", "f"), EndedGroup.Month(2025, 9) to listOf("g")),
            groupEnded(list, now, zone).map { (g, l) -> g to l.map { it.id } },
        )
    }

    @Test
    fun aProjectScopeListsItsOwnSessionsButEveryPendingOne() {
        val st = state(
            session("run-a", "/a", created = now - 1000), session("old-a", "/a", ended = now - 5000),
            session("run-b", "/b"), session("old-b", "/b", ended = now - 4000),
            session("wait-b", "/b", approvals = 1), session("wait-a", "/a", approvals = 1, created = now - 9000),
        )
        val a = homeTimeline(st, HomeScope.Of("/a"), zone = zone)
        assertEquals(setOf("wait-a", "wait-b"), a.attention.map { it.id }.toSet())
        assertEquals(listOf("run-a"), a.running.map { it.id })
        assertEquals(listOf("old-a"), a.ended.map { it.id })
        assertEquals(3, a.total)
        assertFalse(a.meta.project)
        assertTrue(outsideScope(st.sessions.getValue("wait-b"), HomeScope.Of("/a")))
        assertFalse(outsideScope(st.sessions.getValue("wait-a"), HomeScope.Of("/a")))
        // The card from outside the scope names its project; the scope's own does not.
        assertEquals(listOf("b"), cardMeta(st, st.sessions.getValue("wait-b"), HomeScope.Of("/a")))
        assertEquals(emptyList<String>(), cardMeta(st, st.sessions.getValue("wait-a"), HomeScope.Of("/a")))

        val all = homeTimeline(st, HomeScope.All, zone = zone)
        assertEquals(listOf("run-a", "run-b"), all.running.map { it.id })
        assertEquals(listOf("old-b", "old-a"), all.ended.map { it.id })
        assertEquals(6, all.total)
        assertTrue(all.meta.project)
        assertEquals(listOf("a"), cardMeta(st, st.sessions.getValue("wait-a"), HomeScope.All))
    }

    @Test
    fun aRemovedProjectsSessionsOnlyShowWhileTheyGoOn() {
        val st = state(
            session("run-a", "/a"), session("old-a", "/a", ended = now - 5000),
            session("run-h", "/h"), session("wait-h", "/h", approvals = 1), session("old-h", "/h", ended = now - 1000),
            session("x", "/c"), hidden = listOf("/h"),
        )
        val all = homeTimeline(st, HomeScope.All, zone = zone)
        assertEquals(listOf("wait-h"), all.attention.map { it.id })
        assertEquals(setOf("run-a", "run-h", "x"), all.running.map { it.id }.toSet())
        assertEquals(listOf("old-a"), all.ended.map { it.id })
    }

    @Test
    fun theThirdLineOnlyWritesWhatDiffers() {
        val one = state(session("1", "/a"), session("2", "/a", forkedFrom = "1"))
        val m1 = homeTimeline(one, HomeScope.Of("/a"), zone = zone).meta
        assertEquals(RowMeta(), m1)
        assertEquals("", metaLine(one, one.sessions.getValue("1"), m1))
        assertEquals("分叉", metaLine(one, one.sessions.getValue("2"), m1))

        val many = state(
            session("1", "/Code/foo", machine = "m"), session("2", "/Code/foo", agent = Agent.AGENT_CLAUDE, machine = "n", forkedFrom = "1"),
            session("3", "/Code/bar"), machines = listOf("m", "n"),
        )
        val all = homeTimeline(many, HomeScope.All, zone = zone).meta
        assertEquals(RowMeta(project = true, agent = true, machine = true), all)
        assertEquals("分叉 · foo · Claude Code · n", metaLine(many, many.sessions.getValue("2"), all))
        // One CLI in the scope: not written, though the account has two.
        val bar = homeTimeline(many, HomeScope.Of("/Code/bar"), zone = zone).meta
        assertEquals(RowMeta(machine = true), bar)
        assertEquals("m", metaLine(many, many.sessions.getValue("3"), bar))
        // An ended session's CLI counts: the scope used more than one.
        val used = state(session("1", "/a"), session("2", "/a", agent = Agent.AGENT_CLAUDE, ended = now - 1000))
        assertTrue(homeTimeline(used, HomeScope.Of("/a"), zone = zone).meta.agent)
        // A project page never writes the project.
        assertEquals(RowMeta(agent = true, machine = true), rowMeta(many, many.sessions.values.filter { projectKey(it) == "/Code/foo" }, project = false))
    }

    @Test
    fun theFilterMatchesTitlesOnlyAndKeepsTheOrder() {
        val st = state(
            session("1", "/a", title = "补充接口文档", created = now - 1000), session("2", "/a", title = "修复登录超时", created = now - 2000),
            session("3", "/a", title = "给登录接口加限流", ended = now - 3000), session("4", "/a", title = "Upgrade API layer", ended = now - 4000),
            session("5", "/a", title = "接口返回值统一成驼峰", ended = at(2026, 9, 21)),
        )
        val f = homeTimeline(st, HomeScope.Of("/a"), " 接口 ", zone)
        assertEquals(listOf("1"), f.running.map { it.id })
        assertEquals(listOf("3", "5"), f.ended.map { it.id })
        assertEquals(3, f.size)
        // The count that decides whether the box shows does not shrink with the filter.
        assertEquals(5, f.total)
        assertEquals(listOf("4"), homeTimeline(st, HomeScope.Of("/a"), "api", zone).ended.map { it.id })
        // The folder is not the title.
        assertEquals(0, homeTimeline(st, HomeScope.Of("/a"), "/a", zone).size)
        assertEquals(5, homeTimeline(st, HomeScope.Of("/a"), "  ", zone).size)

        assertEquals("3 个会话的标题里有「接口」", filterTail(3, " 接口 "))
        assertEquals("没有标题里有「zzz」的会话", filterTail(0, "zzz"))
    }

    @Test
    fun theTailCountsEndedSessionsOnceTheyTakeMoreThanAPage() {
        assertNull(endedTail(30, 30))
        assertEquals("共 71 个，滚到底加载更早的", endedTail(71, 30))
        assertEquals("共 71 个", endedTail(71, 71))
    }

    @Test
    fun theScopeMenuListsEveryProjectFirstThenPinnedThenTheOthers() {
        val st = state(
            session("1", "/Users/me/Code/foo"), session("2", "/Users/me/Code/foo", created = now - 100),
            session("3", "/Users/me/Code/billing", created = now - 50), session("4", "/Users/me/Code/blog", ended = now - 2 * 3600_000),
            session("5", "/Users/me/gone", ended = now - 1000), pinned = listOf("/Users/me/Code/blog"), hidden = listOf("/Users/me/gone"),
        )
        val rows = scopeRows(st, HomeScope.Of("/Users/me/Code/foo"), now, zone = zone)
        assertEquals(listOf("全部项目", "blog", "billing", "foo"), rows.map { it.name })
        assertEquals(listOf("3 个进行中", "~/Code/blog · 7:41", "~/Code/billing · 1 个进行中", "~/Code/foo · 2 个进行中"), rows.map { it.detail })
        assertEquals(listOf(false, false, false, true), rows.map { it.current })
        assertEquals(listOf("billing"), scopeRows(st, HomeScope.All, now, "BILL", zone).map { it.name })
        // One project: nothing to choose 全部项目 over.
        assertEquals(listOf("foo"), scopeRows(state(session("1", "/Users/me/Code/foo")), HomeScope.Of("/Users/me/Code/foo"), now, zone = zone).map { it.name })
    }
}
