package io.github.lewin671.remoteagent.ui

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.ime
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilledIconButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.foundation.interaction.collectIsDraggedAsState
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.Status
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.contextPercent
import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.core.modeOf
import io.github.lewin671.remoteagent.core.projectKey
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.shortPath
import io.github.lewin671.remoteagent.core.titleOf
import io.github.lewin671.remoteagent.core.whenText
import io.github.lewin671.remoteagent.core.pendingInputs
import io.github.lewin671.remoteagent.proto.CancelInputRequest
import io.github.lewin671.remoteagent.proto.ForkSessionRequest
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.SendMode
import io.github.lewin671.remoteagent.proto.ResumeInputRequest
import io.github.lewin671.remoteagent.proto.InterruptRequest
import io.github.lewin671.remoteagent.proto.ResumeSessionRequest
import io.github.lewin671.remoteagent.proto.StopSessionRequest
import io.github.lewin671.remoteagent.term.TerminalPane
import kotlinx.coroutines.launch

@Composable
fun SessionScreen(sid: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val s = state.sessions[sid]
    val p = pal
    // Not held here: the listing comes a page at a time, so it is asked for by its ID, and
    // only the server saying it has none makes it missing (architecture §8 「按 ID 取」).
    val gone = sessionGone(sid)
    if (s == null) {
        Column(Modifier.fillMaxSize().padding(24.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
            if (gone) {
                Text("找不到这个会话", style = MaterialTheme.typography.titleMedium)
                TextButton(onClick = nav.back) { Text("返回") }
            } else {
                CircularProgressIndicator()
                Text(if (state.conn == ConnState.ONLINE) "读取中…" else "连上服务器后读取这个会话", color = p.muted, modifier = Modifier.padding(top = 12.dp))
            }
        }
        return
    }
    val events by remote.events(sid).collectAsState()
    val d = rememberDerived(events.events, s?.agent() ?: io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX)
    val status = statusIn(state, s)
    // The view last picked on this device; every session opens in it.
    val context = androidx.compose.ui.platform.LocalContext.current
    val viewPrefs = remember(context) { context.getSharedPreferences("session-view", android.content.Context.MODE_PRIVATE) }
    var tab by rememberSaveable { mutableStateOf(viewPrefs.getInt("tab", 0)) }
    BackHandler(enabled = tab == 1) { tab = 0 }
    // Another page (a tool's details) asked for the terminal.
    val wantTerminal = OpenTerminal.requested[sid] == true
    LaunchedEffect(wantTerminal) {
        if (wantTerminal) {
            tab = 1
            OpenTerminal.requested.remove(sid)
        }
    }

    val term = tab == 1 && status != Status.EXITED
    Column(Modifier.fillMaxSize().background(if (term) TermColors.bg else p.card)) {
        SessionTopBar(state, s, status, d.running || status == Status.RUNNING, io.github.lewin671.remoteagent.core.sentMessage(s, events.events), dark = term)
        ConnBanner(state)
        SessionTabs(if (term) 1 else 0, status != Status.EXITED, status == Status.POPUP, dark = term) {
            tab = it
            viewPrefs.edit().putInt("tab", it).apply()
        }
        if (tab == 1 && status != Status.EXITED) {
            TerminalPane(remote, sid, sessionRunning = status != Status.EXITED, modifier = Modifier.weight(1f).fillMaxWidth())
        } else {
            ChatPane(state, s, status, events.events, d, events.loaded, events.error, Modifier.weight(1f), onTerminal = { tab = 1 })
        }
    }
}

/**
 * Back, the title, the status and mode, and more: the session's mode, model and context (an orange dot when it is high), 重命名, 分叉会话, 中断本轮 and 结束会话….
 * [sent]: a message was sent in the session, which a fork needs.
 */
@Composable
private fun SessionTopBar(state: RemoteState, s: SessionView, status: Status, running: Boolean, sent: Boolean, dark: Boolean) {
    val p = pal
    val nav = LocalNav.current
    val remote = LocalRemote.current
    var menu by remember { mutableStateOf(false) }
    var confirmStop by remember { mutableStateOf(false) }
    var projectSettings by remember { mutableStateOf(false) }
    var rename by remember { mutableStateOf(false) }
    var sheet by remember { mutableStateOf(0) } // 1 mode, 2 model
    val act = rememberAction()
    val live = status != Status.EXITED && status != Status.OFFLINE
    // The title is kept on the computer: an ended session can be renamed too, but only while the computer is reachable.
    val canRename = state.conn == ConnState.ONLINE && machineOf(state, s)?.online == true
    // A fork starts at once, in the menu: its row shows the progress, and a failure under it.
    val fork = rememberAction()
    val forking = fork.running
    val forkBlock = io.github.lewin671.remoteagent.core.forkBlocked(status, running, sent)
    // Claude Code records full model IDs; show the alias the list offers.
    var models by remember(s.machineId) { mutableStateOf<io.github.lewin671.remoteagent.proto.ListModelsResponse?>(null) }
    LaunchedEffect(s.machineId, s.agent()) {
        if (s.agent() == io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE) runCatching { models = remote.listModels(s.machineId, s.agent()) }
    }
    val raw = s.info?.model.orEmpty()
    val model = models?.let { ms -> io.github.lewin671.remoteagent.core.findModel(ms.modelsList, raw) { it.slug }?.displayName } ?: raw
    val effort = s.info?.effort.orEmpty()
    // Unknown context is left out, never shown as 0%.
    val pc = if (live) contextPercent(s) else null
    val high = (pc ?: 0) > 80
    val mode = if (status == Status.EXITED) null else io.github.lewin671.remoteagent.core.modeLabel(s)
    // A starting CLI has not reported its mode yet.
    val subtitle = status.text + if (status == Status.EXITED || (status == Status.STARTING && mode == null)) "" else " · ${mode?.text ?: "审批：CLI 配置（未读到）"}"
    val rich = if (mode?.mode == Mode.FULL) androidx.compose.ui.text.buildAnnotatedString {
        append(status.text + " · ")
        pushStyle(androidx.compose.ui.text.SpanStyle(color = p.red)); append(mode.text); pop()
    } else null
    NavBar(titleOf(s), nav.back, subtitle = subtitle, dot = p.tone(status.tone), more = { menu = true }, dark = dark, subtitleRich = rich, moreDot = high)
    if (menu) ActionSheet(
        info = {
            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                Text("${agentName(s.agent())} · ${projectName(projectKey(s))} · ${machineLabel(state, s.machineId)}", style = T.meta, color = p.muted)
                Text("${shortPath(s.info?.cwd.orEmpty())} · 会话 ${s.id.take(16)}", style = T.meta, color = p.muted, fontFamily = MonoFamily)
            }
        },
        actions = buildList {
            if (live) {
                add(SheetAction("审批模式：${mode?.text ?: "未知"}", danger = mode?.mode == Mode.FULL, enabled = !forking) { sheet = 1 })
                add(SheetAction(if (model.isEmpty()) "会话设置" else "模型：$model" + if (effort.isNotEmpty()) " · $effort" else "", enabled = !forking) { sheet = 2 })
                pc?.let { add(SheetAction("上下文：$it%", warn = high, enabled = !forking) { sheet = 2 }) }
            }
            add(SheetAction("项目设置", enabled = !forking) { projectSettings = true })
            if (canRename) {
                add(SheetAction("重命名", enabled = !forking) { rename = true })
                add(SheetAction(
                    if (forking) "正在分叉…" else "分叉会话", enabled = forkBlock == null, busy = forking, keepOpen = true,
                    note = if (forking) "" else forkBlock ?: if (fork.error.isNotEmpty()) "没能分叉：${fork.error}" else "",
                    noteError = forkBlock == null,
                ) {
                    fork.run({
                        remote.command(s.id, remote.cmd { forkSession = ForkSessionRequest.newBuilder().setSessionId(s.id).build() }).also {
                            if (io.github.lewin671.remoteagent.core.forkedSession(it) == null) throw io.github.lewin671.remoteagent.core.CommandException("internal", "意外的回复")
                        }
                    }) { r ->
                        // The new session may not be in the list yet: its page waits for it, as after starting one.
                        val sid = io.github.lewin671.remoteagent.core.forkedSession(r) ?: return@run
                        menu = false
                        nav.session(sid)
                    }
                })
            }
            if (running) add(SheetAction("中断本轮", enabled = !forking) { act.run({ remote.command(s.id, remote.cmd { interrupt = InterruptRequest.newBuilder().setSessionId(s.id).build() }) }) })
            if (status != Status.EXITED) add(SheetAction("结束会话…", danger = true, enabled = !forking) { confirmStop = true })
        },
        onDismiss = {
            menu = false
            fork.error = ""
        },
    )
    // On the session's own CLI: its set is the one this page is about.
    if (projectSettings) projectConfig(state, projectKey(s))?.let { initial ->
        NewSettingsSheet(state, projectKey(s), projectName(projectKey(s)), initial.machineId, s.agent(), onDone = {
            if (it != null) PrefsChanges.run(remote, it)
            projectSettings = false
        }, onClose = { projectSettings = false })
    }
    if (rename) RenameDialog(s, onClose = { rename = false })
    when (sheet) {
        1 -> ModeSheet(s, onClose = { sheet = 0 })
        2 -> ModelSheet(state, s, running, onClose = { sheet = 0 })
    }
    if (act.error.isNotEmpty()) Text(act.error, Modifier.padding(horizontal = 16.dp), color = p.red, style = T.meta)
    if (confirmStop) Confirm(
        "结束这个会话？",
        "${machineLabel(state, s.machineId)} 上的 ${agentName(s.agent())} 会被关掉，聊天记录保留，之后可以恢复。",
        "结束", danger = true, onDismiss = { confirmStop = false },
    ) { act.run({ remote.command(s.id, remote.cmd { stopSession = StopSessionRequest.newBuilder().setSessionId(s.id).build() }) }) }
}

