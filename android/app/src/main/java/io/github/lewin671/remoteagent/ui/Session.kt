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
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Stop
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
import io.github.lewin671.remoteagent.proto.CancelInputRequest
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.InterruptRequest
import io.github.lewin671.remoteagent.proto.ResumeSessionRequest
import io.github.lewin671.remoteagent.proto.StopSessionRequest
import io.github.lewin671.remoteagent.term.TerminalPane
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

@Composable
fun SessionScreen(sid: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val s = state.sessions[sid]
    val p = pal
    var gone by remember { mutableStateOf(false) }
    LaunchedEffect(s == null, state.loaded) {
        if (s == null && state.loaded) {
            delay(15_000)
            gone = true
        }
    }
    if (s == null) {
        Column(Modifier.fillMaxSize().padding(24.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
            if (gone) {
                Text("找不到这个会话", style = MaterialTheme.typography.titleMedium)
                TextButton(onClick = nav.back) { Text("返回") }
            } else {
                CircularProgressIndicator()
                Text("读取中…", color = p.muted, modifier = Modifier.padding(top = 12.dp))
            }
        }
        return
    }
    val events by remote.events(sid).collectAsState()
    val d = rememberDerived(events.events, s?.agent() ?: io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX)
    val status = statusIn(state, s)
    var tab by rememberSaveable { mutableStateOf(0) }
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
        SessionTopBar(state, s, status, d.running, dark = term)
        ConnBanner(state)
        SessionTabs(tab, status != Status.EXITED, status == Status.POPUP, dark = term) { tab = it }
        if (tab == 1 && status != Status.EXITED) {
            TerminalPane(remote, sid, sessionRunning = status != Status.EXITED, modifier = Modifier.weight(1f).fillMaxWidth())
        } else {
            ChatPane(state, s, status, events.events, d, events.loaded, events.error, Modifier.weight(1f), onTerminal = { tab = 1 })
        }
    }
}

/** ‹, the title with its CLI, the status, project and machine, and ⋯: 中断本轮 and 结束会话…. */
@Composable
private fun SessionTopBar(state: RemoteState, s: SessionView, status: Status, running: Boolean, dark: Boolean) {
    val p = pal
    val nav = LocalNav.current
    val remote = LocalRemote.current
    var menu by remember { mutableStateOf(false) }
    var confirmStop by remember { mutableStateOf(false) }
    val act = rememberAction()
    StatusNavBar(titleOf(s), agentName(s.agent()), status, "${projectName(projectKey(s))} · ${machineLabel(state, s.machineId)}", nav.back, { menu = true }, dark)
    if (menu) ActionSheet(
        info = {
            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                Text(shortPath(s.info?.cwd.orEmpty()) + " · " + machineLabel(state, s.machineId), style = T.meta, color = p.muted, fontFamily = MonoFamily)
                Text("会话 ${s.id.take(16)}", style = T.meta, color = p.muted, fontFamily = MonoFamily)
            }
        },
        actions = buildList {
            if (running) add(SheetAction("中断本轮") { act.run({ remote.command(s.id, remote.cmd { interrupt = InterruptRequest.newBuilder().setSessionId(s.id).build() }) }) })
            if (status != Status.EXITED) add(SheetAction("结束会话…", danger = true) { confirmStop = true })
        },
        onDismiss = { menu = false },
    )
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
                terminalOpen = status != Status.EXITED && status != Status.OFFLINE,
                onTerminal = onTerminal,
                onTool = { seq -> nav.tool(s.id, seq) },
                onImage = { seq, i -> nav.image(s.id, seq, i) },
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
                ) { Text("↓ $fresh 条新消息", style = T.smB, color = p.blue) }
            }
        }
        Bottom(state, s, status, d, onTerminal, onDetail = { aid -> nav.approval(s.id, aid) }, onPlan = { nav.plan(s.id) })
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
        o.state == Outgoing.State.SENDING -> "◌ 发送中…" to p.muted
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
        MeBubble(o.text.ifEmpty { null }, caption = caption, captionColor = color, failed = failed, links = links, padded = false)
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
    StatusStrip(state, s, d, status)
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
 * The line above the message box (client-v7): the queue's count and why it
 * waits, and 中断, which stays in the same place with or without a draft.
 * Running tools are told in the chat, where they started.
 */
