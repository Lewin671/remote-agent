package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Derived
import io.github.lewin671.remoteagent.core.RunningTool
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.awaitingApproval
import io.github.lewin671.remoteagent.core.clock
import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.core.eventTime
import io.github.lewin671.remoteagent.core.shortPath
import io.github.lewin671.remoteagent.core.titleOf
import io.github.lewin671.remoteagent.core.toolElapsed
import io.github.lewin671.remoteagent.core.toolLabel
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.InterruptRequest

/** What the chat's tool rows need besides the row itself. */
class ChatContext(
    val sid: String,
    val agent: Agent,
    val derived: Derived,
    /** When the connection dropped, 0 while online: timers stop there. */
    val frozenAt: Long,
    val terminalOpen: Boolean,
    val onTerminal: () -> Unit,
    val onTool: (Long) -> Unit,
    /** Opens the viewer at an event's image. */
    val onImage: (seq: Long, index: Int) -> Unit = { _, _ -> },
)

val LocalChat = compositionLocalOf<ChatContext?> { null }

/** How a tool without a result reads now. */
enum class ToolState(val text: String) {
    RUNNING("处理中"),
    APPROVAL("等待审批"),
    SYNC("等待同步"),
    UNCONFIRMED("状态待确认"),
}

fun toolState(t: RunningTool, unfinished: Boolean, d: Derived, frozenAt: Long): ToolState = when {
    unfinished -> ToolState.UNCONFIRMED
    frozenAt > 0 -> ToolState.SYNC
    awaitingApproval(t, d) -> ToolState.APPROVAL
    else -> ToolState.RUNNING
}

/** A timer reading: 2 分 12 秒 in the chat; "上次 2:12" once it is frozen; nothing when the start is unknown. */
fun toolTimer(t: RunningTool, state: ToolState, now: Long, frozenAt: Long): String {
    if (state == ToolState.UNCONFIRMED || state == ToolState.APPROVAL) return "—"
    val ms = toolElapsed(t, now, frozenAt) ?: return ""
    return if (state == ToolState.SYNC) "上次 ${clock(ms)}" else duration(ms)
}

/**
 * A tool with no result yet, in its place in the chat. Short ones are one
 * line; after [LONG_MS], or once it waits, it shows what is known and where
 * to look: there is no output stream to show, and no guess that it hangs.
 */
