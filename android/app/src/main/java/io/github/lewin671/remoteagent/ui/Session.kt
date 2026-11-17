package io.github.lewin671.remoteagent.ui

import android.content.Context
import android.content.res.Configuration
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.collectIsDraggedAsState
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.ime
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.layout.wrapContentWidth
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.CORE_SESSION_WINDOW_LIMIT
import io.github.lewin671.remoteagent.core.DEFAULT_AGENT
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.filesSupported
import io.github.lewin671.remoteagent.core.listWhen
import io.github.lewin671.remoteagent.core.maxFiles
import io.github.lewin671.remoteagent.core.newRequestId
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.proto.SessionCommandStatus
import io.github.lewin671.remoteagent.core.proto.SessionDisplayStatus
import io.github.lewin671.remoteagent.core.proto.SessionOutgoingState
import io.github.lewin671.remoteagent.core.proto.SessionOutgoingView
import io.github.lewin671.remoteagent.core.proto.SessionPendingInputView
import io.github.lewin671.remoteagent.core.proto.SessionViewModel
import io.github.lewin671.remoteagent.core.proto.SessionWindowRequest
import io.github.lewin671.remoteagent.core.sendModeFor
import io.github.lewin671.remoteagent.core.shortPath
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.CancelInputRequest
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.ForkSessionRequest
import io.github.lewin671.remoteagent.proto.InterruptRequest
import io.github.lewin671.remoteagent.proto.ResumeInputRequest
import io.github.lewin671.remoteagent.proto.ResumeSessionRequest
import io.github.lewin671.remoteagent.proto.SendMode
import io.github.lewin671.remoteagent.proto.SendRequest
import io.github.lewin671.remoteagent.proto.StopSessionRequest
import io.github.lewin671.remoteagent.term.TerminalPane
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/** Sessions whose terminal another page asked to show. */
object OpenTerminal {
    val requested = mutableStateMapOf<String, Boolean>()
}

@Composable
internal fun HistoryPartialNotice(
    sid: String,
    partial: Boolean,
    modifier: Modifier = Modifier,
    waitForNotice: suspend () -> Unit = { delay(1_000) },
) {
    var visible by remember(sid, partial) { mutableStateOf(false) }
    LaunchedEffect(sid, partial) {
        if (partial) {
            waitForNotice()
            visible = true
        }
    }
    if (partial && visible) Hint(uiText(UiMessages.sessionAndroidHistoryPartial()), modifier, inset = 0.dp)
}

