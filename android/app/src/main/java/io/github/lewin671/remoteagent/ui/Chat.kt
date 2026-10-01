package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.border
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.ui.draw.clip
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
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.Derived
import io.github.lewin671.remoteagent.core.shortenPath
import io.github.lewin671.remoteagent.core.shortenPaths
import io.github.lewin671.remoteagent.core.dateTime
import io.github.lewin671.remoteagent.core.decisionText
import io.github.lewin671.remoteagent.core.eventTime
import io.github.lewin671.remoteagent.core.modeOf
import io.github.lewin671.remoteagent.core.originOrNull
import io.github.lewin671.remoteagent.proto.ApprovalDecision
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.proto.TurnOutcome

data class StartPrompt(val text: String, val caption: String, val warning: Boolean = false)

/** A new session's acknowledged prompt until history records it; never resend it here. */
fun pendingStartPrompt(prompt: String?, events: List<Event>, d: Derived, localIds: Set<String>): StartPrompt? {
    if (prompt.isNullOrEmpty() || events.any { it.hasUserMessage() }) return null
    val first = d.inputs.keys.firstOrNull { it !in localIds }
    val status = first?.let { d.inputs[it] }
    val error = first?.let { d.failures[it] }.orEmpty()
    return when (status) {
        io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_CANCELED -> null
        io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_FAILED -> StartPrompt(prompt, "没能发送" + if (error.isEmpty()) "" else "：$error", true)
        io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_NEEDS_TERMINAL -> StartPrompt(prompt, "请到终端确认" + if (error.isEmpty()) "" else "：$error", true)
        io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_UNCONFIRMED -> StartPrompt(prompt, "已送到终端，但没能确认 CLI 收到，不会自动重发", true)
        io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_QUEUED -> StartPrompt(prompt, "已送达电脑，等待输入框就绪")
        else -> StartPrompt(prompt, "已送达电脑")
    }
}

/** What the chat shows, built from the events. */
sealed interface ChatItem {
    val key: String

    /** [from]: who sent it, when the daemon says (v2): a client's name, 本客户端, or the computer's terminal. */
    data class User(
        override val key: String,
        val text: String,
        val local: Boolean,
        val time: Long,
        val from: String? = null,
        val images: List<io.github.lewin671.remoteagent.proto.ImageRef> = emptyList(),
        val seq: Long = 0,
    ) : ChatItem

    /** Images the agent made, as a card of their own. */
    data class Generated(override val key: String, val seq: Long, val prompt: String, val images: List<io.github.lewin671.remoteagent.proto.ImageRef>) : ChatItem
    data class Agent(override val key: String, val text: String) : ChatItem
    data class Thought(override val key: String, val text: String) : ChatItem

    /**
     * One tool call; [decision] says how its approval went, if it needed one.
     * [background]: the call returned but its command went on in the
     * background, so how it ends is not known.
     */
    data class Tool(override val key: String, val call: ToolCall, val decision: String = "", val decisionOk: Boolean = true, val background: Boolean = false, val seq: Long = 0) : ChatItem

    /**
     * A tool that started and has no result yet, where it started. With
     * [unfinished] its turn ended first: it may still run in the background.
     */
    data class Running(override val key: String, val tool: io.github.lewin671.remoteagent.core.RunningTool, val unfinished: Boolean) : ChatItem

    /** Two or more completed tool calls in a row, folded into one line. */
    data class Tools(override val key: String, val calls: List<ToolCall>, val callSeqs: List<Long> = emptyList()) : ChatItem
    /** A line of its own: a centred note, or with [icon] a record like a tool row, [meta] at its right. */
    data class Note(override val key: String, val text: String, val icon: String = "", val tone: Int = 0, val meta: String = "") : ChatItem
    data class Plan(override val key: String, val text: String) : ChatItem

    /** A message the session ended before writing; [text] is known when sent from here. */
    data class Unsent(override val key: String, val requestId: String, val text: String?) : ChatItem
    data class TurnEnd(override val key: String, val text: String) : ChatItem
}

fun toolOk(t: ToolCall) = t.status != "failed" && (!t.hasExitCode() || t.exitCode == 0)

/** How a tool call is named in the chat: paths shortened, as on the web; the details keep them whole. */
fun toolTitle(t: ToolCall, cwd: String, home: String = ""): String = toolTitle(t.kind, t.title, t.itemType, cwd, home)

