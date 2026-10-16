// View models derived from the decrypted state, as in web/src/model.ts:
// projects, statuses, and what a session's events say is pending.
package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalDecision
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.InputState
import io.github.lewin671.remoteagent.proto.SendMode
import io.github.lewin671.remoteagent.proto.PlanProposed
import io.github.lewin671.remoteagent.proto.QuestionAsked
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.TokenUsage
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.time.ZonedDateTime

// --- CLIs ---

/** The CLI a session runs; sessions from before Claude Code support are Codex. */
fun effectiveAgent(a: Agent): Agent = if (a == Agent.AGENT_UNSPECIFIED || a == Agent.UNRECOGNIZED) Agent.AGENT_CODEX else a

fun agentName(a: Agent): String = when (effectiveAgent(a)) {
    Agent.AGENT_CLAUDE -> "Claude Code"
    else -> "Codex"
}

fun SessionView.agent(): Agent = effectiveAgent(info?.agent ?: Agent.AGENT_UNSPECIFIED)

/** The CLIs a machine can start; Codex while it lists none. */
fun machineAgents(m: MachineView?): List<Agent> {
    val listed = m?.meta?.agentsList?.map { effectiveAgent(it.agent) }?.distinct().orEmpty()
    return listed.ifEmpty { listOf(Agent.AGENT_CODEX) }
}

// --- projects ---

data class Project(
    val key: String,
    val name: String,
    /** The project's sessions held here, recent activity first, used for new-session defaults. UI lists sort separately. */
    val sessions: List<SessionView>,
    val updatedMs: Long,
    val hasLive: Boolean,
    val latestCreatedMs: Long,
    val paths: List<String>,
    val machines: List<String>,
    /** The machines it has sessions on: those of the sessions held here, recent first, then the others whose summary names it. */
    val machineIds: List<String> = emptyList(),
    /** How many of its sessions have ended, on all machines: by their summaries, and no fewer than are held here. */
    val ended: Int = 0,
)

/** The project a session belongs to: its working directory's absolute path, the same on every machine. */
fun projectKey(s: SessionView): String = s.info?.cwd.orEmpty()

/** A project's name: the last folder of its path. */
fun projectName(key: String): String = key.split("/").lastOrNull { it.isNotEmpty() } ?: "未知项目"

/** What the machines say of a project (MachineMeta.projects), added up. */
private class ProjectSums {
    val endedBy = HashMap<String, Int>()
    var latestCreatedMs = 0L
    var latestEndedMs = 0L
    val machineIds = ArrayList<String>()
}

/**
 * The account's projects (architecture §8 「项目从哪来」): the machines'
 * project summaries together with the sessions held here. Sessions come a
 * page at a time, so a project whose sessions all ended long ago is known
 * from the summaries alone. Whether one has a session that has not ended is
 * read off the sessions, which are all held; the latest creation is the
 * later of the two; its machines are those of both. A project added with
 * 新项目… is known from its settings alone until its first session: it is
 * listed while the machine chosen for it is one of the account's, as created
 * when it was added.
 */
fun projects(state: RemoteState): List<Project> {
    val byKey = LinkedHashMap<String, MutableList<SessionView>>()
    for (s in state.sessions.values) byKey.getOrPut(projectKey(s)) { mutableListOf() } += s
    val sums = LinkedHashMap<String, ProjectSums>()
    // The machine whose newest session there is the latest comes first.
    val summaries = state.machines.values.flatMap { m -> m.meta?.projectsList.orEmpty().map { m.id to it } }
        .sortedWith(compareByDescending<Pair<String, io.github.lewin671.remoteagent.proto.ProjectSummary>> { it.second.latestCreatedMs }.thenBy { it.first })
    for ((machineId, p) in summaries) {
        val sum = sums.getOrPut(p.path) { ProjectSums() }
        sum.endedBy[machineId] = p.ended
        sum.latestCreatedMs = maxOf(sum.latestCreatedMs, p.latestCreatedMs)
        sum.latestEndedMs = maxOf(sum.latestEndedMs, p.latestEndedMs)
        if (machineId !in sum.machineIds) sum.machineIds += machineId
    }
    val added = state.prefs?.prefs?.settingsList.orEmpty()
        .filter { it.path.isNotEmpty() && it.path !in byKey && it.path !in sums && it.machineId in state.machines }
        .associateBy { it.path }
    return (byKey.keys + sums.keys + added.keys).distinct().map { key ->
        val sorted = byKey[key].orEmpty().sortedWith(compareByDescending<SessionView> { it.updatedMs }.thenBy { it.id })
        val sum = sums[key]
        val own = added[key]
        val machineIds = (sorted.map { it.machineId } + sum?.machineIds.orEmpty() + listOfNotNull(own?.machineId)).distinct()
        Project(
            key = key,
            name = projectName(key),
            sessions = sorted,
            updatedMs = maxOf(sorted.maxOfOrNull { it.updatedMs } ?: 0, sum?.latestCreatedMs ?: 0, sum?.latestEndedMs ?: 0, own?.updatedMs ?: 0),
            hasLive = sorted.any { !exited(it) },
            latestCreatedMs = maxOf(sorted.maxOfOrNull { createdMs(it) } ?: 0, sum?.latestCreatedMs ?: 0, own?.updatedMs ?: 0),
            paths = sorted.mapNotNull { it.info?.cwd?.takeIf { c -> c.isNotEmpty() } }.distinct().ifEmpty { listOf(key).filter { it.isNotEmpty() } },
            machines = machineIds.map { machineName(state.machines[it], it) }.distinct(),
            machineIds = machineIds,
            // Counted machine by machine: each machine's count, or what is held of its
            // sessions where that is more (its metadata may not have arrived, or be behind).
            ended = machineIds.sumOf { m -> maxOf(sum?.endedBy?.get(m) ?: 0, sorted.count { it.machineId == m && exited(it) }) },
        )
    }.sortedWith(compareByDescending<Project> { it.hasLive }.thenByDescending { it.latestCreatedMs }.thenBy { it.key })
}

/** One project of [projects], or null when no summary, session held here or stored settings name it. */
fun project(state: RemoteState, key: String): Project? = projects(state).firstOrNull { it.key == key }

/** The projects a machine has sessions in: by its summary and by the sessions held here, in that order. */
fun machineProjects(state: RemoteState, machineId: String): List<String> =
    (state.machines[machineId]?.meta?.projectsList.orEmpty().map { it.path } +
        state.sessions.values.filter { it.machineId == machineId }.mapNotNull { it.info?.cwd }).distinct()

/**
 * The project list as the account arranges it (architecture §8): pinned
 * projects first in the order they were pinned, the others with live
 * projects first, then by latest session creation; hidden ones apart. Hiding only takes a project off these lists:
 * its sessions, pending items and notifications are not filtered here.
 */