@Composable
private fun StatusStrip(state: RemoteState, s: SessionView, d: io.github.lewin671.remoteagent.core.Derived, status: Status) {
    val p = pal
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val online = state.conn == ConnState.ONLINE
    // Interrupt while it works; a session waiting for the user has ⋯ for that.
    val running = status == Status.RUNNING || status == Status.STARTING
    val q = io.github.lewin671.remoteagent.core.queued(d)
    if (!running && q.isEmpty()) return
    val interrupt = rememberAction()
    ComposeStatus(
        queue = if (q.isNotEmpty()) "排队 ${q.size} 条" else null,
        why = if (q.isNotEmpty()) io.github.lewin671.remoteagent.core.queueWhy(s, status).short else null,
        onQueue = { nav.queue(s.id) },
        interrupt = if (running && online) (if (interrupt.running) "中断中…" else "中断") else null,
        onInterrupt = { interrupt.run({ remote.command(s.id, remote.cmd { this.interrupt = InterruptRequest.newBuilder().setSessionId(s.id).build() }) }) },
        interruptEnabled = !interrupt.running,
    )
    if (interrupt.error.isNotEmpty()) Text("没能中断：${interrupt.error}", Modifier.padding(horizontal = 16.dp), style = T.meta, color = p.red)
}

/**
 * The message box (client-v7): ＋, the text from one line up to about six,
 * and ↑. 添加图片, the mode, the model and the context are in the ＋ menu;
 * full access stays in sight under the text, a full context is an orange dot
 * on ＋. Without the server connection the draft stays and sending and
 * settings wait.
 */
@Composable
internal fun Composer(state: RemoteState, s: SessionView, d: io.github.lewin671.remoteagent.core.Derived, status: Status, onFocus: (Boolean) -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    var text by Outbox.draft(s.id)
    var sheet by remember { mutableStateOf(0) } // 1 mode, 2 model
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
    // Claude Code records full model IDs; show the alias the list offers.
    var models by remember(s.machineId) { mutableStateOf<io.github.lewin671.remoteagent.proto.ListModelsResponse?>(null) }
    LaunchedEffect(s.machineId, s.agent()) {
        if (s.agent() == io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE) runCatching { models = remote.listModels(s.machineId, s.agent()) }
    }
    val raw = s.info?.model.orEmpty()
    val model = models?.let { ms -> io.github.lewin671.remoteagent.core.findModel(ms.modelsList, raw) { it.slug }?.displayName } ?: raw
    val effort = s.info?.effort.orEmpty()
    val label = io.github.lewin671.remoteagent.core.modeLabel(s)
    val pc = contextPercent(s)
    fun send() {
        val msg = text.trim()
        if (msg.isEmpty() && images.images.isEmpty()) return
        // Every image must be on the computer first: never a message with some of them.
        if (!images.ready(s.machineId) && images.images.isNotEmpty()) return
        val picked = images.images.toList()
        if (picked.isEmpty()) {
            text = ""
            Outbox.send(RaApp.instance.scope, remote, s.id, msg)
            return
        }
        // Kept on disk with the message until the CLI has it; sent
        // only once that worked, and only then out of the draft.
        keeping = true
        Outbox.sendWithImages(RaApp.instance.scope, remote, s.id, msg, images.ids(s.machineId), picked, images.original) { kept ->
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
        onSend = ::send,
        menu = listOfNotNull(
            MenuEntry("添加图片", enabled = blocked == null, onClick = pick),
            MenuEntry("模式：${label?.text ?: "未知"} ▾") { sheet = 1 },
            MenuEntry("模型：" + (if (model.isEmpty()) "会话设置" else model + if (effort.isNotEmpty()) " · $effort" else "") + " ▾") { sheet = 2 },
            // Unknown context is left out, never shown as 0%.
            pc?.let { MenuEntry("上下文：$it%") { sheet = 2 } },
        ),
        menuEnabled = online,
        contextHigh = (pc ?: 0) > 80,
        warning = if (label?.mode == Mode.FULL) "完全放行" else null,
        onFocus = onFocus,
        top = { ImageStrip(images, remote, s.machineId, maxImg) },
    )
    if (text.trimStart().startsWith("/")) Text("以 / 开头的内容会作为普通消息发给模型；常用命令在 ＋ 菜单里。", Modifier.padding(horizontal = 16.dp).padding(bottom = 6.dp), style = T.meta, color = p.orangeInk)
    when (sheet) {
        1 -> ModeSheet(s, onClose = { sheet = 0 })
        2 -> ModelSheet(state, s, running, onClose = { sheet = 0 })
    }
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