/** The same for a call still running, so that its row reads the same before and after. */
fun toolTitle(t: io.github.lewin671.remoteagent.core.RunningTool, cwd: String, home: String = ""): String = toolTitle(t.kind, t.title, t.itemType, cwd, home)

private fun toolTitle(kind: ToolKind, title: String, itemType: String, cwd: String, home: String): String {
    // A file edit's title is one path, spaces and all.
    if (kind == ToolKind.TOOL_KIND_FILE_CHANGE) return "编辑 " + shortenPath(title, cwd, home)
    val fallback = when (itemType) {
        "ToolCall", "CommandExecution" -> when (kind) {
            ToolKind.TOOL_KIND_COMMAND -> "命令"
            ToolKind.TOOL_KIND_FILE_CHANGE -> "编辑"
            else -> "工具"
        }
        else -> itemType.ifEmpty { "工具" }
    }
    return shortenPaths(title.takeUnless { it == "ToolCall" || it == "CommandExecution" }.orEmpty().ifEmpty { fallback }, cwd, home)
}

/**
 * What a call's details show as its input: the tool's input, or else the
 * title as the CLI wrote it when the row shortened its paths (a command's
 * input is often its title). Empty when there is nothing more to show.
 */
fun toolRaw(t: ToolCall, cwd: String, home: String): String {
    if (t.input.isNotEmpty() && t.input != t.title) return t.input
    if (t.title == "ToolCall" || t.title == "CommandExecution" ||
        t.title.isEmpty() && (t.itemType == "ToolCall" || t.itemType == "CommandExecution")) return ""
    val asIs = if (t.kind == ToolKind.TOOL_KIND_FILE_CHANGE) "编辑 " + t.title else t.title.ifEmpty { t.itemType }
    return if (asIs != toolTitle(t, cwd, home)) t.title.ifEmpty { t.itemType } else ""
}

/** "运行了 3 条命令" and the like, for a folded run. */
fun toolsSummary(calls: List<ToolCall>): String = when {
    calls.all { it.kind == ToolKind.TOOL_KIND_COMMAND } -> "运行了 ${calls.size} 条命令"
    calls.all { it.kind == ToolKind.TOOL_KIND_FILE_CHANGE } -> "编辑了 ${calls.size} 个文件"
    else -> "完成了 ${calls.size} 个工具调用"
} + calls.sumOf { it.imagesCount }.let { if (it > 0) " · $it 张截图" else "" }

/** Canceled, or answered in the CLI's own prompt: the call that follows shows what happened. */
private fun recorded(d: ApprovalDecision) =
    d != ApprovalDecision.APPROVAL_DECISION_CANCELED && d != ApprovalDecision.APPROVAL_DECISION_TERMINAL

private fun decisionLine(d: ApprovalDecision, message: String) = decisionText(d) + if (message.isNotEmpty()) " · " +
    (if (d == ApprovalDecision.APPROVAL_DECISION_ALLOW_ALWAYS) io.github.lewin671.remoteagent.core.approvalAlwaysText(message) else message) else ""

/** Decisions to show on their tool calls (by the call's seq), and the approvals they came from. */
class ApprovalDecisions(val onCall: Map<Long, Pair<String, Boolean>>, val merged: Set<String>)

/**
 * Which decided approvals are shown on their tool call instead of a line
 * of their own, exactly as approvalsOnCalls in web/src/model.ts. Only the
 * daemon's digests count (ApprovalRequested.input_digest against
 * ToolCall.input_digest, the same computation on the daemon), never one
 * worked out here: the tool and digest must match exactly one decided
 * request and exactly one call, and the call must come after the request.
 * Codex and older daemons give no digest, so their approvals keep their
 * own line; so do parallel or repeated calls that cannot be told apart.
 */