@Composable
fun RunningToolRow(item: ChatItem.Running, cwd: String, home: String) {
    val p = pal
    val ctx = LocalChat.current ?: return
    val t = now(1000)
    val tool = item.tool
    val state = toolState(tool, item.unfinished, ctx.derived, ctx.frozenAt)
    val elapsed = toolElapsed(tool, t, ctx.frozenAt) ?: 0
    val title = toolTitle(tool, cwd, home).lines().first()
    // The turn completed without this tool's result: say only that.
    if (item.unfinished && tool.seq in ctx.derived.noResult) {
        Row(
            Modifier.fillMaxWidth().clickable { ctx.onTool(tool.seq) }.padding(horizontal = 16.dp, vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text("◌", color = p.faint, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.width(8.dp))
            Text(title, style = MaterialTheme.typography.bodyMedium, fontFamily = FontFamily.Monospace, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            Text("未收到工具结果", style = MaterialTheme.typography.bodySmall, color = p.faint)
        }
        return
    }
    if (state == ToolState.RUNNING && elapsed < LONG_MS) {
        Row(
            Modifier.fillMaxWidth().clickable { ctx.onTool(tool.seq) }.padding(horizontal = 16.dp, vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            CircularProgressIndicator(Modifier.size(12.dp), strokeWidth = 1.5.dp)
            Spacer(Modifier.width(8.dp))
            Text(title, style = MaterialTheme.typography.bodyMedium, fontFamily = FontFamily.Monospace, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            val timer = toolTimer(tool, state, t, ctx.frozenAt)
            if (timer.isNotEmpty()) Text(timer, style = MaterialTheme.typography.bodySmall, color = p.faint)
        }
        return
    }
    Column(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp)
            .background(if (state == ToolState.APPROVAL) p.orangeSoft else p.soft, RoundedCornerShape(12.dp))
            .padding(12.dp),
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (state == ToolState.RUNNING) CircularProgressIndicator(Modifier.size(12.dp), strokeWidth = 1.5.dp)
            else Text("◌", color = p.muted, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.width(8.dp))
            val label = if (item.unfinished) "后台命令" else toolLabel(tool.kind, tool.itemType, ctx.agent)
            Text("$label · ${state.text}", style = MaterialTheme.typography.titleSmall, color = if (state == ToolState.APPROVAL) p.orangeInk else p.fg2, modifier = Modifier.weight(1f))
            Text(toolTimer(tool, state, t, ctx.frozenAt), style = MaterialTheme.typography.bodySmall, color = p.muted)
        }
        Text(title, style = MaterialTheme.typography.bodyMedium, fontFamily = FontFamily.Monospace, maxLines = 2, overflow = TextOverflow.Ellipsis)
        Text(
            when (state) {
                ToolState.UNCONFIRMED -> "工具已转到后台；本轮结束不表示后台进程结束。请到终端确认。"
                ToolState.SYNC -> "未连接到服务器，暂时无法确认是否还在运行。"
                ToolState.APPROVAL -> "等你批准后才会执行。"
                ToolState.RUNNING -> "尚未收到工具结果；实时画面可在终端查看。"
            },
            style = MaterialTheme.typography.bodySmall, color = p.muted,
        )
        Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
            TextButton(onClick = { ctx.onTool(tool.seq) }) { Text("查看详情 ›") }
            if (state == ToolState.SYNC || !ctx.terminalOpen) TextButton(onClick = {}, enabled = false) { Text("终端不可用") }
            else TextButton(onClick = ctx.onTerminal) { Text("去终端 ›") }
        }
    }
}

/** After this long a running tool's row says what is and is not known. */
const val LONG_MS = 10_000L

/**
 * The line above the message box while tools run or background work is
 * unconfirmed: "Bash · 2:12 ›", "2 项处理中 · 3:08 ›", "后台工作 · 去终端确认 ›".
 */
fun toolSummary(d: Derived, agent: Agent, now: Long, frozenAt: Long): String? {
    val running = d.tools
    if (running.isNotEmpty()) {
        val oldest = running.minByOrNull { if (it.since > 0) it.since else Long.MAX_VALUE } ?: running.first()
        val ms = toolElapsed(oldest, now, frozenAt)
        if (frozenAt > 0) return "等待同步" + (ms?.let { " · 上次 ${clock(it)}" } ?: "")
        if (running.size == 1) {
            val waiting = awaitingApproval(oldest, d)
            return toolLabel(oldest.kind, oldest.itemType, agent) + " · " + (if (waiting) "等待审批" else ms?.let { clock(it) } ?: "处理中")
        }
        return "${running.size} 项处理中" + (ms?.let { " · ${clock(it)}" } ?: "")
    }
    if (d.background.isNotEmpty()) return "后台工作 · 去终端确认"
    return null
}

/** 工具详情: only what was received; no output stream, no progress, no guess that it hangs, and no stopping one tool. */
@Composable
fun ToolPage(sid: String, seq: Long) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val s = state.sessions[sid]
    val events by remote.events(sid).collectAsState()
    val agent = s?.agent() ?: Agent.AGENT_CODEX
    val d = remember(events.events) { derive(events.events, agent) }
    val p = pal
    val t = now(1000)
    val started = events.events.getOrNull((seq - 1).toInt())?.takeIf { it.seq == seq && it.kindCase == Event.KindCase.TOOL_STARTED }
    PageScaffold("工具详情") {
        if (s == null || started == null) {
            Text("读取中…", Modifier.padding(16.dp), color = p.muted)
            return@PageScaffold
        }
        val st = started.toolStarted
        val tool = d.tools.firstOrNull { it.seq == seq } ?: RunningTool(st.title, st.callId, eventTime(started), st.kind, st.itemType, seq, st.input, st.inputDigest)
        val callSeq = d.completedAt[seq]
        val call = callSeq?.let { events.events.getOrNull((it - 1).toInt()) }?.toolCall
        val frozen = if (state.conn == ConnState.ONLINE) 0 else state.offlineSinceMs
        val cwd = s.info?.cwd.orEmpty()
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Text(titleOf(s), color = p.muted, style = MaterialTheme.typography.bodyMedium)
            if (call != null) {
                val ok = toolOk(call)
                Text(
                    toolLabel(st.kind, st.itemType, agent) + " · " + (if (ok) "完成" else "失败") + (if (call.hasExitCode() && !ok) " · 退出码 ${call.exitCode}" else ""),
                    style = MaterialTheme.typography.titleMedium, color = if (ok) p.greenInk else p.red,
                )
                if (call.durationMs > 0) Text("调用耗时 ${duration(call.durationMs)}", color = p.muted)
            } else {
                val state2 = toolState(tool, seq in d.unfinished, d, frozen)
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(toolLabel(st.kind, st.itemType, agent) + " · " + state2.text, style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                    val ms = toolElapsed(tool, t, frozen)
                    if (ms != null && state2 != ToolState.UNCONFIRMED) Text("调用耗时 " + (if (frozen > 0) "上次 " else "") + duration(ms), color = p.muted)
                }
            }
            if (cwd.isNotEmpty()) Text(shortPath(cwd), style = MaterialTheme.typography.bodySmall, color = p.muted, fontFamily = FontFamily.Monospace)
            Text("完整命令", style = MaterialTheme.typography.titleSmall)
            CodeBlock(st.input.ifEmpty { st.title }, maxLines = 60)
            if (call != null) {
                val out = call.output.trimEnd('\n')
                if (out.isNotEmpty()) {
                    Text("结果", style = MaterialTheme.typography.titleSmall)
                    CodeBlock(out + if (call.outputTruncated) "\n…（已截断）" else "", maxLines = 200)
                }
            } else {
                Text(
                    if (seq in d.unfinished) "这一轮已经结束，但没有收到这个工具的结果。它可能转到了后台，也可能随中断停止了；请到终端确认。"
                    else "工具结果尚未返回。这里没有可用的中间输出，不能据此判断命令卡住。",
                    style = MaterialTheme.typography.bodyMedium,
                )
                Text("聊天中的计时从工具开始事件算起，不是进度百分比。长时间没有输出也可能正常。", style = MaterialTheme.typography.bodySmall, color = p.muted)
            }
            val online = state.conn == ConnState.ONLINE
            OutlinedButton(onClick = { nav.back(); nav.sessionTerminal(sid) }, enabled = online, modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp)) { Text("去终端看实时画面") }
            if (call == null && d.running) {
                val act = rememberAction()
                Text("需要停止这一轮？", style = MaterialTheme.typography.titleSmall)
                OutlinedButton(
                    onClick = { act.run({ remote.command(sid, remote.cmd { interrupt = InterruptRequest.newBuilder().setSessionId(sid).build() }) }) },
                    enabled = online && !act.running, modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp),
                ) { Text(if (act.running) "正在中断…" else "中断本轮", color = p.red) }
                if (act.error.isNotEmpty()) Text("没能中断：${act.error}", color = p.red, style = MaterialTheme.typography.bodySmall)
                Text("中断不保证结束后台进程；后台命令在终端里处理。", style = MaterialTheme.typography.bodySmall, color = p.muted)
            }
        }
    }
}
