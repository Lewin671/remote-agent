package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
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
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.lifecycle.repeatOnLifecycle
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
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
import io.github.lewin671.remoteagent.core.SessionLookup
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.lookUpSession
import io.github.lewin671.remoteagent.core.Status
import io.github.lewin671.remoteagent.core.avatarIndex
import io.github.lewin671.remoteagent.core.machineName
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.statusOf
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

val LocalRemote = staticCompositionLocalOf<Remote> { error("no remote") }

/**
 * Asks for a session that is not held here by its ID: the listing comes a
 * page at a time, so one opened from a notification, a link or a fork's
 * source may not have been fetched (architecture §8 「按 ID 取」). Null once
 * it is held; else how the lookup stands ([lookUpSession]): still reading,
 * missing, or why the server could not be asked. It waits for the
 * connection, and [attempt] changing asks again at once.
 */
@Composable
fun sessionLookup(sid: String, attempt: Int = 0): SessionLookup? {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val held = state.sessions[sid] != null
    val online = state.conn == ConnState.ONLINE
    var lookup by remember(sid) { mutableStateOf<SessionLookup>(SessionLookup.Reading) }
    LaunchedEffect(sid, held, online, attempt) {
        if (held) lookup = SessionLookup.Reading
        if (held || !online) return@LaunchedEffect
        lookUpSession({ remote.fetchSession(sid) }) { lookup = it }
    }
    return if (held) null else lookup
}

/**
 * Fetches earlier pages of the session listing while a list wants them:
 * the next one for [page], every page left for [all] (filtering by title).
 * It waits for the connection, and after a failed page for a retry.
 */
@Composable
fun FetchEarlier(state: RemoteState, page: Boolean, all: Boolean = false) {
    val remote = LocalRemote.current
    val ready = state.moreSessions && state.conn == ConnState.ONLINE && state.sessionsError.isEmpty()
    LaunchedEffect(page, all, ready, state.sessionsNext) {
        if (!ready) return@LaunchedEffect
        try {
            if (all) remote.loadAllSessions() else if (page) remote.loadMoreSessions()
        } catch (e: kotlinx.coroutines.CancellationException) {
            throw e
        } catch (e: Exception) {
            // RemoteState.sessionsError says why; the line at the end offers the retry.
        }
    }
}

/** The line that ends a list whose earlier sessions are wanted and not all fetched; after a failure a tap tries again. */
@Composable
fun EarlierTail(state: RemoteState, modifier: Modifier = Modifier) {
    val remote = LocalRemote.current
    val scope = androidx.compose.runtime.rememberCoroutineScope()
    val failed = state.conn == ConnState.ONLINE && state.sessionsError.isNotEmpty()
    Text(
        io.github.lewin671.remoteagent.core.earlierTail(state), style = T.meta, color = pal.muted,
        modifier = modifier.then(if (failed) Modifier.clickable { scope.launch { runCatching { remote.loadMoreSessions() } } } else Modifier),
    )
}

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
    /** The session with its terminal showing. */
    val sessionTerminal: (String) -> Unit = {},
    val manageProjects: () -> Unit = {},
    val devices: () -> Unit = {},
    val quota: (String) -> Unit = {},
    val question: (sid: String, qid: String) -> Unit = { _, _ -> },
    /** The image viewer at an event's image. */
    val image: (sid: String, seq: Long, index: Int) -> Unit = { _, _, _ -> },
    val client: (String) -> Unit = {},
    val hiddenProjects: () -> Unit = {},
    /** A folder chosen for a new project, to start its first session in. */
    val newProjectAt: (machine: String, path: String) -> Unit = { _, _ -> },
)

val LocalNav = staticCompositionLocalOf<Nav> { error("no nav") }

/** The current time, ticking every [periodMs]. */
@Composable
fun now(periodMs: Long = 30_000): Long {
    var t by remember { mutableLongStateOf(System.currentTimeMillis()) }
    val owner = androidx.lifecycle.compose.LocalLifecycleOwner.current
    LaunchedEffect(periodMs, owner) {
        owner.lifecycle.repeatOnLifecycle(androidx.lifecycle.Lifecycle.State.STARTED) {
            t = System.currentTimeMillis()
            while (true) {
                delay(periodMs)
                t = System.currentTimeMillis()
            }
        }
    }
    return t
}

