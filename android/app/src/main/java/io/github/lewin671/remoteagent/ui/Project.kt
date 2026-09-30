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

fun configLine(state: RemoteState, c: NewConfig, settings: DaemonSettings?): String = listOf(
    machineLabel(state, c.machineId),
    agentName(c.agent),
    listOf(c.model, c.effort).filter { it.isNotEmpty() }.joinToString(" ").ifEmpty { "默认模型" },
    modeLabel(c.mode, settings, c.agent),
).joinToString(" · ")

/** The mode a start uses: chosen, or the machine's default for that CLI once it is known. */
fun modeLabel(chosen: Mode?, settings: DaemonSettings?, agent: Agent): String = when {
    chosen != null -> chosen.text
    settings == null -> "默认模式"
    else -> defaultMode(settings, agent)?.let { "默认：${it.text}" } ?: "默认模式（${agentName(agent)} 自己的配置）"
}

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
    /** Start in this mode, named explicitly so the session runs in what was shown. */
    data class Send(val mode: ApprovalMode) : LaunchMode

    /** The mode is full access: start only after the user confirms it (architecture §7). */
    data object ConfirmFull : LaunchMode

    /** The machine's default is not known yet, so neither is the mode. */
    data object Unknown : LaunchMode
}

/**
 * Resolves the mode of a start. No choice means the daemon's default for the
 * CLI (architecture §13); without one the CLI's own configuration decides.
 */
fun launchMode(chosen: Mode?, settings: DaemonSettings?, agent: Agent): LaunchMode {
    val mode = chosen ?: (settings ?: return LaunchMode.Unknown).let { defaultMode(it, agent) }
    return when (mode) {
        null -> LaunchMode.Send(ApprovalMode.APPROVAL_MODE_UNSPECIFIED)
        Mode.FULL -> LaunchMode.ConfirmFull
        else -> LaunchMode.Send(mode.proto)
    }
}

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
        NewSessionCard(state, projectConfig(state, key), projectName(key), focus)
    }
}