data class ProjectGroups(val pinned: List<Project>, val others: List<Project>, val hidden: List<Project>)

fun projectGroups(state: RemoteState): ProjectGroups {
    val all = projects(state)
    val prefs = state.prefs ?: PrefsObject.EMPTY
    val hidden = prefs.hidden
    val byKey = all.associateBy { it.key }
    val pinned = prefs.pinned.filter { it !in hidden }.mapNotNull { byKey[it] }
    val pinnedKeys = pinned.map { it.key }.toSet()
    return ProjectGroups(
        pinned = pinned,
        others = all.filter { it.key !in pinnedKeys && it.key !in hidden },
        hidden = all.filter { it.key in hidden },
    )
}

fun isHidden(state: RemoteState, key: String): Boolean = state.prefs?.hidden?.contains(key) == true

fun isPinned(state: RemoteState, key: String): Boolean = state.prefs?.let { key in it.pinned && key !in it.hidden } == true

fun machineName(m: MachineView?, id: String = ""): String =
    m?.meta?.hostname?.removeSuffix(".local")?.takeIf { it.isNotEmpty() } ?: m?.id?.take(8) ?: id.take(8).ifEmpty { "未知电脑" }

/** Avatar color index 0..3, stable per project. */
fun avatarIndex(key: String): Int {
    var h = 0L
    for (c in key) h = (h * 31 + c.code) and 0xffffffffL
    return (h % 4).toInt()
}

// A session that has not ended or is not waiting has no such timestamp and
// falls back to creation time; incomplete metadata uses zero, never the
// changing relay timestamp.
fun createdMs(s: SessionView): Long = s.info?.takeIf { it.hasCreatedAt() }?.createdAt?.let { it.seconds * 1000 + it.nanos / 1_000_000 } ?: 0
fun endedMs(s: SessionView): Long = s.info?.takeIf { it.hasEndedAt() }?.endedAt?.let { it.seconds * 1000 + it.nanos / 1_000_000 } ?: createdMs(s)
fun attentionMs(s: SessionView): Long = s.info?.takeIf { it.hasAttentionSince() }?.attentionSince?.let { it.seconds * 1000 + it.nanos / 1_000_000 } ?: createdMs(s)
val liveOrder: Comparator<SessionView> = compareByDescending<SessionView> { createdMs(it) }.thenBy { it.id }
val endedOrder: Comparator<SessionView> = compareByDescending<SessionView> { endedMs(it) }.thenBy { it.id }
val sessionOrder: Comparator<SessionView> = Comparator { a, b ->
    val group = exited(a).compareTo(exited(b))
    if (group != 0) group else if (exited(a)) endedOrder.compare(a, b) else liveOrder.compare(a, b)
}
val attentionOrder: Comparator<SessionView> = compareBy<SessionView> { statusOf(it, null).ordinal }.thenBy { attentionMs(it) }.thenBy { it.id }

// --- sessions ---

enum class Tone { RUN, WAIT, IDLE, OFF }

/** One state per session, the highest in the priority list of the design (client-v1, 状态优先级). */
enum class Status(val text: String, val tone: Tone) {
    LOADING("读取中", Tone.OFF),
    EXITED("已结束", Tone.OFF),
    OFFLINE("电脑离线", Tone.OFF),
    /** Only shown ([shownStatus]): the server is out of reach, so what the session is doing is not known. */
    UNLINKED("未连接", Tone.OFF),
    APPROVAL("等待审批", Tone.WAIT),
    QUESTION("等你回答", Tone.WAIT),
    PLAN("计划待批准", Tone.WAIT),
    POPUP("终端里有弹窗", Tone.WAIT),
    RUNNING("运行中", Tone.RUN),
    STARTING("启动中", Tone.RUN),
    IDLE("空闲", Tone.IDLE),
}

fun exited(s: SessionView): Boolean = s.info?.status == SessionStatus.SESSION_STATUS_EXITED

/**
 * The state to show for a session. Out of reach of the server, the last
 * one heard is not shown as if it still held (client-v30 「断线」); that a
 * session ended stays true.
 */
fun shownStatus(state: RemoteState, s: SessionView): Status {
    val st = statusOf(s, state.machines[s.machineId])
    return if (state.unlinked && st != Status.EXITED && st != Status.LOADING) Status.UNLINKED else st
}

fun statusOf(s: SessionView, machine: MachineView?): Status {
    val i = s.info ?: return Status.LOADING
    if (i.status == SessionStatus.SESSION_STATUS_EXITED) return Status.EXITED
    if (machine != null && !machine.online) return Status.OFFLINE
    if (i.pendingApprovals > 0) return Status.APPROVAL
    if (i.pendingQuestions > 0) return Status.QUESTION
    if (i.planOpen) return Status.PLAN
    if (i.status == SessionStatus.SESSION_STATUS_BLOCKED) return Status.POPUP
    if (i.status == SessionStatus.SESSION_STATUS_RUNNING) return Status.RUNNING
    if (i.status == SessionStatus.SESSION_STATUS_STARTING) return Status.STARTING
    return Status.IDLE
}

/**
 * A hint for a popup in the terminal. Claude Code's trust dialog has its
 * cursor on "No, exit", so Enter alone ends the session
 * (claude-code-notes.md §1); Codex's needs none. Same text as the web.
 */
fun popupHint(agent: Agent, detail: String): String =
    if (effectiveAgent(agent) == Agent.AGENT_CLAUDE && detail == "trust folder") "默认选中的是 No, exit。按 ↓ 选中 Yes, I trust this folder 再按 ⏎" else ""

/** Pending work stays pending while its computer is out of reach. */
fun needsAttention(s: SessionView): Boolean = statusOf(s, null).tone == Tone.WAIT

/** The session's title; without one, "<folder> · <start time>". */
fun titleOf(s: SessionView, zone: ZoneId = ZoneId.systemDefault()): String {
    val t = plain(s.info?.title.orEmpty()).trim()
    if (t.isNotEmpty()) return t
    val i = s.info ?: return "新会话"
    val created = if (i.hasCreatedAt()) i.createdAt.seconds * 1000 else 0
    return if (created > 0) "${projectName(i.cwd)} · ${hhmm(created, zone)}" else projectName(i.cwd)
}

/** The most a session's title holds, counted in code points as the daemon counts them. */
const val TITLE_MAX = 60

/**
 * What the rename field starts with: the session's title, or nothing while
 * it only has the "<folder> · <start time>" placeholder. Same as the web.
 */
fun renameInitial(s: SessionView): String = titleInput(s.info?.title.orEmpty().trim())

/** What the rename field takes of [text]: one line, and nothing past [TITLE_MAX]. */
fun titleInput(text: String): String {
    val line = text.replace(Regex("[\\r\\n]+"), " ")
    return if (line.codePointCount(0, line.length) <= TITLE_MAX) line else line.substring(0, line.offsetByCodePoints(0, TITLE_MAX))
}