fun approvalDecisions(events: List<Event>, d: Derived): ApprovalDecisions {
    val reqs = LinkedHashMap<String, MutableList<Pair<String, Long>>>()
    val calls = HashMap<String, MutableList<Long>>()
    for (e in events) when (e.kindCase) {
        Event.KindCase.APPROVAL_REQUESTED -> {
            val a = e.approvalRequested
            if (a.approvalId !in d.resolved || a.inputDigest.isEmpty()) continue
            reqs.getOrPut(a.toolName + "\u0000" + a.inputDigest) { mutableListOf() } += a.approvalId to e.seq
        }
        Event.KindCase.TOOL_CALL -> {
            val t = e.toolCall
            if (t.inputDigest.isNotEmpty()) calls.getOrPut(t.itemType + "\u0000" + t.inputDigest) { mutableListOf() } += e.seq
        }
        else -> {}
    }
    val onCall = HashMap<Long, Pair<String, Boolean>>()
    val merged = HashSet<String>()
    for ((key, rs) in reqs) {
        val cs = calls[key].orEmpty()
        if (rs.size != 1 || cs.size != 1 || cs[0] < rs[0].second) continue
        val (decision, message) = d.resolved.getValue(rs[0].first)
        onCall[cs[0]] = decisionLine(decision, message) to (decision != ApprovalDecision.APPROVAL_DECISION_DENY && decision != ApprovalDecision.APPROVAL_DECISION_TIMEOUT)
        merged += rs[0].first
    }
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
    who: (io.github.lewin671.remoteagent.proto.Origin?) -> String? = { null },
): List<ChatItem> {
    val out = mutableListOf<ChatItem>()
    val run = mutableListOf<ToolCall>()
    val runSeqs = mutableListOf<Long>()
    var runKey = ""
    val decisions = approvalDecisions(events, d)
    var turnTokens = 0L
    var lastTotal = 0L
    var lastPct: Int? = null
    // A compaction note waiting for the usage after it: its index and the percent before.
    var compacted: Pair<Int, Int?>? = null
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
        unsentAt[e.seq]?.let { ids ->
            flush()
            for (id in ids) out += ChatItem.Unsent("u$id", id, textOf(id))
        }
        when (e.kindCase) {
            Event.KindCase.TOOL_STARTED -> {
                val c = callAt[e.seq]
                if (c != null) {
                    val started = startedTools[e.seq]
                    addCall(k, c, started?.toBackground == true || io.github.lewin671.remoteagent.core.wentToBackground(c.toolCall))
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
                out += ChatItem.User(k, io.github.lewin671.remoteagent.core.imageText(e.userMessage.text, imgs), e.seq in d.typedLocally, eventTime(e), who(e.originOrNull()), imgs, e.seq)
            }
            Event.KindCase.AGENT_MESSAGE -> if (e.agentMessage.text.isNotBlank()) {
                flush()
                out += ChatItem.Agent(k, e.agentMessage.text)
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
                val by = who(d.resolvedBy[a.approvalId])?.takeIf { it != "本客户端" }
                out += ChatItem.Note(k, what, if (ok) "✓" else "✕", if (ok) 1 else 2, decisionLine(r.first, r.second) + (by?.let { " · $it" } ?: ""))
            }
            Event.KindCase.QUESTION_ANSWERED -> {
                flush()
                val answers = e.questionAnswered.answersList.flatMap { it.answersList }
                val by = who(e.originOrNull())?.takeIf { it != "本客户端" }
                out += ChatItem.Note(k, (if (answers.isEmpty()) "已回答提问" else "已回答：" + answers.joinToString("、")) + (by?.let { " · $it" } ?: ""), "✓", 1)
            }
            Event.KindCase.IMAGE_GENERATED -> if (e.imageGenerated.imagesCount > 0) {
                flush()
                out += ChatItem.Generated(k, e.seq, e.imageGenerated.revisedPrompt, e.imageGenerated.imagesList)
            }
            Event.KindCase.PLAN_PROPOSED -> {
                flush()
                out += ChatItem.Plan(k, e.planProposed.text)
            }
            Event.KindCase.TOKEN_USAGE -> {
                if (lastTotal > 0) turnTokens += (e.tokenUsage.totalTokens - lastTotal).coerceAtLeast(0)
                lastTotal = e.tokenUsage.totalTokens
                val u = e.tokenUsage
                lastPct = if (u.contextWindow > 0) minOf(100, (u.lastTotalTokens * 100 / u.contextWindow).toInt()) else null
                compacted?.let { (i, before) ->
                    val note = out[i] as ChatItem.Note
                    if (before != null && lastPct != null) out[i] = note.copy(text = "上下文已压缩 · $before% → $lastPct%")
                    compacted = null
                }
            }
            Event.KindCase.TURN_STARTED -> turnTokens = 0
            Event.KindCase.TURN_ENDED -> {
                flush()
                val t = e.turnEnded
                val parts = mutableListOf<String>()
                if (t.outcome == TurnOutcome.TURN_OUTCOME_ABORTED) parts += "已中断" + (who(e.originOrNull())?.let { "（$it）" } ?: "")
                if (t.durationMs > 0) parts += duration(t.durationMs)
                if (turnTokens > 0) parts += if (turnTokens >= 1000) "${(turnTokens + 500) / 1000}k tokens" else "$turnTokens tokens"
                if (parts.isNotEmpty()) out += ChatItem.TurnEnd(k, parts.joinToString(" · "))
            }
            Event.KindCase.SETTINGS_CHANGED -> {
                val c = e.settingsChanged
                val by = who(e.originOrNull())?.takeIf { it != "本客户端" }
                val text = when {
                    c.compacted -> "上下文已压缩"
                    c.model.isNotEmpty() -> (by?.let { "$it 将模型切到 " } ?: "已切换到 ") + c.model + if (c.effort.isNotEmpty()) " · ${c.effort}" else ""
                    else -> modeOf(c.mode)?.let { (by?.let { b -> "$b 将模式切到 " } ?: "审批模式切到了 ") + it.text }
                }
                if (text != null) {
                    flush()
                    if (c.compacted) compacted = out.size to lastPct
                    out += ChatItem.Note(k, text)
                }
            }
            Event.KindCase.TURN_CONTEXT -> d.terminalChanges[e.seq]?.let {
                flush()
                out += ChatItem.Note(k, "$machine 终端里$it")
            }
            Event.KindCase.SESSION_STATE -> if (e.sessionState.status == SessionStatus.SESSION_STATUS_EXITED) {
                flush()
                out += ChatItem.TurnEnd(k, dateTime(eventTime(e)) + " 结束")
            }
            else -> {}
        }
    }
    flush()
    return out
}

