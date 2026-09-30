package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import io.github.lewin671.remoteagent.core.agentName
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
 * A tool call without its result, as one quiet line that opens in place:
 * the command, what is known, and where to look. There is no output stream
 * to show, and no guess that it hangs.
 */
@Composable
fun ActiveTool(summary: String, command: String, note: String, onDetails: (() -> Unit)?, onTerminal: (() -> Unit)?, key: String, modifier: Modifier = Modifier, startOpen: Boolean = false, summaryColor: Color? = null) {
    val p = pal
    var open by rememberSaveable(key) { mutableStateOf(startOpen) }
    Column(modifier.fillMaxWidth().padding(vertical = 6.dp)) {
        Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { open = !open }, verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(summary, style = T.sm, color = summaryColor ?: p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            ArrowText(if (open) "收起" else "详情", p.muted, T.sm, down = !open, up = open)
        }
        if (open) Column(Modifier.fillMaxWidth().drawBehind { drawLine(p.line, Offset(0.5.dp.toPx(), 0f), Offset(0.5.dp.toPx(), size.height), 1.dp.toPx()) }.padding(start = 10.dp)) {
            if (command.isNotEmpty()) Text(
                command, style = T.mono, color = p.fg2,
                modifier = Modifier.padding(vertical = 6.dp).fillMaxWidth().heightIn(max = 88.dp).clip(Radii.XS).background(p.card).verticalScroll(rememberScrollState()).padding(8.dp),
            )
            if (note.isNotEmpty()) Text(note, style = T.meta, color = p.muted, modifier = Modifier.padding(top = 6.dp))
            if (onDetails != null || onTerminal != null) Row(Modifier.fillMaxWidth().heightIn(min = 44.dp), verticalAlignment = Alignment.CenterVertically) {
                if (onDetails != null) Box(Modifier.heightIn(min = 44.dp).clickable(onClick = onDetails), contentAlignment = Alignment.CenterStart) { ArrowText("查看详情", p.blue, T.sm) }
                Box(Modifier.weight(1f))
                if (onTerminal != null) Box(Modifier.heightIn(min = 44.dp).clickable(onClick = onTerminal), contentAlignment = Alignment.CenterEnd) { ArrowText("去终端", p.blue, T.sm) }
                else Text("终端不可用", style = T.sm, color = p.faint)
            }
        }
    }
}