/**
 * The title to send for what was typed, trimmed as the daemon trims it;
 * null when it is the title the field started with and there is nothing to
 * save. Empty asks for the automatic title back.
 */
fun renameTitle(typed: String, initial: String): String? = typed.trim().takeIf { it != initial }

/** The session was forked from another one (Session.forked_from). */
fun forked(s: SessionView): Boolean = s.info?.forkedFrom.orEmpty().isNotEmpty()

/**
 * Whether a message was ever sent in the session: its title comes from the
 * first one, and the history loaded so far may show one.
 */
fun sentMessage(s: SessionView, events: List<Event>): Boolean = s.info?.title.orEmpty().isNotEmpty() || events.any { it.hasUserMessage() }

/**
 * Why the session cannot be forked now, as the ⋯ menu writes it under
 * 分叉会话; null when it can. A fork takes an idle or ended session in which
 * a message was sent. [running]: a turn is going on by the events, which
 * can be ahead of the session's status. The menu only offers a fork while
 * the computer is reachable and the session is read, so OFFLINE and LOADING
 * do not come here; they read as not up yet.
 */
fun forkBlocked(status: Status, running: Boolean, sent: Boolean): String? = when (status) {
    Status.RUNNING, Status.APPROVAL, Status.QUESTION -> "这一轮结束后可用"
    Status.PLAN, Status.POPUP -> "处理完等待事项后可用"
    Status.STARTING, Status.LOADING, Status.OFFLINE -> "启动完成后可用"
    Status.UNLINKED -> "连上服务器后可用"
    Status.IDLE -> if (running) "这一轮结束后可用" else if (!sent) "发过消息后可用" else null
    Status.EXITED -> if (!sent) "发过消息后可用" else null
}

/** The session a fork made, or null when the answer names none. */
fun forkedSession(r: io.github.lewin671.remoteagent.proto.CommandResult?): String? =
    r?.takeIf { it.hasForkSession() && it.forkSession.hasSession() }?.forkSession?.session?.id?.takeIf { it.isNotEmpty() }

/** How a session that is not held here is being looked up by its ID. */
sealed interface SessionLookup {
    /** Asked for, with no answer that settles it yet. */
    data object Reading : SessionLookup
    /** The server has no such session, and kept saying so. */
    data object Missing : SessionLookup
    /** The server could not be asked; it is asked again after a while. */
    data class Failed(val reason: String) : SessionLookup
}

/** A session the server does not have is asked for this many times, [SESSION_ABSENT_WAIT_MS] apart, before it counts as missing. */
const val SESSION_ABSENT_TRIES = 5
const val SESSION_ABSENT_WAIT_MS = 2_500L

/**
 * Looks a session up by its ID until it is held ([fetch] returns true) or
 * known to be missing (architecture §8 「按 ID 取」). A session just started
 * is acknowledged by its daemon before the daemon has uploaded it, so the
 * server not having it only counts once it said so for ten seconds. The
 * server not answering is no answer: it is asked again, after three seconds
 * and then twice as long each time up to half a minute, and [report] says
 * why meanwhile.
 */
