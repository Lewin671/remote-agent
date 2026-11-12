package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.collectIsDraggedAsState
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.ime
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.wrapContentWidth
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.snapshotFlow
import kotlinx.coroutines.flow.collect
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.DEFAULT_AGENT
import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.Status
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.agentInfo
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.contextPercent
import io.github.lewin671.remoteagent.ui.displayError
import io.github.lewin671.remoteagent.core.pendingInputs
import io.github.lewin671.remoteagent.core.projectKey
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.shortPath
import io.github.lewin671.remoteagent.core.titleOf
import io.github.lewin671.remoteagent.core.whenText
import io.github.lewin671.remoteagent.proto.ForkSessionRequest
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.InterruptRequest
import io.github.lewin671.remoteagent.proto.ResumeInputRequest
import io.github.lewin671.remoteagent.proto.ResumeSessionRequest
import io.github.lewin671.remoteagent.proto.SendMode
import io.github.lewin671.remoteagent.proto.StopSessionRequest
import io.github.lewin671.remoteagent.term.TerminalPane
import kotlinx.coroutines.launch
import kotlinx.coroutines.delay
import androidx.compose.ui.draw.clipToBounds

internal fun sessionSubtitle(status: Status, mode: String?, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String =
    copy(io.github.lewin671.remoteagent.core.listStatus(status)) + if (status == Status.EXITED || mode == null) "" else " · $mode"

/** Brief catch-up is routine when opening cached history; only a persistent gap needs a notice. */
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
    val state by remote.state.collectAsState()
    val s = state.sessions[sid]
    val p = pal
    // Not held here: the listing comes a page at a time, so it is asked for by its ID, and
    // only the server saying it has none makes it missing (architecture §8 「按 ID 取」).
    var lookupAttempt by remember(sid) { mutableStateOf(0) }
    val lookup = sessionLookup(sid, lookupAttempt)
    if (s == null) {
        Column(
            Modifier.fillMaxSize().background(p.paper).windowInsetsPadding(WindowInsets.safeDrawing).padding(24.dp),
            verticalArrangement = Arrangement.Center,
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            val note =
                io.github.lewin671.remoteagent.core.lookupNote(lookup, copy = copy).takeIf {
                    state.conn == ConnState.ONLINE ||
                        lookup == io.github.lewin671.remoteagent.core.SessionLookup.Missing
                }
            if (note != null) {
                Text(
                    note,
                    style = T.s17b,
                    color = p.ink,
                    textAlign = androidx.compose.ui.text.style.TextAlign.Center,
                )
                // The server could not be asked: it is asked again by itself, and at once on a tap.
                if (lookup is io.github.lewin671.remoteagent.core.SessionLookup.Failed)
                    PillButton(uiText(UiMessages.commonRetry()), { lookupAttempt++ }, Modifier.padding(top = 16.dp), ButtonKind.INK)
                TextLink(uiText(UiMessages.commonBack()), nav.back)
            } else {
                Spinner(p.ink2, size = 24.dp)
                Text(
                    if (state.conn == ConnState.ONLINE) copy(UiMessages.projectsReadingDirectory()) else copy(UiMessages.sessionAndroidConnectRead()),
                    color = p.ink2,
                    modifier = Modifier.padding(top = 12.dp),
                )
            }
        }
        return
    }
    val events by remote.events(sid).collectAsState()
    val d =
        rememberDerived(
            events,
            s?.agent() ?: DEFAULT_AGENT,
        )
    val status = statusIn(state, s)
    // The view last picked on this device; every session opens in it.
    val context = androidx.compose.ui.platform.LocalContext.current
    val viewPrefs =
        remember(context) {
            context.getSharedPreferences("session-view", android.content.Context.MODE_PRIVATE)
        }
    var tab by rememberSaveable { mutableStateOf(viewPrefs.getInt("tab", 0)) }
    // Another page (a tool's details) asked for the terminal.
    val wantTerminal = OpenTerminal.requested[sid] == true
    LaunchedEffect(wantTerminal) {
        if (wantTerminal) {
            tab = 1
            OpenTerminal.requested.remove(sid)
        }
    }

    val hasTerminal = terminalAvailable(status)
    // 并排 only in a wide window; elsewhere it shows the chat, and what was picked stays (design-system §6).
    val wide = wideWindow()
    val term = tab == 1 && hasTerminal
    val both = tab == 2 && wide && hasTerminal
    BackHandler { if (term) tab = 0 else nav.back() }
    val landscape = androidx.compose.ui.platform.LocalConfiguration.current.orientation == android.content.res.Configuration.ORIENTATION_LANDSCAPE
    val compactTerminal = term && landscape && WindowInsets.ime.getBottom(LocalDensity.current) > 0
    // The terminal is dark in both themes, up to the screen's edges (design-system §6).
    LightSystemBarIcons(term && !p.dark)
    Column(
        Modifier.fillMaxSize().background(if (term) TermColors.bg else p.paper).windowInsetsPadding(WindowInsets.safeDrawing),
    ) {
        if (!compactTerminal) SessionTopBar(
            state,
            s,
            status,
            d.running || status == Status.RUNNING,
            io.github.lewin671.remoteagent.core.sentMessage(s, events.events),
            dark = term,
        )
        if (!compactTerminal) Segmented(
            if (wide) listOf(copy(UiMessages.sessionChat()), copy(UiMessages.sessionTerminal()), copy(UiMessages.sessionSplit())) else listOf(copy(UiMessages.sessionChat()), copy(UiMessages.sessionTerminal())),
            if (term) 1 else if (both) 2 else 0,
            {
                tab = it
                viewPrefs.edit().putInt("tab", it).apply()
            },
            Modifier.align(Alignment.CenterHorizontally).padding(top = 8.dp).widthIn(min = 188.dp).width(androidx.compose.foundation.layout.IntrinsicSize.Max),
            enabled = { it == 0 || hasTerminal },
            dot = if (status == Status.POPUP) 1 else -1,
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
        } else Row(Modifier.weight(1f)) {
            Column(Modifier.weight(1f)) {
            val note = Modifier.padding(horizontal = 20.dp)
            HistoryPartialNotice(sid, events.historyPartial, note)
            if (events.unavailableCount > 0)
                Hint(copy(UiMessages.sessionHistoryUnavailable(count = events.unavailableCount)), note, inset = 0.dp)
            events.liveError?.let { Hint(uiText(it), note, inset = 0.dp) }
            ChatPane(
                state,
                s,
                status,
                events.events,
                d,
                events.loaded,
                events.error?.let { uiText(it) },
                events.fetchError?.let { uiText(it) },
                Modifier.weight(1f),
                onTerminal = { tab = 1 },
            )
            }
            // 并排: the terminal on its dark panel beside the chat.
            if (both) TerminalPane(
                remote,
                sid,
                sessionRunning = true,
                modifier = Modifier.weight(1f).fillMaxHeight().padding(end = 8.dp, bottom = 8.dp).clip(Radii.R16).background(TermColors.bg),
            )
        }
    }
}

