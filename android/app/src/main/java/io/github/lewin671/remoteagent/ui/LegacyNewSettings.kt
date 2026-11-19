package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.*

/*
 * Legacy project/settings helpers still consumed by non-M1 screens (Session, Schedules, Settings)
 * until M2/M3 migrate those screens onto the shared Core client.
 */

val newSessionModes = listOf(Mode.ASK, Mode.AUTO, Mode.FULL)

data class NewConfig(
    val machineId: String,
    val cwd: String,
    val agent: Agent = DEFAULT_AGENT,
    val model: String = "",
    val effort: String = "",
    val mode: Mode? = null,
    val standardContext: Boolean = false,
)

data class ProjectStart(val config: NewConfig, val settled: Boolean)

data class NewSettingsResult(
    val path: String,
    val machineId: String,
    val moved: Boolean = false,
    val agent: Agent = Agent.AGENT_UNSPECIFIED,
    val sets: List<AgentSettings> = emptyList(),
)

data class DefaultMode(
    val mode: Mode?,
    val known: Boolean,
    val varies: Boolean,
)

fun prefsBlocked(state: RemoteState): UiMessage? = when {
    state.conn != ConnState.ONLINE -> UiMessages.projectsConnectToEdit()
    state.prefs == null -> UiMessages.projectsLoading()
    else -> null
}

fun savedSettings(state: RemoteState, key: String): ProjectSettings? =
    state.prefs?.prefs?.settingsList?.firstOrNull { it.path == key }

private fun projectSessions(state: RemoteState, key: String) =
    state.sessions.values.filter { it.info?.taskOwned != true && projectKey(it) == key }.sortedWith(sessionOrder)

fun projectMachines(state: RemoteState, key: String): List<String> =
    project(state, key, copy = ::sessionText)?.machineIds.orEmpty().ifEmpty { state.machines.keys.toList() }

private fun pastFetched(state: RemoteState, s: SessionView): Boolean =
    state.sessionsNext?.let { afterPlace(s.listedEndedMs, s.id, it) } == true

private class Inheritance(state: RemoteState, key: String) {
    private val all = projectSessions(state, key)
    private val sure = all.filter { !pastFetched(state, it) }
    private val unfetched = state.moreSessions && (project(state, key, copy = ::sessionText)?.ended ?: 0) > all.count { exited(it) }
    var unsettled = false
        private set

    fun first(match: (SessionView) -> Boolean = { true }): SessionView? {
        sure.firstOrNull(match)?.let { return it }
        if (unfetched) unsettled = true
        return all.firstOrNull(match)
    }
}

fun wantedAgent(state: RemoteState, key: String, pick: Agent? = null, saved: ProjectSettings? = savedSettings(state, key)): Agent? =
    wantedAgent(Inheritance(state, key), pick, saved)

private fun wantedAgent(from: Inheritance, pick: Agent?, saved: ProjectSettings?): Agent? =
    pick ?: saved?.agent?.takeIf { it in settingsAgents } ?: from.first()?.agent()

fun agentSet(state: RemoteState, key: String, agent: Agent, saved: ProjectSettings? = savedSettings(state, key)): AgentSettings =
    agentSet(Inheritance(state, key), agent, saved)

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

fun projectStart(state: RemoteState, key: String, pick: Agent? = null, machine: String? = null, saved: ProjectSettings? = savedSettings(state, key)): ProjectStart? {
    val from = Inheritance(state, key)
    val machineId = machine ?: saved?.machineId?.takeIf { it.isNotEmpty() } ?: from.first()?.machineId
        ?: project(state, key, copy = ::sessionText)?.machineIds?.firstOrNull() ?: return null
    val agents = machineAgents(state.machines[machineId])
    val agent = wantedAgent(from, pick, saved)?.takeIf { it in agents } ?: agents.first()
    return ProjectStart(agentConfig(machineId, key, agentSet(from, agent, saved)), !from.unsettled)
}

fun projectConfig(state: RemoteState, key: String, pick: Agent? = null, machine: String? = null, saved: ProjectSettings? = savedSettings(state, key)): NewConfig? =
    projectStart(state, key, pick, machine, saved)?.config

fun agentConfig(machineId: String, cwd: String, s: AgentSettings): NewConfig =
    NewConfig(machineId, cwd, s.agent, s.model, s.effort, modeOf(s.mode)?.takeIf { it in newSessionModes }, s.standardContext)

fun NewConfig.agentSettings(): AgentSettings = AgentSettings.newBuilder().setAgent(agent).setModel(model).setEffort(effort)
    .setMode(mode?.proto ?: ApprovalMode.APPROVAL_MODE_UNSPECIFIED).setStandardContext(agentInfo(agent).standardContextOption && standardContext).build()

