package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.ime
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.sp
import androidx.compose.foundation.border
import androidx.compose.ui.draw.clip
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledIconButton
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.effectiveAgent
import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.defaultMode
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.sessionOrder
import io.github.lewin671.remoteagent.core.exited
import io.github.lewin671.remoteagent.core.machineAgents
import io.github.lewin671.remoteagent.core.modeOf
import io.github.lewin671.remoteagent.core.projectKey
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.projects
import io.github.lewin671.remoteagent.core.shortPath
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.CreateSessionRequest
import io.github.lewin671.remoteagent.proto.DaemonSettings
import io.github.lewin671.remoteagent.proto.GetSettingsRequest
import io.github.lewin671.remoteagent.proto.ListDirectoriesRequest
import io.github.lewin671.remoteagent.proto.ListDirectoriesResponse
import io.github.lewin671.remoteagent.proto.ListModelsResponse

/**
 * The modes a new session can be started in besides the default. Neither CLI
 * starts in plan, and full access is only ever the confirmed machine default.
 */
val newSessionModes = listOf(Mode.ASK, Mode.AUTO)

/** How a new session starts; empty model, effort and mode mean the machine's defaults. */
data class NewConfig(
    val machineId: String,
    val cwd: String,
    val agent: Agent = Agent.AGENT_CODEX,
    val model: String = "",
    val effort: String = "",
    val mode: Mode? = null,
)

/**
 * The configuration follows the project (architecture §13): the latest
 * session's machine, folder, CLI, model, effort and mode. Full access and
 * plan are not carried over. If that machine is offline, another online
 * machine the project used, with its defaults.
 */
fun projectConfig(state: RemoteState, key: String): NewConfig? {
    val sessions = state.sessions.values.filter { projectKey(it) == key }.sortedByDescending { it.updatedMs }
    val last = sessions.firstOrNull() ?: return null
    val mode = modeOf(last)?.takeIf { it != Mode.FULL && it != Mode.PLAN }
    if (state.machines[last.machineId]?.online != false) {
        return NewConfig(last.machineId, key, last.agent(), last.info?.model.orEmpty(), last.info?.effort.orEmpty(), mode)
    }
    val other = sessions.map { it.machineId }.distinct().firstOrNull { state.machines[it]?.online == true } ?: return NewConfig(last.machineId, key, last.agent())
    return NewConfig(other, key, last.agent().takeIf { it in machineAgents(state.machines[other]) } ?: Agent.AGENT_CODEX)
}

/**
 * The mode a CLI gives a session started without one, as the daemon reads
 * it (AgentInfo.default_mode): null when not known (an older daemon, or a
 * configuration it could not read).
 */
data class DefaultMode(
    /** One of the four, or null for a mode outside them (Claude Code's accept edits). */
    val mode: Mode?,
    val known: Boolean,
    /** The folder's project settings may give another default (Claude Code). */
    val varies: Boolean,
)

fun defaultModeOf(m: MachineView?, agent: Agent): DefaultMode {
    val info = m?.meta?.agentsList?.firstOrNull { effectiveAgent(it.agent) == effectiveAgent(agent) }
    val known = info != null && info.defaultModeSource != io.github.lewin671.remoteagent.proto.DefaultModeSource.DEFAULT_MODE_SOURCE_UNSPECIFIED &&
        info.defaultModeSource != io.github.lewin671.remoteagent.proto.DefaultModeSource.UNRECOGNIZED
    return DefaultMode(if (known) modeOf(info!!.defaultMode) else null, known, info?.defaultModeVaries == true)
}

/** The mode a start uses: chosen, or the CLI's effective default on that machine. */
fun modeLabel(chosen: Mode?, d: DefaultMode, agent: Agent): String = when {
    chosen != null -> chosen.text
    !d.known -> "默认模式（不确定，可能是完全放行）"
    d.mode != null -> "默认：${d.mode.text}"
    else -> "默认：${agentName(agent)} 自己的模式"
} + if (chosen == null && d.varies) "（项目设置可能不同）" else ""

/**
 * A new session's configuration (architecture §8): it follows the project,
 * whose latest session or machines may change, until a draft is begun or the
 * user changes it, then it stays, so an update never retargets a draft.
 */
class DraftConfig(initial: NewConfig?) {
    var value by mutableStateOf(initial)
        private set
    private var chosen = false

    /** A new suggestion from the project; taken only while nothing is written or chosen. */
    fun follow(initial: NewConfig?, drafting: Boolean) {
        if (value == null || (!chosen && !drafting)) value = initial
    }

    /** The user's own choice, kept from now on. */
    fun choose(c: NewConfig) {
        value = c
        chosen = true
    }
}