/** A tool that started and has no result yet, where it started. */
@Composable
fun RunningToolRow(item: ChatItem.Running, cwd: String, home: String) {
    val p = pal
    val ctx = LocalChat.current ?: return
    val t = now(1000)
    val tool = item.tool
    val state = toolState(tool, item.unfinished, ctx.derived, ctx.frozenAt)
    val title = toolTitle(tool, cwd, home).lines().first()
    // The turn completed without this tool's result: say only that.
    if (item.unfinished && tool.seq in ctx.derived.noResult) {
        io.github.lewin671.remoteagent.ui.ToolLine("◌", p.faint, title, Modifier.padding(horizontal = 14.dp), meta = "未收到工具结果", onClick = { ctx.onTool(tool.seq) })
        return
    }
    val label = if (item.unfinished) io.github.lewin671.remoteagent.core.backgroundTitle(tool.kind, tool.itemType, ctx.agent) else toolLabel(tool.kind, tool.itemType, ctx.agent)
    val timer = toolTimer(tool, state, t, ctx.frozenAt)
    ActiveTool(
        "◌ $label · ${state.text}" + if (timer.isNotEmpty()) " $timer" else "",
        tool.input.ifEmpty { title },
        when (state) {
            ToolState.UNCONFIRMED -> "工具已转到后台；本轮结束不表示后台进程结束。请到终端确认。"
            ToolState.SYNC -> "未连接到服务器，暂时无法确认是否还在运行。"
            ToolState.APPROVAL -> "等你批准后才会执行。"
            ToolState.RUNNING -> "尚未收到结果 · 可去终端查看"
        },
        onDetails = { ctx.onTool(tool.seq) },
        onTerminal = if (state == ToolState.SYNC || !ctx.terminalOpen) null else ctx.onTerminal,
        key = "run" + tool.seq,
        modifier = Modifier.padding(horizontal = 14.dp),
        summaryColor = if (state == ToolState.APPROVAL) p.orangeInk else null,
    )
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
    val d = rememberDerived(events.events, agent)
    val p = pal
    val t = now(1000)
    val started = events.events.getOrNull((seq - 1).toInt())?.takeIf { it.seq == seq && it.kindCase == Event.KindCase.TOOL_STARTED }
    Column(Modifier.fillMaxSize().background(p.card)) {
        NavBar("工具详情", nav.back, agentName(agent), s?.let { titleOf(it) + " · " + machineLabel(state, it.machineId) })
        if (s == null || started == null) {
            PageNote("读取中…")
            return@Column
        }
        val st = started.toolStarted
        val tool = d.tools.firstOrNull { it.seq == seq } ?: RunningTool(st.title, st.callId, eventTime(started), st.kind, st.itemType, seq, st.input, st.inputDigest)
        val callSeq = d.completedAt[seq]
        val call = callSeq?.let { events.events.getOrNull((it - 1).toInt()) }?.toolCall
        val frozen = if (state.conn == ConnState.ONLINE) 0 else state.offlineSinceMs
        val cwd = s.info?.cwd.orEmpty()
        val label = toolLabel(st.kind, st.itemType, agent)
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 16.dp)) {
            if (call != null) {
                val ok = toolOk(call)
                Row(Modifier.padding(top = 14.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text("$label · " + (if (ok) "完成" else "失败") + (if (call.hasExitCode() && !ok) " · 退出码 ${call.exitCode}" else ""), style = T.smB, color = if (ok) p.muted else p.red, modifier = Modifier.weight(1f))
                    if (call.durationMs > 0) Text("调用耗时 ${duration(call.durationMs)}", style = T.sm, color = p.muted)
                }
            } else {
                val state2 = toolState(tool, seq in d.unfinished, d, frozen)
                val ms = toolElapsed(tool, t, frozen)
                Row(Modifier.padding(top = 14.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text("$label · ${state2.text}", style = T.smB, color = p.muted, modifier = Modifier.weight(1f))
                    if (ms != null && state2 != ToolState.UNCONFIRMED) Text("调用耗时 " + (if (frozen > 0) "上次 " else "") + duration(ms), style = T.sm, color = p.muted)
                }
            }
            if (cwd.isNotEmpty()) Text(shortPath(cwd), style = T.mono13, color = p.muted, modifier = Modifier.padding(bottom = 6.dp))
            val ms = toolElapsed(tool, t, frozen)
            ActiveTool(
                "◌ 完整命令" + if (call == null && ms != null) " ${clock(ms)}" else "",
                st.input.ifEmpty { st.title },
                when {
                    call != null -> ""
                    seq in d.unfinished -> "这一轮已经结束，但没有收到这个工具的结果。它可能转到了后台，也可能随中断停止了；请到终端确认。"
                    else -> "尚未收到工具结果。实时输出请去终端查看。"
                },
                onDetails = null, onTerminal = null, key = "page$seq", startOpen = true,
            )
            if (call != null) {
                val out = call.output.trimEnd('\n')
                if (out.isNotEmpty()) {
                    Text("结果", style = T.smB, color = p.muted, modifier = Modifier.padding(top = 8.dp, bottom = 6.dp))
                    Code(out.lines().take(200).joinToString("\n") + if (call.outputTruncated) "\n…（已截断）" else "")
                }
            } else Hint("耗时不代表进度；没有输出也可能仍在运行。")
        }
        val online = state.conn == ConnState.ONLINE
        Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp).topLine(p.line).padding(top = 8.dp, bottom = 16.dp)) {
            Btn("去终端看实时画面", { nav.back(); nav.sessionTerminal(sid) }, Modifier.fillMaxWidth(), enabled = online)
            if (call == null && d.running) {
                val act = rememberAction()
                DockFoot("需要停止这一轮？", if (act.running) "正在中断…" else "中断本轮", danger = true) {
                    if (online && !act.running) act.run({ remote.command(sid, remote.cmd { interrupt = InterruptRequest.newBuilder().setSessionId(sid).build() }) })
                }
                if (act.error.isNotEmpty()) Text("没能中断：${act.error}", color = p.red, style = T.meta)
                Text("中断不保证结束后台进程；后台命令在终端里处理。", style = T.meta, color = p.muted)
            }
        }
    }
}
