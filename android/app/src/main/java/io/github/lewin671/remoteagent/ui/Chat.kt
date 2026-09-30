package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
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
    data class Note(override val key: String, val text: String, val icon: String = "", val tone: Int = 0) : ChatItem
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
    return shortenPaths(title.ifEmpty { itemType }, cwd, home)
}

/**
 * What a call's details show as its input: the tool's input, or else the
 * title as the CLI wrote it when the row shortened its paths (a command's
 * input is often its title). Empty when there is nothing more to show.
 */
fun toolRaw(t: ToolCall, cwd: String, home: String): String {
    if (t.input.isNotEmpty() && t.input != t.title) return t.input
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

private fun decisionLine(d: ApprovalDecision, message: String) = decisionText(d) + if (message.isNotEmpty()) "：$message" else ""

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
                val ok = r.first == ApprovalDecision.APPROVAL_DECISION_ALLOW
                val what = shortenPaths(a.command.ifEmpty { a.toolName.ifEmpty { a.description } }, cwd, home)
                val by = who(d.resolvedBy[a.approvalId])?.takeIf { it != "本客户端" }
                out += ChatItem.Note(k, listOf(what, decisionLine(r.first, r.second) + (by?.let { " · $it" } ?: "")).filter { it.isNotEmpty() }.joinToString("  "), if (ok) "✓" else "✕", if (ok) 1 else 2)
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
                out += ChatItem.Note(k, dateTime(eventTime(e)) + " 结束")
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

@Composable
fun ChatItemView(item: ChatItem, agent: String, cwd: String = "", home: String = "", onPutBack: ((requestId: String, text: String) -> Unit)? = null) {
    val p = pal
    when (item) {
        is ChatItem.User -> Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 4.dp), horizontalAlignment = Alignment.End) {
            val from = item.from ?: if (item.local) "在终端里输入" else null
            if (from != null) Text(from, style = MaterialTheme.typography.bodySmall, color = p.faint)
            val chat = LocalChat.current
            if (item.images.isNotEmpty()) ImageRow(item.images, { i -> chat?.onImage?.invoke(item.seq, i) }, Modifier.padding(vertical = 4.dp))
            if (item.text.isNotBlank() || item.images.isEmpty()) Box(Modifier.widthIn(max = 320.dp).background(p.blueSoft, RoundedCornerShape(16.dp)).padding(horizontal = 12.dp, vertical = 8.dp)) {
                androidx.compose.foundation.text.selection.SelectionContainer {
                    Text(item.text, style = MaterialTheme.typography.bodyLarge, color = p.fg)
                }
            }
        }
        is ChatItem.Agent -> Markdown(item.text, Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 6.dp))
        is ChatItem.Thought -> Expandable("▸ 思考", "▾ 思考", Modifier.padding(horizontal = 16.dp, vertical = 2.dp)) {
            Text(item.text, style = MaterialTheme.typography.bodyMedium, color = p.muted)
        }
        is ChatItem.Tool -> Column(Modifier.padding(horizontal = 16.dp, vertical = 2.dp)) {
            if (item.background) BackgroundRow(item.call, cwd, home, item.decision)
            else ToolRow(item.call, cwd, home, item.decision, item.decisionOk)
            // A tool's images sit right under it, e.g. the file Claude Code read.
            val chat = LocalChat.current
            if (item.call.imagesCount > 0) ImageRow(item.call.imagesList, { i -> chat?.onImage?.invoke(item.seq, i) }, Modifier.padding(bottom = 4.dp))
        }
        is ChatItem.Running -> RunningToolRow(item, cwd, home)
        is ChatItem.Tools -> Column(Modifier.padding(horizontal = 16.dp, vertical = 2.dp)) {
            var open by rememberSaveable(item.key) { mutableStateOf(false) }
            Text(
                "✓ " + toolsSummary(item.calls) + if (open) "  ▾" else "  ▸",
                Modifier.fillMaxWidth().clickable { open = !open }.padding(vertical = 6.dp),
                style = MaterialTheme.typography.bodyMedium, color = p.muted,
            )
            val chat = LocalChat.current
            if (open) Column(Modifier.padding(start = 12.dp)) {
                item.calls.forEachIndexed { ci, c ->
                    ToolRow(c, cwd, home)
                    // Each call's images stay under it when the group is open.
                    if (c.imagesCount > 0) ImageRow(c.imagesList, { i -> chat?.onImage?.invoke(item.callSeqs.getOrElse(ci) { 0L }, i) }, Modifier.padding(bottom = 4.dp))
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
                }, Modifier.padding(bottom = 4.dp))
            }
        }
        is ChatItem.Generated -> {
            val chat = LocalChat.current
            GeneratedCard(item) { i -> chat?.onImage?.invoke(item.seq, i) }
        }
        is ChatItem.Note -> Row(Modifier.padding(horizontal = 16.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            if (item.icon.isNotEmpty()) {
                Text(item.icon, color = when (item.tone) { 1 -> p.green; 2 -> p.red; else -> p.muted }, style = MaterialTheme.typography.bodyMedium)
                Spacer(Modifier.width(6.dp))
            }
            Text(item.text, style = MaterialTheme.typography.bodyMedium, color = p.muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        is ChatItem.Plan -> Card(Modifier.padding(horizontal = 12.dp, vertical = 6.dp)) {
            Column(Modifier.padding(14.dp)) {
                Text("计划", style = MaterialTheme.typography.titleSmall, color = p.purple)
                var open by rememberSaveable(item.key) { mutableStateOf(false) }
                Box(Modifier.animateContentSize()) {
                    if (open) Markdown(item.text)
                    else Text(
                        item.text.lines().map { io.github.lewin671.remoteagent.core.plain(it) }.filter { it.isNotBlank() }.take(6).joinToString("\n"),
                        style = MaterialTheme.typography.bodyMedium, maxLines = 6, overflow = TextOverflow.Ellipsis,
                    )
                }
                if (item.text.lines().size > 6) Text(if (open) "收起" else "展开全文 ›", color = p.blue, modifier = Modifier.clickable { open = !open }.padding(top = 6.dp))
            }
        }
        is ChatItem.Unsent -> Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 4.dp), horizontalAlignment = Alignment.End) {
            Box(Modifier.widthIn(max = 320.dp).background(p.soft, RoundedCornerShape(16.dp)).padding(horizontal = 12.dp, vertical = 8.dp)) {
                Text(item.text ?: "一条排队的消息", style = MaterialTheme.typography.bodyLarge, color = p.muted)
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("会话结束，没有发出", style = MaterialTheme.typography.bodySmall, color = p.orangeInk)
                if (item.text != null && onPutBack != null) {
                    androidx.compose.material3.TextButton(onClick = { onPutBack(item.requestId, item.text) }) { Text("放回输入框") }
                }
            }
        }
        is ChatItem.TurnEnd -> Text(item.text, Modifier.padding(horizontal = 16.dp, vertical = 4.dp), style = MaterialTheme.typography.bodySmall, color = p.faint)
    }
}