/** What pressing send does about the approval mode. */
sealed interface LaunchMode {
    /** Start in this mode; UNSPECIFIED leaves it to the CLI's default, which the daemon checks again in the folder. */
    data class Send(val mode: ApprovalMode) : LaunchMode

    /** The default is full access, or not known: start only after the user confirms it (architecture §7). */
    data object ConfirmFull : LaunchMode
}

/**
 * Resolves the mode of a start. A chosen mode is sent as it is. Without one
 * the CLI's effective default on the machine decides (AgentInfo): full
 * access, or a default nobody knows, is confirmed first; otherwise the
 * choice is left to the daemon, which refuses a folder whose own settings
 * make it full access unless that was confirmed.
 */
fun launchMode(chosen: Mode?, d: DefaultMode): LaunchMode = when {
    chosen != null -> LaunchMode.Send(chosen.proto)
    !d.known || d.mode == Mode.FULL -> LaunchMode.ConfirmFull
    else -> LaunchMode.Send(ApprovalMode.APPROVAL_MODE_UNSPECIFIED)
}

/** The session a start created, or null when it did not (refused, or asked to confirm first). */
fun startedSession(r: io.github.lewin671.remoteagent.proto.CommandResult?): String? =
    r?.takeIf { it.hasCreateSession() && it.createSession.hasSession() }?.createSession?.session?.id?.takeIf { it.isNotEmpty() }

/** The daemon refused a start because the folder's default is full access and nobody confirmed it. */
fun refusedForFullAccess(e: Throwable): Boolean =
    e is io.github.lewin671.remoteagent.core.CommandException && e.fromDaemon && e.code == "failed_precondition" &&
        (e.message ?: "").contains("full access", ignoreCase = true)

/**
 * A project: its sessions, and at the bottom the message box that starts a
 * new one. With the keyboard up the header folds into the navigation bar.
 */