@Composable
private fun ChatPane(
    state: RemoteState,
    s: SessionView,
    status: Status,
    events: List<io.github.lewin671.remoteagent.proto.Event>,
    d: io.github.lewin671.remoteagent.core.Derived,
    loaded: Boolean,
    error: String?,
    modifier: Modifier,
    onTerminal: () -> Unit,
) {
    val p = pal
    val nav = LocalNav.current
    val remote = LocalRemote.current
    val machine = machineLabel(state, s.machineId)
    val cwd = s.info?.cwd.orEmpty()
    val home = state.machines[s.machineId]?.meta?.home.orEmpty()
    val outbox = Outbox.of(s.id)
    // Messages with images from an earlier run are followed until the CLI has them.
    LaunchedEffect(s.id) { if (Outbox.of(s.id).any { it.images.isNotEmpty() }) Outbox.watch(remote, s.id) }
    val devices = state.devices
    val initialPrompt = Outbox.prompt(s.id)
    val initialId = if (initialPrompt != null) d.inputs.keys.firstOrNull { id -> outbox.none { it.requestId == id } } else null
    val startPrompt = pendingStartPrompt(initialPrompt, events, d, outbox.map { it.requestId }.toSet())
    val projection = remember(remote, s.id, s.agent(), machine, cwd, home, outbox.size, devices, initialPrompt, initialId) { ChatProjection() }
    val items = remember(events, d, projection) {
        projection.update(events, d, machine, cwd, home, s.agent(), { Outbox.text(s.id, it) ?: initialPrompt?.takeIf { _ -> it == initialId } }) { o ->
            io.github.lewin671.remoteagent.core.originLabel(o, state, remote.account.deviceId, machine)
        }
    }
    val pendingInputs = pendingInputs(d)
    LaunchedEffect(d.inputs) {
        d.inputs.forEach { (id, status) ->
            if (status != InputStatus.INPUT_STATUS_QUEUED && status != InputStatus.INPUT_STATUS_HELD_BY_CLI) Cancels.states.remove(id)
        }
    }
    var allPending by remember { mutableStateOf(false) }
    val list = rememberLazyListState()
    val scope = rememberCoroutineScope()
    val atBottom by remember { derivedStateOf { !list.canScrollForward } }
    // Follow new messages while the user is at the bottom; stop once they
    // drag away from it, until they come back. Our own jumps to the end
    // never turn it off, even while the layout has not caught up.
    val followState = remember { Follow() }
    val dragged by list.interactionSource.collectIsDraggedAsState()
    LaunchedEffect(dragged, atBottom) { followState.observe(dragged, atBottom) }
    val follow = followState.on
    var seen by remember { mutableStateOf(items.size) }
    val ime = WindowInsets.ime.getBottom(LocalDensity.current)
    // The dock appearing or going also moves the bottom.
    LaunchedEffect(items.size, outbox.size, d.tools.size, ime, d.approvals.size, d.questions.size, s.info?.planOpen, status) {
        // Past the end: the list stops at its last item, even before its first layout.
        if (follow && items.isNotEmpty()) list.scrollToItem(items.size + d.tools.size + outbox.size + 3)
        if (follow) seen = items.size
    }
    LaunchedEffect(atBottom) { if (atBottom) seen = items.size }

    Column(modifier) {
        Box(Modifier.weight(1f)) {
            val chatCtx = ChatContext(
                s.id, s.agent(), d,
                frozenAt = if (state.conn == ConnState.ONLINE) 0 else state.offlineSinceMs,
                onImage = { seq, i -> nav.image(s.id, seq, i) },
                sessions = state.sessions,
                onSession = { nav.session(it) },
            )
            androidx.compose.runtime.CompositionLocalProvider(LocalChat provides chatCtx) {
            LazyColumn(state = list, modifier = Modifier.fillMaxSize()) {
                item { Spacer(Modifier.height(6.dp)) }
                if (!loaded && events.isEmpty() && startPrompt == null) item { Skeleton() }
                if (startPrompt != null) item(key = "start-prompt") {
                    MeBubble(startPrompt.text, caption = startPrompt.caption, captionColor = if (startPrompt.warning) p.orangeInk else p.muted)
                }
                items(items, key = { it.key }) {
                    ChatItemView(it, agentName(s.agent()), cwd, home) { requestId, text ->
                        // A message the session ended before writing: text and images back.
                        val o = Outbox.find(s.id, requestId)
                        if (o != null) putBack(remote, s.id, o) else Outbox.draft(s.id).value = text
                    }
                }
                if (error != null) item {
                    Banner("后面的记录没读到：$error", Modifier.padding(horizontal = 14.dp, vertical = 8.dp), Tone2.ERROR, action = "重试", onAction = { remote.retryEvents(s.id) })
                }
                val visiblePending = if (pendingInputs.size > 3 && !allPending) pendingInputs.take(2) else pendingInputs
                items(visiblePending.size, key = { "pending-${visiblePending[it].id}" }) { i ->
                    PendingBubble(s.id, visiblePending[i], i + 1, state, remote, if (status == Status.STARTING) agentName(s.agent()) else null)
                }
                if (pendingInputs.size > 3 && !allPending) item {
                    TextAction("还有 ${pendingInputs.size - 2} 条排队 展开", { allPending = true })
                }
                // Messages from this device, until the chat shows them.
                val pending = outbox.filter { o ->
                    val st = d.inputs[o.requestId]
                    st == null || o.state == Outgoing.State.FAILED || o.state == Outgoing.State.UNKNOWN || st == InputStatus.INPUT_STATUS_UNCONFIRMED ||
                        st == InputStatus.INPUT_STATUS_FAILED || st == InputStatus.INPUT_STATUS_NEEDS_TERMINAL
                }
                items(pending, key = { "out" + it.requestId }) { o -> OutgoingView(s.id, o, d.inputs[o.requestId], d.failures[o.requestId], onTerminal) }
                item { Spacer(Modifier.height(8.dp)) }
            }
            }
            val fresh = items.size - seen
            // Only for what came in while reading back; the list keeps following otherwise.
            if (!atBottom && fresh > 0) {
                Box(
                    Modifier.align(Alignment.BottomCenter).padding(bottom = 8.dp).heightIn(min = 44.dp)
                        .shadow(10.dp, RoundedCornerShape(16.dp), ambientColor = androidx.compose.ui.graphics.Color.Black.copy(alpha = .15f))
                        .clip(RoundedCornerShape(16.dp)).background(p.card)
                        .clickable { scope.launch { list.animateScrollToItem(list.layoutInfo.totalItemsCount - 1) } }.padding(horizontal = 12.dp),
                    contentAlignment = Alignment.Center,
                ) { IconText(Lucide.ArrowDown, "$fresh 条新消息", p.blue, T.smB) }
            }
        }
        Bottom(state, s, status, d, onTerminal, onDetail = { aid -> nav.approval(s.id, aid) }, onPlan = { nav.plan(s.id) })
    }
}

