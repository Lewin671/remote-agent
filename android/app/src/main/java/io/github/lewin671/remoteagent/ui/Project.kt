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
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.withStyle
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
import io.github.lewin671.remoteagent.proto.ProjectSettings

/**
 * The modes a new session can be started in besides the default. Neither CLI
 * starts in plan, and full access is only ever the confirmed machine default.
 */
val newSessionModes = listOf(Mode.ASK, Mode.AUTO, Mode.FULL)

/** How a new session starts; empty model, effort and mode mean the machine's defaults. */
data class NewConfig(
    val machineId: String,
    val cwd: String,
    val agent: Agent = Agent.AGENT_CODEX,
    val model: String = "",
    val effort: String = "",
    val mode: Mode? = null,
    /** Claude Code: the standard context window instead of 1M. */
    val standardContext: Boolean = false,
)

/**
 * The configuration follows the project (architecture §13): the latest
 * session's machine, folder, CLI, model, effort and mode. Full access and
 * plan are not carried over. If that machine is offline, another online
 * machine the project used, with its defaults.
 */
fun projectConfig(state: RemoteState, key: String): NewConfig? {
    state.prefs?.prefs?.settingsList?.firstOrNull { it.path == key }?.let {
        return NewConfig(it.machineId, key, effectiveAgent(it.agent), it.model, it.effort, modeOf(it.mode)?.takeUnless { m -> m == Mode.PLAN }, it.standardContext)
    }
    val sessions = state.sessions.values.filter { projectKey(it) == key }.sortedByDescending { it.updatedMs }
    val last = sessions.firstOrNull() ?: return null
    val mode = modeOf(last)?.takeUnless { it == Mode.PLAN }
    return NewConfig(last.machineId, key, last.agent(), last.info?.model.orEmpty(), last.info?.effort.orEmpty(), mode)
}

fun NewConfig.settings(now: Long): ProjectSettings = ProjectSettings.newBuilder().setPath(cwd).setMachineId(machineId)
    .setAgent(agent).setModel(model).setEffort(effort).setMode(mode?.proto ?: ApprovalMode.APPROVAL_MODE_UNSPECIFIED).setStandardContext(standardContext).setUpdatedMs(now).build()

/**
 * The mode a CLI gives a session started without one, as the daemon reads
 * it (AgentInfo.default_mode): null when not known (a configuration it
 * could not read).
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
    !d.known -> "CLI 配置（未读到）"
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

/** Mode sent for a new session; unspecified lets the daemon use the CLI configuration. */
sealed interface LaunchMode {
    /** Start in this mode; UNSPECIFIED leaves it to the CLI's default, which the daemon checks again in the folder. */
    data class Send(val mode: ApprovalMode) : LaunchMode

}

/**
 * Resolves the mode of a start. A chosen mode is sent as it is. Without one
 * the CLI's effective default on the machine decides (AgentInfo): full
 * access or an unknown default is left to the daemon without a dialog.
 */
fun launchMode(chosen: Mode?, d: DefaultMode): LaunchMode = LaunchMode.Send(chosen?.proto ?: ApprovalMode.APPROVAL_MODE_UNSPECIFIED)