@Composable
fun ProjectScreen(key: String, focus: Boolean) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    val t = now()
    val sessions = state.sessions.values.filter { projectKey(it) == key }.sortedWith(sessionOrder)
    val active = sessions.filter { !exited(it) }
    val ended = sessions.filter { exited(it) }
    var showEnded by rememberSaveable { mutableStateOf(false) }
    val machines = sessions.map { machineLabel(state, it.machineId) }.distinct()
    val ime = WindowInsets.ime.getBottom(LocalDensity.current) > 0
    var typing by remember { mutableStateOf(false) }
    val compact = ime && typing
    val hidden = io.github.lewin671.remoteagent.core.isHidden(state, key)
    Column(Modifier.fillMaxSize().background(p.card).imePadding()) {
        NavBar(if (compact) projectName(key) else "", nav.back, subtitle = if (compact) shortPath(key) else null)
        LazyColumn(Modifier.weight(1f)) {
            if (!compact) item {
                Row(Modifier.padding(horizontal = 16.dp).padding(bottom = 2.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
                    Avatar(key, 52)
                    Column(Modifier.weight(1f)) {
                        Text(projectName(key), style = T.h22, color = p.fg, maxLines = 2, overflow = TextOverflow.Ellipsis)
                        Text((listOf(shortPath(key)) + machines).joinToString(" · "), style = T.sm, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Row(Modifier.padding(top = 2.dp), verticalAlignment = Alignment.CenterVertically) {
                            Text("${active.size} 个进行中 · ", style = T.sm, color = p.muted)
                            if (ended.isNotEmpty()) Box(Modifier.clickable { showEnded = !showEnded }) { ArrowText("已结束 ${ended.size}", p.blue, T.smB, down = showEnded) }
                            else Text("没有已结束的会话", style = T.sm, color = p.muted)
                        }
                    }
                }
            }
            if (hidden) item { HiddenBanner(state, key) }
            if (active.isNotEmpty()) {
                item { Label("进行中", active.size) }
                items(active, key = { it.id }) { s -> SessionRow(state, s, t, last = s == active.last(), showProject = false, preview = !compact) }
            }
            val recent = if (showEnded) ended else ended.take(3)
            if (recent.isNotEmpty() && !compact) {
                item { Label(if (showEnded) "已结束" else "最近结束", if (showEnded) ended.size else null) }
                items(recent, key = { it.id }) { s -> SessionRow(state, s, t, last = s == recent.last(), showProject = false) }
            }
            item { Spacer(Modifier.height(12.dp)) }
        }
        // A hidden project stays reachable from its sessions and
        // notifications; starting a new session there restores it first.
        if (!hidden) NewSessionBox(state, projectConfig(state, key), projectName(key), focus, onFocus = { typing = it })
    }
}

/** 这个项目已从列表移除 · 恢复: a notice, not a button in the way. */
@Composable
private fun HiddenBanner(state: RemoteState, key: String) {
    val remote = LocalRemote.current
    val change = PrefsChanges.states[key]
    val blocked = prefsBlocked(state)
    Column(Modifier.padding(horizontal = 16.dp, vertical = 4.dp)) {
        Banner(
            if (change == PrefsChanges.State.Saving) "正在恢复到项目列表…" else "这个项目已从列表移除",
            action = if (blocked == null && change != PrefsChanges.State.Saving) "恢复" else null,
            onAction = { PrefsChanges.run(remote, io.github.lewin671.remoteagent.core.PrefsOp.Unhide(key)) },
        )
        if (blocked != null) Hint(blocked, color = pal.orangeInk)
        if (change is PrefsChanges.State.Failed) Hint("没能保存：${change.error}", color = pal.red)
    }
}

/**
 * Another CLI for a draft (client-v2): the text and images stay; the model
 * and effort belong to the old CLI and go, so the new one starts with its
 * own defaults; the mode is kept when the new CLI has it. The machine's
 * default mode for the new CLI still decides when none is chosen, and full
 * access is still confirmed at send.
 */
fun switchAgent(c: NewConfig, agent: Agent): NewConfig =
    if (c.agent == agent) c else c.copy(agent = agent, model = "", effort = "", mode = c.mode?.takeIf { it in newSessionModes })

/**
 * Another machine for a draft: the model and effort were that machine's
 * list, so they go. The CLI stays when the machine has it; otherwise the
 * caller must have the user pick one ([machineAgents]), never silently.
 */
fun switchMachine(c: NewConfig, machineId: String, agents: List<Agent>): NewConfig =
    c.copy(machineId = machineId, model = "", effort = "", agent = if (c.agent in agents) c.agent else agents.first())

/** The two lines of the configuration: machine and mode, then model and effort, neither cut. */
fun configLines(state: RemoteState, c: NewConfig, d: DefaultMode): Pair<String, String> =
    "${machineLabel(state, c.machineId)} · ${modeLabel(c.mode, d, c.agent)}" to
        (listOf(c.model, c.effort).filter { it.isNotEmpty() }.joinToString(" · ").ifEmpty { "默认模型" })

/**
 * The new-session box (client-v7): ＋, 描述任务…, ↑. The configuration
 * follows the project until a draft is begun, then stays (architecture §8),
 * and is looked at or changed in 新会话设置 from ＋; a machine that goes
 * offline is replaced only by the user's choice. The first message starts
 * the session: there is no start without one.
 */
@Composable
fun NewSessionBox(state: RemoteState, initial: NewConfig?, name: String, focus: Boolean, onFocus: (Boolean) -> Unit = {}) {
    val p = pal
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val key = initial?.cwd ?: name
    // Text and configuration live and come back together, so a draft
    // reopened later still goes where it was going.
    val entry = Drafts.newSession(key, initial)
    val draft = entry.config
    val config = draft.value
    var text by entry.text
    val images = ImageDrafts.forProject(key)
    var settings by remember { mutableStateOf(false) }
    val act = rememberAction()
    val fr = remember { FocusRequester() }
    val drafting = text.isNotBlank() || images.images.isNotEmpty()
    LaunchedEffect(initial, drafting) { draft.follow(initial, drafting) }
    LaunchedEffect(focus) { if (focus) runCatching { fr.requestFocus() } }
    val machine = config?.machineId
    val online = state.conn == ConnState.ONLINE
    val machineOnline = machine != null && online && state.machines[machine]?.online == true
    // The mode a start without a choice gets, as the machine reports it.
    val defaults = config?.let { defaultModeOf(state.machines[it.machineId], it.agent) } ?: DefaultMode(null, false, false)
    // Images go to the machine the session will start on; a new machine gets them again.
    val blocked = uploadBlocked(machine?.let { state.machines[it] }, online, machine?.let { machineLabel(state, it) }.orEmpty())
    LaunchedEffect(machine, blocked, images.images.size) { if (machine != null && blocked == null) images.sendAllTo(remote, machine) }
    val pick = rememberImagePicker(images, remote, machine, blocked, maxImages(machine?.let { state.machines[it] }))
    var confirmFull by remember { mutableStateOf<NewConfig?>(null) }
    fun launch(cfg: NewConfig, mode: ApprovalMode, confirmedFull: Boolean = false) {
        // While an earlier start's outcome is unknown, only it is sent
        // again, as it was: the daemon then answers from its log instead
        // of starting a second session.
        val start = Starts.of(key)
        act.run({
            try {
                val retry = start.attempt.frozen
                val prepared = start.attempt.next {
                    start.prompt = text.trim()
                    // The images this request carries; any picked after it stay in the draft.
                    start.taken = images.images.filter { it.readyFor(cfg.machineId) }
                    val ids = start.taken.mapNotNull { it.imageId }
                    remote.prepare(cfg.machineId, "", remote.cmd {
                        createSession = CreateSessionRequest.newBuilder().setCwd(cfg.cwd).setPrompt(start.prompt).setAgent(cfg.agent)
                            .setModel(cfg.model).setEffort(cfg.effort).setMode(mode).addAllImageIds(ids).setConfirmFullAccess(confirmedFull).build()
                    })
                }
                remote.forward(prepared, retryOfUnknown = retry).also { start.attempt.settle() }
            } catch (e: Exception) {
                val err = start.attempt.settle(e) ?: e
                // The folder's own settings make it full access: nothing ran; ask, then send a new request.
                if (refusedForFullAccess(err)) {
                    confirmFull = cfg
                    return@run null
                }
                throw err
            }
        }) { r ->
            // Only a session that started takes the draft; a refusal that asks
            // for confirmation, or no answer, leaves text and images as they are.
            val sid = startedSession(r) ?: return@run
            Outbox.rememberPrompt(sid, start.prompt)
            nav.session(sid)
            if (text.trim() == start.prompt) text = ""
            val taken = start.taken
            images.detach(taken)
            taken.forEach { it.release() }
            start.taken = emptyList()
        }
    }
    fun start() {
        val cfg = config ?: return
        val st = Starts.of(key)
        // Other content is a new start; the unknown one is given up.
        if (st.attempt.frozen && text.trim() != st.prompt) st.attempt.abandon()
        // An unknown start is resent as prepared, with the mode it had.
        if (st.attempt.frozen) return launch(cfg, ApprovalMode.APPROVAL_MODE_UNSPECIFIED)
        when (val m = launchMode(cfg.mode, defaultModeOf(state.machines[cfg.machineId], cfg.agent))) {
            is LaunchMode.Send -> launch(cfg, m.mode)
            LaunchMode.ConfirmFull -> confirmFull = cfg
        }
    }
    val c = config
    val notices = @Composable {
        Column(Modifier.padding(horizontal = 12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            if (c != null && state.machines[c.machineId]?.online != true && drafting) OfflineChoice(state, c, name) { draft.choose(it) }
            else if (c != null && state.machines[c.machineId]?.online != true) Banner("${machineLabel(state, c.machineId)} 不在线，现在不能新建", tone = Tone2.WARN)
            if (act.running) Banner("正在 ${c?.let { machineLabel(state, it.machineId) }} 上启动…")
            val frozen = Starts.of(key)
            // An unknown start has its own notice below.
            if (act.error.isNotEmpty() && !frozen.attempt.frozen) Banner("启动失败：${act.error}", tone = Tone2.ERROR, action = "重试", onAction = { start() })
            if (!act.running && frozen.attempt.frozen) {
                var gaveUp by remember { mutableStateOf(0) }
                Banner(
                    if (frozen.attempt.expired) "不确定「${frozen.prompt.take(40)}」有没有启动，它已过期，不会再重发。请先看看进行中的会话"
                    else "上次的启动结果不确定。再点 ↑ 会重发同一个请求，电脑收到过就不会再启动一次",
                    tone = Tone2.WARN, action = "放弃", onAction = { frozen.attempt.abandon(); act.error = ""; gaveUp++ },
                )
            }
        }
    }
    notices()
    val fullDefault = c != null && (c.mode == Mode.FULL || (c.mode == null && (defaults.mode == Mode.FULL || !defaults.known)))
    ComposeBox(
        text, { text = it }, "描述任务…",
        canSend = c != null && !act.running && machineOnline && drafting && (images.images.isEmpty() || images.ready(machine)),
        onSend = ::start,
        menu = listOf(
            MenuEntry("添加图片", enabled = blocked == null, onClick = pick),
            MenuEntry("新会话设置 ›", enabled = c != null) { settings = true },
        ),
        warning = if (fullDefault) (if (c!!.mode == Mode.FULL) "完全放行 · 发送前确认" else if (defaults.known) "当前默认：完全放行 · 发送前确认" else "默认模式未知，可能是完全放行 · 发送前确认") else null,
        sendLabel = "开始", focusRequester = fr, onFocus = onFocus,
        top = { ImageStrip(images, remote, machine, maxImages(machine?.let { state.machines[it] })) },
    )
    confirmFull?.let { cfg ->
        val d = defaultModeOf(state.machines[cfg.machineId], cfg.agent)
        Confirm(
            "以完全放行启动？",
            "${machineLabel(state, cfg.machineId)} 上 ${agentName(cfg.agent)} " +
                (if (d.known && d.mode == Mode.FULL) "的默认审批模式是完全放行" else "在这个目录里的默认审批模式可能是完全放行") +
                "：这个会话里执行命令、改文件都不会再问你。不想这样可以在「新会话设置」里选询问或自动。",
            "完全放行启动", danger = true,
            onDismiss = { confirmFull = null },
        ) { launch(cfg, ApprovalMode.APPROVAL_MODE_UNSPECIFIED, confirmedFull = true) }
    }
    if (settings && c != null) NewSettingsSheet(state, c, name, onDone = { draft.choose(it); settings = false }, onClose = { settings = false })
}

/** The old name, for pages that start a session in a folder of their own. */
@Composable
fun NewSessionCard(state: RemoteState, initial: NewConfig?, name: String, focus: Boolean) = NewSessionBox(state, initial, name, focus)

/**
 * The machine went offline while a draft was written: nothing moves by
 * itself. The user picks another machine the project used, told what that
 * machine offers, and checks the box again before sending.
 */
@Composable
private fun OfflineChoice(state: RemoteState, c: NewConfig, name: String, onChoose: (NewConfig) -> Unit) {
    val p = pal
    val others = state.sessions.values.filter { projectKey(it) == c.cwd }.map { it.machineId }.distinct()
        .ifEmpty { state.machines.keys.toList() }.filter { it != c.machineId && state.machines[it]?.online == true }
    Column(Modifier.fillMaxWidth().clip(Radii.MD).background(p.soft).padding(horizontal = 12.dp, vertical = 9.dp)) {
        Text("${machineLabel(state, c.machineId)} 已离线，任务尚未开始", style = T.sm, color = p.orangeInk)
        Text("草稿已保留，请选择在线机器；不自动换机器或 CLI。", style = T.meta, color = p.muted)
        for (id in others) {
            val agents = machineAgents(state.machines[id])
            val cliNote = when {
                c.agent in agents -> "有 ${agentName(c.agent)}；模型和强度会按这台机器重新选择"
                else -> "只有 ${agents.joinToString("、") { agentName(it) }}；会改用 ${agentName(agents.first())}，模型和模式按这台机器重新选择"
            }
            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { onChoose(switchMachine(c, id, agents)) }, verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text("改用 ${machineLabel(state, id)}", style = T.bodyB, color = p.fg)
                    Text(cliNote, style = T.meta, color = p.muted)
                }
                Arrow(p.muted, T.body)
            }
        }
        if (others.isEmpty()) Text("$name 用过的其他电脑也都不在线。", style = T.meta, color = p.muted)
    }
}