/** A status: a dot and a word, not color alone. */
@Composable
fun StatusLabel(status: Status, modifier: Modifier = Modifier) {
    val p = pal
    Row(modifier, verticalAlignment = Alignment.CenterVertically) {
        Dot(p.tone(status.tone))
        Spacer(Modifier.width(5.dp))
        Text(status.text, style = T.meta, color = p.muted)
    }
}

@Composable
fun SectionHeader(title: String, count: Int? = null, modifier: Modifier = Modifier) = Label(title, count, modifier)

/** A card on a grey page. */
@Composable
fun Card(modifier: Modifier = Modifier, onClick: (() -> Unit)? = null, content: @Composable () -> Unit) {
    val p = pal
    Box(modifier.fillMaxWidth().clip(Radii.MD).background(p.card).then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier)) { content() }
}

/** A small grey pill; 44 dp tall when it can be tapped. */
@Composable
fun Chip(text: String, color: Color? = null, bg: Color? = null, onClick: (() -> Unit)? = null) {
    val p = pal
    Box(
        Modifier.heightIn(min = if (onClick != null) 44.dp else 26.dp).then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text, style = T.meta, color = color ?: p.fg2, maxLines = 1, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.clip(RoundedCornerShape(14.dp)).background(bg ?: p.chip).padding(horizontal = 9.dp, vertical = 4.dp),
        )
    }
}

/** The server connection, under the navigation bar and apart from the session's state; nothing when online. */
@Composable
fun ConnBanner(state: RemoteState) {
    val p = pal
    if (state.conn == ConnState.ONLINE) return
    val failed = state.conn != ConnState.CONNECTING && state.error.isNotEmpty() && !state.loaded
    val text = when {
        state.conn == ConnState.CONNECTING -> "连接中…"
        failed -> "连不上服务器：${state.error}"
        else -> "连接断了，正在重连 · 内容可能不是最新的"
    }
    Text(
        text,
        Modifier.fillMaxWidth().background(p.soft).padding(horizontal = 16.dp, vertical = 5.dp),
        style = T.meta,
        color = if (failed) p.red else p.muted,
        maxLines = 2,
        overflow = TextOverflow.Ellipsis,
    )
}

@Composable
fun Confirm(title: String, text: String, confirm: String, danger: Boolean = false, onDismiss: () -> Unit, onConfirm: () -> Unit) =
    Alert(title, text, confirm, danger, onDismiss, onConfirm)

/** A 44 dp row with the page's side padding. */
@Composable
fun Row44(modifier: Modifier = Modifier, onClick: (() -> Unit)? = null, content: @Composable RowScope.() -> Unit) {
    Row(
        modifier.fillMaxWidth().heightIn(min = 44.dp)
            .then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier)
            .padding(horizontal = 16.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        content = content,
    )
}

@Composable
fun Bordered(modifier: Modifier = Modifier, content: @Composable () -> Unit) {
    val p = pal
    Box(modifier.border(1.dp, p.line, Radii.SM).padding(12.dp)) { content() }
}

fun machineOf(state: RemoteState, s: SessionView): MachineView? = state.machines[s.machineId]

fun statusIn(state: RemoteState, s: SessionView): Status = statusOf(s, state.machines[s.machineId])

fun machineLabel(state: RemoteState, id: String): String = machineName(state.machines[id], id)

/** 聊天 / 终端 for a session: the terminal greys out once the session has ended and gets a dot when it needs you. */
@Composable
fun SessionTabs(selected: Int, terminalEnabled: Boolean, needsTerminal: Boolean, dark: Boolean = false, onSelect: (Int) -> Unit) =
    ViewTabs(listOf("聊天", "终端"), selected, onSelect, enabled = { it == 0 || terminalEnabled }, dot = if (needsTerminal) 1 else -1, dark = dark)

/** Incremental event state, with an immutable result per history snapshot. */
@Composable
fun rememberDerived(events: List<io.github.lewin671.remoteagent.proto.Event>, agent: io.github.lewin671.remoteagent.proto.Agent): io.github.lewin671.remoteagent.core.Derived {
    val projection = remember { io.github.lewin671.remoteagent.core.EventProjection() }
    return remember(events, agent) { projection.update(events, agent) }
}