fun duration(ms: Long): String {
    val s = ms / 1000
    return when {
        s < 60 -> "$s 秒"
        s < 3600 -> "${s / 60} 分 ${s % 60} 秒"
        else -> "${s / 3600} 小时 ${s % 3600 / 60} 分"
    }
}

/**
 * A message of the user's, on the right: soft, 18 dp corners with a small
 * one at the bottom right, at most 80% wide; [caption] under it says where
 * it came from or how sending went, with [links] after it.
 */
@Composable
fun MeBubble(
    text: String?,
    caption: String? = null,
    captionColor: Color? = null,
    failed: Boolean = false,
    links: List<Pair<String, () -> Unit>> = emptyList(),
    padded: Boolean = true,
    top: (@Composable () -> Unit)? = null,
) {
    val p = pal
    Column(Modifier.fillMaxWidth().padding(horizontal = if (padded) 14.dp else 0.dp, vertical = 8.dp), horizontalAlignment = Alignment.End) {
        top?.invoke()
        if (text != null) Box(Modifier.fillMaxWidth(0.8f), contentAlignment = Alignment.CenterEnd) {
            val shape = RoundedCornerShape(18.dp, 18.dp, 5.dp, 18.dp)
            Box(
                Modifier.clip(shape).background(if (failed) p.chip else p.soft)
                    .then(if (failed) Modifier.border(1.5.dp, p.red, shape) else Modifier).padding(horizontal = 12.dp, vertical = 8.dp),
            ) {
                androidx.compose.foundation.text.selection.SelectionContainer { Text(text, style = T.body, color = p.fg) }
            }
        }
        if (caption != null || links.isNotEmpty()) Row(Modifier.padding(top = 2.dp), verticalAlignment = Alignment.CenterVertically) {
            if (caption != null) Text(caption, style = T.meta, color = captionColor ?: p.muted, textAlign = TextAlign.End, modifier = Modifier.weight(1f, fill = false))
            for ((label, go) in links) Text(label, style = T.metaB, color = p.blue, modifier = Modifier.padding(start = 6.dp).clickable(onClick = go))
        }
    }
}

/** A thin line with a note in the middle: a turn's time and tokens, a date. */
@Composable
fun SepLine(text: String) {
    val p = pal
    Row(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 12.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.weight(1f).height(1.dp).background(p.line))
        Text(text, style = T.meta, color = p.muted, modifier = Modifier.padding(horizontal = 8.dp))
        Box(Modifier.weight(1f).height(1.dp).background(p.line))
    }
}

