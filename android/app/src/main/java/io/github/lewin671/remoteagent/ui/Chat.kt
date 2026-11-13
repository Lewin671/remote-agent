package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.SizeTransform
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.ui.layout.layout
import androidx.compose.ui.platform.LocalDensity
import kotlin.math.roundToInt
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.border
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.wrapContentHeight
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.DEFAULT_AGENT
import io.github.lewin671.remoteagent.core.Derived
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.core.shortenPath
import io.github.lewin671.remoteagent.core.shortenPaths
import io.github.lewin671.remoteagent.core.dateTime
import io.github.lewin671.remoteagent.core.decisionText
import io.github.lewin671.remoteagent.core.eventTime
import io.github.lewin671.remoteagent.core.modeOf
import io.github.lewin671.remoteagent.core.originOrNull
import io.github.lewin671.remoteagent.proto.ApprovalDecision
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.Question
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.proto.TurnOutcome

data class StartPrompt(val text: String, val caption: String, val warning: Boolean = false, val uncertain: Boolean = false)

/** A new session's acknowledged prompt until history records it; never resend it here. */
fun pendingStartPrompt(prompt: String?, events: List<Event>, d: Derived, localIds: Set<String>, agent: Agent = DEFAULT_AGENT, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): StartPrompt? {
    if (prompt.isNullOrEmpty() || events.any { it.hasUserMessage() }) return null
    val first = d.inputs.keys.firstOrNull { it !in localIds }
    val status = first?.let { d.inputs[it] }
    val reason = first?.let { d.failures[it] }.orEmpty()
    return when (status) {
        io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_CANCELED -> null
        io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_FAILED,
        io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_NEEDS_TERMINAL,
        io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_UNCONFIRMED -> {
            val feedback = inputFeedback(status, reason, agent, copy = copy)
            StartPrompt(prompt, feedback.text, warning = true, uncertain = feedback.uncertain)
        }
        // Still waiting: the pending bubble shows it, with what it waits for and a way to cancel.
        io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_QUEUED,
        io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_HELD_BY_CLI -> null
        else -> StartPrompt(prompt, copy(UiMessages.sessionDelivered()))
    }
}

/** What the chat shows, built from the events. */
sealed interface ChatItem {
    val key: String

    /** [from]: who sent it, when the daemon says (v2): another client's name, or the computer's terminal. */
    data class User(
        override val key: String,
        val text: String,
        val local: Boolean,
        val time: Long,
        val from: String? = null,
        val images: List<io.github.lewin671.remoteagent.proto.ImageRef> = emptyList(),
        val seq: Long = 0,
        val files: List<io.github.lewin671.remoteagent.proto.SentFile> = emptyList(),
        val scheduled: Boolean = false,
    ) : ChatItem

    /** Images the agent made, as a card of their own. */
    data class Generated(override val key: String, val seq: Long, val prompt: String, val images: List<io.github.lewin671.remoteagent.proto.ImageRef>) : ChatItem
    data class QuietScheduled(override val key: String, val original: List<ChatItem>, val times: List<Long>, val through: Long) : ChatItem
    data class Agent(override val key: String, val text: String) : ChatItem
    /** Ordinary AI text; async questions are answered through the composer, never the dock. */
    data class AsyncQuestion(override val key: String, val questions: List<Question>) : ChatItem
    data class Thought(override val key: String, val text: String) : ChatItem

    /**
     * One tool call; [decision] says how its approval went, if it needed one.
     * [background]: the call returned but its command went on in the
     * background, so how it ends is not known.
     */
    data class Tool(override val key: String, val call: ToolCall, val decision: String = "", val decisionOk: Boolean = true, val background: Boolean = false, val seq: Long = 0) : ChatItem

    /** One or more files sent by the agent in a single tool call, replacing that tool call row. */
    data class SharedFiles(
        override val key: String,
        val seq: Long,
        val files: List<io.github.lewin671.remoteagent.proto.SharedFile>,
    ) : ChatItem

    /**
     * A tool that started and has no result yet, where it started. With
     * [unfinished] its turn ended first: it may still run in the background.
     */
    data class Running(override val key: String, val tool: io.github.lewin671.remoteagent.core.RunningTool, val unfinished: Boolean) : ChatItem

    /** Two or more completed tool calls in a row, folded into one line. */
    data class Tools(override val key: String, val calls: List<ToolCall>, val callSeqs: List<Long> = emptyList()) : ChatItem
    /** A line of its own: a centred note, or with [icon] a record like a tool row, [meta] at its right. */
    data class Note(override val key: String, val text: String, val icon: ImageVector? = null, val tone: Int = 0, val meta: String = "", val emphasis: String = "") : ChatItem
    data class Plan(override val key: String, val text: String) : ChatItem

    /** A message the session ended before writing; [text] is known when sent from here. */
    data class Unsent(override val key: String, val requestId: String, val text: String?) : ChatItem
    data class TurnEnd(override val key: String, val text: String) : ChatItem

    /** Where a forked session leaves its source: [time] 分叉自 the source, by the title it had then. */
    data class Fork(override val key: String, val time: String, val sourceId: String, val sourceTitle: String) : ChatItem
}

/**
 * How the fork line names the source: its title now when the session is
 * held here, else the one the event recorded. The line opens the source
 * either way: one on a page of the listing not fetched yet is fetched by
 * its ID when opened (architecture §8 「按 ID 取」).
 */
