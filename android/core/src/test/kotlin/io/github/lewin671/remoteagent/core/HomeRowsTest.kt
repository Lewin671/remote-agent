package io.github.lewin671.remoteagent.core

import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import io.github.lewin671.remoteagent.proto.ProjectSettings
import io.github.lewin671.remoteagent.proto.ProjectSummary
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.core.assertMessageEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneId
import java.time.ZonedDateTime

/** The home page as a list of lines and the sentence at its top (client-v58 #home); the same cases as iOS's HomeRowsTests. */
class HomeRowsTest {
    private val zone = ZoneId.of("Asia/Shanghai")
    private fun at(y: Int, mo: Int, d: Int, h: Int = 12, mi: Int = 0) = ZonedDateTime.of(y, mo, d, h, mi, 0, 0, zone).toInstant().toEpochMilli()
    // A Sunday.
    private val now = at(2026, 10, 4, 9, 41)
    private fun ts(ms: Long) = Timestamp.newBuilder().setSeconds(ms / 1000).build()

    private fun session(
        id: String, cwd: String = "/p/foo", title: String = id, created: Long = now - 3_600_000, ended: Long? = null,
        machine: String = "m", agent: Agent = Agent.AGENT_CODEX, approvals: Int = 0, questions: Int = 0, plan: Boolean = false,
        status: SessionStatus? = null, preview: String = "", detail: String = "",
    ) = SessionView(
        id, machine,
        Session.newBuilder().setCwd(cwd).setTitle(title).setAgent(agent).setCreatedAt(ts(created)).setPendingApprovals(approvals)
            .setPendingQuestions(questions).setPlanOpen(plan).setPreview(preview).setStatusDetail(detail)
            .setStatus(status ?: if (ended != null) SessionStatus.SESSION_STATUS_EXITED else SessionStatus.SESSION_STATUS_IDLE)
            .also { if (ended != null) it.setEndedAt(ts(ended)) }.build(),
        updatedMs = ended ?: created,
        listedEndedMs = ended ?: 0,
    )

    private fun state(sessions: List<SessionView>, machines: List<String> = listOf("m")) = RemoteState(
        sessions = sessions.associateBy { it.id },
        machines = machines.associateWith { MachineView(it, online = true, lastSeenMs = 0) },
        prefs = PrefsObject(1, ProjectPrefs.getDefaultInstance()),
        loaded = true,
        conn = ConnState.ONLINE,
        link = Link.ONLINE,
        synced = true,
    )

    private fun list(st: RemoteState, scope: HomeScope = HomeScope.All, query: String = "", shown: HomeShown = HomeShown()) =
        homeList(st, scope, query, shown, now, zone)

    @Test fun theListIsWhatWaitsThenWhatRunsThenTheEndedByDay() {
        val st = state(listOf(
            session("wait", approvals = 1), session("run", created = now - 60_000, status = SessionStatus.SESSION_STATUS_RUNNING),
            session("idle", created = now - 120_000),
            session("today", ended = at(2026, 10, 4, 7, 10)), session("yesterday", ended = at(2026, 10, 3, 18, 44)),
            session("week", ended = at(2026, 9, 29, 10, 0)), session("month", ended = at(2026, 9, 21, 10, 0)),
            session("lastYear", ended = at(2025, 12, 3, 10, 0)),
        ))
        val l = list(st)
        assertMessageEquals(listOf(
            "s:wait", "h:running", "s:run", "s:idle", "h:today", "s:today", "h:yesterday", "s:yesterday",
            "h:week", "s:week", "h:month:2026-9", "s:month", "h:month:2025-12", "s:lastYear",
        ), l.items.map { it.id })
        assertMessageEquals(listOf("进行中", "今天", "昨天", "近 7 天", "9 月", "2025 年 12 月"), l.items.filterIsInstance<HomeItem.Label>().map { it.title })
        assertTrue(l.empty == null && !l.showFilter && !l.wantAll && !l.wantPage && !l.grows)
        // The approval that waits is the dark card.
        assertMessageEquals(HomeItem.AttentionCard(st.sessions.getValue("wait")), l.items[0])
        // The last running row draws no line under it.
        assertMessageEquals(HomeItem.Running(st.sessions.getValue("run"), last = false), l.items[2])
        assertMessageEquals(HomeItem.Running(st.sessions.getValue("idle"), last = true), l.items[3])
    }

