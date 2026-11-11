package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.fillMaxHeight
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
import androidx.compose.foundation.lazy.itemsIndexed
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
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.DEFAULT_AGENT
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.shownModels
import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.core.agentInfo
import io.github.lewin671.remoteagent.core.defaultsFor
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
import io.github.lewin671.remoteagent.core.pathCrumbs
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
    val agent: Agent = DEFAULT_AGENT,
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
    .setMode(mode?.proto ?: ApprovalMode.APPROVAL_MODE_UNSPECIFIED).setStandardContext(agentInfo(agent).standardContextOption && standardContext).build()

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
    val info = m?.meta?.agentsList?.firstOrNull { agentInfo(it.agent).agent == agentInfo(agent).agent }
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
 * 新任务 in a project (design-system §6 新任务): a whole page, the sentence
 * naming the project and the CLI, the task in large type, and ＋ and the
 * summary of the project's settings above the keyboard. The project's
 * sessions are on the home page in its scope.
 */
@Composable
fun ProjectScreen(key: String, focus: Boolean) {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val p = pal
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
    var picking by remember { mutableStateOf(false) }
    val agents = launch?.let { machineAgents(state.machines[it.machineId]) }.orEmpty()
    Column(Modifier.fillMaxSize().background(p.paper)) {
        // The CLI is in the sentence when there is more than one to choose: the summary does not repeat it.
        val summary = launch?.let { projectSummary(state, it, summaryModels).removePrefix(if (agents.size > 1) agentName(it.agent) + " · " else "") }
        val full = launch != null && startsFull(state, launch)
        NewSessionBox(
            state, start?.config, projectName(key), focus, onMachine = { settingsOpen = "machine" }, settled = start?.settled != false,
            summary = summary, full = full, onSettings = { settingsOpen = "" }, onProject = { picking = true },
            banner = {
                Column(Modifier.padding(horizontal = 12.dp)) {
                    when (val change = PrefsChanges.states[key]) {
                        PrefsChanges.State.Saving -> Notice("正在保存项目设置…", Modifier.padding(top = 8.dp))
                        is PrefsChanges.State.Failed -> Notice("项目设置没能保存：${change.error}", Modifier.padding(top = 8.dp), tone = NoticeTone.ERROR,
                            action = "重试", onAction = { PrefsChanges.run(remote, change.op) })
                        else -> {}
                    }
                    if (hidden) HiddenBanner(state, key)
                }
            },
        )
    }
    if (picking) PickProjectSheet(state, { picking = false }, replace = true)
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

/** The new-session settings as a capsule as wide as its words, opening the project's settings; only 完全放行 is red. */
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
    Row(modifier.heightIn(min = 40.dp).clip(Radii.Pill).background(p.fill).clickable(onClick = onClick).padding(horizontal = 14.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
        Text(shown, style = T.s15, color = p.ink, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
        Arrow(p.ink2, T.s15)
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

/** Whether a start with [c] is 完全放行: chosen, or the mode its CLI gives on that machine when none is. */
fun startsFull(state: RemoteState, c: NewConfig): Boolean =
    c.mode == Mode.FULL || (c.mode == null && defaultModeOf(state.machines[c.machineId], c.agent).mode == Mode.FULL)

fun effectiveModeText(chosen: Mode?, default: DefaultMode): String =
    chosen?.text ?: if (!default.known) "跟随 CLI 设置" else default.mode?.text ?: "CLI 自己的模式"

/** 这个项目已从列表移除 · 重新添加: a notice, not a button in the way. */
@Composable
private fun HiddenBanner(state: RemoteState, key: String) {
    val remote = LocalRemote.current
    val change = PrefsChanges.states[key]
    val blocked = prefsBlocked(state)
    Column(Modifier.padding(horizontal = 16.dp, vertical = 4.dp)) {
        Notice(
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
fun NewSessionBox(
    state: RemoteState,
    initial: NewConfig?,
    name: String,
    focus: Boolean,
    onMachine: () -> Unit = {},
    settled: Boolean = true,
    summary: String? = null,
    full: Boolean = false,
    onSettings: () -> Unit = {},
    onProject: (() -> Unit)? = null,
    banner: @Composable () -> Unit = {},
) {
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
    val pick = rememberImagePicker(images, remote, machine, blocked, maxImages(machine?.let { state.machines[it] }), videos = io.github.lewin671.remoteagent.core.filesSupported(machine?.let { state.machines[it] }))
    val act = rememberAction()
    // Whether a session can be started with the draft as it is: ↑ is drawn by it, and 重试 after a
    // failure is held to it too.
    val canStart = !images.restoring && !images.readingFiles && fileDraftBlocked(images, machine?.let { state.machines[it] }) == null && config != null && settled && !act.running && machineOnline && startable(text, images.images, config.machineId)
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
                            .setModel(cfg.model).setEffort(cfg.effort).setMode(mode).setStandardContext(agentInfo(cfg.agent).standardContextOption && cfg.standardContext).addAllImageIds(ids).addAllFileIds(start.fileIds).build()
                    })
                }
                remote.forward(prepared, retryOfUnknown = retry).also { start.attempt.settle() }
            } catch (e: Exception) {
                val err = start.attempt.settle(e) ?: e
                if (!start.attempt.frozen) start.taken.forEach { it.allowAbort(start.transferOwner) }
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
            nav.startedSession(sid)
            if (text.trim() == start.prompt) text = ""
            val taken = start.taken
            images.detach(taken)
            taken.forEach { it.release(abort = false) }
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
            else if (!settled) Text("正在读取这个项目上次的设置…", style = T.s12, color = p.ink2, modifier = Modifier.fillMaxWidth().padding(horizontal = 4.dp).testTag("settings-unread"))
            if (c != null && state.machines[c.machineId]?.online != true && drafting) OfflineChoice(state, c, name) { draft.choose(it) }
            else if (c != null && state.machines[c.machineId]?.online != true) Notice("${machineLabel(state, c.machineId)} 不在线，现在不能新建", tone = NoticeTone.WARN)
            if (c != null && missing != null) Notice("${machineLabel(state, c.machineId)} 没有 ${agentName(missing)}，这次用 ${agentName(c.agent)}", action = "换机器", onAction = onMachine)
            if (act.running) Notice("正在 ${c?.let { machineLabel(state, it.machineId) }} 上启动…")
            val frozen = Starts.of(key)
            // An unknown start has its own notice below.
            // 重试 is ↑ again: it is offered when ↑ is.
            if (act.error.isNotEmpty() && !frozen.attempt.frozen) Notice("启动失败：${act.error}", tone = NoticeTone.ERROR, action = "重试".takeIf { canStart }, onAction = { start() })
            if (!act.running && frozen.attempt.frozen) {
                var gaveUp by remember { mutableStateOf(0) }
                Notice(
                    if (frozen.attempt.expired) "不确定「${frozen.prompt.take(40)}」有没有启动，它已过期，不会再重发。请先看看进行中的会话"
                    else "上次的启动结果不确定。再点 ↑ 会重发同一个请求，电脑收到过就不会再启动一次",
                    tone = NoticeTone.WARN, action = "放弃", onAction = { frozen.attempt.abandon(); act.error = ""; gaveUp++ },
                )
            }
        }
    }
    var picking by remember { mutableStateOf(false) }
    // 新任务 (design-system §6): close at the top left and the ink send at the right; the sentence
    // 「在 [项目] 用 [CLI]」; what to do in large type; ＋ and the summary of the settings above the keyboard.
    Column(Modifier.fillMaxSize()) {
        Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 6.dp), verticalAlignment = Alignment.CenterVertically) {
            GlassButton(Lucide.X, "关闭", nav.back)
            Spacer(Modifier.weight(1f))
            Box(
                Modifier.size(44.dp).clip(CircleShape).background(if (canStart) p.btn else p.fill2)
                    .clickable(enabled = canStart, role = Role.Button, onClickLabel = "开始") { start() }
                    .semantics { contentDescription = "开始" },
                contentAlignment = Alignment.Center,
            ) { Ic(Lucide.ArrowUp, 22.dp, if (canStart) p.btnFg else p.ink3) }
        }
        androidx.compose.foundation.layout.FlowRow(
            Modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, top = 20.dp),
            horizontalArrangement = Arrangement.spacedBy(10.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
            itemVerticalAlignment = Alignment.CenterVertically,
        ) {
            Text("在", style = T.s17, color = p.ink2)
            Token(onProject, Modifier.testTag("new-project")) {
                ProjectAvatar(key, 26)
                Text(name, style = T.s15b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
            }
            if (c != null && agents.size > 1) {
                Text("用", style = T.s17, color = p.ink2)
                Token({ picking = true }, Modifier.testTag("new-agent")) { Text(agentName(c.agent), style = T.s15b, color = p.ink, maxLines = 1) }
            }
        }
        banner()
        Box(Modifier.padding(top = 10.dp)) { notices() }
        BasicTextField(
            text, { text = it },
            Modifier.weight(1f).fillMaxWidth().padding(start = 24.dp, end = 24.dp, top = 16.dp).focusRequester(fr),
            textStyle = T.s17.copy(color = p.ink),
            cursorBrush = androidx.compose.ui.graphics.SolidColor(p.blue),
            decorationBox = { inner ->
                Box {
                    if (text.isEmpty()) Text("要做什么？", style = T.h22.copy(fontSize = 24.sp), color = p.ink3)
                    inner()
                }
            },
        )
        Box(Modifier.padding(horizontal = 16.dp)) { ImageStrip(images, remote, machine, maxImages(machine?.let { state.machines[it] })) }
        Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 6.dp, bottom = 12.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            AttachButton(
                onPlus = pick,
                onFile = rememberDocumentPicker(images, remote, machine),
                enabled = !images.restoring && (blocked == null || io.github.lewin671.remoteagent.core.filesSupported(machine?.let { state.machines[it] }) && machineOnline),
            )
            if (summary != null) SummaryChip(summary, full, Modifier.weight(1f, fill = false).testTag("project-settings-summary"), onSettings)
        }
    }
    if (picking && c != null) Sheet({ picking = false }) {
        SheetHead("用哪个 CLI", "取消", { picking = false })
        Group(Modifier.padding(start = 0.dp, end = 0.dp), rows = agents.map { a -> @Composable {
            GRow(agentName(a), onClick = {
                picking = false
                entry.agent.value = a
                val to = projectStart(state, key, a, c.machineId)
                draft.retarget(switchAgent(c, to?.config, a), to?.settled != false)
            }, trailing = { if (a == c.agent) Ic(Lucide.Check, 18.dp, p.blue) })
        } })
    }
}