fun forkSource(item: ChatItem.Fork, sessions: Map<String, io.github.lewin671.remoteagent.core.SessionView>, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String =
    sessions[item.sourceId]?.takeIf { it.info != null }?.let { io.github.lewin671.remoteagent.core.listTitle(it).resolve(copy) }
        ?: io.github.lewin671.remoteagent.core.plain(item.sourceTitle).ifEmpty { copy(UiMessages.sessionOtherSession()) }

fun toolOk(t: ToolCall) = t.status != "failed" && (!t.hasExitCode() || t.exitCode == 0)

/** How a tool call is named in the chat: paths shortened, as on the web; the details keep them whole. */
fun toolTitle(t: ToolCall, cwd: String, home: String = "", copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String = toolTitle(t.kind, t.title, t.itemType, cwd, home, copy = copy)

/** The same for a call still running, so that its row reads the same before and after. */
fun toolTitle(t: io.github.lewin671.remoteagent.core.RunningTool, cwd: String, home: String = "", copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String = toolTitle(t.kind, t.title, t.itemType, cwd, home, copy = copy)

private fun toolTitle(kind: ToolKind, title: String, itemType: String, cwd: String, home: String, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String {
    // A file edit's title is one path, spaces and all.
    if (kind == ToolKind.TOOL_KIND_FILE_CHANGE) return copy(UiMessages.toolsEditFile(path = (shortenPath(title, cwd, home)).toString()))
    val fallback = when (itemType) {
        "ToolCall", "CommandExecution" -> when (kind) {
            ToolKind.TOOL_KIND_COMMAND -> copy(UiMessages.toolsCommand())
            ToolKind.TOOL_KIND_FILE_CHANGE -> copy(UiMessages.toolsAndroidEdit())
            else -> copy(UiMessages.toolsTool())
        }
        else -> itemType.ifEmpty { copy(UiMessages.toolsTool()) }
    }
    return shortenPaths(title.takeUnless { it == "ToolCall" || it == "CommandExecution" }.orEmpty().ifEmpty { fallback }, cwd, home)
}

/**
 * What a call's details show as its input: the tool's input, or else the
 * title as the CLI wrote it when the row shortened its paths (a command's
 * input is often its title). Empty when there is nothing more to show.
 */
fun toolRaw(t: ToolCall, cwd: String, home: String, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String {
    if (t.input.isNotEmpty() && t.input != t.title) return t.input
    if (t.title == "ToolCall" || t.title == "CommandExecution" ||
        t.title.isEmpty() && (t.itemType == "ToolCall" || t.itemType == "CommandExecution")) return ""
    val asIs = if (t.kind == ToolKind.TOOL_KIND_FILE_CHANGE) copy(UiMessages.toolsEditFile(path = t.title)) else t.title.ifEmpty { t.itemType }
    return if (asIs != toolTitle(t, cwd, home, copy = copy)) t.title.ifEmpty { t.itemType } else ""
}

/** "运行了 3 条命令" and the like, for a folded run. */
fun toolsSummary(calls: List<ToolCall>, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String = when {
    calls.all { it.kind == ToolKind.TOOL_KIND_COMMAND } -> copy(UiMessages.toolsCompletedCommands(count = calls.size))
    calls.all { it.kind == ToolKind.TOOL_KIND_FILE_CHANGE } -> copy(UiMessages.toolsCompletedFiles(count = calls.size))
    else -> copy(UiMessages.toolsCompletedCalls(count = calls.size))
} + calls.sumOf { it.imagesCount }.let { if (it > 0) copy(UiMessages.imageScreenshots(count = it)) else "" }

/** Canceled, or answered in the CLI's own prompt: the call that follows shows what happened. */
private fun recorded(d: ApprovalDecision) =
    d != ApprovalDecision.APPROVAL_DECISION_CANCELED && d != ApprovalDecision.APPROVAL_DECISION_TERMINAL

private fun decisionLine(d: ApprovalDecision, message: String, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText) = decisionText(d).resolve(copy) + if (message.isNotEmpty()) " · " +
    (if (d == ApprovalDecision.APPROVAL_DECISION_ALLOW_ALWAYS) io.github.lewin671.remoteagent.core.approvalAlwaysText(message).resolve(copy) else message) else ""

internal fun noteText(item: ChatItem.Note) = androidx.compose.ui.text.buildAnnotatedString {
    val split = if (item.emphasis.isNotEmpty() && item.text.endsWith(item.emphasis)) item.text.length - item.emphasis.length else item.text.length
    append(item.text.take(split))
    if (split < item.text.length) {
        pushStyle(androidx.compose.ui.text.SpanStyle(fontWeight = platformWeight(FontWeight.SemiBold)))
        append(item.text.substring(split))
        pop()
    }
}

data class ChatSource(val label: String?, val self: Boolean = false)

internal fun chatSource(o: io.github.lewin671.remoteagent.proto.Origin?, state: io.github.lewin671.remoteagent.core.RemoteState, me: String, machine: String, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): ChatSource? =
    o?.let { ChatSource(io.github.lewin671.remoteagent.core.sessionOrigin(it, state, me, machine)?.resolve(copy), it.kind == io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_CLIENT && it.deviceId == me) }

internal fun messageSource(source: ChatSource?, local: Boolean, machine: String, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String? = when {
    source?.self == true -> null
    source?.label != null -> source.label
    local -> copy(UiMessages.sessionOriginTerminal(name = machine))
    else -> null
}

private fun decisionSource(source: ChatSource?, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String =
    if (source?.self == true) "" else " · ${source?.label ?: copy(UiMessages.sessionOriginElsewhere())}"

/** Decisions to show on their tool calls (by the call's seq), and the approvals they came from. */
class ApprovalDecisions(val onCall: Map<Long, Pair<String, Boolean>>, val merged: Set<String>)

/**
 * Which decided approvals are shown on their tool call instead of a line
 * of their own, exactly as approvalsOnCalls in web/src/model.ts. Only the
 * daemon's digests count (ApprovalRequested.input_digest against
 * ToolCall.input_digest, the same computation on the daemon), never one
 * worked out here: the tool and digest must match exactly one
 * request (including pending requests in the count), which has a decision, and exactly one call, and the call must come after the request.
 * Codex gives no digest, so its approvals keep their
 * own line; so do parallel or repeated calls that cannot be told apart.
 */
fun approvalDecisions(events: List<Event>, d: Derived, who: ((io.github.lewin671.remoteagent.proto.Origin?) -> ChatSource?)? = null, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): ApprovalDecisions {
    val ids = io.github.lewin671.remoteagent.core.approvalCallIDs(events, d)
    val onCall = ids.mapValues { (_, id) ->
        val (decision, message) = d.resolved.getValue(id)
        (decisionLine(decision, message, copy = copy) + if (who != null) decisionSource(who(d.resolvedBy[id]), copy = copy) else "") to (decision != ApprovalDecision.APPROVAL_DECISION_DENY && decision != ApprovalDecision.APPROVAL_DECISION_TIMEOUT)
    }
    val merged = ids.values.toSet()
    return ApprovalDecisions(onCall, merged)
}

/**
 * The chat's rows. Runs of two or more completed tool calls fold into one
 * line. A decided approval is a line where it was asked, unless
 * approvalDecisions ties it to its call, which then carries the decision.
 */
fun buildChat(
    events: List<Event>,
    d: Derived,
    machine: String,
    cwd: String = "",
    home: String = "",
    textOf: (String) -> String? = { null },
    /** Names who caused an event (Event.origin); see originLabel. */
    who: (io.github.lewin671.remoteagent.proto.Origin?) -> ChatSource? = { null },
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText,
    sharedFilesSupported: Boolean = false,
): List<ChatItem> {
    val out = mutableListOf<ChatItem>()
    val run = mutableListOf<ToolCall>()
    val runSeqs = mutableListOf<Long>()
    var runKey = ""
    val decisions = approvalDecisions(events, d, who, copy = copy)
    fun flushRun() {
        when (run.size) {
            0 -> {}
            1 -> out += ChatItem.Tool(runKey, run[0], seq = runSeqs[0])
            else -> out += ChatItem.Tools(runKey, run.toList(), runSeqs.toList())
        }
        run.clear()
        runSeqs.clear()
    }
    fun flush() = flushRun()
    // Messages that never went out, by the seq that says so.
    val unsentAt = d.unsent.entries.groupBy({ it.value }, { it.key })
    // A started tool keeps its place: its call, once it comes, is shown
    // where it started (the same row, updated), not where it completed.
    val callAt = HashMap<Long, Event>()
    val placed = HashSet<Long>()
    for ((startSeq, callSeq) in d.completedAt) {
        events.getOrNull((callSeq - 1).toInt())?.takeIf { it.seq == callSeq }?.let {
            callAt[startSeq] = it
            placed += callSeq
        }
    }
    val running = d.tools.associateBy { it.seq }
    val startedTools = HashMap<Long, io.github.lewin671.remoteagent.core.RunningTool>()
    for (e in events) if (e.kindCase == Event.KindCase.TOOL_STARTED) {
        val t = e.toolStarted
        startedTools[e.seq] = io.github.lewin671.remoteagent.core.RunningTool(t.title, t.callId, eventTime(e), t.kind, t.itemType, e.seq, t.input, t.inputDigest)
    }
    fun addCall(k: String, callEvent: Event, background: Boolean) {
        val t = callEvent.toolCall
        if (sharedFilesSupported && t.sharedFilesCount > 0) {
            flushRun()
            out += ChatItem.SharedFiles(k, callEvent.seq, t.sharedFilesList)
            return
        }
        val decided = decisions.onCall[callEvent.seq]
        if (decided != null) {
            // Kept out of a fold so that its decision stays in sight.
            flushRun()
            // The approval is said next to it; whether it went to the background still shows.
            out += ChatItem.Tool(k, t, decided.first, decided.second, background = background, seq = callEvent.seq)
        } else if (background) {
            flushRun()
            out += ChatItem.Tool(k, t, background = true, seq = callEvent.seq)
        } else if (toolOk(t)) {
            if (run.isEmpty()) runKey = k
            run += t
            runSeqs += callEvent.seq
        } else {
            flushRun()
            out += ChatItem.Tool(k, t, seq = callEvent.seq)
        }
    }
    for (e in events) {
        val k = "e${e.seq}"
        if (io.github.lewin671.remoteagent.core.isSkeleton(e)) {
            flush()
            out += ChatItem.Agent(k, copy(UiMessages.sessionSkeleton()))
            continue
        }
        unsentAt[e.seq]?.let { ids ->
            flush()
            for (id in ids) out += ChatItem.Unsent("u$id", id, textOf(id))
        }
        when (e.kindCase) {
            Event.KindCase.TOOL_STARTED -> {
                val c = callAt[e.seq]
                if (c != null) {
                    addCall(k, c, io.github.lewin671.remoteagent.core.wentToBackground(c.toolCall))
                } else {
                    val r = running[e.seq]
                    if (r != null || e.seq in d.unfinished) {
                        flush()
                        out += ChatItem.Running(k, r ?: startedTools.getValue(e.seq), unfinished = r == null)
                    }
                }
            }
            Event.KindCase.TOOL_CALL -> if (e.seq !in placed) addCall(k, e, io.github.lewin671.remoteagent.core.wentToBackground(e.toolCall))
            Event.KindCase.USER_MESSAGE -> {
                flush()
                val imgs = e.userMessage.imagesList
                out += ChatItem.User(k, io.github.lewin671.remoteagent.core.imageText(io.github.lewin671.remoteagent.core.withoutFileLines(e.userMessage.text, e.userMessage.filesList), imgs).resolve(copy), e.seq in d.typedLocally, eventTime(e), scheduledMessageCaption(e, copy = copy) ?: messageSource(who(e.originOrNull()), e.seq in d.typedLocally, machine, copy = copy), imgs, e.seq, e.userMessage.filesList, e.origin.scheduledTaskId.isNotEmpty())
            }
            Event.KindCase.SCHEDULED_NOTIFICATION -> {
                flush()
                out += ChatItem.Note(k, copy(UiMessages.sessionNotification(text = e.scheduledNotification.text)), Lucide.Check)
            }
            Event.KindCase.AGENT_MESSAGE -> if (e.agentMessage.text.isNotBlank()) {
                flush()
                out += ChatItem.Agent(k, e.agentMessage.text)
            }
            Event.KindCase.QUESTION_ASKED -> if (e.questionAsked.async) {
                flush()
                out += ChatItem.AsyncQuestion(k, e.questionAsked.questionsList)
            }
            Event.KindCase.REASONING -> if (e.reasoning.text.isNotBlank()) {
                flush()
                out += ChatItem.Thought(k, e.reasoning.text)
            }
            Event.KindCase.APPROVAL_REQUESTED -> {
                // A decided request is a line where it was asked, unless its
                // call carries the decision instead.
                val a = e.approvalRequested
                val r = d.resolved[a.approvalId] ?: continue
                if (a.approvalId in decisions.merged || !recorded(r.first)) continue
                flush()
                val ok = r.first == ApprovalDecision.APPROVAL_DECISION_ALLOW || r.first == ApprovalDecision.APPROVAL_DECISION_ALLOW_ALWAYS
                val what = shortenPaths(a.command.ifEmpty { a.toolName.ifEmpty { a.description } }, cwd, home)
                val by = decisionSource(who(d.resolvedBy[a.approvalId]), copy = copy)
                out += ChatItem.Note(k, what, if (ok) Lucide.Check else Lucide.X, if (ok) 1 else 2, decisionLine(r.first, r.second, copy = copy) + by)
            }
            Event.KindCase.QUESTION_ANSWERED -> {
                flush()
                val answers = e.questionAnswered.answersList.flatMap { it.answersList }
                val by = decisionSource(who(e.originOrNull()), copy = copy)
                out += ChatItem.Note(k, (if (answers.isEmpty()) copy(UiMessages.dockAndroidAnswered()) else copy(UiMessages.dockAndroidAnsweredValues(answers = (answers.joinToString("、")).toString()))) + by, Lucide.Check, 1)
            }
            Event.KindCase.IMAGE_GENERATED -> if (e.imageGenerated.imagesCount > 0) {
                flush()
                out += ChatItem.Generated(k, e.seq, e.imageGenerated.revisedPrompt, e.imageGenerated.imagesList)
            }
            Event.KindCase.PLAN_PROPOSED -> {
                flush()
                out += ChatItem.Plan(k, e.planProposed.text)
            }
            Event.KindCase.TURN_ENDED -> {
                flush()
                val t = e.turnEnded
                if (t.outcome == TurnOutcome.TURN_OUTCOME_ABORTED)
                    out += ChatItem.Note("$k-interrupted", (who(e.originOrNull())?.takeUnless { it.self }?.label?.let { copy(UiMessages.sessionAndroidInterruptedBy(it)) } ?: copy(UiMessages.sessionInterrupted())))
                if (t.durationMs > 0) out += ChatItem.TurnEnd(k, copy(UiMessages.sessionTurnDuration(duration = (duration(t.durationMs, copy = copy)).toString())))
            }
            Event.KindCase.SETTINGS_CHANGED -> {
                val c = e.settingsChanged
                val by = who(e.originOrNull())?.takeUnless { it.self }?.label
                var text = when {
                    c.compacted -> copy(UiMessages.sessionCompacted())
                    c.model.isNotEmpty() -> (by?.let { copy(UiMessages.sessionModelChangedBy(origin = it, value = c.model)) } ?: copy(UiMessages.sessionModelChanged(value = c.model))) + if (c.effort.isNotEmpty()) " · ${c.effort}" else ""
                    else -> modeOf(c.mode)?.let { (by?.let { b -> copy(UiMessages.sessionModeChangedBy(origin = b, value = copy(io.github.lewin671.remoteagent.core.sessionMode(it)))) } ?: copy(UiMessages.sessionModeChanged(value = copy(io.github.lewin671.remoteagent.core.sessionMode(it))))) }
                }
                if (text != null) {
                    flush()
                    if (c.compacted) {
                        val usage = d.compactions[e.seq]
                        if (usage?.before != null && usage.after != null) text = copy(UiMessages.sessionAndroidCompactedUsage(after = usage.after.toString(), before = usage.before.toString()))
                    }
                    out += ChatItem.Note(k, text, emphasis = if (!c.compacted && c.model.isEmpty()) modeOf(c.mode)?.let { copy(io.github.lewin671.remoteagent.core.sessionMode(it)) }.orEmpty() else "")
                }
            }
            Event.KindCase.TURN_CONTEXT -> d.terminalChanges[e.seq]?.let {
                flush()
                out += ChatItem.Note(k, copy(UiMessages.sessionAndroidTerminalChanges(changes = (terminalChangesText(it, copy)).toString(), name = machine)))
            }
            Event.KindCase.SESSION_STATE -> if (e.sessionState.status == SessionStatus.SESSION_STATUS_EXITED) {
                flush()
                out += ChatItem.TurnEnd(k, copy(UiMessages.sessionAndroidEndedAt(time = (io.github.lewin671.remoteagent.core.sessionDateTime(eventTime(e)).resolve(copy)).toString())))
            }
            Event.KindCase.SESSION_FORKED -> {
                flush()
                out += ChatItem.Fork(k, io.github.lewin671.remoteagent.core.sessionDateTime(eventTime(e)).resolve(copy), e.sessionForked.sourceSessionId, e.sessionForked.sourceTitle)
            }
            else -> {}
        }
    }
    flush()
    return out
}

fun duration(ms: Long, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String {
    val s = ms / 1000
    return when {
        s < 60 -> copy(UiMessages.timeDurationSeconds(count = s.toInt()))
        s < 3600 -> copy(UiMessages.timeDurationMinutesSeconds(minutes = (s / 60).toInt(), seconds = (s % 60).toInt()))
        else -> copy(UiMessages.timeDurationHoursMinutes(hours = (s / 3600).toInt(), minutes = (s % 3600 / 60).toInt()))
    }
}

/** How far the chat keeps from the screen's sides (design-system §5): 20. */
val ChatSide = 20.dp

/**
 * A message of the user's, on the right: the bubble colour, radius 22, 17
 * text, at most 84% wide; [caption] under it in 13 says where it came from
 * or how sending went, [captionIcon] before it and [links] after it.
 */
enum class CaptionAction { RESTORE, RETRY, DISMISS, TERMINAL }
data class CaptionLink(val action: CaptionAction, val label: String, val onClick: () -> Unit)
private data class CaptionLinkLabel(val action: CaptionAction, val label: String)

@Composable
fun MeBubble(
    text: String?,
    caption: String? = null,
    captionColor: Color? = null,
    captionIcon: ImageVector? = null,
    failed: Boolean = false,
    links: List<CaptionLink> = emptyList(),
    padded: Boolean = true,
    top: (@Composable () -> Unit)? = null,
    foldScheduled: Boolean = false,
    /** The status line changes while it is shown (a message being sent): its words cross-fade and it closes when it goes. */
    animated: Boolean = false,
    /** This replaces the message just sent from here, whose status line it no longer has: the room it took closes. */
    settling: Boolean = false,
) {
    val p = pal
    var expanded by remember(text) { mutableStateOf(false) }
    var long by remember(text) { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth().padding(horizontal = if (padded) ChatSide else 0.dp, vertical = 8.dp), horizontalAlignment = Alignment.End) {
        top?.invoke()
        if (text != null) Box(Modifier.fillMaxWidth(0.84f), contentAlignment = Alignment.CenterEnd) {
            val shape = Radii.R22
            Box(
                Modifier.clip(shape).background(if (failed) p.fill2 else p.bubble)
                    .then(if (failed) Modifier.border(1.5.dp, p.red, shape) else Modifier).padding(horizontal = 16.dp, vertical = 10.dp),
            ) {
                androidx.compose.foundation.text.selection.SelectionContainer { Text(text, style = T.s17, color = p.ink, maxLines = if (foldScheduled && !expanded) 6 else Int.MAX_VALUE, overflow = TextOverflow.Ellipsis, onTextLayout = { if (!expanded) long = it.hasVisualOverflow }) }
            }
        }
        val shown = caption != null || links.isNotEmpty()
        if (animated) {
            // The words of the line cross-fade (120 ms) and its room opens or closes (200 ms).
            val state = if (shown) CaptionState(caption, captionColor, captionIcon, links.map { CaptionLinkLabel(it.action, it.label) }) else null
            AnimatedContent(
                state,
                contentAlignment = Alignment.TopEnd,
                transitionSpec = {
                    (fadeIn(Motion.fadeInSpec(Motion.FAST)) togetherWith fadeOut(Motion.fadeOutSpec(Motion.FAST))) using
                        SizeTransform(clip = true) { _, _ -> Motion.enter(Motion.BASE) }
                },
                label = "status-line",
            ) { st ->
                if (st != null) CaptionRow(st.caption, st.color, st.icon, st.links.map { label -> CaptionLink(label.action, label.label, links.firstOrNull { it.action == label.action }?.onClick ?: {}) }, foldScheduled, long, expanded) { expanded = !expanded }
            }
        } else if (shown) CaptionRow(caption, captionColor, captionIcon, links, foldScheduled, long, expanded) { expanded = !expanded }
        else if (settling) {
            // The line the sent message had shows as empty room that closes, so the bubble does not jump.
            val room = remember { Animatable(1f) }
            LaunchedEffect(Unit) { room.animateTo(0f, Motion.enter(Motion.BASE)) }
            val px = with(LocalDensity.current) { (6.dp + T.s13.lineHeight.toDp()).toPx() }
            Spacer(Modifier.layout { m, c -> val pl = m.measure(c); layout(pl.width, (px * room.value).roundToInt()) {} })
        }
    }
}

/** What a status line says, without what it does: the words, their colour, the icon before them, and the labels of its links. */
private data class CaptionState(val caption: String?, val color: Color?, val icon: ImageVector?, val links: List<CaptionLinkLabel>)

@Composable
private fun CaptionRow(
    caption: String?,
    captionColor: Color?,
    captionIcon: ImageVector?,
    links: List<CaptionLink>,
    foldScheduled: Boolean,
    long: Boolean,
    expanded: Boolean,
    onToggle: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    Row(Modifier.padding(top = 6.dp, end = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        if (captionIcon != null) {
            Mark(captionIcon, captionColor ?: p.ink2, iconSize(T.s13))
            Spacer(Modifier.width(4.dp))
        }
        if (caption != null) Text(caption + if (foldScheduled && long) (" · " + if (expanded) copy(UiMessages.markdownCollapse()) else copy(UiMessages.markdownExpand())) else "", style = T.s13, color = captionColor ?: p.ink2, textAlign = TextAlign.End, modifier = Modifier.weight(1f, fill = false).then(if (foldScheduled && long) Modifier.heightIn(min = 44.dp).clickable(onClick = onToggle) else Modifier))
        for (link in links) Text(link.label, style = T.s13, color = p.blue, modifier = Modifier.padding(start = 12.dp).heightIn(min = 32.dp).clickable(onClick = link.onClick).wrapContentHeight())
    }
}

/** A thin line with a note in the middle, 13 ink2: a turn's time and tokens, where the session ended. */
@Composable
fun SepLine(text: String) {
    val p = pal
    Row(Modifier.fillMaxWidth().padding(horizontal = ChatSide, vertical = 14.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.weight(1f).height(0.5.dp).background(p.sep))
        Text(text, style = T.s13, color = p.ink2, modifier = Modifier.padding(horizontal = 10.dp))
        Box(Modifier.weight(1f).height(0.5.dp).background(p.sep))
    }
}

/**
 * The line where a forked session leaves its source, drawn as [SepLine]:
 * the time, 分叉自, and the source's title on one line, cut short when long.
 * The title opens the source when this client has it; else it is plain text.
 */
@Composable
fun ForkLine(time: String, title: String, onOpen: (() -> Unit)?) {
    val copy = uiText()
    val p = pal
    androidx.compose.foundation.layout.BoxWithConstraints(Modifier.fillMaxWidth().padding(horizontal = ChatSide)) {
        // The rules give way to a long title, down to a stub at each side.
        val room = (maxWidth - 32.dp).coerceAtLeast(0.dp)
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.weight(1f).height(0.5.dp).background(p.sep))
            Row(Modifier.widthIn(max = room).padding(horizontal = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(copy(UiMessages.sessionAndroidForkPrefix(time = time)), style = T.s13, color = p.ink2, maxLines = 1, softWrap = false)
                Spacer(Modifier.width(4.dp))
                Row(
                    Modifier.weight(1f, fill = false).then(if (onOpen != null) Modifier.tap(onClick = onOpen) else Modifier).padding(vertical = 12.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text(title, style = T.s13, color = if (onOpen != null) p.blue else p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                    if (onOpen != null) {
                        Spacer(Modifier.width(4.dp))
                        Arrow(p.blue, T.s13)
                    }
                }
            }
            Box(Modifier.weight(1f).height(0.5.dp).background(p.sep))
        }
    }
}

/** A tool's line (design-system §7 工具执行): a quiet mark in ink3, what it did in mono 13, and a note at the right; all 13 ink2. */
@Composable
fun ToolLine(mark: ImageVector, markColor: Color, title: String, modifier: Modifier = Modifier, meta: String = "", metaColor: Color? = null, onClick: (() -> Unit)? = null, bold: Boolean = false, trailing: (@Composable () -> Unit)? = null) {
    val p = pal
    Row(
        modifier.fillMaxWidth().then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier).padding(vertical = 5.dp).heightIn(min = 20.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Mark(mark, markColor, 15.dp)
        if (bold) Text(title, style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        else Text(title, style = T.mono13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        if (meta.isNotEmpty()) Text(meta, style = T.s13, color = metaColor ?: p.ink2, maxLines = 1)
        trailing?.invoke()
    }
}

@Composable
fun ChatItemView(item: ChatItem, agent: String, cwd: String = "", home: String = "", settling: Boolean = false, onPutBack: ((requestId: String, text: String) -> Unit)? = null) {
    val copy = uiText()
    val p = pal
    val chat = LocalChat.current
    when (item) {
        is ChatItem.QuietScheduled -> {
            var expanded by rememberSaveable(item.key) { mutableStateOf(false) }
            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { expanded = !expanded }.padding(horizontal = ChatSide), verticalAlignment = Alignment.CenterVertically) {
                Text(quietScheduledLabel(item.times, copy = copy), Modifier.weight(1f), style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Arrow(p.ink2, T.s13, down = !expanded, up = expanded)
            }
            if (expanded) item.original.forEach { ChatItemView(it, agent, cwd, home, onPutBack = onPutBack) }
        }
        is ChatItem.User -> MeBubble(
            sentBubbleText(item.text, item.images.isNotEmpty(), item.files.isNotEmpty()),
            caption = item.from,
            foldScheduled = item.scheduled,
            settling = settling,
            top = if (item.images.isNotEmpty() || item.files.isNotEmpty()) ({ Column {
                if (item.images.isNotEmpty()) ImageRow(item.images, { i -> chat?.onImage?.invoke(item.seq, i) }, Modifier.padding(bottom = 4.dp))
                FileChips(item.files)
            } }) else null,
        )
        is ChatItem.Agent -> Markdown(item.text, Modifier.fillMaxWidth().padding(horizontal = ChatSide, vertical = 8.dp), bleed = ChatSide)
        is ChatItem.AsyncQuestion -> SelectionContainer {
            Column(Modifier.fillMaxWidth().padding(horizontal = ChatSide, vertical = 8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                for (q in item.questions) {
                    if (q.header.isNotEmpty()) Text(q.header, style = T.chat.copy(fontWeight = platformWeight(FontWeight.SemiBold)), color = p.ink)
                    Markdown(q.text, bleed = ChatSide)
                    for (o in q.optionsList) Row {
                        Text("•  ", style = T.chat, color = p.ink2)
                        Text(androidx.compose.ui.text.buildAnnotatedString {
                            pushStyle(androidx.compose.ui.text.SpanStyle(fontWeight = platformWeight(FontWeight.SemiBold)))
                            append(o.label)
                            pop()
                            if (o.description.isNotEmpty()) append(" · ${o.description}")
                        }, style = T.chat, color = p.ink)
                    }
                }
            }
        }
        is ChatItem.Thought -> Expandable(copy(UiMessages.sessionAndroidThinking()), Modifier.padding(horizontal = ChatSide)) {
            Text(item.text, style = T.s15, color = p.ink2, modifier = Modifier.padding(bottom = 6.dp))
        }
        is ChatItem.Tool -> Column(Modifier.padding(horizontal = ChatSide)) {
            ExpandableTool(item.key, commandOf(item.call.kind, item.call.title, item.call.input)) {
                if (item.background) BackgroundRow(item.call, cwd, home, item.decision)
                else ToolRow(item.call, cwd, home, item.decision, item.decisionOk)
            }
            // A tool's images sit right under it, e.g. the file Claude Code read.
            if (item.call.imagesCount > 0) ImageRow(item.call.imagesList, { i -> chat?.onImage?.invoke(item.seq, i) }, Modifier.padding(start = 21.dp, bottom = 6.dp))
        }
        is ChatItem.SharedFiles -> if (chat != null) {
            SharedFilesBlock(
                files = item.files,
                sessionId = chat.sid,
                machineName = chat.machineName,
                machineOnline = chat.machineOnline,
                sessionExited = chat.sessionExited,
            )
        }
        is ChatItem.Running -> RunningToolRow(item, cwd, home)
        is ChatItem.Tools -> Column(Modifier.padding(horizontal = ChatSide).animateContentSize(Motion.enter(Motion.BASE))) {
            var open by rememberSaveable(item.key) { mutableStateOf(false) }
            var commands by rememberSaveable(item.key) { mutableStateOf(emptyList<String>()) }
            ToolLine(Lucide.Check, p.ink3, toolsSummary(item.calls, copy = copy), Modifier.heightIn(min = 28.dp), onClick = { open = !open }, bold = true) {
                Arrow(p.ink2, T.s13, down = !open, up = open)
            }
            if (open) Column(Modifier.padding(start = 12.dp)) {
                item.calls.forEachIndexed { ci, c ->
                    androidx.compose.runtime.key(c.callId.ifEmpty { ci.toString() }) {
                        val id = c.callId.ifEmpty { ci.toString() }
                        ExpandableTool("${item.key}/$id", commandOf(c.kind, c.title, c.input),
                            expanded = id in commands, onToggle = { commands = if (id in commands) commands - id else commands + id }) {
                            Text(toolTitle(c, cwd, home, copy = copy).lines().first(), style = T.mono13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis,
                                modifier = Modifier.fillMaxWidth().heightIn(min = 28.dp))
                        }
                    }
                    // Each call's images stay under it when the group is open.
                    if (c.imagesCount > 0) ImageRow(c.imagesList, { i -> chat?.onImage?.invoke(item.callSeqs.getOrElse(ci) { 0L }, i) }, Modifier.padding(start = 21.dp, bottom = 6.dp))
                }
            }
            else {
                // Screenshots stay in sight with the group closed.
                val imgs = item.calls.flatMap { c -> c.imagesList }
                if (imgs.isNotEmpty()) ImageRow(imgs, { i ->
                    var n = i
                    for ((ci, c) in item.calls.withIndex()) {
                        if (n < c.imagesCount) { chat?.onImage?.invoke(item.callSeqs.getOrElse(ci) { 0L }, n); break }
                        n -= c.imagesCount
                    }
                }, Modifier.padding(start = 21.dp, bottom = 6.dp))
            }
        }
        is ChatItem.Generated -> GeneratedCard(item) { i -> chat?.onImage?.invoke(item.seq, i) }
        is ChatItem.Note ->
            if (item.icon != null) ToolLine(item.icon, p.ink3, item.text, Modifier.padding(horizontal = ChatSide), meta = item.meta, metaColor = if (item.tone == 2) p.red else p.ink2)
            else Text(noteText(item), style = T.s13, color = p.ink2, textAlign = TextAlign.Center, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.fillMaxWidth().padding(horizontal = ChatSide, vertical = 10.dp))
        is ChatItem.Plan -> Column(Modifier.padding(horizontal = ChatSide, vertical = 8.dp).fillMaxWidth().clip(Radii.R18).background(p.gbg).padding(14.dp)) {
            Text(copy(UiMessages.modesPlan()), style = T.s13b, color = p.ink)
            var open by rememberSaveable(item.key) { mutableStateOf(false) }
            // The preview does not show all of its six lines: they wrap past the six it is drawn in.
            var cut by remember(item.key) { mutableStateOf(false) }
            Box(Modifier.animateContentSize().padding(top = 4.dp)) {
                if (open) Markdown(item.text)
                else Text(
                    item.text.lines().map { io.github.lewin671.remoteagent.core.plain(it) }.filter { it.isNotBlank() }.take(6).joinToString("\n"),
                    style = ts(13.sp, lineHeight = 1.6f), color = p.ink, maxLines = 6, overflow = TextOverflow.Ellipsis,
                    onTextLayout = { cut = it.hasVisualOverflow },
                )
            }
            // Whatever the preview leaves out can be opened, and what was opened can be closed again.
            if (open || cut || planFolds(item.text, 6)) Box(Modifier.padding(top = 4.dp).clickable { open = !open }) { ArrowText(if (open) copy(UiMessages.markdownCollapse()) else copy(UiMessages.markdownExpand()), p.blue, T.s13b, down = !open, up = open) }
        }
        is ChatItem.Unsent -> MeBubble(
            item.text ?: copy(UiMessages.queueAndroidMessage()), caption = copy(UiMessages.sessionEndedSendFailed()), captionColor = p.red, failed = true,
            links = if (item.text != null && onPutBack != null) listOf(CaptionLink(CaptionAction.RESTORE, copy(UiMessages.composerRestore())) { onPutBack(item.requestId, item.text) }) else emptyList(),
        )
        is ChatItem.TurnEnd -> SepLine(item.text)
        is ChatItem.Fork -> {
            ForkLine(item.time, forkSource(item, chat?.sessions.orEmpty(), copy = copy), if (chat != null && item.sourceId.isNotEmpty()) ({ chat.onSession(item.sourceId) }) else null)
        }
    }
}

/**
 * Whether a plan has more lines than its preview takes. A plan of fewer
 * lines may still be cut where they wrap, which only its layout knows.
 */
fun planFolds(text: String, lines: Int): Boolean = text.lines().size > lines

/** A line that opens what is under it in place: 思考过程 and an arrow down. */
@Composable
fun Expandable(title: String, modifier: Modifier = Modifier, color: Color? = null, content: @Composable () -> Unit) {
    val copy = uiText()
    val p = pal
    var open by rememberSaveable(title) { mutableStateOf(false) }
    Column(modifier.animateContentSize()) {
        Box(Modifier.fillMaxWidth().clickable { open = !open }.padding(vertical = 6.dp)) {
            ArrowText(if (open) copy(UiMessages.sessionAndroidCollapseTitle(title = title)) else title, color ?: p.ink2, T.s13, down = !open, up = open)
        }
        if (open) content()
    }
}

@Composable
private fun ToolRow(t: ToolCall, cwd: String, home: String, decision: String = "", decisionOk: Boolean = true) {
    val copy = uiText()
    val p = pal
    val ok = toolOk(t)
    // A failed call shows its exit code and, when it needed one, its approval: 退出码 7 · 已允许.
    val meta = listOfNotNull(
        if (!ok && t.hasExitCode()) copy(UiMessages.toolsExitCode(code = t.exitCode.toString())) else null,
        decision.takeIf { it.isNotEmpty() },
    ).joinToString(" · ").ifEmpty { if (t.durationMs >= 1000) "${t.durationMs / 1000}s" else "" }
    ToolLine(if (ok) Lucide.Check else Lucide.X, if (ok) p.ink3 else p.ink2, toolTitle(t, cwd, home, copy = copy).lines().first(),
        meta = meta, metaColor = if (!decisionOk) p.red else p.ink2)
}

/**
 * A call that went on in the background: its event is its start, and
 * nothing reliable says when it ends, so it is never shown as done.
 * Worded as on the web client.
 */
@Composable
private fun BackgroundRow(t: ToolCall, cwd: String, home: String, decision: String = "") {
    val copy = uiText()
    val p = pal
    val title = toolTitle(t, cwd, home, copy = copy).lines().first()
    val generic = (t.title.isEmpty() || t.title == "ToolCall" || t.title == "CommandExecution") && (t.itemType == "ToolCall" || t.itemType == "CommandExecution")
    ToolLine(Lucide.LoaderCircle, p.ink3, if (generic) copy(UiMessages.toolsBackground()) else title, meta = listOf(if (generic) "" else copy(UiMessages.toolsBackground()), decision).filter { it.isNotEmpty() }.joinToString(" · "))
}

/** Ordinary messages append without rebuilding old rows. State-changing events use the full builder. */
class ChatProjection {
    private var previous: List<Event> = emptyList()
    private var items: List<ChatItem> = emptyList()
    private var context: List<Any>? = null
    fun update(events: List<Event>, d: Derived, machine: String, cwd: String = "", home: String = "", agent: Agent = DEFAULT_AGENT, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText, textOf: (String) -> String? = { null }, who: (io.github.lewin671.remoteagent.proto.Origin?) -> ChatSource? = { null }, sharedFilesSupported: Boolean = false): List<ChatItem> {
        val nextContext = listOf(machine, cwd, home, agent, copy, sharedFilesSupported)
        val append = context == nextContext && previous.isNotEmpty() && events.size > previous.size && io.github.lewin671.remoteagent.core.unchangedPrefix(events, previous)
        val tail = if (append) events.subList(previous.size, events.size) else emptyList()
        val messages = append && tail.all { it.kindCase == Event.KindCase.USER_MESSAGE || it.kindCase == Event.KindCase.AGENT_MESSAGE || it.kindCase == Event.KindCase.REASONING }
        items = if (messages) {
            val added = tail.mapNotNull { e ->
                val key = "e${e.seq}"
                when (e.kindCase) {
                    Event.KindCase.USER_MESSAGE -> ChatItem.User(key, io.github.lewin671.remoteagent.core.imageText(io.github.lewin671.remoteagent.core.withoutFileLines(e.userMessage.text, e.userMessage.filesList), e.userMessage.imagesList).resolve(copy), e.seq in d.typedLocally, eventTime(e), scheduledMessageCaption(e, copy = copy) ?: messageSource(who(e.originOrNull()), e.seq in d.typedLocally, machine, copy = copy), e.userMessage.imagesList, e.seq, e.userMessage.filesList, e.origin.scheduledTaskId.isNotEmpty())
                    Event.KindCase.AGENT_MESSAGE -> e.agentMessage.text.takeIf { it.isNotBlank() }?.let { ChatItem.Agent(key, it) }
                    Event.KindCase.REASONING -> e.reasoning.text.takeIf { it.isNotBlank() }?.let { ChatItem.Thought(key, it) }
                    else -> null
                }
            }
            if (added.isEmpty()) items else (items as io.github.lewin671.remoteagent.core.PagedList<ChatItem>).append(added)
        } else io.github.lewin671.remoteagent.core.PagedList.from(buildChat(events, d, machine, cwd, home, textOf, who, copy = copy, sharedFilesSupported = sharedFilesSupported))
        previous = events
        context = nextContext
        return items
    }
}

internal fun sentBubbleText(text: String, images: Boolean, files: Boolean): String? = text.takeIf { it.isNotBlank() || !images && !files }

private fun terminalChangesText(changes: List<io.github.lewin671.remoteagent.core.TerminalChange>, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String): String =
    io.github.lewin671.remoteagent.core.terminalChangeMessages(changes, copy).joinToString(copy(UiMessages.commonIosSpokenSeparator())) { copy(it) }
