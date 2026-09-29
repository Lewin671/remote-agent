package io.github.lewin671.remoteagent.ui

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
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
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
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
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.ConnState
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
    val d = remember(events.events) { derive(events.events) }
    val status = statusIn(state, s)
    var tab by rememberSaveable { mutableStateOf(0) }
    BackHandler(enabled = tab == 1) { tab = 0 }

    Column(Modifier.fillMaxSize().background(p.card)) {
        SessionTopBar(state, s, status, d.running, onTerminal = { tab = 1 })
        ConnBanner(state)
        SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 6.dp)) {
            SegmentedButton(tab == 0, { tab = 0 }, SegmentedButtonDefaults.itemShape(0, 2)) { Text("聊天") }
            SegmentedButton(
                tab == 1, { tab = 1 }, SegmentedButtonDefaults.itemShape(1, 2),
                enabled = status != Status.EXITED,
            ) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("终端")
                    if (status == Status.POPUP) {
                        Spacer(Modifier.width(4.dp))
                        Dot(p.orange, 6)
                    }
                }
            }
        }
        if (tab == 1 && status != Status.EXITED) {
            TerminalPane(remote, sid, sessionRunning = status != Status.EXITED, modifier = Modifier.weight(1f).fillMaxWidth())
        } else {
            ChatPane(state, s, status, events.events, d, events.loaded, events.error, Modifier.weight(1f), onTerminal = { tab = 1 })
        }
    }
}