@Composable
fun Expandable(closed: String, opened: String, modifier: Modifier = Modifier, color: Color? = null, content: @Composable () -> Unit) {
    val p = pal
    var open by rememberSaveable(closed) { mutableStateOf(false) }
    Column(modifier.animateContentSize()) {
        Text(
            if (open) opened else closed,
            Modifier.fillMaxWidth().clickable { open = !open }.padding(vertical = 6.dp),
            style = MaterialTheme.typography.bodyMedium, color = color ?: p.muted,
        )
        if (open) content()
    }
}

@Composable
private fun ToolRow(t: ToolCall, cwd: String, home: String, decision: String = "", decisionOk: Boolean = true) {
    val p = pal
    val ok = toolOk(t)
    var open by rememberSaveable(t.callId, t.title) { mutableStateOf(false) }
    val out = t.output.trimEnd('\n')
    Column(Modifier.animateContentSize()) {
        Row(Modifier.fillMaxWidth().clickable { open = !open }.padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(if (ok) "✓" else "✕", color = if (ok) p.green else p.red, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.width(6.dp))
            Text(
                toolTitle(t, cwd, home).lines().first(),
                style = MaterialTheme.typography.bodyMedium, fontFamily = FontFamily.Monospace, fontSize = 13.sp, color = p.fg2,
                maxLines = if (open) 4 else 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false),
            )
            // A failed call shows its exit code and, when it needed one, its approval: 退出码 7 · 已允许.
            val meta = listOfNotNull(
                if (!ok && t.hasExitCode()) "退出码 ${t.exitCode}" else null,
                decision.takeIf { it.isNotEmpty() },
            )
            if (meta.isNotEmpty()) {
                Spacer(Modifier.width(8.dp))
                Text(
                    meta.joinToString(" · "), style = MaterialTheme.typography.bodySmall,
                    color = if (!ok || !decisionOk) p.red else p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis,
                )
            } else if (t.durationMs >= 1000) {
                Spacer(Modifier.width(8.dp))
                Text("${t.durationMs / 1000}s", style = MaterialTheme.typography.bodySmall, color = p.faint)
            }
        }
        if (open) {
            Column(verticalArrangement = Arrangement.spacedBy(6.dp), modifier = Modifier.padding(bottom = 6.dp)) {
                val raw = toolRaw(t, cwd, home)
                if (raw.isNotEmpty()) CodeBlock(raw, maxLines = 30)
                if (out.isNotEmpty()) CodeBlock(out + if (t.outputTruncated) "\n…（已截断）" else "", maxLines = 40)
            }
        } else if (!ok && out.isNotEmpty()) {
            // A failure shows its first line without opening.
            val lines = out.lines()
            CodeBlock(lines.first() + if (lines.size > 1) "\n… 还有 ${lines.size - 1} 行，展开" else "", maxLines = 2, modifier = Modifier.clickable { open = true })
        }
    }
}

