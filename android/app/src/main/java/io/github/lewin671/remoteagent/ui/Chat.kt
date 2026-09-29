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
    data class Tools(override val key: String, val calls: List<ToolCall>) : ChatItem
    data class Note(override val key: String, val text: String, val icon: String = "", val tone: Int = 0) : ChatItem
    data class Plan(override val key: String, val text: String) : ChatItem
    data class TurnEnd(override val key: String, val text: String) : ChatItem
}

fun buildChat(events: List<Event>, d: Derived, machine: String): List<ChatItem> {
    val out = mutableListOf<ChatItem>()
    val tools = mutableListOf<ToolCall>()
    var toolKey = ""
    val requested = HashMap<String, String>()
    var turnTokens = 0L
    var lastTotal = 0L
    var lastPct: Int? = null
    // A compaction note waiting for the usage after it: its index and the percent before.
    var compacted: Pair<Int, Int?>? = null
    fun flushTools() {
        if (tools.isNotEmpty()) out += ChatItem.Tools(toolKey, tools.toList())
        tools.clear()
    }
    for (e in events) {
        val k = "e${e.seq}"
        if (e.kindCase == Event.KindCase.TOOL_CALL) {
            if (tools.isEmpty()) toolKey = k
            tools += e.toolCall
            continue
        }
        when (e.kindCase) {
            Event.KindCase.USER_MESSAGE -> {
                flushTools()
                out += ChatItem.User(k, e.userMessage.text, e.seq in d.typedLocally, eventTime(e))
            }
            Event.KindCase.AGENT_MESSAGE -> if (e.agentMessage.text.isNotBlank()) {
                flushTools()
                out += ChatItem.Agent(k, e.agentMessage.text)
            }
            Event.KindCase.REASONING -> if (e.reasoning.text.isNotBlank()) {
                flushTools()
                out += ChatItem.Thought(k, e.reasoning.text)
            }
            Event.KindCase.APPROVAL_REQUESTED -> requested[e.approvalRequested.approvalId] =
                e.approvalRequested.command.ifEmpty { e.approvalRequested.toolName.ifEmpty { e.approvalRequested.description } }
            Event.KindCase.APPROVAL_RESOLVED -> {
                flushTools()
                val r = e.approvalResolved
                val what = requested[r.approvalId].orEmpty()
                val ok = r.decision == ApprovalDecision.APPROVAL_DECISION_ALLOW
                val icon = when (r.decision) {
                    ApprovalDecision.APPROVAL_DECISION_ALLOW -> "✓"
                    ApprovalDecision.APPROVAL_DECISION_CANCELED -> "–"
                    ApprovalDecision.APPROVAL_DECISION_TERMINAL -> "↗"
                    else -> "✕"
                }
                out += ChatItem.Note(k, listOf(what, decisionText(r.decision) + (if (r.message.isNotEmpty()) "：" + r.message else "")).filter { it.isNotEmpty() }.joinToString("  "), icon, if (ok) 1 else 2)
            }
            Event.KindCase.QUESTION_ANSWERED -> {
                flushTools()
                val answers = e.questionAnswered.answersList.flatMap { it.answersList }
                out += ChatItem.Note(k, if (answers.isEmpty()) "已回答提问" else "已回答：" + answers.joinToString("、"), "✓", 1)
            }
            Event.KindCase.PLAN_PROPOSED -> {
                flushTools()
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
                flushTools()
                val t = e.turnEnded
                val parts = mutableListOf<String>()
                if (t.outcome == TurnOutcome.TURN_OUTCOME_ABORTED) parts += "已中断"
                if (t.durationMs > 0) parts += duration(t.durationMs)
                if (turnTokens > 0) parts += "${(turnTokens + 500) / 1000}k tokens"
                if (parts.isNotEmpty()) out += ChatItem.TurnEnd(k, parts.joinToString(" · "))
            }
            Event.KindCase.SETTINGS_CHANGED -> {
                flushTools()
                val c = e.settingsChanged
                val text = when {
                    c.compacted -> "上下文已压缩"
                    c.model.isNotEmpty() -> "已切换到 ${c.model} · ${c.effort}"
                    else -> modeOf(c.mode)?.let { "审批模式切到了 ${it.text}" }
                }
                if (text != null) {
                    if (c.compacted) compacted = out.size to lastPct
                    out += ChatItem.Note(k, text)
                }
            }
            Event.KindCase.TURN_CONTEXT -> d.terminalChanges[e.seq]?.let {
                flushTools()
                out += ChatItem.Note(k, "$machine 终端里$it")
            }
            Event.KindCase.SESSION_STATE -> if (e.sessionState.status == SessionStatus.SESSION_STATUS_EXITED) {
                flushTools()
                out += ChatItem.Note(k, dateTime(eventTime(e)) + " 结束")
            }
            else -> {}
        }
    }
    flushTools()
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

private fun failed(t: ToolCall) = t.status == "failed" || (t.hasExitCode() && t.exitCode != 0)

private fun summary(calls: List<ToolCall>): String {
    val cmds = calls.count { it.kind == ToolKind.TOOL_KIND_COMMAND }
    val edits = calls.count { it.kind == ToolKind.TOOL_KIND_FILE_CHANGE }
    val other = calls.size - cmds - edits
    return listOfNotNull(
        if (cmds > 0) "运行了 $cmds 条命令" else null,
        if (edits > 0) "编辑了 $edits 处文件" else null,
        if (other > 0) "调用了 $other 个工具" else null,
    ).joinToString("，")
}

@Composable
fun ChatItemView(item: ChatItem, agent: String) {
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
        is ChatItem.Tools -> Column(Modifier.padding(horizontal = 16.dp, vertical = 2.dp)) {
            if (item.calls.size == 1) ToolRow(item.calls[0])
            else {
                val bad = item.calls.count(::failed)
                Expandable(
                    (if (bad > 0) "✕ " else "✓ ") + summary(item.calls), "▾ " + summary(item.calls),
                    color = if (bad > 0) p.red else p.muted,
                ) { Column { item.calls.forEach { ToolRow(it) } } }
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
private fun ToolRow(t: ToolCall) {
    val p = pal
    val bad = failed(t)
    Expandable(
        (if (bad) "✕ " else "✓ ") + t.title.lines().first().take(160),
        "▾ " + t.title.lines().first().take(160),
        color = if (bad) p.red else p.muted,
    ) {
        Column(verticalArrangement = Arrangement.spacedBy(6.dp), modifier = Modifier.padding(bottom = 6.dp)) {
            if (t.input.isNotEmpty() && t.input != t.title) CodeBlock(t.input, maxLines = 30)
            if (t.output.isNotEmpty()) {
                CodeBlock(t.output + if (t.outputTruncated) "\n…" else "", maxLines = 40)
            }
            val meta = listOfNotNull(
                if (t.hasExitCode()) "退出码 ${t.exitCode}" else null,
                if (t.durationMs > 0) duration(t.durationMs) else null,
            )
            if (meta.isNotEmpty()) Text(meta.joinToString(" · "), style = MaterialTheme.typography.bodySmall, color = p.faint, fontFamily = FontFamily.Monospace, fontSize = 11.sp)
        }
    }
}
