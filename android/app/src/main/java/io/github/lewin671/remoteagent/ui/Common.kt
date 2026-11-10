package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.repeatOnLifecycle
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Link
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionLookup
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.Status
import io.github.lewin671.remoteagent.core.lookUpSession
import io.github.lewin671.remoteagent.core.machineName
import io.github.lewin671.remoteagent.core.shownStatus
import io.github.lewin671.remoteagent.core.whenText
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

val LocalRemote = staticCompositionLocalOf<Remote> { error("no remote") }

/**
 * Asks for a session that is not held here by its ID: the listing comes a page at a time, so one
 * opened from a notification, a link or a fork's source may not have been fetched (architecture §8
 * 「按 ID 取」). Null once it is held; else how the lookup stands ([lookUpSession]): still reading,
 * missing, or why the server could not be asked. It waits for the connection, and [attempt]
 * changing asks again at once.
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
 * Fetches earlier pages of the session listing while a list wants them: the next one for [page],
 * every page left for [all] (filtering by title). It waits for the connection, and after a failed
 * page for a retry.
 */
@Composable
fun FetchEarlier(state: RemoteState, page: Boolean, all: Boolean = false) {
    val remote = LocalRemote.current
    val ready =
        state.moreSessions && state.conn == ConnState.ONLINE && state.sessionsError.isEmpty()
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

/**
 * The line that ends a list whose earlier sessions are wanted and not all fetched; after a failure
 * a tap tries again.
 */
@Composable
fun EarlierTail(state: RemoteState, modifier: Modifier = Modifier) {
    val remote = LocalRemote.current
    val scope = androidx.compose.runtime.rememberCoroutineScope()
    val failed = state.conn == ConnState.ONLINE && state.sessionsError.isNotEmpty()
    Text(
        io.github.lewin671.remoteagent.core.earlierTail(state),
        style = T.s12,
        color = pal.ink2,
        modifier =
            modifier.then(
                if (failed)
                    Modifier.clickable {
                        scope.launch { runCatching { remote.loadMoreSessions() } }
                    }
                else Modifier,
            ),
    )
}

/** Navigation actions shared by the screens. */
data class Nav(
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
    /** A folder chosen for a new project, to start its first session in. */
    val newProjectAt: (machine: String, path: String) -> Unit = { _, _ -> },
    val recovery: () -> Unit = {},
    val recover: () -> Unit = {},
    val createAccount: () -> Unit = {},
    val rejoin: (String) -> Unit = {},
    val schedules: () -> Unit = {},
    val task: (machine: String, id: String) -> Unit = { _, _ -> },
    val newTask: (session: String) -> Unit = {},
    val scheduledSession: (sid: String, seq: Long) -> Unit = { _, _ -> },
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

/** A card on a grey page. */
@Composable
fun Card(
    modifier: Modifier = Modifier,
    onClick: (() -> Unit)? = null,
    content: @Composable () -> Unit,
) {
    val p = pal
    Box(
        modifier
            .fillMaxWidth()
            .clip(Radii.R28)
            .background(if (LocalOnSoft.current) p.paper else p.gbg)
            .then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier),
    ) {
        content()
    }
}

/**
 * The way to the server, under the navigation bar and apart from the session's state; nothing when
 * online. It names the nearest stretch that is down (client-v30 「断线」): this device's network, or
 * the server, whose details open from the bar.
 */
@Composable
fun ConnBanner(state: RemoteState) {
    val p = pal
    val line = connLine(state) ?: return
    var details by remember { mutableStateOf(false) }
    val color = if (line.failed) p.red else p.ink2
    Row(
        Modifier.fillMaxWidth()
            .background(p.gbg)
            .then(if (line.details) Modifier.clickable { details = true } else Modifier)
            .padding(horizontal = 16.dp, vertical = 5.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Mark(line.icon, color, 12.dp)
        Spacer(Modifier.width(6.dp))
        Text(line.text, Modifier.weight(1f), style = T.s12, color = color, maxLines = 2, overflow = TextOverflow.Ellipsis)
        if (line.details) Text("详情", Modifier.padding(start = 8.dp), style = T.s12b, color = p.blue)
    }
    if (details && line.details) ServerDetails(state) { details = false }
}

/** The same sentence in a session, as the glass strip under its segmented control (design-system §6 连接状态). */
@Composable
fun ConnStrip(state: RemoteState, modifier: Modifier = Modifier) {
    val line = connLine(state) ?: return
    var details by remember { mutableStateOf(false) }
    Strip(
        line.icon, line.text, modifier,
        action = if (line.details) "详情" else null,
        onClick = if (line.details) ({ details = true }) else null,
        error = line.failed,
        spin = line.icon == Lucide.LoaderCircle,
    )
    if (details && line.details) ServerDetails(state) { details = false }
}

/** What the connection bar or strip says: the icon, the sentence, whether it is a failure in red, and whether 详情 opens. */
class ConnLine(val icon: ImageVector, val text: String, val failed: Boolean, val details: Boolean)

/** Nothing when online; otherwise the nearest stretch that is down (client-v30 「断线」). */
fun connLine(state: RemoteState): ConnLine? {
    if (state.link == Link.ONLINE) return null
    // Nothing kept from before to show: the bar says what the connection is needed for.
    val failed = state.link == Link.NO_SERVER && !state.loaded && state.error.isNotEmpty()
    val icon =
        when (state.link) {
            Link.NO_NETWORK -> Lucide.WifiOff
            Link.NO_SERVER -> if (failed) Lucide.X else Lucide.CloudOff
            else -> Lucide.LoaderCircle
        }
    val text =
        when (state.link) {
            Link.NO_NETWORK -> "本机网络不通 · " + if (state.loaded) "显示的是上次的内容" else "联网后才能读取会话"
            Link.NO_SERVER -> if (failed) "连不上服务器：${state.error}" else "连不上服务器 · 正在重试"
            else -> if (state.synced) "正在重连…" else "连接中…"
        }
    return ConnLine(icon, text, failed, state.link == Link.NO_SERVER)
}

/** Why the server is out of reach: the judgement first, the failure in the system's words last. */
@Composable
internal fun ServerDetails(state: RemoteState, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    Sheet(onClose) {
        SheetHead("连不上服务器")
        Text(
            "本机的网络是通的，但服务器没有回应。可能是服务器停了或正在重启，也可能是现在的网络到不了它。会一直自动重试，连上后补齐这段时间的内容。",
            style = T.s13,
            color = p.ink,
        )
        Text(
            "服务器　${remote.account.server.substringAfter("://")}",
            style = T.s13,
            color = p.ink2,
            modifier = Modifier.padding(top = 10.dp),
        )
        // Since start nothing was reached: when it was last is not known here.
        if (state.synced)
            Text(
                "断开于　${whenText(state.offlineSinceMs, now())}",
                style = T.s13,
                color = p.ink2,
                modifier = Modifier.padding(top = 2.dp),
            )
        if (state.error.isNotEmpty())
            Text(
                "原因　${state.error}",
                style = T.s13,
                color = p.ink2,
                modifier = Modifier.padding(top = 2.dp),
            )
        if (state.errorDetail.isNotEmpty() && state.errorDetail != state.error)
            Code(state.errorDetail, Modifier.fillMaxWidth().padding(top = 10.dp), maxLines = 4)
        PillButton(
            "立即重试",
            {
                remote.reconnectNow()
                onClose()
            },
            Modifier.fillMaxWidth().padding(top = 14.dp),
        )
    }
}

@Composable
fun Confirm(
    title: String,
    text: String,
    confirm: String,
    danger: Boolean = false,
    onDismiss: () -> Unit,
    onConfirm: () -> Unit,
) = Alert(title, text, confirm, danger, onDismiss, onConfirm)

fun machineOf(state: RemoteState, s: SessionView): MachineView? = state.machines[s.machineId]

fun statusIn(state: RemoteState, s: SessionView): Status = shownStatus(state, s)

fun machineLabel(state: RemoteState, id: String): String = machineName(state.machines[id], id)

/** Incremental event state, with an immutable result per history snapshot. */
@Composable
fun rememberDerived(
    events: List<io.github.lewin671.remoteagent.proto.Event>,
    agent: io.github.lewin671.remoteagent.proto.Agent,
): io.github.lewin671.remoteagent.core.Derived {
    val projection = remember { io.github.lewin671.remoteagent.core.EventProjection() }
    return remember(events, agent) { projection.update(events, agent) }
}

@Composable
fun rememberDerived(
    events: io.github.lewin671.remoteagent.core.SessionEvents,
    agent: io.github.lewin671.remoteagent.proto.Agent,
): io.github.lewin671.remoteagent.core.Derived {
    val readable = events.stateEvents
    val history = rememberDerived(readable, agent)
    return remember(history, events.live, readable.size) {
        io.github.lewin671.remoteagent.core.currentActions(history, events.live, readable)
    }
}