    @Test fun aSessionKeepsItsLineWhenItMovesBetweenParts() {
        val waiting = list(state(listOf(session("s", approvals = 1)))).items.last()
        val running = list(state(listOf(session("s")))).items.last()
        val ended = list(state(listOf(session("s", ended = now - 1000)))).items.last()
        assertTrue(waiting.id == "s:s" && running.id == "s:s" && ended.id == "s:s")
        assertMessageEquals(30 + 4 + 1, list(state((0 until 40).map { session("e$it", ended = now - it * 86_400_000L) })).items.map { it.id }.toSet().size)
    }

    @Test fun onlyTheFirstFewWaitAndRunUntilTheRestIsAskedFor() {
        val st = state((0 until 5).map { session("a$it", created = now - it * 1000L, approvals = 1) } + (0 until 8).map { session("r$it", created = now - it * 1000L) })
        val l = list(st)
        // One dark card, then three rows, then 还有 1 件 closes the grey list.
        assertMessageEquals(1, l.items.count { it is HomeItem.AttentionCard })
        val rows = l.items.filterIsInstance<HomeItem.Attention>()
        assertMessageEquals(ATTENTION_MAX, rows.size)
        assertMessageEquals(listOf(true, false, false), rows.map { it.first })
        assertMessageEquals(listOf(false, false, false), rows.map { it.last })
        assertTrue(HomeItem.MoreAttention(1) in l.items)
        assertMessageEquals(RUNNING_MAX, l.items.count { it is HomeItem.Running })
        assertTrue(HomeItem.MoreRunning(3) in l.items)
        val all = list(st, shown = HomeShown(allAttention = true, allRunning = true))
        assertMessageEquals(5 + 1 + 8, all.items.size)
        assertTrue(all.items.any { it is HomeItem.Attention && it.last })
        // A filter shows every match.
        assertMessageEquals(8, list(st, query = "r").items.count { it is HomeItem.Running })
    }

    @Test fun onlyAnApprovalIsTheDarkCard() {
        // Questions, plans and popups are rows, the most urgent of them too.
        val st = state(listOf(session("q", questions = 1), session("p", plan = true)))
        val l = list(st)
        assertFalse(l.items.any { it is HomeItem.AttentionCard })
        assertMessageEquals(HomeItem.Attention(st.sessions.getValue("q"), first = true, last = false), l.items.first())
        assertMessageEquals(HomeItem.Attention(st.sessions.getValue("p"), first = false, last = true), l.items[1])
    }

    @Test fun endedSessionsComeThirtyAtATimeAndTheLastLineCountsThem() {
        val st = state((0 until 71).map { session("e$it", ended = now - (it + 1) * 60_000L) })
        val first = list(st)
        assertMessageEquals(30, first.items.count { it is HomeItem.Ended })
        assertMessageEquals(HomeItem.Tail(HomeTail.Text(UiMessages.homeTotalLoadEarlier(71))), first.items.last())
        assertTrue(first.grows && first.endedHeld == 71 && !first.wantPage)
        val all = list(st, shown = HomeShown(ended = 90))
        assertMessageEquals(HomeItem.Tail(HomeTail.Text(UiMessages.homeTotal(71))), all.items.last())
        assertFalse(all.grows)
        assertTrue(first.showFilter)
        // Thirty or fewer need no last line.
        assertFalse(list(state((0 until 30).map { session("e$it", ended = now - 1000) })).items.any { it is HomeItem.Tail })
    }

    @Test fun pagesStillOnTheServerAreAskedForAsTheEndIsReached() {
        val summary = ProjectSummary.newBuilder().setPath("/p/foo").setEnded(80).build()
        val base = state((0 until 50).map { session("e$it", ended = now - (it + 1) * 60_000L) })
        var st = base.copy(
            sessionsNext = SessionPlace(1, "e49"),
            machines = mapOf("m" to MachineView("m", online = true, lastSeenMs = 0, meta = MachineMeta.newBuilder().addProjects(summary).build())),
        )
        // Thirty of fifty held: nothing to fetch yet, the end shows the next thirty.
        val first = list(st)
        assertTrue(!first.wantPage && first.grows)
        assertMessageEquals(HomeItem.Tail(HomeTail.Text(UiMessages.homeTotalLoadEarlier(80))), first.items.last())
        // Sixty wanted, fifty held: the next page, and the list waits for it.
        val second = list(st, shown = HomeShown(ended = 60))
        assertTrue(second.wantPage && !second.grows)
        assertMessageEquals(HomeItem.Tail(HomeTail.Text(UiMessages.homeTotalLoadEarlier(80))), second.items.last())
        // A page that failed says so, with its retry.
        st = st.copy(sessionsError = "超时")
        assertMessageEquals(HomeItem.Tail(HomeTail.Earlier(UiMessages.homeAndroidEarlierFailedRetry("超时"), retry = true)), list(st, shown = HomeShown(ended = 60)).items.last())
        st = st.copy(conn = ConnState.OFFLINE)
        assertMessageEquals(HomeItem.Tail(HomeTail.Earlier(UiMessages.homeAndroidEarlierOffline(), retry = false)), list(st, shown = HomeShown(ended = 60)).items.last())
    }

