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
import io.github.lewin671.remoteagent.core.inputDigest
import io.github.lewin671.remoteagent.core.shortenPaths
import io.github.lewin671.remoteagent.core.dateTime
import io.github.lewin671.remoteagent.core.decisionText
import io.github.lewin671.remoteagent.core.eventTime
import io.github.lewin671.remoteagent.core.modeOf
import io.github.lewin671.remoteagent.proto.ApprovalDecision
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.proto.TurnOutcome

/** What the chat shows, built from the events. */
sealed interface ChatItem {
    val key: String

    data class User(override val key: String, val text: String, val local: Boolean, val time: Long) : ChatItem
    data class Agent(override val key: String, val text: String) : ChatItem
    data class Thought(override val key: String, val text: String) : ChatItem

    /** One tool call; [decision] says how its approval went, if it needed one. */
    data class Tool(override val key: String, val call: ToolCall, val decision: String = "", val decisionOk: Boolean = true) : ChatItem

    /** Two or more completed tool calls in a row, folded into one line. */
    data class Tools(override val key: String, val calls: List<ToolCall>) : ChatItem
    data class Note(override val key: String, val text: String, val icon: String = "", val tone: Int = 0) : ChatItem
    data class Plan(override val key: String, val text: String) : ChatItem
    data class TurnEnd(override val key: String, val text: String) : ChatItem
}

fun toolOk(t: ToolCall) = t.status != "failed" && (!t.hasExitCode() || t.exitCode == 0)

/** How a tool call is named in the chat: paths shortened, as on the web; the details keep them whole. */
fun toolTitle(t: ToolCall, cwd: String, home: String = ""): String {
    val title = shortenPaths(t.title.ifEmpty { t.itemType }, cwd, home)
    return if (t.kind == ToolKind.TOOL_KIND_FILE_CHANGE) "编辑 $title" else title
}

/** "运行了 3 条命令" and the like, for a folded run. */
fun toolsSummary(calls: List<ToolCall>): String = when {
    calls.all { it.kind == ToolKind.TOOL_KIND_COMMAND } -> "运行了 ${calls.size} 条命令"
    calls.all { it.kind == ToolKind.TOOL_KIND_FILE_CHANGE } -> "编辑了 ${calls.size} 个文件"
    else -> "完成了 ${calls.size} 个工具调用"
}

/** Canceled, or answered in the CLI's own prompt: the call that follows shows what happened. */
private fun recorded(d: ApprovalDecision) =
    d != ApprovalDecision.APPROVAL_DECISION_CANCELED && d != ApprovalDecision.APPROVAL_DECISION_TERMINAL

private fun decisionLine(d: ApprovalDecision, message: String) = decisionText(d) + if (message.isNotEmpty()) "：$message" else ""

/** Decisions to show on their tool calls (by the call's seq), and the approvals they came from. */
class ApprovalDecisions(val onCall: Map<Long, Pair<String, Boolean>>, val merged: Set<String>)

/**
 * Which decided approvals are shown on their tool call instead of a line
 * of their own, exactly as approvalsOnCalls in web/src/model.ts: the call's
 * input_digest equals the digest of the request's input (inputDigest, as
 * the daemon computes it) for the same tool, exactly one decided request
 * and exactly one call share it, and the call comes after the request.
 * Codex and older events have no digest and keep their own line; so do
 * parallel or repeated calls that cannot be told apart.
 */