fun switchMachine(c: NewConfig, machineId: String, agents: List<Agent>): NewConfig =
    c.copy(machineId = machineId, model = "", effort = "", mode = null, agent = if (c.agent in agents) c.agent else agents.first())

fun defaultModeOf(m: MachineView?, agent: Agent): DefaultMode {
    val info = m?.meta?.agentsList?.firstOrNull { agentInfo(it.agent).agent == agentInfo(agent).agent }
    val known = info != null && info.defaultModeSource != io.github.lewin671.remoteagent.proto.DefaultModeSource.DEFAULT_MODE_SOURCE_UNSPECIFIED &&
        info.defaultModeSource != io.github.lewin671.remoteagent.proto.DefaultModeSource.UNRECOGNIZED
    return DefaultMode(if (known) modeOf(info!!.defaultMode) else null, known, info?.defaultModeVaries == true)
}

fun effectiveModeText(chosen: Mode?, default: DefaultMode, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String =
    chosen?.text?.let(copy) ?: if (!default.known) copy(UiMessages.newSessionFollowCli()) else default.mode?.text?.let(copy) ?: copy(UiMessages.commonAndroidCliSOwnMode())

fun modeLabel(chosen: Mode?, d: DefaultMode, agent: Agent, includeDefault: Boolean = true, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String = when {
    chosen != null -> copy(chosen.text)
    !d.known -> copy(UiMessages.newSessionFollowCli())
    d.mode != null -> if (includeDefault) copy(UiMessages.modesDefaultNamed(copy(d.mode.text))) else copy(d.mode.text)
    else -> if (includeDefault) copy(UiMessages.modesDefaultAgent((agentName(agent, copy = copy)).toString())) else copy(UiMessages.modesAgentOwn((agentName(agent, copy = copy)).toString()))
} + if (chosen == null && d.varies) copy(UiMessages.commonAndroidProjectSettingsMayDiffer()) else ""

fun machineSubtitle(installed: List<Agent>, separator: String, missing: Agent? = null, fallback: Agent? = null): UiMessage {
    val names = installed.joinToString(separator) { agentName(it, copy = ::sessionText) }
    return if (missing != null && fallback != null) {
        UiMessages.newSessionInstalledFallback(agent = agentName(fallback, copy = ::sessionText), agents = names, missing = agentName(missing, copy = ::sessionText))
    } else {
        UiMessages.newSessionInstalled(agents = names)
    }
}

fun projectModel(c: NewConfig, models: ListModelsResponse, settings: DaemonSettings? = null): io.github.lewin671.remoteagent.proto.ModelInfo? {
    val defaultModel = defaultsFor(settings, c.agent).model
    val id = c.model.ifEmpty { defaultModel.ifEmpty { models.defaultModel } }
    return io.github.lewin671.remoteagent.core.findModel(models.modelsList, id) { it.slug }
}

@Composable
private fun SetRow(label: String, value: String, onClick: (() -> Unit)?, dot: Color? = null, valueColor: Color? = null) {
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
    val copy = uiText()
    val p = pal
    var allModels by remember(c.machineId, c.agent) { mutableStateOf(false) }
    val shownModel = models?.let { projectModel(c, it, settings) }
    val ms = models
    when {
        ms == null && modelsError.isNotEmpty() -> Notice(uiText(UiMessages.modelsAndroidLoadFailedDefault(agent = agentName(c.agent, copy = copy), reason = modelsError)), tone = NoticeTone.WARN)
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
                        onClick = { edit(c.copy(model = m.slug, effort = m.defaultEffort, mode = c.mode?.takeUnless { m.autoUnavailable && it == Mode.AUTO })) },
                    )
                }
            }
            if (!few) AllModels { allModels = true }
            projectModel(c, ms, settings)?.takeIf { it.effortsList.isNotEmpty() }?.let { m ->
                val defaultEffort = if (c.model.isEmpty()) defaultsFor(settings, c.agent).effort.ifEmpty { m.defaultEffort } else m.defaultEffort
                FieldLabel(uiText(UiMessages.modelsEffort()))
                ChoiceGrid(m.effortsList, c.effort.ifEmpty { defaultEffort }, enabled) { edit(c.copy(effort = it)) }
            }
        }
    }
    if (agentInfo(c.agent).standardContextOption && !scheduling) {
        FieldLabel(uiText(UiMessages.modelsContextWindow()))
        if (shownModel != null && shownModel.contextWindow in 1..io.github.lewin671.remoteagent.core.STANDARD_WINDOW) Hint(uiText(UiMessages.modelsStandardOnly()))
        else Segmented(listOf(uiText(UiMessages.modelsExtendedWindow()), uiText(UiMessages.modelsStandardWindow())), if (c.standardContext) 1 else 0, { edit(c.copy(standardContext = it == 1)) }, Modifier.fillMaxWidth(), enabled = { enabled })
    }
}