suspend fun lookUpSession(fetch: suspend () -> Boolean, wait: suspend (Long) -> Unit = { kotlinx.coroutines.delay(it) }, report: (SessionLookup) -> Unit) {
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

/** What a page about a session says instead of its content while the session is not held; null while it is only being read. */
fun lookupNote(l: SessionLookup?): String? = when (l) {
    SessionLookup.Missing -> "找不到这个会话"
    is SessionLookup.Failed -> "没能读取这个会话：${l.reason}"
    else -> null
}

/** Text for one-line previews: Markdown marks and the CLIs' image placeholders removed. */
fun plain(text: String): String = text
    .replace(Regex("\\[Image #\\d+]\\s*"), "")
    .replace(Regex("```[a-zA-Z0-9]*"), "")
    .replace(Regex("`([^`]*)`"), "$1")
    .replace(Regex("\\*\\*([^*]+)\\*\\*"), "$1")
    .replace(Regex("\\[([^\\]]+)\\]\\([^)]*\\)"), "$1")
    .replace(Regex("(?m)^\\s{0,3}(#{1,6}\\s+|[-*+]\\s+|>\\s?)"), "")
    .replace(Regex("\\s+"), " ")
    .trim()

/** The one-line preview for a list, following the session's status. */
fun previewOf(s: SessionView, status: Status): String {
    val i = s.info ?: return ""
    return when (status) {
        Status.PLAN -> "计划待批准"
        Status.POPUP -> i.statusDetail.ifEmpty { "终端里有弹窗" }
        else -> plain(i.preview).ifEmpty { if (i.title.isEmpty()) "还没发消息" else "" }
    }
}

/** Short relative time, like the mockup: 刚刚, 3 分钟前, 9:12, 昨天, 9月27日. */
fun whenText(ms: Long, now: Long = System.currentTimeMillis(), zone: ZoneId = ZoneId.systemDefault()): String {
    if (ms <= 0) return ""
    val d = now - ms
    if (d < 60_000) return "刚刚"
    if (d < 3_600_000) return "${d / 60_000} 分钟前"
    val then = Instant.ofEpochMilli(ms).atZone(zone).toLocalDate()
    val today = Instant.ofEpochMilli(now).atZone(zone).toLocalDate()
    if (then == today) return hhmm(ms, zone)
    if (then == today.minusDays(1)) return "昨天"
    return "${then.monthValue}月${then.dayOfMonth}日"
}

fun hhmm(ms: Long, zone: ZoneId = ZoneId.systemDefault()): String {
    val t = ZonedDateTime.ofInstant(Instant.ofEpochMilli(ms), zone)
    return "${t.hour}:${"%02d".format(t.minute)}"
}

fun dateTime(ms: Long, zone: ZoneId = ZoneId.systemDefault()): String {
    val t = ZonedDateTime.ofInstant(Instant.ofEpochMilli(ms), zone)
    val today = LocalDate.now(zone)
    val day = if (t.toLocalDate() == today) "" else "${t.monthValue}月${t.dayOfMonth}日 "
    return day + hhmm(ms, zone)
}

// --- approval modes ---

enum class Mode(val text: String, val proto: ApprovalMode) {
    ASK("询问", ApprovalMode.APPROVAL_MODE_ASK),
    AUTO("自动", ApprovalMode.APPROVAL_MODE_AUTO),
    PLAN("计划", ApprovalMode.APPROVAL_MODE_PLAN),
    FULL("完全放行", ApprovalMode.APPROVAL_MODE_FULL_ACCESS),
}

fun modeOf(m: ApprovalMode): Mode? = Mode.entries.firstOrNull { it.proto == m }

/** Claude Code's permission mode as a client mode, or null for its own ones such as acceptEdits. */
fun permissionMode(pm: String): Mode? = when (pm) {
    "default" -> Mode.ASK
    "auto" -> Mode.AUTO
    "plan" -> Mode.PLAN
    "bypassPermissions" -> Mode.FULL
    else -> null
}

/** Claude Code's permission mode as the chat names it: one of the four, or its own name for a mode outside them. */
fun permissionModeName(pm: String): String = permissionMode(pm)?.text ?: if (pm == "acceptEdits") "accept edits" else pm

/** A mode as shown: one of the four, or a CLI's own mode by its name. */
data class ModeLabel(val text: String, val mode: Mode?)

/** The approval mode named from turn-context fields: Claude Code's permission mode, or Codex's approval fields. */
fun contextMode(reviewer: String, policy: String, sandbox: String, collaboration: String, permission: String = ""): Mode? {
    if (permission.isNotEmpty()) return permissionMode(permission)
    if (reviewer.isEmpty() && policy.isEmpty() && collaboration.isEmpty()) return null
    if (collaboration == "plan") return Mode.PLAN
    if (sandbox == "danger-full-access" && policy == "never") return Mode.FULL
    if (reviewer == "auto_review") return Mode.AUTO
    return Mode.ASK
}

fun modeOf(s: SessionView): Mode? = modeLabel(s)?.mode

/**
 * The session's mode. Session.mode is either CLI's mode as a client mode;
 * UNSPECIFIED with a permission mode is one of Claude Code's own, shown by
 * its name. With neither (a Codex context outside the four modes), it is
 * named from the approval fields.
 */
fun modeLabel(s: SessionView): ModeLabel? {
    val i = s.info ?: return null
    modeOf(i.mode)?.let { return ModeLabel(it.text, it) }
    if (i.permissionMode.isNotEmpty()) {
        val m = permissionMode(i.permissionMode)
        return ModeLabel(m?.text ?: i.permissionMode, m)
    }
    return contextMode(i.approvalsReviewer, i.approvalPolicy, i.sandbox, i.collaborationMode)?.let { ModeLabel(it.text, it) }
}

/** Claude Code's haiku has no auto mode. */
fun noAutoMode(s: SessionView): Boolean = s.agent() == Agent.AGENT_CLAUDE && s.info?.model.orEmpty().contains("haiku", ignoreCase = true)

/**
 * The entry of a model list that a session's model is. Codex reports the slug
 * it was started with; Claude Code offers aliases (sonnet, opus) but records
 * the full ID (claude-sonnet-5-5), so an alias matches a full ID that has it
 * as one of its words.
 */
fun <M> findModel(models: List<M>, id: String, slug: (M) -> String): M? {
    if (id.isEmpty()) return null
    // Claude Code is started as opus[1m] to name its 1M window; the model is opus.
    val id = id.removeSuffix("[1m]")
    models.firstOrNull { slug(it) == id }?.let { return it }
    val words = id.lowercase().split(Regex("[-_.\\s]"))
    return models.firstOrNull { slug(it).lowercase() in words }
}

/** Whether the model a CLI reports ([id]) is the one chosen by slug or alias. */
fun modelIs(id: String, slug: String): Boolean =
    id.isNotEmpty() && slug.isNotEmpty() && (id == slug || slug.lowercase() in id.lowercase().split(Regex("[-_.\\s]")))

/**
 * The approval mode a new session gets when none is chosen: the daemon's
 * default for that CLI (architecture §13), or null when the daemon has none
 * and the CLI's own configuration decides.
 */
fun defaultMode(settings: io.github.lewin671.remoteagent.proto.DaemonSettings, agent: Agent): Mode? =
    modeOf(if (effectiveAgent(agent) == Agent.AGENT_CLAUDE) settings.claude.mode else settings.defaultMode)

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
    val kind: io.github.lewin671.remoteagent.proto.ToolKind = io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_UNSPECIFIED,
    val itemType: String = "",
    /** Seq of the ToolStarted event; its row in the chat keeps this place. */
    val seq: Long = 0,
    val input: String = "",
    val inputDigest: String = "",
) {
    /** Claude Code's Bash told to run in the background: its call returns while the command goes on. */
    val toBackground: Boolean get() = backgroundInput(input)
}

/** Whether a tool input asks for a background run (Claude Code's Bash run_in_background). */
fun backgroundInput(input: String): Boolean = Regex("\"run_in_background\"\\s*:\\s*true").containsMatchIn(input)

/**
 * The call went on in the background: this event is its start, not its
 * end. The daemon says so (ToolCall.background).
 */
fun wentToBackground(c: io.github.lewin671.remoteagent.proto.ToolCall): Boolean =
    c.background != io.github.lewin671.remoteagent.proto.ToolBackground.TOOL_BACKGROUND_UNSPECIFIED &&
        c.background != io.github.lewin671.remoteagent.proto.ToolBackground.UNRECOGNIZED

/** Why a call is in the background and what that means, as the web client says it. */
fun backgroundNote(b: io.github.lewin671.remoteagent.proto.ToolBackground): String {
    val rest = "它什么时候结束这里不知道，请到终端确认。"
    return when (b) {
        io.github.lewin671.remoteagent.proto.ToolBackground.TOOL_BACKGROUND_REQUESTED -> "按模型的要求在后台运行；"
        io.github.lewin671.remoteagent.proto.ToolBackground.TOOL_BACKGROUND_TIMED_OUT -> "运行超时后被移到后台继续运行；"
        io.github.lewin671.remoteagent.proto.ToolBackground.TOOL_BACKGROUND_BY_USER -> "在电脑终端里被移到后台（Ctrl+B）；"
        io.github.lewin671.remoteagent.proto.ToolBackground.TOOL_BACKGROUND_STILL_RUNNING -> "这一轮结束时它仍在运行；"
        else -> "调用已返回，命令转到了后台；"
    } + rest
}

/** A background call's title: 后台命令 for a shell call, else the tool's name with （后台）. */
fun backgroundTitle(kind: io.github.lewin671.remoteagent.proto.ToolKind, itemType: String, agent: Agent): String =
    if (kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_COMMAND || kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_UNSPECIFIED) "后台命令"
    else "${toolLabel(kind, itemType, agent)}（后台）"

/**
 * A tool whose end is not known (architecture §13, 工具执行反馈): its call
 * went to the background, or the turn ended before its result came. It is
 * not shown as a success; the terminal is where to check.
 */
class BackgroundTool(val tool: RunningTool, /** The call returned (it went to the background) rather than never coming back. */ val returned: Boolean)