/** A tappable word in the new task's sentence: fill, 15 / 600 ink, 36 tall. */
@Composable
private fun Token(onClick: (() -> Unit)?, modifier: Modifier = Modifier, content: @Composable RowScope.() -> Unit) {
    Row(
        modifier.heightIn(min = 36.dp).clip(Radii.Pill).background(pal.fill)
            .then(if (onClick != null) Modifier.clickable(role = Role.Button, onClick = onClick) else Modifier)
            .padding(start = 5.dp, end = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(6.dp),
        content = content,
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

fun machineSubtitle(installed: List<Agent>, missing: Agent? = null, fallback: Agent? = null): String {
    val names = installed.joinToString("、") { agentName(it) }
    return if (missing != null && fallback != null) {
        "已装 $names；这台机器没有 ${agentName(missing)}，会改用 ${agentName(fallback)}"
    } else {
        "已装 $names"
    }
}

/**
 * The machine went offline while a draft was written: nothing moves by
 * itself. The user picks another machine the project used, told what that
 * machine offers, and checks the box again before sending.
 */
@Composable
private fun OfflineChoice(state: RemoteState, c: NewConfig, name: String, onChoose: (NewConfig) -> Unit) {
    val p = pal
    val others = projectMachines(state, c.cwd).filter { it != c.machineId && state.machines[it]?.online == true }
    Column(Modifier.fillMaxWidth().clip(Radii.R14).background(p.gbg).padding(horizontal = 12.dp, vertical = 9.dp)) {
        Text("${machineLabel(state, c.machineId)} 已离线，任务尚未开始", style = T.s13, color = p.orangeInk)
        Text("草稿已保留，请选择在线机器；不自动换机器或 CLI。", style = T.s12, color = p.ink2)
        for (id in others) {
            val agents = machineAgents(state.machines[id])
            val cliNote = when {
                c.agent in agents -> machineSubtitle(agents)
                else -> machineSubtitle(agents, missing = c.agent, fallback = switchMachine(c, id, agents).agent)
            }
            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { onChoose(switchMachine(c, id, agents)) }, verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text("改用 ${machineLabel(state, id)}", style = T.s15b, color = p.ink)
                    Text(cliNote, style = T.s12, color = p.ink2)
                }
                Arrow(p.ink2, T.s15)
            }
        }
        if (others.isEmpty()) Text("$name 用过的其他电脑也都不在线。", style = T.s12, color = p.ink2)
    }
}

/** A project-setting row: a short label, the value at the right, an arrow. */
@Composable
private fun SetRow(label: String, value: String, onClick: (() -> Unit)?, dot: androidx.compose.ui.graphics.Color? = null, valueColor: androidx.compose.ui.graphics.Color? = null) {
    val p = pal
    if (LocalDensity.current.fontScale >= 1.8f) {
        Column(Modifier.fillMaxWidth().heightIn(min = 44.dp).then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier).padding(horizontal = 14.dp, vertical = 8.dp)) {
            Text(label, style = T.s13, color = p.ink2, maxLines = 1)
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.End) {
                if (dot != null) StatusMark(MarkKind.ONLINE, color = dot)
                Text(value, style = T.s13, color = valueColor ?: p.ink, textAlign = androidx.compose.ui.text.style.TextAlign.End,
                    modifier = Modifier.padding(start = 8.dp).weight(1f, fill = false))
                Spacer(Modifier.width(8.dp))
                Arrow(p.ink3, ts(18.sp))
            }
        }
        return
    }
    Row(
        Modifier.fillMaxWidth().heightIn(min = 44.dp).then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier).padding(horizontal = 14.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Text(label, style = T.s13, color = p.ink2, maxLines = 1, modifier = Modifier.widthIn(min = 36.dp).weight(1f))
        if (dot != null) StatusMark(MarkKind.ONLINE, color = dot)
        Text(value, style = T.s13, color = valueColor ?: p.ink, maxLines = 2, overflow = TextOverflow.Ellipsis, textAlign = androidx.compose.ui.text.style.TextAlign.End, modifier = Modifier.widthIn(max = 200.dp))
        Arrow(p.ink3, ts(18.sp))
    }
}