/** Light status and navigation bar icons while [on]: over the terminal, which is dark in the light theme too. */
@Composable
private fun LightSystemBarIcons(on: Boolean) {
    val view = androidx.compose.ui.platform.LocalView.current
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

/**
 * Back, the title, the status and mode, and more: the session's mode, model and context (an orange
 * dot when it is high), 重命名, 分叉会话, 中断本轮 and 结束会话…. [sent]: a message was sent in the session, which
 * a fork needs.
 */
@Composable
private fun SessionTopBar(
    state: RemoteState,
    s: SessionView,
    status: Status,
    running: Boolean,
    sent: Boolean,
    dark: Boolean,
) {
    val copy = uiText()
    val p = pal
    val nav = LocalNav.current
    val remote = LocalRemote.current
    var menu by remember { mutableStateOf(false) }
    var confirmStop by remember { mutableStateOf(false) }
    var rename by remember { mutableStateOf(false) }
    var sheet by remember { mutableStateOf(0) } // 1 mode, 2 model, 3 project settings
    val act = rememberAction()
    // The title is kept on the computer: an ended session can be renamed too, but only while the
    // computer is reachable.
    val canRename = state.conn == ConnState.ONLINE && machineOf(state, s)?.online == true
    val live = status != Status.EXITED && canRename
    // A fork starts at once, in the menu: its row shows the progress, and a failure under it. It is
    // kept
    // for the account: one whose outcome is unknown is still that fork after the menu or the page
    // was left.
    val fork = Forks.of(s.id)
    val forking = fork.running
    val forkScope = rememberCoroutineScope()
    val forkRow =
        forkAction(
            io.github.lewin671.remoteagent.core.forkBlocked(status, running, sent)?.let(copy),
            forking,
            fork.error,
            fork.frozen,
            fork.expired,
            copy = copy,
        )
    // Claude Code records full model IDs; show the alias the list offers.
    var models by
        remember(s.machineId) {
            mutableStateOf<io.github.lewin671.remoteagent.proto.ListModelsResponse?>(null)
        }
    // The ⋯ menu's model row says the model's name, as the new-session summary does, so it reads the list when it opens too.
    LaunchedEffect(s.machineId, s.agent(), menu) {
        if (agentInfo(s.agent()).sessionNeedsModelList || menu)
            runCatching { models = remote.listModels(s.machineId, s.agent()) }
    }
    val raw = s.info?.model.orEmpty()
    // Before the list is there, the name the CLI's slugs have (gpt-6.1-sol is GPT-6.1-Sol), never the bare ID.
    val model =
        models?.let { io.github.lewin671.remoteagent.core.modelLabel(it.modelsList, raw, s.agent()) }
            ?: agentInfo(s.agent()).modelLabel(raw)
    val effort = s.info?.effort.orEmpty()
    // Unknown context is left out, never shown as 0%.
    val pc = if (live) contextPercent(s) else null
    val high = (pc ?: 0) > 80
    val mode =
        if (status == Status.EXITED) null else io.github.lewin671.remoteagent.core.modeLabel(s, copy = copy)
    val subtitle = sessionSubtitle(status, mode?.let { it.mode?.let { m -> copy(io.github.lewin671.remoteagent.core.sessionMode(m)) } ?: it.text }, copy = copy)
    val rich =
        if (mode?.mode == Mode.FULL)
            androidx.compose.ui.text.buildAnnotatedString {
                append(copy(io.github.lewin671.remoteagent.core.listStatus(status)) + " · ")
                pushStyle(androidx.compose.ui.text.SpanStyle(color = p.red))
                append(mode.mode?.let { copy(io.github.lewin671.remoteagent.core.sessionMode(it)) } ?: mode.text)
                pop()
            }
        else null
    BackHandler(enabled = s.info?.taskOwned == true && !dark) { nav.task(s.machineId, s.info!!.scheduledTaskId) }
    val back = { if (s.info?.taskOwned == true) nav.task(s.machineId, s.info!!.scheduledTaskId) else nav.back() }
    Row(
        Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        GlassButton(Lucide.ChevronLeft, uiText(UiMessages.commonBack()), back, dark = dark)
        Column(Modifier.weight(1f), horizontalAlignment = Alignment.CenterHorizontally) {
            Text(uiText(io.github.lewin671.remoteagent.core.listTitle(s)), style = T.s17b, color = if (dark) CardColors.text else p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Row(verticalAlignment = Alignment.CenterVertically) {
                val sub = if (dark) CardColors.text2 else p.ink2
                markOf(status)?.let {
                    StatusMark(it, size = 7.dp, color = if (dark) CardColors.text2.takeIf { _ -> it != MarkKind.WAITING } else null)
                    Spacer(Modifier.width(5.dp))
                }
                if (rich != null) Text(rich, style = T.s12, color = sub, maxLines = 1, overflow = TextOverflow.Ellipsis)
                else Text(subtitle, style = T.s12, color = sub, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
        GlassButton(Lucide.Ellipsis, copy(UiMessages.commonAndroidMore()), { menu = true }, dot = high, dark = dark)
    }
    if (menu)
        ActionSheet(
            info = {
                SheetInfo(
                    sessionMenuTitle(state, s, copy = copy),
                    shortPath(s.info?.cwd.orEmpty()),
                    mono = true,
                    lead = { AgentMarkLead(s.agent(), T.s17b, p.ink) },
                )
            },
            groups = listOf(
                buildList {
                    if (live) {
                        if (mode != null) add(
                            SheetAction(
                                copy(UiMessages.modesTitle()),
                                value = mode.mode?.let { copy(io.github.lewin671.remoteagent.core.sessionMode(it)) } ?: mode.text,
                                danger = mode.mode == Mode.FULL,
                                enabled = !forking,
                            ) {
                                sheet = 1
                            }
                        )
                        add(
                            SheetAction(
                                if (model.isEmpty()) copy(UiMessages.sessionSettings()) else copy(UiMessages.modelsTitle()),
                                value = model + if (effort.isNotEmpty()) " · $effort" else "",
                                enabled = !forking,
                            ) {
                                sheet = 2
                            }
                        )
                        pc?.let {
                            add(
                                SheetAction(copy(UiMessages.sessionContext()), value = copy(UiMessages.sessionContextUsed(percent = it)), warn = high, enabled = !forking) {
                                    sheet = 2
                                }
                            )
                        }
                    }
                },
                buildList {
                    add(SheetAction(copy(UiMessages.projectsAndroidSettings()), enabled = !forking && projectConfig(state, projectKey(s)) != null) {
                        sheet = 3
                    })
                    if (canRename) {
                        add(SheetAction(copy(UiMessages.sessionRename()), enabled = !forking) { rename = true })
                        if (agentInfo(s.agent()).forks) add(
                            SheetAction(
                                forkRow.text,
                                enabled = forkRow.enabled,
                                busy = forkRow.busy,
                                keepOpen = true,
                                note = forkRow.note,
                                noteError = forkRow.noteError,
                            ) {
                                if (forkRow.abandons) return@SheetAction Forks.abandon(fork)
                                forkScope.launch {
                                    val sid =
                                        Forks.send(
                                            fork,
                                            {
                                                remote.prepare(
                                                    s.machineId,
                                                    s.id,
                                                    remote.cmd {
                                                        forkSession =
                                                            ForkSessionRequest.newBuilder()
                                                                .setSessionId(s.id)
                                                                .build()
                                                    },
                                                )
                                            },
                                            { p, retry ->
                                                remote.forward(p, retryOfUnknown = retry)
                                            },
                                        ) ?: return@launch
                                    // The new session may not be in the list yet: its page waits
                                    // for it, as after starting one.
                                    menu = false
                                    nav.session(sid)
                                }
                            }
                        )
                    }
                    add(SheetAction(copy(UiMessages.sessionScheduleSend()), enabled = canRename && io.github.lewin671.remoteagent.core.scheduleSupported(state.machines[s.machineId])) { nav.newTask(s.id) })
                    if (running && canRename)
                        add(
                            SheetAction(copy(UiMessages.sessionInterruptTurn()), danger = true, enabled = !forking) {
                                act.run({
                                    remote.command(
                                        s.id,
                                        remote.cmd {
                                            interrupt =
                                                InterruptRequest.newBuilder()
                                                    .setSessionId(s.id)
                                                    .build()
                                        },
                                    )
                                })
                            }
                        )
                },
                buildList {
                    if (status != Status.EXITED && canRename)
                        add(
                            SheetAction(copy(UiMessages.sessionEndAction()), danger = true, enabled = !forking) {
                                confirmStop = true
                            }
                        )
                },
            ),
            onDismiss = {
                menu = false
                Forks.dismissError(fork)
            },
        )
    if (rename) RenameDialog(s, onClose = { rename = false })
    when (sheet) {
        1 -> ModeSheet(s, onClose = { sheet = 0 })
        2 -> ModelSheet(state, s, running, onClose = { sheet = 0 })
        3 -> projectConfig(state, projectKey(s))?.let { initial ->
            val key = projectKey(s)
            NewSettingsSheet(state, key, projectName(key, copy = copy), initial.machineId, s.agent(), onDone = { op ->
                if (op != null) PrefsChanges.run(remote, op)
                sheet = 0
            }, onClose = { sheet = 0 })
        }
    }
    if (act.error.isNotEmpty())
        Text(act.error, Modifier.padding(horizontal = 20.dp), color = p.red, style = T.s12)
    if (confirmStop)
        Confirm(
            copy(UiMessages.sessionEndTitle()),
            copy(UiMessages.sessionEndNote(agent = (agentName(s.agent(), copy = copy)).toString(), name = (machineLabel(state, s.machineId)).toString())),
            copy(UiMessages.sessionEnd()),
            danger = true,
            onDismiss = { confirmStop = false },
        ) {
            act.run({
                remote.command(
                    s.id,
                    remote.cmd {
                        stopSession = StopSessionRequest.newBuilder().setSessionId(s.id).build()
                    },
                )
            })
        }
}

@Composable
internal fun ChatPane(
    state: RemoteState,
    s: SessionView,
    status: Status,
    events: List<io.github.lewin671.remoteagent.proto.Event>,
    d: io.github.lewin671.remoteagent.core.Derived,
    loaded: Boolean,
    error: String?,
    fetchError: String?,
    modifier: Modifier,
    onTerminal: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val nav = LocalNav.current
    val remote = LocalRemote.current
    val machine = machineLabel(state, s.machineId)
    val cwd = s.info?.cwd.orEmpty()
    val home = state.machines[s.machineId]?.meta?.home.orEmpty()
    val outbox = Outbox.of(s.id)
    // Durable records regain their abort action before either removal or restore-to-input is offered.
    outbox.flatMap { it.images }.forEach { ImageDrafts.forSession(s.id).bindAbort(it, remote) }
    // Messages with images from an earlier run are followed until the CLI has them.
    LaunchedEffect(s.id, outbox.any { it.images.isNotEmpty() }) {
        if (outbox.any { it.images.isNotEmpty() }) Outbox.watch(remote, s.id)
    }
    val devices = state.devices
    val initialPrompt = Outbox.prompt(s.id)
    val initialId =
        if (initialPrompt != null)
            d.inputs.keys.firstOrNull { id -> outbox.none { it.requestId == id } }
        else null
    val startPrompt =
        pendingStartPrompt(initialPrompt, events, d, outbox.map { it.requestId }.toSet(), s.agent(), copy = copy)
    val projection =
        remember(
            remote,
            s.id,
            s.agent(),
            machine,
            cwd,
            home,
            outbox.size,
            devices,
            initialPrompt,
            initialId,
        ) {
            ChatProjectionWorker()
        }
    val rows = remember(remote, s.id) { mutableStateOf(ChatProjectionWorker.Rows.EMPTY) }
    var revealedRun by remember(s.id) { mutableStateOf(0L) }
    val requestedRun = ScheduleJump.requested[s.id]
    val reveal = requestedRun ?: revealedRun
    val currentReveal by rememberUpdatedState(reveal)
    val list = rememberLazyListState()
    // Follow new messages while the user is at the bottom; stop once they
    // drag away from it, until they come back. Our own jumps to the end
    // never turn it off, even while the layout has not caught up.
    val followState = remember { Follow() }
    val unseen = remember { NewMessages() }
    // Which rows are new, to play their entrance (§4 动效); none of the rows a session is opened with.
    val entrances = remember(s.id) { RowEntrances() }
    fun show(next: ChatProjectionWorker.Rows?) {
        if (next == null || next.version <= rows.value.version) return
        // History joining in front is not new, and the reader stays where they were.
        val joined = joinedRows(rows.value, next)
        entrances.rows(next.items, joined > 0)
        if (joined > 0 && !followState.on) {
            unseen.joined(joined)
            list.keepPlace(rows.value.items.map { it.key }, next.items.map { it.key }, joined)
        } else if (joined > 0 || rows.value.items.isEmpty() && next.items.isNotEmpty()) {
            // Following: the end from the first frame of the longer list, and of a history shown for the
            // first time. Left to the list, it would keep the index on a long history and show older
            // rows for a frame.
            list.requestScrollToItem(next.items.size + pendingInputs(d).size + outbox.size + 3)
        }
        rows.value = next
    }
    // Capture UI-owned outbox state before handing the immutable inputs to the worker.
    val texts = outbox.associate { it.requestId to it.text }
    LaunchedEffect(events, d, projection, texts, copy) {
        projection.update(
            events,
            d,
            machine,
            cwd,
            home,
            s.agent(),
            copy,
            { texts[it] ?: initialPrompt?.takeIf { _ -> it == initialId } },
        ) { o ->
            chatSource(
                o,
                state,
                remote.account.deviceId,
                machine,
                copy = copy,
            )
        }
        show(projection.rows { currentReveal })
    }
    LaunchedEffect(projection, reveal) { show(projection.rows { currentReveal }) }
    val items = rows.value.items
    val pendingInputs = pendingInputs(d)
    LaunchedEffect(d.inputs, outbox.size) {
        // The messages the CLI has recorded are the chat's own rows from here on.
        Outbox.settle(s.id, d.inputDetails)
        d.inputs.forEach { (id, status) ->
            if (
                status != InputStatus.INPUT_STATUS_QUEUED &&
                    status != InputStatus.INPUT_STATUS_HELD_BY_CLI
            )
                Cancels.states.remove(id)
        }
    }
    var allPending by remember { mutableStateOf(false) }
    val visiblePending = if (pendingInputs.size > 3 && !allPending) pendingInputs.take(2) else pendingInputs
    val sentHere = outbox.filter { outgoingShown(d.inputs[it.requestId]) }
    val scope = rememberCoroutineScope()
    val atBottom by remember { derivedStateOf { !list.canScrollForward } }
    val dragged by list.interactionSource.collectIsDraggedAsState()
    LaunchedEffect(dragged, atBottom) { followState.observe(dragged, atBottom) }
    val follow = followState.on
    val endIndex by rememberUpdatedState(items.size + visiblePending.size + sentHere.size + 3)
    val ime = WindowInsets.ime.getBottom(LocalDensity.current)
    // The dock appearing or going also moves the bottom.
    LaunchedEffect(
        followState.sends,
        items.size,
        outbox.size,
        visiblePending,
        sentHere.size,
        ime,
        d.approvals.size,
        d.questions.size,
        s.info?.planOpen,
        status,
    ) {
        // Past the end: the list stops at its last item, even before its first layout.
        if (follow && items.isNotEmpty())
            list.scrollToItem(endIndex)
        if (follow) unseen.saw(items.size)
        // A bubble opening or the dock coming moves the end for a few frames: stay on it, frame by frame.
        if (follow && items.isNotEmpty()) list.pinBottom({ endIndex }, { followState.on }, { list.moved() })
    }
    // Queued rows can arrive without a new projected chat item. Their entrance and action
    // line also change height after the initial scroll has settled. Observe every measured
    // layout, including the actual tail count, rather than only the viewport height.
    LaunchedEffect(list) {
        snapshotFlow { list.layoutInfo }.collect { layout ->
            if (followState.on && !list.isScrollInProgress && list.canScrollForward && layout.totalItemsCount > 0)
                list.requestScrollToItem(layout.totalItemsCount - 1)
        }
    }
    LaunchedEffect(atBottom) { if (atBottom) unseen.saw(items.size) }
    val jump = ScheduleJump.requested[s.id]
    LaunchedEffect(jump, items, loaded) {
        if (jump != null && loaded && items.isNotEmpty()) {
            val index = items.indexOfFirst { item -> item.key.removePrefix("e").toLongOrNull()?.let { it >= jump } == true || item is ChatItem.QuietScheduled && item.through >= jump }
            if (index >= 0) { revealedRun = jump; followState.observe(true, false); list.scrollToItem(index); ScheduleJump.requested.remove(s.id) }
        }
    }

    // The dock may take about 60% of what the session shows, not less than 240 dp.
    var shownHeight by remember { androidx.compose.runtime.mutableIntStateOf(0) }
    val density = LocalDensity.current
    val dockMax = with(density) { maxOf(240.dp, (shownHeight * 0.6f).toDp()) }

    // What waits under the chat: messages sent from here and messages queued behind the running turn.
    entrances.outgoing(sentHere.map { "out" + it.requestId } + visiblePending.map { "pending-${it.id}" })

    // A bottom-following history can place rows above its viewport while pages join.
    // Keep those rows inside the chat, below the history notices.
    Column(modifier.clipToBounds().onSizeChanged { shownHeight = it.height }) {
        Box(Modifier.weight(1f)) {
            val chatCtx =
                ChatContext(
                    s.id,
                    s.agent(),
                    d,
                    frozenAt = if (state.conn == ConnState.ONLINE) 0 else state.offlineSinceMs,
                    onImage = { seq, i -> nav.image(s.id, seq, i) },
                    sessions = state.sessions,
                    onSession = { nav.session(it) },
                )
            androidx.compose.runtime.CompositionLocalProvider(LocalChat provides chatCtx) {
                // Padding, not a leading item without a key: the list keeps its place by the
                // key of the first row in view, which history joining in front must not move.
                LazyColumn(
                    state = list,
                    modifier = Modifier.fillMaxSize(),
                    contentPadding = PaddingValues(top = 12.dp),
                ) {
                    if (!loaded && events.isEmpty() && startPrompt == null) item { Skeleton() }
                    if (startPrompt != null)
                        item(key = "start-prompt") {
                            MeBubble(
                                startPrompt.text,
                                caption = startPrompt.caption,
                                captionColor = if (!startPrompt.warning) p.ink2 else if (startPrompt.uncertain) p.orangeInk else p.red,
                                failed = startPrompt.warning && !startPrompt.uncertain,
                            )
                        }
                    items(items, key = { it.key }) {
                        val kind = remember(it.key) { entrances.take(it.key) }
                        Box(Modifier.entrance(kind)) {
                            ChatItemView(it, agentName(s.agent(), copy = copy), cwd, home, settling = kind == Entrance.SETTLE) { requestId, text ->
                                // A message the session ended before writing: text and images back.
                                val o = Outbox.find(s.id, requestId)
                                if (o != null) putBack(remote, s.id, o)
                                else {
                                    val accepted = d.accepted[requestId]
                                    val input = d.inputDetails[requestId]
                                    putBackReferences(remote, s.id, text,
                                        accepted?.files?.takeIf { it.isNotEmpty() } ?: input?.filesList.orEmpty(),
                                        accepted?.imageIds?.takeIf { it.isNotEmpty() } ?: input?.imageIdsList.orEmpty())
                                }
                            }
                        }
                    }
                    // Offline, the connection banner says the content may be stale and
                    // the next connection fetches again: a failed fetch adds nothing.
                    val shown = error ?: fetchError?.takeIf { state.conn == ConnState.ONLINE }
                    if (shown != null)
                        item {
                            Notice(
                                copy(UiMessages.sessionAndroidHistoryTailFailed(reason = shown)),
                                Modifier.padding(horizontal = ChatSide, vertical = 8.dp),
                                NoticeTone.ERROR,
                                action = uiText(UiMessages.commonRetry()),
                                onAction = { remote.retryEvents(s.id) },
                            )
                        }
                    items(visiblePending.size, key = { "pending-${visiblePending[it].id}" }) { i ->
                        Box(Modifier.entrance(remember { entrances.take("pending-${visiblePending[i].id}") })) {
                            PendingBubble(
                                s.id,
                                visiblePending[i],
                                events,
                                i + 1,
                                pendingInputs.size,
                                state,
                                remote,
                                if (status == Status.STARTING) agentName(s.agent(), copy = copy) else null,
                            )
                        }
                    }
                    if (pendingInputs.size > 3 && !allPending)
                        item {
                            TextAction(copy(UiMessages.queueMore(count = pendingInputs.size - 2)), { allPending = true }, Modifier.padding(horizontal = ChatSide), style = T.s13)
                        }
                    // Messages from this device, until the chat shows them.
                    items(sentHere, key = { "out" + it.requestId }) { o ->
                        Box(Modifier.entrance(remember { entrances.take("out" + o.requestId) })) {
                            OutgoingView(
                                s.id,
                                o,
                                d.inputs[o.requestId],
                                d.failures[o.requestId],
                                onTerminal,
                            )
                        }
                    }
                    item { Spacer(Modifier.height(8.dp)) }
                }
            }
            // The chat fades out under the segmented control and strips, as the content does under glass.
            Box(Modifier.fillMaxWidth().height(12.dp).background(androidx.compose.ui.graphics.Brush.verticalGradient(listOf(p.paper, p.paper.copy(alpha = 0f)))))
            val fresh = unseen.count(items.size)
            var shownFresh by remember { androidx.compose.runtime.mutableIntStateOf(0) }
            if (fresh > 0) shownFresh = fresh
            // Only for what came in while reading back; the list keeps following otherwise. It rises
            // in and fades out (§4 动效).
            Reveal(
                visible = !follow && !atBottom && fresh > 0,
                modifier = Modifier.align(Alignment.BottomCenter).padding(bottom = 8.dp),
                enter = Motion.appear(Motion.BASE, riseDp = Motion.BUBBLE_RISE_DP),
                exit = Motion.vanish(Motion.BASE, shrink = false),
            ) {
                GlassCapsule({ scope.launch { list.animateScrollToItem(list.layoutInfo.totalItemsCount - 1) } }) {
                    Spacer(Modifier.width(6.dp))
                    IconText(Lucide.ArrowDown, copy(UiMessages.sessionNewMessages(count = shownFresh)), p.ink, T.s13)
                }
            }
        }
        androidx.compose.runtime.CompositionLocalProvider(LocalDockMax provides dockMax) {
            if (s.info?.taskOwned == true) Hint(copy(UiMessages.sessionReplyMoves()), Modifier.padding(horizontal = ChatSide), inset = 0.dp)
            Bottom(
                state,
                s,
                status,
                d,
                onTerminal,
                onPlan = { nav.plan(s.id) },
                onSent = followState::sent,
            )
        }
    }
}

@Composable
private fun PendingBubble(
    sid: String,
    pending: io.github.lewin671.remoteagent.core.PendingInput,
    events: List<io.github.lewin671.remoteagent.proto.Event>,
    index: Int,
    total: Int,
    state: RemoteState,
    remote: io.github.lewin671.remoteagent.core.Remote,
    starting: String?,
) {
    val copy = uiText()
    if (
        io.github.lewin671.remoteagent.core.isSkeleton(pending.id) ||
            io.github.lewin671.remoteagent.core.isSkeleton(pending.text)
    ) {
        Text(copy(UiMessages.sessionSkeleton()), style = T.s13, color = pal.ink2, modifier = Modifier.padding(horizontal = ChatSide))
        return
    }
    val p = pal
    var expanded by remember(pending.id) { mutableStateOf(false) }
    val cancel = Cancels.states[pending.id]
    val resume = rememberAction()
    val online = state.conn == ConnState.ONLINE
    val label = pendingInputLabel(pending, starting, cancel, resume.running, resume.error.isNotEmpty(), copy = copy)
    val origin =
        chatSource(
            pending.origin,
            state,
            remote.account.deviceId,
            "",
            copy = copy,
        )
    // A message waiting its turn: a thin faint outline and no fill, its text in ink2 (design-system §6 输入框).
    Column(
        Modifier.fillMaxWidth().padding(horizontal = ChatSide, vertical = 6.dp),
        horizontalAlignment = Alignment.End,
    ) {
        Column(
            Modifier.fillMaxWidth(.84f).wrapContentWidth(Alignment.End)
                .clip(Radii.R22)
                .border(1.dp, p.faint, Radii.R22)
                .clickable { expanded = !expanded }
                .padding(horizontal = 16.dp, vertical = 10.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            if (pending.text.isNotEmpty() || pending.files.isEmpty()) Text(
                pending.text.ifEmpty { copy(UiMessages.imageAndroidBubble()) },
                style = T.s17,
                color = p.ink2,
                maxLines = if (expanded) Int.MAX_VALUE else 4,
                overflow = TextOverflow.Ellipsis,
            )
            FileChips(pending.files)
        }
        Row(
            Modifier.fillMaxWidth(.84f).heightIn(min = 36.dp).padding(start = 8.dp, end = 8.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.End,
        ) {
            Text(
                scheduledPendingCaption(pending, events, label, copy = copy) ?: pendingInputCaption(label, index, total, origin, !pending.paused && !pending.held && cancel == null, copy = copy),
                style = T.s13,
                color = if (cancel is Cancels.State.Failed) p.red else p.ink2,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                textAlign = androidx.compose.ui.text.style.TextAlign.End,
                modifier = Modifier.weight(1f, fill = false),
            )
            if (pending.paused && cancel != Cancels.State.Canceling)
                TextAction(
                    if (resume.error.isNotEmpty()) uiText(UiMessages.commonRetry()) else copy(UiMessages.composerSend()),
                    {
                        resume.run({
                            remote.command(
                                sid,
                                remote.cmd {
                                    resumeInput =
                                        ResumeInputRequest.newBuilder()
                                            .setSessionId(sid)
                                            .setRequestId(pending.id)
                                            .build()
                                },
                            )
                        })
                    },
                    enabled = online && !resume.running,
                    modifier = Modifier.padding(start = 8.dp),
                    style = T.s13,
                    alignment = Alignment.Center,
                )
            if (cancel != Cancels.State.Canceling)
                TextAction(
                    if (cancel is Cancels.State.Failed) uiText(UiMessages.commonRetry()) else uiText(UiMessages.commonCancel()),
                    { Cancels.cancel(remote, sid, pending.id) },
                    enabled = online,
                    modifier = Modifier.padding(start = 8.dp),
                    style = T.s13,
                    alignment = Alignment.Center,
                )
        }
    }
}

@Composable
private fun OutgoingView(
    sid: String,
    o: Outgoing,
    input: InputStatus?,
    failure: String?,
    onTerminal: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val remote = LocalRemote.current
    val agent =
        remote.state.value.sessions[sid]?.agent()
            ?: DEFAULT_AGENT
    val feedback = when (input) {
        InputStatus.INPUT_STATUS_FAILED, InputStatus.INPUT_STATUS_NEEDS_TERMINAL, InputStatus.INPUT_STATUS_UNCONFIRMED -> inputFeedback(input, failure.orEmpty(), agent)
        else -> deliveryFailure(o.failureDisplay?.resolve(copy) ?: o.error, o.state == Outgoing.State.UNKNOWN, kind = o.failureKind, copy = copy)
    }
    val failed = input == InputStatus.INPUT_STATUS_FAILED || (input == null && o.state == Outgoing.State.FAILED)
    val links = mutableListOf<Pair<String, () -> Unit>>()
    var sending = false
    val (caption, color) =
        when {
            // Nothing was submitted and the terminal is as it was.
            input == InputStatus.INPUT_STATUS_FAILED -> {
                links +=
                    copy(UiMessages.composerRestore()) to
                        {
                            putBack(remote, sid, o)
                            Unit
                        }
                feedback.text to p.red
            }
            // Not submitted, and part of it may still be in the input box.
            input == InputStatus.INPUT_STATUS_NEEDS_TERMINAL -> {
                links += copy(UiMessages.sessionGoTerminal()) to onTerminal
                if (o.hasLocalAttachments || (o.imageIds.isEmpty() && o.fileIds.isEmpty() && o.text.isNotEmpty())) links += copy(UiMessages.composerRestore()) to { putBack(remote, sid, o); Unit }
                feedback.text to p.orangeInk
            }
            input == InputStatus.INPUT_STATUS_UNCONFIRMED -> {
                // Its images are kept until then; the user decides when to let go.
                links +=
                    copy(UiMessages.sessionDismissNotice()) to
                        {
                            Outbox.remove(sid, o)
                            o.images.forEach { it.release(abort = false) }
                        }
                feedback.text to p.orangeInk
            }
            o.state == Outgoing.State.SENDING -> {
                sending = true
                copy(UiMessages.queueResuming()) to p.ink2
            }
            o.state == Outgoing.State.SENT -> copy(UiMessages.sessionDelivered()) to p.ink2
            else -> {
                if (o.canPutBack)
                    links +=
                        copy(UiMessages.composerRestore()) to
                            {
                                putBack(remote, sid, o)
                                Unit
                            }
                if (!o.missingImages)
                    links += uiText(UiMessages.commonRetry()) to { Outbox.deliver(RaApp.instance.scope, remote, sid, o) }
                links +=
                    copy(UiMessages.sessionDismissNotice()) to
                        {
                            Outbox.remove(sid, o)
                            o.images.forEach { it.release(abort = false) }
                        }
                feedback.text to if (feedback.uncertain) p.orangeInk else p.red
            }
        }
    Column(Modifier.fillMaxWidth().padding(horizontal = ChatSide)) {
        FileChips(o.images.filter { it.isFile }.map { io.github.lewin671.remoteagent.proto.SentFile.newBuilder().setFileId(it.fileId.orEmpty()).setName(it.fileName!!).setSizeBytes(it.fileBytes).build() })
        if (o.images.any { !it.isFile })
            Row(
                Modifier.align(Alignment.End).padding(top = 8.dp),
                horizontalArrangement = Arrangement.spacedBy(4.dp),
            ) {
                for (img in o.images.filterNot { it.isFile }.take(4)) img.thumb?.let {
                    androidx.compose.foundation.Image(
                        it,
                        null,
                        Modifier.size(64.dp).clip(Radii.R14),
                        contentScale = androidx.compose.ui.layout.ContentScale.Crop,
                    )
                }
            }
        MeBubble(
            o.text.ifEmpty { null },
            caption = caption,
            captionColor = color,
            captionIcon = if (sending) Lucide.LoaderCircle else null,
            failed = failed,
            links = links,
            padded = false,
            animated = true,
        )
    }
}

/** The dock, the queue and the composer, or a status card when nothing can be sent. */
@Composable
private fun Bottom(
    state: RemoteState,
    s: SessionView,
    status: Status,
    d: io.github.lewin671.remoteagent.core.Derived,
    onTerminal: () -> Unit,
    onPlan: () -> Unit,
    onSent: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val remote = LocalRemote.current
    val m = state.machines[s.machineId]
    if (status == Status.EXITED) {
        val act = rememberAction()
        EndCard(
            if (act.running) copy(UiMessages.sessionResuming()) else copy(UiMessages.sessionEnded()),
            buildString {
                append(exitReason(s.info?.statusDetail.orEmpty(), d.scheduledClosed, d.idleClosed, copy = copy))
                if (state.unlinked) append(copy(UiMessages.sessionConnectResume()))
                else if (m?.online == true)
                    append(copy(UiMessages.sessionResumeNote(name = (machineLabel(state, s.machineId)).toString())))
                else append(copy(UiMessages.sessionOfflineResume(name = (machineLabel(state, s.machineId)).toString())))
            },
            running = act.running,
        ) {
            if (m?.online == true && !state.unlinked && !act.running)
                PillButton(
                    if (act.error.isNotEmpty()) copy(UiMessages.sessionTryAgain()) else copy(UiMessages.sessionResume()),
                    {
                        act.run({
                            remote.command(
                                s.id,
                                remote.cmd {
                                    resumeSession =
                                        ResumeSessionRequest.newBuilder().setSessionId(s.id).build()
                                },
                            )
                        })
                    },
                    Modifier.fillMaxWidth().padding(top = 14.dp),
                    if (act.error.isNotEmpty()) ButtonKind.SECONDARY else ButtonKind.INK,
                    enabled = state.conn == ConnState.ONLINE,
                )
            if (act.error.isNotEmpty())
                Text(
                    copy(UiMessages.sessionAndroidResumeFailed(reason = act.error)),
                    color = p.red,
                    style = T.s13,
                    modifier = Modifier.padding(top = 8.dp),
                )
        }
        return
    }
    if (status == Status.OFFLINE) {
        val t = now()
        EndCard(
            copy(UiMessages.newSessionMachineOffline(name = (machineLabel(state, s.machineId)).toString())),
            copy(UiMessages.sessionOfflineNote(lastSeen = (if ((m?.lastSeenMs ?: 0) > 0) copy(UiMessages.sessionDisconnectedAgo(io.github.lewin671.remoteagent.core.listWhen(m!!.lastSeenMs, t).resolve(copy))) else "").toString())),
        ) {}
        return
    }
    val dockItems = buildList {
        d.approvals.values.forEach { add(DockItem.Approval(it)) }
        d.questions.values.forEach { add(DockItem.Question(it)) }
        if (s.info?.planOpen == true && d.plan != null) add(DockItem.Plan(d.plan!!.text))
        if (status == Status.POPUP) add(DockItem.Popup(s.info?.statusDetail.orEmpty()))
    }
    val first = dockItems.firstOrNull()
    // What the dock last showed stays while it goes, so it has something to fade out and close with.
    val shown = remember { androidx.compose.runtime.mutableStateOf<DockItem?>(null) }
    if (first != null) shown.value = first
    // From the top of the message box it rises 12 dp and fades in while the chat gives up the room; on
    // its way out it fades and closes (§4 动效, 停靠卡). The chat's end stays in view meanwhile.
    Reveal(
        visible = first != null,
        enter = Motion.appear(Motion.BASE, riseDp = Motion.DOCK_RISE_DP, grow = true),
        exit = Motion.vanish(Motion.BASE),
    ) {
        when (val item = shown.value) {
            is DockItem.Approval -> ApprovalDock(s, item.a, (dockItems.size - 1).coerceAtLeast(0))
            is DockItem.Question -> QuestionDock(s, item.q, (dockItems.size - 1).coerceAtLeast(0), onTerminal)
            is DockItem.Plan -> PlanDock(s, item.text, (dockItems.size - 1).coerceAtLeast(0), onPlan)
            is DockItem.Popup -> PopupDock(s, item.title, (dockItems.size - 1).coerceAtLeast(0), onTerminal)
            null -> {}
        }
    }
    Composer(state, s, d, status, onSent = onSent, onFocus = {})
}

/**
 * In place of the message box when nothing can be sent: the session ended,
 * or its computer is away. A gbg card, radius 28 (design-system §6 输入框):
 * why first, then what can be done.
 */
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
                .padding(18.dp)
        ) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (running) Spinner(p.ink2, size = 15.dp) else StatusMark(MarkKind.OFF)
                Text(title, style = T.s17b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            Text(text, style = T.s15, color = p.ink2, modifier = Modifier.padding(top = 6.dp))
            actions()
        }
    }
}

/**
 * The placeholder of the message box: it follows what the message will do (wait for the answer, go to
 * the plan), never what is running, so it does not change while a turn's tools start and stop.
 */
internal fun composerPlaceholder(questionPending: Boolean, planOpen: Boolean, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String =
    when {
        questionPending -> copy(UiMessages.composerQueueAfterAnswer())
        planOpen -> copy(UiMessages.composerPlanPlaceholder())
        else -> copy(UiMessages.composerMessage())
    }

/**
 * The message box: the plus adds images, the text from one line up to about six, and send. The
 * mode, the model and the context are in the more menu of the navigation bar. Without the server
 * connection the draft stays and sending waits.
 */
@Composable
internal fun Composer(
    state: RemoteState,
    s: SessionView,
    d: io.github.lewin671.remoteagent.core.Derived,
    status: Status,
    onSent: () -> Unit = {},
    onFocus: (Boolean) -> Unit,
) {
    val copy = uiText()
    val p = pal
    val remote = LocalRemote.current
    val interruptAction = rememberAction()
    val view = androidx.compose.ui.platform.LocalView.current
    var text by Outbox.draft(s.id)
    val online = state.conn == ConnState.ONLINE
    val running = status == Status.RUNNING || d.running
    val images = ImageDrafts.forSession(s.id)
    val keeping = Outbox.keeping(s.id)
    val machine = state.machines[s.machineId]
    val maxImg = maxImages(machine)
    val blocked = uploadBlocked(machine, online, machineLabel(state, s.machineId), copy = copy)
    // Images picked before the machine went away go there again once it is back.
    RefreshDraftUploads(images, remote, s.machineId, blocked)
    val pick = rememberImagePicker(images, remote, s.machineId, blocked, maxImg, videos = io.github.lewin671.remoteagent.core.filesSupported(machine))
    fun send(mode: SendMode = SendMode.SEND_MODE_STEER) {
        val msg = text.trim()
        if (images.restoring || images.readingFiles || fileDraftBlocked(images, machine, copy = copy) != null || (msg.isEmpty() && images.images.isEmpty())) return
        images.sendAllTo(remote, s.machineId)
        // Every image must be on the computer first: never a message with some of them.
        if (!images.ready(s.machineId) && images.images.isNotEmpty()) return
        val picked = images.images.toList()
        // The one light tap of a message going out.
        Motion.tap(view)
        if (picked.isEmpty()) {
            text = ""
            onSent()
            Outbox.send(RaApp.instance.scope, remote, s.id, msg, mode)
            return
        }
        // Kept on disk with the message until the CLI has it; sent
        // only once that worked, and only then out of the draft.
        Outbox.sendWithImages(
            RaApp.instance.scope,
            remote,
            s.id,
            msg,
            images.ids(s.machineId),
            picked,
            images.original,
            mode,
            fileIds = images.fileIds(s.machineId),
        ) { kept ->
            kept
                .onSuccess {
                    if (text.trim() == msg) text = ""
                    images.detach(picked)
                    // The message is in the chat now: back to the end to see it, as for a text one.
                    onSent()
                }
                .onFailure {
                    images.notice = copy(UiMessages.composerAndroidNotSent(reason = (displayError(it)).toString()))
                    images.sendAllTo(remote, s.machineId)
                }
        }
    }
    ComposeBox(
        text,
        { text = it },
        placeholder = composerPlaceholder(d.questions.isNotEmpty(), s.info?.planOpen == true, copy = copy),
        canSend =
            !images.restoring && !images.readingFiles && fileDraftBlocked(images, machine, copy = copy) == null &&
                !keeping &&
                online &&
                (text.isNotBlank() || images.images.isNotEmpty()) &&
                (images.images.isEmpty() || images.ready(s.machineId)),
        onSend = { send() },
        onPlus = pick,
        onFile = rememberDocumentPicker(images, remote, s.machineId),
        plusEnabled = !images.restoring && (blocked == null || io.github.lewin671.remoteagent.core.filesSupported(machine) && online && machine?.online == true),
        onQueue = if (running) ({ send(SendMode.SEND_MODE_QUEUE) }) else null,
        onInterrupt =
            if (running)
                ({
                    interruptAction.run({
                        remote.command(
                            s.id,
                            remote.cmd {
                                interrupt = InterruptRequest.newBuilder().setSessionId(s.id).build()
                            },
                        )
                    })
                })
            else null,
        canInterrupt = online && !interruptAction.running,
        hasDraft = text.isNotBlank() || images.images.isNotEmpty(),
        onFocus = onFocus,
        top = { ImageStrip(images, remote, s.machineId, maxImg) },
    )
    if (interruptAction.error.isNotEmpty())
        Notice(copy(UiMessages.sessionAndroidInterruptFailed(reason = interruptAction.error)), Modifier.padding(horizontal = 12.dp).padding(bottom = 8.dp), NoticeTone.ERROR)
    if (text.trimStart().startsWith("/"))
        Text(
            copy(UiMessages.composerSlashNote()),
            Modifier.padding(horizontal = ChatSide).padding(bottom = 8.dp),
            style = T.s13,
            color = p.orangeInk,
        )
}

/** Grey bars while the history loads. */
@Composable
fun Skeleton() {
    val p = pal
    Column(Modifier.padding(horizontal = ChatSide, vertical = 8.dp)) {
        for (w in listOf(0.7f, 0.9f, 0.5f)) Box(
            Modifier.padding(vertical = 7.dp)
                .fillMaxWidth(w)
                .height(12.dp)
                .clip(RoundedCornerShape(6.dp))
                .background(p.fill2)
        )
    }
}

/**
 * Whether the chat follows new messages. Only the user turns it off, by dragging away from the
 * bottom; reaching the bottom turns it back on. A scroll the app makes to the end ends before the
 * list has laid out the new position, so judging by it would stop following on a long history.
 */
class Follow {
    var on by mutableStateOf(true)
        private set

    /** Counts the messages the user sent from this view; a change is the cue to go to the end. */
    var sends by mutableIntStateOf(0)
        private set

    fun observe(userDragging: Boolean, atBottom: Boolean) {
        if (atBottom) on = true else if (userDragging) on = false
    }

    /**
     * The user sent a message: sending is asking to see the answer, so the chat goes back to the end
     * and follows again, even from far up. Messages that arrive from elsewhere never do this.
     */
    fun sent() {
        on = true
        sends++
    }
}

/**
 * What the「新消息」button counts: rows added at the end since the reader was last at the
 * bottom. Rows the history joins in front are counted as seen (iOS `ScrollState.prepended`).
 */
class NewMessages {
    private var seen by mutableIntStateOf(0)

    fun saw(rows: Int) {
        seen = rows
    }

    /** [rows] came in front while the reader was not following. */
    fun joined(rows: Int) {
        seen += rows
    }

    fun count(rows: Int) = maxOf(0, rows - seen)
}

/**
 * How many of [after]'s rows came in front of [before]'s when the history before them joined:
 * the shown events then start earlier. 0 when nothing joined.
 */
fun joinedRows(before: ChatProjectionWorker.Rows, after: ChatProjectionWorker.Rows): Int {
    if (before.items.isEmpty()) return 0
    val from = after.events.firstOrNull()?.seq ?: 1
    val shownFrom = before.events.firstOrNull()?.seq ?: 1
    if (java.lang.Long.compareUnsigned(from, shownFrom) >= 0) return 0
    return rowsInFront(before.items.map { it.key }, after.items.map { it.key })
}

/**
 * Rows of [after] in front of those of [before], by the last row both have: what [after] adds
 * behind that row is not in front (iOS `rowsInFront`).
 */
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

/**
 * Keeps the row at the top of the list in view where it is while [joined] rows come in front.
 * The list finds its first row by key only a short way from where it was, so on a long history
 * it would keep the index and show other rows. Call before the new rows are shown.
 */
fun androidx.compose.foundation.lazy.LazyListState.keepPlace(
    before: List<Any>,
    after: List<Any>,
    joined: Int,
) {
    val index = firstVisibleItemIndex
    val key = layoutInfo.visibleItemsInfo.firstOrNull { it.index == index }?.key
    val was = if (key == null) -1 else before.indexOf(key)
    val now = if (was < 0) -1 else after.indexOf(key)
    requestScrollToItem(index + if (now >= 0) now - was else joined, firstVisibleItemScrollOffset)
}

/**
 * Whether the session's terminal can be shown now. An ended session and one whose computer is away
 * have none: the page shows the chat meanwhile and leaves the view picked on this device as it was
 * (design-system §6 「视图切换」).
 */
fun terminalAvailable(status: Status): Boolean = status != Status.EXITED && status != Status.OFFLINE

// The daemon's status detail when a client-created session's login shell
// could not start the CLI (internal/daemon/shell.go); the timeout one adds
// the second phrase.
private const val LOGIN_SHELL_DETAIL = "the login shell"
private const val LOGIN_SHELL_TIMEOUT_DETAIL = "did not start the CLI within"

// Why the session ended, in plain words. A login shell that could not start
// the CLI says so in the daemon's own words (they name the command or the
// timeout), with what the user can do about it.
fun exitReason(detail: String, scheduledClosed: Boolean = false, idleClosed: Boolean = false, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String {
    if (detail == "stopped" && scheduledClosed) return copy(UiMessages.sessionScheduledClosed())
    if (detail == "stopped" && idleClosed) return copy(UiMessages.sessionIdleClosed())
    if (detail == "stopped") return copy(UiMessages.sessionManuallyClosed())
    if (detail.startsWith(LOGIN_SHELL_DETAIL)) {
        return if (detail.contains(LOGIN_SHELL_TIMEOUT_DETAIL)) copy(UiMessages.sessionShellTimeout(detail = detail))
        else copy(UiMessages.sessionShellFailed(detail = detail))
    }
    return copy(UiMessages.sessionTmuxMissing())
}

/**
 * Forks of sessions asked from here, per session, for this run. One whose outcome is unknown is
 * sent again unchanged, the same request (architecture §10): the daemon answers it from its log
 * with the session it already made, instead of forking the session a second time. They outlive the
 * menu and the page, which may both be left meanwhile.
 */
object Forks {
    class Fork {
        val attempt = io.github.lewin671.remoteagent.core.Attempt()
        /** It is on its way to the computer. */
        var running by mutableStateOf(false)
            internal set

        /** Why the last try did not make a session; empty while one is running and once one did. */
        var error by mutableStateOf("")
            internal set

        /** The outcome is unknown: only the same sealed command is sent again. */
        var frozen by mutableStateOf(false)
            internal set

        /** The unknown fork expired and is not sent again. */
        var expired by mutableStateOf(false)
            internal set
    }

    private val bySession = HashMap<String, Fork>()

    fun of(sid: String): Fork = bySession.getOrPut(sid) { Fork() }

    fun clear() = bySession.clear()

    /**
     * Forks, or sends the earlier fork again while its outcome is unknown. One try at a time.
     * Returns the new session's ID, or null when none is known to have been made; [f] says why.
     */
    suspend fun send(
        f: Fork,
        prepare: () -> io.github.lewin671.remoteagent.core.PreparedCommand, forward:
            suspend (
                io.github.lewin671.remoteagent.core.PreparedCommand, Boolean,
            ) -> io.github.lewin671.remoteagent.proto.CommandResult?): String? {
        if (f.running) return null
        f.running = true
        f.error = ""
        try {
            val retry = f.attempt.frozen
            val result = forward(f.attempt.next(prepare), retry)
            f.attempt.settle()
            return io.github.lewin671.remoteagent.core.forkedSession(result)
                ?: throw io.github.lewin671.remoteagent.core.CommandException("internal", sessionText(UiMessages.newSessionUnexpectedReply()))
        } catch (e: kotlinx.coroutines.CancellationException) {
            // The page went away with the request on its way: whether the computer got it is not
            // known.
            f.error =
                displayError(
                    f.attempt.settle(
                        io.github.lewin671.remoteagent.core.CommandException(
                            io.github.lewin671.remoteagent.core.CommandException.UNKNOWN,
                            sessionText(UiMessages.sessionAndroidExecutionUnknown()),
                        )
                    ) ?: e
                )
            throw e
        } catch (e: Exception) {
            f.error = displayError(f.attempt.settle(e) ?: e)
            return null
        } finally {
            f.frozen = f.attempt.frozen
            f.expired = f.attempt.expired
            f.running = false
        }
    }

    /** Gives up on a fork that expired with its outcome unknown: the next one is a new request. */
    fun abandon(f: Fork) {
        if (f.running) return
        f.attempt.abandon()
        f.error = ""
        f.frozen = false
        f.expired = false
    }

    /**
     * The menu was closed: a fork that surely did not happen is not said again. One that may have
     * is kept.
     */
    fun dismissError(f: Fork) {
        if (!f.frozen) f.error = ""
    }
}

/**
 * 分叉会话 in a session's menu: what the row says, and whether a tap gives up the last fork instead of
 * forking.
 */
data class ForkAction(
    val text: String = sessionText(UiMessages.sessionFork()),
    val note: String = "",
    /** The note is something that went wrong, not when the row can be used. */
    val noteError: Boolean = false,
    val enabled: Boolean = true,
    val busy: Boolean = false,
    val abandons: Boolean = false,
)

/**
 * [blocked]: why the session cannot be forked now ([forkBlocked]). The rest is how a fork asked
 * from here stands ([Forks.Fork]). A fork that may have happened is only ever sent again as it was;
 * once that request has expired the row gives it up, and only the tap after that asks for a new
 * fork.
 */
fun forkAction(
    blocked: String?,
    running: Boolean,
    error: String,
    frozen: Boolean,
    expired: Boolean,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText,
): ForkAction =
    when {
        running -> ForkAction(text = copy(UiMessages.sessionForking()), busy = true)
        blocked != null -> ForkAction(text = copy(UiMessages.sessionFork()), note = blocked, enabled = false)
        expired ->
            ForkAction(
                text = copy(UiMessages.sessionAbandonFork()),
                note = copy(UiMessages.sessionForkExpired()),
                noteError = true,
                abandons = true,
            )
        frozen -> ForkAction(text = copy(UiMessages.sessionFork()), note = copy(UiMessages.sessionForkUnknown()), noteError = true)
        else -> ForkAction(text = copy(UiMessages.sessionFork()), note = if (error.isNotEmpty()) copy(UiMessages.sessionForkFailed(reason = error)) else "", noteError = true)
    }

/** Sessions whose terminal another page asked to show. */
object OpenTerminal {
    val requested = androidx.compose.runtime.mutableStateMapOf<String, Boolean>()
}

/**
 * A message that was not submitted goes back into the box, text and images, for the user to send
 * again as a new message. Its images go to the computer again: the daemon may have used up the old
 * uploads. When they do not fit next to images already in the box nothing moves, and the box says
 * why.
 */
internal fun putBack(
    remote: io.github.lewin671.remoteagent.core.Remote,
    sid: String,
    o: Outgoing,
): Boolean {
    val d = ImageDrafts.forSession(sid)
    if (d.awaitingRestore) { d.whenRestored { putBack(remote, sid, o) }; return true }
    val s = remote.state.value.sessions[sid]
    if (!canRestoreFiles(remote, s?.machineId, o.fileIds.isNotEmpty() || o.images.any { it.isFile }, d)) return false
    val max = maxImages(s?.let { remote.state.value.machines[it.machineId] })
    // Restored outbox entries have their durable transfer identity but no live abort closure.
    o.images.forEach { d.bindAbort(it, remote) }
    if (o.images.isNotEmpty() && !d.attach(o.images, max, o.original, io.github.lewin671.remoteagent.core.maxFiles(s?.let { remote.state.value.machines[it.machineId] }))) return false
    val cur = Outbox.draft(sid).value
    Outbox.draft(sid).value = if (cur.isBlank()) o.text else cur.trimEnd() + "\n" + o.text
    Outbox.takeBack(sid, o, d)
    s?.let { d.sendAllTo(remote, it.machineId) }
    return true
}

/** Check before changing text, transfers or the failed record, including remote-only history. */
private fun canRestoreFiles(remote: Remote, machine: String?, hasFiles: Boolean, draft: ImageDraft): Boolean =
    try { remote.requireFileSupport(machine, hasFiles); true }
    catch (e: io.github.lewin671.remoteagent.core.CommandException) { draft.notice = displayError(e, remote.state.value); false }

internal fun putBackReferences(
    remote: Remote, sid: String, text: String,
    files: List<io.github.lewin671.remoteagent.proto.SentFile>, imageIds: List<String>,
): Boolean {
    val tray = ImageDrafts.forSession(sid)
    if (tray.awaitingRestore) { tray.whenRestored { putBackReferences(remote, sid, text, files, imageIds) }; return true }
    val target = remote.state.value.sessions[sid]?.machineId
    if (!canRestoreFiles(remote, target, files.isNotEmpty(), tray)) return false
    val machine = remote.state.value.machines[target]
    if (!tray.restoreReferences(files, imageIds, maxImages(machine), io.github.lewin671.remoteagent.core.maxFiles(machine))) return false
    Outbox.draft(sid).value = text
    return true
}

/** What a session's ⋯ menu is about: the CLI, the project, and the machine only where the account has more than one (design-system §6). */
internal fun sessionMenuTitle(state: RemoteState, s: SessionView, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String =
    listOfNotNull(agentName(s.agent(), copy = copy), projectName(projectKey(s), copy = copy), machineLabel(state, s.machineId).takeIf { state.machines.size > 1 }).joinToString(" · ")
