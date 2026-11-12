package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.listProjectName
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.UiMessage
import io.github.lewin671.remoteagent.core.MessageText
import io.github.lewin671.remoteagent.core.messageText
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.ui.semantics.selected
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
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.ime
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
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.snapshotFlow
import androidx.compose.runtime.withFrameNanos
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.layout.boundsInWindow
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
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
 * The modes a scheduled task can be given besides the default; a new task
 * has none of its own (architecture §13). Neither CLI starts in plan.
 */
val newSessionModes = listOf(Mode.ASK, Mode.AUTO, Mode.FULL)

/**
 * How a new session starts; empty model and effort mean the machine's defaults.
 * [mode] is a scheduled task's only: a new task's is always null, and its
 * request leaves the mode to the computer.
 */
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
 * machine's defaults), else the model and effort the project's latest
 * session on that CLI runs with, else the machine's defaults. No approval
 * mode: one stored earlier is not read, and none is carried over.
 */
fun agentSet(state: RemoteState, key: String, agent: Agent, saved: ProjectSettings? = savedSettings(state, key)): AgentSettings =
    agentSet(Inheritance(state, key), agent, saved)

/** Whether [agentSet] is settled for this CLI: see [ProjectStart.settled]. */
fun agentSetSettled(state: RemoteState, key: String, agent: Agent, saved: ProjectSettings? = savedSettings(state, key)): Boolean =
    Inheritance(state, key).also { agentSet(it, agent, saved) }.unsettled.not()