fun approvalDecisions(events: List<Event>, d: Derived): ApprovalDecisions {
    val reqs = LinkedHashMap<String, MutableList<Pair<String, Long>>>()
    val calls = HashMap<String, MutableList<Long>>()
    for (e in events) when (e.kindCase) {
        Event.KindCase.APPROVAL_REQUESTED -> {
            val a = e.approvalRequested
            if (a.approvalId !in d.resolved) continue
            val digest = inputDigest(a.toolName, a.toolInputJson)
            if (digest.isEmpty()) continue
            reqs.getOrPut(a.toolName + "\u0000" + digest) { mutableListOf() } += a.approvalId to e.seq
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
fun buildChat(events: List<Event>, d: Derived, machine: String, cwd: String = "", home: String = ""): List<ChatItem> {
    val out = mutableListOf<ChatItem>()
    val run = mutableListOf<ToolCall>()
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
            1 -> out += ChatItem.Tool(runKey, run[0])
            else -> out += ChatItem.Tools(runKey, run.toList())
        }
        run.clear()
    }
    fun flush() = flushRun()
    for (e in events) {
        val k = "e${e.seq}"
        when (e.kindCase) {
            Event.KindCase.TOOL_CALL -> {
                val t = e.toolCall
                val decided = decisions.onCall[e.seq]
                if (decided != null) {
                    // Kept out of a fold so that its decision stays in sight.
                    flushRun()
                    out += ChatItem.Tool(k, t, decided.first, decided.second)
                } else if (toolOk(t)) {
                    if (run.isEmpty()) runKey = k
                    run += t
                } else {
                    flushRun()
                    out += ChatItem.Tool(k, t)
                }
            }
            Event.KindCase.USER_MESSAGE -> {
                flush()
                out += ChatItem.User(k, e.userMessage.text, e.seq in d.typedLocally, eventTime(e))
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
                out += ChatItem.Note(k, listOf(what, decisionLine(r.first, r.second)).filter { it.isNotEmpty() }.joinToString("  "), if (ok) "✓" else "✕", if (ok) 1 else 2)
            }
            Event.KindCase.QUESTION_ANSWERED -> {
                flush()
                val answers = e.questionAnswered.answersList.flatMap { it.answersList }
                out += ChatItem.Note(k, if (answers.isEmpty()) "已回答提问" else "已回答：" + answers.joinToString("、"), "✓", 1)
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
                if (t.outcome == TurnOutcome.TURN_OUTCOME_ABORTED) parts += "已中断"
                if (t.durationMs > 0) parts += duration(t.durationMs)
                if (turnTokens > 0) parts += if (turnTokens >= 1000) "${(turnTokens + 500) / 1000}k tokens" else "$turnTokens tokens"
                if (parts.isNotEmpty()) out += ChatItem.TurnEnd(k, parts.joinToString(" · "))
            }
            Event.KindCase.SETTINGS_CHANGED -> {
                val c = e.settingsChanged
                val text = when {
                    c.compacted -> "上下文已压缩"
                    c.model.isNotEmpty() -> "已切换到 ${c.model}" + if (c.effort.isNotEmpty()) " · ${c.effort}" else ""
                    else -> modeOf(c.mode)?.let { "审批模式切到了 ${it.text}" }
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
fun ChatItemView(item: ChatItem, agent: String, cwd: String = "", home: String = "") {
    val p = pal
    when (item) {
        is ChatItem.User -> Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 4.dp), horizontalAlignment = Alignment.End) {
            if (item.local) Text("在终端里输入", style = MaterialTheme.typography.bodySmall, color = p.faint)
            Box(Modifier.widthIn(max = 320.dp).background(p.blueSoft, RoundedCornerShape(16.dp)).padding(horizontal = 12.dp, vertical = 8.dp)) {
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
            ToolRow(item.call, cwd, home, item.decision, item.decisionOk)
        }
        is ChatItem.Tools -> Column(Modifier.padding(horizontal = 16.dp, vertical = 2.dp)) {
            Expandable("✓ " + toolsSummary(item.calls) + "  ▸", "✓ " + toolsSummary(item.calls) + "  ▾") {
                Column(Modifier.padding(start = 12.dp)) { item.calls.forEach { ToolRow(it, cwd, home) } }
            }
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
            if (decision.isNotEmpty()) {
                Spacer(Modifier.width(8.dp))
                Text(decision, style = MaterialTheme.typography.bodySmall, color = if (decisionOk) p.muted else p.red, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            if (!ok && t.hasExitCode()) {
                Spacer(Modifier.width(8.dp))
                Text("退出码 ${t.exitCode}", style = MaterialTheme.typography.bodySmall, color = p.red)
            } else if (t.durationMs >= 1000) {
                Spacer(Modifier.width(8.dp))
                Text("${t.durationMs / 1000}s", style = MaterialTheme.typography.bodySmall, color = p.faint)
            }
        }
        if (open) {
            Column(verticalArrangement = Arrangement.spacedBy(6.dp), modifier = Modifier.padding(bottom = 6.dp)) {
                if (t.input.isNotEmpty() && t.input != t.title) CodeBlock(t.input, maxLines = 30)
                if (out.isNotEmpty()) CodeBlock(out + if (t.outputTruncated) "\n…（已截断）" else "", maxLines = 40)
            }
        } else if (!ok && out.isNotEmpty()) {
            // A failure shows its first line without opening.
            val lines = out.lines()
            CodeBlock(lines.first() + if (lines.size > 1) "\n… 还有 ${lines.size - 1} 行，展开" else "", maxLines = 2, modifier = Modifier.clickable { open = true })
        }
    }
}