/** The session a start created, or null when it did not. */
fun startedSession(r: io.github.lewin671.remoteagent.proto.CommandResult?): String? =
    r?.takeIf { it.hasCreateSession() && it.createSession.hasSession() }?.createSession?.session?.id?.takeIf { it.isNotEmpty() }

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
    val machines = sessions.sortedWith(compareByDescending<io.github.lewin671.remoteagent.core.SessionView> { it.updatedMs }.thenBy { it.id })
        .map { machineLabel(state, it.machineId) }.distinct()
    val ime = WindowInsets.ime.getBottom(LocalDensity.current) > 0
    var typing by remember { mutableStateOf(false) }
    val compact = ime && typing
    val hidden = io.github.lewin671.remoteagent.core.isHidden(state, key)
    var settingsOpen by remember { mutableStateOf(false) }
    val selected = projectConfig(state, key)
    var summaryModels by remember(key, selected?.machineId, selected?.agent) { mutableStateOf<ListModelsResponse?>(null) }
    LaunchedEffect(selected?.machineId, selected?.agent) {
        val cfg = selected ?: return@LaunchedEffect
        summaryModels = runCatching { remote.listModels(cfg.machineId, cfg.agent) }.getOrNull()
    }
    Column(Modifier.fillMaxSize().background(p.card).imePadding()) {
        NavBar(if (compact) projectName(key) else "", nav.back, subtitle = if (compact) shortPath(key) else null, more = { settingsOpen = true })
        LazyColumn(Modifier.weight(1f)) {
            if (!compact) item {
                Row(Modifier.padding(horizontal = 16.dp).padding(bottom = 2.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
                    Avatar(key, 52)
                    Column(Modifier.weight(1f)) {
                        Text(projectName(key), style = T.h22, color = p.fg, maxLines = 2, overflow = TextOverflow.Ellipsis)
                        Text((listOf(shortPath(key)) + machines).joinToString(" · "), style = T.sm, color = p.muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
                        Row(Modifier.padding(top = 2.dp), verticalAlignment = Alignment.CenterVertically) {
                            Text("${active.size} 个进行中 · ", style = T.sm, color = p.muted)
                            Box(Modifier.clickable(enabled = ended.isNotEmpty()) { showEnded = !showEnded }) { ArrowText("已结束 ${ended.size}", p.blue, T.smB, down = showEnded) }
                        }
                        if (selected != null) {
                            val full = selected.mode == Mode.FULL || (selected.mode == null && defaultModeOf(state.machines[selected.machineId], selected.agent).mode == Mode.FULL)
                            SummaryChip(projectSummary(state, selected, summaryModels), full, Modifier.padding(top = 6.dp).testTag("project-settings-summary")) { settingsOpen = true }
                        }
                    }
                }
            }
            when (val change = PrefsChanges.states[key]) {
                PrefsChanges.State.Saving -> item { Banner("正在保存项目设置…", Modifier.padding(horizontal = 12.dp, vertical = 4.dp)) }
                is PrefsChanges.State.Failed -> item { Banner("项目设置没能保存：${change.error}", Modifier.padding(horizontal = 12.dp, vertical = 4.dp), tone = Tone2.ERROR,
                    action = "重试", onAction = { PrefsChanges.run(remote, change.op) }) }
                else -> {}
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
    if (settingsOpen && selected != null) NewSettingsSheet(state, selected, projectName(key), onDone = {
        PrefsChanges.run(remote, io.github.lewin671.remoteagent.core.PrefsOp.SetSettings(it.settings(System.currentTimeMillis())))
        Drafts.newSession(key, selected).config.choose(it)
        settingsOpen = false
    }, onClose = { settingsOpen = false })
}

/** The new-session settings as a button that opens the project's settings; only 完全放行 is red. */
@Composable
private fun SummaryChip(summary: String, full: Boolean, modifier: Modifier = Modifier, onClick: () -> Unit) {
    val p = pal
    val shown = buildAnnotatedString {
        val start = if (full) summary.indexOf(Mode.FULL.text) else -1
        if (start >= 0) {
            append(summary.substring(0, start))
            withStyle(SpanStyle(color = p.red)) { append(Mode.FULL.text) }
            append(summary.substring(start + Mode.FULL.text.length))
        } else append(summary)
    }
    Row(modifier.clip(RoundedCornerShape(10.dp)).background(p.chip).clickable(onClick = onClick).heightIn(min = 32.dp).padding(horizontal = 10.dp, vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(shown, style = T.sm, color = p.fg2, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
        Arrow(p.faint)
    }
}

fun projectSummary(state: RemoteState, c: NewConfig, models: ListModelsResponse? = null): String {
    val default = defaultModeOf(state.machines[c.machineId], c.agent)
    val mode = effectiveModeText(c.mode, default)
    val modelId = c.model.ifEmpty { models?.defaultModel.orEmpty() }
    val chosen = models?.let { io.github.lewin671.remoteagent.core.findModel(it.modelsList, modelId) { m -> m.slug } }
    return listOf(agentName(c.agent), mode,
        machineLabel(state, c.machineId), chosen?.displayName?.ifEmpty { modelId } ?: modelId.ifEmpty { "默认模型" },
        c.effort.ifEmpty { chosen?.defaultEffort.orEmpty() }, contextSummary(c, chosen?.contextWindow ?: 0)).filter { it.isNotEmpty() }.joinToString(" · ")
}

/** The context window new Claude Code sessions get, as the summary writes it: 1M unless the project asks for the standard one or the model has no larger one. */
fun contextSummary(c: NewConfig, modelWindow: Long): String {
    if (c.agent != Agent.AGENT_CLAUDE) return ""
    val standard = c.standardContext || modelWindow in 1..io.github.lewin671.remoteagent.core.STANDARD_WINDOW
    return io.github.lewin671.remoteagent.core.contextText(if (standard) io.github.lewin671.remoteagent.core.STANDARD_WINDOW else modelWindow.takeIf { it > 0 } ?: 1_000_000L).orEmpty()
}

fun effectiveModeText(chosen: Mode?, default: DefaultMode): String =
    chosen?.text ?: if (!default.known) "审批：CLI 配置（未读到）" else default.mode?.text ?: "CLI 自己的模式"

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
 * The new-session box (client-v8): ＋ adds images; configuration comes from
 * project settings and stays with an active draft. An offline machine is
 * replaced only by the user's choice for this send. The first message starts
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
    fun launch(cfg: NewConfig, mode: ApprovalMode) {
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
                            .setModel(cfg.model).setEffort(cfg.effort).setMode(mode).setStandardContext(cfg.agent == Agent.AGENT_CLAUDE && cfg.standardContext).addAllImageIds(ids).build()
                    })
                }
                remote.forward(prepared, retryOfUnknown = retry).also { start.attempt.settle() }
            } catch (e: Exception) {
                val err = start.attempt.settle(e) ?: e
                throw err
            }
        }) { r ->
            // Only a session that started takes the draft; a refusal that asks
            // for confirmation, or no answer, leaves text and images as they are.
            val sid = startedSession(r) ?: return@run
            if (state.prefs?.prefs?.settingsList?.any { it.path == key } != true)
                PrefsChanges.run(remote, io.github.lewin671.remoteagent.core.PrefsOp.SetSettings((initial ?: cfg).settings(System.currentTimeMillis())))
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
        launch(cfg, cfg.mode?.proto ?: ApprovalMode.APPROVAL_MODE_UNSPECIFIED)
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
    ComposeBox(
        text, { text = it }, "描述任务…",
        canSend = c != null && !act.running && machineOnline && drafting && (images.images.isEmpty() || images.ready(machine)),
        onSend = ::start,
        onPlus = pick, plusEnabled = blocked == null,
        sendLabel = "开始", focusRequester = fr, onFocus = onFocus,
        top = { ImageStrip(images, remote, machine, maxImages(machine?.let { state.machines[it] })) },
    )
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

/** A project-setting row: a short label, the value at the right, ›. */
@Composable
private fun SetRow(label: String, value: String, onClick: (() -> Unit)?, dot: androidx.compose.ui.graphics.Color? = null, valueColor: androidx.compose.ui.graphics.Color? = null) {
    val p = pal
    if (LocalDensity.current.fontScale >= 1.8f) {
        Column(Modifier.fillMaxWidth().heightIn(min = 44.dp).then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier).padding(horizontal = 14.dp, vertical = 8.dp)) {
            Text(label, style = T.sm, color = p.muted, maxLines = 1)
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.End) {
                if (dot != null) Dot(dot)
                Text(value, style = T.sm, color = valueColor ?: p.fg2, textAlign = androidx.compose.ui.text.style.TextAlign.End,
                    modifier = Modifier.padding(start = 8.dp).weight(1f, fill = false))
                Text("›", style = ts(18.sp, lineHeight = 1f), color = p.faint, modifier = Modifier.padding(start = 8.dp))
            }
        }
        return
    }
    Row(
        Modifier.fillMaxWidth().heightIn(min = 44.dp).then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier).padding(horizontal = 14.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Text(label, style = T.sm, color = p.muted, maxLines = 1, modifier = Modifier.widthIn(min = 36.dp).weight(1f))
        if (dot != null) Dot(dot)
        Text(value, style = T.sm, color = valueColor ?: p.fg2, maxLines = 2, overflow = TextOverflow.Ellipsis, textAlign = androidx.compose.ui.text.style.TextAlign.End, modifier = Modifier.widthIn(max = 200.dp))
        Text("›", style = ts(18.sp, lineHeight = 1f), color = p.faint)
    }
}

