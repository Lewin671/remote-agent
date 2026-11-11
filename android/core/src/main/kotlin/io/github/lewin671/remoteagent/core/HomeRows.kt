// The home page as the list it draws (client-v58 #home, architecture §8
// 「主页的范围」): the sentence at its top, which lines there are and in what
// order, what each row writes, what ends the list, and which pages of the
// listing it wants meanwhile. The part of the home page that decides and
// draws nothing, so that the screen only lays the lines out and the rules
// are tested here. iOS has the same rules in RACore/HomeRows.swift.
package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import java.time.ZoneId

/** How much of each part of the timeline the page shows; reset with the scope. */
data class HomeShown(
    /** 「还有 N 件」 was opened. */
    val allAttention: Boolean = false,
    /** 「还有 N 个」 under 进行中 was opened. */
    val allRunning: Boolean = false,
    /** How many ended sessions are rendered; reaching the end adds [ENDED_PAGE]. */
    val ended: Int = ENDED_PAGE,
)

/** Why the page has nothing to list. */
sealed interface HomeEmpty {
    /** Nothing was read yet: grey bars. */
    data object Loading : HomeEmpty
    /** The account has no session and no project. */
    data class Account(val why: AccountEmpty) : HomeEmpty
    /** The project chosen as the scope has no session. */
    data object Scope : HomeEmpty
}

/** What an account without sessions is told to do next. */
sealed interface AccountEmpty {
    /** No computer has joined: the authority adds one. */
    data object NoMachines : AccountEmpty
    /** Computers joined, none is online. */
    data object NoneOnline : AccountEmpty
    /** A computer is online, by its name. */
    data class Ready(val machine: String) : AccountEmpty
    /** The server is out of reach, so which computers are online is not known; the sentence at the top says why. */
    data object Unlinked : AccountEmpty
}

/** What ends the list. */
sealed interface HomeTail {
    /** A count: 「共 71 个」, 「3 个会话的标题里有「接口」」. */
    data class Text(val text: String) : HomeTail
    /** Earlier pages are wanted and on their way, waiting for the connection, or failed ([earlierTail]); [retry] when a tap tries again. */
    data class Earlier(val text: String, val retry: Boolean) : HomeTail
}

/**
 * One line of the home page's list. A session keeps its ID in whichever
 * part it is, so one that moves (an approval answered, a session ended) is
 * the same line to the list.
 */
sealed interface HomeItem {
    val id: String

    data class Label(val key: String, val title: String) : HomeItem {
        override val id get() = "h:$key"
    }

    /** The most urgent of what waits, when it is an approval: the dark card, decided right there. */
    data class AttentionCard(val session: SessionView) : HomeItem {
        override val id get() = "s:" + session.id
    }

    /** Another thing that waits, one row of the grey list: [first] and [last] say where the list's corners are. */
    data class Attention(val session: SessionView, val first: Boolean, val last: Boolean) : HomeItem {
        override val id get() = "s:" + session.id
    }

    /** 「还有 N 件」, the last row of the grey list. */
    data class MoreAttention(val count: Int) : HomeItem {
        override val id get() = "more:attention"
    }

    data class Running(val session: SessionView, val last: Boolean) : HomeItem {
        override val id get() = "s:" + session.id
    }

    /** 「还有 N 个」 under 进行中. */
    data class MoreRunning(val count: Int) : HomeItem {
        override val id get() = "more:running"
    }

    data class Ended(val session: SessionView) : HomeItem {
        override val id get() = "s:" + session.id
    }

    data class Tail(val tail: HomeTail) : HomeItem {
        override val id get() = "tail"
    }
}

