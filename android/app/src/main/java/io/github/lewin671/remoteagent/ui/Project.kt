package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
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

/** The daemon refused a start because the folder's default is full access and nobody confirmed it. */
fun refusedForFullAccess(e: Throwable): Boolean =
    e is io.github.lewin671.remoteagent.core.CommandException && e.fromDaemon && e.code == "failed_precondition" &&
        (e.message ?: "").contains("full access", ignoreCase = true)

@Composable
fun ProjectScreen(key: String, focus: Boolean) {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val p = pal
    val t = now()
    val sessions = state.sessions.values.filter { projectKey(it) == key }.sortedByDescending { it.updatedMs }
    val active = sessions.filter { !exited(it) }
    val ended = sessions.filter { exited(it) }
    var showEnded by rememberSaveable { mutableStateOf(false) }
    val machines = sessions.map { machineLabel(state, it.machineId) }.distinct()
    PageScaffold("") {
        LazyColumn(Modifier.weight(1f)) {
            item {
                Row(Modifier.padding(horizontal = 16.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                    Avatar(key, 44)
                    Spacer(Modifier.width(12.dp))
                    Column {
                        Text(projectName(key), style = MaterialTheme.typography.headlineSmall)
                        Text((listOf(shortPath(key)) + machines).joinToString(" · "), style = MaterialTheme.typography.bodySmall, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    }
                }
                Row(Modifier.padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text("${active.size} 个进行中 · ", style = MaterialTheme.typography.bodyMedium, color = p.muted)
                    Text("已结束 ${ended.size} ›", style = MaterialTheme.typography.bodyMedium, color = p.blue, modifier = Modifier.clickable { showEnded = !showEnded }.padding(vertical = 12.dp))
                }
            }
            if (active.isNotEmpty()) {
                item { SectionHeader("进行中", active.size) }
                item { Card(Modifier.padding(horizontal = 12.dp)) { Column { active.forEachIndexed { i, s -> SessionRow(state, s, t, i > 0, showProject = false) } } } }
            }
            val recent = if (showEnded) ended else ended.take(3)
            if (recent.isNotEmpty()) {
                item { SectionHeader(if (showEnded) "已结束" else "最近结束", if (showEnded) ended.size else null) }
                item { Card(Modifier.padding(horizontal = 12.dp)) { Column { recent.forEachIndexed { i, s -> SessionRow(state, s, t, i > 0, showProject = false) } } } }
            }
            item { Spacer(Modifier.height(12.dp)) }
        }
        // A hidden project stays reachable from its sessions and
        // notifications; starting a new session there restores it first.
        if (io.github.lewin671.remoteagent.core.isHidden(state, key)) HiddenBanner(state, key)
        else NewSessionCard(state, projectConfig(state, key), projectName(key), focus)
    }
}

@Composable
private fun HiddenBanner(state: RemoteState, key: String) {
    val p = pal
    val remote = LocalRemote.current
    val change = PrefsChanges.states[key]
    Column(Modifier.fillMaxWidth().background(p.card).padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Text("这个项目已从列表移除。", style = MaterialTheme.typography.titleSmall)
        Text("会话和提醒照常；要在这里新建会话，先把它恢复到项目列表。", style = MaterialTheme.typography.bodySmall, color = p.muted)
        val blocked = prefsBlocked(state)
        if (blocked != null) Text(blocked, style = MaterialTheme.typography.bodySmall, color = p.orangeInk)
        if (change is PrefsChanges.State.Failed) Text("没能保存：${change.error}", style = MaterialTheme.typography.bodySmall, color = p.red)
        Button(
            onClick = { PrefsChanges.run(remote, io.github.lewin671.remoteagent.core.PrefsOp.Unhide(key)) },
            enabled = blocked == null && change != PrefsChanges.State.Saving,
            modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp),
        ) { Text(if (change == PrefsChanges.State.Saving) "正在恢复…" else "恢复到项目列表") }
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
 * The new-session card: pick the CLI, write, send. The configuration follows
 * the project until a draft is begun, then stays (architecture §8): a
 * machine that goes offline is replaced only by the user's choice.
 */
@Composable
fun NewSessionCard(state: RemoteState, initial: NewConfig?, name: String, focus: Boolean) {
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
    var change by remember { mutableStateOf(false) }
    var pickCli by remember { mutableStateOf(false) }
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
    var confirmFull by remember { mutableStateOf<Pair<NewConfig, Boolean>?>(null) }
    fun launch(cfg: NewConfig, mode: ApprovalMode, terminalOnly: Boolean, confirmedFull: Boolean = false) {
        // While an earlier start's outcome is unknown, only it is sent
        // again, as it was: the daemon then answers from its log instead
        // of starting a second session.
        val start = Starts.of(key)
        act.run({
            try {
                val retry = start.attempt.frozen
                val prepared = start.attempt.next {
                    start.prompt = if (terminalOnly) "" else text.trim()
                    // The images this request carries; any picked after it stay in the draft.
                    start.taken = if (terminalOnly) emptyList() else images.images.filter { it.readyFor(cfg.machineId) }
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
                    confirmFull = cfg to terminalOnly
                    return@run null
                }
                throw err
            }
        }) { r ->
            r?.createSession?.session?.id?.let {
                Outbox.rememberPrompt(it, start.prompt)
                if (terminalOnly) nav.sessionTerminal(it) else nav.session(it)
            }
            if (!terminalOnly) {
                if (text.trim() == start.prompt) text = ""
                val taken = start.taken
                images.detach(taken)
                taken.forEach { it.release() }
            }
            start.taken = emptyList()
        }
    }
    fun start(terminalOnly: Boolean) {
        val cfg = config ?: return
        val st = Starts.of(key)
        // Other content is a new start; the unknown one is given up.
        if (st.attempt.frozen && (if (terminalOnly) "" else text.trim()) != st.prompt) st.attempt.abandon()
        // An unknown start is resent as prepared, with the mode it had.
        if (st.attempt.frozen) return launch(cfg, ApprovalMode.APPROVAL_MODE_UNSPECIFIED, terminalOnly)
        when (val m = launchMode(cfg.mode, defaultModeOf(state.machines[cfg.machineId], cfg.agent))) {
            is LaunchMode.Send -> launch(cfg, m.mode, terminalOnly)
            LaunchMode.ConfirmFull -> confirmFull = cfg to terminalOnly
        }
    }
    val c = config
    val agents = machineAgents(c?.let { state.machines[it.machineId] })
    Column(Modifier.fillMaxWidth().imePadding().background(p.card).padding(12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("＋ 新会话", style = MaterialTheme.typography.titleSmall, color = p.fg2, modifier = Modifier.weight(1f))
            if (c != null) {
                // The CLI is chosen here, not in a second-level sheet; one installed CLI is just named.
                if (agents.size > 1) Chip(agentName(c.agent) + " ▾") { pickCli = true }
                else Chip(agentName(c.agent))
            }
        }
        if (c != null && state.machines[c.machineId]?.online != true && drafting) OfflineChoice(state, c, name) { draft.choose(it) }
        Column(Modifier.padding(top = 6.dp).background(p.soft, RoundedCornerShape(20.dp)).padding(horizontal = 6.dp, vertical = 4.dp)) {
            ImageStrip(images, remote, machine, maxImages(machine?.let { state.machines[it] }))
            Row(verticalAlignment = Alignment.Bottom) {
                ImageAddButton(images, remote, machine, blocked, maxImages(machine?.let { state.machines[it] }))
                androidx.compose.foundation.text.BasicTextField(
                    text, { text = it },
                    Modifier.weight(1f).heightIn(min = 40.dp).padding(horizontal = 6.dp, vertical = 10.dp).focusRequester(fr),
                    textStyle = MaterialTheme.typography.bodyLarge.copy(color = p.fg),
                    cursorBrush = androidx.compose.ui.graphics.SolidColor(p.blue),
                    maxLines = 6,
                    decorationBox = { inner ->
                        androidx.compose.foundation.layout.Box {
                            if (text.isEmpty()) Text("在 $name 里开始一个任务…", style = MaterialTheme.typography.bodyLarge, color = p.faint)
                            inner()
                        }
                    },
                )
                FilledIconButton(
                    onClick = { start(terminalOnly = false) },
                    enabled = c != null && !act.running && machineOnline && drafting &&
                        (images.images.isEmpty() || images.ready(machine)) &&
                        true,
                    modifier = Modifier.size(44.dp),
                    colors = IconButtonDefaults.filledIconButtonColors(containerColor = p.blueBg),
                ) { Icon(Icons.Default.ArrowUpward, "开始", tint = p.card) }
            }
        }
        // Claude Code records full model IDs; name them as its list does.
        val models by androidx.compose.runtime.produceState<ListModelsResponse?>(null, c?.machineId, c?.agent, machineOnline) {
            value = if (c != null && machineOnline) runCatching { remote.listModels(c.machineId, c.agent) }.getOrNull() else null
        }
        if (c != null) Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(top = 4.dp)) {
            val shown = models?.let { ms -> io.github.lewin671.remoteagent.core.findModel(ms.modelsList, c.model) { it.slug } }
            val (l1, l2) = configLines(state, shown?.let { c.copy(model = it.displayName.ifEmpty { it.slug }) } ?: c, defaults)
            Column(Modifier.weight(1f)) {
                Text(l1, style = MaterialTheme.typography.bodySmall, color = if (c.mode == null && (defaults.mode == Mode.FULL || !defaults.known)) p.red else p.muted)
                Text(l2, style = MaterialTheme.typography.bodySmall, color = p.muted)
            }
            TextButton(onClick = { change = true }) { Text("更改") }
        }
        if (c != null && state.machines[c.machineId]?.online != true && !drafting) Text("${machineLabel(state, c.machineId)} 不在线，现在不能新建", color = p.orangeInk, style = MaterialTheme.typography.bodySmall)

        if (act.running) Text("正在 ${c?.let { machineLabel(state, it.machineId) }} 上启动…", color = p.muted, style = MaterialTheme.typography.bodySmall)
        val frozen = Starts.of(key)
        // An unknown start has its own notice below.
        if (act.error.isNotEmpty() && !frozen.attempt.frozen) Text("启动失败：${act.error}", color = p.red, style = MaterialTheme.typography.bodySmall)
        if (!act.running && frozen.attempt.frozen) {
            Text(
                if (frozen.attempt.expired) "不确定「${frozen.prompt.take(40)}」有没有启动，它已过期，不会再重发。请先看看进行中的会话"
                else "上次的启动结果不确定。再点 ↑ 会重发同一个请求（「${frozen.prompt.take(40)}」），电脑收到过就不会再启动一次",
                color = p.orangeInk, style = MaterialTheme.typography.bodySmall,
            )
            var gaveUp by remember { mutableStateOf(0) }
            TextButton(onClick = { frozen.attempt.abandon(); act.error = ""; gaveUp++ }) { Text("放弃这次启动") }
        }
        if (c != null && !drafting) TextButton(
            onClick = { start(terminalOnly = true) },
            enabled = machineOnline && !act.running,
        ) { Text("只打开终端 ›") }
    }
    confirmFull?.let { (cfg, terminalOnly) ->
        val d = defaultModeOf(state.machines[cfg.machineId], cfg.agent)
        Confirm(
            "以完全放行启动？",
            "${machineLabel(state, cfg.machineId)} 上 ${agentName(cfg.agent)} " +
                (if (d.known && d.mode == Mode.FULL) "的默认审批模式是完全放行" else "在这个目录里的默认审批模式可能是完全放行") +
                "：这个会话里执行命令、改文件都不会再问你。不想这样可以在「更改」里选询问或自动。",
            "完全放行启动", danger = true,
            onDismiss = { confirmFull = null },
        ) { launch(cfg, ApprovalMode.APPROVAL_MODE_UNSPECIFIED, terminalOnly, confirmedFull = true) }
    }
    if (change && c != null) ConfigSheet(state, c, onDone = { draft.choose(it); change = false }, onClose = { change = false })
    if (pickCli && c != null) CliSheet(state, c, onPick = { draft.choose(switchAgent(c, it)); pickCli = false }, onClose = { pickCli = false })
}

/** 新会话使用的 CLI: only what the machine has installed; the draft stays. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun CliSheet(state: RemoteState, c: NewConfig, onPick: (Agent) -> Unit, onClose: () -> Unit) {
    val p = pal
    val m = state.machines[c.machineId]
    ModalBottomSheet(onDismissRequest = onClose, sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)) {
        Column(Modifier.padding(horizontal = 16.dp).padding(bottom = 24.dp)) {
            Text("新会话使用的 CLI", style = MaterialTheme.typography.titleMedium)
            for (a in machineAgents(m)) Row44(onClick = { onPick(a) }) {
                Column(Modifier.weight(1f)) {
                    Text(agentName(a), style = MaterialTheme.typography.titleSmall)
                    Text("${machineLabel(state, c.machineId)} 上已安装", style = MaterialTheme.typography.bodySmall, color = p.muted)
                }
                if (a == c.agent) Text("✓", color = p.blue)
            }
            Text("切换后采用该 CLI 的默认模型和有效模式，草稿保留。", style = MaterialTheme.typography.bodySmall, color = p.muted)
            val missing = Agent.entries.filter { it != Agent.AGENT_UNSPECIFIED && it != Agent.UNRECOGNIZED && it !in machineAgents(m) }
            for (a in missing) Text("${machineLabel(state, c.machineId)} 没有 ${agentName(a)}，要用它请在「更改」里换机器", style = MaterialTheme.typography.bodySmall, color = p.muted)
        }
    }
}

/**
 * The machine went offline while a draft was written: nothing moves by
 * itself. The user picks another machine the project used, told what that
 * machine offers, and checks the card again before sending.
 */
@Composable
private fun OfflineChoice(state: RemoteState, c: NewConfig, name: String, onChoose: (NewConfig) -> Unit) {
    val p = pal
    val others = state.sessions.values.filter { projectKey(it) == c.cwd }.map { it.machineId }.distinct()
        .ifEmpty { state.machines.keys.toList() }.filter { it != c.machineId && state.machines[it]?.online == true }
    Column(Modifier.fillMaxWidth().padding(vertical = 6.dp).background(p.orangeSoft, RoundedCornerShape(12.dp)).padding(12.dp)) {
        Text("${machineLabel(state, c.machineId)} 已离线，任务尚未开始", style = MaterialTheme.typography.titleSmall, color = p.orangeInk)
        Text("草稿和配置保留，不自动换机器或 CLI。", style = MaterialTheme.typography.bodySmall, color = p.orangeInk)
        for (id in others) {
            val agents = machineAgents(state.machines[id])
            val cliNote = when {
                c.agent in agents -> "有 ${agentName(c.agent)}；模型和强度会按这台机器重新选择"
                else -> "只有 ${agents.joinToString("、") { agentName(it) }}；会改用 ${agentName(agents.first())}，模型和模式按这台机器重新选择"
            }
            Row44(onClick = { onChoose(switchMachine(c, id, agents)) }) {
                Column(Modifier.weight(1f)) {
                    Text("改用 ${machineLabel(state, id)}", style = MaterialTheme.typography.titleSmall)
                    Text(cliNote, style = MaterialTheme.typography.bodySmall, color = p.muted)
                }
                Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = p.faint)
            }
        }
        if (others.isEmpty()) Text("$name 用过的其他电脑也都不在线。", style = MaterialTheme.typography.bodySmall, color = p.muted)
        Text("换机器后回到新建卡片，检查配置，再点发送。", style = MaterialTheme.typography.bodySmall, color = p.muted)
    }
}

/** 这次的设置: machine (those the project used), folder, CLI, model, effort and mode. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ConfigSheet(state: RemoteState, initial: NewConfig, onDone: (NewConfig) -> Unit, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    var c by remember { mutableStateOf(initial) }
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
    ModalBottomSheet(onDismissRequest = onClose, sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)) {
        Column(Modifier.padding(horizontal = 16.dp).padding(bottom = 24.dp).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("这次的设置", style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                TextButton(onClick = { onDone(c) }) { Text("完成") }
            }
            Text("机器", style = MaterialTheme.typography.titleSmall)
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                for (id in used) {
                    val online = state.machines[id]?.online == true
                    val has = c.agent in machineAgents(state.machines[id])
                    FilterChip(c.machineId == id, { if (online) c = switchMachine(c, id, machineAgents(state.machines[id])) },
                        { Text(machineLabel(state, id) + if (!online) "（不在线）" else if (!has) "（没有 ${agentName(c.agent)}）" else "") }, enabled = online)
                }
            }
            Text("目录", style = MaterialTheme.typography.titleSmall)
            Text(shortPath(c.cwd), style = MaterialTheme.typography.bodyMedium)
            val agents = machineAgents(state.machines[c.machineId])
            if (agents.size > 1) {
                Text("CLI", style = MaterialTheme.typography.titleSmall)
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    for (a in agents) FilterChip(c.agent == a, { c = switchAgent(c, a) }, { Text(agentName(a)) })
                }
            }
            Text("模型", style = MaterialTheme.typography.titleSmall)
            val ms = models
            when {
                modelsError.isNotEmpty() -> Text("没能读取模型列表，会用 ${agentName(c.agent)} 自己的默认模型：$modelsError", color = p.orangeInk, style = MaterialTheme.typography.bodySmall)
                ms == null -> Text("读取 ${machineLabel(state, c.machineId)} 的设置…", color = p.muted)
                else -> {
                    Column {
                        Row44(onClick = { c = c.copy(model = "", effort = "") }) {
                            Text("默认" + if (ms.defaultModel.isNotEmpty()) "（${ms.defaultModel}）" else "", Modifier.weight(1f))
                            if (c.model.isEmpty()) Text("✓", color = p.blue)
                        }
                        for (m in ms.modelsList) Row44(onClick = {
                            val noAuto = c.agent == Agent.AGENT_CLAUDE && m.slug.contains("haiku", ignoreCase = true)
                            c = c.copy(model = m.slug, effort = m.defaultEffort, mode = c.mode?.takeUnless { noAuto && it == Mode.AUTO })
                        }) {
                            Text(m.displayName.ifEmpty { m.slug }, Modifier.weight(1f))
                            if (c.model == m.slug) Text("✓", color = p.blue)
                        }
                    }
                    ms.modelsList.firstOrNull { it.slug == c.model }?.takeIf { it.effortsList.isNotEmpty() }?.let { m ->
                        Text("推理强度", style = MaterialTheme.typography.titleSmall)
                        Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                            for (e in m.effortsList) FilterChip(c.effort == e, { c = c.copy(effort = e) }, { Text(e) })
                        }
                    }
                }
            }
            Text("审批", style = MaterialTheme.typography.titleSmall)
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                FilterChip(c.mode == null, { c = c.copy(mode = null) }, { Text("默认") })
                for (m in newSessionModes) {
                    // Claude Code's haiku has no auto mode.
                    val off = m == Mode.AUTO && c.agent == Agent.AGENT_CLAUDE && c.model.contains("haiku", ignoreCase = true)
                    FilterChip(c.mode == m, { c = c.copy(mode = m) }, { Text(m.text + if (off) "（Haiku 不支持）" else "") }, enabled = !off)
                }
            }
            Text("默认沿用这个项目最近一个会话。改了之后，这个项目下次也会沿用新的设置。", style = MaterialTheme.typography.bodySmall, color = p.muted)
        }
    }
}

/** 新会话 · 选项目, reached from the ＋ on the home page. */
@Composable
fun PickProjectScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    PageScaffold("新会话 · 选项目") {
        val groups = io.github.lewin671.remoteagent.core.projectGroups(state)
        LazyColumn {
            if (groups.pinned.isNotEmpty()) item { SectionHeader("常用项目") }
            items(groups.pinned + groups.others, key = { it.key }) { pr ->
                if (groups.pinned.isNotEmpty() && pr == groups.others.firstOrNull()) SectionHeader("其他项目")
                Row44(onClick = { nav.projectNew(pr.key) }) {
                    Avatar(pr.key)
                    Column(Modifier.weight(1f)) {
                        Text(pr.name, style = MaterialTheme.typography.titleSmall)
                        val ms = pr.sessions.map { it.machineId }.distinct().joinToString("、") { id ->
                            machineLabel(state, id) + if (state.machines[id]?.online == true) "" else "（不在线）"
                        }
                        Text(shortPath(pr.key) + " · " + ms, style = MaterialTheme.typography.bodySmall, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    }
                    Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = p.faint)
                }
            }
            item {
                Row44(onClick = { nav.newProject() }) {
                    Column(Modifier.weight(1f)) {
                        Text("＋ 新项目…", style = MaterialTheme.typography.titleSmall, color = p.blue)
                        Text("选机器和目录", style = MaterialTheme.typography.bodySmall, color = p.muted)
                    }
                }
            }
        }
    }
}