/** A message as the daemon accepted it, and how far it has got. */
data class Accepted(
    val text: String,
    val imageIds: List<String>,
    val origin: io.github.lewin671.remoteagent.proto.Origin?,
    val stage: io.github.lewin671.remoteagent.proto.InputStage,
)

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
    /** Full latest input state: mode and pause are needed for pending bubbles. */
    val inputDetails: LinkedHashMap<String, InputState> = LinkedHashMap(),
    /** What the daemon accepted, per request ID: text, images and sender, shared by every client. */
    val accepted: HashMap<String, Accepted> = HashMap(),
    /** Why a message ended FAILED or NEEDS_TERMINAL, per request ID. */
    val failures: HashMap<String, String> = HashMap(),
    /**
     * Messages that never went out because the session ended: request ID to
     * the seq of the event that says so. The daemon records them as CANCELED
     * with reason "exited"; ones queued before that existed, left queued
     * when the session ended, count too.
     */
    val unsent: LinkedHashMap<String, Long> = LinkedHashMap(),
    var running: Boolean = false,
    var usage: TokenUsage? = null,
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
    /** Mode or model changes made in the terminal, by the seq of the turn context that shows them. */
    val terminalChanges: HashMap<Long, String> = HashMap(),
)

/** Pending approvals and questions, input states and the running turn. */
fun derive(events: List<Event>, agent: Agent = Agent.AGENT_CODEX): Derived = EventReducer(agent).apply { append(events) }.snapshot()

internal class EventReducer(agent: Agent) {
    private val d = Derived()
    // The mode as the chat names it, a CLI's own name for one outside the four; empty until a context says.
    private var mode = ""
    private var model = ""
    // The model and effort last switched to from here, until a context shows a model.
    private var expected: Pair<String, String>? = null
    // Messages sent from clients are recorded as submitted before the CLI
    // records them; the rest were typed in the terminal.
    private var submitted = 0
    // A user message with no submitted state before it, which may still come
    // right after: Claude Code records the message first.
    private var unclaimed: Long? = null
    // A plan was proposed since entering plan mode: leaving it is the plan's
    // answer, however given, not a mode switch made in the terminal.
    private var planned = false
    private val claude = effectiveAgent(agent) == Agent.AGENT_CLAUDE
    // Starts a turn ended without a result, by call ID, for a result that comes later.
    private val unfinishedById = HashMap<String, RunningTool>()
    fun append(events: List<Event>) {
        for (e in events) {
            when (e.kindCase) {
                Event.KindCase.APPROVAL_REQUESTED -> d.approvals[e.approvalRequested.approvalId] = e.approvalRequested
                Event.KindCase.APPROVAL_RESOLVED -> {
                    d.approvals.remove(e.approvalResolved.approvalId)
                    d.resolved[e.approvalResolved.approvalId] = e.approvalResolved.decision to e.approvalResolved.message
                    e.originOrNull()?.let { d.resolvedBy[e.approvalResolved.approvalId] = it }
                }
                Event.KindCase.QUESTION_ASKED -> if (!e.questionAsked.async) d.questions[e.questionAsked.questionId] = e.questionAsked
                Event.KindCase.QUESTION_ANSWERED -> {
                    d.questions.remove(e.questionAnswered.questionId)
                    d.answered[e.questionAnswered.questionId] = e.questionAnswered.answersList.flatMap { it.answersList }
                }
                Event.KindCase.TURN_STARTED -> {
                    d.running = true
                    d.background.clear()
                }
                Event.KindCase.TURN_ENDED -> {
                    d.running = false
                    d.questions.clear()
                    // Still running when an interrupted turn ended: whether it
                    // ever ends is not known (an interrupt does not stop
                    // background commands). A turn that completed without a
                    // tool's result only says no result came.
                    val aborted = e.turnEnded.outcome == io.github.lewin671.remoteagent.proto.TurnOutcome.TURN_OUTCOME_ABORTED
                    for (t in d.tools) {
                        if (aborted) d.background += BackgroundTool(t, returned = false) else d.noResult += t.seq
                        d.unfinished += t.seq
                        if (t.callId.isNotEmpty()) unfinishedById[t.callId] = t
                    }
                    d.tools.clear()
                }
                Event.KindCase.INPUT_STATE -> {
                    val st = e.inputState
                    val late = d.inputs[st.requestId] == InputStatus.INPUT_STATUS_UNCONFIRMED
                    d.inputs[st.requestId] = st.status
                    val before = d.inputDetails[st.requestId]
                    d.inputDetails[st.requestId] = st.toBuilder().apply {
                        if (before != null && st.mode == SendMode.SEND_MODE_UNSPECIFIED) mode = before.mode
                        if (before != null && st.text.isEmpty()) text = before.text
                    }.build()
                    val prev = d.accepted[st.requestId]
                    if (prev == null && st.status == InputStatus.INPUT_STATUS_QUEUED && (st.text.isNotEmpty() || st.imageIdsCount > 0)) {
                        d.accepted[st.requestId] = Accepted(st.text, st.imageIdsList, e.originOrNull(), st.stage)
                    } else if (prev != null && st.stage != io.github.lewin671.remoteagent.proto.InputStage.INPUT_STAGE_UNSPECIFIED) {
                        d.accepted[st.requestId] = prev.copy(stage = st.stage)
                    }
                    if (st.status == InputStatus.INPUT_STATUS_FAILED || st.status == InputStatus.INPUT_STATUS_NEEDS_TERMINAL) d.failures[st.requestId] = st.reason
                    if (e.inputState.status == InputStatus.INPUT_STATUS_CANCELED && e.inputState.reason == "exited") d.unsent[e.inputState.requestId] = e.seq
                    if (e.inputState.status == InputStatus.INPUT_STATUS_SUBMITTED) {
                        // Codex marks a message submitted before recording it;
                        // Claude Code records the message first and the state
                        // follows, claiming the message just before it. A
                        // message confirmed after it was reported unconfirmed
                        // is the one recorded right after this state.
                        val u = unclaimed
                        if (!late && claude && u != null && e.seq - u <= 3) {
                            d.typedLocally.remove(u)
                            unclaimed = null
                        } else {
                            submitted++
                        }
                    }
                }
                Event.KindCase.USER_MESSAGE -> if (e.hasOrigin() && e.origin.kind != io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_UNSPECIFIED) {
                    // The daemon says who sent it: no guessing.
                    if (e.origin.kind == io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_LOCAL) d.typedLocally += e.seq
                    else if (submitted > 0) submitted--
                } else {
                    if (submitted > 0) submitted--
                    // Codex sends this itself when a plan is approved.
                    else if (e.userMessage.text != "Implement the plan.") {
                        d.typedLocally += e.seq
                        unclaimed = e.seq
                    }
                }
                Event.KindCase.TOOL_STARTED -> e.toolStarted.let {
                    d.tools += RunningTool(it.title, it.callId, eventTime(e), it.kind, it.itemType, e.seq, it.input, it.inputDigest)
                }
                Event.KindCase.TOOL_CALL -> {
                    // Starts and calls pair by call ID (for Codex, later
                    // entries of one call carry "<id>#2" and have no start).
                    val id = e.toolCall.callId
                    val j = if (id.isNotEmpty()) d.tools.indexOfFirst { it.callId == id } else -1
                    if (j >= 0) {
                        val t = d.tools.removeAt(j)
                        d.completedAt[t.seq] = e.seq
                        if (t.toBackground || wentToBackground(e.toolCall)) d.background += BackgroundTool(t, returned = true)
                    } else if (id.isNotEmpty()) {
                        // A call reported after its turn ended (a Codex exec still
                        // running): it belongs to that start, not to a new row.
                        val t = unfinishedById[id]
                        if (t != null && t.seq !in d.completedAt) {
                            d.completedAt[t.seq] = e.seq
                            d.unfinished -= t.seq
                            d.noResult -= t.seq
                            d.background.removeAll { it.tool.seq == t.seq }
                            if (wentToBackground(e.toolCall)) d.background += BackgroundTool(t, returned = true)
                        }
                    }
                }
                Event.KindCase.TOKEN_USAGE -> d.usage = e.tokenUsage
                Event.KindCase.PLAN_PROPOSED -> {
                    d.plan = e.planProposed
                    planned = true
                }
                Event.KindCase.SETTINGS_CHANGED -> {
                    val c = e.settingsChanged
                    // The next context should show this model; the client names it
                    // by slug or alias, the context by the CLI's own ID.
                    if (c.model.isNotEmpty()) expected = c.model to c.effort
                    modeOf(c.mode)?.let { mode = it.text }
                }
                Event.KindCase.TURN_CONTEXT -> {
                    val c = e.turnContext
                    // Claude Code's mode by its own name, so that one outside the four is a change like any other.
                    val m = if (c.permissionMode.isNotEmpty()) permissionModeName(c.permissionMode)
                    else contextMode(c.approvalsReviewer, c.approvalPolicy, c.sandbox, c.collaborationMode)?.text.orEmpty()
                    val planning = c.collaborationMode == "plan" || c.permissionMode == "plan"
                    val mm = "${c.model} ${c.effort}"
                    val changes = mutableListOf<String>()
                    if (mode.isNotEmpty() && m.isNotEmpty() && m != mode && !(mode == Mode.PLAN.text && planned)) changes += "模式切到了 $m"
                    if (m.isNotEmpty() && m != mode) planned = false
                    // Plan mode runs at its own effort; only a model change counts
                    // there. A context without a model is Claude Code's mode, read
                    // off the screen; it says nothing about the model. After a
                    // switch from here the next context is compared with what was
                    // chosen; a different model means the terminal changed it since.
                    val want = expected
                    val changed = if (want != null) {
                        !(modelIs(c.model, want.first) && (want.second.isEmpty() || c.effort.isEmpty() || c.effort == want.second))
                    } else {
                        mm != model
                    }
                    if ((model.isNotEmpty() || want != null) && c.model.isNotEmpty() && changed && !planning) {
                        changes += "模型切到了 ${c.model}" + if (c.effort.isNotEmpty()) " · ${c.effort}" else ""
                    }
                    if (c.model.isNotEmpty() && !planning) expected = null
                    if (changes.isNotEmpty()) d.terminalChanges[e.seq] = changes.joinToString("，")
                    if (m.isNotEmpty()) mode = m
                    if (!planning && c.model.isNotEmpty()) model = mm
                }
                Event.KindCase.SESSION_STATE -> if (e.sessionState.status == SessionStatus.SESSION_STATUS_EXITED) {
                    d.running = false
                    d.approvals.clear()
                    d.questions.clear()
                    for (t in d.tools) d.unfinished += t.seq
                    d.tools.clear()
                    d.background.clear()
                }
                // What came before is a copy of the source's history: nothing
                // of it is pending or running in this session.
                Event.KindCase.SESSION_FORKED -> {
                    d.running = false
                    d.approvals.clear()
                    d.questions.clear()
                    for (t in d.tools) d.unfinished += t.seq
                    d.tools.clear()
                    d.background.clear()
                    submitted = 0
                    unclaimed = null
                }
                else -> {}
            }
        }
    }
    fun snapshot(): Derived = Derived(
        approvals = LinkedHashMap(d.approvals),
        resolved = HashMap(d.resolved),
        resolvedBy = HashMap(d.resolvedBy),
        questions = LinkedHashMap(d.questions),
        answered = HashMap(d.answered),
        inputs = LinkedHashMap(d.inputs),
        inputDetails = LinkedHashMap(d.inputDetails),
        accepted = HashMap(d.accepted),
        failures = HashMap(d.failures),
        unsent = LinkedHashMap(d.unsent),
        tools = ArrayList(d.tools),
        background = ArrayList(d.background),
        completedAt = HashMap(d.completedAt),
        unfinished = HashSet(d.unfinished),
        noResult = HashSet(d.noResult),
        typedLocally = HashSet(d.typedLocally),
        terminalChanges = HashMap(d.terminalChanges),
        running = d.running, usage = d.usage, plan = d.plan,
    )
}