/** The home page for a scope and what is typed in the filter. */
data class HomeList(
    val items: List<HomeItem>,
    val empty: HomeEmpty?,
    /** The title filter is offered: more than [FILTER_OVER] sessions in the scope, or something is typed. */
    val showFilter: Boolean,
    /** Every page left is to be fetched: a filter has to see every title. */
    val wantAll: Boolean,
    /** The next page is to be fetched: more ended sessions are to be shown than were. */
    val wantPage: Boolean,
    /** Reaching the end of the list shows the next [ENDED_PAGE] ended sessions. */
    val grows: Boolean,
    /** Which of project, CLI and machine a row's third line writes. */
    val meta: RowMeta,
    /** How many ended sessions are held for the scope, for the effect that grows the list. */
    val endedHeld: Int,
)

private fun groupId(g: EndedGroup): String = when (g) {
    EndedGroup.Today -> "today"
    EndedGroup.Yesterday -> "yesterday"
    EndedGroup.Week -> "week"
    is EndedGroup.Month -> "month:${g.year}-${g.month}"
}

/** What an account with no session and no project is told. */
fun accountEmpty(state: RemoteState): AccountEmpty {
    if (state.machines.isEmpty()) return AccountEmpty.NoMachines
    // Out of reach of the server every computer reads as offline, which says nothing about them.
    if (state.unlinked) return AccountEmpty.Unlinked
    val first = state.machines.values.filter { it.online }.minByOrNull { it.id } ?: return AccountEmpty.NoneOnline
    return AccountEmpty.Ready(machineName(first, first.id))
}

/**
 * The home page's list (design-system §6 「等你的事」「会话行」): what waits,
 * for the whole account and first, its most urgent approval as the dark
 * card and the rest at most [ATTENTION_MAX] rows before 「还有 N 件」; 进行中,
 * at most [RUNNING_MAX] rows before 「还有 N 个」; the ended sessions by day,
 * [HomeShown.ended] of them; and the line that ends the list. A filter shows
 * every match.
 */
fun homeList(
    state: RemoteState,
    scope: HomeScope,
    query: String = "",
    shown: HomeShown = HomeShown(),
    now: Long,
    zone: ZoneId = ZoneId.systemDefault(),
): HomeList {
    val filtering = query.isNotBlank()
    val tl = homeTimeline(state, scope, query, zone)
    // Without any project, removed ones included, there is nothing to scope by.
    val anyProject = projects(state).isNotEmpty()
    val wantAll = filtering && state.moreSessions
    val wantPage = !filtering && needsEndedPage(state, tl.ended, shown.ended, tl.endedTotal)

    val empty = when {
        !state.loaded && state.sessions.isEmpty() -> HomeEmpty.Loading
        state.sessions.isEmpty() && !anyProject -> HomeEmpty.Account(accountEmpty(state))
        scope != HomeScope.All && tl.total == 0 && !filtering -> HomeEmpty.Scope
        else -> null
    }

    val items = mutableListOf<HomeItem>()
    // Only an approval is decided on the card; a question, a plan or a popup is a row like the rest.
    val card = tl.attention.firstOrNull()?.takeIf { statusOf(it, state.machines[it.machineId]) == Status.APPROVAL }
    if (card != null) items += HomeItem.AttentionCard(card)
    val waiting = if (card == null) tl.attention else tl.attention.drop(1)
    val rows = if (shown.allAttention || filtering) waiting else waiting.take(ATTENTION_MAX)
    val hidden = waiting.size - rows.size
    rows.forEachIndexed { i, s -> items += HomeItem.Attention(s, first = i == 0, last = i == rows.lastIndex && hidden == 0) }
    if (hidden > 0) items += HomeItem.MoreAttention(hidden)
    if (tl.running.isNotEmpty()) {
        items += HomeItem.Label("running", "进行中")
        val running = if (shown.allRunning || filtering) tl.running else tl.running.take(RUNNING_MAX)
        running.forEachIndexed { i, s -> items += HomeItem.Running(s, last = i == running.lastIndex) }
        if (running.size < tl.running.size) items += HomeItem.MoreRunning(tl.running.size - running.size)
    }
    val ended = tl.ended.take(maxOf(0, shown.ended))
    for ((group, sessions) in groupEnded(ended, now, zone)) {
        items += HomeItem.Label(groupId(group), groupTitle(group, now, zone))
        sessions.forEach { items += HomeItem.Ended(it) }
    }
    // More to show: held here and not rendered yet, or still on the server.
    val held = ended.size < tl.ended.size
    val more = held || (!filtering && state.moreSessions && tl.ended.size < tl.endedTotal)
    // What a filter matched is only known once every page is here.
    val tail = if (filtering) filterTail(tl.size, query).takeUnless { wantAll } else endedTail(tl.endedTotal, more)
    // The listing's first page could not be fetched: said here with its retry, whatever the list wants.
    val listFailed = !state.sessionsListed && state.sessionsError.isNotEmpty()
    if (tail != null || more || wantAll || wantPage || listFailed) {
        // While pages are on their way the line says so, unless it already counts the scope's sessions;
        // a page that failed is said either way, with its retry.
        items += HomeItem.Tail(
            if (listFailed || ((wantAll || wantPage) && (tail == null || state.sessionsError.isNotEmpty())))
                HomeTail.Earlier(earlierTail(state), retry = state.conn == ConnState.ONLINE && state.sessionsError.isNotEmpty())
            else HomeTail.Text(tail.orEmpty()),
        )
    }
    return HomeList(
        items = items,
        empty = empty,
        showFilter = tl.total > FILTER_OVER || query.isNotEmpty(),
        wantAll = wantAll,
        wantPage = wantPage,
        // Once the ones before them are all rendered; those not fetched yet are asked for by wantPage.
        grows = more && shown.ended <= tl.ended.size,
        meta = tl.meta,
        endedHeld = tl.ended.size,
    )
}