fun projectModel(c: NewConfig, models: ListModelsResponse, settings: DaemonSettings? = null): io.github.lewin671.remoteagent.proto.ModelInfo? {
    val defaultModel = if (c.agent == Agent.AGENT_CLAUDE) settings?.claude?.model.orEmpty() else settings?.defaultModel.orEmpty()
    val id = c.model.ifEmpty { defaultModel.ifEmpty { models.defaultModel } }
    return io.github.lewin671.remoteagent.core.findModel(models.modelsList, id) { it.slug }
}

/**
 * 项目设置 (client-v8): CLI, machine, model and effort, and approval mode,
 * each chosen on a page of the sheet. The folder is the project's read-only path.
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
    LaunchedEffect(c.machineId, c.agent, page == "model") {
        models = null
        modelsError = ""
        try {
            models = remote.listModels(c.machineId, c.agent, fresh = true)
        } catch (e: Exception) {
            modelsError = errorText(e)
        }
    }
    var settings by remember { mutableStateOf<DaemonSettings?>(null) }
    LaunchedEffect(c.machineId, c.agent, page == "model") {
        settings = null
        runCatching { remote.machineCommand(c.machineId, remote.cmd { getSettings = GetSettingsRequest.getDefaultInstance() }).getSettings.settings }
            .onSuccess { settings = it }
    }
    val used = state.sessions.values.filter { projectKey(it) == initial.cwd }.map { it.machineId }.distinct().ifEmpty { state.machines.keys.toList() }
    val agents = machineAgents(state.machines[c.machineId])
    val blockedPrefs = prefsBlocked(state)
    val defaults = defaultModeOf(state.machines[c.machineId], c.agent)
    val shownModel = models?.let { projectModel(c, it, settings) }
    Sheet(onClose) {
        when (page) {
            "" -> {
                SheetHead("项目设置", if (blockedPrefs == null) "完成" else null, { onDone(c) })
                if (blockedPrefs != null) Banner(blockedPrefs)
                Row(Modifier.fillMaxWidth().padding(bottom = 12.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    Avatar(c.cwd, 22)
                    Column(Modifier.weight(1f)) {
                        Text(name, style = T.bodyB, color = p.fg)
                        Text(shortPath(c.cwd), style = T.mono, color = p.muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
                    }
                }
                FieldLabel("新会话")
                Column(Modifier.fillMaxWidth().clip(Radii.MD).background(p.soft)) {
                    val rows = listOf<@Composable () -> Unit>(
                        { SetRow("CLI", agentName(c.agent), if (agents.size > 1) ({ page = "cli" }) else null) },
                        { SetRow("机器", machineLabel(state, c.machineId), { page = "machine" }, dot = if (state.machines[c.machineId]?.online == true) p.green else p.gray) },
                        {
                            SetRow("模型", listOf(shownModel?.displayName?.ifEmpty { null } ?: c.model, c.effort).filter { it.isNotEmpty() }.joinToString(" · ").ifEmpty { "默认模型" }, { page = "model" })
                        },
                        *(if (c.agent == Agent.AGENT_CLAUDE) arrayOf<@Composable () -> Unit>({
                            // A model without a larger window has nothing to choose.
                            val fixed = shownModel != null && shownModel.contextWindow in 1..io.github.lewin671.remoteagent.core.STANDARD_WINDOW
                            SetRow("上下文", if (fixed) "标准 200k · 这个模型只有标准窗口" else if (c.standardContext) "标准 200k" else "1M", if (fixed) null else ({ page = "context" }))
                        }) else emptyArray()),
                        { SetRow("审批", effectiveModeText(c.mode, defaults), { page = "mode" }, valueColor = if (c.mode == Mode.FULL || (c.mode == null && (defaults.mode == Mode.FULL || !defaults.known))) p.red else null) },
                    )
                    rows.forEachIndexed { i, r ->
                        if (i > 0) androidx.compose.material3.HorizontalDivider(color = p.line, thickness = 1.dp)
                        r()
                    }
                }
                Hint("只影响之后新建的会话，进行中的不变；在你的设备之间同步。")
                TextAction(if (io.github.lewin671.remoteagent.core.isPinned(state, c.cwd)) "取消常用" else "设为常用", {
                    PrefsChanges.run(remote, if (io.github.lewin671.remoteagent.core.isPinned(state, c.cwd)) io.github.lewin671.remoteagent.core.PrefsOp.Unpin(c.cwd) else io.github.lewin671.remoteagent.core.PrefsOp.Pin(c.cwd))
                }, enabled = blockedPrefs == null)
                TextAction("从项目列表移除", { PrefsChanges.run(remote, io.github.lewin671.remoteagent.core.PrefsOp.Hide(c.cwd)); onClose() }, enabled = blockedPrefs == null, color = p.red)
            }
            "cli" -> {
                SheetHead("新会话使用的 CLI", if (blockedPrefs == null) "完成" else null, { onDone(c) }, onBack = { page = "" })
                agents.forEachIndexed { i, a ->
                    ListItem(agentName(a), "${machineLabel(state, c.machineId)} 上已安装", checked = a == c.agent, last = i == agents.size - 1, onClick = { c = switchAgent(c, a); page = "" })
                }
                Hint("切换后采用该 CLI 的默认模型和有效模式，草稿保留。当前机器只有一种 CLI 时只显示名称。")
                val missing = Agent.entries.filter { it != Agent.AGENT_UNSPECIFIED && it != Agent.UNRECOGNIZED && it !in agents }
                for (a in missing) Banner("${machineLabel(state, c.machineId)} 没有 ${agentName(a)}", Modifier.padding(top = 8.dp), action = "换机器", onAction = { page = "machine" })
            }
            "machine" -> {
                SheetHead("机器", if (blockedPrefs == null) "完成" else null, { onDone(c) }, onBack = { page = "" })
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
                SheetHead("模型", if (blockedPrefs == null) "完成" else null, { onDone(c) }, onBack = { page = "" })
                val ms = models
                when {
                    modelsError.isNotEmpty() -> Banner("没能读取模型列表，会用 ${agentName(c.agent)} 自己的默认模型：$modelsError", tone = Tone2.WARN)
                    ms == null -> Text("读取 ${machineLabel(state, c.machineId)} 的设置…", style = T.sm, color = p.muted, modifier = Modifier.padding(vertical = 12.dp))
                    else -> {
                        val defaultModel = (if (c.agent == Agent.AGENT_CLAUDE) settings?.claude?.model.orEmpty() else settings?.defaultModel.orEmpty()).ifEmpty { ms.defaultModel }
                        ListItem("默认" + if (defaultModel.isNotEmpty()) "（$defaultModel）" else "", checked = c.model.isEmpty(), onClick = { c = c.copy(model = "", effort = "") })
                        ms.modelsList.forEachIndexed { i, m ->
                            ListItem(m.displayName.ifEmpty { m.slug }, checked = c.model == m.slug, last = i == ms.modelsCount - 1, onClick = {
                                val noAuto = c.agent == Agent.AGENT_CLAUDE && m.slug.contains("haiku", ignoreCase = true)
                                c = c.copy(model = m.slug, effort = m.defaultEffort, mode = c.mode?.takeUnless { noAuto && it == Mode.AUTO })
                            })
                        }
                        projectModel(c, ms, settings)?.takeIf { it.effortsList.isNotEmpty() }?.let { m ->
                            val defaultEffort = if (c.model.isEmpty()) {
                                (if (c.agent == Agent.AGENT_CLAUDE) settings?.claude?.effort.orEmpty() else settings?.defaultEffort.orEmpty()).ifEmpty { m.defaultEffort }
                            } else m.defaultEffort
                            FieldLabel("推理强度")
                            ChoiceGrid(m.effortsList, c.effort.ifEmpty { defaultEffort }, true) { c = c.copy(effort = it) }
                        }
                    }
                }
            }
            "context" -> {
                SheetHead("上下文窗口", if (blockedPrefs == null) "完成" else null, { onDone(c) }, onBack = { page = "" })
                ListItem("1M", "Claude Code 的默认窗口", checked = !c.standardContext, onClick = { c = c.copy(standardContext = false); page = "" })
                ListItem("标准 200k", "超过 200k 前自动压缩", checked = c.standardContext, last = true, onClick = { c = c.copy(standardContext = true); page = "" })
                Hint("窗口在会话开始时确定，改了只影响之后新建的会话。")
            }
            "mode" -> {
                SheetHead("审批模式", if (blockedPrefs == null) "完成" else null, { onDone(c) }, onBack = { page = "" })
                ListItem("默认", "跟随 ${agentName(c.agent)} 自己的配置", checked = c.mode == null, onClick = { c = c.copy(mode = null); page = "" })
                newSessionModes.forEachIndexed { i, m ->
                    // Claude Code's haiku has no auto mode.
                    val off = m == Mode.AUTO && c.agent == Agent.AGENT_CLAUDE && c.model.contains("haiku", ignoreCase = true)
                    ListItem(m.text, if (off) "当前 Haiku 模型不支持" else modeHelp.getValue(m).replace("CLI", agentName(c.agent)), checked = c.mode == m, enabled = !off, last = i == newSessionModes.size - 1, onClick = { c = c.copy(mode = m); page = "" })
                }
                Hint("完全放行的新会话直接按此设置启动。")
            }
        }
    }
}

/** The list of 新会话 · 选项目: 常用 first, then the others, and 新项目…. */
@Composable
fun ProjectPickList(state: RemoteState, onPick: (String) -> Unit, onNew: () -> Unit) {
    val p = pal
    val groups = io.github.lewin671.remoteagent.core.projectGroups(state)
    var search by remember { mutableStateOf("") }
    val all = groups.pinned + groups.others
    if (all.size > 8) OutlinedTextField(search, { search = it }, label = { Text("搜索项目") }, singleLine = true,
        modifier = Modifier.fillMaxWidth().padding(bottom = 8.dp))
    val match: (io.github.lewin671.remoteagent.core.Project) -> Boolean = { search.isBlank() || it.name.contains(search, true) || it.key.contains(search, true) }
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
    if (groups.pinned.any(match)) FieldLabel("常用项目")
    groups.pinned.filter(match).forEach { row(it) }
    if (groups.others.any(match)) FieldLabel(if (groups.pinned.isNotEmpty()) "其他项目" else "项目")
    groups.others.filter(match).forEach { row(it) }
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
                        if (i == parts.size - 1) { pushStyle(androidx.compose.ui.text.SpanStyle(color = p.fg, fontWeight = platformWeight(androidx.compose.ui.text.font.FontWeight.SemiBold))); append(part); pop() } else append(part)
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
    val initial = projectConfig(state, path) ?: NewConfig(machine, path, machineAgents(state.machines[machine]).first())
    val entry = Drafts.newSession(path, initial)
    val selected = entry.config.value ?: initial
    var summaryModels by remember(selected.machineId, selected.agent) { mutableStateOf<ListModelsResponse?>(null) }
    LaunchedEffect(selected.machineId, selected.agent) {
        summaryModels = runCatching { remote.listModels(selected.machineId, selected.agent) }.getOrNull()
    }
    var settingsOpen by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxSize().background(p.card).imePadding()) {
        NavBar(projectName(path), nav.back, subtitle = shortPath(path), more = { settingsOpen = true })
        SummaryChip(projectSummary(state, selected, summaryModels), selected.mode == Mode.FULL, Modifier.padding(horizontal = 16.dp, vertical = 4.dp)) { settingsOpen = true }
        Text("项目在第一个会话开始后才出现在列表里。", style = T.sm, color = p.muted, modifier = Modifier.padding(16.dp).weight(1f))
        NewSessionBox(state, initial, projectName(path), focus = true)
    }
    if (settingsOpen) NewSettingsSheet(state, selected, projectName(path), onDone = {
        entry.config.choose(it)
        PrefsChanges.run(remote, io.github.lewin671.remoteagent.core.PrefsOp.SetSettings(it.settings(System.currentTimeMillis())))
        settingsOpen = false
    }, onClose = { settingsOpen = false })
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