@Composable
fun SessionScreen(sid: String) {
    val copy = uiText()
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val p = pal
    val state by remote.state.collectAsState()

    var windowLimit by rememberSaveable(sid) { mutableIntStateOf(CORE_SESSION_WINDOW_LIMIT) }
    val pendingJump = ScheduleJump.requested[sid] ?: 0L
    var revealSeq by remember(sid) { mutableLongStateOf(pendingJump) }
    LaunchedEffect(pendingJump) {
        if (pendingJump > 0L) revealSeq = pendingJump
    }

    val initialWindow = remember(sid) {
        SessionWindowRequest.newBuilder().setLimit(windowLimit).apply {
            if (revealSeq > 0L) this.revealSeq = revealSeq
        }.build()
    }
    LaunchedEffect(remote, sid) { remote.openSession(sid) }
    LaunchedEffect(remote, sid, windowLimit, revealSeq) {
        remote.updateSessionWindow(
            sid,
            SessionWindowRequest.newBuilder().setLimit(windowLimit).apply {
                if (revealSeq > 0L) this.revealSeq = revealSeq
            }.build(),
        )
    }

    val vm by remote.watchSession(sid, initialWindow).collectAsState(initial = SessionViewModel.getDefaultInstance())
    val header = vm.header

    LaunchedEffect(remote, sid, header.machineId) {
        remote.restoreComposerEvents.collect { eff ->
            if (eff.sessionId != sid) return@collect
            val draft = ImageDrafts.forSession(sid)
            if (eff.text.isNotEmpty()) {
                val cur = draft.text.value
                draft.text.value = if (cur.isBlank()) eff.text else cur.trimEnd() + "\n" + eff.text
            }
            if (Outbox.takeBack(sid, eff.requestId, draft)) {
                draft.images.forEach { draft.bindAbort(it, remote) }
                if (header.machineId.isNotEmpty()) draft.sendAllTo(remote, header.machineId)
            }
        }
    }

    LaunchedEffect(vm.composer.outgoingList, vm.composer.pendingInputsList) {
        val active = buildSet {
            vm.composer.outgoingList.forEach { add(it.requestId) }
            vm.composer.pendingInputsList.forEach { add(it.requestId) }
        }
        Outbox.pruneSettled(sid, active)
    }

    if (!header.found) {
        Column(
            Modifier.fillMaxSize().background(p.paper).windowInsetsPadding(WindowInsets.safeDrawing).padding(24.dp),
            verticalArrangement = Arrangement.Center,
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            if (header.missing || (header.sessionsListed && header.hasMissingText())) {
                Text(
                    uiText(header.missingText).ifEmpty { copy(UiMessages.sessionAndroidUnknown()) },
                    style = T.s17b,
                    color = p.ink,
                    textAlign = TextAlign.Center,
                )
                PillButton(uiText(UiMessages.commonRetry()), { remote.openSession(sid) }, Modifier.padding(top = 16.dp), ButtonKind.INK)
                TextLink(uiText(UiMessages.commonBack()), nav.back)
            } else {
                Spinner(p.ink2, size = 24.dp)
                Text(
                    if (header.hasStatusText()) uiText(header.statusText) else copy(UiMessages.projectsReadingDirectory()),
                    style = T.s15,
                    color = p.ink2,
                    modifier = Modifier.padding(top = 12.dp),
                )
            }
        }
        return
    }

    val context = LocalContext.current
    val viewPrefs = remember(context) { context.getSharedPreferences("session-view", Context.MODE_PRIVATE) }
    var tab by rememberSaveable { mutableIntStateOf(viewPrefs.getInt("tab", 0)) }
    val wantTerminal = OpenTerminal.requested[sid] == true
    LaunchedEffect(wantTerminal) {
        if (wantTerminal) {
            tab = 1
            OpenTerminal.requested.remove(sid)
        }
    }

    val hasTerminal = header.capabilities.terminalAvailable
    val wide = wideWindow()
    val term = tab == 1 && hasTerminal
    val both = tab == 2 && wide && hasTerminal
    BackHandler { if (term) tab = 0 else nav.back() }
    val landscape = LocalConfiguration.current.orientation == Configuration.ORIENTATION_LANDSCAPE
    val compactTerminal = term && landscape && WindowInsets.ime.getBottom(LocalDensity.current) > 0
    LightSystemBarIcons(term && !p.dark)

    Column(Modifier.fillMaxSize().background(if (term) TermColors.bg else p.paper).windowInsetsPadding(WindowInsets.safeDrawing)) {
        if (!compactTerminal) SessionTopBar(sid, state, vm, dark = term)
        if (!compactTerminal) Segmented(
            if (wide) listOf(copy(UiMessages.sessionChat()), copy(UiMessages.sessionTerminal()), copy(UiMessages.sessionSplit()))
            else listOf(copy(UiMessages.sessionChat()), copy(UiMessages.sessionTerminal())),
            if (term) 1 else if (both) 2 else 0,
            {
                tab = it
                viewPrefs.edit().putInt("tab", it).apply()
            },
            Modifier.align(Alignment.CenterHorizontally).padding(top = 8.dp).widthIn(min = 188.dp).width(IntrinsicSize.Max),
            enabled = { it == 0 || hasTerminal },
            dot = if (header.status == SessionDisplayStatus.SESSION_DISPLAY_STATUS_POPUP) 1 else -1,
            dark = term,
        )
        ConnStrip(state, Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp))
        if (!term) SessionScheduleStrip(sid, Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp))
        if (term) {
            TerminalPane(
                remote,
                sid,
                sessionRunning = true,
                modifier = Modifier.weight(1f).fillMaxWidth(),
                compact = compactTerminal,
            )
        } else {
            Row(Modifier.weight(1f)) {
                Column(Modifier.weight(1f)) {
                    val note = Modifier.padding(horizontal = 20.dp)
                    val tl = vm.timeline
                    HistoryPartialNotice(sid, tl.historyPartial, note)
                    if (tl.hasHistoryNotice()) {
                        Hint(uiText(tl.historyNotice), note, inset = 0.dp)
                    }
                    if (tl.hasFetchError()) {
                        Hint(uiText(tl.fetchError), note, inset = 0.dp)
                    }
                    ChatPane(
                        sid = sid,
                        state = state,
                        vm = vm,
                        modifier = Modifier.weight(1f),
                        onTerminal = { tab = 1 },
                        onLoadEarlierWindow = {
                            if (tl.hasGap) remote.loadEarlierSessionHistory(sid)
                            if (tl.hasEarlier) windowLimit += CORE_SESSION_WINDOW_LIMIT
                        },
                        onScheduleRevealed = {
                            if (ScheduleJump.requested[sid] != null) ScheduleJump.requested.remove(sid)
                        },
                    )
                }
                if (both) TerminalPane(
                    remote,
                    sid,
                    sessionRunning = true,
                    modifier = Modifier.weight(1f).fillMaxHeight().padding(end = 8.dp, bottom = 8.dp).clip(Radii.R16).background(TermColors.bg),
                )
            }
        }
    }
}

@Composable
private fun LightSystemBarIcons(on: Boolean) {
    val view = LocalView.current
    val dark = pal.dark
    androidx.compose.runtime.DisposableEffect(on, dark) {
        val window = generateSequence(view.context) { (it as? android.content.ContextWrapper)?.baseContext }
            .filterIsInstance<android.app.Activity>().firstOrNull()?.window
        val bars = window?.let { androidx.core.view.WindowCompat.getInsetsController(it, view) }
        bars?.isAppearanceLightStatusBars = !on && !dark
        bars?.isAppearanceLightNavigationBars = !on && !dark
        onDispose {
            bars?.isAppearanceLightStatusBars = !dark
            bars?.isAppearanceLightNavigationBars = !dark
        }
    }
}

private fun displayMarkKind(status: SessionDisplayStatus): MarkKind? = when (status) {
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_STARTING,
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_RUNNING -> MarkKind.RUNNING
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_APPROVAL,
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_QUESTION,
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_PLAN,
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_POPUP -> MarkKind.WAITING
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_IDLE -> MarkKind.IDLE
    else -> null
}