    @Test fun aFilterWantsEveryPageAndCountsWhatItMatched() {
        var st = state(listOf(session("a", title = "补充接口文档"), session("b", title = "接口限流", ended = now - 1000), session("c", title = "别的")))
        assertMessageEquals(HomeItem.Tail(HomeTail.Text(UiMessages.homeMatches(2, "接口"))), list(st, query = "接口").items.last())
        assertMessageEquals(listOf(HomeItem.Tail(HomeTail.Text(UiMessages.homeNoMatches("没有")))), list(st, query = "没有").items)
        assertTrue(list(st, query = "接口").showFilter)
        // While pages are left the count is not known yet.
        st = st.copy(sessionsNext = SessionPlace(1, "b"))
        val loading = list(st, query = "接口")
        assertTrue(loading.wantAll && !loading.wantPage)
        assertMessageEquals(HomeItem.Tail(HomeTail.Earlier(UiMessages.homeLoadingEarlier(), retry = false)), loading.items.last())
        // Blank is not a filter.
        assertFalse(list(st, query = "  ").wantAll)
    }

    @Test fun anEmptyPageSaysWhy() {
        assertMessageEquals(HomeEmpty.Loading, list(RemoteState()).empty)
        assertMessageEquals(HomeEmpty.Account(AccountEmpty.NoMachines), list(RemoteState(loaded = true)).empty)
        val offline = state(emptyList()).copy(machines = mapOf("m" to MachineView("m", online = false, lastSeenMs = 0)))
        assertMessageEquals(HomeEmpty.Account(AccountEmpty.NoneOnline), list(offline).empty)
        val online = offline.copy(machines = mapOf("m" to MachineView("m", online = true, lastSeenMs = 0, meta = MachineMeta.newBuilder().setHostname("MacBook.local").build())))
        assertMessageEquals(HomeEmpty.Account(AccountEmpty.Ready("MacBook")), list(online).empty)
        // Out of reach of the server nothing is said of the computers.
        assertMessageEquals(HomeEmpty.Account(AccountEmpty.Unlinked), list(online.copy(link = Link.NO_NETWORK)).empty)
        // A project added and not used yet, chosen as the scope.
        val added = RemoteState(
            loaded = true,
            machines = mapOf("m" to MachineView("m", online = true, lastSeenMs = 0)),
            prefs = PrefsObject(1, ProjectPrefs.newBuilder().addSettings(ProjectSettings.newBuilder().setPath("/p/new").setMachineId("m")).build()),
        )
        assertMessageEquals(HomeEmpty.Scope, list(added, scope = HomeScope.Of("/p/new")).empty)
        assertNull(list(added, scope = HomeScope.All).empty)
    }

    @Test fun aRowWritesItsStateItsPreviewAndOnlyWhatDiffers() {
        val run = session("run", title = "修复登录超时", created = now - 20_000, status = SessionStatus.SESSION_STATUS_RUNNING, preview = "正在执行 `go test`")
        val st = state(listOf(run), machines = listOf("m", "n"))
        val row = sessionRowText(st, run, RowMeta(project = true, agent = true, machine = true), now, zone)
        assertMessageEquals(SessionRowText("修复登录超时".messageText(), UiMessages.timeJustNow().messageText(), Status.RUNNING, "正在执行 go test".messageText(), "foo · m".messageText(), Agent.AGENT_CODEX), row)
        assertMessageEquals("修复登录超时，运行中，正在执行 go test，Codex，foo，m，刚刚", row.spoken)
        // The status word already says a plan waits.
        assertTrue(sessionRowText(st, session("plan", plan = true), RowMeta(), now, zone).preview.isEmpty())
        // The mark leads the third line only where the list used more than one CLI.
        assertNull(sessionRowText(st, run, RowMeta(project = true, machine = true), now, zone).agent)
        // Out of reach of the server the last state heard is not shown as if it held.
        assertMessageEquals(Status.UNLINKED, sessionRowText(st.copy(link = Link.NO_SERVER), run, RowMeta(), now, zone).status)
    }