@Composable
private fun PendingBubble(sid: String, pending: io.github.lewin671.remoteagent.core.PendingInput, index: Int, state: RemoteState, remote: io.github.lewin671.remoteagent.core.Remote, starting: String?) {
    val p = pal
    var expanded by remember(pending.id) { mutableStateOf(false) }
    val cancel = Cancels.states[pending.id]
    val resume = rememberAction()
    val online = state.conn == ConnState.ONLINE
    val label = when {
        cancel == Cancels.State.Canceling -> "正在取消…"
        cancel is Cancels.State.Failed -> "取消没成功，仍在排队"
        resume.running -> "正在发送…"
        resume.error.isNotEmpty() -> "发送没成功，仍已暂停"
        pending.paused -> "已暂停 · 中断前排着的"
        pending.held -> "插入这一轮 · 等当前工具结束后并入"
        pending.mode == SendMode.SEND_MODE_QUEUE -> "排队 · 这一轮结束后发"
        // No turn to insert into yet: the CLI is still coming up.
        starting != null -> "已送达电脑 · 等 $starting 就绪后发出"
        else -> "插入这一轮 · 等输入框就绪"
    }
    val origin = io.github.lewin671.remoteagent.core.originLabel(pending.origin, state, remote.account.deviceId, "")
    Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 6.dp), horizontalAlignment = Alignment.End) {
        Column(Modifier.fillMaxWidth(.8f).clip(Radii.MD).background(p.card).border(1.dp, p.line, Radii.MD).clickable { expanded = !expanded }.padding(10.dp)) {
            Text(pending.text.ifEmpty { "（图片）" }, style = T.body, color = p.fg, maxLines = if (expanded) Int.MAX_VALUE else 4,
                overflow = TextOverflow.Ellipsis)
        }
        Row(Modifier.fillMaxWidth(.8f).heightIn(min = 36.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(label + if (origin != null && origin != "本客户端") " · $origin" else if (index > 0 && pending.mode == SendMode.SEND_MODE_QUEUE) " · 第 $index 条" else "",
                style = T.meta, color = if (cancel is Cancels.State.Failed) p.red else p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            if (pending.paused && cancel != Cancels.State.Canceling) TextAction(if (resume.error.isNotEmpty()) "重试发送" else "发送", {
                resume.run({ remote.command(sid, remote.cmd { resumeInput = ResumeInputRequest.newBuilder().setSessionId(sid).setRequestId(pending.id).build() }) })
            }, enabled = online && !resume.running, style = T.metaB)
            if (cancel != Cancels.State.Canceling) TextAction(if (cancel is Cancels.State.Failed) "重试" else "取消", { Cancels.cancel(remote, sid, pending.id) }, enabled = online, style = T.metaB)
        }
    }
}

@Composable
private fun OutgoingView(sid: String, o: Outgoing, input: InputStatus?, failure: String?, onTerminal: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    val agent = remote.state.value.sessions[sid]?.agent() ?: io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX
    val failed = input == InputStatus.INPUT_STATUS_FAILED || input == InputStatus.INPUT_STATUS_NEEDS_TERMINAL ||
        (input == null && o.state != Outgoing.State.SENDING && o.state != Outgoing.State.SENT)
    val links = mutableListOf<Pair<String, () -> Unit>>()
    var sending = false
    val (caption, color) = when {
        // Nothing was submitted and the terminal is as it was.
        input == InputStatus.INPUT_STATUS_FAILED -> {
            links += "放回输入框" to { putBack(remote, sid, o); Unit }
            "没能发送：" + io.github.lewin671.remoteagent.core.failureText(failure.orEmpty(), agent) + "。电脑的输入框已恢复原样" to p.red
        }
        // Not submitted, and part of it may still be in the input box.
        input == InputStatus.INPUT_STATUS_NEEDS_TERMINAL -> {
            links += "去终端" to onTerminal
            links += "放回输入框" to { putBack(remote, sid, o); Unit }
            "没能发送：" + io.github.lewin671.remoteagent.core.failureText(failure.orEmpty(), agent) + "。电脑的输入框里可能还留着一部分，不会自动重发" to p.red
        }
        input == InputStatus.INPUT_STATUS_UNCONFIRMED -> {
            // Its images are kept until then; the user decides when to let go.
            links += "移除此提示" to { Outbox.remove(sid, o); o.images.forEach { it.release() } }
            "已送到终端，但没能确认 ${agentName(agent)} 收到，不会自动重发" to p.orangeInk
        }
        o.state == Outgoing.State.SENDING -> {
            sending = true
            "发送中…" to p.muted
        }
        o.state == Outgoing.State.SENT -> "已送达电脑" to p.muted
        else -> {
            links += "重试" to { Outbox.deliver(RaApp.instance.scope, remote, sid, o) }
            links += "移除此提示" to { Outbox.remove(sid, o); o.images.forEach { it.release() } }
            (if (o.state == Outgoing.State.UNKNOWN) "没能确认送达：" else "没能送达：") + o.error to p.red
        }
    }
    Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp)) {
        if (o.images.isNotEmpty()) Row(Modifier.align(Alignment.End).padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
            for (img in o.images.take(4)) img.thumb?.let {
                androidx.compose.foundation.Image(it, null, Modifier.size(56.dp).clip(RoundedCornerShape(10.dp)), contentScale = androidx.compose.ui.layout.ContentScale.Crop)
            }
        }
        MeBubble(o.text.ifEmpty { null }, caption = caption, captionColor = color, captionIcon = if (sending) Lucide.LoaderCircle else null, failed = failed, links = links, padded = false)
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
    onDetail: (String) -> Unit,
    onPlan: () -> Unit,
) {
    val p = pal
    val remote = LocalRemote.current
    val m = state.machines[s.machineId]
    if (status == Status.EXITED) {
        val act = rememberAction()
        EndCard("会话已结束", buildString {
            append(if (s.info?.statusDetail == "stopped") "会话是被手动结束的。" else "tmux 会话已经不在了。")
            if (m?.online == true) append("恢复会在 ${machineLabel(state, s.machineId)} 上用同样的设置重新打开它，聊天记录都在。")
            else append("${machineLabel(state, s.machineId)} 不在线，连上后才能恢复。")
        }, dot = if (act.running) p.blueBg else p.gray, title2 = if (act.running) "正在恢复…" else null) {
            if (m?.online == true && !act.running) Btn(
                if (act.error.isNotEmpty()) "再试一次" else "恢复会话",
                { act.run({ remote.command(s.id, remote.cmd { resumeSession = ResumeSessionRequest.newBuilder().setSessionId(s.id).build() }) }) },
                Modifier.fillMaxWidth(), if (act.error.isNotEmpty()) BtnKind.SECONDARY else BtnKind.PRIMARY, enabled = state.conn == ConnState.ONLINE,
            )
            if (act.error.isNotEmpty()) Text("恢复失败：${act.error}", color = p.red, style = T.meta, modifier = Modifier.padding(top = 6.dp))
        }
        return
    }
    if (status == Status.OFFLINE) {
        val t = now()
        EndCard(
            "${machineLabel(state, s.machineId)} 不在线",
            (if ((m?.lastSeenMs ?: 0) > 0) whenText(m!!.lastSeenMs, t) + "断开。" else "") + "电脑上的会话可能还在跑，重新连上后这里会自动更新，现在只能看历史。",
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
    if (first != null) {
        when (first) {
            is DockItem.Approval -> ApprovalDock(s, first.a, dockItems.size - 1) { onDetail(first.a.approvalId) }
            is DockItem.Question -> QuestionDock(s, first.q, dockItems.size - 1, onTerminal)
            is DockItem.Plan -> PlanDock(s, first.text, dockItems.size - 1, onPlan)
            is DockItem.Popup -> PopupDock(s, first.title, dockItems.size - 1, onTerminal)
        }
    }
    Composer(state, s, d, status, onFocus = {})
}

/** In place of the message box when nothing can be sent: the session ended, or its computer is away. */
@Composable
private fun EndCard(title: String, text: String, dot: androidx.compose.ui.graphics.Color? = null, title2: String? = null, actions: @Composable () -> Unit) {
    val p = pal
    Column(Modifier.fillMaxWidth().padding(start = 12.dp, end = 12.dp, top = 6.dp, bottom = 20.dp).clip(Radii.MD).background(p.soft).padding(horizontal = 14.dp, vertical = 12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Dot(dot ?: p.gray)
            Spacer(Modifier.width(8.dp))
            Text(title2 ?: title, style = T.bodyB, color = p.fg)
        }
        Text(text, style = T.sm, color = p.muted, modifier = Modifier.padding(top = 3.dp, bottom = 10.dp))
        actions()
    }
}

/**
 * The message box: the plus adds images, the text from one line up to about
 * six, and send. The mode, the model and the context are in the more menu of the
 * navigation bar. Without the server connection the draft stays and sending
 * waits.
 */
@Composable
internal fun Composer(state: RemoteState, s: SessionView, d: io.github.lewin671.remoteagent.core.Derived, status: Status, onFocus: (Boolean) -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    val interruptAction = rememberAction()
    var text by Outbox.draft(s.id)
    val online = state.conn == ConnState.ONLINE
    val running = status == Status.RUNNING || d.running
    val images = ImageDrafts.forSession(s.id)
    var keeping by remember { mutableStateOf(false) }
    val machine = state.machines[s.machineId]
    val maxImg = maxImages(machine)
    val blocked = uploadBlocked(machine, online, machineLabel(state, s.machineId))
    // Images picked before the machine went away go there again once it is back.
    LaunchedEffect(blocked, images.images.size) { if (blocked == null) images.sendAllTo(remote, s.machineId) }
    val pick = rememberImagePicker(images, remote, s.machineId, blocked, maxImg)
    fun send(mode: SendMode = SendMode.SEND_MODE_STEER) {
        val msg = text.trim()
        if (msg.isEmpty() && images.images.isEmpty()) return
        // Every image must be on the computer first: never a message with some of them.
        if (!images.ready(s.machineId) && images.images.isNotEmpty()) return
        val picked = images.images.toList()
        if (picked.isEmpty()) {
            text = ""
            Outbox.send(RaApp.instance.scope, remote, s.id, msg, mode)
            return
        }
        // Kept on disk with the message until the CLI has it; sent
        // only once that worked, and only then out of the draft.
        keeping = true
        Outbox.sendWithImages(RaApp.instance.scope, remote, s.id, msg, images.ids(s.machineId), picked, images.original, mode) { kept ->
            keeping = false
            kept.onSuccess {
                if (text.trim() == msg) text = ""
                images.detach(picked)
            }.onFailure { images.notice = "没能保存待发的图，这条没有发出：${errorText(it)}" }
        }
    }
    ComposeBox(
        text, { text = it },
        placeholder = when {
            d.questions.isNotEmpty() -> "排队发送，回答后继续"
            d.tools.isNotEmpty() -> "消息…"
            else -> "消息"
        },
        canSend = !keeping && online && (text.isNotBlank() || images.images.isNotEmpty()) && (images.images.isEmpty() || images.ready(s.machineId)),
        onSend = { send() },
        onPlus = pick, plusEnabled = blocked == null,
        onQueue = if (running) ({ send(SendMode.SEND_MODE_QUEUE) }) else null,
        onInterrupt = if (running) ({ interruptAction.run({ remote.command(s.id, remote.cmd { interrupt = InterruptRequest.newBuilder().setSessionId(s.id).build() }) }) }) else null,
        canInterrupt = online && !interruptAction.running,
        hasDraft = text.isNotBlank() || images.images.isNotEmpty(),
        onFocus = onFocus,
        top = { ImageStrip(images, remote, s.machineId, maxImg) },
    )
    if (interruptAction.error.isNotEmpty()) Banner("没能中断：${interruptAction.error}", Modifier.padding(horizontal = 12.dp), Tone2.ERROR)
    if (text.trimStart().startsWith("/")) Text("以 / 开头的内容会作为普通消息发给模型，而不是执行斜杠命令。", Modifier.padding(horizontal = 16.dp).padding(bottom = 6.dp), style = T.meta, color = p.orangeInk)
}

/** Grey bars while the history loads. */
@Composable
fun Skeleton() {
    val p = pal
    Column(Modifier.padding(horizontal = 14.dp, vertical = 8.dp)) {
        for (w in listOf(0.7f, 0.9f, 0.5f)) Box(Modifier.padding(vertical = 7.dp).fillMaxWidth(w).height(12.dp).clip(RoundedCornerShape(6.dp)).background(p.chip))
    }
}

/**
 * Whether the chat follows new messages. Only the user turns it off, by
 * dragging away from the bottom; reaching the bottom turns it back on. A
 * scroll the app makes to the end ends before the list has laid out the new
 * position, so judging by it would stop following on a long history.
 */
class Follow {
    var on by mutableStateOf(true)
        private set

    fun observe(userDragging: Boolean, atBottom: Boolean) {
        if (atBottom) on = true else if (userDragging) on = false
    }
}

/** Sessions whose terminal another page asked to show. */
object OpenTerminal {
    val requested = androidx.compose.runtime.mutableStateMapOf<String, Boolean>()
}

/**
 * A message that was not submitted goes back into the box, text and
 * images, for the user to send again as a new message. Its images go to
 * the computer again: the daemon may have used up the old uploads. When
 * they do not fit next to images already in the box nothing moves, and
 * the box says why.
 */
internal fun putBack(remote: io.github.lewin671.remoteagent.core.Remote, sid: String, o: Outgoing): Boolean {
    val d = ImageDrafts.forSession(sid)
    val s = remote.state.value.sessions[sid]
    val max = maxImages(s?.let { remote.state.value.machines[it.machineId] })
    if (o.images.isNotEmpty() && !d.attach(o.images, max, o.original)) return false
    val cur = Outbox.draft(sid).value
    Outbox.draft(sid).value = if (cur.isBlank()) o.text else cur.trimEnd() + "\n" + o.text
    Outbox.takeBack(sid, o)
    s?.let { d.sendAllTo(remote, it.machineId) }
    return true
}
