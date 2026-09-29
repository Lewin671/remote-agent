// View models derived from the decrypted state, as in web/src/model.ts:
// projects, statuses, and what a session's events say is pending.
package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalDecision
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.InputStatus
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

/** The CLIs a machine can start; daemons from before Claude Code support only run Codex. */
fun machineAgents(m: MachineView?): List<Agent> {
    val listed = m?.meta?.agentsList?.map { effectiveAgent(it.agent) }?.distinct().orEmpty()
    return listed.ifEmpty { listOf(Agent.AGENT_CODEX) }
}

// --- projects ---

data class Project(
    val key: String,
    val name: String,
    /** Most recent first. */
    val sessions: List<SessionView>,
    val updatedMs: Long,
    val paths: List<String>,
    val machines: List<String>,
)

/** The project a session belongs to: its working directory's absolute path, the same on every machine. */
fun projectKey(s: SessionView): String = s.info?.cwd.orEmpty()

/** A project's name: the last folder of its path. */
fun projectName(key: String): String = key.split("/").lastOrNull { it.isNotEmpty() } ?: "未知项目"

fun projects(state: RemoteState): List<Project> {
    val byKey = LinkedHashMap<String, MutableList<SessionView>>()
    for (s in state.sessions.values) byKey.getOrPut(projectKey(s)) { mutableListOf() } += s
    return byKey.map { (key, list) ->
        val sorted = list.sortedByDescending { it.updatedMs }
        Project(
            key = key,
            name = projectName(key),
            sessions = sorted,
            updatedMs = sorted.maxOfOrNull { it.updatedMs } ?: 0,
            paths = sorted.mapNotNull { it.info?.cwd?.takeIf { c -> c.isNotEmpty() } }.distinct(),
            machines = sorted.map { machineName(state.machines[it.machineId], it.machineId) }.distinct(),
        )
    }.sortedByDescending { it.updatedMs }
}

fun machineName(m: MachineView?, id: String = ""): String =
    m?.meta?.hostname?.removeSuffix(".local")?.takeIf { it.isNotEmpty() } ?: m?.id?.take(8) ?: id.take(8).ifEmpty { "未知电脑" }

/** Avatar color index 0..3, stable per project. */
fun avatarIndex(key: String): Int {
    var h = 0L
    for (c in key) h = (h * 31 + c.code) and 0xffffffffL
    return (h % 4).toInt()
}

// --- sessions ---

enum class Tone { RUN, WAIT, IDLE, OFF }

/** One state per session, the highest in the priority list of the design (client-v1, 状态优先级). */
enum class Status(val text: String, val tone: Tone) {
    LOADING("读取中", Tone.OFF),
    EXITED("已结束", Tone.OFF),
    OFFLINE("电脑离线", Tone.OFF),
    APPROVAL("等待审批", Tone.WAIT),
    QUESTION("等你回答", Tone.WAIT),
    PLAN("计划待批准", Tone.WAIT),
    POPUP("终端里有弹窗", Tone.WAIT),
    RUNNING("运行中", Tone.RUN),
    STARTING("启动中", Tone.RUN),
    IDLE("空闲", Tone.IDLE),
}

fun exited(s: SessionView): Boolean = s.info?.status == SessionStatus.SESSION_STATUS_EXITED

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

fun needsAttention(s: SessionView, machine: MachineView?): Boolean = statusOf(s, machine).tone == Tone.WAIT

/** The session's title; without one, "<folder> · <start time>". */
fun titleOf(s: SessionView, zone: ZoneId = ZoneId.systemDefault()): String {
    val t = plain(s.info?.title.orEmpty()).trim()
    if (t.isNotEmpty()) return t
    val i = s.info ?: return "新会话"
    val created = if (i.hasCreatedAt()) i.createdAt.seconds * 1000 else 0
    return if (created > 0) "${projectName(i.cwd)} · ${hhmm(created, zone)}" else projectName(i.cwd)
}

/** Text for one-line previews: Markdown marks removed. */
fun plain(text: String): String = text
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
 * its name. Older daemons set neither: it is named from the approval fields.
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
    models.firstOrNull { slug(it) == id }?.let { return it }
    val words = id.lowercase().split(Regex("[-_.\\s]"))
    return models.firstOrNull { slug(it).lowercase() in words }
}

/** Context window use in percent, or null when unknown. */
fun contextPercent(s: SessionView): Int? {
    val i = s.info ?: return null
    if (i.contextWindow <= 0) return null
    return minOf(100, (i.contextTokens * 100 / i.contextWindow).toInt())
}

// --- events ---

class RunningTool(val title: String, val callId: String, val since: Long)

class Derived(
    /** Pending approvals, in the order they were asked. */
    val approvals: LinkedHashMap<String, ApprovalRequested> = LinkedHashMap(),
    val resolved: HashMap<String, Pair<ApprovalDecision, String>> = HashMap(),
    val questions: LinkedHashMap<String, QuestionAsked> = LinkedHashMap(),
    val answered: HashMap<String, List<String>> = HashMap(),
    /** Latest state per request ID of messages sent with Send. */
    val inputs: LinkedHashMap<String, InputStatus> = LinkedHashMap(),
    var running: Boolean = false,
    var usage: TokenUsage? = null,
    var plan: PlanProposed? = null,
    /** Tools started and not yet completed, oldest first. */
    val tools: MutableList<RunningTool> = mutableListOf(),
    /** User messages typed in the terminal rather than sent from a client. */
    val typedLocally: MutableSet<Long> = HashSet(),
    /** Mode or model changes made in the terminal, by the seq of the turn context that shows them. */
    val terminalChanges: HashMap<Long, String> = HashMap(),
)