private fun agentSet(from: Inheritance, agent: Agent, saved: ProjectSettings?): AgentSettings {
    saved?.agentsList?.firstOrNull { it.agent == agent }?.let { return it.toBuilder().clearMode().build() }
    val b = AgentSettings.newBuilder().setAgent(agent)
    from.first { it.agent() == agent }?.let { last ->
        b.setModel(last.info?.model.orEmpty()).setEffort(last.info?.effort.orEmpty())
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

/** The configuration's CLI set, as project settings store it; a scheduled task's keeps its mode in it. */
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
    // Project settings keep no approval mode.
    return PrefsOp.SetSettings(used.cwd, project.machineId, agent = used.agent, sets = listOfNotNull(set?.copy(mode = null)?.agentSettings()), keepExisting = true)
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
fun modeLabel(chosen: Mode?, d: DefaultMode, agent: Agent, includeDefault: Boolean = true): String = when {
    chosen != null -> chosen.text
    !d.known -> "跟随 CLI 设置"
    d.mode != null -> if (includeDefault) "默认：${d.mode.text}" else d.mode.text
    else -> if (includeDefault) "默认：${agentName(agent)} 自己的模式" else "${agentName(agent)} 自己的模式"
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
 * The request that starts a new task. It names no approval mode: the
 * computer applies its default for the CLI (设置 › 电脑 › 新项目默认设置).
 */
fun newTaskRequest(cfg: NewConfig, prompt: String, imageIds: List<String>, fileIds: List<String>): CreateSessionRequest =
    CreateSessionRequest.newBuilder().setCwd(cfg.cwd).setPrompt(prompt).setAgent(cfg.agent)
        .setModel(cfg.model).setEffort(cfg.effort).setMode(ApprovalMode.APPROVAL_MODE_UNSPECIFIED)
        .setStandardContext(agentInfo(cfg.agent).standardContextOption && cfg.standardContext)
        .addAllImageIds(imageIds).addAllFileIds(fileIds).build()

/** What keeps a new task from starting in the computer's default mode, auto: see [autoIssue]. */
enum class AutoIssue {
    /** This model has no auto mode. */
    MODEL,
    /** No model of the account has it. */
    ACCOUNT,
}

/**
 * Why a session cannot start in [mode] (the computer's default for a new
 * task) with this CLI and model, or null: only auto can be missing.
 */
fun autoIssue(agent: Agent, model: String, mode: Mode?, models: List<io.github.lewin671.remoteagent.proto.ModelInfo>?): AutoIssue? {
    if (!agentInfo(agent).autoMayBeUnavailable || mode != Mode.AUTO || !io.github.lewin671.remoteagent.core.autoUnavailable(models, model)) return null
    return if (io.github.lewin671.remoteagent.core.allAutoUnavailable(models)) AutoIssue.ACCOUNT else AutoIssue.MODEL
}

/** The session a start created, or null when it did not. */
fun startedSession(r: io.github.lewin671.remoteagent.proto.CommandResult?): String? =
    r?.takeIf { it.hasCreateSession() && it.createSession.hasSession() }?.createSession?.session?.id?.takeIf { it.isNotEmpty() }

/**
 * 新任务 in a project (design-system §6 新任务): a whole page, the title saying
 * where (the project, and under it the computer when there are several), and
 * the box above the keyboard with ＋, the CLI, the model and ↑. The project's
 * sessions are on the home page in its scope.
 */
@Composable
fun ProjectScreen(key: String, focus: Boolean) {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val p = pal
    val hidden = io.github.lewin671.remoteagent.core.isHidden(state, key)
    // The machine picker uses project settings; model choices stay with the composer.
    var settingsOpen by remember { mutableStateOf<String?>(null) }
    // What the next session starts with, and whether the session it inherits from is known
    // yet ([ProjectStart]): the message box fetches pages until it is, and starts nothing before.
    val seed = projectStart(state, key)
    val entry = Drafts.newSession(key, seed?.config, seed?.settled != false)
    val start = projectStart(state, key, entry.agent.value)
    // 项目设置 is about the project: its machine and its set for the CLI selected in the box.
    val selected = projectConfig(state, key, draftPick(state, entry))
    var picking by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxSize().background(p.paper)) {
        NewSessionBox(
            state, start?.config, uiText(listProjectName(key)), focus, onMachine = { settingsOpen = "machine" }, settled = start?.settled != false,
            onProject = { picking = true },
            banner = {
                Column(Modifier.padding(horizontal = 12.dp)) {
                    when (val change = PrefsChanges.states[key]) {
                        PrefsChanges.State.Saving -> Notice(uiText(UiMessages.projectsAndroidSavingSettings()), Modifier.padding(top = 8.dp))
                        is PrefsChanges.State.Failed -> Notice(uiText(UiMessages.projectsAndroidSettingsFailed(reason = change.error)), Modifier.padding(top = 8.dp), tone = NoticeTone.ERROR,
                            action = uiText(UiMessages.commonRetry()), onAction = { PrefsChanges.run(remote, change.op) })
                        else -> {}
                    }
                    if (hidden) HiddenBanner(state, key)
                }
            },
        )
    }
    if (picking) PickProjectSheet(state, { picking = false }, replace = true)
    val open = settingsOpen
    if (open != null && selected != null) NewSettingsSheet(state, key, uiText(listProjectName(key)), selected.machineId, selected.agent, open, onDone = { op ->
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

/** The model a start with [c] uses, as the box names it: its display name, the machine's default before one is chosen. */
fun modelLabel(c: NewConfig, models: ListModelsResponse? = null): MessageText {
    val modelId = c.model.ifEmpty { models?.defaultModel.orEmpty() }
    val chosen = models?.let { io.github.lewin671.remoteagent.core.findModel(it.modelsList, modelId) { m -> m.slug } }
    return (chosen?.displayName?.ifEmpty { modelId } ?: modelId).takeIf { it.isNotEmpty() }?.messageText() ?: UiMessages.newSessionDefaultModel().messageText()
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
        Notice(
            if (change == PrefsChanges.State.Saving) uiText(UiMessages.projectsAndroidAdding()) else uiText(UiMessages.projectsRemoved()),
            action = if (blocked == null && change != PrefsChanges.State.Saving) uiText(UiMessages.projectsAddAgain()) else null,
            onAction = { PrefsChanges.run(remote, PrefsOp.Unhide(key)) },
        )
        if (blocked != null) Hint(uiText(blocked), color = pal.orangeInk, inset = 12.dp)
        if (change is PrefsChanges.State.Failed) Hint(uiText(UiMessages.projectsAndroidSaveFailed(reason = change.error)), color = pal.red, inset = 12.dp)
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

/**
 * The new-session page (client-v8, design-system §6 新任务): close at the top
 * left and, in the middle, where it runs: the project ([onProject] changes
 * it) and, with more than one computer, the computer under it ([onMachine]).
 * The box above the keyboard is the session's: the text, then ＋, the CLI
 * (when the machine has more than one, client-v26), the model ([onModel])
 * and ↑. The configuration comes from project settings and stays with an
 * active draft; there is no approval mode, the computer applies its default.
 * An offline machine is replaced only by the user's choice for this send.
 * The first message starts the session: there is no start without one.
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
    onProject: (() -> Unit)? = null,
    banner: @Composable () -> Unit = {},
) {
    val p = pal
    val remote = LocalRemote.current
    val nav = LocalNav.current
    androidx.activity.compose.BackHandler { nav.back() }
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
    // The mode a new task gets is the machine's default; it cannot start when that is auto and the model has none.
    val defaults = config?.let { defaultModeOf(state.machines[it.machineId], it.agent) } ?: DefaultMode(null, false, false)
    // Keep the edit with its destination, outside the popup: every user dismissal saves it.
    var edited by remember(key, machine, config?.agent) { mutableStateOf<NewConfig?>(null) }
    val closePicker by rememberUpdatedState<() -> Unit> {
        val next = edited
        edited = null
        val target = draft.value
        if (next != null && settled && prefsBlocked(state) == null &&
            next.cwd == target?.cwd && next.machineId == target.machineId && next.agent == target.agent) {
            val op = PrefsOp.SetSettings(key, next.machineId, sets = listOf(next.agentSettings()))
            PrefsChanges.run(remote, op)
            draft.choose(next)
        }
    }
    val picker = remember { ComposerPicker { closePicker() } }
    val keyboard = LocalSoftwareKeyboardController.current
    val focusManager = LocalFocusManager.current
    val density = LocalDensity.current
    val ime = WindowInsets.ime
    val pickerScope = rememberCoroutineScope()
    var opening by remember { mutableStateOf<Job?>(null) }
    fun afterKeyboard(action: () -> Unit) {
        opening?.cancel()
        focusManager.clearFocus(force = true)
        keyboard?.hide()
        opening = pickerScope.launch {
            // Visibility flips at the start of an IME animation. Its bottom
            // reaches zero only after the page has recovered its full height.
            snapshotFlow { ime.getBottom(density) }.first { it == 0 }
            withFrameNanos { }
            action()
        }
    }
    var models by remember(machine, config?.agent) { mutableStateOf<ListModelsResponse?>(null) }
    var modelsRead by remember(machine, config?.agent) { mutableStateOf(false) }
    var modelsError by remember(machine, config?.agent) { mutableStateOf("") }
    var settings by remember(machine, config?.agent) { mutableStateOf<DaemonSettings?>(null) }
    LaunchedEffect(key, machine, config?.agent) {
        opening?.cancel()
        picker.cancel()
        val cfg = config ?: return@LaunchedEffect
        runCatching { remote.listModels(cfg.machineId, cfg.agent) }
            .onSuccess { models = it }.onFailure { modelsError = errorText(it) }
        modelsRead = true
    }
    // Keep the composer's snapshot visible while refreshing. Remote coalesces an in-flight cold
    // read with this request; opening a picker never creates a second empty model state.
    LaunchedEffect(machine, config?.agent, picker.open == "model") {
        val cfg = config ?: return@LaunchedEffect
        if (picker.open == "model") {
            runCatching { remote.listModels(cfg.machineId, cfg.agent, fresh = true) }
                .onSuccess { models = it; modelsError = "" }.onFailure { modelsError = errorText(it) }
        }
    }
    LaunchedEffect(machine, config?.agent, picker.open == "model") {
        val cfg = config ?: return@LaunchedEffect
        if (picker.open != "model" && settings != null) return@LaunchedEffect
        runCatching { remote.machineCommand(cfg.machineId, remote.cmd { getSettings = GetSettingsRequest.getDefaultInstance() }).getSettings.settings }
            .onSuccess { settings = it }
    }
    val issue = config?.let { autoIssue(it.agent, it.model.ifEmpty { models?.defaultModel.orEmpty() }, defaults.mode, models?.modelsList) }
    // Images go to the machine the session will start on; a new machine gets them again.
    val blocked = uploadBlocked(machine?.let { state.machines[it] }, online, machine?.let { machineLabel(state, it) }.orEmpty())
    RefreshDraftUploads(images, remote, machine, blocked)
    val pick = rememberImagePicker(images, remote, machine, blocked, maxImages(machine?.let { state.machines[it] }), videos = io.github.lewin671.remoteagent.core.filesSupported(machine?.let { state.machines[it] }))
    val act = rememberAction()
    // Whether a session can be started with the draft as it is: ↑ is drawn by it, and 重试 after a
    // failure is held to it too.
    val canStart = !images.restoring && !images.readingFiles && fileDraftBlocked(images, machine?.let { state.machines[it] }) == null && config != null && settled && issue == null && !act.running && machineOnline && startable(text, images.images, config.machineId)
    fun launch(cfg: NewConfig) {
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
                    remote.prepare(cfg.machineId, "", remote.cmd { createSession = newTaskRequest(cfg, start.prompt, ids, start.fileIds) })
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
        launch(cfg)
    }
    val c = config
    val agents = c?.let { machineAgents(state.machines[it.machineId]) }.orEmpty()
    val missing = c?.let { missingAgent(state, it, entry.agent.value) }
    val notices = @Composable {
        Column(Modifier.padding(horizontal = 12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            // Why nothing can start yet; a page that failed says so, with the tap that tries again.
            if (!settled && (state.conn != ConnState.ONLINE || state.sessionsError.isNotEmpty())) EarlierTail(state, Modifier.fillMaxWidth().padding(horizontal = 4.dp).testTag("settings-unread"))
            else if (!settled) Text(uiText(UiMessages.newSessionReadingPrevious()), style = T.s12, color = p.ink2, modifier = Modifier.fillMaxWidth().padding(horizontal = 4.dp).testTag("settings-unread"))
            if (c != null && state.machines[c.machineId]?.online != true && drafting) OfflineChoice(state, c, name) { draft.choose(it) }
            else if (c != null && state.machines[c.machineId]?.online != true) Notice(uiText(UiMessages.newSessionAndroidMachineOffline(name = machineLabel(state, c.machineId))), tone = NoticeTone.WARN)
            if (c != null && missing != null) Notice(uiText(UiMessages.newSessionCliFallback(agent = agentName(c.agent), missing = agentName(missing), name = machineLabel(state, c.machineId))), action = uiText(UiMessages.newSessionChangeMachine()), onAction = { afterKeyboard(onMachine) })
            if (c != null && issue == AutoIssue.MODEL) Notice(uiText(UiMessages.newSessionAndroidModelNoAuto(name = machineLabel(state, c.machineId))),
                tone = NoticeTone.WARN, action = uiText(UiMessages.newSessionChangeModel()), onAction = { picker.toggle("model") })
            if (c != null && issue == AutoIssue.ACCOUNT) Notice(uiText(UiMessages.newSessionAndroidAccountNoAuto(name = machineLabel(state, c.machineId))),
                tone = NoticeTone.WARN, action = uiText(UiMessages.newSessionChangeDefaults()), onAction = { nav.device(c.machineId) })
            if (act.running) Notice(uiText(UiMessages.newSessionStartingOn(name = (c?.let { machineLabel(state, it.machineId) }).toString())))
            val frozen = Starts.of(key)
            // An unknown start has its own notice below.
            // 重试 is ↑ again: it is offered when ↑ is.
            if (act.error.isNotEmpty() && !frozen.attempt.frozen) Notice(uiText(UiMessages.newSessionStartFailed(reason = act.error)), tone = NoticeTone.ERROR, action = uiText(UiMessages.commonRetry()).takeIf { canStart }, onAction = { start() })
            if (!act.running && frozen.attempt.frozen) {
                var gaveUp by remember { mutableStateOf(0) }
                Notice(
                    if (frozen.attempt.expired) uiText(UiMessages.newSessionAndroidExpiredStart(prompt = frozen.prompt.take(40)))
                    else uiText(UiMessages.newSessionAndroidUnknownStart()),
                    tone = NoticeTone.WARN, action = uiText(UiMessages.newSessionAndroidAbandon()), onAction = { frozen.attempt.abandon(); act.error = ""; gaveUp++ },
                )
            }
        }
    }
    // 新任务 (design-system §6): close at the top left, where it runs in the middle; the notices; the box above the keyboard.
    ComposerPickerBack(picker)
    Column(Modifier.fillMaxSize().composerPickerEvents(picker)) {
        Row(Modifier.fillMaxWidth().onGloballyPositioned { picker.top = it.boundsInWindow().bottom }.padding(start = 16.dp, end = 16.dp, top = 6.dp), verticalAlignment = Alignment.CenterVertically) {
            GlassButton(Lucide.X, uiText(UiMessages.commonClose()), nav.back)
            Column(Modifier.weight(1f).padding(horizontal = 8.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                Row(
                    Modifier.heightIn(min = 32.dp).clip(Radii.Pill)
                        .then(if (onProject != null) Modifier.clickable(role = Role.Button, onClickLabel = uiText(UiMessages.newSessionAndroidChangeProject()), onClick = { afterKeyboard(onProject) }) else Modifier)
                        .padding(horizontal = 6.dp).testTag("new-project"),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(6.dp),
                ) {
                    ProjectAvatar(key, 22)
                    Text(name, style = T.s17b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                    if (onProject != null) Arrow(p.ink3, T.s13, down = true)
                }
                // The computer only where there is more than one to mean.
                if (c != null && state.machines.size > 1) Row(
                    Modifier.heightIn(min = 24.dp).clip(Radii.Pill).clickable(role = Role.Button, onClickLabel = uiText(UiMessages.newSessionAndroidChangeComputer()), onClick = { afterKeyboard(onMachine) })
                        .padding(horizontal = 6.dp).testTag("new-machine"),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(2.dp),
                ) {
                    Text(machineLabel(state, c.machineId), style = T.s12, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                    Arrow(p.ink3, T.s12, down = true)
                }
            }
            Spacer(Modifier.size(44.dp))
        }
        banner()
        Column(Modifier.weight(1f).fillMaxWidth().verticalScroll(rememberScrollState()).padding(top = 10.dp)) { notices() }
        ComposeBox(
            text, { text = it },
            modifier = Modifier.onGloballyPositioned { picker.composer = it.boundsInWindow() }.testTag("new-composer"),
            placeholder = uiText(UiMessages.newSessionPrompt()),
            canSend = canStart,
            onSend = { start() },
            onPlus = pick,
            onFile = rememberDocumentPicker(images, remote, machine),
            plusEnabled = !images.restoring && (blocked == null || io.github.lewin671.remoteagent.core.filesSupported(machine?.let { state.machines[it] }) && machineOnline),
            sendLabel = uiText(UiMessages.newSessionAndroidStart()),
            focusRequester = fr,
            hasDraft = drafting,
            top = { ImageStrip(images, remote, machine, maxImages(machine?.let { state.machines[it] })) },
            tools = {
                // Pending choices are visible immediately; persistence still happens only on close.
                val current = edited ?: c
                if (current != null) NewTaskTools(
                    agent = current.agent.takeIf { agents.size > 1 },
                    summary = newTaskModelSummary(current, models, settings, modelsRead, agents.size == 1),
                    modelOpen = picker.open == "model",
                    onAgent = { picker.toggle("agent") }, onModel = { picker.toggle("model") },
                    modifier = Modifier.weight(1f, fill = false),
                    agentModifier = Modifier.onGloballyPositioned { picker.agent = it.boundsInWindow() },
                    modelModifier = Modifier.onGloballyPositioned { picker.model = it.boundsInWindow() },
                )
            },
        )
    }
    if (picker.open == "agent" && c != null) ComposerPopover(picker, uiText(UiMessages.newSessionCliPickerLabel()), panel = false) {
        agents.forEach { a ->
            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clip(RoundedCornerShape(8.dp))
                .selectable(selected = a == c.agent, role = Role.RadioButton, onClick = {
                    picker.close()
                    entry.agent.value = a
                    val to = projectStart(state, key, a, c.machineId)
                    draft.retarget(switchAgent(c, to?.config, a), to?.settled != false)
                }).padding(horizontal = 12.dp, vertical = 8.dp),
                verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                AgentMark(a, T.s17, p.ink)
                Text(agentName(a), style = T.s17, color = p.ink, modifier = Modifier.weight(1f))
                if (a == c.agent) Ic(Lucide.Check, 18.dp, p.blue)
            }
        }
    }
    if (picker.open == "model" && c != null) ComposerPopover(picker, uiText(UiMessages.modelsTitle()), panel = true) {
        val current = edited ?: c
        val blockedPrefs = prefsBlocked(state)
        NewTaskModelPanel(current, models, modelsError, settings, machineLabel(state, c.machineId),
            enabled = settled && blockedPrefs == null, onClose = picker::close,
            notice = {
                if (blockedPrefs != null) Notice(uiText(blockedPrefs))
                if (!settled) Text(uiText(UiMessages.newSessionReadingPrevious()), style = T.s13, color = p.ink2, modifier = Modifier.testTag("settings-unread"))
            }, edit = { edited = it })
    }
}

/** The CLI or the model in the new task's box: no fill, 15 ink2, a small down arrow, 40 tall. */
@Composable
internal fun BoxTool(onClick: () -> Unit, label: String, modifier: Modifier = Modifier, active: Boolean = false, content: @Composable RowScope.() -> Unit) {
    Row(
        modifier.heightIn(min = 40.dp).clip(Radii.Pill)
            .then(if (active) Modifier.background(pal.fill).semantics { selected = true } else Modifier)
            .clickable(interactionSource = remember { MutableInteractionSource() }, indication = if (active) null else androidx.compose.foundation.LocalIndication.current,
                role = Role.Button, onClick = onClick)
            .semantics(mergeDescendants = true) { contentDescription = label }.padding(horizontal = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        content()
        Arrow(pal.ink3, T.s12, down = true)
    }
}

/**
 * Whether a draft is something to start a session with on [machineId]:
 * there is no start with nothing to send, and none while an image is not on
 * that machine yet. Never an empty session, never one with only some of the
 * images.
 */
fun startable(text: String, images: List<PendingImage>, machineId: String): Boolean =
    (text.isNotBlank() || images.isNotEmpty()) && images.all { it.readyFor(machineId) }

fun machineSubtitle(installed: List<Agent>, separator: String, missing: Agent? = null, fallback: Agent? = null): UiMessage {
    val names = installed.joinToString(separator) { agentName(it) }
    return if (missing != null && fallback != null) {
        UiMessages.newSessionInstalledFallback(agent = agentName(fallback), agents = names, missing = agentName(missing))
    } else {
        UiMessages.newSessionInstalled(agents = names)
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
        Text(uiText(UiMessages.newSessionAndroidOfflineDraft(name = machineLabel(state, c.machineId))), style = T.s13, color = p.orangeInk)
        Text(uiText(UiMessages.newSessionAndroidDraftPreserved()), style = T.s12, color = p.ink2)
        for (id in others) {
            val agents = machineAgents(state.machines[id])
            val cliNote = when {
                c.agent in agents -> uiText(machineSubtitle(agents, uiText(UiMessages.commonListSeparator())))
                else -> uiText(machineSubtitle(agents, uiText(UiMessages.commonListSeparator()), missing = c.agent, fallback = switchMachine(c, id, agents).agent))
            }
            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { onChoose(switchMachine(c, id, agents)) }, verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text(uiText(UiMessages.newSessionUseMachine(name = machineLabel(state, id))), style = T.s15b, color = p.ink)
                    Text(cliNote, style = T.s12, color = p.ink2)
                }
                Arrow(p.ink2, T.s15)
            }
        }
        if (others.isEmpty()) Text(uiText(UiMessages.newSessionAndroidOthersOffline(name = name)), style = T.s12, color = p.ink2)
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
 * 项目设置 (client-v26): the machine (where the account has more than one), and the model and effort
 * and context window (Claude Code, on the model's page) of one CLI, [agent], the one selected in the new
 * task's box (or the session's); a new task has no approval mode. Another CLI's set is reached by
 * selecting that CLI there, so the sheet has no switch between them; with more than one CLI on the machine
 * the set is under a title with the CLI's name. With one machine the model is all there is, so its page
 * opens at once. It opens on [open]; opened at a page, that page is the whole sheet, with no way back.
 * Which CLI a new session uses is not chosen here. [onDone] gets the change to store, or null when
 * nothing was changed.
 * [scheduling]: a scheduled task's settings, which keep the project's name and path, the 权限 row (its
 * approval mode), the note and, while the task is created, the project's actions. An existing scheduled
 * task has no CLI choice outside the sheet, so there ([chooseAgent]) the CLIs are switched between in it.
 */
@Composable
fun NewSettingsSheet(state: RemoteState, cwd: String, name: String, machineId: String, agent: Agent, open: String = "", onDone: (PrefsOp.SetSettings?) -> Unit, onClose: () -> Unit, initial: NewConfig? = null, scheduling: Boolean = false, lockMachine: Boolean = false, chooseAgent: Boolean = false) {
    val p = pal
    val remote = LocalRemote.current
    val nav = LocalNav.current
    var tab by remember { mutableStateOf(agent) }
    var removing by remember { mutableStateOf(false) }
    var machine by remember { mutableStateOf(machineId) }
    // The sets changed here, by CLI; the others stay as stored.
    val edited = remember { mutableStateMapOf<Agent, NewConfig>().apply { if (initial != null) put(initial.agent, initial) } }
    val manyMachines = state.machines.size > 1
    // A new task's settings with one machine are the model's page alone.
    val start = open.ifEmpty { if (!scheduling && !manyMachines) "model" else "" }
    var page by remember { mutableStateOf(start) }
    // Opened right at a page: done with it is done with the sheet.
    val back: (() -> Unit)? = if (start.isEmpty()) ({ page = "" }) else null
    // Models are per machine: on another one every CLI starts over.
    val moved = machine != machineId
    val agents = machineAgents(state.machines[machine])
    // The CLI the sheet was opened for, or, on a machine that does not have it, the first it has.
    val cur = (if (chooseAgent) tab else agent).let { if (it in agents) it else agents.first() }
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
                SheetHead(if (lockMachine) "任务设置" else uiText(UiMessages.projectsAndroidSettings()), if (blockedPrefs == null) uiText(UiMessages.commonDone()) else null, { done() })
                if (blockedPrefs != null) Notice(uiText(blockedPrefs))
                if (scheduling) Row(Modifier.fillMaxWidth().padding(bottom = 12.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    ProjectAvatar(cwd, 28)
                    Column(Modifier.weight(1f)) {
                        Text(name, style = T.s15b, color = p.ink)
                        Text(shortPath(cwd), style = T.mono12, color = p.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
                    }
                }
                FieldLabel(uiText(UiMessages.newSessionAndroidNewSession()))
                val machineRow: @Composable () -> Unit =
                    { SetRow(uiText(UiMessages.commonMachine()), machineLabel(state, machine), if (lockMachine) null else ({ page = "machine" }), dot = if (state.machines[machine]?.online == true) p.green else p.faint) }
                // A new task's machine is there only to choose among several.
                val machineRows = if (scheduling || manyMachines) listOf(machineRow) else emptyList()
                val rows = listOf<@Composable () -> Unit>(
                    {
                        SetRow(uiText(UiMessages.modelsTitle()), listOf(shownModel?.displayName?.ifEmpty { null } ?: c.model, c.effort).filter { it.isNotEmpty() }.joinToString(" · ").ifEmpty { uiText(UiMessages.newSessionDefaultModel()) }, { page = "model" })
                    },
                    // A scheduled task's approval mode; a new task has none.
                    *(if (scheduling) arrayOf<@Composable () -> Unit>({
                        SetRow("权限", effectiveModeText(c.mode, defaults), { page = "mode" }, valueColor = if (c.mode == Mode.FULL || (c.mode == null && (defaults.mode == Mode.FULL || !defaults.known))) p.red else null)
                    }) else emptyArray()),
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
                    else Text(uiText(UiMessages.newSessionAndroidReadingCli(agent = agentName(cur))), style = T.s13, color = p.ink2, modifier = Modifier.fillMaxWidth().padding(14.dp))
                }
                if (unread && agents.size > 1) {
                    if (machineRows.isNotEmpty()) card(machineRows)
                    if (chooseAgent) Segmented(agents.map { agentName(it) }, agents.indexOf(cur), { tab = agents[it] }, Modifier.padding(top = 6.dp, bottom = 10.dp).fillMaxWidth()) else FieldLabel(agentName(cur))
                    card(listOf(reading))
                } else if (unread) {
                    card(machineRows + listOf(reading))
                } else if (agents.size > 1) {
                    // The machine is shared; the set under it is the CLI's, whose name is its title.
                    if (machineRows.isNotEmpty()) card(machineRows)
                    if (chooseAgent) Segmented(agents.map { agentName(it) }, agents.indexOf(cur), { tab = agents[it] }, Modifier.padding(top = 6.dp, bottom = 10.dp).fillMaxWidth()) else FieldLabel(agentName(cur))
                    card(rows)
                    if (scheduling) Hint(if (lockMachine) "只修改这个任务；已开出的对话保留原来的设置。" else "各 CLI 独立保存一套。只影响之后新建的会话，进行中的不变；在你的设备之间同步。")
                } else {
                    card(machineRows + rows)
                    if (scheduling) Hint(if (lockMachine) "只修改这个任务；已开出的对话保留原来的设置。" else "只影响之后新建的会话，进行中的不变；在你的设备之间同步。")
                }
                // The project's own actions are in 设置 › 项目管理; a scheduled task being created keeps them here.
                if (scheduling && !lockMachine) {
                // In line with the words of the cards above: the sheet's 16 plus the card rows' 12.
                TextAction(if (io.github.lewin671.remoteagent.core.isPinned(state, cwd)) "取消常用" else "设为常用", {
                    PrefsChanges.run(remote, if (io.github.lewin671.remoteagent.core.isPinned(state, cwd)) PrefsOp.Unpin(cwd) else PrefsOp.Pin(cwd))
                }, Modifier.padding(horizontal = 12.dp), enabled = blockedPrefs == null)
                TextAction("移除项目…", { removing = true }, Modifier.padding(horizontal = 12.dp), enabled = blockedPrefs == null, color = p.red)
                if (removing) RemoveProjectConfirm(cwd, onDismiss = { removing = false }) { onClose(); nav.home() }
                }
            }
            "machine" -> {
                SheetHead(uiText(UiMessages.commonMachine()), if (blockedPrefs == null) uiText(UiMessages.commonDone()) else null, { done() }, onBack = back)
                SheetList { used.forEachIndexed { i, id ->
                    val m = state.machines[id]
                    val online = m?.online == true
                    val agents = machineAgents(m)
                    val has = agent in agents
                    val fallback = if (has) null else switchMachine(c, id, agents).agent
                    val sub = when {
                        !online -> uiText(UiMessages.newSessionAndroidOfflineNoStart())
                        !has -> uiText(machineSubtitle(agents, uiText(UiMessages.commonListSeparator()), missing = agent, fallback = fallback))
                        else -> uiText(machineSubtitle(agents, uiText(UiMessages.commonListSeparator())))
                    }
                    ListItem(
                        machineLabel(state, id),
                        sub,
                        checked = machine == id, enabled = online, last = i == used.size - 1,
                        onClick = {
                            // Opened at this page for a new task: the choice is the whole change.
                            if (back == null && !scheduling) onDone(if (id == machineId) null else PrefsOp.SetSettings(cwd, id, moved = true))
                            else {
                                if (id != machine) {
                                    machine = id
                                    edited.clear()
                                }
                                page = ""
                            }
                        },
                        trailing = { StatusMark(if (online) MarkKind.ONLINE else MarkKind.OFF) },
                    )
                } }
                Hint(if (scheduling) "换机器后，各 CLI 的模型和权限从那台机器的默认设置开始。" else uiText(UiMessages.newSessionAndroidChangeMachineHint()))
            }
            "model" -> {
                SheetHead(uiText(UiMessages.modelsTitle()), if (blockedPrefs == null) uiText(UiMessages.commonDone()) else null, { done() }, onBack = back)
                ModelSettingsBody(c, models, modelsError, settings, machineLabel(state, c.machineId), scheduling) { edit(it) }
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

/** Shared by project/scheduled sheets and the new task's anchored panel. */
@Composable
internal fun ModelSettingsBody(
    c: NewConfig,
    models: ListModelsResponse?,
    modelsError: String,
    settings: DaemonSettings?,
    machineName: String,
    scheduling: Boolean,
    enabled: Boolean = true,
    edit: (NewConfig) -> Unit,
) {
    val p = pal
    var allModels by remember(c.machineId, c.agent) { mutableStateOf(false) }
    val shownModel = models?.let { projectModel(c, it, settings) }
    val ms = models
    when {
        ms == null && modelsError.isNotEmpty() -> Notice(uiText(UiMessages.modelsAndroidLoadFailedDefault(agent = agentName(c.agent), reason = modelsError)), tone = NoticeTone.WARN)
        ms == null -> Text(uiText(UiMessages.modelsAndroidReadingMachine(name = machineName)), style = T.s13, color = p.ink2, modifier = Modifier.padding(vertical = 12.dp))
        else -> {
            val defaultModel = defaultsFor(settings, c.agent).model.ifEmpty { ms.defaultModel }
            val shown = shownModels(ms.modelsList, ms.modelsList.firstOrNull { it.slug == c.model }, allModels)
            val few = shown.size == ms.modelsCount
            SheetList {
            ListItem(if (defaultModel.isNotEmpty()) uiText(UiMessages.modelsDefaultNamed(defaultModel)) else uiText(UiMessages.modelsDefault()), checked = c.model.isEmpty(), enabled = enabled, last = shown.isEmpty(), onClick = { edit(c.copy(model = "", effort = "")) })
            shown.forEachIndexed { i, m ->
                ListItem(
                    m.displayName.ifEmpty { m.slug },
                    small = if (m.effortsList.size == 1) uiText(UiMessages.modelsFixedEffort(effort = m.effortsList[0])) else null,
                    checked = c.model == m.slug, enabled = enabled,
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
                FieldLabel(uiText(UiMessages.modelsEffort()))
                ChoiceGrid(m.effortsList, c.effort.ifEmpty { defaultEffort }, enabled) { edit(c.copy(effort = it)) }
            }
        }
    }
    // Claude Code's context window, on the model's page: a new task's settings are this page.
    if (agentInfo(c.agent).standardContextOption && !scheduling) {
        FieldLabel(uiText(UiMessages.modelsContextWindow()))
        // A model without a larger window has nothing to choose.
        if (shownModel != null && shownModel.contextWindow in 1..io.github.lewin671.remoteagent.core.STANDARD_WINDOW) Hint(uiText(UiMessages.modelsStandardOnly()))
        else Segmented(listOf(uiText(UiMessages.modelsExtendedWindow()), uiText(UiMessages.modelsStandardWindow())), if (c.standardContext) 1 else 0, { edit(c.copy(standardContext = it == 1)) }, Modifier.fillMaxWidth(), enabled = { enabled })
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
            SheetHead(uiText(UiMessages.newSessionAndroidChooseProject()), uiText(UiMessages.commonCancel()), onClose)
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
    PageScaffold(uiText(UiMessages.newSessionAndroidChooseProject())) {
        FormColumn(Modifier.weight(1f).verticalScrollFaded()) {
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
fun DirPickerBody(state: RemoteState, title: String = uiText(UiMessages.projectsNewChooseDirectory()), onBack: (() -> Unit)? = null, onChosen: (machine: String, path: String) -> Unit) {
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
            MachineSegmented(
                listed.map { machineLabel(state, it.id) }, listed.map { it.online }, listed.indexOfFirst { it.id == machine },
                { i -> val m = listed[i]; if (machine != m.id) { machine = m.id; path = ""; dirs = null } },
                Modifier.padding(bottom = 10.dp),
            )
        }
        val d = dirs
        when {
            machine.isEmpty() -> Notice(uiText(UiMessages.projectsAndroidNoOnline()), tone = NoticeTone.WARN)
            error.isNotEmpty() -> Notice(uiText(UiMessages.projectsDirectoryFailed(reason = error)), tone = NoticeTone.ERROR, action = uiText(UiMessages.commonRetry()), onAction = { attempt++ })
            d == null -> Text(uiText(UiMessages.projectsReadingDirectory()), style = T.s13, color = p.ink2, modifier = Modifier.padding(vertical = 12.dp))
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
                if (d.dirsList.isEmpty()) Text(uiText(UiMessages.projectsNoSubdirectories()), style = T.s13, color = p.ink2, modifier = Modifier.padding(vertical = 8.dp))
                // The folder before stays, dimmed, until the next one is listed: no blank in between.
                val listState = remember(d.path) { androidx.compose.foundation.lazy.LazyListState() }
                Box(Modifier.weight(1f).fillMaxWidth().alpha(if (loading) .45f else 1f)) {
                    if (d.parent.isNotEmpty() || d.dirsList.isNotEmpty()) LazyColumn(
                        Modifier.scrollEdgeFades(listState).clip(Radii.R20).background(if (LocalOnSoft.current) p.raised else p.gbg),
                        state = listState,
                    ) {
                        if (d.parent.isNotEmpty()) item(key = "..") {
                            ListItem(uiText(UiMessages.projectsParentDirectory()), last = d.dirsList.isEmpty(), onClick = if (loading) null else ({ path = d.parent }), icon = Lucide.ChevronLeft)
                        }
                        itemsIndexed(d.dirsList, key = { _, name -> name }) { i, name ->
                            ListItem(name, last = i == d.dirsList.size - 1, onClick = if (loading) null else ({ path = d.path.trimEnd('/') + "/" + name }), trailing = { Arrow(p.ink2, T.s15) })
                        }
                    }
                }
                PillButton(
                    uiText(UiMessages.projectsUseDirectory(name = if (parts == listOf("/")) "/" else uiText(listProjectName(d.path)))), { onChosen(machine, d.path) },
                    Modifier.fillMaxWidth().padding(top = 10.dp), ButtonKind.INK, enabled = !loading,
                )
            }
        }
    }
}

/** 新项目 · 选目录 as a page (from the empty home page). */
@Composable
fun NewProjectScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    PageScaffold(uiText(UiMessages.projectsNewChooseDirectory())) {
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
    var settingsOpen by remember { mutableStateOf<String?>(null) }
    Column(Modifier.fillMaxSize().background(p.paper)) {
        NewSessionBox(
            state, initial, uiText(listProjectName(path)), focus = true, onMachine = { settingsOpen = "machine" }, settled = start?.settled != false,
        )
    }
    val open = settingsOpen
    if (open != null) NewSettingsSheet(state, path, uiText(listProjectName(path)), selected.machineId, selected.agent, open, onDone = { op ->
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
