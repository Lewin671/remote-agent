// The home page as a session timeline with a scope (client-v27,
// architecture §8 「主页的范围」): which sessions it lists, how the ended
// ones are grouped by day, what a row writes, and filtering by title.
package io.github.lewin671.remoteagent.core

import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.time.temporal.ChronoUnit

/** What the home page lists: one project, or every project. */
sealed interface HomeScope {
    data object All : HomeScope
    data class Of(val key: String) : HomeScope
}

/** 需要处理 shows this many cards before 「还有 N 项待处理」. */
const val ATTENTION_MAX = 3

/** 进行中 shows this many rows before 「还有 N 个进行中」. */
const val RUNNING_MAX = 5

/** Ended sessions are rendered this many at a time. */
const val ENDED_PAGE = 30

/** With more sessions than this in the scope the home page offers the title filter; with more projects, the scope menu a search. */
const val FILTER_OVER = 8

/** The projects on the lists: pinned in their order, then the others; removed ones are not among them. */
fun listedProjects(state: RemoteState, copy: MessageResolver): List<Project> = projectGroups(state, copy = copy).let { it.pinned + it.others }

/** The scope before the user chose one: the only listed project, else every project. */
fun defaultScope(state: RemoteState, copy: MessageResolver): HomeScope = listedProjects(state, copy = copy).singleOrNull()?.let { HomeScope.Of(it.key) } ?: HomeScope.All

/**
 * The scope the home page uses: the one remembered on this device, or the
 * default when none was chosen, when the remembered project was removed or
 * is not there, and when 全部项目 was remembered but there is no more than
 * one project to tell apart (the menu does not offer it then).
 */
fun resolveScope(remembered: HomeScope?, state: RemoteState, copy: MessageResolver): HomeScope {
    val listed = listedProjects(state, copy = copy)
    return when (remembered) {
        is HomeScope.Of -> if (listed.any { it.key == remembered.key }) remembered else defaultScope(state, copy = copy)
        HomeScope.All -> if (listed.size > 1) remembered else defaultScope(state, copy = copy)
        null -> defaultScope(state, copy = copy)
    }
}

/** The key the remembered scope's project is marked with: the account's, for this use alone. */
fun scopeKey(root: ByteArray): ByteArray = Keys.deriveKey(root, "home-scope")

/**
 * The scope as this device's settings keep it. Those are plaintext on disk,
 * so a project is written as a mark only the account's key makes
 * (HMAC-SHA256 of its path under [key]), never as its path.
 */
fun encodeScope(scope: HomeScope, key: ByteArray): String = when (scope) {
    HomeScope.All -> "all"
    is HomeScope.Of -> "project:" + hmacSha256(key, scope.key.toByteArray()).hex()
}

/**
 * The scope read back: the one of [listed] whose mark was stored. Null for
 * nothing stored, something else, and a project that is not listed.
 */
fun decodeScope(stored: String?, key: ByteArray, listed: List<Project>): HomeScope? = when (stored) {
    null -> null
    "all" -> HomeScope.All
    else -> listed.firstOrNull { encodeScope(HomeScope.Of(it.key), key) == stored }?.let { HomeScope.Of(it.key) }
}

/** Which of 项目, CLI and 机器 a row's third line writes: only what differs between the rows of its list. */
data class RowMeta(val project: Boolean = false, val agent: Boolean = false, val machine: Boolean = false)

/**
 * What the rows of a list write: the project only where the list spans
 * projects, the CLI only when [sessions] (everything in the scope or the
 * list) used more than one, the machine only when the account has more than
 * one computer.
 */
fun rowMeta(state: RemoteState, sessions: Collection<SessionView>, project: Boolean): RowMeta =
    RowMeta(project = project, agent = sessions.map { it.agent() }.distinct().size > 1, machine = state.machines.size > 1)

/**
 * A row's third line: 分叉 · 项目 · 机器, each only when it says something; empty for none.
 * The CLI is not among the words: its mark leads the line ([RowMeta.agent] says whether).
 */
fun metaLine(state: RemoteState, s: SessionView, meta: RowMeta): MessageText = buildList {
    if (forked(s)) add(UiMessages.homeForked().messageText())
    if (meta.project) add(listProjectName(projectKey(s)))
    if (meta.machine) add(listMachineName(state.machines[s.machineId], s.machineId))
}.joinMessages(" · ")

/** Whether a title has [query] in it, ignoring case; a blank query matches everything. */
fun titleMatches(s: SessionView, query: String, zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): Boolean =
    query.isBlank() || titleOf(s, zone, copy = copy).contains(query.trim(), ignoreCase = true)

/**
 * The home page's lists for a scope.
 * [attention] is not scoped: every session of the account that waits, so a
 * project chosen as the scope never hides another one's approval.
 * [running] and [ended] are the scope's own; under 全部项目 a removed
 * project's sessions are only among the first two.
 * [ended] holds the ended sessions fetched so far: the listing comes a
 * page at a time (architecture §8 「会话列表分页」). [endedTotal] is how many
 * the scope has by the machines' project summaries, and [total] the scope's
 * sessions: those that have not ended and [endedTotal]. Neither depends on
 * [query].
 */