fun projectModel(c: NewConfig, models: ListModelsResponse, settings: DaemonSettings? = null): io.github.lewin671.remoteagent.proto.ModelInfo? {
    val defaultModel = defaultsFor(settings, c.agent).model
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
fun NewSettingsSheet(state: RemoteState, cwd: String, name: String, machineId: String, agent: Agent, open: String = "", onDone: (PrefsOp.SetSettings?) -> Unit, onClose: () -> Unit, initial: NewConfig? = null, scheduling: Boolean = false, lockMachine: Boolean = false) {
    val p = pal
    val remote = LocalRemote.current
    val nav = LocalNav.current
    var removing by remember { mutableStateOf(false) }
    var machine by remember { mutableStateOf(machineId) }
    var tab by remember { mutableStateOf(agent) }
    // The sets changed here, by CLI; the others stay as stored.
    val edited = remember { mutableStateMapOf<Agent, NewConfig>().apply { if (initial != null) put(initial.agent, initial) } }
    var page by remember { mutableStateOf(open) }
    var allModels by remember { mutableStateOf(false) }
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
    // Tasks need the displayed launch config even when no setting was edited.
    fun done() = onDone(if (lockMachine || scheduling) PrefsOp.SetSettings(cwd, machine, moved = moved, agent = cur, sets = listOf(c.agentSettings()))
        else if (!moved && edited.isEmpty()) null else PrefsOp.SetSettings(cwd, machine, moved = moved, sets = edited.values.map { it.agentSettings() }))
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
    val blockedPrefs = if (lockMachine) null else prefsBlocked(state)
    val defaults = defaultModeOf(state.machines[c.machineId], c.agent)
    val shownModel = models?.let { projectModel(c, it, settings) }
    Sheet(onClose) {
        when (page) {
            "" -> {
                SheetHead(if (lockMachine) "任务设置" else "项目设置", if (blockedPrefs == null) "完成" else null, { done() })
                if (blockedPrefs != null) Notice(blockedPrefs)
                Row(Modifier.fillMaxWidth().padding(bottom = 12.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    ProjectAvatar(cwd, 28)
                    Column(Modifier.weight(1f)) {
                        Text(name, style = T.s15b, color = p.ink)
                        Text(shortPath(cwd), style = T.mono12, color = p.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
                    }
                }
                FieldLabel("新会话")
                val machineRow: @Composable () -> Unit =
                    { SetRow("机器", machineLabel(state, machine), if (lockMachine) null else ({ page = "machine" }), dot = if (state.machines[machine]?.online == true) p.green else p.faint) }
                val rows = listOf<@Composable () -> Unit>(
                    {
                        SetRow("模型", listOf(shownModel?.displayName?.ifEmpty { null } ?: c.model, c.effort).filter { it.isNotEmpty() }.joinToString(" · ").ifEmpty { "默认模型" }, { page = "model" })
                    },
                    *(if (agentInfo(c.agent).standardContextOption && !scheduling) arrayOf<@Composable () -> Unit>({
                        // A model without a larger window has nothing to choose.
                        val fixed = shownModel != null && shownModel.contextWindow in 1..io.github.lewin671.remoteagent.core.STANDARD_WINDOW
                        SetRow("上下文", if (fixed) "标准 200k · 这个模型只有标准窗口" else if (c.standardContext) "标准 200k" else "1M", if (fixed) null else ({ page = "context" }))
                    }) else emptyArray()),
                    { SetRow(if (lockMachine) "权限" else "审批", effectiveModeText(c.mode, defaults), { page = "mode" }, valueColor = if (c.mode == Mode.FULL || (c.mode == null && (defaults.mode == Mode.FULL || !defaults.known))) p.red else null) },
                )
                @Composable
                fun card(rows: List<@Composable () -> Unit>) = SheetList {
                    rows.forEachIndexed { i, r ->
                        if (i > 0) androidx.compose.material3.HorizontalDivider(color = p.sep, thickness = 0.5.dp)
                        r()
                    }
                }
                val reading: @Composable () -> Unit = {
                    if (state.conn != ConnState.ONLINE || state.sessionsError.isNotEmpty()) EarlierTail(state, Modifier.fillMaxWidth().padding(14.dp))
                    else Text("正在读取 ${agentName(cur)} 上次的设置…", style = T.s13, color = p.ink2, modifier = Modifier.fillMaxWidth().padding(14.dp))
                }
                if (unread && agents.size > 1) {
                    card(listOf(machineRow))
                    Segmented(agents.map { agentName(it) }, agents.indexOf(cur), { tab = agents[it] }, Modifier.padding(top = 6.dp, bottom = 10.dp).fillMaxWidth())
                    card(listOf(reading))
                } else if (unread) {
                    card(listOf(machineRow, reading))
                } else if (agents.size > 1) {
                    // The machine is shared; each CLI has its own set, seen under its tab.
                    card(listOf(machineRow))
                    Segmented(agents.map { agentName(it) }, agents.indexOf(cur), { tab = agents[it] }, Modifier.padding(top = 6.dp, bottom = 10.dp).fillMaxWidth())
                    card(rows)
                    Hint(if (lockMachine) "只修改这个任务；已开出的对话保留原来的设置。" else "各 CLI 独立保存一套，新建时用哪个在输入框旁选。只影响之后新建的会话，进行中的不变；在你的设备之间同步。")
                } else {
                    card(listOf(machineRow) + rows)
                    Hint(if (lockMachine) "只修改这个任务；已开出的对话保留原来的设置。" else "只影响之后新建的会话，进行中的不变；在你的设备之间同步。")
                }
                if (!lockMachine) {
                TextAction(if (io.github.lewin671.remoteagent.core.isPinned(state, cwd)) "取消常用" else "设为常用", {
                    PrefsChanges.run(remote, if (io.github.lewin671.remoteagent.core.isPinned(state, cwd)) PrefsOp.Unpin(cwd) else PrefsOp.Pin(cwd))
                }, enabled = blockedPrefs == null)
                TextAction("移除项目…", { removing = true }, enabled = blockedPrefs == null, color = p.red)
                if (removing) RemoveProjectConfirm(cwd, onDismiss = { removing = false }) { onClose(); nav.home() }
                }
            }
            "machine" -> {
                SheetHead("机器", if (blockedPrefs == null) "完成" else null, { done() }, onBack = { page = "" })
                SheetList { used.forEachIndexed { i, id ->
                    val m = state.machines[id]
                    val online = m?.online == true
                    val agents = machineAgents(m)
                    val has = agent in agents
                    val fallback = if (has) null else switchMachine(c, id, agents).agent
                    val sub = when {
                        !online -> "不在线，现在不能新建"
                        !has -> machineSubtitle(agents, missing = agent, fallback = fallback)
                        else -> machineSubtitle(agents)
                    }
                    ListItem(
                        machineLabel(state, id),
                        sub,
                        checked = machine == id, enabled = online, last = i == used.size - 1,
                        onClick = {
                            if (id != machine) {
                                machine = id
                                edited.clear()
                            }
                            page = ""
                        },
                        trailing = { StatusMark(if (online) MarkKind.ONLINE else MarkKind.OFF) },
                    )
                } }
                Hint("换机器后，各 CLI 的模型和审批从那台机器的默认设置开始。")
            }
            "model" -> {
                SheetHead("模型", if (blockedPrefs == null) "完成" else null, { done() }, onBack = { page = "" })
                val ms = models
                when {
                    modelsError.isNotEmpty() -> Notice("没能读取模型列表，会用 ${agentName(c.agent)} 自己的默认模型：$modelsError", tone = NoticeTone.WARN)
                    ms == null -> Text("读取 ${machineLabel(state, c.machineId)} 的设置…", style = T.s13, color = p.ink2, modifier = Modifier.padding(vertical = 12.dp))
                    else -> {
                        val defaultModel = defaultsFor(settings, c.agent).model.ifEmpty { ms.defaultModel }
                        val shown = shownModels(ms.modelsList, ms.modelsList.firstOrNull { it.slug == c.model }, allModels)
                        val few = shown.size == ms.modelsCount
                        SheetList {
                        ListItem("默认" + if (defaultModel.isNotEmpty()) "（$defaultModel）" else "", checked = c.model.isEmpty(), last = shown.isEmpty(), onClick = { edit(c.copy(model = "", effort = "")) })
                        shown.forEachIndexed { i, m ->
                            ListItem(
                                m.displayName.ifEmpty { m.slug },
                                small = if (m.effortsList.size == 1) "推理强度固定为 ${m.effortsList[0]}" else null,
                                checked = c.model == m.slug,
                                last = i == shown.size - 1,
                                onClick = {
                                    edit(c.copy(model = m.slug, effort = m.defaultEffort, mode = c.mode?.takeUnless { m.autoUnavailable && it == Mode.AUTO }))
                                },
                            )
                        }
                        }
                        if (!few) AllModels { allModels = true }
                        projectModel(c, ms, settings)?.takeIf { it.effortsList.isNotEmpty() }?.let { m ->
                            val defaultEffort = if (c.model.isEmpty()) {
                                defaultsFor(settings, c.agent).effort.ifEmpty { m.defaultEffort }
                            } else m.defaultEffort
                            FieldLabel("推理强度")
                            ChoiceGrid(m.effortsList, c.effort.ifEmpty { defaultEffort }, true) { edit(c.copy(effort = it)) }
                        }
                    }
                }
            }
            "context" -> {
                SheetHead("上下文窗口", if (blockedPrefs == null) "完成" else null, { done() }, onBack = { page = "" })
                SheetList {
                    ListItem("1M", "Claude Code 的默认窗口", checked = !c.standardContext, onClick = { edit(c.copy(standardContext = false)); page = "" })
                    ListItem("标准 200k", "超过 200k 前自动压缩", checked = c.standardContext, last = true, onClick = { edit(c.copy(standardContext = true)); page = "" })
                }
                Hint("窗口在会话开始时确定，改了只影响之后新建的会话。")
            }
            "mode" -> {
                SheetHead("审批模式", if (blockedPrefs == null) "完成" else null, { done() }, onBack = { page = "" })
                val chosen = models?.let { projectModel(c, it, settings) }
                SheetList {
                ListItem("默认", "跟随 ${agentName(c.agent)} 自己的配置", checked = c.mode == null, onClick = { edit(c.copy(mode = null)); page = "" })
                newSessionModes.forEachIndexed { i, m ->
                    // The list says which models come without the auto mode
                    // (Claude Code's Haiku 4.5); until it has come, none is.
                    val off = m == Mode.AUTO && chosen?.autoUnavailable == true
                    ListItem(m.text, if (off) "当前模型不支持" else modeHelp.getValue(m).replace("CLI", agentName(c.agent)), checked = c.mode == m, enabled = !off, last = i == newSessionModes.size - 1, onClick = { edit(c.copy(mode = m)); page = "" })
                }
                }
                Hint("完全放行的新会话直接按此设置启动。")
            }
        }
    }
}

/** The list of 新会话 · 选项目: the projects as every list shows them, each leading to its new-session box. */
@Composable
fun ProjectPickList(state: RemoteState, onPick: (String) -> Unit, onNew: () -> Unit, card: Color = pal.raised) {
    val p = pal
    ProjectList(state, onNew = onNew, card = card) { pr ->
        ProjectRow(state, pr, onClick = { onPick(pr.key) }) { Arrow(p.ink3, ts(18.sp)) }
    }
}

/**
 * 新会话 · 选项目 from the new-session button on the home page, as a sheet; 新项目… turns it
 * into 新项目 · 选目录. Choosing opens the project with its box focused.
 */
@Composable
fun PickProjectSheet(state: RemoteState, onClose: () -> Unit, replace: Boolean = false) {
    val nav = LocalNav.current
    var browsing by remember { mutableStateOf(false) }
    Sheet(onClose, scroll = !browsing) {
        if (!browsing) {
            SheetHead("新会话 · 选项目", "取消", onClose)
            ProjectPickList(state, onPick = { onClose(); if (replace) nav.back(); nav.projectNew(it) }, onNew = { browsing = true })
        } else {
            DirPickerBody(state, onBack = { browsing = false }) { machine, path -> onClose(); nav.newProjectAt(machine, path) }
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
            ProjectPickList(state, onPick = { nav.projectNew(it) }, onNew = { nav.newProject() }, card = pal.gbg)
        }
    }
}

/**
 * 新项目 · 选目录 as a bottom sheet (design-system §6 弹层): it fills the sheet, so the head and the
 * button stay and only the list scrolls. A sheet is closed by its scrim or its handle, so the head has
 * no 取消; [onBack] is there when the sheet came from a step before it.
 */
@Composable
fun DirPickerBody(state: RemoteState, title: String = "新项目 · 选目录", onBack: (() -> Unit)? = null, onChosen: (machine: String, path: String) -> Unit) {
    // Not the whole height: the sheet stays below the status bar, whose icons are drawn for the page behind it.
    Column(Modifier.fillMaxWidth().fillMaxHeight(.88f)) {
        SheetHead(title, onBack = onBack)
        DirPicker(state, Modifier.weight(1f), onChosen)
    }
}

/**
 * Choose a machine (only where the account has more than one; an offline one is greyed), then browse to
 * a folder; [onChosen] with the folder. The machines and the path above the list and the button below it
 * stay; the list between them scrolls and fades into the surface it is on at the edge it passes under,
 * so nothing is drawn under the status bar. Give it a bounded height.
 */
@Composable
fun DirPicker(state: RemoteState, modifier: Modifier = Modifier, onChosen: (machine: String, path: String) -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    var machine by rememberSaveable { mutableStateOf(state.machines.values.firstOrNull { it.online }?.id.orEmpty()) }
    var path by remember { mutableStateOf("") }
    var dirs by remember { mutableStateOf<ListDirectoriesResponse?>(null) }
    var loading by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    var attempt by remember { mutableStateOf(0) }
    LaunchedEffect(machine, path, attempt) {
        if (machine.isEmpty()) return@LaunchedEffect
        loading = true
        error = ""
        try {
            val r = remote.machineCommand(machine, remote.cmd { listDirectories = ListDirectoriesRequest.newBuilder().setPath(path).build() })
            dirs = r.listDirectories
        } catch (e: Exception) {
            error = errorText(e)
        } finally {
            loading = false
        }
    }
    Column(modifier) {
        if (state.machines.size > 1) {
            val listed = state.machines.values.sortedWith(compareByDescending<MachineView> { it.online }.thenBy { machineLabel(state, it.id) })
            SheetList(Modifier.padding(bottom = 10.dp)) {
                listed.forEachIndexed { i, m ->
                    ListItem(
                        machineLabel(state, m.id),
                        if (m.online) null else "不在线，现在不能新建",
                        checked = machine == m.id, enabled = m.online, last = i == listed.size - 1,
                        onClick = { if (machine != m.id) { machine = m.id; path = ""; dirs = null } },
                        trailing = { StatusMark(if (m.online) MarkKind.ONLINE else MarkKind.OFF) },
                    )
                }
            }
        }
        val d = dirs
        when {
            machine.isEmpty() -> Notice("没有在线的电脑", tone = NoticeTone.WARN)
            error.isNotEmpty() -> Notice("读不了这个目录：$error", tone = NoticeTone.ERROR, action = "重试", onAction = { attempt++ })
            d == null -> Text("读取中…", style = T.s13, color = p.ink2, modifier = Modifier.padding(vertical = 12.dp))
            else -> {
                val parts = pathCrumbs(d.path)
                Text(
                    androidx.compose.ui.text.buildAnnotatedString {
                        parts.forEachIndexed { i, part ->
                            if (i > 0) append(" / ")
                            if (i == parts.size - 1) { pushStyle(androidx.compose.ui.text.SpanStyle(color = p.ink, fontWeight = platformWeight(androidx.compose.ui.text.font.FontWeight.SemiBold))); append(part); pop() } else append(part)
                        }
                    },
                    style = T.mono13, color = p.ink2, modifier = Modifier.padding(bottom = 6.dp),
                )
                if (d.dirsList.isEmpty()) Text("没有子目录", style = T.s13, color = p.ink2, modifier = Modifier.padding(vertical = 8.dp))
                // The folder before stays, dimmed, until the next one is listed: no blank in between.
                val listState = remember(d.path) { androidx.compose.foundation.lazy.LazyListState() }
                val fade = if (LocalOnSoft.current) p.gbg else p.paper
                Box(Modifier.weight(1f).fillMaxWidth().alpha(if (loading) .45f else 1f)) {
                    if (d.parent.isNotEmpty() || d.dirsList.isNotEmpty()) LazyColumn(
                        Modifier.clip(Radii.R20).background(if (LocalOnSoft.current) p.raised else p.gbg),
                        state = listState,
                    ) {
                        if (d.parent.isNotEmpty()) item(key = "..") {
                            ListItem("上一级", last = d.dirsList.isEmpty(), onClick = if (loading) null else ({ path = d.parent }), icon = Lucide.ChevronLeft)
                        }
                        itemsIndexed(d.dirsList, key = { _, name -> name }) { i, name ->
                            ListItem(name, last = i == d.dirsList.size - 1, onClick = if (loading) null else ({ path = d.path.trimEnd('/') + "/" + name }), trailing = { Arrow(p.ink2, T.s15) })
                        }
                    }
                    ScrollFade(fade, top = true, show = listState.canScrollBackward)
                    ScrollFade(fade, top = false, show = listState.canScrollForward)
                }
                PillButton(
                    "用 ${if (parts == listOf("/")) "/" else projectName(d.path)} 这个目录", { onChosen(machine, d.path) },
                    Modifier.fillMaxWidth().padding(top = 10.dp), ButtonKind.INK, enabled = !loading,
                )
            }
        }
    }
}

/** The edge of a scrolling list that content passes under: the surface behind it, solid to clear, 24 dp deep (design-system §4 材质). */
@Composable
private fun BoxScope.ScrollFade(color: Color, top: Boolean, show: Boolean) {
    val alpha by androidx.compose.animation.core.animateFloatAsState(if (show) 1f else 0f, label = "scrollFade")
    if (alpha <= 0f) return
    Box(
        Modifier.align(if (top) Alignment.TopCenter else Alignment.BottomCenter).fillMaxWidth().height(24.dp).alpha(alpha)
            .background(androidx.compose.ui.graphics.Brush.verticalGradient(if (top) listOf(color, color.copy(alpha = 0f)) else listOf(color.copy(alpha = 0f), color))),
    )
}

/** 新项目 · 选目录 as a page (from the empty home page). */
@Composable
fun NewProjectScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    PageScaffold("新项目 · 选目录") {
        DirPicker(state, Modifier.weight(1f).padding(horizontal = 16.dp).padding(bottom = 8.dp)) { machine, path -> nav.newProjectAt(machine, path) }
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
    val agents = machineAgents(state.machines[selected.machineId])
    Column(Modifier.fillMaxSize().background(p.paper)) {
        NewSessionBox(
            state, initial, projectName(path), focus = true, onMachine = { settingsOpen = "machine" }, settled = start?.settled != false,
            summary = projectSummary(state, selected, summaryModels).removePrefix(if (agents.size > 1) agentName(selected.agent) + " · " else ""),
            full = startsFull(state, selected), onSettings = { settingsOpen = "" },
        )
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
        var fileIds: List<String> = emptyList()
            private set
        internal var transferOwner: Any = Any()
            private set
        private var preparedImages: List<Processed?> = emptyList()
        /** The images the prepared request names, kept for a retry of the same request. */
        var taken: List<PendingImage> = emptyList()
            set(value) {
                field = value
                transferOwner = Any()
                value.forEach { it.protectTransfer(transferOwner) }
                imageIds = value.filterNot { it.isFile }.mapNotNull { it.imageId }
                fileIds = value.filter { it.isFile }.mapNotNull { it.fileId }
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
