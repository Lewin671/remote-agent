// View models derived from the decrypted state, as in web/src/model.ts:
// projects, statuses, and what a session's events say is pending.
package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.corebind.Projection
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalDecision
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.EventBatch
import io.github.lewin671.remoteagent.proto.InputState
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.LiveSnapshot
import io.github.lewin671.remoteagent.proto.PlanProposed
import io.github.lewin671.remoteagent.proto.ProjectionState
import io.github.lewin671.remoteagent.proto.QuestionAsked
import io.github.lewin671.remoteagent.proto.SendMode
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.TokenUsage
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.time.ZonedDateTime

// --- CLIs ---

/** The CLI a session runs; sessions from before Claude Code support are Codex. */
fun SessionView.agent(): Agent = agentInfo(info?.agent).agent

/** The CLIs a machine can start; Codex while it lists none. */
fun machineAgents(m: MachineView?): List<Agent> {
    val listed = m?.meta?.agentsList?.map { agentInfo(it.agent).agent }?.distinct().orEmpty()
    return listed.ifEmpty { listOf(DEFAULT_AGENT) }
}

// --- projects ---

data class Project(
    val key: String,
    val name: String,
    /**
     * The project's sessions held here, recent activity first, used for new-session defaults. UI
     * lists sort separately.
     */
    val sessions: List<SessionView>,
    val updatedMs: Long,
    val hasLive: Boolean,
    val latestCreatedMs: Long,
    val paths: List<String>,
    val machines: List<String>,
    /**
     * The machines it has sessions on: those of the sessions held here, recent first, then the
     * others whose summary names it.
     */
    val machineIds: List<String> = emptyList(),
    /**
     * How many of its sessions have ended, on all machines: by their summaries, and no fewer than
     * are held here.
     */
    val ended: Int = 0,
)

/**
 * The project a session belongs to: its working directory's absolute path, the same on every
 * machine.
 */
fun projectKey(s: SessionView): String = s.info?.cwd.orEmpty()

/** A project's name: the last folder of its path. */
fun projectName(key: String, copy: MessageResolver): String = key.split("/").lastOrNull { it.isNotEmpty() } ?: copy(UiMessages.projectsUnknown())

/** What the machines say of a project (MachineMeta.projects), added up. */
private class ProjectSums {
    val endedBy = HashMap<String, Int>()
    var latestCreatedMs = 0L
    var latestEndedMs = 0L
    val machineIds = ArrayList<String>()
}

/**
 * The account's projects (architecture §8 「项目从哪来」): the machines' project summaries together with
 * the sessions held here. Sessions come a page at a time, so a project whose sessions all ended
 * long ago is known from the summaries alone. Whether one has a session that has not ended is read
 * off the sessions, which are all held; the latest creation is the later of the two; its machines
 * are those of both. A project added with 新项目… is known from its settings alone until its first
 * session: it is listed while the machine chosen for it is one of the account's, as created when it
 * was added.
 */
fun projects(state: RemoteState, copy: MessageResolver): List<Project> {
    val byKey = LinkedHashMap<String, MutableList<SessionView>>()
    for (s in state.sessions.values.filter { it.info?.taskOwned != true || needsAttention(it) }) byKey.getOrPut(projectKey(s)) { mutableListOf() } += s
    val sums = LinkedHashMap<String, ProjectSums>()
    // The machine whose newest session there is the latest comes first.
    val summaries =
        state.machines.values
            .flatMap { m -> m.meta?.projectsList.orEmpty().map { m.id to it } }
            .sortedWith(
                compareByDescending<
                        Pair<String, io.github.lewin671.remoteagent.proto.ProjectSummary>,
                    > {
                        it.second.latestCreatedMs
                    }
                    .thenBy { it.first },
            )
    for ((machineId, p) in summaries) {
        val sum = sums.getOrPut(p.path) { ProjectSums() }
        sum.endedBy[machineId] = p.ended
        sum.latestCreatedMs = maxOf(sum.latestCreatedMs, p.latestCreatedMs)
        sum.latestEndedMs = maxOf(sum.latestEndedMs, p.latestEndedMs)
        if (machineId !in sum.machineIds) sum.machineIds += machineId
    }
    val added =
        state.prefs
            ?.prefs
            ?.settingsList
            .orEmpty()
            .filter {
                it.path.isNotEmpty() &&
                    it.path !in byKey &&
                    it.path !in sums &&
                    it.machineId in state.machines
            }
            .associateBy { it.path }
    return (byKey.keys + sums.keys + added.keys)
        .distinct()
        .map { key ->
            val sorted =
                byKey[key]
                    .orEmpty()
                    .sortedWith(compareByDescending<SessionView> { it.updatedMs }.thenBy { it.id })
            val sum = sums[key]
            val own = added[key]
            val machineIds =
                (sorted.map { it.machineId } +
                        sum?.machineIds.orEmpty() +
                        listOfNotNull(own?.machineId))
                    .distinct()
            Project(
                key = key,
                name = projectName(key, copy = copy),
                sessions = sorted,
                updatedMs =
                    maxOf(
                        sorted.maxOfOrNull { it.updatedMs } ?: 0,
                        sum?.latestCreatedMs ?: 0,
                        sum?.latestEndedMs ?: 0,
                        own?.updatedMs ?: 0,
                    ),
                hasLive = sorted.any { !exited(it) },
                latestCreatedMs =
                    maxOf(
                        sorted.maxOfOrNull { createdMs(it) } ?: 0,
                        sum?.latestCreatedMs ?: 0,
                        own?.updatedMs ?: 0,
                    ),
                paths =
                    sorted
                        .mapNotNull { it.info?.cwd?.takeIf { c -> c.isNotEmpty() } }
                        .distinct()
                        .ifEmpty { listOf(key).filter { it.isNotEmpty() } },
                machines = machineIds.map { machineName(state.machines[it], it, copy = copy) }.distinct(),
                machineIds = machineIds,
                // Counted machine by machine: each machine's count, or what is held of its
                // sessions where that is more (its metadata may not have arrived, or be behind).
                ended =
                    machineIds.sumOf { m ->
                        maxOf(
                            sum?.endedBy?.get(m) ?: 0,
                            sorted.count { it.machineId == m && exited(it) },
                        )
                    },
            )
        }
        .sortedWith(
            compareByDescending<Project> { it.hasLive }
                .thenByDescending { it.latestCreatedMs }
                .thenBy { it.key },
        )
}

/**
 * One project of [projects], or null when no summary, session held here or stored settings name it.
 */
fun project(state: RemoteState, key: String, copy: MessageResolver): Project? =
    projects(state, copy = copy).firstOrNull { it.key == key }

/**
 * The projects a machine has sessions in: by its summary and by the sessions held here, in that
 * order.
 */
fun machineProjects(state: RemoteState, machineId: String): List<String> =
    (state.machines[machineId]?.meta?.projectsList.orEmpty().map { it.path } +
            state.sessions.values.filter { it.machineId == machineId }.mapNotNull { it.info?.cwd })
        .distinct()

fun isHidden(state: RemoteState, key: String): Boolean = state.prefs?.hidden?.contains(key) == true

fun isPinned(state: RemoteState, key: String): Boolean =
    state.prefs?.let { key in it.pinned && key !in it.hidden } == true

fun machineName(m: MachineView?, id: String = "", copy: MessageResolver): String =
    m?.meta?.hostname?.removeSuffix(".local")?.takeIf { it.isNotEmpty() }
        ?: m?.id?.take(8)
        ?: id.take(8).ifEmpty { copy(UiMessages.commonUnknownComputer()) }