/** A row of 新会话设置: a short label, the value at the right, ›. */
@Composable
private fun SetRow(label: String, value: String, onClick: (() -> Unit)?, dot: androidx.compose.ui.graphics.Color? = null, valueColor: androidx.compose.ui.graphics.Color? = null) {
    val p = pal
    Row(
        Modifier.fillMaxWidth().heightIn(min = 44.dp).then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier).padding(horizontal = 14.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Text(label, style = T.sm, color = p.muted, modifier = Modifier.width(34.dp).weight(1f))
        if (dot != null) Dot(dot)
        Text(value, style = T.sm, color = valueColor ?: p.fg2, maxLines = 2, overflow = TextOverflow.Ellipsis, textAlign = androidx.compose.ui.text.style.TextAlign.End, modifier = Modifier.widthIn(max = 200.dp))
        Text("›", style = ts(18.sp, lineHeight = 1f), color = p.faint)
    }
}

/**
 * 新会话设置 (client-v7): CLI, machine (those the project used), folder,
 * model and effort, and approval mode, each chosen on a page of the sheet.
 * Changing the CLI or machine keeps the draft.
 */
@Composable
fun NewSettingsSheet(state: RemoteState, initial: NewConfig, name: String, onDone: (NewConfig) -> Unit, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    var c by remember { mutableStateOf(initial) }
    var page by remember { mutableStateOf("") }
    var models by remember { mutableStateOf<ListModelsResponse?>(null) }
    var modelsError by remember { mutableStateOf("") }
    LaunchedEffect(c.machineId, c.agent) {
        models = null
        modelsError = ""
        try {
            models = remote.listModels(c.machineId, c.agent)
        } catch (e: Exception) {
            modelsError = errorText(e)
        }
    }
    val used = state.sessions.values.filter { projectKey(it) == initial.cwd }.map { it.machineId }.distinct().ifEmpty { state.machines.keys.toList() }
    val agents = machineAgents(state.machines[c.machineId])
    val defaults = defaultModeOf(state.machines[c.machineId], c.agent)
    val shownModel = models?.let { ms -> io.github.lewin671.remoteagent.core.findModel(ms.modelsList, c.model) { it.slug } }
    Sheet(onClose) {
        when (page) {
            "" -> {
                SheetHead("新会话设置", "完成", { onDone(c) })
                Column(Modifier.fillMaxWidth().clip(Radii.MD).background(p.soft)) {
                    val rows = listOf<@Composable () -> Unit>(
                        { SetRow("CLI", agentName(c.agent), if (agents.size > 1) ({ page = "cli" }) else null) },
                        { SetRow("机器", machineLabel(state, c.machineId), { page = "machine" }, dot = if (state.machines[c.machineId]?.online == true) p.green else p.gray) },
                        { SetRow("目录", shortPath(c.cwd), null) },
                        {
                            SetRow("模型", listOf(shownModel?.displayName?.ifEmpty { null } ?: c.model, c.effort).filter { it.isNotEmpty() }.joinToString(" · ").ifEmpty { "默认模型" }, { page = "model" })
                        },
                        { SetRow("审批", modeLabel(c.mode, defaults, c.agent), { page = "mode" }, valueColor = if (c.mode == Mode.FULL || (c.mode == null && (defaults.mode == Mode.FULL || !defaults.known))) p.red else null) },
                    )
                    rows.forEachIndexed { i, r ->
                        if (i > 0) androidx.compose.material3.HorizontalDivider(color = p.line, thickness = 1.dp)
                        r()
                    }
                }
                Hint("沿用 $name 最近会话的设置；更换 CLI 或机器会保留草稿。")
            }
            "cli" -> {
                SheetHead("新会话使用的 CLI", onBack = { page = "" })
                agents.forEachIndexed { i, a ->
                    ListItem(agentName(a), "${machineLabel(state, c.machineId)} 上已安装", checked = a == c.agent, last = i == agents.size - 1, onClick = { c = switchAgent(c, a); page = "" })
                }
                Hint("切换后采用该 CLI 的默认模型和有效模式，草稿保留。当前机器只有一种 CLI 时只显示名称。")
                val missing = Agent.entries.filter { it != Agent.AGENT_UNSPECIFIED && it != Agent.UNRECOGNIZED && it !in agents }
                for (a in missing) Banner("${machineLabel(state, c.machineId)} 没有 ${agentName(a)}", Modifier.padding(top = 8.dp), action = "换机器", onAction = { page = "machine" })
            }
            "machine" -> {
                SheetHead("机器", onBack = { page = "" })
                used.forEachIndexed { i, id ->
                    val online = state.machines[id]?.online == true
                    val has = c.agent in machineAgents(state.machines[id])
                    ListItem(
                        machineLabel(state, id), if (!online) "不在线，现在不能新建" else if (!has) "没有 ${agentName(c.agent)}，会改用 ${agentName(machineAgents(state.machines[id]).first())}" else null,
                        checked = c.machineId == id, enabled = online, last = i == used.size - 1,
                        onClick = { c = switchMachine(c, id, machineAgents(state.machines[id])); page = "" },
                        trailing = { Dot(if (online) p.green else p.gray) },
                    )
                }
            }
            "model" -> {
                SheetHead("模型", onBack = { page = "" })
                val ms = models
                when {
                    modelsError.isNotEmpty() -> Banner("没能读取模型列表，会用 ${agentName(c.agent)} 自己的默认模型：$modelsError", tone = Tone2.WARN)
                    ms == null -> Text("读取 ${machineLabel(state, c.machineId)} 的设置…", style = T.sm, color = p.muted, modifier = Modifier.padding(vertical = 12.dp))
                    else -> {
                        ListItem("默认" + if (ms.defaultModel.isNotEmpty()) "（${ms.defaultModel}）" else "", checked = c.model.isEmpty(), onClick = { c = c.copy(model = "", effort = "") })
                        ms.modelsList.forEachIndexed { i, m ->
                            ListItem(m.displayName.ifEmpty { m.slug }, checked = c.model == m.slug, last = i == ms.modelsCount - 1, onClick = {
                                val noAuto = c.agent == Agent.AGENT_CLAUDE && m.slug.contains("haiku", ignoreCase = true)
                                c = c.copy(model = m.slug, effort = m.defaultEffort, mode = c.mode?.takeUnless { noAuto && it == Mode.AUTO })
                            })
                        }
                        ms.modelsList.firstOrNull { it.slug == c.model }?.takeIf { it.effortsList.isNotEmpty() }?.let { m ->
                            FieldLabel("推理强度")
                            ChoiceGrid(m.effortsList, c.effort, true) { c = c.copy(effort = it) }
                        }
                    }
                }
            }
            "mode" -> {
                SheetHead("审批模式", onBack = { page = "" })
                ListItem("默认", modeLabel(null, defaults, c.agent), checked = c.mode == null, onClick = { c = c.copy(mode = null); page = "" })
                newSessionModes.forEachIndexed { i, m ->
                    // Claude Code's haiku has no auto mode.
                    val off = m == Mode.AUTO && c.agent == Agent.AGENT_CLAUDE && c.model.contains("haiku", ignoreCase = true)
                    ListItem(m.text, if (off) "当前 Haiku 模型不支持" else modeHelp.getValue(m).replace("CLI", agentName(c.agent)), checked = c.mode == m, enabled = !off, last = i == newSessionModes.size - 1, onClick = { c = c.copy(mode = m); page = "" })
                }
                Hint("完全放行只能沿用电脑上的默认设置，发送前会再确认一次。")
            }
        }
    }
}