/** Type and send to start a session; the configuration line says what it will use. */
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
    var change by remember { mutableStateOf(false) }
    val act = rememberAction()
    val fr = remember { FocusRequester() }
    LaunchedEffect(initial, text.isNotBlank()) { draft.follow(initial, text.isNotBlank()) }
    LaunchedEffect(focus) { if (focus) runCatching { fr.requestFocus() } }
    // The machine's defaults, to show and confirm the mode a start will use.
    var settings by remember { mutableStateOf<DaemonSettings?>(null) }
    var settingsError by remember { mutableStateOf("") }
    val machine = config?.machineId
    val machineOnline = machine != null && state.conn == ConnState.ONLINE && state.machines[machine]?.online == true
    LaunchedEffect(machine, machineOnline) {
        settings = null
        settingsError = ""
        if (machine == null || !machineOnline) return@LaunchedEffect
        try {
            settings = remote.machineCommand(machine, remote.cmd { getSettings = GetSettingsRequest.getDefaultInstance() }).getSettings.settings
        } catch (e: kotlin.coroutines.cancellation.CancellationException) {
            throw e
        } catch (e: Exception) {
            settingsError = errorText(e)
        }
    }
    var confirmFull by remember { mutableStateOf<NewConfig?>(null) }
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
                    remote.prepare(cfg.machineId, "", remote.cmd {
                        createSession = CreateSessionRequest.newBuilder().setCwd(cfg.cwd).setPrompt(text.trim()).setAgent(cfg.agent)
                            .setModel(cfg.model).setEffort(cfg.effort).setMode(mode).build()
                    })
                }
                remote.forward(prepared, retryOfUnknown = retry).also { start.attempt.settle() }
            } catch (e: Exception) {
                throw start.attempt.settle(e) ?: e
            }
        }) { r ->
            r?.createSession?.session?.id?.let {
                Outbox.rememberPrompt(it, start.prompt)
                nav.session(it)
            }
            text = ""
        }
    }
    Column(Modifier.fillMaxWidth().imePadding().background(p.card).padding(12.dp)) {
        Text("＋ 新会话", style = MaterialTheme.typography.titleSmall, color = p.fg2)
        val c = config
        Row(verticalAlignment = Alignment.Bottom, modifier = Modifier.padding(top = 6.dp)) {
            OutlinedTextField(
                text, { text = it }, Modifier.weight(1f).heightIn(min = 48.dp, max = 160.dp).focusRequester(fr),
                placeholder = { Text("在 $name 里要 ${agentName(c?.agent ?: Agent.AGENT_CODEX)} 做什么？") },
                shape = RoundedCornerShape(22.dp),
            )
            Spacer(Modifier.width(8.dp))
            FilledIconButton(
                onClick = {
                    val cfg = config ?: return@FilledIconButton
                    val start = Starts.of(key)
                    // Other content is a new start; the unknown one is given up.
                    if (start.attempt.frozen && text.trim() != start.prompt) start.attempt.abandon()
                    // An unknown start is resent as prepared, with the mode it had.
                    if (start.attempt.frozen) return@FilledIconButton launch(cfg, ApprovalMode.APPROVAL_MODE_UNSPECIFIED)
                    when (val m = launchMode(cfg.mode, settings, cfg.agent)) {
                        is LaunchMode.Send -> launch(cfg, m.mode)
                        LaunchMode.ConfirmFull -> confirmFull = cfg
                        LaunchMode.Unknown -> {}
                    }
                },
                enabled = c != null && !act.running && machineOnline &&
                    (Starts.of(key).attempt.frozen || launchMode(c.mode, settings, c.agent) != LaunchMode.Unknown),
                modifier = Modifier.size(48.dp),
                colors = IconButtonDefaults.filledIconButtonColors(containerColor = p.blueBg),
            ) { Icon(Icons.Default.ArrowUpward, "开始", tint = p.card) }
        }
        if (c != null) Row(verticalAlignment = Alignment.CenterVertically) {
            Text(configLine(state, c, settings), style = MaterialTheme.typography.bodySmall, color = p.muted, modifier = Modifier.weight(1f), maxLines = 2)
            TextButton(onClick = { change = true }) { Text("更改") }
        }
        if (c != null && state.machines[c.machineId]?.online != true) Text("${machineLabel(state, c.machineId)} 不在线，现在不能新建", color = p.orangeInk, style = MaterialTheme.typography.bodySmall)
        if (c != null && c.mode == null && settingsError.isNotEmpty()) {
            Text("没能读取 ${machineLabel(state, c.machineId)} 的默认审批模式：$settingsError。可以在「更改」里选一个模式再开始", color = p.orangeInk, style = MaterialTheme.typography.bodySmall)
        }
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
    }
    val c = config
    confirmFull?.let { cfg ->
        Confirm(
            "以完全放行启动？",
            "${machineLabel(state, cfg.machineId)} 上 ${agentName(cfg.agent)} 的默认审批模式是完全放行：这个会话里执行命令、改文件都不会再问你。",
            "完全放行启动", danger = true,
            onDismiss = { confirmFull = null },
        ) { launch(cfg, ApprovalMode.APPROVAL_MODE_FULL_ACCESS) }
    }
    if (change && c != null) ConfigSheet(state, c, onDone = { draft.choose(it); change = false }, onClose = { change = false })
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
                    FilterChip(c.machineId == id, { if (online) c = c.copy(machineId = id, model = "", effort = "", agent = c.agent.takeIf { it in machineAgents(state.machines[id]) } ?: Agent.AGENT_CODEX) },
                        { Text(machineLabel(state, id) + if (online) "" else "（不在线）") }, enabled = online)
                }
            }
            Text("目录", style = MaterialTheme.typography.titleSmall)
            Text(shortPath(c.cwd), style = MaterialTheme.typography.bodyMedium)
            val agents = machineAgents(state.machines[c.machineId])
            if (agents.size > 1) {
                Text("CLI", style = MaterialTheme.typography.titleSmall)
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    for (a in agents) FilterChip(c.agent == a, { c = c.copy(agent = a, model = "", effort = "") }, { Text(agentName(a)) })
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
                        for (m in ms.modelsList) Row44(onClick = { c = c.copy(model = m.slug, effort = m.defaultEffort) }) {
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
                for (m in newSessionModes) FilterChip(c.mode == m, { c = c.copy(mode = m) }, { Text(m.text) })
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
        LazyColumn {
            items(projects(state), key = { it.key }) { pr ->
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
    }

    private val byProject = HashMap<String, Start>()

    fun of(project: String): Start = byProject.getOrPut(project) { Start() }

    fun clear() = byProject.clear()
}