// A session that has not ended or is not waiting has no such timestamp and
// falls back to creation time; incomplete metadata uses zero, never the
// changing relay timestamp.
fun createdMs(s: SessionView): Long =
    s.info
        ?.takeIf { it.hasCreatedAt() }
        ?.createdAt
        ?.let { it.seconds * 1000 + it.nanos / 1_000_000 } ?: 0

fun endedMs(s: SessionView): Long =
    s.info?.takeIf { it.hasEndedAt() }?.endedAt?.let { it.seconds * 1000 + it.nanos / 1_000_000 }
        ?: createdMs(s)

val sessionOrder: Comparator<SessionView> = Comparator { a, b ->
    val group = exited(a).compareTo(exited(b))
    if (group != 0) group
    else if (exited(a)) compareByDescending<SessionView> { endedMs(it) }.thenBy { it.id }.compare(a, b)
    else compareByDescending<SessionView> { createdMs(it) }.thenBy { it.id }.compare(a, b)
}

// --- sessions ---

enum class Tone {
    RUN,
    WAIT,
    IDLE,
    OFF,
}

/** One state per session, the highest in the priority list of the design (client-v1, 状态优先级). */
enum class Status(val text: UiMessage, val tone: Tone) {
    LOADING(UiMessages.listStateAndroidLoading(), Tone.OFF),
    EXITED(UiMessages.listStateEnded(), Tone.OFF),
    OFFLINE(UiMessages.listStateOffline(), Tone.OFF),
    /**
     * Only shown ([shownStatus]): the server is out of reach, so what the session is doing is not
     * known.
     */
    UNLINKED(UiMessages.listStateUnlinked(), Tone.OFF),
    APPROVAL(UiMessages.listStateApproval(), Tone.WAIT),
    QUESTION(UiMessages.listStateQuestion(), Tone.WAIT),
    PLAN(UiMessages.listStatePlan(), Tone.WAIT),
    POPUP(UiMessages.listStatePopup(), Tone.WAIT),
    RUNNING(UiMessages.listStateRunning(), Tone.RUN),
    STARTING(UiMessages.listStateStarting(), Tone.RUN),
    IDLE(UiMessages.listStateIdle(), Tone.IDLE),
}

fun exited(s: SessionView): Boolean = s.info?.status == SessionStatus.SESSION_STATUS_EXITED

/**
 * The state to show for a session. Out of reach of the server, the last one heard is not shown as
 * if it still held (client-v30 「断线」); that a session ended stays true.
 */
fun shownStatus(state: RemoteState, s: SessionView): Status {
    val st = statusOf(s, state.machines[s.machineId])
    return if (state.peer(s.machineId).unlinked && st != Status.EXITED && st != Status.LOADING) Status.UNLINKED
    else st
}

fun statusOf(s: SessionView, machine: MachineView?): Status {
    val i = s.info ?: return Status.LOADING
    if (i.status == SessionStatus.SESSION_STATUS_EXITED) return Status.EXITED
    if (machine != null && !machine.pathState.reachable) return Status.OFFLINE
    if (i.pendingApprovals > 0) return Status.APPROVAL
    if (i.pendingQuestions > 0) return Status.QUESTION
    if (i.planOpen) return Status.PLAN
    if (i.status == SessionStatus.SESSION_STATUS_BLOCKED) return Status.POPUP
    if (i.status == SessionStatus.SESSION_STATUS_RUNNING) return Status.RUNNING
    if (i.status == SessionStatus.SESSION_STATUS_STARTING) return Status.STARTING
    return Status.IDLE
}

// Raw popup details are adapter diagnostics, not user-facing copy.
fun popupTitle(detail: String): MessageText =
    if (detail.isNotEmpty()) UiMessages.listStatePopup().messageText() else "".messageText()

/**
 * A hint for a popup in the terminal. Claude Code's trust dialog has its cursor on "No, exit", so
 * Enter alone ends the session (claude-code-notes.md §1); Codex's needs none. Same text as the web.
 */
fun popupHint(agent: Agent, detail: String): MessageText =
    if (detail == "trust folder") agentInfo(agent).trustFolderHint.orEmpty() else "".messageText()

/** Pending work stays pending while its computer is out of reach. */
fun needsAttention(s: SessionView): Boolean = statusOf(s, null).tone == Tone.WAIT

