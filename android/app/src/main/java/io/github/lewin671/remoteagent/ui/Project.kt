package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Box
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
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
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
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
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
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
import io.github.lewin671.remoteagent.core.PrefsOp
import io.github.lewin671.remoteagent.core.applySettings
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.settingsAgents
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentSettings
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

/** The project's stored settings, or null while none were saved. */
fun savedSettings(state: RemoteState, key: String): ProjectSettings? = state.prefs?.prefs?.settingsList?.firstOrNull { it.path == key }

// Of the sessions held here: every one that has not ended, and the ended ones fetched so far (architecture §8 「会话列表分页」).
// In the order of the listing, live sessions before ended ones (architecture §8).
private fun projectSessions(state: RemoteState, key: String) = state.sessions.values.filter { it.info?.taskOwned != true && projectKey(it) == key }.sortedWith(io.github.lewin671.remoteagent.core.sessionOrder)

/**
 * The machines a project has sessions on, by the sessions held here and the
 * machines' project summaries; every machine when nothing names it yet.
 */
fun projectMachines(state: RemoteState, key: String): List<String> =
    io.github.lewin671.remoteagent.core.project(state, key)?.machineIds.orEmpty().ifEmpty { state.machines.keys.toList() }

/**
 * Where a project's new sessions inherit from (architecture §13 「初值」): the
 * first session in the listing's order, of the project or of one CLI in it.
 * The listing comes a page at a time, in that order, so the answer is the
 * first such session among those within the pages fetched ([sure]): every
 * session before it is held. While none of them matches and the project has
 * ended sessions that were not fetched, the answer is not known: the session
 * may be among those, and one held past the pages fetched (kept from before
 * a reconnect, or fetched by its ID) may not be the first.
 */
private class Inheritance(state: RemoteState, key: String) {
    private val all = projectSessions(state, key)
    private val sure = all.filter { !io.github.lewin671.remoteagent.core.pastFetched(state, it) }
    private val unfetched = state.moreSessions && (io.github.lewin671.remoteagent.core.project(state, key)?.ended ?: 0) > all.count { exited(it) }

    /** Set once a session was looked for whose place among those not fetched is not known. */
    var unsettled = false
        private set

    /** The first session that matches; when [unsettled] turns true, only the best guess of those held. */
    fun first(match: (SessionView) -> Boolean = { true }): SessionView? {
        sure.firstOrNull(match)?.let { return it }
        if (unfetched) unsettled = true
        return all.firstOrNull(match)
    }
}

/**
 * The CLI a project's next session is meant to use, before looking at what
 * the machine has: the one picked next to the message box, else the one the
 * latest new session used, else the latest session's. Null when nothing says.
 */
fun wantedAgent(state: RemoteState, key: String, pick: Agent? = null, saved: ProjectSettings? = savedSettings(state, key)): Agent? =
    wantedAgent(Inheritance(state, key), pick, saved)

private fun wantedAgent(from: Inheritance, pick: Agent?, saved: ProjectSettings?): Agent? =
    pick ?: saved?.agent?.takeIf { it in settingsAgents } ?: from.first()?.agent()

/**
 * One CLI's set in a project: the stored entry (empty fields are the
 * machine's defaults), else what the project's latest session on that CLI
 * runs with, else the machine's defaults. Plan is never carried over.
 */
fun agentSet(state: RemoteState, key: String, agent: Agent, saved: ProjectSettings? = savedSettings(state, key)): AgentSettings =
    agentSet(Inheritance(state, key), agent, saved)

/** Whether [agentSet] is settled for this CLI: see [ProjectStart.settled]. */
fun agentSetSettled(state: RemoteState, key: String, agent: Agent, saved: ProjectSettings? = savedSettings(state, key)): Boolean =
    Inheritance(state, key).also { agentSet(it, agent, saved) }.unsettled.not()

private fun agentSet(from: Inheritance, agent: Agent, saved: ProjectSettings?): AgentSettings {
    saved?.agentsList?.firstOrNull { it.agent == agent }?.let { return it }
    val b = AgentSettings.newBuilder().setAgent(agent)
    from.first { it.agent() == agent }?.let { last ->
        b.setModel(last.info?.model.orEmpty()).setEffort(last.info?.effort.orEmpty())
        modeOf(last)?.takeIf { it in newSessionModes }?.let { b.setMode(it.proto) }
    }
    return b.build()
}

/**
 * What a project's next session starts with, and whether that is settled.
 * It is not while something in it would be inherited from a session and
 * which session that is cannot be told yet ([Inheritance]): [config] is
 * then only the best guess, no session is started with it, and the pages
 * of the listing are fetched until it is settled. This is the one place
 * that answers both, for the CLI the session would really start on, which
 * is not the remembered one where the machine lacks that.
 */
data class ProjectStart(val config: NewConfig, val settled: Boolean)

/**
 * What a project's next session starts with (architecture §13). The machine
 * is the explicitly chosen [machine], else the stored one, else the latest session's,
 * else the one whose summary
 * has the project's newest session (its sessions may all be on pages not
 * fetched). The CLI is [wantedAgent], or the machine's first
 * when it has none or lacks that one, with that CLI's own set. Null while no
 * machine is known.
 */
fun projectStart(state: RemoteState, key: String, pick: Agent? = null, machine: String? = null, saved: ProjectSettings? = savedSettings(state, key)): ProjectStart? {
    val from = Inheritance(state, key)
    val machineId = machine ?: saved?.machineId?.takeIf { it.isNotEmpty() } ?: from.first()?.machineId
        ?: io.github.lewin671.remoteagent.core.project(state, key)?.machineIds?.firstOrNull() ?: return null
    val agents = machineAgents(state.machines[machineId])
    val agent = wantedAgent(from, pick, saved)?.takeIf { it in agents } ?: agents.first()
    return ProjectStart(agentConfig(machineId, key, agentSet(from, agent, saved)), !from.unsettled)
}