    @Test fun anEndedRowWritesWhenItEndedAfterTheProjectWhereTheListSpansProjects() {
        val s = session("e", "/p/blog", title = "发布秋季文章", ended = at(2026, 10, 4, 7, 50))
        assertMessageEquals("blog · 7:50", endedRowText(s, project = true, now, zone).right)
        assertMessageEquals("7:50", endedRowText(s, project = false, now, zone).right)
        assertMessageEquals("发布秋季文章，已结束，blog，7:50", endedRowText(s, project = true, now, zone).spoken)
        assertMessageEquals("周二", endedRowText(session("w", ended = at(2026, 9, 29, 10, 0)), project = false, now, zone).right)
    }

    @Test fun aRowOfWhatWaitsSaysItsTypeAndWhatAbout() {
        val st = state(emptyList(), machines = listOf("m", "n"))
        val api = session("api", "/p/foo", title = "重构 API 层", approvals = 1)
        val request = ApprovalRequested.newBuilder().setApprovalId("A1").setCommand("go test ./... \\\n  -count=1").build()
        assertMessageEquals(PendingRowText("重构 API 层".messageText(), UiMessages.listStateAndroidRequestCommand().messageText(), "go test ./... \\ -count=1".messageText()), pendingRowText(st, api, HomeScope.All, request, null, zone))
        // Before the approval is read the status says what waits.
        assertMessageEquals("等待审批", pendingRowText(st, api, HomeScope.All, null, null, zone).kind)
        val dep = session("dep", "/p/bar", title = "升级依赖", agent = Agent.AGENT_CLAUDE, questions = 1)
        // In another project's scope the row names its project.
        val asked = pendingRowText(st, dep, HomeScope.Of("/p/foo"), null, "大版本也一起升吗？", zone)
        assertMessageEquals("Claude Code 在问你", asked.kind)
        assertMessageEquals("bar · 大版本也一起升吗？", asked.summary)
        assertMessageEquals("升级依赖，Claude Code 在问你，bar，大版本也一起升吗？", asked.spoken)
        assertMessageEquals("x", pendingRowText(st, dep, HomeScope.Of("/p/bar"), null, "x", zone).summary)
        val plan = pendingRowText(st, session("p", plan = true), HomeScope.All, null, null, zone)
        assertTrue(messageTextForTest(UiMessages.listStatePlan()) == plan.kind.testText() && plan.summary.isEmpty())
        val popup = session("t", status = SessionStatus.SESSION_STATUS_BLOCKED, detail = "Trust this folder?")
        assertMessageEquals(PendingRowText("t".messageText(), UiMessages.listStatePopup().messageText(), "Trust this folder?".messageText()), pendingRowText(st, popup, HomeScope.All, null, null, zone))
    }

    @Test fun theDarkCardSaysWhatAndUntilWhenAndWhere() {
        val st = state(emptyList(), machines = listOf("m", "n"))
        val api = session("api", "/p/foo", title = "重构 API 层", approvals = 1)
        val request = ApprovalRequested.newBuilder().setApprovalId("A1").setCommand("go test ./...").setDeadline(ts(now + 8 * 60_000 - 20_000)).build()
        assertMessageEquals(ApprovalCardText("请求执行".messageText(), "剩 8 分钟，超时拒绝".messageText(), false, "重构 API 层".messageText(), "foo · m".messageText()), approvalCardText(st, api, request, timeoutNote = true, now, zone))
        // The computer is written only where the account has more than one.
        assertMessageEquals("foo", approvalCardText(state(emptyList()), api, request, timeoutNote = true, now, zone).place)
        assertMessageEquals("剩 8 分钟", approvalCardText(st, api, request, timeoutNote = false, now, zone).time)
        // The last minute is orange.
        assertTrue(approvalCardText(st, api, request.toBuilder().setDeadline(ts(now + 30_000)).build(), timeoutNote = true, now, zone).hot)
        // A tool that is not a shell command is named, and no deadline says nothing.
        val write = ApprovalRequested.newBuilder().setApprovalId("A3").setToolName("Write").setToolInputJson("{\"file_path\":\"/p/foo/a.go\"}").build()
        val w = approvalCardText(st, api, write, timeoutNote = true, now, zone)
        assertTrue(w.kind.testText() == "请求权限 · Write" && w.time.isEmpty() && !w.hot)
        // Out of reach of the server it cannot be decided: 未连接.
        val cut = approvalCardText(st.copy(link = Link.NO_NETWORK), api, request, timeoutNote = true, now, zone)
        assertTrue(cut.time.testText() == "未连接" && !cut.hot)
    }