// --- what a row writes ---

/** A session that has not ended, as its row writes it (design-system §6 会话行). */
data class SessionRowText(
    val title: String,
    val time: String,
    /** The status word; its tone draws the graphic before the row, the word itself is not coloured. */
    val status: Status,
    /** After the status word; empty when the word already says it. */
    val preview: String,
    /** The third line's words; empty for none. The CLI is not among them. */
    val meta: String,
    /** The CLI whose mark leads the third line, when the list used more than one; null for none. */
    val agent: Agent? = null,
) {
    /** The row as one sentence for the screen reader: the title first, then its state, what it last said, where it runs and when. */
    val spoken: String
        get() = listOf(title, status.text, preview, agent?.let { agentName(it) }.orEmpty(), meta.replace(" · ", "，"), time).filter { it.isNotEmpty() }.joinToString("，")
}

fun sessionRowText(state: RemoteState, s: SessionView, meta: RowMeta, now: Long, zone: ZoneId = ZoneId.systemDefault()): SessionRowText {
    val st = shownStatus(state, s)
    val pv = previewOf(s, st)
    return SessionRowText(
        title = titleOf(s, zone),
        time = whenText(s.updatedMs, now, zone),
        status = st,
        // The status word already says a plan waits.
        preview = if (pv == st.text) "" else pv,
        meta = metaLine(state, s, meta),
        agent = s.agent().takeIf { meta.agent },
    )
}

/** An ended session on its one line: the title, and at the right when it ended, after the project where the list spans projects. */
data class EndedRowText(val title: String, val project: String, val time: String) {
    /** 「blog · 7:50」, or either alone. */
    val right: String get() = listOf(project, time).filter { it.isNotEmpty() }.joinToString(" · ")

    val spoken: String get() = listOf(title, "已结束", project, time).filter { it.isNotEmpty() }.joinToString("，")
}

fun endedRowText(s: SessionView, project: Boolean, now: Long, zone: ZoneId = ZoneId.systemDefault()): EndedRowText =
    EndedRowText(titleOf(s, zone), if (project) projectName(projectKey(s)) else "", endedTime(endedMs(s), now, zone))

/**
 * One row of what waits (design-system §6 「等你的事」): the title, then
 * 「类型 · 摘要」 with the type in orange. Another project's row names its
 * project before the summary when the scope is one project.
 */