/** Pending approvals and questions, input states and the running turn. */
fun derive(events: List<Event>, agent: Agent = Agent.AGENT_CODEX): Derived {
    val d = Derived()
    var mode: Mode? = null
    var model = ""
    // Messages sent from clients are recorded as submitted before the CLI
    // records them; the rest were typed in the terminal.
    var submitted = 0
    // A user message with no submitted state before it, which may still come
    // right after: Claude Code records the message first.
    var unclaimed: Long? = null
    // A plan was proposed since entering plan mode: leaving it is the plan's
    // answer, however given, not a mode switch made in the terminal.
    var planned = false
    val claude = effectiveAgent(agent) == Agent.AGENT_CLAUDE
    for (e in events) {
        when (e.kindCase) {
            Event.KindCase.APPROVAL_REQUESTED -> d.approvals[e.approvalRequested.approvalId] = e.approvalRequested
            Event.KindCase.APPROVAL_RESOLVED -> {
                d.approvals.remove(e.approvalResolved.approvalId)
                d.resolved[e.approvalResolved.approvalId] = e.approvalResolved.decision to e.approvalResolved.message
            }
            Event.KindCase.QUESTION_ASKED -> if (!e.questionAsked.async) d.questions[e.questionAsked.questionId] = e.questionAsked
            Event.KindCase.QUESTION_ANSWERED -> {
                d.questions.remove(e.questionAnswered.questionId)
                d.answered[e.questionAnswered.questionId] = e.questionAnswered.answersList.flatMap { it.answersList }
            }
            Event.KindCase.TURN_STARTED -> d.running = true
            Event.KindCase.TURN_ENDED -> {
                d.running = false
                d.questions.clear()
                d.tools.clear()
            }
            Event.KindCase.INPUT_STATE -> {
                d.inputs[e.inputState.requestId] = e.inputState.status
                if (e.inputState.status == InputStatus.INPUT_STATUS_SUBMITTED) {
                    // Codex marks a message submitted before recording it;
                    // Claude Code records the message first and the state
                    // follows, claiming the message just before it.
                    val u = unclaimed
                    if (claude && u != null && e.seq - u <= 3) {
                        d.typedLocally.remove(u)
                        unclaimed = null
                    } else {
                        submitted++
                    }
                }
            }
            Event.KindCase.USER_MESSAGE -> {
                if (submitted > 0) submitted--
                // Codex sends this itself when a plan is approved.
                else if (e.userMessage.text != "Implement the plan.") {
                    d.typedLocally += e.seq
                    unclaimed = e.seq
                }
            }
            Event.KindCase.TOOL_STARTED -> d.tools += RunningTool(e.toolStarted.title, e.toolStarted.callId, eventTime(e))
            Event.KindCase.TOOL_CALL -> {
                // Claude Code runs tools in parallel and pairs them by ID;
                // Codex gives no ID and they pair in order.
                val id = e.toolCall.callId
                val i = if (id.isNotEmpty()) d.tools.indexOfFirst { it.callId == id } else -1
                val j = if (i >= 0) i else d.tools.indexOfFirst { it.callId.isEmpty() }
                if (j >= 0) d.tools.removeAt(j)
            }
            Event.KindCase.TOKEN_USAGE -> d.usage = e.tokenUsage
            Event.KindCase.PLAN_PROPOSED -> {
                d.plan = e.planProposed
                planned = true
            }
            Event.KindCase.SETTINGS_CHANGED -> {
                val c = e.settingsChanged
                // The client names models by slug or alias, contexts by the
                // CLI's own ID: start over from the next context rather than
                // compare the two.
                if (c.model.isNotEmpty()) model = ""
                modeOf(c.mode)?.let { mode = it }
            }
            Event.KindCase.TURN_CONTEXT -> {
                val c = e.turnContext
                val m = contextMode(c.approvalsReviewer, c.approvalPolicy, c.sandbox, c.collaborationMode, c.permissionMode)
                val planning = c.collaborationMode == "plan" || c.permissionMode == "plan"
                val mm = "${c.model} ${c.effort}"
                val changes = mutableListOf<String>()
                if (mode != null && m != null && m != mode && !(mode == Mode.PLAN && planned)) changes += "模式切到了 ${m.text}"
                if (m != null && m != mode) planned = false
                // Plan mode runs at its own effort; only a model change counts
                // there. A context without a model is Claude Code's mode, read
                // off the screen; it says nothing about the model.
                if (model.isNotEmpty() && c.model.isNotEmpty() && mm != model && !planning) {
                    changes += "模型切到了 ${c.model}" + if (c.effort.isNotEmpty()) " · ${c.effort}" else ""
                }
                if (changes.isNotEmpty()) d.terminalChanges[e.seq] = changes.joinToString("，")
                if (m != null) mode = m
                if (!planning && c.model.isNotEmpty()) model = mm
            }
            Event.KindCase.SESSION_STATE -> if (e.sessionState.status == SessionStatus.SESSION_STATUS_EXITED) {
                d.running = false
                d.approvals.clear()
                d.questions.clear()
                d.tools.clear()
            }
            else -> {}
        }
    }
    return d
}

fun eventTime(e: Event): Long = if (e.hasTime()) e.time.seconds * 1000 + e.time.nanos / 1_000_000 else 0

fun decisionText(d: ApprovalDecision): String = when (d) {
    ApprovalDecision.APPROVAL_DECISION_ALLOW -> "已允许"
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

fun shortPath(p: String): String = p.replace(Regex("^/Users/[^/]+"), "~").replace(Regex("^/home/[^/]+"), "~")

/** Minutes left until a deadline, for "剩 N 分钟". */
fun minutesLeft(deadlineMs: Long, now: Long = System.currentTimeMillis()): Long = maxOf(0, (deadlineMs - now + 59_999) / 60_000)
