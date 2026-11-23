// View models derived from the decrypted state, as in web/src/model.ts:
// projects, statuses, and what a session's events say is pending.
package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.SessionStatus
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

/** 2:12, 1:02:03: a running timer. */
fun clock(ms: Long): String {
    val s = ms / 1000
    return if (s >= 3600) "%d:%02d:%02d".format(s / 3600, s % 3600 / 60, s % 60)
    else "%d:%02d".format(s / 60, s % 60)
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

/** What the machine says it offers; null until its metadata is read. */
fun capabilities(m: MachineView?): io.github.lewin671.remoteagent.proto.MachineCapabilities? =
    m?.pathState?.capabilities

/**
 * Why no command goes to [m]: it speaks another protocol version than this app (PEER_PROTOCOL_VERSION),
 * said by which side to update; null when they match or the machine's metadata is not read yet.
 */
fun protocolMismatch(m: MachineView?): MessageText? {
    val theirs = m?.pathState?.protocolVersion ?: return null
    return when {
        theirs < PEER_PROTOCOL_VERSION -> localized { copy -> copy(UiMessages.errorsRemoteAgentOnIsOutOfDateUpdateIt(listMachineName(m).resolve(copy))) }
        theirs > PEER_PROTOCOL_VERSION -> UiMessages.commonAndroidThisAppVersionIsOldUpdateAndRetry().messageText()
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
