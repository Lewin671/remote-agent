package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
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
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.repeatOnLifecycle
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.proto.ConnectionBannerKind
import io.github.lewin671.remoteagent.core.proto.ConnectionViewModel
import kotlinx.coroutines.delay

val LocalRemote = staticCompositionLocalOf<Remote> { error("no remote") }

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
    /** A session the new-task page just started: it takes the page's place, so back goes where the page was opened from. */
    val startedSession: (String) -> Unit = {},
    /** 设置' sub-pages: they slide in as pages, like the rest. */
    val licenses: () -> Unit = {},
    val phoneStorage: () -> Unit = {},
    /** 电脑状态 of a computer; its attachment storage (the computer's page, scrolled to it); a new session that asks the Agent to clean up. */
    val machineStatus: (String) -> Unit = {},
    val deviceStorage: (String) -> Unit = {},
    val cleanup: (String) -> Unit = {},
    val deviceConnection: (String) -> Unit = {},
    val openUsage: (String, io.github.lewin671.remoteagent.proto.Agent, String) -> Unit = { _, _, _ -> },
    val notifications: () -> Unit = {},
    val deviceForAgent: (String, io.github.lewin671.remoteagent.proto.Agent) -> Unit = { id, _ -> device(id) },
    val manageMachineProjects: (String) -> Unit = { manageProjects() },
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
            .clip(Radii.R24)
            .background(if (LocalOnSoft.current) p.raised else p.gbg)
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
fun ConnBanner(conn: ConnectionViewModel) {
    val p = pal
    if (!conn.hasBanner() || !conn.banner.visible) return
    val b = conn.banner
    val icon = when (b.kind) {
        ConnectionBannerKind.CONNECTION_BANNER_KIND_NO_NETWORK -> Lucide.WifiOff
        ConnectionBannerKind.CONNECTION_BANNER_KIND_NO_SERVER -> if (b.errorTone) Lucide.X else Lucide.CloudOff
        else -> Lucide.LoaderCircle
    }
    val text = if (b.errorTone && b.hasFailedText()) uiText(b.failedText) else uiText(b.text)
    var details by remember { mutableStateOf(false) }
    val color = if (b.errorTone) p.red else p.ink2
    Row(
        Modifier.fillMaxWidth()
            .background(p.gbg)
            .then(if (b.showDetails) Modifier.clickable { details = true } else Modifier)
            .padding(horizontal = 16.dp, vertical = 5.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Mark(icon, color, 12.dp)
        Spacer(Modifier.width(6.dp))
        Text(text, Modifier.weight(1f), style = T.s12, color = color, maxLines = 2, overflow = TextOverflow.Ellipsis)
        if (b.showDetails) Text(uiText(UiMessages.commonDetails()), Modifier.padding(start = 8.dp), style = T.s12b, color = p.blue)
    }
    if (details && b.showDetails) ServerDetails(conn) { details = false }
}

/** The same sentence in a session, as the glass strip under its segmented control (design-system §6 连接状态). */
@Composable
fun ConnStrip(conn: ConnectionViewModel, modifier: Modifier = Modifier) {
    if (!conn.hasBanner() || !conn.banner.visible) return
    val b = conn.banner
    val icon = when (b.kind) {
        ConnectionBannerKind.CONNECTION_BANNER_KIND_NO_NETWORK -> Lucide.WifiOff
        ConnectionBannerKind.CONNECTION_BANNER_KIND_NO_SERVER -> if (b.errorTone) Lucide.X else Lucide.CloudOff
        else -> Lucide.LoaderCircle
    }
    val text = if (b.errorTone && b.hasFailedText()) uiText(b.failedText) else uiText(b.text)
    var details by remember { mutableStateOf(false) }
    Strip(
        icon, text, modifier,
        action = if (b.showDetails) uiText(UiMessages.commonDetails()) else null,
        onClick = if (b.showDetails) ({ details = true }) else null,
        error = b.errorTone,
        spin = b.kind == ConnectionBannerKind.CONNECTION_BANNER_KIND_WAITING,
    )
    if (details && b.showDetails) ServerDetails(conn) { details = false }
}

/** Why the server is out of reach: the judgement first, the failure in the system's words last. */
@Composable
internal fun ServerDetails(conn: ConnectionViewModel, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    val d = conn.serverDetails
    val reason = when {
        d.hasReasonText() -> uiText(d.reasonText)
        conn.hasReasonText() -> uiText(conn.reasonText)
        else -> ""
    }
    Sheet(onClose) {
        SheetHead(uiText(UiMessages.connectionNoServer()))
        Text(
            uiText(UiMessages.connectionAndroidExplanation()),
            style = T.s13,
            color = p.ink,
        )
        Text(
            uiText(UiMessages.connectionAndroidServer(name = remote.account.server.substringAfter("://"))),
            style = T.s13,
            color = p.ink2,
            modifier = Modifier.padding(top = 10.dp),
        )
        // Since start nothing was reached: when it was last is not known here.
        if (d.showDisconnectedAt)
            Text(
                uiText(UiMessages.connectionAndroidDisconnectedAt(time = uiText(io.github.lewin671.remoteagent.core.listWhen(d.disconnectedAtMs, now())))),
                style = T.s13,
                color = p.ink2,
                modifier = Modifier.padding(top = 2.dp),
            )
        if (reason.isNotEmpty())
            Text(
                uiText(UiMessages.connectionAndroidReason(reason = reason)),
                style = T.s13,
                color = p.ink2,
                modifier = Modifier.padding(top = 2.dp),
            )
        PillButton(
            uiText(UiMessages.connectionAndroidRetryNow()),
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
) = Alert(title = title, text = text, confirm = confirm, danger = danger, onDismiss = onDismiss, onConfirm = onConfirm)

object ScheduleJump {
    val requested = mutableStateMapOf<String, Long>()
}
