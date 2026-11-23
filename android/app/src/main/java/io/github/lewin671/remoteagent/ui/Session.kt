package io.github.lewin671.remoteagent.ui

import android.content.Context
import android.content.res.Configuration
import androidx.activity.compose.BackHandler
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.Crossfade
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
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
import androidx.compose.foundation.lazy.itemsIndexed
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
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Popup
import androidx.compose.ui.window.PopupProperties
import kotlin.math.roundToInt
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.CORE_SESSION_WINDOW_LIMIT
import io.github.lewin671.remoteagent.core.DEFAULT_AGENT
import io.github.lewin671.remoteagent.core.MAX_IMAGES
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.listWhen
import io.github.lewin671.remoteagent.core.maxFiles
import io.github.lewin671.remoteagent.core.newRequestId
import io.github.lewin671.remoteagent.core.proto.ComputerReachability
import io.github.lewin671.remoteagent.core.proto.SessionCommandStatus
import io.github.lewin671.remoteagent.core.proto.SessionDisplayStatus
import io.github.lewin671.remoteagent.core.proto.SessionSharedFileItem
import io.github.lewin671.remoteagent.core.proto.SessionTimelineItem
import io.github.lewin671.remoteagent.core.proto.SessionViewModel
import io.github.lewin671.remoteagent.core.proto.SessionWindowRequest
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.ForkSessionRequest
import io.github.lewin671.remoteagent.proto.InterruptRequest
import io.github.lewin671.remoteagent.proto.ResumeSessionRequest
import io.github.lewin671.remoteagent.proto.SendMode
import io.github.lewin671.remoteagent.proto.SendRequest
import io.github.lewin671.remoteagent.proto.StopSessionRequest
import io.github.lewin671.remoteagent.term.TerminalPane
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/** Sessions whose terminal another page asked to show. */
private const val TAIL_ERROR_KEY = "tail-error"

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
    val syncView by remote.coreHost.syncStateView.collectAsState()

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

    val vm by remember(remote, sid, initialWindow) { remote.watchSession(sid, initialWindow) }
        .collectAsState(initial = remember(sid) { remote.currentSessionView(sid) ?: SessionViewModel.getDefaultInstance() })
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

    LaunchedEffect(remote, sid) {
        remote.resendAttachmentsEvents.collect { eff ->
            if (eff.sessionId != sid) return@collect
            Outbox.resendWithAttachments(this, remote, sid, eff)
        }
    }

    LaunchedEffect(vm.composer.retainedAttachmentRequestIdsList, vm.composer.pendingTray.orderedItemsList) {
        val active = buildSet {
            addAll(vm.composer.retainedAttachmentRequestIdsList)
            vm.composer.pendingTray.orderedItemsList.forEach { add(it.requestId) }
        }
        Outbox.pruneSettled(sid, active)
    }

    if (!header.found) {
        Column(
            Modifier.fillMaxSize().background(p.paper).windowInsetsPadding(WindowInsets.safeDrawing).padding(24.dp),
            verticalArrangement = Arrangement.Center,
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            if (header.lookupFailed) {
                Text(
                    uiText(header.lookupErrorText).ifEmpty { copy(UiMessages.sessionAndroidUnknown()) },
                    style = T.s17b,
                    color = p.ink,
                    textAlign = TextAlign.Center,
                )
                PillButton(uiText(UiMessages.commonRetry()), { remote.retrySessionLoad(sid) }, Modifier.padding(top = 16.dp), ButtonKind.INK)
                TextLink(uiText(UiMessages.commonBack()), nav.back)
            } else if (header.missing || (header.sessionsListed && header.hasMissingText())) {
                Text(
                    uiText(header.missingText).ifEmpty { copy(UiMessages.sessionAndroidUnknown()) },
                    style = T.s17b,
                    color = p.ink,
                    textAlign = TextAlign.Center,
                )
                PillButton(uiText(UiMessages.commonRetry()), { remote.retrySessionLoad(sid) }, Modifier.padding(top = 16.dp), ButtonKind.INK)
                TextLink(uiText(UiMessages.commonBack()), nav.back)
            } else {
                var showSpinner by remember(sid) { mutableStateOf(false) }
                LaunchedEffect(sid) {
                    delay(300)
                    showSpinner = true
                }
                if (showSpinner) {
                    Spinner(p.ink2, size = 24.dp)
                    Text(
                        if (header.hasStatusText()) uiText(header.statusText) else copy(UiMessages.projectsReadingDirectory()),
                        style = T.s15,
                        color = p.ink2,
                        modifier = Modifier.padding(top = 12.dp),
                    )
                }
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
        if (!compactTerminal) SessionTopBar(sid, vm = vm, dark = term)
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
        ConnStrip(syncView, Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp))
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
    val forkAct = actions.forkAction
    val forking = forkAct.busy
    val high = header.hasContext && header.highContextWarning
    val statusStr = uiText(header.statusText)
    val modeStr = if (header.exited || !header.hasApprovalModeLabel()) "" else uiText(header.approvalModeLabel)
    val subtitleText = listOf(statusStr, modeStr).filter { it.isNotEmpty() }.joinToString(" · ")
    val rich = if (!header.exited && header.approvalMode == ApprovalMode.APPROVAL_MODE_FULL_ACCESS && modeStr.isNotEmpty()) {
        buildAnnotatedString {
            append("$statusStr · ")
            pushStyle(SpanStyle(color = p.red))
            append(modeStr)
            pop()
        }
    } else null

    val taskOwned = header.taskOwned
    BackHandler(enabled = taskOwned && !dark) { nav.task(header.machineId, header.scheduledTaskId) }
    val back = { if (taskOwned) nav.task(header.machineId, header.scheduledTaskId) else nav.back() }

    val online = header.computerOnline && !header.computerUnlinked
    val chip = if (header.hasDirectChip()) header.directChip else null
    val directActive = !header.exited && online && chip != null && chip.active
    var directOpen by remember(sid) { mutableStateOf(false) }
    LaunchedEffect(header.exited, online) {
        if (header.exited || !online) directOpen = false
    }
    val showDirectBadge = !dark && ((!header.exited && online && chip?.visible == true) || directOpen)
    val computer = if (header.hasComputerName()) uiText(header.computerName) else header.machineId.take(8)

    Box(Modifier.fillMaxWidth()) {
        Row(
            Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            GlassButton(Lucide.ChevronLeft, uiText(UiMessages.commonBack()), back, dark = dark)
            Column(
                Modifier.weight(1f)
                    .heightIn(min = 44.dp)
                    .then(
                        if (showDirectBadge) {
                            Modifier
                                .clickable(role = Role.Button, onClickLabel = copy(UiMessages.transportPopoverA11Y())) {
                                    directOpen = !directOpen
                                }
                                .semantics { contentDescription = copy(UiMessages.transportPopoverA11Y()) }
                        } else Modifier,
                    ),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.Center,
            ) {
                Text(uiText(header.title), style = T.s17b, color = if (dark) CardColors.text else p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Row(verticalAlignment = Alignment.CenterVertically) {
                    val sub = if (dark) CardColors.text2 else p.ink2
                    displayMarkKind(header.status)?.let { mk ->
                        StatusMark(mk, size = 7.dp, color = if (dark && mk != MarkKind.WAITING) CardColors.text2 else null)
                        Spacer(Modifier.width(5.dp))
                    }
                    if (rich != null) Text(rich, style = T.s12, color = sub, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                    else Text(subtitleText, style = T.s12, color = sub, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                    AnimatedVisibility(
                        visible = showDirectBadge,
                        enter = fadeIn(tween(200)),
                        exit = fadeOut(tween(160)),
                    ) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Text(" · ", style = T.s12, color = sub)
                            Ic(Lucide.Zap, 12.dp, sub)
                            Spacer(Modifier.width(3.dp))
                            val label = if (chip != null && chip.hasLabelText()) uiText(chip.labelText).ifEmpty { copy(UiMessages.transportDirect()) } else copy(UiMessages.transportDirect())
                            Text(label, style = T.s12, color = sub, maxLines = 1)
                        }
                    }
                }
            }
            GlassButton(Lucide.Ellipsis, copy(UiMessages.commonAndroidMore()), { menu = true }, dot = high, dark = dark)
        }
        if (directOpen && !dark) {
            Popup(
                alignment = Alignment.TopCenter,
                onDismissRequest = { directOpen = false },
                properties = PopupProperties(focusable = true, dismissOnBackPress = true, dismissOnClickOutside = true),
            ) {
                val popBg = if (p.dark) p.raised else p.paper
                Column(
                    Modifier
                        .padding(top = 58.dp, start = 20.dp, end = 20.dp)
                        .widthIn(max = 332.dp)
                        .fillMaxWidth()
                        .floating(p, Radii.R18)
                        .clip(Radii.R18)
                        .background(popBg)
                        .border(0.5.dp, p.sep, Radii.R18)
                        .padding(start = 16.dp, end = 16.dp, top = 14.dp, bottom = 6.dp),
                ) {
                    Crossfade(
                        targetState = directActive,
                        animationSpec = tween(if (Motion.reduced) 100 else 200),
                    ) { isDirect ->
                        Column {
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                StatusMark(if (isDirect) MarkKind.ONLINE else MarkKind.OFF, color = if (isDirect) p.green else p.faint)
                                val title = when {
                                    isDirect && chip != null && chip.hasPopoverTitle() -> uiText(chip.popoverTitle)
                                    isDirect -> copy(UiMessages.transportPopoverDirectTitle(name = computer))
                                    chip != null && chip.hasPopoverTitle() && !chip.active -> uiText(chip.popoverTitle)
                                    else -> copy(UiMessages.transportPopoverRelayTitle(name = computer))
                                }
                                Text(title, style = T.s15b.digits(), color = p.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
                            }
                            val body = when {
                                isDirect && chip != null && chip.hasPopoverBody() -> uiText(chip.popoverBody)
                                isDirect -> copy(UiMessages.transportPopoverDirectBody())
                                chip != null && chip.hasPopoverBody() && !chip.active -> uiText(chip.popoverBody)
                                else -> copy(UiMessages.transportPopoverRelayBody())
                            }
                            Text(
                                body,
                                style = T.s13,
                                color = p.ink2,
                                modifier = Modifier.padding(top = 6.dp),
                            )
                        }
                    }
                    Row(
                        Modifier.fillMaxWidth().padding(top = 10.dp).topLine(p.sep).heightIn(min = 44.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.SpaceBetween,
                    ) {
                        val footerStatus = when {
                            !directActive -> if (chip != null && !chip.active && chip.hasPopoverFoot()) uiText(chip.popoverFoot) else copy(UiMessages.transportPopoverSwitchedRelay())
                            chip?.hasPopoverFoot() == true -> uiText(chip.popoverFoot)
                            else -> copy(UiMessages.transportPopoverStable())
                        }
                        Text(footerStatus, style = T.s13, color = p.ink2, modifier = Modifier.weight(1f, fill = false))
                        TextAction(
                            copy(UiMessages.transportPopoverSettings()),
                            onClick = {
                                directOpen = false
                                nav.deviceConnection(header.machineId)
                            },
                            arrow = true,
                        )
                    }
                }
            }
        }
    }

    if (menu) {
        val computer = if (header.hasComputerName()) uiText(header.computerName) else header.machineId
        val menuTitle = listOfNotNull(
            header.agentName.ifEmpty { null },
            if (header.hasProjectName()) uiText(header.projectName).ifEmpty { null } else null,
            computer.takeIf { it.isNotEmpty() && header.multipleMachines },
        ).joinToString(" · ")
        val modelLabel = header.modelLabel.ifEmpty { header.model }
        val effortStr = if (header.hasEffortLabel()) uiText(header.effortLabel) else header.effort
        val modelValue = modelLabel + if (effortStr.isNotEmpty()) " · $effortStr" else ""

        ActionSheet(
            info = {
                SheetInfo(
                    menuTitle,
                    header.shortPath.ifEmpty { header.cwd },
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
                    add(SheetAction(copy(UiMessages.projectsAndroidSettings()), enabled = !forking && header.projectKey.isNotEmpty()) { sheet = 3 })
                    if (actions.canRename) {
                        add(SheetAction(copy(UiMessages.sessionRename()), enabled = !forking) { rename = true })
                    }
                    if (forkAct.visible) {
                        add(
                            SheetAction(
                                uiText(forkAct.label).ifEmpty { copy(UiMessages.sessionFork()) },
                                enabled = forkAct.enabled,
                                busy = forkAct.busy,
                                keepOpen = true,
                                note = uiText(forkAct.note),
                                noteError = forkAct.noteError,
                            ) {
                                if (forkAct.abandons && forkAct.canAbandon) {
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
        2 -> ModelSheet(
            sid,
            vm,
            onClose = { sheet = 0 },
            onOpenUsage = {
                val ag = header.agent
                nav.openUsage(header.machineId, ag, header.model)
            },
        )
        3 -> header.projectKey.takeIf { it.isNotEmpty() }?.let { key ->
            CoreProjectSettingsSheet(projectKey = key, onClose = { sheet = 0 })
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

    val trayItems = vm.composer.pendingTray.orderedItemsList
    val scope = rememberCoroutineScope()
    val atBottom by remember { derivedStateOf { !list.canScrollForward } }
    val dragged by list.interactionSource.collectIsDraggedAsState()
    LaunchedEffect(dragged, atBottom) { followState.observe(dragged, atBottom) }
    val follow = followState.on
    val endIndex by rememberUpdatedState(items.size + 4)
    val ime = WindowInsets.ime.getBottom(LocalDensity.current)

    LaunchedEffect(
        followState.sends,
        items.size,
        trayItems.size,
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
        snapshotFlow { list.layoutInfo.totalItemsCount to list.layoutInfo.viewportEndOffset }.collect { (count, _) ->
            if (followState.on && !list.isScrollInProgress && list.canScrollForward && count > 0) {
                list.scrollToItem(count - 1, 1_000_000)
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

    entrances.outgoing(trayItems.map { it.requestId })

    Column(modifier.clipToBounds().onSizeChanged { shownHeight = it.height }) {
        val sharedItemsById = remember(items) {
            buildMap<String, SessionSharedFileItem> {
                fun visit(list: List<SessionTimelineItem>) {
                    for (it in list) {
                        when (it.kindCase) {
                            SessionTimelineItem.KindCase.SHARED_FILES ->
                                it.sharedFiles.filesList.forEach { f -> put(f.fileId, f) }
                            SessionTimelineItem.KindCase.QUIET_SCHEDULED_GROUP ->
                                visit(it.quietScheduledGroup.itemsList)
                            else -> Unit
                        }
                    }
                }
                visit(items)
            }
        }
        SharedFileHost(
            sessionId = sid,
            itemsById = sharedItemsById,
            modifier = Modifier.weight(1f),
        ) {
            Box(Modifier.fillMaxSize()) {
                val chatCtx = ChatContext(
                    sid = sid,
                    agent = header.agent.takeIf { it != Agent.AGENT_UNSPECIFIED } ?: DEFAULT_AGENT,
                    agentName = header.agentName,
                    onImage = { seq, i -> nav.image(sid, seq, i) },
                    onSession = { nav.session(it) },
                )
                CompositionLocalProvider(LocalChat provides chatCtx) {
                    var showSkeleton by remember(sid) { mutableStateOf(false) }
                    LaunchedEffect(sid, tl.loading, items.isEmpty()) {
                        if (tl.loading && items.isEmpty()) {
                            delay(300)
                            showSkeleton = true
                        } else {
                            showSkeleton = false
                        }
                    }
                    LazyColumn(
                        state = list,
                        modifier = Modifier.fillMaxSize(),
                        contentPadding = PaddingValues(top = 12.dp),
                    ) {
                        if ((tl.hasEarlier || tl.canLoadEarlier || tl.backfilling) && (items.isNotEmpty() || !tl.loading)) {
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
                        if (showSkeleton && tl.loading && items.isEmpty()) item(key = "skeleton") { Skeleton() }
                        itemsIndexed(items, key = { _, it -> it.id }) { i, item ->
                            val kind = remember(item.id) { entrances.take(item.id) }
                            Box(Modifier.entrance(kind)) {
                                TimelineItemView(
                                    item = item,
                                    receiptBefore = i > 0 && items[i - 1].kindCase == SessionTimelineItem.KindCase.NOTIFY_RECEIPT,
                                    receiptAfter = i + 1 < items.size && items[i + 1].kindCase == SessionTimelineItem.KindCase.NOTIFY_RECEIPT,
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
                            item(key = TAIL_ERROR_KEY) {
                                Notice(
                                    copy(UiMessages.sessionAndroidHistoryTailFailed(reason = uiText(tl.error))),
                                    Modifier.padding(horizontal = ChatSide, vertical = 8.dp),
                                    NoticeTone.ERROR,
                                    action = uiText(UiMessages.commonRetry()),
                                    onAction = { remote.loadEarlierSessionHistory(sid) },
                                )
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
            if (header.taskOwned) {
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

    if (header.computerUnlinked && header.computerReachability != ComputerReachability.COMPUTER_REACHABILITY_UNKNOWN) {
        val computer = if (header.hasComputerName()) uiText(header.computerName) else header.machineId.take(8)
        val offlineNote = if (header.hasOfflineText()) uiText(header.offlineText)
        else copy(UiMessages.projectsComputerOffline())
        EndCard(copy(UiMessages.newSessionMachineOffline(name = computer)), offlineNote) {}
        return
    }

    var dockDenying by remember(sid) { mutableStateOf(false) }
    var dockOpen by remember(sid) { mutableStateOf(false) }
    val showDock = vm.dock.hasItem

    Reveal(
        visible = showDock || dockDenying,
        enter = Motion.appear(Motion.BASE, riseDp = Motion.DOCK_RISE_DP, grow = true),
        exit = Motion.vanish(Motion.BASE),
    ) {
        SessionDock(
            vm,
            onTerminal = onTerminal,
            onPlan = onPlan,
            onQuestion = onQuestion,
            onApproval = onApproval,
            onDenyingChange = { dockDenying = it },
            onOpenChange = { dockOpen = it },
        )
    }

    if (!dockDenying) {
        Composer(sid = sid, state = state, vm = vm, onSent = onSent, onFocus = {}, onTerminal = onTerminal, demoted = showDock && dockOpen)
    }
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
internal fun Composer(
    sid: String,
    state: RemoteState,
    vm: SessionViewModel,
    onSent: () -> Unit,
    onFocus: (Boolean) -> Unit,
    onTerminal: (() -> Unit)? = null,
    demoted: Boolean = false,
) {
    val copy = uiText()
    val p = pal
    val remote = LocalRemote.current
    val view = LocalView.current
    val header = vm.header
    val composer = vm.composer
    val caps = header.capabilities
    val machineId = header.machineId
    val online = header.computerOnline && !header.computerUnlinked
    val attachCaps = header.attachmentCaps()

    val images = ImageDrafts.forSession(sid)
    var text by images.text
    val keeping = Outbox.keeping(sid)
    val maxImg = attachCaps.maxImages
    val blocked = if (caps.hasImageUploadBlockedReason()) uiText(caps.imageUploadBlockedReason).ifEmpty { null } else null
    RefreshDraftUploads(images, remote, machineId, blocked, attachCaps)
    val pick = rememberImagePicker(images, remote, machineId, blocked, attachCaps)

    fun send(requestedMode: SendMode? = null) {
        val mode = requestedMode ?: composer.defaultSendMode.takeIf { it != SendMode.SEND_MODE_UNSPECIFIED } ?: SendMode.SEND_MODE_STEER
        val msg = text.trim()
        if (images.restoring || images.readingFiles || fileDraftBlocked(images, attachCaps, copy = copy) != null || (msg.isEmpty() && images.images.isEmpty())) return
        images.sendAllTo(remote, machineId, attachCaps)
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
                images.sendAllTo(remote, machineId, attachCaps)
            }
        }
    }

    val interruptSlot = vm.actions.interrupt
    val interruptBusy = interruptSlot.busy || interruptSlot.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING
    val placeholder = if (composer.hasPlaceholder()) uiText(composer.placeholder) else copy(UiMessages.composerMessage())
    val hasPendingTray = composer.hasPendingTray() && (composer.pendingTray.totalCount > 0 || (composer.pendingTray.hasUndo() && composer.pendingTray.undo.visible))
    val imeOpen = WindowInsets.ime.getBottom(LocalDensity.current) > 0
    val compactWidth = LocalConfiguration.current.screenWidthDp < 600

    ComposeBox(
        text = text,
        onText = { text = it },
        placeholder = placeholder,
        canSend = composer.enabled &&
            !images.restoring && !images.readingFiles && fileDraftBlocked(images, attachCaps, copy = copy) == null &&
            !keeping && online &&
            (text.isNotBlank() || images.images.isNotEmpty()) &&
            (images.images.isEmpty() || images.ready(machineId)),
        onSend = { send() },
        onPlus = pick,
        onFile = rememberDocumentPicker(images, remote, machineId, attachCaps),
        plusEnabled = composer.canAttachImages && !images.restoring && (blocked == null || (attachCaps.supportsFiles && online && header.computerOnline)),
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
        demoted = demoted,
        onFocus = onFocus,
        hasPendingTray = hasPendingTray,
        pendingTray = if (hasPendingTray) ({
            PendingTray(
                tray = composer.pendingTray,
                keyboardCollapsed = imeOpen && compactWidth,
                onTerminal = onTerminal,
                onCancelItem = { remote.cancelTrayItem(sid, it) },
                onIgnoreItem = { remote.ignoreTrayItem(sid, it) },
                onPromoteItem = { remote.promoteTrayItem(sid, it) },
                onRestoreItem = { remote.restoreTrayItem(sid, it) },
                onRetryItem = { remote.retryTrayItem(sid, it) },
                onResumeItem = { remote.resumeTrayItem(sid, it) },
                onCancelAllPaused = { remote.cancelAllPaused(sid) },
                onUndo = { remote.undoCancel(sid) },
            )
        }) else null,
        top = { ImageStrip(images, remote, machineId, maxImg, attachCaps) },
    )
    if (interruptSlot.hasErrorText()) {
        Notice(copy(UiMessages.sessionAndroidInterruptFailed(reason = uiText(interruptSlot.errorText))), Modifier.padding(horizontal = 12.dp).padding(bottom = 8.dp), NoticeTone.ERROR)
    }
    if (composer.hasDisabledReason() && !composer.enabled && !hasPendingTray) {
        Hint(uiText(composer.disabledReason), Modifier.padding(horizontal = ChatSide), inset = 0.dp)
    }
    if (text.trimStart().startsWith("/")) {
        val warn = if (composer.hasSlashWarningText()) uiText(composer.slashWarningText) else copy(UiMessages.composerSlashNote())
        if (warn.isNotEmpty()) {
            Text(warn, Modifier.padding(horizontal = ChatSide).padding(bottom = 8.dp), style = T.s13, color = p.orangeInk)
        }
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