/** Reuses reducer state only for an unchanged prefix; published results never mutate. */
class EventProjection {
    private var previous: List<Event> = emptyList()
    private var agent: Agent? = null
    private var reducer = EventReducer(Agent.AGENT_CODEX)
    private var result = Derived()
    fun update(events: List<Event>, agent: Agent = Agent.AGENT_CODEX): Derived {
        if (this.agent == agent && events === previous) return result
        val append = this.agent == agent && unchangedPrefix(events, previous)
        if (!append) reducer = EventReducer(agent)
        reducer.append(if (append) events.subList(previous.size, events.size) else events)
        result = reducer.snapshot()
        previous = events
        this.agent = agent
        return result
    }
}

/** Match Claude Code by daemon digest, or Codex by the command it reported for the call. */
fun awaitingApproval(t: RunningTool, d: Derived): Boolean = d.approvals.values.any { a ->
    if (t.inputDigest.isNotEmpty()) a.inputDigest.isNotEmpty() && a.inputDigest == t.inputDigest
    else a.inputDigest.isEmpty() && a.command.isNotEmpty() && (a.command == t.input || a.command == t.title)
}

/**
 * How long a tool has run, in ms, or null when its start time is unknown.
 * While disconnected ([frozenAt] > 0) the reading stops where the
 * connection dropped, so it does not suggest the tool is still running.
 */
fun toolElapsed(t: RunningTool, now: Long, frozenAt: Long = 0): Long? {
    if (t.since <= 0) return null
    val end = if (frozenAt > 0) minOf(frozenAt, now) else now
    return (end - t.since).coerceAtLeast(0)
}