/** The configuration of [projectStart], settled or not. */
fun projectConfig(state: RemoteState, key: String, pick: Agent? = null, machine: String? = null, saved: ProjectSettings? = savedSettings(state, key)): NewConfig? =
    projectStart(state, key, pick, machine, saved)?.config

fun agentConfig(machineId: String, cwd: String, s: AgentSettings): NewConfig =
    NewConfig(machineId, cwd, s.agent, s.model, s.effort, modeOf(s.mode)?.takeIf { it in newSessionModes }, s.standardContext)

/** The configuration's CLI set, as project settings store it. */
fun NewConfig.agentSettings(): AgentSettings = AgentSettings.newBuilder().setAgent(agent).setModel(model).setEffort(effort)
    .setMode(mode?.proto ?: ApprovalMode.APPROVAL_MODE_UNSPECIFIED).setStandardContext(agent == Agent.AGENT_CLAUDE && standardContext).build()

/**
 * The CLI that was meant but that the machine of [c] does not have, so [c]
 * uses another: said above the message box, never silently.
 */
fun missingAgent(state: RemoteState, c: NewConfig, pick: Agent? = null): Agent? =
    wantedAgent(state, c.cwd, pick)?.takeIf { it != c.agent && it !in machineAgents(state.machines[c.machineId]) }

/**
 * What a started session leaves in the project's settings, or null when
 * nothing changes. The first one establishes them; later ones only remember
 * the CLI and add its set if the project has none for it. [project] is the
 * project's own configuration for that start: a machine picked just for this
 * start is not stored, and neither are that machine's defaults.
 */
fun startSettings(saved: ProjectSettings?, project: NewConfig, used: NewConfig): PrefsOp.SetSettings? {
    if (saved != null && saved.agent == used.agent && saved.agentsList.any { it.agent == used.agent }) return null
    val set = if (used.machineId == project.machineId) used else project.takeIf { it.agent == used.agent }
    return PrefsOp.SetSettings(used.cwd, project.machineId, agent = used.agent, sets = listOfNotNull(set?.agentSettings()), keepExisting = true)
}

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
    !d.known -> "跟随 CLI 设置"
    d.mode != null -> "默认：${d.mode.text}"
    else -> "默认：${agentName(agent)} 自己的模式"
} + if (chosen == null && d.varies) "（项目设置可能不同）" else ""

/**
 * A new session's configuration (architecture §8): it follows the project,
 * whose latest session or machines may change, until a draft is begun or the
 * user changes it, then it stays, so an update never retargets a draft.
 */
class DraftConfig(initial: NewConfig?, settled: Boolean = true) {
    var value by mutableStateOf(initial)
        private set
    private var chosen = false
    // What it holds was only a guess ([ProjectStart.settled]): what the project
    // says next replaces it even under a draft, whose text stays.
    private var guess = !settled

    /**
     * A new suggestion from the project; taken while nothing is written or
     * chosen, and always over a guess: a guess is never the user's choice,
     * even after they chose for another CLI and then switched to this one.
     * [settled]: whether the suggestion is more than a guess itself.
     */
    fun follow(initial: NewConfig?, drafting: Boolean, settled: Boolean = true) {
        if (value == null || guess || (!chosen && !drafting)) {
            value = initial
            guess = !settled
        }
    }

    /** A completed start releases choices made for that draft only. */
    fun reset() {
        chosen = false
        guess = false
    }

    /** Another CLI for this draft: taken at once, written or not, and still following the project. */
    fun retarget(c: NewConfig, settled: Boolean = true) {
        value = c
        guess = !settled
    }

    /** The user's own choice, kept from now on. */
    fun choose(c: NewConfig) {
        value = c
        chosen = true
        guess = false
    }
}

/**
 * The configuration a project's page describes under its title: the one ↑
 * starts the next session with. That is the draft's own ([DraftConfig]),
 * which stops following the project once something is written or a machine
 * is picked for this start, so what the page says and what it sends are one
 * thing. [project] is what the project's settings say now; it stands in
 * while there is no draft, and for a project removed from the list, whose
 * page has no message box to keep a draft's configuration current.
 */
fun launchConfig(draft: NewConfig?, project: NewConfig?, hidden: Boolean): NewConfig? = if (hidden) project else draft ?: project

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
 * new one. The header folds into the navigation bar once it is scrolled away;
 * the keyboard only shortens the list, so nothing jumps as it comes and goes.
 */
