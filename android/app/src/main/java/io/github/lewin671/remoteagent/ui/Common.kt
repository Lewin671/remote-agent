package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.Status
import io.github.lewin671.remoteagent.core.avatarIndex
import io.github.lewin671.remoteagent.core.machineName
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.statusOf
import kotlinx.coroutines.delay

val LocalRemote = staticCompositionLocalOf<Remote> { error("no remote") }

/** Navigation actions shared by the screens. */
class Nav(
    val back: () -> Unit,
    val session: (String) -> Unit,
    val project: (String) -> Unit,
    /** The project page with the new-session box focused. */
    val projectNew: (String) -> Unit,
    val home: () -> Unit,
    val settings: () -> Unit,
    val device: (String) -> Unit,
    val pair: () -> Unit,
    val approval: (sid: String, aid: String) -> Unit,
    val plan: (String) -> Unit,
    val newProject: () -> Unit,
    val pickProject: () -> Unit,
)

val LocalNav = staticCompositionLocalOf<Nav> { error("no nav") }

/** The current time, ticking every [periodMs]. */
@Composable
fun now(periodMs: Long = 30_000): Long {
    var t by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(periodMs) {
        while (true) {
            delay(periodMs)
            t = System.currentTimeMillis()
        }
    }
    return t
}

@Composable
fun Dot(color: Color, size: Int = 8) {
    Box(Modifier.size(size.dp).clip(CircleShape).background(color))
}

/** A status: a dot and a word, not color alone. */
@Composable
fun StatusLabel(status: Status, modifier: Modifier = Modifier) {
    val p = pal
    Row(modifier, verticalAlignment = Alignment.CenterVertically) {
        Dot(p.tone(status.tone))
        Spacer(Modifier.width(5.dp))
        Text(status.text, style = MaterialTheme.typography.bodySmall, color = if (status.tone == io.github.lewin671.remoteagent.core.Tone.WAIT) p.orangeInk else p.muted)
    }
}

@Composable
fun Avatar(key: String, size: Int = 36) {
    val p = pal
    val colors = listOf(p.blue to p.blueSoft, p.green to Color(0x2922A05A), p.purple to p.purpleSoft, p.orange to p.orangeSoft)
    val (fg, bg) = colors[avatarIndex(key)]
    Box(Modifier.size(size.dp).clip(RoundedCornerShape(10.dp)).background(bg), contentAlignment = Alignment.Center) {
        Text(projectName(key).take(1).lowercase(), color = fg, fontWeight = FontWeight.Bold, style = MaterialTheme.typography.titleSmall)
    }
}

@Composable
fun SectionHeader(title: String, count: Int? = null, modifier: Modifier = Modifier) {
    val p = pal
    Row(modifier.padding(start = 16.dp, end = 16.dp, top = 18.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(title, style = MaterialTheme.typography.titleSmall, color = p.fg2)
        if (count != null) {
            Spacer(Modifier.width(6.dp))
            Text("$count", style = MaterialTheme.typography.bodySmall, color = p.muted)
        }
    }
}

@Composable
fun Card(modifier: Modifier = Modifier, onClick: (() -> Unit)? = null, content: @Composable () -> Unit) {
    val p = pal
    Surface(
        modifier = modifier.fillMaxWidth().then(if (onClick != null) Modifier.clip(RoundedCornerShape(14.dp)).clickable(onClick = onClick) else Modifier),
        shape = RoundedCornerShape(14.dp),
        color = p.card,
        border = if (p.dark) androidx.compose.foundation.BorderStroke(1.dp, p.line) else null,
    ) { content() }
}

@Composable
fun Chip(text: String, color: Color? = null, bg: Color? = null, onClick: (() -> Unit)? = null) {
    val p = pal
    Box(
        Modifier.heightIn(min = 32.dp).clip(RoundedCornerShape(16.dp)).background(bg ?: p.chip)
            .then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier)
            .padding(horizontal = 10.dp, vertical = 6.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(text, style = MaterialTheme.typography.labelSmall, color = color ?: p.fg2, maxLines = 1)
    }
}

/** "◌ 连接断了，正在重连 · 内容可能不是最新的" under the top bar; nothing when online. */
@Composable
fun ConnBanner(state: RemoteState) {
    val p = pal
    if (state.conn == ConnState.ONLINE) return
    val text = when (state.conn) {
        ConnState.CONNECTING -> "◌ 连接中…"
        else -> if (state.error.isNotEmpty() && !state.loaded) "✕ 连不上服务器：${state.error}" else "◌ 连接断了，正在重连 · 内容可能不是最新的"
    }
    Text(
        text,
        Modifier.fillMaxWidth().background(p.orangeSoft).padding(horizontal = 16.dp, vertical = 8.dp),
        style = MaterialTheme.typography.bodySmall,
        color = p.orangeInk,
        maxLines = 2,
        overflow = TextOverflow.Ellipsis,
    )
}

@Composable
fun Confirm(title: String, text: String, confirm: String, danger: Boolean = false, onDismiss: () -> Unit, onConfirm: () -> Unit) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(title) },
        text = { Text(text) },
        confirmButton = {
            TextButton(onClick = { onDismiss(); onConfirm() }) { Text(confirm, color = if (danger) pal.red else MaterialTheme.colorScheme.primary) }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("取消") } },
    )
}

@Composable
fun Row44(modifier: Modifier = Modifier, onClick: (() -> Unit)? = null, content: @Composable RowScope.() -> Unit) {
    Row(
        modifier.fillMaxWidth().heightIn(min = 48.dp)
            .then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier)
            .padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        content = content,
    )
}

@Composable
fun Bordered(modifier: Modifier = Modifier, content: @Composable () -> Unit) {
    val p = pal
    Box(modifier.border(1.dp, p.line, RoundedCornerShape(12.dp)).padding(12.dp)) { content() }
}

fun machineOf(state: RemoteState, s: SessionView): MachineView? = state.machines[s.machineId]

fun statusIn(state: RemoteState, s: SessionView): Status = statusOf(s, state.machines[s.machineId])

fun machineLabel(state: RemoteState, id: String): String = machineName(state.machines[id], id)