/** The list of 新会话 · 选项目: 常用 first, then the others, and 新项目…. */
@Composable
fun ProjectPickList(state: RemoteState, onPick: (String) -> Unit, onNew: () -> Unit) {
    val p = pal
    val groups = io.github.lewin671.remoteagent.core.projectGroups(state)
    @Composable
    fun row(pr: io.github.lewin671.remoteagent.core.Project) {
        Row(Modifier.fillMaxWidth().heightIn(min = 52.dp).bottomLine(p.line).clickable { onPick(pr.key) }, verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Avatar(pr.key, 22)
            Column(Modifier.weight(1f)) {
                Text(pr.name, style = T.bodyB, color = p.fg)
                val ms = pr.sessions.map { it.machineId }.distinct().joinToString("、") { id ->
                    machineLabel(state, id) + if (state.machines[id]?.online == true) "" else "（不在线）"
                }
                Text(shortPath(pr.key) + if (ms.isNotEmpty()) " · $ms" else "", style = T.meta, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            Text("›", style = ts(18.sp, lineHeight = 1f), color = p.faint)
        }
    }
    if (groups.pinned.isNotEmpty()) FieldLabel("常用项目")
    groups.pinned.forEach { row(it) }
    if (groups.others.isNotEmpty()) FieldLabel(if (groups.pinned.isNotEmpty()) "其他项目" else "项目")
    groups.others.forEach { row(it) }
    Row(Modifier.fillMaxWidth().heightIn(min = 52.dp).clickable(onClick = onNew), verticalAlignment = Alignment.CenterVertically) {
        Text("＋ 新项目…", style = T.bodyB, color = p.blue)
        Text("  选机器和目录", style = T.meta, color = p.muted)
    }
}

/**
 * 新会话 · 选项目 from the ＋ on the home page, as a sheet; 新项目… turns it
 * into 新项目 · 选目录. Choosing opens the project with its box focused.
 */
@Composable
fun PickProjectSheet(state: RemoteState, onClose: () -> Unit) {
    val nav = LocalNav.current
    var browsing by remember { mutableStateOf(false) }
    Sheet(onClose) {
        if (!browsing) {
            SheetHead("新会话 · 选项目", "取消", onClose)
            ProjectPickList(state, onPick = { onClose(); nav.projectNew(it) }, onNew = { browsing = true })
        } else {
            SheetHead("新项目 · 选目录", "取消", onClose, onBack = { browsing = false })
            DirPicker(state) { machine, path -> onClose(); nav.newProjectAt(machine, path) }
        }
    }
}

/** 新会话 · 选项目 as a page, for links that name the route. */
@Composable
fun PickProjectScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    PageScaffold("新会话 · 选项目") {
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 16.dp)) {
            ProjectPickList(state, onPick = { nav.projectNew(it) }, onNew = { nav.newProject() })
        }
    }
}