@Composable
private fun SessionTopBar(state: RemoteState, s: SessionView, status: Status, running: Boolean, onTerminal: () -> Unit) {
    val p = pal
    val nav = LocalNav.current
    val remote = LocalRemote.current
    var menu by remember { mutableStateOf(false) }
    var confirmStop by remember { mutableStateOf(false) }
    val act = rememberAction()
    Row(Modifier.fillMaxWidth().padding(horizontal = 4.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        IconButton(onClick = nav.back) { Icon(Icons.AutoMirrored.Filled.ArrowBack, "返回") }
        Column(Modifier.weight(1f)) {
            Text(titleOf(s), style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Row(verticalAlignment = Alignment.CenterVertically) {
                StatusLabel(status)
                Text(
                    " · ${projectName(projectKey(s))} · ${machineLabel(state, s.machineId)}" +
                        (if (s.agent() != io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX) " · ${agentName(s.agent())}" else ""),
                    style = MaterialTheme.typography.bodySmall, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis,
                )
            }
        }
        Box {
            IconButton(onClick = { menu = true }) { Icon(Icons.Default.MoreVert, "更多") }
            DropdownMenu(menu, { menu = false }) {
                Column(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                    Text(shortPath(s.info?.cwd.orEmpty()) + " · " + machineLabel(state, s.machineId), style = MaterialTheme.typography.bodySmall, color = p.muted)
                    Text("会话 ${s.id.take(16)}", style = MaterialTheme.typography.bodySmall, color = p.faint, fontFamily = FontFamily.Monospace)
                }
                HorizontalDivider()
                if (running) DropdownMenuItem({ Text("中断本轮") }, onClick = {
                    menu = false
                    act.run({ remote.command(s.id, remote.cmd { interrupt = InterruptRequest.newBuilder().setSessionId(s.id).build() }) })
                })
                if (status != Status.EXITED) {
                    DropdownMenuItem({ Text("终端") }, onClick = { menu = false; onTerminal() })
                    DropdownMenuItem({ Text("结束会话…", color = p.red) }, onClick = { menu = false; confirmStop = true })
                }
            }
        }
    }
    if (act.error.isNotEmpty()) Text(act.error, Modifier.padding(horizontal = 16.dp), color = p.red, style = MaterialTheme.typography.bodySmall)
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
    val items = remember(events, d) { buildChat(events, d, machine) }
    val list = rememberLazyListState()
    val scope = rememberCoroutineScope()
    val outbox = Outbox.of(s.id)
    val t = now(1000)
    val atBottom by remember { derivedStateOf { !list.canScrollForward } }
    var seen by remember { mutableStateOf(items.size) }
    LaunchedEffect(items.size, outbox.size, d.tools.size) {
        if (atBottom || seen == 0) list.scrollToItem(Int.MAX_VALUE.coerceAtMost(list.layoutInfo.totalItemsCount.coerceAtLeast(1) - 1).coerceAtLeast(0))
        if (atBottom) seen = items.size
    }
    LaunchedEffect(atBottom) { if (atBottom) seen = items.size }

    Column(modifier) {
        Box(Modifier.weight(1f)) {
            LazyColumn(state = list, modifier = Modifier.fillMaxSize()) {
                if (!loaded && events.isEmpty()) item { Text("读取中…", Modifier.padding(16.dp), color = p.muted) }
                items(items, key = { it.key }) { ChatItemView(it, agentName(s.agent())) }
                if (error != null) item {
                    Card(Modifier.padding(12.dp)) {
                        Column(Modifier.padding(14.dp)) {
                            Text("后面的记录没读到", style = MaterialTheme.typography.titleSmall)
                            Text(error, style = MaterialTheme.typography.bodyMedium, color = p.muted)
                            TextButton(onClick = { remote.retryEvents(s.id) }) { Text("重试") }
                        }
                    }
                }
                items(d.tools, key = { "tool" + it.since + it.title }) { tool ->
                    Row(Modifier.padding(horizontal = 16.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                        CircularProgressIndicator(Modifier.size(12.dp), strokeWidth = 1.5.dp)
                        Spacer(Modifier.width(8.dp))
                        Text(tool.title.lines().first(), style = MaterialTheme.typography.bodyMedium, fontFamily = FontFamily.Monospace, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                        if (tool.since > 0) Text(duration((t - tool.since).coerceAtLeast(0)), style = MaterialTheme.typography.bodySmall, color = p.faint)
                    }
                }
                // Messages from this device, until the chat shows them.
                val pending = outbox.filter { o ->
                    val st = d.inputs[o.requestId]
                    st == null || o.state == Outgoing.State.FAILED || o.state == Outgoing.State.UNKNOWN || st == InputStatus.INPUT_STATUS_UNCONFIRMED
                }
                items(pending, key = { "out" + it.requestId }) { o -> OutgoingView(s.id, o, d.inputs[o.requestId]) }
                item { Spacer(Modifier.height(8.dp)) }
            }
            val fresh = items.size - seen
            if (!atBottom && fresh > 0) {
                Surface(
                    Modifier.align(Alignment.BottomCenter).padding(8.dp).clickable { scope.launch { list.animateScrollToItem(list.layoutInfo.totalItemsCount - 1) } },
                    shape = RoundedCornerShape(18.dp), color = p.blueBg, shadowElevation = 3.dp,
                ) { Text("↓ $fresh 条新消息", Modifier.padding(horizontal = 14.dp, vertical = 8.dp), color = p.card) }
            } else if (!atBottom) {
                Surface(
                    Modifier.align(Alignment.BottomCenter).padding(8.dp).clickable { scope.launch { list.animateScrollToItem(list.layoutInfo.totalItemsCount - 1) } },
                    shape = RoundedCornerShape(18.dp), color = p.chip, shadowElevation = 2.dp,
                ) { Text("回到最新", Modifier.padding(horizontal = 14.dp, vertical = 8.dp), color = p.fg2) }
            }
        }
        Bottom(state, s, status, d, onTerminal, onDetail = { aid -> nav.approval(s.id, aid) }, onPlan = { nav.plan(s.id) })
    }
}

@Composable
private fun OutgoingView(sid: String, o: Outgoing, input: InputStatus?) {
    val p = pal
    val remote = LocalRemote.current
    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 4.dp), horizontalAlignment = Alignment.End) {
        Box(Modifier.background(p.blueSoft.copy(alpha = 0.6f), RoundedCornerShape(16.dp)).padding(horizontal = 12.dp, vertical = 8.dp)) {
            Text(o.text, style = MaterialTheme.typography.bodyLarge, color = p.fg2)
        }
        when {
            input == InputStatus.INPUT_STATUS_UNCONFIRMED -> Text("已送到终端，但没能确认 ${agentName(remote.state.value.sessions[sid]?.agent() ?: io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX)} 收到，不会自动重发", style = MaterialTheme.typography.bodySmall, color = p.orangeInk)
            o.state == Outgoing.State.SENDING -> Text("◌ 发送中…", style = MaterialTheme.typography.bodySmall, color = p.muted)
            o.state == Outgoing.State.SENT -> Text("已送达电脑", style = MaterialTheme.typography.bodySmall, color = p.muted)
            else -> {
                Text(
                    (if (o.state == Outgoing.State.UNKNOWN) "没能确认送达：" else "没能送达：") + o.error,
                    style = MaterialTheme.typography.bodySmall, color = p.red,
                )
                Row {
                    TextButton(onClick = { Outbox.deliver(RaApp.instance.scope, remote, sid, o) }) { Text("重试") }
                    TextButton(onClick = { Outbox.remove(sid, o) }) { Text("移除此提示", color = p.muted) }
                }
            }
        }
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
    val t = now()
    if (status == Status.EXITED) {
        val act = rememberAction()
        StatusCard("会话已结束", buildString {
            append(s.info?.statusDetail?.takeIf { it.isNotEmpty() }?.let { "$it。" } ?: "")
            if (m?.online == true) append("恢复会在 ${machineLabel(state, s.machineId)} 上用同样的设置重新打开它，聊天记录都在。")
            else append("${machineLabel(state, s.machineId)} 不在线，连上后才能恢复。")
        }) {
            if (m?.online == true) {
                Button(
                    onClick = { act.run({ remote.command(s.id, remote.cmd { resumeSession = ResumeSessionRequest.newBuilder().setSessionId(s.id).build() }) }) },
                    enabled = !act.running && state.conn == ConnState.ONLINE, modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp),
                ) { Text(if (act.running) "正在恢复…" else if (act.error.isNotEmpty()) "再试一次" else "恢复会话") }
            }
            if (act.error.isNotEmpty()) Text("恢复失败：${act.error}", color = p.red, style = MaterialTheme.typography.bodySmall)
        }
        return
    }
    if (status == Status.OFFLINE) {
        StatusCard(
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
    var typing by remember { mutableStateOf(false) }
    val density = LocalDensity.current
    val ime = WindowInsets.ime.getBottom(density) > 0
    val focus = LocalFocusManager.current
    val keyboard = LocalSoftwareKeyboardController.current
    val first = dockItems.firstOrNull()
    if (first != null) {
        if (typing && ime) DockCollapsed(first) { keyboard?.hide(); focus.clearFocus() }
        else when (first) {
            is DockItem.Approval -> ApprovalDock(s, first.a, dockItems.size - 1) { onDetail(first.a.approvalId) }
            is DockItem.Question -> QuestionDock(s, first.q, dockItems.size - 1, onTerminal)
            is DockItem.Plan -> PlanDock(s, first.text, dockItems.size - 1, onPlan)
            is DockItem.Popup -> PopupDock(s, first.title, dockItems.size - 1, onTerminal)
        }
    }
    QueueView(s, d, status)
    Composer(state, s, d, status, onFocus = { typing = it })
}

@Composable
private fun StatusCard(title: String, text: String, actions: @Composable () -> Unit) {
    val p = pal
    Surface(Modifier.fillMaxWidth().padding(12.dp), shape = RoundedCornerShape(16.dp), color = p.soft) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(title, style = MaterialTheme.typography.titleSmall)
            Text(text, style = MaterialTheme.typography.bodyMedium, color = p.muted)
            actions()
        }
    }
}

/** Messages accepted but waiting, with why and a way to cancel them. */
@Composable
private fun QueueView(s: SessionView, d: io.github.lewin671.remoteagent.core.Derived, status: Status) {
    val p = pal
    val remote = LocalRemote.current
    val queued = d.inputs.filterValues { it == InputStatus.INPUT_STATUS_QUEUED }.keys.toList()
    if (queued.isEmpty()) return
    val why = when (s.info?.inputWait) {
        "draft" -> "终端里有没发出的草稿，发完或清掉后再发"
        "popup" -> "弹窗处理完再发"
        "unrecognized" -> "认不出输入框，已暂停"
        else -> when (status) {
            Status.QUESTION -> "答完再发"
            Status.APPROVAL -> "审批完再发"
            Status.RUNNING -> "本轮结束后按顺序发"
            Status.STARTING -> "等 ${agentName(s.agent())} 就绪后发出"
            else -> "即将发出"
        }
    }
    Column(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp).background(p.soft, RoundedCornerShape(12.dp)).padding(10.dp)) {
        Text("排队 · $why", style = MaterialTheme.typography.bodySmall, color = p.muted)
        for (id in queued) {
            val act = rememberAction()
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    Outbox.text(s.id, id) ?: "（从其他设备发出的消息）",
                    style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f), color = if (act.running) p.faint else p.fg,
                )
                IconButton(onClick = {
                    act.run({ remote.command(s.id, remote.cmd { cancelInput = CancelInputRequest.newBuilder().setSessionId(s.id).setRequestId(id).build() }) })
                }, enabled = !act.running) { Text("✕", color = p.muted) }
            }
        }
    }
}

