package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
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

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.proto.ToolKind
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive

/** Original input, never the shortened preview or just its first line. */
fun commandOf(kind: ToolKind, title: String, input: String): String {
    if (kind == ToolKind.TOOL_KIND_FILE_CHANGE || input.isEmpty() || input == title) return title
    val value = runCatching { Json.parseToJsonElement(input) }.getOrElse { return input }
    if (value !is JsonObject) return input
    for (key in listOf("command", "CommandLine", "cmd")) {
        val command = value[key] as? JsonPrimitive
        if (command?.isString == true) return command.content
    }
    return title
}

/** Saveable with its chat row, including independent calls inside a group. */
@Composable
fun ExpandableTool(rowKey: String, text: String, modifier: Modifier = Modifier, textColor: Color? = null, expanded: Boolean? = null, onToggle: (() -> Unit)? = null, preview: @Composable () -> Unit) {
    val copy = uiText()
    var saved by rememberSaveable(rowKey) { mutableStateOf(false) }
    val open = expanded ?: saved
    val p = pal
    val maxHeight = with(LocalDensity.current) { (19.5.sp.toPx() * 12).toDp() }
    Column(modifier.animateContentSize(Motion.enter(Motion.BASE))
        .semantics { stateDescription = if (open) copy(UiMessages.projectsIosExpanded()) else copy(UiMessages.toolsAndroidCollapsed()) }
        .clickable(role = Role.Button) { if (onToggle != null) onToggle() else saved = !saved }) {
        preview()
        if (open) Box(Modifier.fillMaxWidth().clip(RoundedCornerShape(16.dp)).background(p.gbg)) {
            SelectionContainer {
                Text(text, style = T.mono13.copy(lineHeight = 19.5.sp), color = textColor ?: p.ink2,
                    modifier = Modifier.fillMaxWidth().padding(vertical = 12.dp).heightIn(max = maxHeight)
                        .verticalScroll(rememberScrollState()).padding(start = 14.dp, end = 44.dp))
            }
            Box(Modifier.align(Alignment.TopEnd)) { CopyButton(text) }
        }
    }
}

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
enum class ToolState(val message: io.github.lewin671.remoteagent.core.UiMessage) {
    RUNNING(UiMessages.toolsAndroidProcessing()),
    APPROVAL(UiMessages.listStateApproval()),
    SYNC(UiMessages.toolsWaitingSync()),
    UNCONFIRMED(UiMessages.toolsAndroidUnconfirmed()),
}

fun toolState(t: RunningTool, unfinished: Boolean, d: Derived, frozenAt: Long): ToolState = when {
    unfinished -> ToolState.UNCONFIRMED
    frozenAt > 0 -> ToolState.SYNC
    awaitingApproval(t, d) -> ToolState.APPROVAL
    else -> ToolState.RUNNING
}

/** Name the elapsed time and freeze it at disconnection; omit unknown start times. */
fun toolTimer(t: RunningTool, state: ToolState, now: Long, frozenAt: Long, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String {
    if (state == ToolState.UNCONFIRMED || state == ToolState.APPROVAL) return "—"
    val ms = toolElapsed(t, now, frozenAt) ?: return ""
    return copy(UiMessages.toolsElapsed(duration = (clock(ms)).toString())) + if (state == ToolState.SYNC) copy(UiMessages.toolsAndroidSyncSuffix()) else ""
}

/**
 * A tool that started and has no result yet, where it started: the running
 * command on a grey row of its own (design-system §7 工具执行), turning, with
 * how long it has run. Out of touch its clock stops at 等待同步; one the turn
 * left behind is a plain line, 后台运行.
 */
@Composable
fun RunningToolRow(item: ChatItem.Running, cwd: String, home: String) {
    val copy = uiText()
    val p = pal
    val ctx = LocalChat.current ?: return
    val t = now(1000)
    val tool = item.tool
    val state = toolState(tool, item.unfinished, ctx.derived, ctx.frozenAt)
    val title = toolTitle(tool, cwd, home, copy = copy).lines().first()
    // The turn completed without this tool's result: say only that.
    ExpandableTool(item.key, commandOf(tool.kind, tool.title, tool.input), Modifier.padding(horizontal = ChatSide),
        textColor = if (item.unfinished && tool.seq !in ctx.derived.noResult) p.ink2 else p.ink) {
        if (item.unfinished && tool.seq in ctx.derived.noResult) NowRow(title, copy(UiMessages.toolsWaitingSync()), spin = false)
        else if (item.unfinished) ToolLine(Lucide.LoaderCircle, p.ink3, title, meta = copy(UiMessages.toolsBackground()))
        else {
            val timer = toolTimer(tool, state, t, ctx.frozenAt, copy = copy)
            if (state == ToolState.SYNC) NowRow(title, timer.ifEmpty { copy(UiMessages.toolsWaitingSync()) }, spin = false)
            else NowRow(title, if (state == ToolState.APPROVAL) copy(state.message) else timer, spin = true)
        }
    }
}

@Composable
private fun NowRow(title: String, meta: String, spin: Boolean) {
    val p = pal
    Row(
        Modifier.fillMaxWidth().padding(vertical = 6.dp).clip(Radii.R18).background(p.gbg).padding(horizontal = 14.dp, vertical = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        if (spin) Spinner(p.ink2, size = 15.dp) else Mark(Lucide.LoaderCircle, p.ink3, 15.dp)
        Text(title, style = T.mono13, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        if (meta.isNotEmpty()) Text(meta, style = T.s13, color = p.ink2, maxLines = 1)
    }
}