/** A tool's line: a quiet mark, what it did in mono, and a note at the right. */
@Composable
fun ToolLine(mark: String, markColor: Color, title: String, modifier: Modifier = Modifier, meta: String = "", metaColor: Color? = null, onClick: (() -> Unit)? = null, bold: Boolean = false, trailing: (@Composable () -> Unit)? = null) {
    val p = pal
    Row(
        modifier.fillMaxWidth().then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier).padding(vertical = 4.dp).heightIn(min = 20.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Mark(mark, markColor)
        if (bold) Text(title, style = ts(13.sp, FontWeight.Medium), color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        else Text(title, style = T.mono, color = p.fg2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        if (meta.isNotEmpty()) Text(meta, style = T.meta, color = metaColor ?: p.muted, maxLines = 1)
        trailing?.invoke()
    }
}

@Composable
fun ChatItemView(item: ChatItem, agent: String, cwd: String = "", home: String = "", onPutBack: ((requestId: String, text: String) -> Unit)? = null) {
    val p = pal
    val chat = LocalChat.current
    when (item) {
        is ChatItem.User -> MeBubble(
            item.text.takeIf { it.isNotBlank() || item.images.isEmpty() },
            caption = item.from ?: if (item.local) "在终端里输入" else null,
            top = if (item.images.isNotEmpty()) ({ ImageRow(item.images, { i -> chat?.onImage?.invoke(item.seq, i) }, Modifier.padding(bottom = 4.dp)) }) else null,
        )
        is ChatItem.Agent -> Markdown(item.text, Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 8.dp))
        is ChatItem.Thought -> Expandable("思考过程", Modifier.padding(horizontal = 14.dp)) {
            Text(item.text, style = T.sm, color = p.muted, modifier = Modifier.padding(bottom = 6.dp))
        }
        is ChatItem.Tool -> Column(Modifier.padding(horizontal = 14.dp)) {
            if (item.background) BackgroundRow(item.call, cwd, home, item.decision)
            else ToolRow(item.call, cwd, home, item.decision, item.decisionOk)
            // A tool's images sit right under it, e.g. the file Claude Code read.
            if (item.call.imagesCount > 0) ImageRow(item.call.imagesList, { i -> chat?.onImage?.invoke(item.seq, i) }, Modifier.padding(start = 21.dp, bottom = 6.dp))
        }
        is ChatItem.Running -> RunningToolRow(item, cwd, home)
        is ChatItem.Tools -> Column(Modifier.padding(horizontal = 14.dp)) {
            var open by rememberSaveable(item.key) { mutableStateOf(false) }
            ToolLine("✓", p.muted, toolsSummary(item.calls), Modifier.heightIn(min = 28.dp), onClick = { open = !open }, bold = true) {
                Arrow(p.muted, T.sm, down = !open, up = open)
            }
            if (open) Column(Modifier.padding(start = 12.dp)) {
                item.calls.forEachIndexed { ci, c ->
                    Text(toolTitle(c, cwd, home).lines().first(), style = T.mono, color = p.fg2, maxLines = 1, overflow = TextOverflow.Ellipsis)
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
            if (item.icon.isNotEmpty()) ToolLine(item.icon, p.muted, item.text, Modifier.padding(horizontal = 14.dp), meta = item.meta, metaColor = if (item.tone == 2) p.red else p.muted)
            else Text(item.text, style = T.meta, color = p.muted, textAlign = TextAlign.Center, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 10.dp))
        is ChatItem.Plan -> Column(Modifier.padding(horizontal = 14.dp, vertical = 8.dp).fillMaxWidth().clip(Radii.MD).background(p.soft).padding(12.dp)) {
            Text("计划", style = T.smB, color = p.fg2)
            var open by rememberSaveable(item.key) { mutableStateOf(false) }
            Box(Modifier.animateContentSize().padding(top = 4.dp)) {
                if (open) Markdown(item.text)
                else Text(
                    item.text.lines().map { io.github.lewin671.remoteagent.core.plain(it) }.filter { it.isNotBlank() }.take(6).joinToString("\n"),
                    style = ts(13.sp, lineHeight = 1.6f), color = p.fg, maxLines = 6, overflow = TextOverflow.Ellipsis,
                )
            }
            if (item.text.lines().size > 6) Box(Modifier.padding(top = 4.dp).clickable { open = !open }) { ArrowText(if (open) "收起" else "展开全文", p.blue, T.smB, down = !open, up = open) }
        }
        is ChatItem.Unsent -> MeBubble(
            item.text ?: "一条排队的消息", caption = "会话结束，没有发出", captionColor = p.orangeInk,
            links = if (item.text != null && onPutBack != null) listOf("放回输入框" to { onPutBack(item.requestId, item.text) }) else emptyList(),
        )
        is ChatItem.TurnEnd -> SepLine(item.text)
    }
}

/** A line that opens what is under it in place: 思考过程 ⌄. */
@Composable
fun Expandable(title: String, modifier: Modifier = Modifier, color: Color? = null, content: @Composable () -> Unit) {
    val p = pal
    var open by rememberSaveable(title) { mutableStateOf(false) }
    Column(modifier.animateContentSize()) {
        Box(Modifier.fillMaxWidth().clickable { open = !open }.padding(vertical = 6.dp)) {
            ArrowText(if (open) "收起$title" else title, color ?: p.muted, T.sm, down = !open, up = open)
        }
        if (open) content()
    }
}

@Composable
private fun ToolRow(t: ToolCall, cwd: String, home: String, decision: String = "", decisionOk: Boolean = true) {
    val p = pal
    val ok = toolOk(t)
    // A failed call shows its exit code and, when it needed one, its approval: 退出码 7 · 已允许.
    val meta = listOfNotNull(
        if (!ok && t.hasExitCode()) "退出码 ${t.exitCode}" else null,
        decision.takeIf { it.isNotEmpty() },
    ).joinToString(" · ").ifEmpty { if (t.durationMs >= 1000) "${t.durationMs / 1000}s" else "" }
    ToolLine(if (ok) "✓" else "✕", if (ok) p.muted else p.red, toolTitle(t, cwd, home).lines().first(),
        meta = meta, metaColor = if (!ok || !decisionOk) p.red else p.muted)
}

/**
 * A call that went on in the background: its event is its start, and
 * nothing reliable says when it ends, so it is never shown as done.
 * Worded as on the web client.
 */
@Composable
private fun BackgroundRow(t: ToolCall, cwd: String, home: String, decision: String = "") {
    val p = pal
    val title = toolTitle(t, cwd, home).lines().first()
    val generic = (t.title.isEmpty() || t.title == "ToolCall" || t.title == "CommandExecution") && (t.itemType == "ToolCall" || t.itemType == "CommandExecution")
    ToolLine("◌", p.blue, if (generic) "后台运行" else title, meta = if (generic) "" else "后台运行")
}

/** Ordinary messages append without rebuilding old rows. State-changing events use the full builder. */
class ChatProjection {
    private var previous: List<Event> = emptyList()
    private var items: List<ChatItem> = emptyList()
    private var context: List<Any>? = null
    fun update(events: List<Event>, d: Derived, machine: String, cwd: String = "", home: String = "", agent: io.github.lewin671.remoteagent.proto.Agent = io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX, textOf: (String) -> String? = { null }, who: (io.github.lewin671.remoteagent.proto.Origin?) -> String? = { null }): List<ChatItem> {
        val nextContext = listOf(machine, cwd, home, agent)
        val append = context == nextContext && previous.isNotEmpty() && events.size > previous.size && io.github.lewin671.remoteagent.core.unchangedPrefix(events, previous)
        val tail = if (append) events.subList(previous.size, events.size) else emptyList()
        val messages = append && tail.all { it.kindCase == Event.KindCase.USER_MESSAGE || it.kindCase == Event.KindCase.AGENT_MESSAGE || it.kindCase == Event.KindCase.REASONING }
        items = if (messages) {
            val added = tail.mapNotNull { e ->
                val key = "e${e.seq}"
                when (e.kindCase) {
                    Event.KindCase.USER_MESSAGE -> ChatItem.User(key, io.github.lewin671.remoteagent.core.imageText(e.userMessage.text, e.userMessage.imagesList), e.seq in d.typedLocally, eventTime(e), who(e.originOrNull()), e.userMessage.imagesList, e.seq)
                    Event.KindCase.AGENT_MESSAGE -> e.agentMessage.text.takeIf { it.isNotBlank() }?.let { ChatItem.Agent(key, it) }
                    Event.KindCase.REASONING -> e.reasoning.text.takeIf { it.isNotBlank() }?.let { ChatItem.Thought(key, it) }
                    else -> null
                }
            }
            if (added.isEmpty()) items else (items as io.github.lewin671.remoteagent.core.PagedList<ChatItem>).append(added)
        } else io.github.lewin671.remoteagent.core.PagedList.from(buildChat(events, d, machine, cwd, home, textOf, who))
        previous = events
        context = nextContext
        return items
    }
}