/**
 * A call that went on in the background: its event is its start, and
 * nothing reliable says when it ends, so it is never shown as done.
 * Worded as on the web client.
 */
@Composable
private fun BackgroundRow(t: ToolCall, cwd: String, home: String, decision: String = "") {
    val p = pal
    val chat = LocalChat.current
    Column(Modifier.fillMaxWidth().padding(vertical = 4.dp).background(p.soft, RoundedCornerShape(12.dp)).padding(12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("◌", color = p.muted, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.width(8.dp))
            Text(
                io.github.lewin671.remoteagent.core.backgroundTitle(t.kind, t.itemType, chat?.agent ?: io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX) + " · 状态待确认",
                style = MaterialTheme.typography.titleSmall, color = p.fg2, modifier = Modifier.weight(1f),
            )
            if (t.backgroundId.isNotEmpty()) Text(t.backgroundId, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace, color = p.muted)
        }
        Text(toolTitle(t, cwd, home).lines().first(), style = MaterialTheme.typography.bodyMedium, fontFamily = FontFamily.Monospace, maxLines = 2, overflow = TextOverflow.Ellipsis)
        Text(io.github.lewin671.remoteagent.core.backgroundNote(t.background), style = MaterialTheme.typography.bodySmall, color = p.muted)
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (decision.isNotEmpty()) Text(decision, style = MaterialTheme.typography.bodySmall, color = p.muted, modifier = Modifier.weight(1f)) else Spacer(Modifier.weight(1f))
            if (chat != null && chat.terminalOpen && chat.frozenAt == 0L) androidx.compose.material3.TextButton(onClick = chat.onTerminal) { Text("去终端 ›") }
            else Text("终端不可用", style = MaterialTheme.typography.bodySmall, color = p.faint)
        }
    }
}


/** Ordinary messages append without rebuilding old rows. State-changing events use the full builder. */
class ChatProjection {
    private var previous: List<Event> = emptyList()
    private var items: List<ChatItem> = emptyList()
    fun update(events: List<Event>, d: Derived, machine: String, cwd: String = "", home: String = "", textOf: (String) -> String? = { null }, who: (io.github.lewin671.remoteagent.proto.Origin?) -> String? = { null }): List<ChatItem> {
        val append = previous.isNotEmpty() && events.size > previous.size && previous.indices.all { events[it] === previous[it] }
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
            if (added.isEmpty()) items else items + added
        } else buildChat(events, d, machine, cwd, home, textOf, who)
        previous = events
        return items
    }
}