/** 新项目: choose a machine (offline ones greyed), then browse to a folder. */
@Composable
fun NewProjectScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    var machine by rememberSaveable { mutableStateOf(state.machines.values.firstOrNull { it.online }?.id.orEmpty()) }
    var path by remember { mutableStateOf("") }
    var dirs by remember { mutableStateOf<ListDirectoriesResponse?>(null) }
    var error by remember { mutableStateOf("") }
    var attempt by remember { mutableStateOf(0) }
    var chosen by remember { mutableStateOf<String?>(null) }
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
    val ch = chosen
    if (ch != null) {
        PageScaffold(projectName(ch)) {
            Spacer(Modifier.weight(1f))
            NewSessionCard(state, NewConfig(machine, ch, machineAgents(state.machines[machine]).first()), projectName(ch), focus = true)
        }
        return
    }
    PageScaffold("新项目 · 选目录") {
        Column(Modifier.padding(horizontal = 16.dp)) {
            Text("机器", style = MaterialTheme.typography.titleSmall)
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                for (m in state.machines.values) {
                    FilterChip(machine == m.id, { machine = m.id; path = "" }, { Text(machineLabel(state, m.id)) }, enabled = m.online)
                }
            }
        }
        val d = dirs
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            when {
                machine.isEmpty() -> Text("没有在线的电脑", Modifier.padding(16.dp), color = p.muted)
                error.isNotEmpty() -> Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text("读不了这个目录：$error", color = p.red, modifier = Modifier.weight(1f))
                    TextButton(onClick = { attempt++ }) { Text("重试") }
                }
                d == null -> Text("读取中…", Modifier.padding(16.dp), color = p.muted)
                else -> {
                    Text(shortPath(d.path), Modifier.padding(16.dp), style = MaterialTheme.typography.titleSmall)
                    if (d.parent.isNotEmpty()) Row44(onClick = { path = d.parent }) { Text("‹ 上一级", color = p.blue) }
                    if (d.dirsList.isEmpty()) Text("没有子目录", Modifier.padding(16.dp), color = p.muted)
                    for (name in d.dirsList) Row44(onClick = { path = d.path.trimEnd('/') + "/" + name }) {
                        Text(name, Modifier.weight(1f))
                        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = p.faint)
                    }
                }
            }
        }
        if (d != null) Button(onClick = { chosen = d.path }, modifier = Modifier.fillMaxWidth().padding(12.dp).heightIn(min = 48.dp)) {
            Text("用 ${projectName(d.path)} 这个目录")
        }
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