@Composable
fun NewSettingsSheet(state: RemoteState, cwd: String, name: String, machineId: String, agent: Agent, open: String = "", onDone: (NewSettingsResult?) -> Unit, onClose: () -> Unit, initial: NewConfig? = null, scheduling: Boolean = false, lockMachine: Boolean = false, chooseAgent: Boolean = false) {
    val copy = uiText()
    val p = pal
    val remote = LocalRemote.current
    val nav = LocalNav.current
    var tab by remember { mutableStateOf(agent) }
    var removing by remember { mutableStateOf(false) }
    var machine by remember { mutableStateOf(machineId) }
    val edited = remember { mutableStateMapOf<Agent, NewConfig>().apply { if (initial != null) put(initial.agent, initial) } }
    val manyMachines = state.machines.size > 1
    val start = open.ifEmpty { if (!scheduling && !manyMachines) "model" else "" }
    var page by remember { mutableStateOf(start) }
    val back: (() -> Unit)? = if (start.isEmpty()) ({ page = "" }) else null
    val moved = machine != machineId
    val agents = machineAgents(state.machines[machine])
    val cur = (if (chooseAgent) tab else agent).let { if (it in agents) it else agents.first() }
    val c = edited[cur] ?: if (moved) NewConfig(machine, cwd, cur) else agentConfig(machine, cwd, agentSet(state, cwd, cur))
    val unread = !moved && edited[cur] == null && !agentSetSettled(state, cwd, cur)
    FetchEarlier(state, page = unread)
    fun edit(next: NewConfig) { edited[cur] = next }
    fun done() = onDone(
        if (lockMachine || scheduling) NewSettingsResult(cwd, machine, moved = moved, agent = cur, sets = listOf(c.agentSettings()))
        else if (!moved && edited.isEmpty()) null else NewSettingsResult(cwd, machine, moved = moved, agent = cur, sets = edited.values.map { it.agentSettings() }),
    )
    var models by remember { mutableStateOf<ListModelsResponse?>(null) }
    var modelsError by remember { mutableStateOf("") }
    LaunchedEffect(c.machineId, c.agent, page == "model") {
        models = null; modelsError = ""
        try { models = remote.listModels(c.machineId, c.agent, fresh = true) } catch (e: Exception) { modelsError = displayError(e) }
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
    val settingsDirty = moved || edited.isNotEmpty()
    Sheet(onClose, dirty = settingsDirty) {
        when (page) {
            "" -> {
                SheetHead(if (lockMachine) copy(UiMessages.newSessionTaskSettings()) else uiText(UiMessages.projectsAndroidSettings()), if (blockedPrefs == null) uiText(UiMessages.commonDone()) else null, { done() })
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
                    { SetRow(uiText(UiMessages.commonMachine()), machineLabel(state, machine), if (lockMachine) null else ({ page = "machine" }), dot = if (state.machines[machine]?.pathState?.reachable == true) p.green else p.faint) }
                val machineRows = if (scheduling || manyMachines) listOf(machineRow) else emptyList()
                val rows = listOf<@Composable () -> Unit>(
                    { SetRow(uiText(UiMessages.modelsTitle()), listOf(shownModel?.displayName?.ifEmpty { null } ?: c.model, c.effort).filter { it.isNotEmpty() }.joinToString(" · ").ifEmpty { uiText(UiMessages.newSessionDefaultModel()) }, { page = "model" }) },
                    *(if (scheduling) arrayOf<@Composable () -> Unit>({
                        SetRow(copy(UiMessages.newSessionPermissions()), effectiveModeText(c.mode, defaults, copy = copy), { page = "mode" }, valueColor = if (c.mode == Mode.FULL || (c.mode == null && (defaults.mode == Mode.FULL || !defaults.known))) p.red else null)
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
                    else Text(uiText(UiMessages.newSessionAndroidReadingCli(agent = agentName(cur, copy = uiText()))), style = T.s13, color = p.ink2, modifier = Modifier.fillMaxWidth().padding(14.dp))
                }
                if (unread && agents.size > 1) {
                    if (machineRows.isNotEmpty()) card(machineRows)
                    if (chooseAgent) Segmented(agents.map { agentName(it, copy = uiText()) }, agents.indexOf(cur), { tab = agents[it] }, Modifier.padding(top = 6.dp, bottom = 10.dp).fillMaxWidth()) else FieldLabel(agentName(cur, copy = uiText()))
                    card(listOf(reading))
                } else if (unread) {
                    card(machineRows + listOf(reading))
                } else if (agents.size > 1) {
                    if (machineRows.isNotEmpty()) card(machineRows)
                    if (chooseAgent) Segmented(agents.map { agentName(it, copy = uiText()) }, agents.indexOf(cur), { tab = agents[it] }, Modifier.padding(top = 6.dp, bottom = 10.dp).fillMaxWidth()) else FieldLabel(agentName(cur, copy = uiText()))
                    card(rows)
                    if (scheduling) Hint(if (lockMachine) copy(UiMessages.newSessionOnlyChangesThisTaskExistingConversationsKeepTheirSettings()) else copy(UiMessages.commonAndroidEachCliSavesItsOwnSettingsOnlyFutureSessionsAre()))
                } else {
                    card(machineRows + rows)
                    if (scheduling) Hint(if (lockMachine) copy(UiMessages.newSessionOnlyChangesThisTaskExistingConversationsKeepTheirSettings()) else copy(UiMessages.newSessionOnlyAffectsFutureSessionsActiveSessionsStayUnchangedSynced()))
                }
                if (scheduling && !lockMachine) {
                    TextAction(if (isPinned(state, cwd)) copy(UiMessages.projectsUnpin()) else copy(UiMessages.projectsPin()), {
                        remote.coreHost.toggleProjectPin(cwd)
                    }, Modifier.padding(horizontal = 12.dp), enabled = blockedPrefs == null)
                    TextAction(copy(UiMessages.projectsRemoveAction()), { removing = true }, Modifier.padding(horizontal = 12.dp), enabled = blockedPrefs == null, color = p.red)
                    if (removing) RemoveProjectConfirm(cwd, onDismiss = { removing = false }) {
                        remote.coreHost.toggleProjectHide(cwd)
                        onClose()
                        nav.home()
                    }
                }
            }
            "machine" -> {
                SheetHead(uiText(UiMessages.commonMachine()), if (blockedPrefs == null) uiText(UiMessages.commonDone()) else null, { done() }, onBack = back)
                SheetList {
                    used.forEachIndexed { i, id ->
                        val m = state.machines[id]
                        val online = m?.pathState?.reachable == true
                        val magents = machineAgents(m)
                        val has = agent in magents
                        val fallback = if (has) null else switchMachine(c, id, magents).agent
                        val sub = when {
                            !online -> uiText(UiMessages.newSessionAndroidOfflineNoStart())
                            !has -> uiText(machineSubtitle(magents, uiText(UiMessages.commonListSeparator()), missing = agent, fallback = fallback))
                            else -> uiText(machineSubtitle(magents, uiText(UiMessages.commonListSeparator())))
                        }
                        ListItem(
                            machineLabel(state, id), sub, checked = machine == id, enabled = online, last = i == used.size - 1,
                            onClick = {
                                if (back == null && !scheduling) onDone(if (id == machineId) null else NewSettingsResult(cwd, id, moved = true))
                                else { if (id != machine) { machine = id; edited.clear() }; page = "" }
                            },
                            trailing = { StatusMark(if (online) MarkKind.ONLINE else MarkKind.OFF) },
                        )
                    }
                }
                Hint(if (scheduling) copy(UiMessages.commonAndroidAfterSwitchingMachinesEachCliStartsWithThatMachineS()) else uiText(UiMessages.newSessionAndroidChangeMachineHint()))
            }
            "model" -> {
                SheetHead(uiText(UiMessages.modelsTitle()), if (blockedPrefs == null) uiText(UiMessages.commonDone()) else null, { done() }, onBack = back)
                ModelSettingsBody(c, models, modelsError, settings, machineLabel(state, c.machineId), scheduling) { edit(it) }
            }
            "mode" -> {
                SheetHead(copy(UiMessages.modesTitle()), if (blockedPrefs == null) copy(UiMessages.commonDone()) else null, { done() }, onBack = { page = "" })
                val chosen = models?.let { projectModel(c, it, settings) }
                SheetList {
                    ListItem(copy(UiMessages.modelsDefault()), copy(UiMessages.newSessionFollowSConfiguration((agentName(c.agent, copy = uiText())).toString())), checked = c.mode == null, onClick = { edit(c.copy(mode = null)); page = "" })
                    newSessionModes.forEachIndexed { i, m ->
                        val off = m == Mode.AUTO && chosen?.autoUnavailable == true
                        ListItem(copy(m.text), if (off) copy(UiMessages.modesModelUnsupported()) else modeHelp.getValue(m).replace("CLI", agentName(c.agent, copy = uiText())), checked = c.mode == m, enabled = !off, last = i == newSessionModes.size - 1, onClick = { edit(c.copy(mode = m)); page = "" })
                    }
                }
                Hint(copy(UiMessages.commonAndroidNewFullAccessSessionsStartDirectlyWithTheseSettings()))
            }
        }
    }
}