@Composable
fun ProjectScreen(key: String, focus: Boolean) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    val t = now()
    val sessions = state.sessions.values.filter { it.info?.taskOwned != true && projectKey(it) == key }.sortedWith(sessionOrder)
    val active = sessions.filter { !exited(it) }
    val ended = sessions.filter { exited(it) }
    // The rows are one project's: never the project, the CLI only when its sessions used more than one (client-v27).
    val meta = io.github.lewin671.remoteagent.core.rowMeta(state, sessions, project = false)
    var showEnded by rememberSaveable { mutableStateOf(false) }
    // The project as the machines' summaries and the sessions held here have it: its ended
    // sessions come a page at a time, so how many there are is the summaries' to say.
    val project = io.github.lewin671.remoteagent.core.project(state, key)
    val endedTotal = maxOf(project?.ended ?: 0, ended.size)
    val machines = project?.machineIds.orEmpty().map { machineLabel(state, it) }.distinct()
    // 最近结束 wants three; the whole list asks for a page each time its end is reached. Either
    // way what is shown must not reach past the pages fetched: a session kept from before a
    // reconnect, or fetched by its ID, has others before it that are fetched first.
    FetchEarlier(state, page = io.github.lewin671.remoteagent.core.needsEndedPage(state, ended, if (showEnded) ended.size else 3, endedTotal))
    val list = rememberLazyListState()
    val compact by remember { derivedStateOf { list.firstVisibleItemIndex > 0 } }
    val hidden = io.github.lewin671.remoteagent.core.isHidden(state, key)
    // The page of project settings that is open ("" is the first), or null.
    var settingsOpen by remember { mutableStateOf<String?>(null) }
    // What the next session starts with, and whether the session it inherits from is known
    // yet ([ProjectStart]): the message box fetches pages until it is, and starts nothing before.
    val seed = projectStart(state, key)
    val entry = Drafts.newSession(key, seed?.config, seed?.settled != false)
    val start = projectStart(state, key, entry.agent.value)
    // 项目设置 is about the project: its machine and its set for the CLI selected next to the message box.
    val selected = projectConfig(state, key, draftPick(state, entry))
    // The summary says what ↑ starts with: the draft's own configuration, which the project's settings no
    // longer change once a draft is begun or a machine was picked for this start.
    val launch = launchConfig(entry.config.value, selected, hidden)
    var summaryModels by remember(key, launch?.machineId, launch?.agent) { mutableStateOf<ListModelsResponse?>(null) }
    LaunchedEffect(launch?.machineId, launch?.agent) {
        val cfg = launch ?: return@LaunchedEffect
        summaryModels = runCatching { remote.listModels(cfg.machineId, cfg.agent) }.getOrNull()
    }
    Column(Modifier.fillMaxSize().background(p.card).imePadding()) {
        val folded = launch?.let { listOf(agentName(it.agent), effectiveModeText(it.mode, defaultModeOf(state.machines[it.machineId], it.agent)), summaryMachine(state, it.machineId)).filter { it.isNotEmpty() }.joinToString(" · ") }
        NavBar(if (compact) projectName(key) else "", nav.back, subtitle = if (compact) folded ?: shortPath(key) else null, more = { settingsOpen = "" })
        LazyColumn(Modifier.weight(1f), state = list) {
            item {
                Row(Modifier.padding(horizontal = 16.dp).padding(bottom = 2.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
                    Avatar(key, 52)
                    Column(Modifier.weight(1f)) {
                        Text(projectName(key), style = T.h22, color = p.fg, maxLines = 2, overflow = TextOverflow.Ellipsis)
                        Text((listOf(shortPath(key)) + machines).joinToString(" · "), style = T.sm, color = p.muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
                        Row(Modifier.padding(top = 2.dp), verticalAlignment = Alignment.CenterVertically) {
                            Text("${active.size} 个进行中 · ", style = T.sm, color = p.muted)
                            Box(Modifier.clickable(enabled = endedTotal > 0) { showEnded = !showEnded }) { ArrowText("已结束 $endedTotal", p.blue, T.smB, down = showEnded) }
                        }
                        if (launch != null) {
                            val full = launch.mode == Mode.FULL || (launch.mode == null && defaultModeOf(state.machines[launch.machineId], launch.agent).mode == Mode.FULL)
                            SummaryChip(projectSummary(state, launch, summaryModels), full, Modifier.padding(top = 6.dp).testTag("project-settings-summary")) { settingsOpen = "" }
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
                items(active, key = { it.id }) { s -> SessionRow(state, s, t, meta, last = s == active.last()) }
            }
            val recent = if (showEnded) ended else ended.take(3)
            val earlier = state.moreSessions && ended.size < endedTotal
            if (recent.isNotEmpty() || (showEnded && earlier)) {
                item { Label(if (showEnded) "已结束" else "最近结束", if (showEnded) endedTotal else null) }
                items(recent, key = { it.id }) { s -> EndedRow(s, t) }
                // Composed when the end of the list is reached: that is when the next page is fetched.
                if (showEnded && earlier) item(key = "earlier") {
                    FetchEarlier(state, page = true)
                    EarlierTail(state, Modifier.padding(start = 35.dp, end = 16.dp, top = 12.dp, bottom = 4.dp).testTag("project-earlier"))
                }
            }
            item { Spacer(Modifier.height(12.dp)) }
        }
        // A hidden project stays reachable from its sessions and
        // notifications; starting a new session there restores it first.
        if (!hidden) NewSessionBox(state, start?.config, projectName(key), focus, onMachine = { settingsOpen = "machine" }, settled = start?.settled != false)
    }
    val open = settingsOpen
    if (open != null && selected != null) NewSettingsSheet(state, key, projectName(key), selected.machineId, selected.agent, open, onDone = { op ->
        if (op != null) {
            PrefsChanges.run(remote, op)
            // The draft takes the new settings at once, written or not.
            projectConfig(state, key, entry.agent.value, saved = applySettings(savedSettings(state, key), op, System.currentTimeMillis()))?.let { entry.config.choose(it) }
        }
        settingsOpen = null
    }, onClose = { settingsOpen = null })
}

/**
 * The CLI a draft is on, as the pick its project's configuration is read
 * with. A CLI the draft only fell back to is not one: the pick stays.
 */
private fun draftPick(state: RemoteState, entry: Drafts.NewSession): Agent? =
    entry.config.value?.takeIf { missingAgent(state, it, entry.agent.value) == null }?.agent ?: entry.agent.value

/**
 * The CLI of the next session, in a row of its own above the message box
 * (client-v26): a grey pill of the machine's CLIs, each 44 dp to touch.
 */
@Composable
fun AgentSwitch(agents: List<Agent>, selected: Agent, modifier: Modifier = Modifier, onSelect: (Agent) -> Unit) {
    val p = pal
    Row(modifier.fillMaxWidth()) {
        Row(
            Modifier.drawBehind { drawRoundRect(p.chip, Offset(0f, 4.dp.toPx()), Size(size.width, size.height - 8.dp.toPx()), CornerRadius(10.dp.toPx())) }
                .padding(horizontal = 2.dp).selectableGroup().semantics { contentDescription = "新会话使用的 CLI" },
            horizontalArrangement = Arrangement.spacedBy(2.dp),
        ) {
            for (a in agents) {
                val on = a == selected
                Box(Modifier.heightIn(min = 44.dp).selectable(selected = on, role = Role.RadioButton, onClick = { onSelect(a) }).padding(vertical = 6.dp), contentAlignment = Alignment.Center) {
                    Box(
                        Modifier.widthIn(min = 56.dp).heightIn(min = 32.dp)
                            .then(if (on) Modifier.shadow(1.dp, RoundedCornerShape(8.dp)).background(p.card, RoundedCornerShape(8.dp)) else Modifier).padding(horizontal = 10.dp),
                        contentAlignment = Alignment.Center,
                    ) { Text(agentName(a), style = if (on) T.smB else T.sm, color = if (on) p.fg else p.muted, maxLines = 1) }
                }
            }
        }
    }
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

/** The machine as the summary names it: only where there is more than one to mean, and cut short when long, so it is the name that gives way and not the model after it. */
internal fun summaryMachine(state: RemoteState, id: String): String =
    if (state.machines.size > 1) machineLabel(state, id).let { if (it.length > SUMMARY_MACHINE_MAX) it.take(SUMMARY_MACHINE_MAX - 1) + "…" else it } else ""

private const val SUMMARY_MACHINE_MAX = 16

/** Mode, machine and model; the effort and the context window are in the project's settings. */
fun projectSummary(state: RemoteState, c: NewConfig, models: ListModelsResponse? = null): String {
    val default = defaultModeOf(state.machines[c.machineId], c.agent)
    val mode = effectiveModeText(c.mode, default)
    val modelId = c.model.ifEmpty { models?.defaultModel.orEmpty() }
    val chosen = models?.let { io.github.lewin671.remoteagent.core.findModel(it.modelsList, modelId) { m -> m.slug } }
    return listOf(agentName(c.agent), mode,
        summaryMachine(state, c.machineId), chosen?.displayName?.ifEmpty { modelId } ?: modelId.ifEmpty { "默认模型" }).filter { it.isNotEmpty() }.joinToString(" · ")
}

fun effectiveModeText(chosen: Mode?, default: DefaultMode): String =
    chosen?.text ?: if (!default.known) "跟随 CLI 设置" else default.mode?.text ?: "CLI 自己的模式"

/** 这个项目已从列表移除 · 重新添加: a notice, not a button in the way. */
@Composable
private fun HiddenBanner(state: RemoteState, key: String) {
    val remote = LocalRemote.current
    val change = PrefsChanges.states[key]
    val blocked = prefsBlocked(state)
    Column(Modifier.padding(horizontal = 16.dp, vertical = 4.dp)) {
        Banner(
            if (change == PrefsChanges.State.Saving) "正在添加到项目列表…" else "这个项目已从列表移除",
            action = if (blocked == null && change != PrefsChanges.State.Saving) "重新添加" else null,
            onAction = { PrefsChanges.run(remote, PrefsOp.Unhide(key)) },
        )
        if (blocked != null) Hint(blocked, color = pal.orangeInk)
        if (change is PrefsChanges.State.Failed) Hint("没能保存：${change.error}", color = pal.red)
    }
}

/**
 * Another CLI for a draft (client-v26): the text and images stay, and the
 * draft takes the project's set for that CLI ([project]). On a machine
 * picked just for this start the model and effort are that machine's defaults.
 */
fun switchAgent(c: NewConfig, project: NewConfig?, agent: Agent): NewConfig {
    val set = project?.takeIf { it.agent == agent } ?: NewConfig(c.machineId, c.cwd, agent)
    return if (set.machineId == c.machineId) set else set.copy(machineId = c.machineId, model = "", effort = "")
}

/**
 * Another machine for a draft: the model and effort were that machine's
 * list, so they go, and the mode is that machine's default again. The CLI
 * stays when the machine has it; otherwise the caller must have the user
 * pick one ([machineAgents]), never silently.
 */
fun switchMachine(c: NewConfig, machineId: String, agents: List<Agent>): NewConfig =
    c.copy(machineId = machineId, model = "", effort = "", mode = null, agent = if (c.agent in agents) c.agent else agents.first())

/** The two lines of the configuration: machine and mode, then model and effort, neither cut. */
fun configLines(state: RemoteState, c: NewConfig, d: DefaultMode): Pair<String, String> =
    "${machineLabel(state, c.machineId)} · ${modeLabel(c.mode, d, c.agent)}" to
        (listOf(c.model, c.effort).filter { it.isNotEmpty() }.joinToString(" · ").ifEmpty { "默认模型" })

/**
 * The new-session box (client-v8): the plus adds images; configuration comes from
 * project settings and stays with an active draft, except the CLI, which is
 * switched in the row above the box when the machine has more than one
 * (client-v26). An offline machine is replaced only by the user's choice for
 * this send. The first message starts the session: there is no start without
 * one. [onMachine] opens the machine choice of project settings.
 * [settled]: whether [initial] is more than a guess ([ProjectStart]). While
 * it is not, the pages of the listing are fetched and nothing starts; what
 * is written stays, and takes the settings once they are known.
 */
@Composable
fun NewSessionBox(state: RemoteState, initial: NewConfig?, name: String, focus: Boolean, onMachine: () -> Unit = {}, settled: Boolean = true) {
    val p = pal
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val key = initial?.cwd ?: name
    // Text and configuration live and come back together, so a draft
    // reopened later still goes where it was going.
    val entry = Drafts.newSession(key, initial, settled)
    val draft = entry.config
    FetchEarlier(state, page = !settled)
    val config = draft.value
    var text by entry.text
    val images = ImageDrafts.forProject(key)
    val fr = remember { FocusRequester() }
    val drafting = text.isNotBlank() || images.images.isNotEmpty()
    LaunchedEffect(initial, drafting, settled) { draft.follow(initial, drafting, settled) }
    // Focused when the page opens, not again when coming back to it from the session it started.
    var focused by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(focus) { if (focus && !focused) { focused = true; runCatching { fr.requestFocus() } } }
    val machine = config?.machineId
    val online = state.conn == ConnState.ONLINE
    val machineOnline = machine != null && online && state.machines[machine]?.online == true
    // The mode a start without a choice gets, as the machine reports it.
    val defaults = config?.let { defaultModeOf(state.machines[it.machineId], it.agent) } ?: DefaultMode(null, false, false)
    // Images go to the machine the session will start on; a new machine gets them again.
    val blocked = uploadBlocked(machine?.let { state.machines[it] }, online, machine?.let { machineLabel(state, it) }.orEmpty())
    RefreshDraftUploads(images, remote, machine, blocked)
    val pick = rememberImagePicker(images, remote, machine, blocked, maxImages(machine?.let { state.machines[it] }))
    val act = rememberAction()
    // Whether a session can be started with the draft as it is: ↑ is drawn by it, and 重试 after a
    // failure is held to it too.
    val canStart = !images.restoring && config != null && settled && !act.running && machineOnline && startable(text, images.images, config.machineId)
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
                    start.config = cfg
                    // The images this request carries; any picked after it stay in the draft.
                    start.taken = images.images.filter { it.readyFor(cfg.machineId) }
                    val ids = start.imageIds
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
            // What the session started with is what the request said, not what the page says by now.
            val used = start.config ?: cfg
            startSettings(savedSettings(state, key), projectConfig(state, key, used.agent, initial?.machineId) ?: used, used)?.let { PrefsChanges.run(remote, it) }
            // The project remembers this CLI now; the next draft starts from it.
            entry.agent.value = null
            if (text.trim() == start.prompt && images.images.toList() == start.taken) draft.reset()
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
        if (machine != null && blocked == null) images.sendAllTo(remote, machine)
        if (!canStart) return
        if (!startable(text, images.images, machine ?: return)) return
        val cfg = config ?: return
        val st = Starts.of(key)
        // Another message, other images, another machine or other settings is a new start; the unknown one is given up.
        if (st.attempt.frozen && !st.same(text.trim(), cfg, images.images)) {
            st.attempt.abandon()
            st.taken = emptyList()
        }
        // An unknown start is resent as prepared, with the mode it had.
        if (st.attempt.frozen) return launch(cfg, ApprovalMode.APPROVAL_MODE_UNSPECIFIED)
        launch(cfg, cfg.mode?.proto ?: ApprovalMode.APPROVAL_MODE_UNSPECIFIED)
    }
    val c = config
    val agents = c?.let { machineAgents(state.machines[it.machineId]) }.orEmpty()
    val missing = c?.let { missingAgent(state, it, entry.agent.value) }
    val notices = @Composable {
        Column(Modifier.padding(horizontal = 12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            // Why nothing can start yet; a page that failed says so, with the tap that tries again.
            if (!settled && (state.conn != ConnState.ONLINE || state.sessionsError.isNotEmpty())) EarlierTail(state, Modifier.fillMaxWidth().padding(horizontal = 4.dp).testTag("settings-unread"))
            else if (!settled) Text("正在读取这个项目上次的设置…", style = T.meta, color = p.muted, modifier = Modifier.fillMaxWidth().padding(horizontal = 4.dp).testTag("settings-unread"))
            if (c != null && state.machines[c.machineId]?.online != true && drafting) OfflineChoice(state, c, name) { draft.choose(it) }
            else if (c != null && state.machines[c.machineId]?.online != true) Banner("${machineLabel(state, c.machineId)} 不在线，现在不能新建", tone = Tone2.WARN)
            if (c != null && missing != null) Banner("${machineLabel(state, c.machineId)} 没有 ${agentName(missing)}，这次用 ${agentName(c.agent)}", action = "换机器", onAction = onMachine)
            if (act.running) Banner("正在 ${c?.let { machineLabel(state, it.machineId) }} 上启动…")
            val frozen = Starts.of(key)
            // An unknown start has its own notice below.
            // 重试 is ↑ again: it is offered when ↑ is.
            if (act.error.isNotEmpty() && !frozen.attempt.frozen) Banner("启动失败：${act.error}", tone = Tone2.ERROR, action = "重试".takeIf { canStart }, onAction = { start() })
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
    if (c != null && agents.size > 1) AgentSwitch(agents, c.agent, Modifier.padding(horizontal = 16.dp)) { a ->
        entry.agent.value = a
        val to = projectStart(state, key, a, c.machineId)
        draft.retarget(switchAgent(c, to?.config, a), to?.settled != false)
    }
    ComposeBox(
        text, { text = it }, "描述任务…",
        canSend = canStart,
        onSend = ::start,
        onPlus = pick, plusEnabled = !images.restoring && blocked == null,
        sendLabel = "开始", focusRequester = fr,
        top = { ImageStrip(images, remote, machine, maxImages(machine?.let { state.machines[it] })) },
    )
}

/**
 * Whether a draft is something to start a session with on [machineId]:
 * there is no start with nothing to send, and none while an image is not on
 * that machine yet. Never an empty session, never one with only some of the
 * images.
 */
fun startable(text: String, images: List<PendingImage>, machineId: String): Boolean =
    (text.isNotBlank() || images.isNotEmpty()) && images.all { it.readyFor(machineId) }

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
    val others = projectMachines(state, c.cwd).filter { it != c.machineId && state.machines[it]?.online == true }
    Column(Modifier.fillMaxWidth().clip(Radii.MD).background(p.soft).padding(horizontal = 12.dp, vertical = 9.dp)) {
        Text("${machineLabel(state, c.machineId)} 已离线，任务尚未开始", style = T.sm, color = p.orangeInk)
        Text("草稿已保留，请选择在线机器；不自动换机器或 CLI。", style = T.meta, color = p.muted)
        for (id in others) {
            val agents = machineAgents(state.machines[id])
            val cliNote = when {
                c.agent in agents -> "有 ${agentName(c.agent)}；模型和模式会按这台机器重新选择"
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

/** A project-setting row: a short label, the value at the right, an arrow. */
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
                Spacer(Modifier.width(8.dp))
                Arrow(p.faint, ts(18.sp))
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
        Arrow(p.faint, ts(18.sp))
    }
}

fun projectModel(c: NewConfig, models: ListModelsResponse, settings: DaemonSettings? = null): io.github.lewin671.remoteagent.proto.ModelInfo? {
    val defaultModel = if (c.agent == Agent.AGENT_CLAUDE) settings?.claude?.model.orEmpty() else settings?.defaultModel.orEmpty()
    val id = c.model.ifEmpty { defaultModel.ifEmpty { models.defaultModel } }
    return io.github.lewin671.remoteagent.core.findModel(models.modelsList, id) { it.slug }
}

/**
 * 项目设置 (client-v26): the machine, and for each CLI the machine has its
 * own model and effort, context window (Claude Code) and approval mode,
 * under a tab per CLI when there is more than one; each is chosen on a page
 * of the sheet. The folder is the project's read-only path. It opens on
 * [agent], the CLI selected next to the message box, and on [open]. Which
 * CLI a new session uses is not chosen here. [onDone] gets the change to
 * store, or null when nothing was changed.
 */
@Composable
fun NewSettingsSheet(state: RemoteState, cwd: String, name: String, machineId: String, agent: Agent, open: String = "", onDone: (PrefsOp.SetSettings?) -> Unit, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    val nav = LocalNav.current
    var removing by remember { mutableStateOf(false) }
    var machine by remember { mutableStateOf(machineId) }
    var tab by remember { mutableStateOf(agent) }
    // The sets changed here, by CLI; the others stay as stored.
    val edited = remember { mutableStateMapOf<Agent, NewConfig>() }
    var page by remember { mutableStateOf(open) }
    // Models are per machine: on another one every CLI starts over.
    val moved = machine != machineId
    val agents = machineAgents(state.machines[machine])
    val cur = if (tab in agents) tab else agents.first()
    val c = edited[cur] ?: if (moved) NewConfig(machine, cwd, cur) else agentConfig(machine, cwd, agentSet(state, cwd, cur))
    // What this CLI's set starts from may be a session not fetched yet: it is fetched before the set can be changed here.
    val unread = !moved && edited[cur] == null && !agentSetSettled(state, cwd, cur)
    FetchEarlier(state, page = unread)
    fun edit(next: NewConfig) {
        edited[cur] = next
    }
    fun done() = onDone(if (!moved && edited.isEmpty()) null else PrefsOp.SetSettings(cwd, machine, moved = moved, sets = edited.values.map { it.agentSettings() }))
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
    val used = projectMachines(state, cwd)
    val blockedPrefs = prefsBlocked(state)
    val defaults = defaultModeOf(state.machines[c.machineId], c.agent)
    val shownModel = models?.let { projectModel(c, it, settings) }
    Sheet(onClose) {
        when (page) {
            "" -> {
                SheetHead("项目设置", if (blockedPrefs == null) "完成" else null, { done() })
                if (blockedPrefs != null) Banner(blockedPrefs)
                Row(Modifier.fillMaxWidth().padding(bottom = 12.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    Avatar(cwd, 22)
                    Column(Modifier.weight(1f)) {
                        Text(name, style = T.bodyB, color = p.fg)
                        Text(shortPath(cwd), style = T.mono, color = p.muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
                    }
                }
                FieldLabel("新会话")
                val machineRow: @Composable () -> Unit =
                    { SetRow("机器", machineLabel(state, machine), { page = "machine" }, dot = if (state.machines[machine]?.online == true) p.green else p.gray) }
                val rows = listOf<@Composable () -> Unit>(
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
                @Composable
                fun card(rows: List<@Composable () -> Unit>) = Column(Modifier.fillMaxWidth().clip(Radii.MD).background(p.soft)) {
                    rows.forEachIndexed { i, r ->
                        if (i > 0) androidx.compose.material3.HorizontalDivider(color = p.line, thickness = 1.dp)
                        r()
                    }
                }
                val reading: @Composable () -> Unit = {
                    if (state.conn != ConnState.ONLINE || state.sessionsError.isNotEmpty()) EarlierTail(state, Modifier.fillMaxWidth().padding(14.dp))
                    else Text("正在读取 ${agentName(cur)} 上次的设置…", style = T.sm, color = p.muted, modifier = Modifier.fillMaxWidth().padding(14.dp))
                }
                if (unread && agents.size > 1) {
                    card(listOf(machineRow))
                    ViewTabs(agents.map { agentName(it) }, agents.indexOf(cur), { tab = agents[it] }, inset = 0.dp, modifier = Modifier.padding(top = 6.dp, bottom = 4.dp))
                    card(listOf(reading))
                } else if (unread) {
                    card(listOf(machineRow, reading))
                } else if (agents.size > 1) {
                    // The machine is shared; each CLI has its own set, seen under its tab.
                    card(listOf(machineRow))
                    ViewTabs(agents.map { agentName(it) }, agents.indexOf(cur), { tab = agents[it] }, inset = 0.dp, modifier = Modifier.padding(top = 6.dp, bottom = 4.dp))
                    card(rows)
                    Hint("两个 CLI 各存一套，新建时用哪个在输入框旁选。只影响之后新建的会话，进行中的不变；在你的设备之间同步。")
                } else {
                    card(listOf(machineRow) + rows)
                    Hint("只影响之后新建的会话，进行中的不变；在你的设备之间同步。")
                }
                TextAction(if (io.github.lewin671.remoteagent.core.isPinned(state, cwd)) "取消常用" else "设为常用", {
                    PrefsChanges.run(remote, if (io.github.lewin671.remoteagent.core.isPinned(state, cwd)) PrefsOp.Unpin(cwd) else PrefsOp.Pin(cwd))
                }, enabled = blockedPrefs == null)
                TextAction("移除项目…", { removing = true }, enabled = blockedPrefs == null, color = p.red)
                if (removing) RemoveProjectConfirm(cwd, onDismiss = { removing = false }) { onClose(); nav.home() }
            }
            "machine" -> {
                SheetHead("机器", if (blockedPrefs == null) "完成" else null, { done() }, onBack = { page = "" })
                used.forEachIndexed { i, id ->
                    val online = state.machines[id]?.online == true
                    // Said for the CLI selected next to the message box: 换机器 comes here to find it.
                    val has = agent in machineAgents(state.machines[id])
                    ListItem(
                        machineLabel(state, id), if (!online) "不在线，现在不能新建" else if (!has) "没有 ${agentName(agent)}" else null,
                        checked = machine == id, enabled = online, last = i == used.size - 1,
                        onClick = {
                            if (id != machine) {
                                machine = id
                                edited.clear()
                            }
                            page = ""
                        },
                        trailing = { Dot(if (online) p.green else p.gray) },
                    )
                }
                Hint("换机器后，各 CLI 的模型和审批从那台机器的默认设置开始。")
            }
            "model" -> {
                SheetHead("模型", if (blockedPrefs == null) "完成" else null, { done() }, onBack = { page = "" })
                val ms = models
                when {
                    modelsError.isNotEmpty() -> Banner("没能读取模型列表，会用 ${agentName(c.agent)} 自己的默认模型：$modelsError", tone = Tone2.WARN)
                    ms == null -> Text("读取 ${machineLabel(state, c.machineId)} 的设置…", style = T.sm, color = p.muted, modifier = Modifier.padding(vertical = 12.dp))
                    else -> {
                        val defaultModel = (if (c.agent == Agent.AGENT_CLAUDE) settings?.claude?.model.orEmpty() else settings?.defaultModel.orEmpty()).ifEmpty { ms.defaultModel }
                        ListItem("默认" + if (defaultModel.isNotEmpty()) "（$defaultModel）" else "", checked = c.model.isEmpty(), onClick = { edit(c.copy(model = "", effort = "")) })
                        ms.modelsList.forEachIndexed { i, m ->
                            ListItem(m.displayName.ifEmpty { m.slug }, checked = c.model == m.slug, last = i == ms.modelsCount - 1, onClick = {
                                edit(c.copy(model = m.slug, effort = m.defaultEffort, mode = c.mode?.takeUnless { m.autoUnavailable && it == Mode.AUTO }))
                            })
                        }
                        projectModel(c, ms, settings)?.takeIf { it.effortsList.isNotEmpty() }?.let { m ->
                            val defaultEffort = if (c.model.isEmpty()) {
                                (if (c.agent == Agent.AGENT_CLAUDE) settings?.claude?.effort.orEmpty() else settings?.defaultEffort.orEmpty()).ifEmpty { m.defaultEffort }
                            } else m.defaultEffort
                            FieldLabel("推理强度")
                            ChoiceGrid(m.effortsList, c.effort.ifEmpty { defaultEffort }, true) { edit(c.copy(effort = it)) }
                        }
                    }
                }
            }
            "context" -> {
                SheetHead("上下文窗口", if (blockedPrefs == null) "完成" else null, { done() }, onBack = { page = "" })
                ListItem("1M", "Claude Code 的默认窗口", checked = !c.standardContext, onClick = { edit(c.copy(standardContext = false)); page = "" })
                ListItem("标准 200k", "超过 200k 前自动压缩", checked = c.standardContext, last = true, onClick = { edit(c.copy(standardContext = true)); page = "" })
                Hint("窗口在会话开始时确定，改了只影响之后新建的会话。")
            }
            "mode" -> {
                SheetHead("审批模式", if (blockedPrefs == null) "完成" else null, { done() }, onBack = { page = "" })
                ListItem("默认", "跟随 ${agentName(c.agent)} 自己的配置", checked = c.mode == null, onClick = { edit(c.copy(mode = null)); page = "" })
                val chosen = models?.let { projectModel(c, it, settings) }
                newSessionModes.forEachIndexed { i, m ->
                    // The list says which models come without the auto mode
                    // (Claude Code's Haiku 4.5); until it has come, none is.
                    val off = m == Mode.AUTO && chosen?.autoUnavailable == true
                    ListItem(m.text, if (off) "当前模型不支持" else modeHelp.getValue(m).replace("CLI", agentName(c.agent)), checked = c.mode == m, enabled = !off, last = i == newSessionModes.size - 1, onClick = { edit(c.copy(mode = m)); page = "" })
                }
                Hint("完全放行的新会话直接按此设置启动。")
            }
        }
    }
}

/** The list of 新会话 · 选项目: the projects as every list shows them, each leading to its new-session box. */
@Composable
fun ProjectPickList(state: RemoteState, onPick: (String) -> Unit, onNew: () -> Unit) {
    val p = pal
    ProjectList(state, onNew = onNew) { pr ->
        ProjectRow(state, pr, onClick = { onPick(pr.key) }) { Arrow(p.faint, ts(18.sp)) }
    }
}

/**
 * 新会话 · 选项目 from the new-session button on the home page, as a sheet; 新项目… turns it
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
 * with the folder.
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
            if (d.dirsList.isEmpty()) Text("没有子目录", style = T.sm, color = p.muted, modifier = Modifier.padding(vertical = 8.dp))
            if (d.parent.isNotEmpty()) ListItem("上一级", last = d.dirsList.isEmpty(), onClick = { path = d.parent }, icon = Lucide.ChevronLeft)
            d.dirsList.forEachIndexed { i, name ->
                ListItem(name, last = i == d.dirsList.size - 1, onClick = { path = d.path.trimEnd('/') + "/" + name }, trailing = { Arrow(p.muted, T.body) })
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

/**
 * A folder chosen for a new project: its box, focused, with that machine's defaults. The
 * project is stored with the machine chosen, so it is listed before its first session,
 * here and on the account's other clients.
 */
@Composable
fun NewProjectAtScreen(machine: String, path: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    // The folder may be a project that has sessions already: what it starts with is settled the same way.
    LaunchedEffect(machine, path) {
        addProject(remote, state, machine, path)
    }
    val seed = projectStart(state, path, machine = machine)
    val entry = Drafts.newSession(path, seed?.config, seed?.settled != false)
    val start = projectStart(state, path, entry.agent.value, machine)
    val initial = start?.config ?: NewConfig(machine, path)
    val selected = entry.config.value ?: initial
    var summaryModels by remember(selected.machineId, selected.agent) { mutableStateOf<ListModelsResponse?>(null) }
    LaunchedEffect(selected.machineId, selected.agent) {
        summaryModels = runCatching { remote.listModels(selected.machineId, selected.agent) }.getOrNull()
    }
    var settingsOpen by remember { mutableStateOf<String?>(null) }
    Column(Modifier.fillMaxSize().background(p.card).imePadding()) {
        NavBar(projectName(path), nav.back, subtitle = shortPath(path), more = { settingsOpen = "" })
        SummaryChip(projectSummary(state, selected, summaryModels), selected.mode == Mode.FULL, Modifier.padding(horizontal = 16.dp, vertical = 4.dp)) { settingsOpen = "" }
        Text("这个项目还没有会话。", style = T.sm, color = p.muted, modifier = Modifier.padding(16.dp).weight(1f))
        NewSessionBox(state, initial, projectName(path), focus = true, onMachine = { settingsOpen = "machine" }, settled = start?.settled != false)
    }
    val open = settingsOpen
    if (open != null) NewSettingsSheet(state, path, projectName(path), selected.machineId, selected.agent, open, onDone = { op ->
        if (op != null) {
            PrefsChanges.run(remote, op)
            projectConfig(state, path, entry.agent.value, null, applySettings(savedSettings(state, path), op, System.currentTimeMillis()))?.let { entry.config.choose(it) }
        }
        settingsOpen = null
    }, onClose = { settingsOpen = null })
}

/** New-session starts per project, for this run of the app: one whose outcome is unknown is retried as it was. */
object Starts {
    class Start {
        val attempt = io.github.lewin671.remoteagent.core.Attempt()
        var prompt = ""
        /** The configuration the prepared request starts the session with. */
        var config: NewConfig? = null
        /** The upload IDs frozen when the request is prepared; draft images can be uploaded again. */
        var imageIds: List<String> = emptyList()
            private set
        private var preparedImages: List<Processed?> = emptyList()
        /** The images the prepared request names, kept for a retry of the same request. */
        var taken: List<PendingImage> = emptyList()
            set(value) {
                field = value
                imageIds = value.mapNotNull { it.imageId }
                preparedImages = value.map { it.processed }
            }

        /**
         * Whether a start with this message, configuration and images is the
         * one last prepared: only then is a retry of an unknown outcome that
         * request again.
         */
        fun same(prompt: String, cfg: NewConfig, images: List<PendingImage>): Boolean =
            prompt == this.prompt && cfg == config && images.map { it.id } == taken.map { it.id } &&
                images.map { it.processed } == preparedImages
    }

    private val byProject = HashMap<String, Start>()

    fun of(project: String): Start = byProject.getOrPut(project) { Start() }

    fun clear() = byProject.clear()
}