class HomeTimeline(
    val attention: List<SessionView>,
    val running: List<SessionView>,
    val ended: List<SessionView>,
    val total: Int,
    val meta: RowMeta,
    val endedTotal: Int = ended.size,
) {
    /** How many sessions are listed, for the line under a filtered list. */
    val size: Int get() = attention.size + running.size + ended.size
}

/** Whether a session belongs to another project than the one the scope names. */
fun outsideScope(s: SessionView, scope: HomeScope): Boolean = scope is HomeScope.Of && projectKey(s) != scope.key

fun homeTimeline(state: RemoteState, scope: HomeScope, query: String = "", zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): HomeTimeline {
    val all = state.sessions.values
    val waiting = all.filter(::needsAttention).sortedWith(attentionOrder)
    val waitingIds = waiting.mapTo(HashSet()) { it.id }
    val hidden = state.prefs?.hidden.orEmpty()
    val running = all.filter { it.info?.taskOwned != true && !exited(it) && it.id !in waitingIds && !outsideScope(it, scope) }.sortedWith(liveOrder)
    val ended = all.filter { it.info?.taskOwned != true && exited(it) && if (scope is HomeScope.Of) projectKey(it) == scope.key else projectKey(it) !in hidden }.sortedWith(endedOrder)
    val live = waiting.filter { !outsideScope(it, scope) } + running
    val inScope = live + ended
    // How many ended sessions the scope has is the summaries' to say: only
    // some of them are held here. Never fewer than are.
    val summed = projects(state, copy = copy).filter { if (scope is HomeScope.Of) it.key == scope.key else it.key !in hidden }.sumOf { it.ended }
    val endedTotal = maxOf(summed, ended.size)
    val match = { s: SessionView -> titleMatches(s, query, zone, copy = copy) }
    return HomeTimeline(
        attention = waiting.filter(match),
        running = running.filter(match),
        ended = ended.filter(match),
        total = live.size + endedTotal,
        meta = rowMeta(state, inScope, project = scope == HomeScope.All),
        endedTotal = endedTotal,
    )
}

/**
 * Whether showing the first [want] of a scope's ended sessions takes another
 * page of the listing. [held] are the ones fetched so far, newest first, and
 * [total] how many the scope has by the summaries. A page is due while fewer
 * are held than are to be shown, and while what would be shown reaches past
 * the pages fetched: sessions kept from before a reconnect, or fetched on
 * their own, with others not fetched yet before them. A page may hold none
 * of a project's sessions; asking again goes on until enough are held or no
 * page is left. Before the listing's first page came, that page is the one
 * due.
 */
fun needsEndedPage(state: RemoteState, held: List<SessionView>, want: Int, total: Int): Boolean {
    if (!state.moreSessions) return false
    return held.size < minOf(want, total) || held.take(want).any { pastFetched(state, it) }
}

/**
 * Whether a session held here lies past the pages fetched: kept from before
 * a reconnect, or fetched by its ID, with sessions not fetched yet before
 * it in the listing. One that has not ended never does.
 */
fun pastFetched(state: RemoteState, s: SessionView): Boolean =
    state.sessionsNext?.let { afterPlace(s.listedEndedMs, s.id, it) } == true

/** The line under a card of 需要处理: the project where the list spans projects or the card is from outside the scope, and the machine when there are several. */
fun cardMeta(state: RemoteState, s: SessionView, scope: HomeScope, copy: MessageResolver): List<String> = buildList {
    if (scope == HomeScope.All || outsideScope(s, scope)) add(projectName(projectKey(s), copy = copy))
    if (state.machines.size > 1) add(machineName(state.machines[s.machineId], s.machineId, copy = copy))
}

// --- ended sessions by day ---

/** The groups ended sessions fall in, by the local calendar day they ended on. */
sealed interface EndedGroup {
    data object Today : EndedGroup
    data object Yesterday : EndedGroup
    /** Two to six days ago. */
    data object Week : EndedGroup
    data class Month(val year: Int, val month: Int) : EndedGroup
}

private fun dayOf(ms: Long, zone: ZoneId): LocalDate = Instant.ofEpochMilli(ms).atZone(zone).toLocalDate()

/** The group of a session that ended at [ms]; a time ahead of this device's clock counts as today. */
fun endedGroup(ms: Long, now: Long, zone: ZoneId = ZoneId.systemDefault()): EndedGroup {
    val day = dayOf(ms, zone)
    return when (ChronoUnit.DAYS.between(day, dayOf(now, zone))) {
        in Long.MIN_VALUE..0 -> EndedGroup.Today
        1L -> EndedGroup.Yesterday
        in 2..6 -> EndedGroup.Week
        else -> EndedGroup.Month(day.year, day.monthValue)
    }
}