    @Test fun theSentenceAtTheTopSaysHowThingsStand() {
        val wait = session("w", "/p/bar", approvals = 1)
        val run = session("r", status = SessionStatus.SESSION_STATUS_RUNNING)
        val idle = session("i")
        val idle2 = session("i2")
        fun head(st: RemoteState, scope: HomeScope = HomeScope.All) = homeHeadline(st, scope)
        assertMessageEquals(HomeHeadline(HomeHeadline.Kind.Waiting(1), UiMessages.homeWaitingCount(1), UiMessages.homeRunningAndIdle(1, 2).messageText()), head(state(listOf(wait, run, idle, idle2))))
        assertMessageEquals("1 个会话空闲", head(state(listOf(wait, idle)))?.sub)
        // What waits is the account's; what runs is the scope's.
        assertMessageEquals(HomeHeadline(HomeHeadline.Kind.Waiting(1), UiMessages.homeWaitingCount(1)), head(state(listOf(wait, run)), HomeScope.Of("/p/bar")))
        assertMessageEquals(HomeHeadline(HomeHeadline.Kind.Running, UiMessages.homeRunningCount(1), UiMessages.homeOtherIdle(2).messageText()), head(state(listOf(run, idle, idle2))))
        assertMessageEquals(HomeHeadline(HomeHeadline.Kind.NothingWaits, UiMessages.homeNothingWaiting(), UiMessages.homeIdleCount(1).messageText()), head(state(listOf(idle))))
        assertMessageEquals(HomeHeadline(HomeHeadline.Kind.NothingWaits, UiMessages.homeNothingWaiting()), head(state(listOf(session("e", ended = now - 1000)))))
        assertMessageEquals(HomeHeadline(HomeHeadline.Kind.NoSessions, UiMessages.homeNoSessions()), head(state(emptyList())))
        assertMessageEquals(HomeHeadline.Kind.Unreadable, head(state(emptyList()).copy(sessionsListed = false, sessionsError = "超时"))?.kind)
        assertMessageEquals(HomeHeadline(HomeHeadline.Kind.EmptyProject, UiMessages.homeProjectNoSessions(), "/p/new".messageText(), subPath = true), head(state(listOf(idle)), HomeScope.Of("/p/new")))
        // Connected and nothing read yet: no sentence; not connected yet, 连接中….
        assertMessageEquals(HomeHeadline.Kind.Connecting, head(RemoteState())?.kind)
        assertNull(head(RemoteState(link = Link.ONLINE)))
        // Out of reach the connection is the sentence, the nearest stretch that is down.
        val kept = state(listOf(wait, run))
        assertMessageEquals(HomeHeadline(HomeHeadline.Kind.NoNetwork, UiMessages.connectionNoNetwork(), UiMessages.connectionPreviousBelow().messageText()), head(kept.copy(link = Link.NO_NETWORK)))
        assertMessageEquals(HomeHeadline(HomeHeadline.Kind.Reconnecting, UiMessages.connectionReconnecting(), UiMessages.connectionPreviousBelow().messageText()), head(kept.copy(link = Link.WAITING)))
        val first = head(kept.copy(link = Link.WAITING, synced = false))
        assertTrue(first?.title?.key == "connection.connecting" && first.unlinked)
        assertMessageEquals(HomeHeadline(HomeHeadline.Kind.NoServer, UiMessages.connectionNoServer(), UiMessages.connectionRetryingPrevious().messageText(), details = true), head(kept.copy(link = Link.NO_SERVER)))
        assertMessageEquals("联网后才能读取会话", head(RemoteState(link = Link.NO_NETWORK))?.sub)
        assertMessageEquals(
            HomeHeadline(HomeHeadline.Kind.NoServer, UiMessages.connectionNoServer(), "证书无效".messageText(), subError = true, details = true),
            head(RemoteState(link = Link.NO_SERVER, error = "证书无效")),
        )
    }

    @Test fun theTabCountsWhatWaitsAndKeepsItOutOfReach() {
        val st = state(listOf(session("a", approvals = 1), session("q", questions = 1), session("i")))
        assertMessageEquals(2, waitingCount(st))
        assertMessageEquals(2, waitingCount(st.copy(link = Link.NO_SERVER, machines = mapOf("m" to MachineView("m", online = false, lastSeenMs = 0)))))
    }
}