data class PendingRowText(
    val title: String,
    /** 请求执行, 「Codex 在问你」, 计划待批准, 终端里有弹窗. */
    val kind: String,
    /** After the type; empty for nothing. */
    val summary: String,
) {
    val spoken: String get() = listOf(title, kind, summary.replace(" · ", "，")).filter { it.isNotEmpty() }.joinToString("，")
}

/** [approval] and [question]: the first of each still waiting, from the session's events. */
fun pendingRowText(
    state: RemoteState,
    s: SessionView,
    scope: HomeScope,
    approval: ApprovalRequested?,
    question: String?,
    zone: ZoneId = ZoneId.systemDefault(),
): PendingRowText {
    // What it waits for stays true while the server is out of reach.
    val st = statusOf(s, state.machines[s.machineId])
    val (kind, about) = when (st) {
        // One line: the row is a summary, the card and the dock show the command whole.
        Status.APPROVAL -> (approval?.let(::approvalHead) ?: st.text) to (approval?.let { approvalBody(it).split(Regex("\\s+")).filter(String::isNotEmpty).joinToString(" ") } ?: "")
        Status.QUESTION -> "${agentName(s.agent())} 在问你" to question.orEmpty()
        Status.POPUP -> st.text to s.info?.statusDetail.orEmpty()
        else -> st.text to ""
    }
    val other = if (outsideScope(s, scope)) projectName(projectKey(s)) else ""
    return PendingRowText(titleOf(s, zone), kind, listOf(other, about).filter { it.isNotEmpty() }.joinToString(" · "))
}

/** The head and the place of the dark card of an approval (design-system §6 「终端卡」). */
data class ApprovalCardText(
    /** 请求执行, 「请求权限 · Write」. */
    val kind: String,
    /** 「剩 8 分钟，超时拒绝」, 未连接 out of reach of the server, or empty for no deadline. */
    val time: String,
    /** The deadline is less than a minute away: the time turns orange. */
    val hot: Boolean,
    val title: String,
    /** 「项目 · 机器」. */
    val place: String,
) {
    val spoken: String get() = listOf(title, kind, place.replace(" · ", "，"), time).filter { it.isNotEmpty() }.joinToString("，")
}

/** [timeoutNote]: the home page's card says what the end of the time does (「，超时拒绝」); a docked one does not. */
fun approvalCardText(
    state: RemoteState,
    s: SessionView,
    approval: ApprovalRequested,
    timeoutNote: Boolean,
    now: Long,
    zone: ZoneId = ZoneId.systemDefault(),
): ApprovalCardText {
    val deadline = if (approval.hasDeadline()) approval.deadline.seconds * 1000 else 0
    val left = deadline - now
    val time = when {
        state.unlinked -> "未连接"
        deadline > 0 -> "剩 ${minutesLeft(deadline, now)} 分钟" + if (timeoutNote && left > 0) "，超时拒绝" else ""
        else -> ""
    }
    return ApprovalCardText(
        kind = approvalHead(approval),
        time = time,
        hot = !state.unlinked && deadline > 0 && left < 60_000,
        title = titleOf(s, zone),
        // The computer is named only where the account has more than one (design-system §6 终端卡).
        place = listOfNotNull(projectName(projectKey(s)), machineName(state.machines[s.machineId], s.machineId).takeIf { state.machines.size > 1 }).joinToString(" · "),
    )
}

// --- the sentence at the top ---