@Composable
private fun Composer(state: RemoteState, s: SessionView, d: io.github.lewin671.remoteagent.core.Derived, status: Status, onFocus: (Boolean) -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    var text by rememberSaveable(s.id) { mutableStateOf("") }
    var sheet by remember { mutableStateOf(0) } // 1 mode, 2 model
    val online = state.conn == ConnState.ONLINE
    val interrupt = rememberAction()
    val running = status == Status.RUNNING || d.running
    Column(Modifier.fillMaxWidth().background(p.card).padding(horizontal = 12.dp, vertical = 6.dp)) {
        Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
            val mode = modeOf(s)
            Chip(
                mode?.text ?: "模式",
                color = when (mode) { Mode.FULL -> p.red; Mode.PLAN -> p.purple; Mode.AUTO -> p.greenInk; else -> p.fg2 },
                bg = when (mode) { Mode.FULL -> p.redSoft; Mode.PLAN -> p.purpleSoft; else -> p.chip },
            ) { sheet = 1 }
            val model = s.info?.model.orEmpty()
            Chip(if (model.isEmpty()) "模型读取中" else "$model · ${s.info?.effort.orEmpty()} ▾") { sheet = 2 }
            contextPercent(s)?.let { pc ->
                Chip("$pc%", color = if (pc > 80) p.orangeInk else p.fg2, bg = if (pc > 80) p.orangeSoft else p.chip) { sheet = 2 }
            }
        }
        Row(verticalAlignment = Alignment.Bottom, modifier = Modifier.padding(top = 6.dp)) {
            OutlinedTextField(
                text, { text = it },
                Modifier.weight(1f).heightIn(min = 48.dp, max = 160.dp).onFocusChanged { onFocus(it.isFocused) },
                placeholder = { Text("消息") },
                shape = RoundedCornerShape(22.dp),
                colors = OutlinedTextFieldDefaults.colors(unfocusedBorderColor = p.line),
            )
            Spacer(Modifier.width(8.dp))
            if (text.isBlank() && running) {
                FilledIconButton(
                    onClick = { interrupt.run({ remote.command(s.id, remote.cmd { this.interrupt = InterruptRequest.newBuilder().setSessionId(s.id).build() }) }) },
                    enabled = online && !interrupt.running, modifier = Modifier.size(48.dp),
                    colors = IconButtonDefaults.filledIconButtonColors(containerColor = p.fg),
                ) { Icon(Icons.Default.Stop, "中断本轮", tint = p.card) }
            } else {
                FilledIconButton(
                    onClick = {
                        val msg = text.trim()
                        if (msg.isEmpty()) return@FilledIconButton
                        text = ""
                        Outbox.send(RaApp.instance.scope, remote, s.id, msg)
                    },
                    enabled = online && text.isNotBlank(), modifier = Modifier.size(48.dp),
                    colors = IconButtonDefaults.filledIconButtonColors(containerColor = p.blueBg),
                ) { Icon(Icons.Default.ArrowUpward, "发送", tint = p.card) }
            }
        }
        if (text.trimStart().startsWith("/")) Text("以 / 开头的内容会作为普通消息发给模型；常用命令在上面的按钮里。", style = MaterialTheme.typography.bodySmall, color = p.orangeInk)
        if (interrupt.error.isNotEmpty()) Text(interrupt.error, style = MaterialTheme.typography.bodySmall, color = p.red)
    }
    when (sheet) {
        1 -> ModeSheet(s, onClose = { sheet = 0 })
        2 -> ModelSheet(state, s, running, onClose = { sheet = 0 })
    }
}