/** The session's title; without one, "<folder> · <start time>". */
fun titleOf(s: SessionView, zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String {
    val t = plain(s.info?.title.orEmpty()).trim()
    if (t.isNotEmpty()) return t
    val i = s.info ?: return copy(UiMessages.newSessionAndroidNewSession())
    val created = if (i.hasCreatedAt()) i.createdAt.seconds * 1000 else 0
    return if (created > 0) "${projectName(i.cwd, copy = copy)} · ${hhmm(created, zone)}" else projectName(i.cwd, copy = copy)
}

/** The most a session's title holds, counted in code points as the daemon counts them. */
const val TITLE_MAX = 60

/**
 * What the rename field starts with: the session's title, or nothing while it only has the
 * "<folder> · <start time>" placeholder. Same as the web.
 */
fun renameInitial(s: SessionView): String = titleInput(s.info?.title.orEmpty().trim())

/** What the rename field takes of [text]: one line, and nothing past [TITLE_MAX]. */
fun titleInput(text: String): String {
    val line = text.replace(Regex("[\\r\\n]+"), " ")
    return if (line.codePointCount(0, line.length) <= TITLE_MAX) line
    else line.substring(0, line.offsetByCodePoints(0, TITLE_MAX))
}

/**
 * The title to send for what was typed, trimmed as the daemon trims it; null when it is the title
 * the field started with and there is nothing to save. Empty asks for the automatic title back.
 */
fun renameTitle(typed: String, initial: String): String? = typed.trim().takeIf { it != initial }

/** The session was forked from another one (Session.forked_from). */
fun forked(s: SessionView): Boolean = s.info?.forkedFrom.orEmpty().isNotEmpty()

/**
 * Whether a message was ever sent in the session: its title comes from the first one, and the
 * history loaded so far may show one.
 */
fun sentMessage(s: SessionView, events: List<Event>): Boolean =
    s.info?.title.orEmpty().isNotEmpty() || events.any { it.hasUserMessage() }

/**
 * Why the session cannot be forked now, as the ⋯ menu writes it under 分叉会话; null when it can. A
 * fork takes an idle or ended session in which a message was sent. [running]: a turn is going on by
 * the events, which can be ahead of the session's status. The menu only offers a fork while the
 * computer is reachable and the session is read, so OFFLINE and LOADING do not come here; they read
 * as not up yet.
 */
fun forkBlocked(status: Status, running: Boolean, sent: Boolean): UiMessage? =
    when (status) {
        Status.RUNNING,
        Status.APPROVAL,
        Status.QUESTION -> UiMessages.sessionForkAfterTurn()
        Status.PLAN,
        Status.POPUP -> UiMessages.sessionForkAfterPending()
        Status.STARTING,
        Status.LOADING,
        Status.OFFLINE -> UiMessages.sessionForkAfterStart()
        Status.UNLINKED -> UiMessages.sessionAndroidForkConnected()
        Status.IDLE -> if (running) UiMessages.sessionForkAfterTurn() else if (!sent) UiMessages.sessionForkAfterMessage() else null
        Status.EXITED -> if (!sent) UiMessages.sessionForkAfterMessage() else null
    }

/** The session a fork made, or null when the answer names none. */
fun forkedSession(r: io.github.lewin671.remoteagent.proto.CommandResult?): String? =
    r?.takeIf { it.hasForkSession() && it.forkSession.hasSession() }
        ?.forkSession
        ?.session
        ?.id
        ?.takeIf { it.isNotEmpty() }

/** How a session that is not held here is being looked up by its ID. */
sealed interface SessionLookup {
    /** Asked for, with no answer that settles it yet. */
    data object Reading : SessionLookup

    /** The server has no such session, and kept saying so. */
    data object Missing : SessionLookup

    /** The server could not be asked; it is asked again after a while. */
    data class Failed(val reason: MessageText) : SessionLookup
}

/**
 * A session the server does not have is asked for this many times, [SESSION_ABSENT_WAIT_MS] apart,
 * before it counts as missing.
 */
const val SESSION_ABSENT_TRIES = 5
const val SESSION_ABSENT_WAIT_MS = 2_500L

/**
 * Looks a session up by its ID until it is held ([fetch] returns true) or known to be missing
 * (architecture §8 「按 ID 取」). A session just started is acknowledged by its daemon before the
 * daemon has uploaded it, so the server not having it only counts once it said so for ten seconds.
 * The server not answering is no answer: it is asked again, after three seconds and then twice as
 * long each time up to half a minute, and [report] says why meanwhile.
 */
suspend fun lookUpSession(
    fetch: suspend () -> Boolean,
    wait: suspend (Long) -> Unit = { kotlinx.coroutines.delay(it) },
    report: (SessionLookup) -> Unit,
) {
    var absent = 0
    var pause = 3_000L
    while (true) {
        try {
            if (fetch()) return
            if (++absent >= SESSION_ABSENT_TRIES) return report(SessionLookup.Missing)
            report(SessionLookup.Reading)
            pause = 3_000L
            wait(SESSION_ABSENT_WAIT_MS)
        } catch (e: kotlinx.coroutines.CancellationException) {
            throw e
        } catch (e: Exception) {
            report(SessionLookup.Failed(errorText(e)))
            wait(pause)
            pause = minOf(pause * 2, 30_000L)
        }
    }
}

/**
 * What a page about a session says instead of its content while the session is not held; null while
 * it is only being read.
 */
fun lookupNote(l: SessionLookup?, copy: MessageResolver): String? =
    when (l) {
        SessionLookup.Missing -> copy(UiMessages.commonAndroidCannotFindThisSession())
        is SessionLookup.Failed -> copy(UiMessages.commonAndroidCouldNotReadThisSessionValue1(l.reason.resolve(copy)))
        else -> null
    }

/** Table rows in list previews keep their words, without pipes or alignment rows. */
private fun previewTableText(text: String): String {
    val lines = text.split("\n")
    val pipe = Regex("(?<!\\\\)\\|")
    fun cells(line: String) = line.trim().removePrefix("|")
        .replace(Regex("(?<!\\\\)\\|$"), "").split(pipe).map { it.trim() }
    fun separator(line: String) = '|' in line && cells(line).all { it.matches(Regex(":?-{3,}:?")) }
    var inTable = false
    return lines.mapIndexed { i, line ->
        if (separator(line)) {
            inTable = true
            ""
        } else if ('|' in line && (line.trimStart().startsWith("|") ||
                Regex("(?<!\\\\)\\|\\s*$").containsMatchIn(line) || inTable || separator(lines.getOrNull(i + 1).orEmpty()))) {
            cells(line).filter { it.isNotEmpty() }.joinToString("、") { it.replace("\\|", "|") }
        } else {
            inTable = false
            line
        }
    }.joinToString("\n")
}

/** Text for one-line previews: Markdown marks and the CLIs' image placeholders removed. */
fun plain(text: String): String =
    previewTableText(text)
        .replace(Regex("\\[Image #\\d+]\\s*"), "")
        .replace(Regex("```[a-zA-Z0-9]*"), "")
        .replace(Regex("`([^`]*)`"), "$1")
        .replace(Regex("\\*\\*([^*]+)\\*\\*"), "$1")
        .replace(Regex("\\[([^\\]]+)\\]\\([^)]*\\)"), "$1")
        .replace(Regex("(?m)^\\s{0,3}(#{1,6}\\s+|[-*+]\\s+|>\\s?)"), "")
        .replace(Regex("\\s+"), " ")
        .trim()

/** The one-line preview for a list, following the session's status. */
fun previewOf(s: SessionView, status: Status, copy: MessageResolver): String {
    val i = s.info ?: return ""
    return when (status) {
        Status.PLAN -> copy(UiMessages.listStatePlan())
        Status.POPUP -> copy(UiMessages.listStatePopup())
        else -> plain(i.preview).ifEmpty { if (i.title.isEmpty()) copy(UiMessages.listStateNoMessage()) else "" }
    }
}

/** Short relative time, like the mockup: 刚刚, 3 分钟前, 9:12, 昨天, 9月27日. */
fun whenText(
    ms: Long,
    now: Long = System.currentTimeMillis(),
    zone: ZoneId = ZoneId.systemDefault(),
    copy: MessageResolver): String {
    if (ms <= 0) return ""
    val d = now - ms
    if (d < 60_000) return copy(UiMessages.timeJustNow())
    if (d < 3_600_000) return copy(UiMessages.timeMinutesAgo((d / 60_000).toInt()))
    val then = Instant.ofEpochMilli(ms).atZone(zone).toLocalDate()
    val today = Instant.ofEpochMilli(now).atZone(zone).toLocalDate()
    if (then == today) return hhmm(ms, zone)
    if (then == today.minusDays(1)) return copy(UiMessages.timeYesterday())
    return copy(UiMessages.timeMonthDay((then.dayOfMonth).toInt(), (then.monthValue).toInt()))
}

/** Elapsed hours today, as a machine's last-seen time is shown on the web. */
fun ago(ms: Long, now: Long = System.currentTimeMillis(), zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String {
    val elapsed = now - ms
    if (ms > 0 && elapsed >= 3_600_000 && elapsed < 86_400_000 &&
        Instant.ofEpochMilli(ms).atZone(zone).toLocalDate() == Instant.ofEpochMilli(now).atZone(zone).toLocalDate())
        return copy(UiMessages.timeHoursAgo((elapsed / 3_600_000).toInt()))
    return whenText(ms, now, zone, copy = copy)
}

fun hhmm(ms: Long, zone: ZoneId = ZoneId.systemDefault()): String {
    val t = ZonedDateTime.ofInstant(Instant.ofEpochMilli(ms), zone)
    return "${t.hour}:${"%02d".format(t.minute)}"
}

fun dateTime(ms: Long, zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String {
    val t = ZonedDateTime.ofInstant(Instant.ofEpochMilli(ms), zone)
    val today = LocalDate.now(zone)
    return if (t.toLocalDate() == today) hhmm(ms, zone)
        else copy(UiMessages.timeAndroidDayClock(hhmm(ms, zone), copy(UiMessages.timeMonthDay(t.dayOfMonth, t.monthValue))))
}

// --- approval modes ---

enum class Mode(val text: UiMessage, val proto: ApprovalMode) {
    ASK(UiMessages.modesAsk(), ApprovalMode.APPROVAL_MODE_ASK),
    AUTO(UiMessages.modesAuto(), ApprovalMode.APPROVAL_MODE_AUTO),
    PLAN(UiMessages.modesPlan(), ApprovalMode.APPROVAL_MODE_PLAN),
    FULL(UiMessages.modesFullAccess(), ApprovalMode.APPROVAL_MODE_FULL_ACCESS),
}

fun modeOf(m: ApprovalMode): Mode? = Mode.entries.firstOrNull { it.proto == m }

/** Claude Code's permission mode as a client mode, or null for its own ones such as acceptEdits. */
fun permissionMode(pm: String): Mode? =
    when (pm) {
        "default" -> Mode.ASK
        "auto" -> Mode.AUTO
        "plan" -> Mode.PLAN
        "bypassPermissions" -> Mode.FULL
        else -> null
    }

/** A mode as shown: one of the four, or a CLI's own mode by its name. */
data class ModeLabel(val text: String, val mode: Mode?)

/**
 * The approval mode named from turn-context fields: Claude Code's permission mode, or Codex's
 * approval fields.
 */
fun contextMode(
    reviewer: String,
    policy: String,
    sandbox: String,
    collaboration: String,
    permission: String = "",
): Mode? {
    if (permission.isNotEmpty()) return permissionMode(permission)
    if (reviewer.isEmpty() && policy.isEmpty() && collaboration.isEmpty()) return null
    if (collaboration == "plan") return Mode.PLAN
    if (sandbox == "danger-full-access" && policy == "never") return Mode.FULL
    if (reviewer == "auto_review") return Mode.AUTO
    return Mode.ASK
}

fun modeOf(s: SessionView): Mode? = modeOf(s.info?.mode ?: ApprovalMode.APPROVAL_MODE_UNSPECIFIED) ?: permissionMode(s.info?.permissionMode.orEmpty()) ?: s.info?.let { contextMode(it.approvalsReviewer, it.approvalPolicy, it.sandbox, it.collaborationMode) }

/**
 * The session's mode. Session.mode is either CLI's mode as a client mode; UNSPECIFIED with a
 * permission mode is one of Claude Code's own, shown by its name. With neither (a Codex context
 * outside the four modes), it is named from the approval fields.
 */
fun modeLabel(s: SessionView, copy: MessageResolver): ModeLabel? {
    val i = s.info ?: return null
    if (i.status == SessionStatus.SESSION_STATUS_STARTING) return null
    modeOf(i.mode)?.let {
        return ModeLabel(copy(it.text), it)
    }
    if (i.permissionMode.isNotEmpty()) {
        val m = permissionMode(i.permissionMode)
        return ModeLabel(m?.text?.let(copy) ?: i.permissionMode, m)
    }
    return contextMode(i.approvalsReviewer, i.approvalPolicy, i.sandbox, i.collaborationMode)?.let {
        ModeLabel(copy(it.text), it)
    }
}

/** Whether the daemon marks every reported model as lacking auto approval mode. */
fun allAutoUnavailable(models: List<io.github.lewin671.remoteagent.proto.ModelInfo>?): Boolean {
    return !models.isNullOrEmpty() && models.all { it.autoUnavailable }
}

/**
 * Whether the daemon reports auto approval as unavailable for a model.
 * An unknown model or a list not read yet gives no answer.
 */
fun autoUnavailable(
    models: List<io.github.lewin671.remoteagent.proto.ModelInfo>?,
    model: String,
): Boolean {
    if (allAutoUnavailable(models)) return true
    if (models == null || model.isEmpty()) return false
    return findModel(models, model) { it.slug }?.autoUnavailable == true
}

/**
 * The models a picker lists before 全部模型: a short list whole, so none is out of reach; a long
 * one its first three and the picked one, so 推理强度 below the list stays in sight.
 */
fun <M> shownModels(models: List<M>, picked: M?, all: Boolean): List<M> {
    if (all || models.size <= 5) return models
    val top = models.take(3)
    return if (picked == null || picked in top) top else top + picked
}

/**
 * The entry of a model list that a session's model is. Codex reports the slug it was started with;
 * Claude Code offers aliases (sonnet, opus) but records the full ID (claude-sonnet-5-5), so an
 * alias matches a full ID that has it as one of its words.
 */
fun <M> findModel(models: List<M>, id: String, slug: (M) -> String): M? {
    if (id.isEmpty()) return null
    // A "[1m]" suffix names the 1M window on the command line; it is not part of the ID.
    val id = id.removeSuffix("[1m]")
    models
        .firstOrNull { slug(it) == id }
        ?.let {
            return it
        }
    val words = id.lowercase().split(Regex("[-_.\\s]"))
    return models.firstOrNull { slug(it).lowercase() in words }
}

/** Whether the model a CLI reports ([id]) is the one chosen by slug or alias. */
fun modelIs(id: String, slug: String): Boolean =
    id.isNotEmpty() &&
        slug.isNotEmpty() &&
        (id == slug || slug.lowercase() in id.lowercase().split(Regex("[-_.\\s]")))

/**
 * A model's title as shown: the name the list gives it, or, when the list is not there (before it was read, or
 * from /model), the name Codex gives such slugs: gpt-6.1-sol is GPT-6.1-Sol.
 */
fun modelLabel(
    models: List<io.github.lewin671.remoteagent.proto.ModelInfo>?,
    id: String,
    agent: Agent = DEFAULT_AGENT,
): String {
    if (id.isEmpty()) return ""
    val m = if (models != null) findModel(models, id) { it.slug } else null
    if (m != null && m.displayName.isNotEmpty()) return m.displayName
    if (models == null) return id
    return agentInfo(agent).modelLabel(id)
}

/**
 * The approval mode a new session gets when none is chosen: the daemon's default for that CLI
 * (architecture §13), or null when the daemon has none and the CLI's own configuration decides.
 */
fun defaultMode(
    settings: io.github.lewin671.remoteagent.proto.DaemonSettings,
    agent: Agent = DEFAULT_AGENT,
): Mode? = modeOf(defaultsFor(settings, agent).mode)

/** Context window use in percent, or null when unknown. */
fun contextPercent(s: SessionView): Int? {
    val i = s.info ?: return null
    if (i.contextWindow <= 0) return null
    return minOf(100, (i.contextTokens * 100 / i.contextWindow).toInt())
}

// --- events ---

class RunningTool(
    val title: String,
    val callId: String,
    /** Event time of the start, from the daemon; 0 when unknown. */
    val since: Long,
    val kind: io.github.lewin671.remoteagent.proto.ToolKind =
        io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_UNSPECIFIED,
    val itemType: String = "",
    /** Seq of the ToolStarted event; its row in the chat keeps this place. */
    val seq: Long = 0,
    val input: String = "",
    val inputDigest: String = "",
    val awaitingApproval: Boolean = false,
)

/**
 * The call went on in the background: this event is its start, not its end. The daemon says so
 * (ToolCall.background).
 */
fun wentToBackground(c: io.github.lewin671.remoteagent.proto.ToolCall): Boolean =
    c.background !=
        io.github.lewin671.remoteagent.proto.ToolBackground.TOOL_BACKGROUND_UNSPECIFIED &&
        c.background != io.github.lewin671.remoteagent.proto.ToolBackground.UNRECOGNIZED

/** Why a call is in the background and what that means, as the web client says it. */
fun backgroundNote(b: io.github.lewin671.remoteagent.proto.ToolBackground): UiMessage = when (b) {
    io.github.lewin671.remoteagent.proto.ToolBackground.TOOL_BACKGROUND_REQUESTED -> UiMessages.toolsBackgroundAgent()
    io.github.lewin671.remoteagent.proto.ToolBackground.TOOL_BACKGROUND_TIMED_OUT -> UiMessages.toolsBackgroundTimeout()
    io.github.lewin671.remoteagent.proto.ToolBackground.TOOL_BACKGROUND_BY_USER -> UiMessages.toolsBackgroundTerminal()
    io.github.lewin671.remoteagent.proto.ToolBackground.TOOL_BACKGROUND_STILL_RUNNING -> UiMessages.toolsBackgroundTurn()
    else -> UiMessages.toolsBackgroundReturned()
}

/** A background call's title: 后台命令 for a shell call, else the tool's name with （后台）. */
fun backgroundTitle(
    kind: io.github.lewin671.remoteagent.proto.ToolKind,
    itemType: String,
    agent: Agent,
): MessageText = when {
    kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_COMMAND || kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_UNSPECIFIED -> UiMessages.toolsAndroidBackgroundCommand()
    agentInfo(agent).toolLabelFromItemType && itemType.isNotEmpty() -> UiMessages.toolsAndroidBackgroundLabel(itemType)
    kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_FILE_CHANGE -> UiMessages.toolsAndroidBackgroundEdit()
    kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_WEB_SEARCH -> UiMessages.toolsAndroidBackgroundSearch()
    kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_MCP -> UiMessages.toolsAndroidBackgroundLabel("MCP")
    else -> UiMessages.toolsAndroidBackgroundGeneric()
}.messageText()

/**
 * A tool whose end is not known (architecture §13, 工具执行反馈): its call went to the background, or the
 * turn ended before its result came. It is not shown as a success; the terminal is where to check.
 */
class BackgroundTool(
    val tool: RunningTool,
    /** The call returned (it went to the background) rather than never coming back. */
    val returned: Boolean,
)

/** A message as the daemon accepted it, and how far it has got. */
data class Accepted(
    val text: String,
    val imageIds: List<String>,
    val origin: io.github.lewin671.remoteagent.proto.Origin?,
    val stage: io.github.lewin671.remoteagent.proto.InputStage,
    val files: List<io.github.lewin671.remoteagent.proto.SentFile> = emptyList(),
)

data class Compaction(val before: Int?, val after: Int? = null)

class Derived(
    /** Pending approvals, in the order they were asked. */
    val approvals: LinkedHashMap<String, ApprovalRequested> = LinkedHashMap(),
    val resolved: HashMap<String, Pair<ApprovalDecision, String>> = HashMap(),
    /** Who decided an approval, when the daemon says (v2). */
    val resolvedBy: HashMap<String, io.github.lewin671.remoteagent.proto.Origin> = HashMap(),
    val questions: LinkedHashMap<String, QuestionAsked> = LinkedHashMap(),
    val answered: HashMap<String, List<String>> = HashMap(),
    /** Latest state per request ID of messages sent with Send. */
    val inputs: LinkedHashMap<String, InputStatus> = LinkedHashMap(),
    /** Last input-state event per request ID, for ordering live snapshots. */
    val inputSeqs: HashMap<String, Long> = HashMap(),
    /** Full latest input state: mode and pause are needed for pending bubbles. */
    val inputDetails: LinkedHashMap<String, InputState> = LinkedHashMap(),
    /**
     * What the daemon accepted, per request ID: text, images and sender, shared by every client.
     */
    val accepted: HashMap<String, Accepted> = HashMap(),
    /** Why a message ended FAILED or NEEDS_TERMINAL, per request ID. */
    val failures: HashMap<String, String> = HashMap(),
    /**
     * Messages that never went out because the session ended: request ID to the seq of the event
     * that says so. The daemon records them as CANCELED with reason "exited". An exit alone does
     * not invent input outcomes.
     */
    val unsent: LinkedHashMap<String, Long> = LinkedHashMap(),
    var running: Boolean = false,
    var runningSeq: Long = 0,
    var scheduledClosed: Boolean = false,
    var idleClosed: Boolean = false,
    var usage: TokenUsage? = null,
    val compactions: HashMap<Long, Compaction> = HashMap(),
    var plan: PlanProposed? = null,
    /** Tools started and not yet completed in this turn, oldest first. */
    val tools: MutableList<RunningTool> = mutableListOf(),
    /** Tools of the last turn whose end is not known, until another turn starts. */
    val background: MutableList<BackgroundTool> = mutableListOf(),
    /** Start seq to the seq of the call that completed it. */
    val completedAt: HashMap<Long, Long> = HashMap(),
    /** Seqs of starts the turn ended without a result for. */
    val unfinished: MutableSet<Long> = HashSet(),
    /** Of those, the ones whose turn completed normally: no result came, nothing more is known. */
    val noResult: MutableSet<Long> = HashSet(),
    /** User messages typed in the terminal rather than sent from a client. */
    val typedLocally: MutableSet<Long> = HashSet(),
    /**
     * Mode or model changes made in the terminal, by the seq of the turn context that shows them.
     */
    val terminalChanges: HashMap<Long, List<TerminalChange>> = HashMap(),
    /** Inputs the model has not received yet, in acceptance order. */
    val pendingInputs: List<PendingInput> = emptyList(),
    /** Unambiguous daemon digest associations from ToolCall seq to ApprovalRequested ID. */
    val approvalCallIds: Map<Long, String> = emptyMap(),
)

sealed interface TerminalChange {
    data class ModeChanged(val mode: Mode?, val raw: String = "") : TerminalChange
    data class ModelChanged(val model: String, val effort: String) : TerminalChange
}

private fun toRunningTool(t: io.github.lewin671.remoteagent.proto.RunningTool) = RunningTool(
    title = t.title,
    callId = t.callId,
    since = t.sinceMs,
    kind = t.kind,
    itemType = t.itemType,
    seq = t.seq,
    input = t.input,
    inputDigest = t.inputDigest,
    awaitingApproval = t.awaitingApproval,
)

/** Converts a shared-core ProjectionState protobuf into the Kotlin :core view model. */
fun fromProjectionState(ps: ProjectionState): Derived {
    val approvals = LinkedHashMap<String, ApprovalRequested>(ps.approvalsCount)
    for (a in ps.approvalsList) approvals[a.approvalId] = a.request

    val resolved = HashMap<String, Pair<ApprovalDecision, String>>(ps.resolvedCount)
    val resolvedBy = HashMap<String, io.github.lewin671.remoteagent.proto.Origin>()
    for ((id, r) in ps.resolvedMap) {
        resolved[id] = r.decision to r.message
        if (r.hasOrigin()) resolvedBy[id] = r.origin
    }

    val questions = LinkedHashMap<String, QuestionAsked>(ps.questionsCount)
    for (q in ps.questionsList) questions[q.questionId] = q.request

    val answered = HashMap<String, List<String>>(ps.answeredCount)
    for ((id, a) in ps.answeredMap) answered[id] = a.answersList

    val inputs = LinkedHashMap<String, InputStatus>(ps.inputsCount)
    val inputSeqs = HashMap<String, Long>(ps.inputsCount)
    val inputDetails = LinkedHashMap<String, InputState>(ps.inputsCount)
    val accepted = HashMap<String, Accepted>(ps.inputsCount)
    val failures = HashMap<String, String>()
    for (ip in ps.inputsList) {
        inputs[ip.requestId] = ip.status
        inputSeqs[ip.requestId] = ip.lastSeq
        if (ip.hasDetail()) inputDetails[ip.requestId] = ip.detail
        if (ip.hasAccepted()) {
            val a = ip.accepted
            accepted[ip.requestId] = Accepted(
                a.text,
                a.imageIdsList,
                if (a.hasOrigin()) a.origin else null,
                a.stage,
                a.filesList,
            )
        }
        if (ip.hasFailureReason()) failures[ip.requestId] = ip.failureReason
    }

    val pendingInputs = ps.pendingInputsList.map { p ->
        PendingInput(
            p.requestId,
            p.text,
            p.mode,
            p.paused,
            p.held,
            if (p.hasOrigin()) p.origin else null,
            p.filesList,
        )
    }

    val unsent = LinkedHashMap<String, Long>(ps.unsentCount)
    for (u in ps.unsentList) unsent[u.requestId] = u.seq

    val compactions = HashMap<Long, Compaction>(ps.compactionsCount)
    for ((seq, c) in ps.compactionsMap) {
        compactions[seq] = Compaction(
            if (c.hasBeforePercent()) c.beforePercent else null,
            if (c.hasAfterPercent()) c.afterPercent else null,
        )
    }

    val tools = ps.toolsList.mapTo(ArrayList(ps.toolsCount)) { toRunningTool(it) }
    val background = ps.backgroundList.mapTo(ArrayList(ps.backgroundCount)) { b ->
        BackgroundTool(toRunningTool(b.tool), b.returned)
    }

    val terminalChanges = HashMap<Long, List<TerminalChange>>(ps.terminalChangesCount)
    for ((seq, list) in ps.terminalChangesMap) {
        terminalChanges[seq] = list.changesList.mapNotNull { ch ->
            when (ch.kindCase) {
                io.github.lewin671.remoteagent.proto.TerminalChange.KindCase.MODE -> {
                    val m = ch.mode
                    val raw = if (m.rawMode == "acceptEdits") "accept edits" else m.rawMode
                    val mode = modeOf(m.mode)
                    TerminalChange.ModeChanged(mode, raw.ifEmpty { mode?.name.orEmpty() })
                }
                io.github.lewin671.remoteagent.proto.TerminalChange.KindCase.MODEL ->
                    TerminalChange.ModelChanged(ch.model.model, ch.model.effort)
                else -> null
            }
        }
    }

    return Derived(
        approvals = approvals,
        resolved = resolved,
        resolvedBy = resolvedBy,
        questions = questions,
        answered = answered,
        inputs = inputs,
        inputSeqs = inputSeqs,
        inputDetails = inputDetails,
        accepted = accepted,
        failures = failures,
        unsent = unsent,
        running = ps.running,
        runningSeq = ps.runningSeq,
        scheduledClosed = ps.scheduledClosed,
        idleClosed = ps.idleClosed,
        usage = if (ps.hasUsage()) ps.usage else null,
        compactions = compactions,
        plan = if (ps.hasPlan()) ps.plan else null,
        tools = tools,
        background = background,
        completedAt = HashMap(ps.completedAtMap),
        unfinished = HashSet(ps.unfinishedSeqsList),
        noResult = HashSet(ps.noResultSeqsList),
        typedLocally = HashSet(ps.typedLocallySeqsList),
        terminalChanges = terminalChanges,
        pendingInputs = pendingInputs,
        approvalCallIds = HashMap(ps.approvalCallIdsMap),
    )
}

private fun decodeProjectionState(bytes: ByteArray?): Derived =
    if (bytes == null || bytes.isEmpty()) Derived()
    else fromProjectionState(ProjectionState.parseFrom(bytes))

private fun encodeEventBatch(events: List<Event>): ByteArray =
    if (events.isEmpty()) ByteArray(0)
    else EventBatch.newBuilder().addAllEvents(events).build().toByteArray()

/** Pending approvals and questions, input states and the running turn. */
fun derive(events: List<Event>, agent: Agent = DEFAULT_AGENT): Derived {
    val resolvedAgent = if (agent == Agent.AGENT_UNSPECIFIED) DEFAULT_AGENT else agent
    return decodeProjectionState(Corebind.reduce(resolvedAgent.number, encodeEventBatch(events)))
}

/** Reuses reducer state in the shared Go core for an unchanged prefix; published results never mutate. */
class EventProjection {
    private var previous: List<Event> = emptyList()
    private var agent: Agent? = null
    private var core: Projection? = null
    private var result = Derived()
    private var liveResult: Derived? = null
    private var lastLive: LiveSnapshot? = null
    private var liveValid = false

    fun update(events: List<Event>, agent: Agent = DEFAULT_AGENT): Derived {
        val resolvedAgent = if (agent == Agent.AGENT_UNSPECIFIED) DEFAULT_AGENT else agent
        if (this.agent == resolvedAgent && events === previous) return result
        val handle = core ?: Corebind.newProjection(resolvedAgent.number).also { core = it }
        val append = this.agent == resolvedAgent && unchangedPrefix(events, previous)
        if (append) {
            if (events.size == previous.size) {
                previous = events
                return result
            }
            val delta = events.subList(previous.size, events.size)
            result = decodeProjectionState(handle.append(encodeEventBatch(delta)))
        } else {
            if (this.agent != null) handle.reset(resolvedAgent.number)
            result = if (events.isEmpty()) {
                decodeProjectionState(handle.snapshot())
            } else {
                decodeProjectionState(handle.append(encodeEventBatch(events)))
            }
        }
        previous = events
        this.agent = resolvedAgent
        liveValid = false
        return result
    }

    /**
     * Overlays a fresh authenticated owner [live] snapshot onto the reducer's current historical
     * state, clearing pending actions when [live] is null.
     */
    fun currentActions(live: LiveSnapshot?): Derived {
        if (liveValid && live === lastLive) return liveResult!!
        val handle = core ?: Corebind.newProjection((agent ?: DEFAULT_AGENT).number).also { core = it }
        return decodeProjectionState(handle.currentActions(live?.toByteArray() ?: ByteArray(0))).also {
            lastLive = live
            liveResult = it
            liveValid = true
        }
    }
}

/** Whether a running tool is waiting on a pending approval request, as computed by the shared core. */
fun awaitingApproval(t: RunningTool, d: Derived): Boolean =
    d.tools.firstOrNull { it.seq == t.seq }?.awaitingApproval ?: t.awaitingApproval

/**
 * How long a tool has run, in ms, or null when its start time is unknown. While disconnected
 * ([frozenAt] > 0) the reading stops where the connection dropped, so it does not suggest the tool
 * is still running.
 */
fun toolElapsed(t: RunningTool, now: Long, frozenAt: Long = 0): Long? {
    if (t.since <= 0) return null
    val end = if (frozenAt > 0) minOf(frozenAt, now) else now
    return (end - t.since).coerceAtLeast(0)
}

/** 2:12, 1:02:03: a running timer. */
fun clock(ms: Long): String {
    val s = ms / 1000
    return if (s >= 3600) "%d:%02d:%02d".format(s / 3600, s % 3600 / 60, s % 60)
    else "%d:%02d".format(s / 60, s % 60)
}

/** A tool's short name: Claude Code's own tool name (Bash, Read), else what kind of tool it is. */
fun toolLabel(
    kind: io.github.lewin671.remoteagent.proto.ToolKind,
    itemType: String,
    agent: Agent,
): MessageText =
    when {
        agentInfo(agent).toolLabelFromItemType && itemType.isNotEmpty() -> itemType.messageText()
        kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_COMMAND -> UiMessages.toolsCommand().messageText()
        kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_FILE_CHANGE -> UiMessages.toolsAndroidEdit().messageText()
        kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_MCP -> "MCP".messageText()
        kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_WEB_SEARCH -> UiMessages.toolsAndroidSearch().messageText()
        else -> UiMessages.toolsTool().messageText()
    }

fun eventTime(e: Event): Long =
    if (e.hasTime()) e.time.seconds * 1000 + e.time.nanos / 1_000_000 else 0

fun decisionText(d: ApprovalDecision): MessageText =
    when (d) {
        ApprovalDecision.APPROVAL_DECISION_ALLOW -> UiMessages.approvalAllowed().messageText()
        ApprovalDecision.APPROVAL_DECISION_ALLOW_ALWAYS -> UiMessages.approvalAllowed().messageText()
        ApprovalDecision.APPROVAL_DECISION_DENY -> UiMessages.approvalDenied().messageText()
        ApprovalDecision.APPROVAL_DECISION_TIMEOUT -> UiMessages.approvalTimedOut().messageText()
        ApprovalDecision.APPROVAL_DECISION_CANCELED -> UiMessages.queueCanceled().messageText()
        ApprovalDecision.APPROVAL_DECISION_TERMINAL -> UiMessages.approvalInTerminal().messageText()
        else -> "".messageText()
    }

/** Splits a "(Recommended)" marker off an option label. */
fun optionLabel(label: String): Pair<String, Boolean> {
    val m = Regex("^(.*?)\\s*\\((Recommended|推荐)\\)\\s*$", RegexOption.IGNORE_CASE).find(label)
    return if (m != null) m.groupValues[1] to true else label to false
}

private val pathToken = Regex("""(^|[\s=:(\[{,])(?:(["'])(/[^"'\n]*)\2|(/[^\s"'`]*))""")
private val homeDir = Regex("^/(?:Users|home)/[^/:]+")
private val lineSuffix = Regex("^:\\d+(?::\\d+)*$")

/** macOS reports /tmp, /var and /etc under /private: the same place. */
private fun samePlace(p: String) = p.replaceFirst(Regex("^/private(?=/(?:tmp|var|etc)(?:/|$))"), "")

/**
 * Absolute paths in a tool title, shortened for display, exactly as shortenPaths in
 * web/src/model.ts (its cases are copied into ShortPathsTest). Paths are whole tokens: a quoted one
 * is the whole quoted text, spaces and all; an unquoted one runs to whitespace or a quote; a token
 * starts with / after the start, a space, =, :, (, [, { or a comma. Under the session folder a path
 * becomes relative to it, the folder itself "."; else under the home folder ~/…, and without a
 * known home /Users/<name> and /home/<name> count as one. A folder only matches when what follows
 * it is /, nothing or a :line suffix; anything else, or anything unsure, stays as written.
 */
fun shortenPaths(text: String, cwd: String, home: String = ""): String =
    pathToken.replace(text) { m ->
        val before = m.groupValues[1]
        val quoted = m.groups[3]?.value
        val path = quoted ?: m.groups[4]?.value.orEmpty()
        val short = shortenPath(path, cwd, home)
        when {
            short == path -> m.value
            quoted != null -> before + m.groupValues[2] + short + m.groupValues[2]
            else -> before + short
        }
    }

/**
 * One whole path, spaces and all (a file edit's title), shortened by the rule of [shortenPaths];
 * unchanged when unsure. Same as shortenPath in web/src/model.ts.
 */
fun shortenPath(path: String, cwd: String, home: String = ""): String {
    val base = samePlace(cwd).trimEnd('/')
    val h = samePlace(home).trimEnd('/')
    // What follows a folder in the path: a subpath, nothing, or :line[:col].
    fun under(p: String, dir: String): String? {
        if (dir.isEmpty() || !p.startsWith(dir)) return null
        val rest = p.substring(dir.length)
        return if (rest.isEmpty() || rest[0] == '/' || lineSuffix.matches(rest)) rest else null
    }
    val p = samePlace(path)
    under(p, base)?.let { r ->
        return if (r.isEmpty() || r == "/") "." else if (r[0] == '/') r.substring(1) else ".$r"
    }
    val hm = h.ifEmpty { homeDir.find(p)?.value.orEmpty() }
    return under(p, hm)?.let { "~$it" } ?: path
}

fun shortPath(p: String): String =
    p.replace(Regex("^/Users/[^/]+"), "~").replace(Regex("^/home/[^/]+"), "~")

/**
 * A folder's path as the picker writes it, piece by piece: ~ / Code / foo. A path outside the home folder keeps
 * its leading slash on the first piece (/srv / www), so it does not read as one below the home folder; the root is "/".
 */
fun pathCrumbs(path: String): List<String> {
    val parts = shortPath(path).split("/").filter { it.isNotEmpty() }
    if (parts.firstOrNull() == "~") return parts
    return if (parts.isEmpty()) listOf("/") else listOf("/" + parts.first()) + parts.drop(1)
}

/** Minutes left until a deadline, for "剩 N 分钟". */
fun minutesLeft(deadlineMs: Long, now: Long = System.currentTimeMillis()): Long =
    maxOf(0, (deadlineMs - now + 59_999) / 60_000)

// --- the message queue ---

/** Why a message was not submitted (InputState.reason), in words. */
fun failureText(reason: String, agent: Agent, copy: MessageResolver): UiMessage =
    when (reason) {
        "image_not_recognized" -> UiMessages.composerImagesUnrecognized(agentName(agent, copy = copy))
        "input_changed" -> UiMessages.composerInputUnconfirmed()
        "timeout" -> UiMessages.composerReadyTimeout()
        "restarted" -> UiMessages.composerComputerRestarted()
        "write_failed" -> UiMessages.composerWriteFailed()
        "exited" -> UiMessages.sessionEnded()
        else -> UiMessages.composerNotWritten()
    }

/** Messages the daemon accepted and has not written yet, in the order it accepted them. */
fun queued(d: Derived): List<String> = d.pendingInputs.map { it.id }

data class PendingInput(
    val id: String,
    val text: String,
    val mode: SendMode,
    val paused: Boolean,
    val held: Boolean,
    val origin: io.github.lewin671.remoteagent.proto.Origin?,
    val files: List<io.github.lewin671.remoteagent.proto.SentFile> = emptyList(),
)

/** Only inputs the model has not received; event order is acceptance order. */
fun pendingInputs(d: Derived): List<PendingInput> = d.pendingInputs

/** CLI's native second approval option, translated only when its form is known. */
fun approvalAlwaysText(message: String): MessageText {
    Regex("(?:Yes, and )?don't ask again for commands that start with [`]?(.+?)[`]?$")
        .matchEntire(message)
        ?.let {
            return UiMessages.approvalAndroidPrefix(it.groupValues[1].trim('`')).messageText()
        }
    Regex("(?:Yes, and )?always allow access to (.+?) from this project$")
        .matchEntire(message)
        ?.let {
            return UiMessages.approvalAlwaysDirectory(it.groupValues[1]).messageText()
        }
    if (message.contains("accept edits", ignoreCase = true)) return UiMessages.approvalAutoEdits().messageText()
    return message.messageText()
}

/**
 * Why the first queued message waits: a short form for the line above the message box and a longer
 * one for the queue page. The daemon's own reason (Session.input_wait) comes first; else the
 * session's state says it.
 */
data class QueueWhy(val short: UiMessage, val long: UiMessage)

fun queueWhy(s: SessionView, status: Status, copy: MessageResolver): QueueWhy =
    when (s.info?.inputWait) {
        "draft" -> QueueWhy(UiMessages.queueAndroidDraft(), UiMessages.queueTerminalDraft())
        "popup" -> QueueWhy(UiMessages.queueWhenReady(), UiMessages.queueAndroidPopupNote())
        "unrecognized" -> QueueWhy(UiMessages.queuePaused(), UiMessages.queueUnrecognized())
        else ->
            when (status) {
                Status.QUESTION -> QueueWhy(UiMessages.queueAndroidAfterAnswer(), UiMessages.queueAndroidAnswerNote())
                Status.APPROVAL -> QueueWhy(UiMessages.queueAndroidAfterApproval(), UiMessages.queueAndroidApprovalNote())
                Status.PLAN -> QueueWhy(UiMessages.queueAndroidAfterPlan(), UiMessages.queueAndroidPlanNote())
                Status.POPUP -> QueueWhy(UiMessages.queueWhenReady(), UiMessages.queueAndroidPopupNote())
                Status.RUNNING -> QueueWhy(UiMessages.queueAfterCurrentTurn(), UiMessages.queueAndroidTurnNote())
                Status.STARTING -> QueueWhy(UiMessages.queueWhenReady(), UiMessages.queueAndroidAgentNote(agentName(s.agent(), copy = copy)))
                else -> QueueWhy(UiMessages.queueAndroidSendingSoon(), UiMessages.queueAndroidInputNote())
            }
    }

/**
 * Where a message the user tried to cancel ended up, once it left the queue: the cancel worked,
 * someone else canceled it, or it was written first.
 */
enum class CancelOutcome {
    CANCELED,
    CANCELED_ELSEWHERE,
    WRITTEN,
    UNKNOWN,
}

fun cancelOutcome(status: InputStatus?, canceledHere: Boolean): CancelOutcome =
    when (status) {
        InputStatus.INPUT_STATUS_CANCELED ->
            if (canceledHere) CancelOutcome.CANCELED else CancelOutcome.CANCELED_ELSEWHERE
        InputStatus.INPUT_STATUS_SUBMITTED,
        InputStatus.INPUT_STATUS_UNCONFIRMED -> CancelOutcome.WRITTEN
        else -> CancelOutcome.UNKNOWN
    }

// --- devices and where actions came from ---

/** The account name, or a readable platform name disambiguated across all clients. */
fun clientName(d: DeviceView?, id: String, clients: Collection<DeviceView> = emptyList(), copy: MessageResolver): String =
    d?.meta?.name.orEmpty().trim().ifEmpty { defaultClientName(d, id, clients, copy = copy) }

private sealed interface ClientBase {
    data class Raw(val value: String) : ClientBase
    data object Browser : ClientBase
    data object Client : ClientBase

    fun display(copy: MessageResolver): String = when (this) {
        is Raw -> value
        Browser -> copy(UiMessages.securityBrowser())
        Client -> copy(UiMessages.securityClient())
    }
}

private fun clientBase(d: DeviceView?): ClientBase {
    val meta = d?.meta
    val description = meta?.description.orEmpty().trim()
    val model = description.substringAfter(" · ", "").trim()
    return when (meta?.platform) {
        "web" -> description.replace(" on ", " · ").takeIf { it.isNotEmpty() }?.let(ClientBase::Raw) ?: ClientBase.Browser
        "android" -> ClientBase.Raw("Android" + if (model.isNotEmpty()) " · $model" else "")
        "ios" -> ClientBase.Raw(model.ifEmpty { "iOS" })
        else -> ClientBase.Client
    }
}

/** Compare raw metadata / fallback identities, including clients with a user-supplied name. */
fun defaultClientName(d: DeviceView?, id: String, clients: Collection<DeviceView> = emptyList(), copy: MessageResolver): String {
    val base = clientBase(d)
    val duplicate = clients.any { it.kind == io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT && it.id != id && clientBase(it) == base }
    return base.display(copy) + if (duplicate) " · ${shortId(id)}" else ""
}

/** The last four hex digits of a device ID, as shown next to a name. */
private fun shortId(id: String): String = id.takeLast(4)

/**
 * Who an event says caused it (Event.origin), for display, or null when the event does not say.
 * Only a signed client is named, and only by the account's device list; anything unsure is "另一端",
 * never a guess.
 */
fun originLabel(
    o: io.github.lewin671.remoteagent.proto.Origin?,
    state: RemoteState,
    me: String,
    machine: String,
    copy: MessageResolver): String? {
    if (o == null) return null
    return when (o.kind) {
        io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_CLIENT ->
            when {
                o.deviceId == me -> copy(UiMessages.sessionOriginSelf())
                // A loaded account directory includes this client.
                o.deviceId.isEmpty() || state.devices.isEmpty() -> copy(UiMessages.sessionOriginOther())
                else ->
                    state.devices[o.deviceId]?.let { clientName(it, o.deviceId, state.devices.values, copy = copy) }
                        ?: copy(UiMessages.sessionOriginRemoved())
            }
        io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_LOCAL -> copy(UiMessages.sessionOriginTerminal((machine).toString()))
        io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_SYSTEM -> null
        else -> null
    }
}

fun Event.originOrNull(): io.github.lewin671.remoteagent.proto.Origin? =
    if (hasOrigin()) origin else null

/** What the machine says it offers; null until its metadata is read. */
fun capabilities(m: MachineView?): io.github.lewin671.remoteagent.proto.MachineCapabilities? =
    m?.pathState?.capabilities

/**
 * Why no command goes to [m]: it speaks another protocol version than this app (PROTOCOL_VERSION),
 * said by which side to update; null when they match or the machine's metadata is not read yet.
 */
fun protocolMismatch(m: MachineView?): MessageText? {
    val theirs = m?.pathState?.protocolVersion ?: return null
    return when {
        theirs < PROTOCOL_VERSION -> localized { copy -> copy(UiMessages.errorsRemoteAgentOnIsOutOfDateUpdateIt(listMachineName(m).resolve(copy))) }
        theirs > PROTOCOL_VERSION -> UiMessages.commonAndroidThisAppVersionIsOldUpdateAndRetry().messageText()
        else -> null
    }
}

/** A model's context window as the pickers write it: 1M 上下文, 200k 上下文; null when unknown. */
fun contextText(tokens: Long, copy: MessageResolver): String? =
    when {
        tokens <= 0 -> null
        tokens >= 1_000_000 -> copy(UiMessages.modelsContextSize("${(tokens / 100_000 / 10.0).toString().removeSuffix(".0")}M"))
        else -> copy(UiMessages.modelsContextSize("${(tokens + 500) / 1000}k"))
    }

/** The standard context window, which a Claude Code session can be held to. */
const val STANDARD_WINDOW = 200_000L

/** Unambiguous daemon digest associations over the complete observed history, computed by the shared core. */
fun approvalCallIDs(events: List<Event>, d: Derived): Map<Long, String> = d.approvalCallIds

/**
 * Current pending actions come only from an authenticated owner reply, never from replayable
 * historical events. Other history remains read-only data; a pending plan's text is read from the
 * history event the reply names.
 */
fun currentActions(
    events: List<Event>,
    live: LiveSnapshot?,
    agent: Agent = DEFAULT_AGENT,
): Derived = EventProjection().run {
    update(events, agent)
    currentActions(live)
}

fun inputTimingLabel(agent: Agent, mode: SendMode, held: Boolean, starting: String?): UiMessage = when {
    !agentInfo(agent).steersDuringTurn -> starting?.let { UiMessages.queueAgentReady(it) } ?: UiMessages.queueAfterTurn()
    held -> UiMessages.queueInsert()
    mode == SendMode.SEND_MODE_QUEUE -> UiMessages.queueAfterTurn()
    starting != null -> UiMessages.queueAgentReady(starting)
    else -> UiMessages.queueInsert()
}