/** 今天, 昨天, 近 7 天, 9 月, and 2025 年 12 月 for a month of another year. */
fun groupTitle(g: EndedGroup, now: Long, zone: ZoneId = ZoneId.systemDefault()): UiMessage = when (g) {
    EndedGroup.Today -> UiMessages.timeToday()
    EndedGroup.Yesterday -> UiMessages.timeYesterday()
    EndedGroup.Week -> UiMessages.timeLastWeek()
    is EndedGroup.Month -> if (g.year == dayOf(now, zone).year) UiMessages.timeMonthGroup(g.month) else UiMessages.timeYearMonthGroup(g.month, g.year)
}

/** Time-of-day stays a clock; dates and weekdays keep message keys until displayed. */
fun endedTime(ms: Long, now: Long, zone: ZoneId = ZoneId.systemDefault()): MessageText {
    if (ms <= 0) return "".messageText()
    return when (endedGroup(ms, now, zone)) {
        EndedGroup.Today, EndedGroup.Yesterday -> hhmm(ms, zone).messageText()
        EndedGroup.Week -> UiMessages.timeWeekday(listOf("mon", "tue", "wed", "thu", "fri", "sat", "sun")[dayOf(ms, zone).dayOfWeek.value - 1]).messageText()
        is EndedGroup.Month -> dayOf(ms, zone).let { UiMessages.timeMonthDay(it.dayOfMonth, it.monthValue).messageText() }
    }
}

/** Ended sessions under their groups, both in the order given (newest first). */
fun groupEnded(ended: List<SessionView>, now: Long, zone: ZoneId = ZoneId.systemDefault()): List<Pair<EndedGroup, List<SessionView>>> {
    val groups = LinkedHashMap<EndedGroup, MutableList<SessionView>>()
    for (s in ended) groups.getOrPut(endedGroup(endedMs(s), now, zone)) { mutableListOf() } += s
    return groups.map { it.key to it.value }
}

/**
 * The line that ends the list: how many ended sessions the scope has
 * ([total], by the summaries), once they do not fit one page; null before
 * that. [more]: not all of them are shown yet.
 */
fun endedTail(total: Int, more: Boolean): UiMessage? = when {
    total <= ENDED_PAGE -> null
    more -> UiMessages.homeTotalLoadEarlier(total)
    else -> UiMessages.homeTotal(total)
}

/** The listing tail keeps its diagnostics raw, inside a complete translated sentence. */
fun earlierTail(state: RemoteState): MessageText = when {
    state.conn != ConnState.ONLINE -> UiMessages.homeAndroidEarlierOffline().messageText()
    state.sessionsError.isNotEmpty() && !state.sessionsListed -> localized { copy -> copy(UiMessages.homeAndroidListFailedRetry(state.sessionsError.resolve(copy))) }
    state.sessionsError.isNotEmpty() -> localized { copy -> copy(UiMessages.homeAndroidEarlierFailedRetry(state.sessionsError.resolve(copy))) }
    else -> UiMessages.homeLoadingEarlier().messageText()
}

/** The line that ends a filtered list. */
fun filterTail(count: Int, query: String): UiMessage =
    if (count == 0) UiMessages.homeNoMatches(query.trim()) else UiMessages.homeMatches(count, query.trim())

// --- the scope menu ---

/** A row of 看哪个项目. */
data class ScopeRow(val scope: HomeScope, val name: MessageText, val detail: MessageText, val current: Boolean)

/** Counts use the same waiting/running split as the home, including offline computers. */
fun activityCounts(sessions: Collection<SessionView>): UiMessage? {
    val waiting = sessions.count(::needsAttention)
    val running = sessions.count { it.info?.taskOwned != true && !exited(it) && !needsAttention(it) }
    return when {
        waiting > 0 && running > 0 -> UiMessages.projectsPendingAndLive(waiting, running)
        waiting > 0 -> UiMessages.projectsPendingCount(waiting)
        running > 0 -> UiMessages.projectsLiveCount(running)
        else -> null
    }
}

/** The same path and activity counts wherever a project is listed. */
fun projectDetail(pr: Project): MessageText =
    listOfNotNull(shortPath(pr.key).messageText(), activityCounts(pr.sessions)?.messageText()).joinMessages(" · ")

/**
 * The scope menu: 全部项目 when there is more than one project to choose
 * between, then the pinned projects, then the others in the lists' order.
 * [search] keeps the projects whose name or path has it.
 */
fun scopeRows(state: RemoteState, current: HomeScope, search: String = "", copy: MessageResolver): List<ScopeRow> {
    val listed = listedProjects(state, copy = copy)
    val q = search.trim()
    return buildList {
        if (listed.size > 1 && q.isEmpty()) {
            val counts = activityCounts(state.sessions.values)
            add(ScopeRow(HomeScope.All, UiMessages.homeAllProjects().messageText(), (counts ?: UiMessages.projectsCount(listed.size)).messageText(), current == HomeScope.All))
        }
        for (pr in listed) {
            if (q.isNotEmpty() && !pr.name.contains(q, ignoreCase = true) && !pr.key.contains(q, ignoreCase = true)) continue
            add(ScopeRow(HomeScope.Of(pr.key), listProjectName(pr.key), projectDetail(pr), current == HomeScope.Of(pr.key)))
        }
    }
}