/** 2:12, 1:02:03: a running timer. */
fun clock(ms: Long): String {
    val s = ms / 1000
    return if (s >= 3600) "%d:%02d:%02d".format(s / 3600, s % 3600 / 60, s % 60) else "%d:%02d".format(s / 60, s % 60)
}

/** A tool's short name: Claude Code's own tool name (Bash, Read), else what kind of tool it is. */
fun toolLabel(kind: io.github.lewin671.remoteagent.proto.ToolKind, itemType: String, agent: Agent): String = when {
    effectiveAgent(agent) == Agent.AGENT_CLAUDE && itemType.isNotEmpty() -> itemType
    kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_COMMAND -> "命令"
    kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_FILE_CHANGE -> "编辑"
    kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_MCP -> "MCP"
    kind == io.github.lewin671.remoteagent.proto.ToolKind.TOOL_KIND_WEB_SEARCH -> "搜索"
    else -> "工具"
}

fun eventTime(e: Event): Long = if (e.hasTime()) e.time.seconds * 1000 + e.time.nanos / 1_000_000 else 0

fun decisionText(d: ApprovalDecision): String = when (d) {
    ApprovalDecision.APPROVAL_DECISION_ALLOW -> "已允许"
    ApprovalDecision.APPROVAL_DECISION_ALLOW_ALWAYS -> "已允许"
    ApprovalDecision.APPROVAL_DECISION_DENY -> "已拒绝"
    ApprovalDecision.APPROVAL_DECISION_TIMEOUT -> "超时，已拒绝"
    ApprovalDecision.APPROVAL_DECISION_CANCELED -> "已取消"
    ApprovalDecision.APPROVAL_DECISION_TERMINAL -> "已在终端处理"
    else -> ""
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
 * Absolute paths in a tool title, shortened for display, exactly as
 * shortenPaths in web/src/model.ts (its cases are copied into
 * ShortPathsTest). Paths are whole tokens: a quoted one is the whole quoted
 * text, spaces and all; an unquoted one runs to whitespace or a quote; a
 * token starts with / after the start, a space, =, :, (, [, { or a comma.
 * Under the session folder a path becomes relative to it, the folder itself
 * "."; else under the home folder ~/…, and without a known home
 * /Users/<name> and /home/<name> count as one. A folder only matches when
 * what follows it is /, nothing or a :line suffix; anything else, or
 * anything unsure, stays as written.
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
 * One whole path, spaces and all (a file edit's title), shortened by the
 * rule of [shortenPaths]; unchanged when unsure. Same as shortenPath in
 * web/src/model.ts.
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
    under(p, base)?.let { r -> return if (r.isEmpty() || r == "/") "." else if (r[0] == '/') r.substring(1) else ".$r" }
    val hm = h.ifEmpty { homeDir.find(p)?.value.orEmpty() }
    return under(p, hm)?.let { "~$it" } ?: path
}

fun shortPath(p: String): String = p.replace(Regex("^/Users/[^/]+"), "~").replace(Regex("^/home/[^/]+"), "~")

/** Minutes left until a deadline, for "剩 N 分钟". */
fun minutesLeft(deadlineMs: Long, now: Long = System.currentTimeMillis()): Long = maxOf(0, (deadlineMs - now + 59_999) / 60_000)

// --- the message queue ---

/** Why a message was not submitted (InputState.reason), in words. */
fun failureText(reason: String, agent: Agent): String = when (reason) {
    "image_not_recognized" -> "${agentName(agent)} 没认出图片"
    "input_changed" -> "无法确认终端输入框的内容"
    "timeout" -> "等待超时"
    "restarted" -> "电脑上的 remote-agent 重启了"
    "write_failed" -> "写入终端失败"
    "exited" -> "会话已结束"
    "" -> "原因未知"
    else -> reason
}

/** Messages the daemon accepted and has not written yet, in the order it accepted them. */
fun queued(d: Derived): List<String> = d.inputs.filterValues { it == InputStatus.INPUT_STATUS_QUEUED || it == InputStatus.INPUT_STATUS_HELD_BY_CLI }.keys.toList()

data class PendingInput(val id: String, val text: String, val mode: SendMode, val paused: Boolean, val held: Boolean, val origin: io.github.lewin671.remoteagent.proto.Origin?)

/** Only inputs the model has not received; event order is acceptance order. */
fun pendingInputs(d: Derived): List<PendingInput> = d.inputDetails.values.mapNotNull { st ->
    if (st.status != InputStatus.INPUT_STATUS_QUEUED && st.status != InputStatus.INPUT_STATUS_HELD_BY_CLI) return@mapNotNull null
    val accepted = d.accepted[st.requestId]
    PendingInput(st.requestId, accepted?.text ?: st.text, st.mode.takeIf { it != SendMode.SEND_MODE_UNSPECIFIED } ?: SendMode.SEND_MODE_STEER,
        st.paused, st.status == InputStatus.INPUT_STATUS_HELD_BY_CLI, accepted?.origin)
}

/** CLI's native second approval option, translated only when its form is known. */
fun approvalAlwaysText(message: String): String {
    Regex("(?:Yes, and )?don't ask again for commands that start with [`]?(.+?)[`]?$").matchEntire(message)?.let {
        return "`${it.groupValues[1].trim('`')}` 开头的命令以后不再问"
    }
    Regex("(?:Yes, and )?always allow access to (.+?) from this project$").matchEntire(message)?.let {
        return "本项目访问 ${it.groupValues[1]} 以后不再问"
    }
    if (message.contains("accept edits", ignoreCase = true)) return "本会话自动接受编辑"
    return message
}

/**
 * Why the first queued message waits: a short form for the line above the
 * message box and a longer one for the queue page. The daemon's own reason
 * (Session.input_wait) comes first; else the session's state says it.
 */
data class QueueWhy(val short: String, val long: String)

fun queueWhy(s: SessionView, status: Status): QueueWhy = when (s.info?.inputWait) {
    "draft" -> QueueWhy("终端里有草稿", "终端里有没发出的草稿，发完或清掉后再发")
    "popup" -> QueueWhy("就绪后发送", "等终端里的弹窗处理完")
    "unrecognized" -> QueueWhy("已暂停", "认不出输入框，已暂停")
    else -> when (status) {
        Status.QUESTION -> QueueWhy("回答后继续", "等提问回答完")
        Status.APPROVAL -> QueueWhy("审批后继续", "等审批处理完")
        Status.PLAN -> QueueWhy("处理计划后继续", "等计划处理完")
        Status.POPUP -> QueueWhy("就绪后发送", "等终端里的弹窗处理完")
        Status.RUNNING -> QueueWhy("本轮结束后发送", "等待本轮结束")
        Status.STARTING -> QueueWhy("就绪后发送", "等 ${agentName(s.agent())} 就绪")
        else -> QueueWhy("即将发送", "等待输入框就绪")
    }
}

/**
 * Where a message the user tried to cancel ended up, once it left the queue:
 * the cancel worked, someone else canceled it, or it was written first.
 */
enum class CancelOutcome { CANCELED, CANCELED_ELSEWHERE, WRITTEN, UNKNOWN }

fun cancelOutcome(status: InputStatus?, canceledHere: Boolean): CancelOutcome = when (status) {
    InputStatus.INPUT_STATUS_CANCELED -> if (canceledHere) CancelOutcome.CANCELED else CancelOutcome.CANCELED_ELSEWHERE
    InputStatus.INPUT_STATUS_SUBMITTED, InputStatus.INPUT_STATUS_UNCONFIRMED -> CancelOutcome.WRITTEN
    else -> CancelOutcome.UNKNOWN
}

// --- devices and where actions came from ---

/** What kind of client, from what it says it runs on: Chrome, Safari, Android; a browser that says nothing is 浏览器. */
fun platformLabel(meta: io.github.lewin671.remoteagent.proto.DeviceMeta?): String {
    val first = meta?.description.orEmpty().trim().split(Regex("\\s+")).first()
    if (first.isNotEmpty() && !first[0].isDigit()) return first
    return when (meta?.platform) {
        "android" -> "Android"
        "ios" -> "iOS"
        "web" -> "浏览器"
        else -> "客户端"
    }
}

/** A client's name: the one given in its sealed metadata, else its kind and the end of its ID (Chrome · a8f2). */
fun clientName(d: DeviceView?, id: String): String {
    return d?.meta?.name.orEmpty().trim().ifEmpty { defaultClientName(d, id) }
}

/** The name a client has until the user gives it one, and again once they take theirs back. */
fun defaultClientName(d: DeviceView?, id: String): String = "${platformLabel(d?.meta)} · ${shortId(id)}"

/** What a client is, under its name in the list: an app or a browser, and what it says it runs on. */
fun clientDetail(d: DeviceView?): String {
    val kind = when (d?.meta?.platform) {
        "android", "ios" -> "App"
        "web" -> "浏览器"
        else -> ""
    }
    return listOf(kind, d?.meta?.description.orEmpty().trim()).filter { it.isNotEmpty() }.joinToString(" · ").ifEmpty { "种类未知" }
}

/** A day as lists write it: 9月27日, with the year when it is not this one. */
fun dayText(ms: Long, now: Long = System.currentTimeMillis(), zone: ZoneId = ZoneId.systemDefault()): String {
    val day = Instant.ofEpochMilli(ms).atZone(zone).toLocalDate()
    val year = if (day.year == Instant.ofEpochMilli(now).atZone(zone).year) "" else "${day.year}年"
    return "$year${day.monthValue}月${day.dayOfMonth}日"
}

/** When a client was last used: 刚刚, 3 分钟前, 今天 9:12, 昨天, 9月27日; "" when the server has no record of it. */
fun lastUsed(d: DeviceView?, now: Long = System.currentTimeMillis(), zone: ZoneId = ZoneId.systemDefault()): String {
    val t = d?.lastSeenMs ?: 0
    if (t <= 0) return ""
    val ago = now - t
    if (ago < 60_000) return "刚刚"
    if (ago < 3_600_000) return "${ago / 60_000} 分钟前"
    val then = Instant.ofEpochMilli(t).atZone(zone).toLocalDate()
    val today = Instant.ofEpochMilli(now).atZone(zone).toLocalDate()
    if (then == today) return "今天 ${hhmm(t, zone)}"
    if (then == today.minusDays(1)) return "昨天"
    return dayText(t, now, zone)
}

/** What the list says beside a client: when it was last used, else the day it was added. */
fun clientUsed(d: DeviceView?, now: Long = System.currentTimeMillis(), zone: ZoneId = ZoneId.systemDefault()): String {
    val used = lastUsed(d, now, zone)
    if (used.isNotEmpty()) return used + (if (used.last().isDigit()) " " else "") + "使用"
    return if ((d?.createdMs ?: 0) > 0) "${dayText(d!!.createdMs, now, zone)}添加" else "已配对"
}

/** Clients as the list orders them: this one, then the most recently used first, then those with no record, the newest first. */
fun sortClients(clients: Collection<DeviceView>, me: String): List<DeviceView> =
    clients.sortedWith(compareBy<DeviceView> { it.id != me }.thenByDescending { it.lastSeenMs }.thenByDescending { it.createdMs })

/** The last four hex digits of a device ID, as shown next to a name. */
fun shortId(id: String): String = id.takeLast(4)

/**
 * Who an event says caused it (Event.origin), for display, or null when
 * the event does not say. Only a signed client is named, and only by the
 * account's device list; anything unsure is "另一客户端", never a guess.
 */
fun originLabel(o: io.github.lewin671.remoteagent.proto.Origin?, state: RemoteState, me: String, machine: String): String? {
    if (o == null) return null
    return when (o.kind) {
        io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_CLIENT -> when {
            o.deviceId == me -> "本客户端"
            o.deviceId.isEmpty() -> "另一客户端"
            else -> state.devices[o.deviceId]?.let { clientName(it, o.deviceId) } ?: "客户端 · ${shortId(o.deviceId)}"
        }
        io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_LOCAL -> "$machine 本地终端"
        io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_SYSTEM -> null
        else -> null
    }
}

fun Event.originOrNull(): io.github.lewin671.remoteagent.proto.Origin? = if (hasOrigin()) origin else null

/** What the machine says it offers; null until its metadata is read. */
fun capabilities(m: MachineView?): io.github.lewin671.remoteagent.proto.MachineCapabilities? =
    m?.meta?.takeIf { it.hasCapabilities() }?.capabilities

/**
 * Why no command goes to [m]: it speaks another protocol version than this
 * app (PROTOCOL_VERSION), said by which side to update; null when they
 * match or the machine's metadata is not read yet.
 */
fun protocolMismatch(m: MachineView?): String? {
    val theirs = m?.meta?.protocolVersion ?: return null
    return when {
        theirs < PROTOCOL_VERSION -> "${machineName(m)} 上的 remote-agent 版本较旧，请在电脑上更新后再试"
        theirs > PROTOCOL_VERSION -> "这个 App 版本较旧，请更新后再试"
        else -> null
    }
}

/** A model's context window as the pickers write it: 1M 上下文, 200k 上下文; null when unknown. */
fun contextText(tokens: Long): String? = when {
    tokens <= 0 -> null
    tokens >= 1_000_000 -> "${(tokens / 100_000 / 10.0).toString().removeSuffix(".0")}M 上下文"
    else -> "${(tokens + 500) / 1000}k 上下文"
}

/** The standard context window, which a Claude Code session can be held to. */
const val STANDARD_WINDOW = 200_000L