@Composable
private fun SessionTopBar(
    sid: String,
    state: RemoteState,
    vm: SessionViewModel,
    dark: Boolean,
) {
    val copy = uiText()
    val p = pal
    val nav = LocalNav.current
    val remote = LocalRemote.current
    val header = vm.header
    val actions = vm.actions
    val caps = header.capabilities

    var menu by remember { mutableStateOf(false) }
    var confirmStop by remember { mutableStateOf(false) }
    var rename by remember { mutableStateOf(false) }
    var sheet by remember { mutableStateOf(0) } // 1 mode, 2 model, 3 project settings

    val forkSlot = actions.fork
    val forking = forkSlot.busy || forkSlot.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING
    val forkRow = forkAction(
        blocked = if (actions.hasForkBlockedReason()) uiText(actions.forkBlockedReason).ifEmpty { null } else null,
        running = forking,
        error = if (forkSlot.hasErrorText()) uiText(forkSlot.errorText) else "",
        frozen = forkSlot.frozen,
        expired = forkSlot.expired,
        copy = copy,
    )
    val high = header.hasContext && header.highContextWarning
    val statusStr = uiText(header.statusText)
    val modeStr = if (!header.exited && header.hasApprovalModeLabel()) uiText(header.approvalModeLabel) else ""
    val subtitleText = if (modeStr.isEmpty()) statusStr else "$statusStr · $modeStr"
    val rich = if (!header.exited && header.approvalMode == ApprovalMode.APPROVAL_MODE_FULL_ACCESS && modeStr.isNotEmpty()) {
        buildAnnotatedString {
            append("$statusStr · ")
            pushStyle(SpanStyle(color = p.red))
            append(modeStr)
            pop()
        }
    } else null

    val taskOwned = header.isScheduled && header.scheduledTaskId.isNotEmpty()
    BackHandler(enabled = taskOwned && !dark) { nav.task(header.machineId, header.scheduledTaskId) }
    val back = { if (taskOwned) nav.task(header.machineId, header.scheduledTaskId) else nav.back() }

    Row(
        Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        GlassButton(Lucide.ChevronLeft, uiText(UiMessages.commonBack()), back, dark = dark)
        Column(Modifier.weight(1f), horizontalAlignment = Alignment.CenterHorizontally) {
            Text(uiText(header.title), style = T.s17b, color = if (dark) CardColors.text else p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Row(verticalAlignment = Alignment.CenterVertically) {
                val sub = if (dark) CardColors.text2 else p.ink2
                displayMarkKind(header.status)?.let { mk ->
                    StatusMark(mk, size = 7.dp, color = if (dark && mk != MarkKind.WAITING) CardColors.text2 else null)
                    Spacer(Modifier.width(5.dp))
                }
                if (rich != null) Text(rich, style = T.s12, color = sub, maxLines = 1, overflow = TextOverflow.Ellipsis)
                else Text(subtitleText, style = T.s12, color = sub, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
        GlassButton(Lucide.Ellipsis, copy(UiMessages.commonAndroidMore()), { menu = true }, dot = high, dark = dark)
    }

    if (menu) {
        val computer = if (header.hasComputerName()) uiText(header.computerName) else header.machineId
        val menuTitle = listOfNotNull(
            header.agentName.ifEmpty { null },
            if (header.hasProjectName()) uiText(header.projectName).ifEmpty { null } else null,
            computer.takeIf { it.isNotEmpty() && state.machines.size > 1 },
        ).joinToString(" · ")
        val modelLabel = header.modelLabel.ifEmpty { header.model }
        val effortStr = if (header.hasEffortLabel()) uiText(header.effortLabel) else header.effort
        val modelValue = modelLabel + if (effortStr.isNotEmpty()) " · $effortStr" else ""

        ActionSheet(
            info = {
                SheetInfo(
                    menuTitle,
                    shortPath(header.cwd),
                    mono = true,
                    lead = { AgentMarkLead(header.agent.takeIf { it != Agent.AGENT_UNSPECIFIED } ?: DEFAULT_AGENT, T.s17b, p.ink) },
                )
            },
            groups = listOf(
                buildList {
                    if (!header.exited && (actions.modeChoicesCount > 0 || actions.canSwitchMode)) {
                        add(
                            SheetAction(
                                copy(UiMessages.modesTitle()),
                                value = modeStr,
                                danger = header.approvalMode == ApprovalMode.APPROVAL_MODE_FULL_ACCESS,
                                enabled = !forking && actions.canSwitchMode,
                            ) { sheet = 1 },
                        )
                    }
                    if (!header.exited) {
                        add(
                            SheetAction(
                                if (modelLabel.isEmpty()) copy(UiMessages.sessionSettings()) else copy(UiMessages.modelsTitle()),
                                value = modelValue,
                                enabled = !forking,
                            ) { sheet = 2 },
                        )
                    }
                    if (!header.exited && header.hasContext) {
                        add(
                            SheetAction(
                                copy(UiMessages.sessionContext()),
                                value = copy(UiMessages.sessionContextUsed(percent = header.contextPercent)),
                                warn = high,
                                enabled = !forking,
                            ) { sheet = 2 },
                        )
                    }
                },
                buildList {
                    val cfg = header.projectKey.takeIf { it.isNotEmpty() }?.let { projectConfig(state, it) }
                    add(SheetAction(copy(UiMessages.projectsAndroidSettings()), enabled = !forking && cfg != null) { sheet = 3 })
                    if (actions.canRename) {
                        add(SheetAction(copy(UiMessages.sessionRename()), enabled = !forking) { rename = true })
                    }
                    if (caps.supportsFork) {
                        add(
                            SheetAction(
                                forkRow.text,
                                enabled = forkRow.enabled && (actions.canFork || forkRow.abandons),
                                busy = forkRow.busy,
                                keepOpen = true,
                                note = forkRow.note,
                                noteError = forkRow.noteError,
                            ) {
                                if (forkRow.abandons && forkSlot.canAbandon) {
                                    remote.abandonSessionCommand(sid, forkSlot.slotKey)
                                } else {
                                    remote.submitSessionCommand(
                                        sid,
                                        Command.newBuilder().setForkSession(ForkSessionRequest.newBuilder().setSessionId(sid)).build(),
                                        machineId = header.machineId,
                                    )
                                    menu = false
                                }
                            },
                        )
                    }
                    if (caps.supportsSchedule) {
                        add(SheetAction(copy(UiMessages.sessionScheduleSend()), enabled = !forking) { nav.newTask(sid) })
                    }
                    if (actions.canInterrupt) {
                        add(
                            SheetAction(copy(UiMessages.sessionInterruptTurn()), danger = true, enabled = !forking) {
                                remote.submitSessionCommand(
                                    sid,
                                    Command.newBuilder().setInterrupt(InterruptRequest.newBuilder().setSessionId(sid)).build(),
                                    machineId = header.machineId,
                                )
                            },
                        )
                    }
                },
                buildList {
                    if (actions.canEnd) {
                        add(SheetAction(copy(UiMessages.sessionEndAction()), danger = true, enabled = !forking) { confirmStop = true })
                    }
                },
            ),
            onDismiss = { menu = false },
        )
    }

    if (rename) RenameDialog(sid, vm, onClose = { rename = false })
    when (sheet) {
        1 -> ModeSheet(sid, vm, onClose = { sheet = 0 })
        2 -> ModelSheet(sid, vm, onClose = { sheet = 0 })
        3 -> header.projectKey.takeIf { it.isNotEmpty() }?.let { key ->
            projectConfig(state, key)?.let { initial ->
                val ag = header.agent.takeIf { it != Agent.AGENT_UNSPECIFIED } ?: DEFAULT_AGENT
                NewSettingsSheet(
                    state,
                    key,
                    if (header.hasProjectName()) uiText(header.projectName) else projectName(key, copy = copy),
                    initial.machineId,
                    ag,
                    onDone = { res ->
                        if (res != null) {
                            remote.coreHost.openNewSession(key)
                            remote.coreHost.selectNewSessionMachine(res.machineId)
                            res.sets.forEach { set ->
                                remote.coreHost.selectNewSessionAgent(set.agent)
                                remote.coreHost.selectNewSessionModel(set.model, set.effort, set.standardContext, set.mode)
                            }
                        }
                        sheet = 0
                    },
                    onClose = { sheet = 0 },
                )
            }
        }
    }

    val endSlot = actions.end
    if (endSlot.hasErrorText()) {
        Text(uiText(endSlot.errorText), Modifier.padding(horizontal = 20.dp), color = p.red, style = T.s12)
    }
    if (confirmStop) {
        Confirm(
            copy(UiMessages.sessionEndTitle()),
            if (actions.hasEndConfirmNote()) uiText(actions.endConfirmNote) else copy(UiMessages.sessionEnd()),
            copy(UiMessages.sessionEnd()),
            danger = true,
            onDismiss = { confirmStop = false },
        ) {
            confirmStop = false
            remote.submitSessionCommand(
                sid,
                Command.newBuilder().setStopSession(StopSessionRequest.newBuilder().setSessionId(sid)).build(),
                machineId = header.machineId,
            )
        }
    }
}

@Composable
internal fun ChatPane(
    sid: String,
    state: RemoteState,
    vm: SessionViewModel,
    modifier: Modifier,
    onTerminal: () -> Unit,
    onLoadEarlierWindow: () -> Unit,
    onScheduleRevealed: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val nav = LocalNav.current
    val remote = LocalRemote.current
    val header = vm.header
    val tl = vm.timeline
    val machineName = if (header.hasComputerName()) uiText(header.computerName) else machineLabel(state, header.machineId)
    val list = rememberLazyListState()
    val followState = remember { Follow() }
    val unseen = remember { NewMessages() }
    val entrances = remember { RowEntrances() }

    val items = tl.itemsList
    var prevIds by remember(sid) { mutableStateOf<List<String>>(emptyList()) }
    var prevFirstSeq by remember(sid) { mutableStateOf(0L) }
    LaunchedEffect(items, tl.windowFirstSeq) {
        val ids = items.map { it.id }
        val joined = prevFirstSeq > 0L && tl.windowFirstSeq > 0L && tl.windowFirstSeq < prevFirstSeq
        if (joined && prevIds.isNotEmpty()) {
            val n = rowsInFront(prevIds, ids)
            list.keepPlace(prevIds, ids, n)
            unseen.joined(n)
        }
        entrances.rows(items, joined)
        prevIds = ids
        if (tl.windowFirstSeq > 0L) prevFirstSeq = tl.windowFirstSeq
    }

    val pendingInputs = vm.composer.pendingInputsList
    var allPending by remember { mutableStateOf(false) }
    val visiblePending = if (pendingInputs.size > 3 && !allPending) pendingInputs.take(2) else pendingInputs
    val outgoing = vm.composer.outgoingList

    val scope = rememberCoroutineScope()
    val atBottom by remember { derivedStateOf { !list.canScrollForward } }
    val dragged by list.interactionSource.collectIsDraggedAsState()
    LaunchedEffect(dragged, atBottom) { followState.observe(dragged, atBottom) }
    val follow = followState.on
    val endIndex by rememberUpdatedState(items.size + visiblePending.size + outgoing.size + 4)
    val ime = WindowInsets.ime.getBottom(LocalDensity.current)

    LaunchedEffect(
        followState.sends,
        items.size,
        outgoing.size,
        visiblePending.size,
        ime,
        vm.dock.hasItem,
        vm.dock.kind,
        header.exited,
        header.computerOnline,
    ) {
        if (follow && items.isNotEmpty()) list.scrollToItem(endIndex)
        if (follow) unseen.saw(items.size)
        if (follow && items.isNotEmpty()) list.pinBottom({ endIndex }, { followState.on }, { list.moved() })
    }
    LaunchedEffect(list) {
        snapshotFlow { list.layoutInfo }.collect { layout ->
            if (followState.on && !list.isScrollInProgress && list.canScrollForward && layout.totalItemsCount > 0) {
                list.requestScrollToItem(layout.totalItemsCount - 1)
            }
        }
    }
    LaunchedEffect(atBottom) { if (atBottom) unseen.saw(items.size) }

    val anchorId = tl.anchorItemId
    LaunchedEffect(anchorId, items.size) {
        if (anchorId.isNotEmpty()) {
            val idx = items.indexOfFirst { it.id == anchorId }
            if (idx >= 0) {
                followState.observe(userDragging = true, atBottom = false)
                val offset = if (tl.hasEarlier || tl.canLoadEarlier || tl.backfilling) 1 else 0
                list.scrollToItem(idx + offset)
                onScheduleRevealed()
            }
        }
    }

    var shownHeight by remember { mutableIntStateOf(0) }
    val density = LocalDensity.current
    val dockMax = with(density) { maxOf(240.dp, (shownHeight * 0.6f).toDp()) }

    entrances.outgoing(outgoing.map { "out" + it.requestId } + visiblePending.map { "pending-${it.requestId}" })

    Column(modifier.clipToBounds().onSizeChanged { shownHeight = it.height }) {
        SharedFileHost(
            sessionId = sid,
            machineName = machineName,
            machineOnline = header.computerOnline,
            sessionExited = header.exited,
            modifier = Modifier.weight(1f),
        ) {
            Box(Modifier.fillMaxSize()) {
                val chatCtx = ChatContext(
                    sid = sid,
                    agent = header.agent.takeIf { it != Agent.AGENT_UNSPECIFIED } ?: DEFAULT_AGENT,
                    agentName = header.agentName,
                    onImage = { seq, i -> nav.image(sid, seq, i) },
                    onSession = { nav.session(it) },
                    machineName = machineName,
                    machineOnline = header.computerOnline,
                    sessionExited = header.exited,
                )
                CompositionLocalProvider(LocalChat provides chatCtx) {
                    LazyColumn(
                        state = list,
                        modifier = Modifier.fillMaxSize(),
                        contentPadding = PaddingValues(top = 12.dp),
                    ) {
                        if (tl.hasEarlier || tl.canLoadEarlier || tl.backfilling) {
                            item(key = "load-earlier") {
                                Box(Modifier.fillMaxWidth().padding(vertical = 8.dp), contentAlignment = Alignment.Center) {
                                    if (tl.backfilling) {
                                        Text(copy(UiMessages.sessionLoadingEarlier()), style = T.s13, color = p.ink2)
                                    } else {
                                        TextAction(
                                            copy(UiMessages.scheduleEarlier()),
                                            onLoadEarlierWindow,
                                            style = T.s13,
                                            alignment = Alignment.Center,
                                        )
                                    }
                                }
                            }
                        }
                        if (tl.loading && items.isEmpty()) item(key = "skeleton") { Skeleton() }
                        items(items, key = { it.id }) { item ->
                            val kind = remember(item.id) { entrances.take(item.id) }
                            Box(Modifier.entrance(kind)) {
                                TimelineItemView(
                                    item = item,
                                    settling = kind == Entrance.SETTLE,
                                    onRestoreInput = { _, text ->
                                        val draft = ImageDrafts.forSession(sid)
                                        val cur = draft.text.value
                                        draft.text.value = if (cur.isBlank()) text else cur.trimEnd() + "\n" + text
                                    },
                                    onTerminalPopup = onTerminal,
                                )
                            }
                        }
                        if (tl.hasError()) {
                            item(key = "tail-error") {
                                Notice(
                                    copy(UiMessages.sessionAndroidHistoryTailFailed(reason = uiText(tl.error))),
                                    Modifier.padding(horizontal = ChatSide, vertical = 8.dp),
                                    NoticeTone.ERROR,
                                    action = uiText(UiMessages.commonRetry()),
                                    onAction = { remote.loadEarlierSessionHistory(sid) },
                                )
                            }
                        }
                        items(visiblePending, key = { "pending-${it.requestId}" }) { pending ->
                            Box(Modifier.entrance(remember { entrances.take("pending-${pending.requestId}") })) {
                                PendingBubble(sid, header.machineId, header.computerOnline && !header.computerUnlinked, pending)
                            }
                        }
                        if (pendingInputs.size > 3 && !allPending) {
                            item(key = "pending-more") {
                                TextAction(copy(UiMessages.queueMore(count = pendingInputs.size - 2)), { allPending = true }, Modifier.padding(horizontal = ChatSide), style = T.s13)
                            }
                        }
                        items(outgoing, key = { "out" + it.requestId }) { o ->
                            Box(Modifier.entrance(remember { entrances.take("out" + o.requestId) })) {
                                OutgoingView(sid, header.machineId, o)
                            }
                        }
                        item(key = "end-spacer") { Spacer(Modifier.height(8.dp)) }
                    }
                }
                Box(Modifier.fillMaxWidth().height(12.dp).background(Brush.verticalGradient(listOf(p.paper, p.paper.copy(alpha = 0f)))))
                val fresh = unseen.count(items.size)
                var shownFresh by remember { mutableIntStateOf(0) }
                if (fresh > 0) shownFresh = fresh
                Reveal(
                    visible = !follow && !atBottom && fresh > 0,
                    modifier = Modifier.align(Alignment.BottomCenter).padding(bottom = 8.dp),
                    enter = Motion.appear(Motion.BASE, riseDp = Motion.BUBBLE_RISE_DP),
                    exit = Motion.vanish(Motion.BASE, shrink = false),
                ) {
                    GlassCapsule({ scope.launch { list.animateScrollToItem((list.layoutInfo.totalItemsCount - 1).coerceAtLeast(0)) } }) {
                        Spacer(Modifier.width(6.dp))
                        IconText(Lucide.ArrowDown, copy(UiMessages.sessionNewMessages(count = shownFresh)), p.ink, T.s13)
                    }
                }
            }
        }
        CompositionLocalProvider(LocalDockMax provides dockMax) {
            if (header.isScheduled && header.scheduledTaskId.isNotEmpty()) {
                Hint(copy(UiMessages.sessionReplyMoves()), Modifier.padding(horizontal = ChatSide), inset = 0.dp)
            }
            BottomArea(
                sid = sid,
                state = state,
                vm = vm,
                onTerminal = onTerminal,
                onPlan = { nav.plan(sid) },
                onQuestion = { qid -> nav.question(sid, qid) },
                onApproval = { aid -> nav.approval(sid, aid) },
                onSent = followState::sent,
            )
        }
    }
}

@Composable
private fun PendingBubble(
    sid: String,
    machineId: String,
    online: Boolean,
    pending: SessionPendingInputView,
) {
    val copy = uiText()
    val p = pal
    val remote = LocalRemote.current
    var expanded by remember(pending.requestId) { mutableStateOf(false) }
    val cancelBusy = pending.cancel.busy || pending.cancel.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING
    val resumeBusy = pending.resume.busy || pending.resume.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING
    val captionText = listOfNotNull(
        if (pending.hasOriginLabel()) uiText(pending.originLabel).ifEmpty { null } else null,
        if (pending.hasLabel()) uiText(pending.label).ifEmpty { null } else null,
        if (pending.hasCaption()) uiText(pending.caption).ifEmpty { null } else null,
    ).joinToString(" · ")
    val errText = if (pending.hasFeedback()) uiText(pending.feedback) else ""

    Column(Modifier.fillMaxWidth().padding(horizontal = ChatSide, vertical = 6.dp), horizontalAlignment = Alignment.End) {
        Column(
            Modifier.fillMaxWidth(.84f).wrapContentWidth(Alignment.End)
                .clip(Radii.R22)
                .border(1.dp, p.faint, Radii.R22)
                .clickable { expanded = !expanded }
                .padding(horizontal = 16.dp, vertical = 10.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Text(
                pending.text.ifEmpty { copy(UiMessages.imageAndroidBubble()) },
                style = T.s17,
                color = p.ink2,
                maxLines = if (expanded) Int.MAX_VALUE else 4,
                overflow = TextOverflow.Ellipsis,
            )
        }
        Row(
            Modifier.fillMaxWidth(.84f).heightIn(min = 36.dp).padding(start = 8.dp, end = 8.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.End,
        ) {
            val shownCap = errText.ifEmpty { captionText }
            Text(
                shownCap,
                style = T.s13,
                color = if (errText.isNotEmpty() && !pending.feedbackUncertain) p.red else if (pending.feedbackUncertain) p.orangeInk else p.ink2,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                textAlign = TextAlign.End,
                modifier = Modifier.weight(1f, fill = false),
            )
            if (pending.canResume) {
                TextAction(
                    if (pending.resume.status == SessionCommandStatus.SESSION_COMMAND_STATUS_FAILED) copy(UiMessages.commonRetry()) else copy(UiMessages.composerSend()),
                    {
                        remote.submitSessionCommand(
                            sid,
                            Command.newBuilder().setResumeInput(ResumeInputRequest.newBuilder().setSessionId(sid).setRequestId(pending.requestId)).build(),
                            machineId = machineId,
                        )
                    },
                    enabled = online && !resumeBusy,
                    modifier = Modifier.padding(start = 8.dp),
                    style = T.s13,
                    alignment = Alignment.Center,
                )
            }
            if (pending.canCancel) {
                TextAction(
                    if (pending.cancel.status == SessionCommandStatus.SESSION_COMMAND_STATUS_FAILED) copy(UiMessages.commonRetry()) else copy(UiMessages.commonCancel()),
                    {
                        remote.submitSessionCommand(
                            sid,
                            Command.newBuilder().setCancelInput(CancelInputRequest.newBuilder().setSessionId(sid).setRequestId(pending.requestId)).build(),
                            machineId = machineId,
                        )
                    },
                    enabled = online && !cancelBusy,
                    modifier = Modifier.padding(start = 8.dp),
                    style = T.s13,
                    alignment = Alignment.Center,
                )
            }
        }
    }
}

@Composable
private fun OutgoingView(
    sid: String,
    machineId: String,
    o: SessionOutgoingView,
) {
    val copy = uiText()
    val p = pal
    val remote = LocalRemote.current
    val local = Outbox.find(sid, o.requestId)
    val failed = o.state == SessionOutgoingState.SESSION_OUTGOING_STATE_FAILED
    val uncertain = o.feedbackUncertain || o.state == SessionOutgoingState.SESSION_OUTGOING_STATE_UNKNOWN

    val captionText = when {
        o.hasFeedback() -> uiText(o.feedback)
        o.hasCaption() -> uiText(o.caption)
        else -> ""
    }
    val captionColor = when {
        failed && !uncertain -> p.red
        uncertain -> p.orangeInk
        else -> p.ink2
    }

    val links = buildList {
        if (o.canRestore) {
            add(CaptionLink(CaptionAction.RESTORE, copy(UiMessages.composerRestore())) {
                remote.restoreOutgoing(sid, o.requestId)
            })
        }
        if (o.canRetry) {
            add(CaptionLink(CaptionAction.RETRY, copy(UiMessages.commonRetry())) {
                remote.submitSessionCommand(
                    sid,
                    Command.newBuilder().setSend(
                        SendRequest.newBuilder()
                            .setSessionId(sid)
                            .setRequestId(o.requestId)
                            .setText(o.text)
                            .addAllImageIds(o.imageIdsList)
                            .addAllFileIds(o.fileIdsList)
                            .setMode(o.mode),
                    ).build(),
                    machineId = machineId,
                )
            })
        }
        if (o.canDismiss) {
            add(CaptionLink(CaptionAction.DROP, copy(UiMessages.sessionDismissNotice())) {
                remote.dismissOutgoing(sid, o.requestId)
                Outbox.remove(sid, o.requestId, releaseImages = true)
            })
        }
    }

    Column(Modifier.fillMaxWidth().padding(horizontal = ChatSide)) {
        if (local != null) {
            FileChips(
                local.images.filter { it.isFile }.map {
                    io.github.lewin671.remoteagent.proto.SentFile.newBuilder()
                        .setFileId(it.fileId.orEmpty())
                        .setName(it.fileName.orEmpty())
                        .setSizeBytes(it.fileBytes)
                        .build()
                },
            )
            if (local.images.any { !it.isFile }) {
                Row(Modifier.align(Alignment.End).padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                    for (img in local.images.filterNot { it.isFile }.take(4)) img.thumb?.let { bmp ->
                        Image(bmp, null, Modifier.size(64.dp).clip(Radii.R14), contentScale = ContentScale.Crop)
                    }
                }
            }
        }
        MeBubble(
            o.text.ifEmpty { if (o.imageIdsCount > 0 || o.fileIdsCount > 0 || local != null) null else "" },
            caption = captionText.ifEmpty { null },
            captionColor = captionColor,
            failed = failed && !uncertain,
            links = links,
            padded = false,
            animated = true,
        )
    }
}

@Composable
private fun BottomArea(
    sid: String,
    state: RemoteState,
    vm: SessionViewModel,
    onTerminal: () -> Unit,
    onPlan: () -> Unit,
    onQuestion: (String) -> Unit,
    onApproval: (String) -> Unit,
    onSent: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val remote = LocalRemote.current
    val header = vm.header

    if (header.exited) {
        val resumeSlot = vm.actions.resume
        val resuming = resumeSlot.busy || resumeSlot.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING
        val err = if (resumeSlot.hasErrorText()) uiText(resumeSlot.errorText) else ""
        EndCard(
            if (resuming) copy(UiMessages.sessionResuming()) else copy(UiMessages.sessionEnded()),
            listOfNotNull(
                if (header.hasEndedText()) uiText(header.endedText).ifEmpty { null } else null,
                if (vm.actions.hasResumeNote()) uiText(vm.actions.resumeNote).ifEmpty { null } else null,
            ).joinToString(" "),
            running = resuming,
        ) {
            if (vm.actions.canResume && !resuming) {
                PillButton(
                    if (err.isNotEmpty()) copy(UiMessages.sessionTryAgain()) else copy(UiMessages.sessionResume()),
                    {
                        remote.submitSessionCommand(
                            sid,
                            Command.newBuilder().setResumeSession(ResumeSessionRequest.newBuilder().setSessionId(sid)).build(),
                            machineId = header.machineId,
                        )
                    },
                    Modifier.fillMaxWidth().padding(top = 14.dp),
                    if (err.isNotEmpty()) ButtonKind.SECONDARY else ButtonKind.INK,
                    enabled = header.computerOnline && !header.computerUnlinked,
                )
            }
            if (err.isNotEmpty()) {
                Text(copy(UiMessages.sessionAndroidResumeFailed(reason = err)), color = p.red, style = T.s13, modifier = Modifier.padding(top = 8.dp))
            }
        }
        return
    }

    if (!header.computerOnline) {
        val t = now()
        val computer = if (header.hasComputerName()) uiText(header.computerName) else machineLabel(state, header.machineId)
        val offlineNote = if (header.hasOfflineText()) uiText(header.offlineText)
        else copy(UiMessages.sessionOfflineNote(lastSeen = if (header.offlineSinceMs > 0) copy(UiMessages.sessionDisconnectedAgo(listWhen(header.offlineSinceMs, t).resolve(copy))) else ""))
        EndCard(copy(UiMessages.newSessionMachineOffline(name = computer)), offlineNote) {}
        return
    }

    Reveal(
        visible = vm.dock.hasItem,
        enter = Motion.appear(Motion.BASE, riseDp = Motion.DOCK_RISE_DP, grow = true),
        exit = Motion.vanish(Motion.BASE),
    ) {
        SessionDock(vm, onTerminal = onTerminal, onPlan = onPlan, onQuestion = onQuestion, onApproval = onApproval)
    }

    Composer(sid = sid, state = state, vm = vm, onSent = onSent, onFocus = {})
}

@Composable
private fun EndCard(
    title: String,
    text: String,
    running: Boolean = false,
    actions: @Composable () -> Unit,
) {
    val p = pal
    Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.TopCenter) {
        Column(
            Modifier.widthIn(max = ChatMax).fillMaxWidth()
                .padding(start = 12.dp, end = 12.dp, top = 6.dp, bottom = 12.dp)
                .clip(Radii.R28)
                .background(p.gbg)
                .padding(18.dp),
        ) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (running) Spinner(p.ink2, size = 15.dp) else StatusMark(MarkKind.OFF)
                Text(title, style = T.s17b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            if (text.isNotEmpty()) Text(text, style = T.s15, color = p.ink2, modifier = Modifier.padding(top = 6.dp))
            actions()
        }
    }
}

@Composable
private fun Composer(
    sid: String,
    state: RemoteState,
    vm: SessionViewModel,
    onSent: () -> Unit,
    onFocus: (Boolean) -> Unit,
) {
    val copy = uiText()
    val p = pal
    val remote = LocalRemote.current
    val view = LocalView.current
    val header = vm.header
    val composer = vm.composer
    val caps = header.capabilities
    val machineId = header.machineId
    val machine = state.machines[machineId]
    val online = header.computerOnline && !header.computerUnlinked
    val agent = header.agent.takeIf { it != Agent.AGENT_UNSPECIFIED } ?: DEFAULT_AGENT

    val images = ImageDrafts.forSession(sid)
    var text by images.text
    val keeping = Outbox.keeping(sid)
    val maxImg = caps.maxImages.takeIf { it > 0 } ?: maxImages(machine)
    val machineName = if (header.hasComputerName()) uiText(header.computerName) else machineLabel(state, machineId)
    val blocked = uploadBlocked(machine, online, machineName, copy = copy)
    RefreshDraftUploads(images, remote, machineId, blocked)
    val pick = rememberImagePicker(images, remote, machineId, blocked, maxImg, videos = caps.supportsFiles || filesSupported(machine))

    fun send(requestedMode: SendMode = SendMode.SEND_MODE_STEER) {
        val mode = sendModeFor(agent, requestedMode, composer.hasActiveTurn)
        val msg = text.trim()
        if (images.restoring || images.readingFiles || fileDraftBlocked(images, machine, copy = copy) != null || (msg.isEmpty() && images.images.isEmpty())) return
        images.sendAllTo(remote, machineId)
        if (!images.ready(machineId) && images.images.isNotEmpty()) return
        val picked = images.images.toList()
        Motion.tap(view)
        if (picked.isEmpty()) {
            text = ""
            onSent()
            remote.submitSessionCommand(
                sid,
                Command.newBuilder().setSend(
                    SendRequest.newBuilder().setSessionId(sid).setRequestId(newRequestId()).setText(msg).setMode(mode),
                ).build(),
                machineId = machineId,
            )
            return
        }
        Outbox.sendWithImages(
            RaApp.instance.scope,
            remote,
            sid,
            msg,
            images.ids(machineId),
            picked,
            images.original,
            mode,
            fileIds = images.fileIds(machineId),
        ) { kept ->
            kept.onSuccess {
                if (text.trim() == msg) text = ""
                images.detach(picked)
                onSent()
            }.onFailure {
                images.notice = copy(UiMessages.composerAndroidNotSent(reason = displayError(it)))
                images.sendAllTo(remote, machineId)
            }
        }
    }

    val interruptSlot = vm.actions.interrupt
    val interruptBusy = interruptSlot.busy || interruptSlot.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING
    val placeholder = if (composer.hasPlaceholder()) uiText(composer.placeholder) else copy(UiMessages.composerMessage())

    ComposeBox(
        text = text,
        onText = { text = it },
        placeholder = placeholder,
        canSend = composer.enabled &&
            !images.restoring && !images.readingFiles && fileDraftBlocked(images, machine, copy = copy) == null &&
            !keeping && online &&
            (text.isNotBlank() || images.images.isNotEmpty()) &&
            (images.images.isEmpty() || images.ready(machineId)),
        onSend = { send() },
        onPlus = pick,
        onFile = rememberDocumentPicker(images, remote, machineId),
        plusEnabled = composer.canAttachImages && !images.restoring && (blocked == null || (caps.supportsFiles && online && header.computerOnline)),
        onQueue = if (composer.supportsQueueMode && composer.hasActiveTurn) ({ send(SendMode.SEND_MODE_QUEUE) }) else null,
        onInterrupt = if (composer.interruptVisible) ({
            remote.submitSessionCommand(
                sid,
                Command.newBuilder().setInterrupt(InterruptRequest.newBuilder().setSessionId(sid)).build(),
                machineId = machineId,
            )
        }) else null,
        canInterrupt = online && vm.actions.canInterrupt && !interruptBusy,
        hasDraft = text.isNotBlank() || images.images.isNotEmpty(),
        onFocus = onFocus,
        top = { ImageStrip(images, remote, machineId, maxImg) },
    )
    if (interruptSlot.hasErrorText()) {
        Notice(copy(UiMessages.sessionAndroidInterruptFailed(reason = uiText(interruptSlot.errorText))), Modifier.padding(horizontal = 12.dp).padding(bottom = 8.dp), NoticeTone.ERROR)
    }
    if (composer.hasDisabledReason() && !composer.enabled) {
        Hint(uiText(composer.disabledReason), Modifier.padding(horizontal = ChatSide), inset = 0.dp)
    }
    if (text.trimStart().startsWith("/")) {
        Text(copy(UiMessages.composerSlashNote()), Modifier.padding(horizontal = ChatSide).padding(bottom = 8.dp), style = T.s13, color = p.orangeInk)
    }
}

@Composable
fun Skeleton() {
    val p = pal
    Column(Modifier.padding(horizontal = ChatSide, vertical = 8.dp)) {
        for (w in listOf(0.7f, 0.9f, 0.5f)) Box(
            Modifier.padding(vertical = 7.dp).fillMaxWidth(w).height(12.dp).clip(RoundedCornerShape(6.dp)).background(p.fill2),
        )
    }
}

class Follow {
    var on by mutableStateOf(true)
        private set
    var sends by mutableIntStateOf(0)
        private set

    fun observe(userDragging: Boolean, atBottom: Boolean) {
        if (atBottom) on = true else if (userDragging) on = false
    }

    fun sent() {
        on = true
        sends++
    }
}

class NewMessages {
    private var seen by mutableIntStateOf(0)
    fun saw(rows: Int) { seen = rows }
    fun joined(rows: Int) { seen += rows }
    fun count(rows: Int) = maxOf(0, rows - seen)
}

fun rowsInFront(before: List<String>, after: List<String>): Int {
    val at = HashMap<String, Int>(after.size * 2)
    after.forEachIndexed { i, key -> at[key] = i }
    var behind = 0
    for (i in before.indices.reversed()) {
        val j = at[before[i]] ?: continue
        behind = (after.size - 1 - j) - (before.size - 1 - i)
        break
    }
    return maxOf(0, after.size - before.size - maxOf(0, behind))
}

fun LazyListState.keepPlace(before: List<Any>, after: List<Any>, joined: Int) {
    val index = firstVisibleItemIndex
    val key = layoutInfo.visibleItemsInfo.firstOrNull { it.index == index }?.key
    val was = if (key == null) -1 else before.indexOf(key)
    val now = if (was < 0) -1 else after.indexOf(key)
    requestScrollToItem(index + if (now >= 0) now - was else joined, firstVisibleItemScrollOffset)
}

data class ForkAction(
    val text: String = sessionText(UiMessages.sessionFork()),
    val note: String = "",
    val noteError: Boolean = false,
    val enabled: Boolean = true,
    val busy: Boolean = false,
    val abandons: Boolean = false,
)

fun forkAction(
    blocked: String?,
    running: Boolean,
    error: String,
    frozen: Boolean,
    expired: Boolean,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText,
): ForkAction = when {
    running -> ForkAction(text = copy(UiMessages.sessionForking()), busy = true)
    blocked != null -> ForkAction(text = copy(UiMessages.sessionFork()), note = blocked, enabled = false)
    expired -> ForkAction(text = copy(UiMessages.sessionAbandonFork()), note = copy(UiMessages.sessionForkExpired()), noteError = true, abandons = true)
    frozen -> ForkAction(text = copy(UiMessages.sessionFork()), note = copy(UiMessages.sessionForkUnknown()), noteError = true)
    else -> ForkAction(text = copy(UiMessages.sessionFork()), note = if (error.isNotEmpty()) copy(UiMessages.sessionForkFailed(reason = error)) else "", noteError = true)
}
