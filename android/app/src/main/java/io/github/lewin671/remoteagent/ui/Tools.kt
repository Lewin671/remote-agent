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
    /** Opens the viewer at an event's image. */
    val onImage: (seq: Long, index: Int) -> Unit = { _, _ -> },
    /** The sessions this client has, to name the one a fork came from. */
    val sessions: Map<String, io.github.lewin671.remoteagent.core.SessionView> = emptyMap(),
    /** Opens another session. */
    val onSession: (String) -> Unit = {},
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

/** Name the elapsed time and freeze it at disconnection; omit unknown start times. */
fun toolTimer(t: RunningTool, state: ToolState, now: Long, frozenAt: Long): String {
    if (state == ToolState.UNCONFIRMED || state == ToolState.APPROVAL) return "—"
    val ms = toolElapsed(t, now, frozenAt) ?: return ""
    return "已运行 ${clock(ms)}" + if (state == ToolState.SYNC) " · 等待同步" else ""
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
        io.github.lewin671.remoteagent.ui.ToolLine(Lucide.LoaderCircle, p.faint, title, Modifier.padding(horizontal = 14.dp), meta = "等待同步")
        return
    }
    val timer = toolTimer(tool, state, t, ctx.frozenAt)
    ToolLine(Lucide.LoaderCircle, p.blue, title, Modifier.padding(horizontal = 14.dp),
        meta = if (state == ToolState.SYNC) timer.ifEmpty { "等待同步" } else if (item.unfinished) "后台运行" else if (state == ToolState.APPROVAL) state.text else timer)
}