/**
 * Choose a machine (offline ones greyed), then browse to a folder; [onChosen]
 * with the folder. A project appears in the lists once its first session starts.
 */
@Composable
fun DirPicker(state: RemoteState, onChosen: (machine: String, path: String) -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    var machine by rememberSaveable { mutableStateOf(state.machines.values.firstOrNull { it.online }?.id.orEmpty()) }
    var path by remember { mutableStateOf("") }
    var dirs by remember { mutableStateOf<ListDirectoriesResponse?>(null) }
    var error by remember { mutableStateOf("") }
    var attempt by remember { mutableStateOf(0) }
    LaunchedEffect(machine, path, attempt) {
        if (machine.isEmpty()) return@LaunchedEffect
        dirs = null
        error = ""
        try {
            val r = remote.machineCommand(machine, remote.cmd { listDirectories = ListDirectoriesRequest.newBuilder().setPath(path).build() })
            dirs = r.listDirectories
        } catch (e: Exception) {
            error = errorText(e)
        }
    }
    Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(bottom = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        Text("机器", style = T.meta, color = p.muted)
        for (m in state.machines.values) {
            val on = machine == m.id
            Row(
                Modifier.heightIn(min = 44.dp).alpha(if (m.online) 1f else .45f).clickable(enabled = m.online) { machine = m.id; path = "" },
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Row(
                    Modifier.clip(RoundedCornerShape(14.dp)).background(if (on) p.blueSoft else p.chip).then(if (on) Modifier.border(1.5.dp, p.blue, RoundedCornerShape(14.dp)) else Modifier)
                        .padding(horizontal = 10.dp, vertical = 4.dp),
                    verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(5.dp),
                ) {
                    Dot(if (m.online) p.green else p.gray)
                    Text(machineLabel(state, m.id), style = T.sm, color = if (on) p.blue else p.fg2)
                }
            }
        }
    }
    val d = dirs
    when {
        machine.isEmpty() -> Banner("没有在线的电脑", tone = Tone2.WARN)
        error.isNotEmpty() -> Banner("读不了这个目录：$error", tone = Tone2.ERROR, action = "重试", onAction = { attempt++ })
        d == null -> Text("读取中…", style = T.sm, color = p.muted, modifier = Modifier.padding(vertical = 12.dp))
        else -> {
            val parts = shortPath(d.path).split("/").filter { it.isNotEmpty() }
            Text(
                androidx.compose.ui.text.buildAnnotatedString {
                    parts.forEachIndexed { i, part ->
                        if (i > 0) append(" / ")
                        if (i == parts.size - 1) { pushStyle(androidx.compose.ui.text.SpanStyle(color = p.fg, fontWeight = androidx.compose.ui.text.font.FontWeight.SemiBold)); append(part); pop() } else append(part)
                    }
                },
                style = T.mono13, color = p.muted, modifier = Modifier.padding(bottom = 6.dp),
            )
            val rows = buildList<Pair<String, () -> Unit>> {
                if (d.parent.isNotEmpty()) add("‹ 上一级" to { path = d.parent })
                for (name in d.dirsList) add(name to { path = d.path.trimEnd('/') + "/" + name })
            }
            if (d.dirsList.isEmpty()) Text("没有子目录", style = T.sm, color = p.muted, modifier = Modifier.padding(vertical = 8.dp))
            rows.forEachIndexed { i, (label, go) ->
                ListItem(label, last = i == rows.size - 1, onClick = go, trailing = if (label.startsWith("‹")) null else ({ Text("›", style = T.body, color = p.muted) }))
            }
            Btn("用 ${projectName(d.path)} 这个目录", { onChosen(machine, d.path) }, Modifier.fillMaxWidth().padding(top = 10.dp), BtnKind.PRIMARY)
        }
    }
}