/** The sentence at the top of the home page (design-system §6 「大标题」): how things stand, and the line under it. */
data class HomeHeadline(
    val kind: Kind,
    /** The whole sentence. */
    val title: String,
    /** The line under it; empty for none. */
    val sub: String = "",
    /** The line under it is the reason the server cannot be reached, with nothing kept to show: red. */
    val subError: Boolean = false,
    /** The line under it is a path, in mono. */
    val subPath: Boolean = false,
    /** 详情 at the end of the line under it opens why the server cannot be reached. */
    val details: Boolean = false,
) {
    /** Which sentence it is: the stretch of the way that is down, or how the sessions stand. */
    sealed interface Kind {
        data object NoNetwork : Kind
        data object Connecting : Kind
        data object Reconnecting : Kind
        data object NoServer : Kind
        /** 「N 件事等你」: [count] is the number, drawn orange. */
        data class Waiting(val count: Int) : Kind
        data object Running : Kind
        data object NothingWaits : Kind
        data object NoSessions : Kind
        data object Unreadable : Kind
        data object EmptyProject : Kind
    }

    /** Whether the sentence is about the connection; the page's buttons wait for it then. */
    val unlinked: Boolean
        get() = kind == Kind.NoNetwork || kind == Kind.Connecting || kind == Kind.Reconnecting || kind == Kind.NoServer
}

/**
 * The first of these that holds: the connection when it is down (the
 * nearest stretch, as 「断线时显示什么」 says it), what waits for the whole
 * account, what runs in the scope, that the project chosen has none yet,
 * that no session was ever started, and else that nothing waits. The line
 * under it counts the scope's running and idle sessions apart. Null while
 * nothing was read yet.
 */
fun homeHeadline(state: RemoteState, scope: HomeScope): HomeHeadline? {
    // Something from before to show under the sentence: what was cached or listed.
    val kept = state.loaded || state.sessions.isNotEmpty()
    val before = "下面是上次的内容"
    when (state.link) {
        Link.NO_NETWORK -> return HomeHeadline(HomeHeadline.Kind.NoNetwork, "本机网络不通", if (kept) before else "联网后才能读取会话")
        Link.WAITING -> return if (state.synced) HomeHeadline(HomeHeadline.Kind.Reconnecting, "正在重连…", if (kept) before else "")
        else HomeHeadline(HomeHeadline.Kind.Connecting, "连接中…", if (kept) before else "")
        Link.NO_SERVER -> return if (kept) HomeHeadline(HomeHeadline.Kind.NoServer, "连不上服务器", "正在重试，$before", details = true)
        else HomeHeadline(HomeHeadline.Kind.NoServer, "连不上服务器", state.error.ifEmpty { "服务器没有回应" }, subError = true, details = true)
        Link.ONLINE -> {}
    }
    if (!state.loaded && state.sessions.isEmpty()) return null
    val tl = homeTimeline(state, scope)
    var running = 0
    var idle = 0
    for (s in tl.running) when (statusOf(s, state.machines[s.machineId]).tone) {
        Tone.RUN -> running++
        Tone.IDLE -> idle++
        else -> {}
    }
    if (tl.attention.isNotEmpty()) {
        val n = tl.attention.size
        val parts = listOf(if (running > 0) "$running 个会话在运行" else "", if (idle > 0) (if (running > 0) "$idle 个空闲" else "$idle 个会话空闲") else "")
        return HomeHeadline(HomeHeadline.Kind.Waiting(n), "$n 件事等你", parts.filter { it.isNotEmpty() }.joinToString("，"))
    }
    if (running > 0) return HomeHeadline(HomeHeadline.Kind.Running, "$running 个会话在运行", if (idle > 0) "另有 $idle 个空闲" else "")
    if (scope is HomeScope.Of && tl.total == 0) return HomeHeadline(HomeHeadline.Kind.EmptyProject, "这个项目还没有会话", shortPath(scope.key), subPath = true)
    if (state.sessions.isEmpty()) {
        // The listing failed before anything came: that there are none is not known.
        return if (!state.sessionsListed && state.sessionsError.isNotEmpty()) HomeHeadline(HomeHeadline.Kind.Unreadable, "暂时读不到会话")
        else HomeHeadline(HomeHeadline.Kind.NoSessions, "还没有会话")
    }
    return HomeHeadline(HomeHeadline.Kind.NothingWaits, "没有等你的事", if (idle > 0) "$idle 个会话空闲" else "")
}

/** The count on the 会话 tab: everything that waits for the user, kept while the server is out of reach. */
fun waitingCount(state: RemoteState): Int = state.sessions.values.count(::needsAttention)
