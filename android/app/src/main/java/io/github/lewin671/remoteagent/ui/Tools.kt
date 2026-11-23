package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.DEFAULT_AGENT
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.clock
import io.github.lewin671.remoteagent.core.proto.SessionToolCallItem
import io.github.lewin671.remoteagent.core.proto.SessionToolStatus
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ToolKind
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive

private val GENERIC_TOOL_TYPES = setOf("ToolCall", "CommandExecution")

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
fun ExpandableTool(
    rowKey: String,
    text: String,
    modifier: Modifier = Modifier,
    textColor: Color? = null,
    expanded: Boolean? = null,
    onToggle: (() -> Unit)? = null,
    preview: @Composable () -> Unit,
) {
    val copy = uiText()
    var saved by rememberSaveable(rowKey) { mutableStateOf(false) }
    val open = expanded ?: saved
    val p = pal
    val maxHeight = with(LocalDensity.current) { (19.5.sp.toPx() * 12).toDp() }
    Column(
        modifier.animateContentSize(Motion.enter(Motion.BASE))
            .semantics { stateDescription = if (open) copy(UiMessages.projectsIosExpanded()) else copy(UiMessages.toolsAndroidCollapsed()) }
            .clickable(role = Role.Button) { if (onToggle != null) onToggle() else saved = !saved },
    ) {
        preview()
        if (open) Box(Modifier.fillMaxWidth().clip(RoundedCornerShape(16.dp)).background(p.gbg)) {
            SelectionContainer {
                Text(
                    text,
                    style = T.mono13.copy(lineHeight = 19.5.sp),
                    color = textColor ?: p.ink2,
                    modifier = Modifier.fillMaxWidth().padding(vertical = 12.dp).heightIn(max = maxHeight)
                        .verticalScroll(rememberScrollState()).padding(start = 14.dp, end = 44.dp),
                )
            }
            Box(Modifier.align(Alignment.TopEnd)) { CopyButton(text) }
        }
    }
}

/** What the chat's timeline rows need from screen/app context. */
class ChatContext(
    val sid: String,
    val agent: Agent = DEFAULT_AGENT,
    val agentName: String = "",
    val onImage: (seq: Long, index: Int) -> Unit = { _, _ -> },
    val onSession: (String) -> Unit = {},
    val localOutboxById: Map<String, Outgoing> = emptyMap(),
)

val LocalChat = compositionLocalOf<ChatContext?> { null }

/** Renders one [SessionToolCallItem] from Core directly. */
@Composable
internal fun ToolCallRow(
    tool: SessionToolCallItem,
    rowKey: String,
    seq: Long,
    expanded: Boolean? = null,
    onToggle: (() -> Unit)? = null,
    modifier: Modifier = Modifier.padding(horizontal = ChatSide),
) {
    val copy = uiText()
    val p = pal
    val ctx = LocalChat.current
    val body = commandOf(tool.toolKind, tool.title, tool.rawInput).ifEmpty { tool.detail }
    val decision = if (tool.hasDecisionLabel()) uiText(tool.decisionLabel) else ""
    val bgLabel = if (tool.hasBackgroundLabel()) uiText(tool.backgroundLabel) else ""
    when (tool.status) {
        SessionToolStatus.SESSION_TOOL_STATUS_RUNNING -> {
            val t = now(1000)
            val frozen = tool.frozenAtMs > 0
            val elapsedMs = when {
                frozen && tool.sinceMs > 0 -> (tool.frozenAtMs - tool.sinceMs).coerceAtLeast(0L)
                tool.sinceMs > 0 -> (t - tool.sinceMs).coerceAtLeast(0L)
                tool.elapsedMs > 0 -> tool.elapsedMs
                else -> -1L
            }
            val timer = if (elapsedMs >= 0L) {
                copy(UiMessages.toolsElapsed(duration = clock(elapsedMs))) +
                    if (frozen) copy(UiMessages.toolsAndroidSyncSuffix()) else ""
            } else ""
            ExpandableTool(rowKey, body, modifier, textColor = p.ink, expanded = expanded, onToggle = onToggle) {
                if (frozen) NowRow(tool.title.lines().firstOrNull().orEmpty(), timer.ifEmpty { copy(UiMessages.toolsWaitingSync()) }, spin = false)
                else NowRow(tool.title.lines().firstOrNull().orEmpty(), timer.ifEmpty { copy(UiMessages.toolsAndroidProcessing()) }, spin = true)
            }
        }
        SessionToolStatus.SESSION_TOOL_STATUS_UNCONFIRMED -> {
            ExpandableTool(rowKey, body, modifier, textColor = p.ink2, expanded = expanded, onToggle = onToggle) {
                if (tool.noResult) NowRow(tool.title.lines().firstOrNull().orEmpty(), copy(UiMessages.toolsWaitingSync()), spin = false)
                else ToolLine(Lucide.LoaderCircle, p.ink3, tool.title.lines().firstOrNull().orEmpty(), meta = copy(UiMessages.toolsAndroidUnconfirmed()))
            }
        }
        SessionToolStatus.SESSION_TOOL_STATUS_BACKGROUND -> {
            val title = tool.title.lines().firstOrNull().orEmpty()
            val generic = (title.isEmpty() || title in GENERIC_TOOL_TYPES) && tool.itemType in GENERIC_TOOL_TYPES
            val meta = listOfNotNull(
                if (generic) null else bgLabel.ifEmpty { copy(UiMessages.toolsBackground()) },
                decision.takeIf { it.isNotEmpty() },
            ).joinToString(" · ")
            ExpandableTool(rowKey, body, modifier, textColor = p.ink2, expanded = expanded, onToggle = onToggle) {
                ToolLine(Lucide.LoaderCircle, p.ink3, if (generic) copy(UiMessages.toolsBackground()) else title, meta = meta)
            }
        }
        else -> {
            val ok = tool.status == SessionToolStatus.SESSION_TOOL_STATUS_OK
            val meta = listOfNotNull(
                if (!ok && tool.hasExitCode) copy(UiMessages.toolsExitCode(code = tool.exitCode.toString())) else null,
                decision.takeIf { it.isNotEmpty() },
            ).joinToString(" · ").ifEmpty {
                if (tool.elapsedMs >= 1000) "${tool.elapsedMs / 1000}s" else ""
            }
            Column(modifier) {
                ExpandableTool(rowKey, body, Modifier, expanded = expanded, onToggle = onToggle) {
                    ToolLine(
                        if (ok) Lucide.Check else Lucide.X,
                        if (ok) p.ink3 else p.ink2,
                        tool.title.lines().firstOrNull().orEmpty(),
                        meta = meta,
                        metaColor = if (tool.hasDecisionLabel() && !tool.decisionAllowed) p.red else p.ink2,
                    )
                }
                if (tool.imagesCount > 0) ImageRow(tool.imagesList, { i -> ctx?.onImage?.invoke(seq, i) })
            }
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