/** 新项目 · 选目录 as a page (from the empty home page). */
@Composable
fun NewProjectScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    PageScaffold("新项目 · 选目录") {
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 16.dp)) {
            DirPicker(state) { machine, path -> nav.newProjectAt(machine, path) }
        }
    }
}

/** A folder chosen for a new project: its box, focused, with that machine's defaults. */
@Composable
fun NewProjectAtScreen(machine: String, path: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    Column(Modifier.fillMaxSize().background(p.card).imePadding()) {
        NavBar(projectName(path), nav.back, subtitle = shortPath(path) + " · " + machineLabel(state, machine))
        Text("项目在第一个会话开始后才出现在列表里。", style = T.sm, color = p.muted, modifier = Modifier.padding(16.dp).weight(1f))
        NewSessionBox(state, NewConfig(machine, path, machineAgents(state.machines[machine]).first()), projectName(path), focus = true)
    }
}

/** New-session starts per project, for this run of the app: one whose outcome is unknown is retried as it was. */
object Starts {
    class Start {
        val attempt = io.github.lewin671.remoteagent.core.Attempt()
        var prompt = ""
        /** The images the prepared request names, kept for a retry of the same request. */
        var taken: List<PendingImage> = emptyList()
    }

    private val byProject = HashMap<String, Start>()

    fun of(project: String): Start = byProject.getOrPut(project) { Start() }

    fun clear() = byProject.clear()
}
