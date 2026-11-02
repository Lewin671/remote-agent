package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.*
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.selection.toggleable
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.luminance
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.window.DialogWindowProvider
import androidx.core.view.WindowCompat
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.customActions
import androidx.compose.foundation.gestures.Orientation
import androidx.compose.foundation.gestures.draggable
import androidx.compose.foundation.gestures.rememberDraggableState
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.IntOffset
import kotlin.math.roundToInt
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.material3.Text
import androidx.lifecycle.compose.LifecycleStartEffect
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.ui.graphics.Color
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.Agent
import kotlinx.coroutines.launch
import kotlinx.coroutines.Dispatchers
import kotlinx.serialization.json.*
import java.time.*

@Composable
fun SchedulesScreen() {
    val remote = LocalRemote.current
    LifecycleStartEffect(remote) {
        remote.schedules.showList()
        onStopOrDispose {}
    }
    val state by remote.state.collectAsState()
    val snapshots by remote.schedules.state.collectAsState()
    val unread by remote.schedules.unread.collectAsState()
    val tasks = remote.schedules.tasks(state)
    val targets = tasks.filter { it.target.text("type") == "resume_session" }.map { it.target.text("session") }.distinct()
    LaunchedEffect(targets, state.conn) {
        if (state.conn == ConnState.ONLINE) for (sid in targets) if (sid.isNotEmpty() && state.sessions[sid]?.info == null) {
            try { remote.fetchSession(sid) } catch (e: kotlinx.coroutines.CancellationException) { throw e } catch (_: Exception) { /* A tap retries through sessionLookup. */ }
        }
    }
    val nav = LocalNav.current
    val scope = rememberCoroutineScope()
    val t = Instant.ofEpochMilli(now())
    val context = LocalContext.current
    val preferences = remember(context) { context.getSharedPreferences("schedule-display", android.content.Context.MODE_PRIVATE) }
    var endedOpen by remember { mutableStateOf(preferences.getBoolean("ended-open", false)) }
    Column(Modifier.fillMaxSize().readableColumn()) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            LargeTitle("定时任务", Modifier.weight(1f))
            Box(Modifier.padding(end = 12.dp).size(44.dp).clickable(role = Role.Button) { nav.newTask("") }.semantics { contentDescription = "新任务" }, contentAlignment = Alignment.Center) { Ic(Lucide.Plus, 22.dp, pal.blue) }
        }
        if (!state.loaded) ConnBanner(state) else ScheduleOffline(state, tasks)
        LazyColumn(Modifier.weight(1f)) {
            snapshots.forEach { (machine, snapshot) ->
                if (snapshot.error.isNotEmpty()) item("error-$machine") { Banner(snapshot.error, action = "重试", onAction = { scope.launch(Dispatchers.Main.immediate) { remote.schedules.refresh(machine) } }) }
            }
            if (tasks.isEmpty()) item {
                Column(Modifier.padding(horizontal = 32.dp, vertical = 64.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                    Text(if (snapshots.values.any { it.loading }) "正在读取任务…" else "还没有定时任务", style = T.h17, color = pal.fg)
                    Text(SCHEDULE_EMPTY, style = T.sm, color = pal.muted, modifier = Modifier.padding(top = 12.dp))
                }
            }
            ScheduleInboxGroup.entries.forEach { group ->
                val rows = scheduleSorted(tasks, group) { unread[ScheduleTaskKey(it.machine, it.id)] == true }
                if (rows.isNotEmpty()) {
                    item("group-$group") {
                        if (group == ScheduleInboxGroup.ENDED) Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable {
                            endedOpen = !endedOpen
                            preferences.edit().putBoolean("ended-open", endedOpen).apply()
                        }.padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                            Text("已结束", Modifier.weight(1f), style = T.smB, color = pal.muted)
                            Text("${rows.size}", style = T.sm, color = pal.muted)
                            Ic(if (endedOpen) Lucide.ChevronUp else Lucide.ChevronDown, 16.dp, pal.muted)
                        }
                        else Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp).heightIn(min = 44.dp), verticalAlignment = Alignment.CenterVertically) {
                            Text(group.label, Modifier.weight(1f), style = T.smB, color = if (group == ScheduleInboxGroup.ATTENTION) pal.orangeInk else pal.muted)
                            if (group == ScheduleInboxGroup.ATTENTION) Text("${rows.size}", style = T.sm, color = pal.muted)
                            else if (rows.any { unread[ScheduleTaskKey(it.machine, it.id)] == true }) TextAction("全部标为已读", { remote.schedules.markRead(rows) }, style = T.sm)
                        }
                    }
                    if (group != ScheduleInboxGroup.ENDED || endedOpen) items(rows, key = { "${it.machine}/${it.id}" }) { task ->
                        val hasNew = unread[ScheduleTaskKey(task.machine, task.id)] == true
                        TaskRow(task, state, t, last = task == rows.last(), hasNew = hasNew, tasks = tasks,
                            onMark = { if (hasNew) remote.schedules.markRead(listOf(task)) else remote.schedules.markUnread(task) }) { nav.task(task.machine, task.id) }
                    }
                }
            }
        }
    }
}

/** One leading slot: attention wins over new-result blue, otherwise it stays empty. */
@Composable
fun TaskRow(task: ScheduledTask, state: RemoteState, now: Instant = Instant.now(), last: Boolean = false, hasNew: Boolean = false,
    tasks: List<ScheduledTask> = listOf(task), onMark: () -> Unit = {}, onClick: () -> Unit) {
    val attention = taskGroup(task) == TaskGroup.ATTENTION
    val running = scheduleInProgress(task)
    val canMark = scheduleCanMark(task, hasNew)
    val action = if (hasNew) "标为已读" else "标为未读"
    var offset by remember(task.machine, task.id, hasNew, canMark) { mutableFloatStateOf(0f) }
    var width by remember { mutableIntStateOf(1) }
    val reveal = with(LocalDensity.current) { 88.dp.toPx() }
    Box(Modifier.fillMaxWidth().onSizeChanged { width = it.width }) {
        if (canMark && offset < 0) Row(Modifier.matchParentSize().background(pal.blueBg).padding(end = 16.dp),
            horizontalArrangement = Arrangement.End, verticalAlignment = Alignment.CenterVertically) {
            Row(Modifier.width(72.dp).fillMaxHeight().clickable { offset = 0f; onMark() }, horizontalArrangement = Arrangement.Center, verticalAlignment = Alignment.CenterVertically) {
                if (hasNew) { Ic(Lucide.Check, 16.dp, Color.White); Spacer(Modifier.width(4.dp)) }
                Text(if (hasNew) "已读" else "未读", style = T.smB, color = Color.White)
            }
        }
        Row(Modifier.offset { IntOffset(offset.roundToInt(), 0) }.fillMaxWidth().background(pal.card)
            .then(if (canMark) Modifier.draggable(rememberDraggableState { delta -> offset = (offset + delta).coerceIn(-width.toFloat(), 0f) }, Orientation.Horizontal,
                onDragStopped = {
                    if (-offset >= width * .65f) { offset = 0f; onMark() }
                    else offset = if (-offset >= reveal / 2) -reveal else 0f
                }) else Modifier)
            .semantics { if (canMark) customActions = listOf(CustomAccessibilityAction(action) { onMark(); true }) }
            .clickable { if (offset != 0f) offset = 0f else onClick() }.padding(start = 6.dp, end = 16.dp, top = 10.dp), verticalAlignment = Alignment.Top) {
            Box(Modifier.width(22.dp).padding(top = 8.dp), contentAlignment = Alignment.TopCenter) {
                if (attention || hasNew) ScheduleResultDot(if (attention) pal.orange else pal.blue, if (attention) "需要你处理" else "有新结果")
            }
            Column(Modifier.weight(1f).then(if (last) Modifier else Modifier.bottomLine(pal.line)).padding(bottom = 10.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(task.title, Modifier.weight(1f), style = if (attention || hasNew) T.bodyB else T.body.copy(fontWeight = platformWeight(FontWeight.Medium)),
                        color = if (task.status == "paused") pal.fg2 else pal.fg, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(scheduleNotificationTime(task, now), style = T.meta, color = pal.muted, maxLines = 1)
                }
                val preview = schedulePreview(task, now)
                val summary = buildAnnotatedString {
                    if (attention && !running) {
                        withStyle(SpanStyle(color = pal.orangeInk, fontWeight = T.smB.fontWeight)) { append(scheduleAttentionWord(task)) }
                        append(preview.removePrefix(scheduleAttentionWord(task)))
                    } else append(preview)
                }
                Text(summary, modifier = Modifier.padding(top = 1.dp), style = T.sm, color = if (hasNew) pal.fg else pal.muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
                Row(Modifier.padding(top = 3.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                    if (running) { Mark(Lucide.LoaderCircle, pal.muted, 12.dp); Text("正在运行 ·", style = T.meta, color = pal.muted) }
                    Text(scheduleRowMetadata(task, state, tasks), style = T.meta, color = pal.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
        }
    }
}

@Composable
internal fun ScheduleResultDot(color: Color = pal.blue, description: String = "进入时未读") {
    Box(Modifier.size(8.dp).background(color, CircleShape).semantics { contentDescription = description })
}

@Composable
private fun ScheduleOffline(state: RemoteState, tasks: List<ScheduledTask>) {
    // The connection bar speaks for a lost relay; this names the computers that are away.
    val away = tasks.map { it.machine }.distinct().filter { state.machines[it]?.online != true }
    if (state.conn == ConnState.ONLINE && away.isNotEmpty())
        Banner(away.joinToString("、") { machineLabel(state, it) } + " " + SCHEDULE_OFFLINE)
}

@Composable
fun ScheduleDetailScreen(machine: String, id: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val snapshots by remote.schedules.state.collectAsState()
    val task = snapshots[machine]?.tasks?.firstOrNull { it.id == id }?.takeIf { scheduleSupported(state.machines[machine]) }
    val entryUnread by remote.schedules.detailUnread.collectAsState()
    val capturedDetail by remote.schedules.capturedDetail.collectAsState()
    LifecycleStartEffect(remote, machine, id) {
        remote.schedules.openTask(machine, id)
        onStopOrDispose { remote.schedules.hideTask(machine, id) }
    }
    DisposableEffect(remote, machine, id) { onDispose { remote.schedules.closeTask(machine, id) } }
    val writable = scheduleWritable(state, machine)
    val scope = rememberCoroutineScope()
    var edit by remember { mutableStateOf("") }
    var editBase by remember { mutableStateOf<ScheduledTask?>(null) }
    fun beginEdit(kind: String) { editBase = task; edit = kind }
    var menu by remember { mutableStateOf(false) }
    // A visit pins the result being read; arriving results only offer an explicit refresh.
    var selectedResult by remember(machine, id) { mutableStateOf<ScheduledRun?>(null) }
    var resultCaptured by remember(machine, id) { mutableStateOf(false) }
    LaunchedEffect(task, capturedDetail) {
        if (!resultCaptured && task != null && capturedDetail == ScheduleTaskKey(machine, id)) {
            selectedResult = task.latestNotification; resultCaptured = true
        }
    }
    var deleting by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var uncertain by remember { mutableStateOf(false) }
    val t = Instant.ofEpochMilli(now())
    fun operate(op: String, spec: JsonObject? = null) {
        if (busy || task == null) return
        busy = true; error = ""
        scope.launch(Dispatchers.Main.immediate) {
            try { remote.schedules.operate(machine, op, if (spec != null) editBase ?: task else task, spec); edit = ""; editBase = null; if (op == "cancel") nav.back() }
            catch (e: kotlinx.coroutines.CancellationException) { throw e }
            catch (e: Exception) { error = errorText(e); uncertain = (e as? CommandException)?.unknown == true; if (e is ScheduleConflictException) { edit = ""; editBase = null } }
            finally { busy = false }
        }
    }
    val attention = task?.let(::taskGroup) == TaskGroup.ATTENTION
    val onceFailed = task?.let(::scheduleFailedOnce) == true
    val targetMissing = task?.let(::scheduleTargetMissing) == true
    fun recover() {
        val current = task ?: return
        val systemCancelled = current.status == "cancelled"
        if (systemCancelled && (current.trigger.text("type") == "at" || current.trigger.text("type") == "after_turn" && current.trigger["repeat"]?.jsonPrimitive?.booleanOrNull != true)) beginEdit("time")
        else operate(if (systemCancelled) "update" else if (onceFailed) "run" else "resume", if (systemCancelled) current.spec else null)
    }
    val enabled = writable && !busy && !uncertain
    val definitionEnabled = enabled && task?.let(::scheduleDefinitionAllowed) == true
    Column(Modifier.fillMaxSize().background(pal.bg)) {
        NavBar(task?.title ?: "定时任务", nav.back, subtitle = task?.let { scheduleDetailSubtitle(it, state, t) }, more = { menu = true })
        if (!state.loaded) ConnBanner(state)
        if (task == null) {
            Banner(snapshots[machine]?.error?.ifEmpty { null } ?: if (snapshots[machine]?.loading == true) "正在读取任务…" else "任务未找到，可能已在另一端删除")
            return@Column
        }
        Column(Modifier.weight(1f).fillMaxWidth().readableColumn().verticalScroll(rememberScrollState())) {
            if (error.isNotEmpty()) Banner(error, tone = Tone2.ERROR)
            if (uncertain) TextAction("重新读取", { scope.launch(Dispatchers.Main.immediate) { remote.schedules.refresh(machine); if (remote.schedules.state.value[machine]?.error.isNullOrEmpty()) uncertain = false } }, Modifier.padding(horizontal = 16.dp))
            if (attention) {
                ScheduleAttentionBanner(scheduleAttentionText(task, t))
                if (!targetMissing) Btn(if (onceFailed) "立即运行一次" else "恢复", { recover() }, Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp), BtnKind.PRIMARY, enabled)
            }
            if (resultCaptured && task.latestNotification?.id != selectedResult?.id && task.latestNotification != null) {
                TextAction("有新结果", { selectedResult = task.latestNotification }, Modifier.fillMaxWidth().padding(horizontal = 16.dp), style = T.sm)
            }
            task.active?.let { run ->
                Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                    Row(Modifier.fillMaxWidth().padding(start = 14.dp, end = 6.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        Mark(Lucide.LoaderCircle, pal.blue, 14.dp)
                        Text("正在运行 · ${scheduleClock(run.time)} 开始", Modifier.weight(1f), style = T.sm, color = pal.blue)
                        if (run.session.isNotEmpty()) TextAction("对话", { nav.scheduledSession(run.session, run.seq) }, style = T.sm, arrow = true)
                    }
                }
            }
            val result = if (resultCaptured) selectedResult?.let { selected -> task.allRuns.firstOrNull { it.id == selected.id } ?: selected } else task.latestNotification
            if (result != null) ScheduleLatestResult(result, t, capturedDetail == ScheduleTaskKey(machine, id) && result.id in entryUnread,
                showConversation = !(task.target.text("type") == "resume_session" && task.active != null)) {
                nav.scheduledSession(result.session, result.seq)
            }
            val earlier = task.allRuns.filter { it.id != result?.id && it.id != task.active?.id &&
                (!it.notified || result != null && it.notificationTime <= result.notificationTime) }.sortedByDescending { it.notificationTime }
            var allRuns by remember(task.id) { mutableStateOf(false) }
            if (earlier.isNotEmpty()) {
                Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp).heightIn(min = 44.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text("更早", Modifier.weight(1f), style = T.smB, color = pal.muted)
                    Text("${earlier.size} 次", style = T.sm, color = pal.muted)
                }
                Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                    Column {
                        scheduleRunGroups(if (allRuns) earlier else earlier.take(5)).forEach { group -> ScheduleRunGroup(group, t, entryUnread) { run -> if (run.session.isNotEmpty()) nav.scheduledSession(run.session, run.seq) } }
                        if (!allRuns && earlier.size > 5) TextAction("查看全部 ${earlier.size} 次", { allRuns = true }, Modifier.fillMaxWidth())
                    }
                }
            } else if (result == null && task.active == null) Hint("还没有运行", Modifier.padding(horizontal = 16.dp))
            Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                Column {
                    Row(Modifier.fillMaxWidth().bottomLine(pal.line).padding(horizontal = 14.dp), verticalAlignment = Alignment.CenterVertically) {
                        Text("让 agent 做什么", Modifier.weight(1f), style = T.body, color = pal.fg)
                        TextAction("编辑", { beginEdit("message") }, enabled = definitionEnabled, style = T.sm)
                    }
                    ScheduleDescription(task.message, Modifier.padding(horizontal = 14.dp, vertical = 12.dp))
                }
            }
            Label("设置")
            Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                Column {
                    ScheduleSetting("什么时候", scheduleTiming(task.trigger), definitionEnabled) { beginEdit("time") }
                    ToggleRow("完成后通知", "", task.notifyOnFinish, enabled = enabled, onChange = { on -> if (enabled) { editBase = task; operate("update", JsonObject(task.spec + ("notify" to JsonPrimitive(if (on) "finish" else "agent_only")))) } })
                    val end = scheduleLimits(task, t)
                    if (end.isNotEmpty()) ScheduleSetting("结束", end, definitionEnabled) { beginEdit("time") }
                    if (task.target.text("type") == "resume_session") {
                        ScheduleSetting("发到", scheduleDestination(task, state), enabled && state.sessions[task.target.text("session")]?.info != null) { nav.session(task.target.text("session")) }
                    } else ScheduleSetting("在哪运行", taskLocation(task, state), definitionEnabled) { beginEdit("config") }
                    Box(Modifier.bottomLine(pal.line)) {
                        GRow("权限", taskPermission(task, state), onClick = if (task.target.text("type") == "new_session" && definitionEnabled) ({ beginEdit("mode") }) else null,
                            chevron = task.target.text("type") == "new_session" && definitionEnabled, enabled = definitionEnabled, singleLineValue = true)
                    }
                    ScheduleSetting("任务名", task.title, scheduleRenameAllowed(writable, busy || uncertain)) { beginEdit("name") }
                }
            }
            Hint(scheduleCreation(task, t), Modifier.padding(horizontal = 16.dp))
            if (!writable) Hint("${machineLabel(state, machine)} 连上后才能修改、运行或删除。", Modifier.padding(horizontal = 16.dp, vertical = 8.dp))
            Hint("立即运行一次不占自动次数及日历当天名额。删除任务会停止后续调度，已发出的消息和已开的对话保留。", Modifier.padding(horizontal = 16.dp, vertical = 8.dp))
        }
    }
    if (task != null) {
        if (menu) ActionSheet(info = {
            Column {
                Text(task.title, style = T.bodyB, color = pal.fg)
                Text(taskLocation(task, state) + " · " + scheduleCreation(task, t), style = T.meta, color = pal.muted)
            }
        }, actions = buildList {
            add(SheetAction("立即运行一次", enabled = enabled && (scheduleRunAllowed(task) || scheduleFailedOnce(task))) { operate("run") })
            add(SheetAction("标为未读", enabled = task.latestNotification != null) { remote.schedules.markUnread(machine, id); nav.schedules() })
            if (taskGroup(task) != TaskGroup.ENDED && !onceFailed) add(SheetAction(if (task.status == "paused" || attention) "恢复" else "暂停", enabled = enabled) {
                if (task.status == "paused" || attention) recover() else operate("pause")
            })
            add(SheetAction("删除任务…", danger = true, enabled = enabled) { deleting = true })
        }, onDismiss = { menu = false })
        val base = editBase ?: task
        if (edit == "name") ScheduleRenameDialog(base, { edit = "" }, enabled = scheduleRenameAllowed(writable, busy || uncertain)) { name ->
            check(scheduleRenameAllowed(writable, busy || uncertain)) { "当前任务不能单独改名" }
            busy = true
            try { remote.schedules.rename(base, name) }
            catch (e: Exception) { uncertain = (e as? CommandException)?.unknown == true; if (e is ScheduleConflictException) { edit = ""; editBase = null; error = errorText(e) }; throw e }
            finally { busy = false }
        }
        if (edit == "time") ScheduleTimeSheet(base.trigger, { operate("update", JsonObject(base.spec + ("trigger" to it))) }, { edit = "" }, enabled = definitionEnabled)
        if (edit == "message") ScheduleMessageEditor(base.message, busy, error, { edit = "" }, { text -> operate("update", JsonObject(base.spec + ("message" to JsonPrimitive(text)))) }, writable = definitionEnabled)
        if (edit == "config" || edit == "mode") {
            val cfg = scheduleConfig(base)
            NewSettingsSheet(scheduleEditingState(state, machine), cfg.cwd, projectName(cfg.cwd), machine, cfg.agent, if (edit == "mode") "mode" else "",
                onDone = { op ->
                    val picked = op?.sets?.firstOrNull { it.agent == op.agent }
                    if (picked == null) edit = "" else operate("update", JsonObject(base.spec + ("target" to scheduleTarget(agentConfig(machine, cfg.cwd, picked), base.target))))
                }, onClose = { edit = "" }, initial = cfg, scheduling = true, lockMachine = true)
        }
        if (deleting) Confirm("删除这个任务？", "停止后续调度，已发出的消息和已开的对话保留。", "删除", true, { deleting = false }) { deleting = false; operate("cancel") }
    }
}

/** Shares the result cards' content inset, inside the same constrained detail column. */
@Composable
internal fun ScheduleAttentionBanner(text: String, modifier: Modifier = Modifier) {
    Box(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 8.dp).then(modifier)
        .clip(Radii.SM).background(pal.soft).padding(horizontal = 14.dp, vertical = 12.dp)) {
        Text(text, style = T.sm, color = pal.orangeInk)
    }
}

/** Expands only for this visit; a complete description remains selectable. */
@Composable
fun ScheduleDescription(message: String, modifier: Modifier = Modifier) {
    var expanded by remember(message) { mutableStateOf(false) }
    var overflow by remember(message) { mutableStateOf(false) }
    Column(modifier) {
        Text(message, style = T.body, color = pal.fg, maxLines = if (expanded) Int.MAX_VALUE else 3,
            overflow = TextOverflow.Ellipsis, onTextLayout = { if (!expanded) overflow = it.hasVisualOverflow })
        if (expanded || overflow) Row(Modifier.heightIn(min = 44.dp).clickable { expanded = !expanded }, verticalAlignment = Alignment.CenterVertically) {
            Text(if (expanded) "收起" else "展开全文", style = T.smB, color = pal.blue)
            Ic(if (expanded) Lucide.ChevronUp else Lucide.ChevronDown, 14.dp, pal.blue)
        }
    }
}

@Composable
fun ScheduleMessageEditor(initial: String, busy: Boolean, error: String, close: () -> Unit, save: (String) -> Unit, writable: Boolean = true) {
    var text by remember { mutableStateOf(initial) }
    var discard by remember { mutableStateOf(false) }
    fun cancel() { if (text != initial) discard = true else close() }
    Dialog({ if (!busy) cancel() }, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        val view = LocalView.current
        val light = pal.fg.luminance() < .5f
        SideEffect {
            (view.parent as? DialogWindowProvider)?.window?.let { window ->
                WindowCompat.getInsetsController(window, view).apply { isAppearanceLightStatusBars = light; isAppearanceLightNavigationBars = light }
            }
        }
        BackHandler { if (!busy) cancel() }
        Column(Modifier.fillMaxSize().background(pal.card).safeDrawingPadding().imePadding()) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp), verticalAlignment = Alignment.CenterVertically) {
                TextAction("取消", { cancel() }, enabled = !busy)
                Text("让 agent 做什么", Modifier.weight(1f), style = T.bodyB, color = pal.fg, textAlign = androidx.compose.ui.text.style.TextAlign.Center)
                TextAction(if (busy) "保存中…" else "保存", { save(text) }, enabled = !busy && writable && scheduleMessageValid(text))
            }
            if (error.isNotEmpty()) Banner(error, tone = Tone2.ERROR)
            BasicTextField(text, { text = it }, Modifier.weight(1f).fillMaxWidth().padding(16.dp),
                textStyle = T.body.copy(color = pal.fg), cursorBrush = SolidColor(pal.blue), readOnly = busy || !writable)
            if (text.toByteArray(Charsets.UTF_8).size > SCHEDULE_MESSAGE_MAX_BYTES) Hint("太长了，最多 32 KB", Modifier.padding(16.dp))
        }
        if (discard) Confirm("放弃修改？", "修改还没有保存。", "放弃", true, { discard = false }) { discard = false; close() }
    }
}

@Composable
internal fun ScheduleRunGroup(group: RunGroup, now: Instant, entryUnread: Set<String> = emptySet(), open: (ScheduledRun) -> Unit) {
    var expanded by rememberSaveable(group.key) { mutableStateOf(false) }
    if (group.quiet && group.runs.size > 1) {
        Box(Modifier.bottomLine(pal.line)) {
            GRow("${scheduleWhen(group.runs.first().notificationTime, now).substringBefore(' ')} · ${group.runs.size} 次，都没有通知", singleLineValue = true, onClick = { expanded = !expanded }, trailing = {
                Ic(if (expanded) Lucide.ChevronUp else Lucide.ChevronDown, 16.dp, pal.muted)
            })
        }
        val latest = group.runs.first()
        Text("最近一次 ${scheduleClock(latest.notificationTime)}：${latest.foldedSummary}", style = T.meta, color = pal.muted, modifier = Modifier.padding(start = 14.dp, end = 14.dp, bottom = 12.dp))
        if (!expanded) return
    }
    group.runs.forEach { run ->
        Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).then(if (run.session.isNotEmpty()) Modifier.clickable { open(run) } else Modifier)
            .bottomLine(pal.line).padding(horizontal = 14.dp, vertical = 12.dp), verticalAlignment = Alignment.Top) {
            Box(Modifier.width(22.dp).padding(top = 5.dp), contentAlignment = Alignment.TopCenter) { if (run.id in entryUnread) ScheduleResultDot() }
            Column(Modifier.weight(1f)) {
                Text(scheduleWhen(run.notificationTime, now), style = T.sm, color = pal.muted)
                Text(run.summary.ifEmpty { run.foldedSummary }, style = T.body, color = pal.fg, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
            if (run.session.isNotEmpty()) Ic(Lucide.ChevronRight, 16.dp, pal.faint)
        }
    }
}

@Composable
fun ScheduleSetting(label: String, value: String, enabled: Boolean = true, onClick: () -> Unit) {
    Box(Modifier.bottomLine(pal.line)) {
        GRow(label, value, onClick = onClick, chevron = enabled, enabled = enabled, singleLineValue = true)
    }
}

/** A CLI setting uses the same grouped row and small segments as the settings page. */
@Composable
internal fun ScheduleAgentSetting(agents: List<Agent>, selected: Agent, enabled: Boolean, onSelect: (Agent) -> Unit) {
    if (agents.size < 2) return
    Box(Modifier.bottomLine(pal.line)) {
        GRow("CLI", trailing = {
            SegPick(agents.map(::agentName), agents.indexOf(selected), enabled) { onSelect(agents[it]) }
        })
    }
}

fun scheduleConfig(task: ScheduledTask): NewConfig = NewConfig(task.machine, task.target.text("cwd"), if (task.target.text("agent") == "claude") Agent.AGENT_CLAUDE else Agent.AGENT_CODEX,
    task.target.text("model"), task.target.text("effort"), when(task.target.text("mode")) { "ask" -> Mode.ASK; "auto" -> Mode.AUTO; "full" -> Mode.FULL; else -> null })
fun scheduleTarget(cfg: NewConfig, base: JsonObject = JsonObject(emptyMap())): JsonObject {
    val fields = base.toMutableMap()
    fields.putAll(buildJsonObject {
        put("type", "new_session"); put("cwd", cfg.cwd); put("agent", if (cfg.agent == Agent.AGENT_CLAUDE) "claude" else "codex")
        put("model", cfg.model); put("effort", cfg.effort)
    })
    if (cfg.mode == null) fields.remove("mode") else fields["mode"] = JsonPrimitive(cfg.mode.name.lowercase())
    return JsonObject(fields)
}

@Composable
fun NewScheduleScreen(session: String) {
    val close = LocalNav.current.back
    if (session.isEmpty()) ScheduleEditor(session, close)
    else Sheet(close, scroll = false) { Box(Modifier.fillMaxHeight(.9f)) { ScheduleEditor(session, close) } }
}

@Composable
fun ScheduleEditor(session: String, onClose: () -> Unit) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val scope = rememberCoroutineScope()
    val current = session.isNotEmpty()
    var title by remember { mutableStateOf("") }
    var message by remember { mutableStateOf("") }
    var notifyFinish by rememberSaveable { mutableStateOf(true) }
    var project by remember { mutableStateOf("") }
    var config by remember { mutableStateOf<NewConfig?>(null) }
    var pickProject by remember { mutableStateOf(false) }
    var pickDirectory by remember { mutableStateOf(false) }
    var settings by remember { mutableStateOf<String?>(null) }
    var timeOpen by remember { mutableStateOf(false) }
    var triggerText by rememberSaveable { mutableStateOf(ScheduleTime().trigger().toString()) }
    val trigger = remember(triggerText) { Json.parseToJsonElement(triggerText).jsonObject }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    val request = rememberSaveable { newRequestId() }
    var frozen by remember { mutableStateOf<String?>(null) }
    val start = if (project.isNotEmpty()) projectStart(state, project, config?.agent, config?.machineId) else null
    FetchEarlier(state, page = config == null && start?.settled == false)
    val cfg = config ?: start?.config
    val machine = if (current) state.sessions[session]?.machineId else cfg?.machineId
    val writable = machine != null && scheduleWritable(state, machine)
    val editable = frozen == null && !busy
    val blocked = machine?.let { if (!scheduleSupported(state.machines[it])) "这台电脑需要更新 remote-agent，才能管理定时任务" else if (!writable) SCHEDULE_OFFLINE else null }
    Column(Modifier.fillMaxSize().background(pal.card)) {
        if (current) SheetHead("定时发到这个对话", "取消", onClose) else NavBar("新任务", onClose)
        ConnBanner(state)
        Column(Modifier.weight(1f).fillMaxWidth().readableColumn().imePadding().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp)) {
            if (error.isNotEmpty()) Banner(error, tone = Tone2.ERROR)
            if (blocked != null) Banner(blocked, tone = Tone2.WARN)
            FieldLabel("让 agent 做什么")
            ReasonField(message, { if (editable) message = it }, "检查项目的更新，有值得注意的变化再通知我。", maxLines = if (current) 4 else 8)
            if (message.toByteArray(Charsets.UTF_8).size > SCHEDULE_MESSAGE_MAX_BYTES) Hint("太长了，最多 32 KB")
            FieldLabel("任务名")
            ReasonField(title, { if (editable) title = scheduleNameInput(it) }, if (message.isBlank()) "留空就用消息的第一句" else "留空会用：${scheduleName("", message)}", singleLine = true)
            ToggleRow("完成后通知", "", notifyFinish, enabled = editable) { notifyFinish = it }
            ScheduleSetting("什么时候", scheduleTiming(trigger), editable) { timeOpen = true }
            if (!current) {
                ScheduleSetting("项目", project.takeIf { it.isNotEmpty() }?.let(::projectName) ?: "选择项目", editable) { pickProject = true }
                if (cfg != null) {
                    ScheduleAgentSetting(machineAgents(state.machines[cfg.machineId]), cfg.agent, editable) { agent -> config = projectConfig(state, project, agent, cfg.machineId) }
                    val permission = effectiveModeText(cfg.mode, defaultModeOf(state.machines[cfg.machineId], cfg.agent))
                    ScheduleSetting("权限", permission, editable) { settings = "mode" }
                    val model = listOf(cfg.model.ifEmpty { "默认模型" }, cfg.effort).filter { it.isNotEmpty() }.joinToString(" · ")
                    ScheduleSetting(if (state.machines.size > 1) "机器和模型" else "模型", if (state.machines.size > 1) "${machineLabel(state, cfg.machineId)} · $model" else model, editable) { settings = if (state.machines.size > 1) "" else "model" }
                }
                Hint("每次运行新开一个对话，归在这个任务下面，不进会话列表。完成后默认通知；关闭后仅由 agent 主动通知。")
                if (machine == null && state.machines.values.any { !scheduleSupported(it) }) Hint("电脑需要更新 remote-agent，才能创建定时任务。")
            } else Hint("消息等当前这一轮结束后再发，不会打断正在做的事。")
            val plan = schedulePlanBlocked(current,cfg,state)
            if (plan) Hint("计划模式不能创建执行任务，请先选择其他权限")
            if (!current && cfg != null && resolvedScheduleConfig(cfg, state).mode == null) Hint("请选择明确的权限，再创建定时任务。")
            val ready = !plan && (current || cfg?.let { resolvedScheduleConfig(it, state).mode != null } == true) && writable && !busy && (scheduleMessageValid(message) || frozen != null) && (current || cfg != null && (config != null || start?.settled == true))
            Btn(if (busy) "创建中…" else if (frozen != null) "重试创建" else "创建", {
                if (!ready) return@Btn
                val spec = frozen?.let { Json.parseToJsonElement(it).jsonObject } ?: taskSpec(scheduleName(title, message), message, trigger,
                    if (current) buildJsonObject { put("type", "resume_session"); put("session", session) } else scheduleTarget(resolvedScheduleConfig(cfg!!, state))).let { JsonObject(it + ("notify" to JsonPrimitive(if (notifyFinish) "finish" else "agent_only"))) }.also { frozen = it.toString() }
                busy = true; error = ""
                scope.launch(Dispatchers.Main.immediate) {
                    try {
                        val result = remote.schedules.operate(machine!!, "create", spec = spec, requestId = request)
                        onClose()
                        if (result != null) nav.task(machine, result.id)
                    } catch (e: kotlinx.coroutines.CancellationException) { throw e }
                    catch (e: Exception) { error = errorText(e); if (e is CommandException && e.fromDaemon && !e.unknown) frozen = null }
                    finally { busy = false }
                }
            }, Modifier.fillMaxWidth().padding(vertical = 16.dp), BtnKind.PRIMARY, ready)
            if (frozen != null) Hint("创建结果尚未确认，再试一次使用同一个请求。返回后可先查看任务列表。")
        }
    }
    if (timeOpen) ScheduleTimeSheet(trigger, { triggerText = it.toString(); timeOpen = false }, { timeOpen = false })
    if (pickProject) Sheet({ pickProject = false }) {
        SheetHead("选项目", "取消", { pickProject = false })
        ProjectPickList(state, { path -> project = path; config = null; pickProject = false }, { pickProject = false; pickDirectory = true })
    }
    if (pickDirectory) Sheet({ pickDirectory = false }) {
        SheetHead("选目录", "取消", { pickDirectory = false })
        DirPicker(state) { m, path -> project = path; config = NewConfig(m, path, machineAgents(state.machines[m]).first()); pickDirectory = false }
    }
    val settingsPage = settings
    if (settingsPage != null && cfg != null) NewSettingsSheet(state, project, projectName(project), cfg.machineId, cfg.agent, settingsPage,
        onDone = { op ->
            if (op != null) {
                config = agentConfig(op.machineId, project, op.sets.single { it.agent == op.agent })
            }
            settings = null
        }, onClose = { settings = null }, initial = cfg, scheduling = true)
}

@Composable
fun ScheduleTimeSheet(initial: JsonObject, done: (JsonObject) -> Unit, close: () -> Unit, enabled: Boolean = true) {
    val original = remember { ScheduleTime.from(initial) }
    var value by remember { mutableStateOf(original) }
    var replaced by remember { mutableStateOf(false) }
    val keepTurnDelay = initial.text("type") == "after_turn" && !replaced
    var error by remember { mutableStateOf("") }
    val context = androidx.compose.ui.platform.LocalContext.current
    fun date(current: String, picked: (String) -> Unit) {
        val d = runCatching { LocalDate.parse(current) }.getOrElse { LocalDate.now() }
        android.app.DatePickerDialog(context, { _, y, m, day -> picked(LocalDate.of(y, m + 1, day).toString()) }, d.year, d.monthValue - 1, d.dayOfMonth).show()
    }
    Sheet(close) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Text("什么时候", Modifier.weight(1f), style = T.h17, color = pal.fg)
            TextAction("完成", {
                try { done(if (keepTurnDelay || value == original && !replaced) initial else value.trigger()) } catch (e: Exception) { error = e.message ?: "请检查时间" }
            }, enabled = enabled && (keepTurnDelay || value.kind != 2 || value.weekdays.isNotEmpty()))
        }
        SegPick(listOf("一次", "每天", "每周", "每隔"), if (keepTurnDelay) -1 else value.kind) { replaced = true; value = value.copy(kind = it) }
        if (keepTurnDelay) {
            Hint("当前是「${scheduleFrequency(initial)}」。直接完成会保留原规则；选择上面的时间类型后再更改。")
        } else {
        if (error.isNotEmpty()) Banner(error, tone = Tone2.ERROR)
        if (value.kind == 0) ScheduleSetting("日期", value.date) { date(value.date) { value = value.copy(date = it) } }
        if (value.kind != 3) {
            ScheduleSetting("时间", value.time) {
                val t = runCatching { LocalTime.parse(value.time) }.getOrElse { LocalTime.of(8, 0) }
                android.app.TimePickerDialog(context, { _, h, m -> value = value.copy(time = "%02d:%02d".format(h, m)) }, t.hour, t.minute, true).show()
            }
        } else {
            FieldLabel("间隔")
            ReasonField(value.interval, { value = value.copy(interval = it) }, "正整数")
            GRow("单位", trailing = { SegPick(listOf("分钟", "小时", "天", "秒"), value.unit) { value = value.copy(unit = it) } })
        }
        if (value.kind == 1) ToggleRow("只在工作日", "周一到周五", value.workdays) { value = value.copy(workdays = it) }
        if (value.kind == 2) {
            Row(Modifier.fillMaxWidth().clip(Radii.SM).background(pal.chip).padding(2.dp), horizontalArrangement = Arrangement.spacedBy(2.dp)) {
                (1..7).forEach { day ->
                    val selected = day in value.weekdays
                    Box(Modifier.weight(1f).heightIn(min = 44.dp).clip(Radii.SM).background(if (selected) (if (pal.card == androidx.compose.ui.graphics.Color.White) pal.card else pal.track) else androidx.compose.ui.graphics.Color.Transparent)
                        .toggleable(value = selected, role = Role.Checkbox) { value = value.copy(weekdays = if (day in value.weekdays) value.weekdays - day else value.weekdays + day) }, contentAlignment = Alignment.Center) {
                        Text(listOf("一", "二", "三", "四", "五", "六", "日")[day-1], style = if (selected) T.smB else T.sm, color = if (selected) pal.fg else pal.muted)
                    }
                }
            }
        }
        if (value.kind != 0) {
            GRow("结束", trailing = { SegPick(listOf("不结束", "运行指定次数", "截止日期"), value.end) { value = value.copy(end = it) } })
            if (value.end == 1) ReasonField(value.count, { value = value.copy(count = it) }, "自动运行次数")
            if (value.end == 2) ScheduleSetting("结束日期", value.until) { date(value.until) { value = value.copy(until = it, deadline = "") } }
        }
        if (value.kind != 3) {
            FieldLabel("时区")
            ReasonField(value.zone, { value = value.inZone(it.trim()) }, "例如 Asia/Shanghai")
        } else Hint(SCHEDULE_INTERVAL_HINT)
        }
    }
}

@Composable
fun SessionScheduleStrip(sid: String) {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val snapshots by remote.schedules.state.collectAsState()
    val tasks = snapshots.values.flatMap { it.tasks }.filter { scheduleSupported(state.machines[it.machine]) && it.target.text("type") == "resume_session" && it.target.text("session") == sid && taskGroup(it) != TaskGroup.ENDED }
    if (tasks.isEmpty()) return
    val nav = LocalNav.current
    val next = tasks.minByOrNull { it.next ?: Instant.MAX }!!
    val text = sessionScheduleText(tasks, state, Instant.ofEpochMilli(now()))
    var choosing by remember { mutableStateOf(false) }
    if (choosing) Sheet({ choosing = false }) {
        SheetHead("定时任务", "取消", { choosing = false })
        tasks.forEach { task -> GRow(task.title, onClick = { choosing = false; nav.task(task.machine, task.id) }, chevron = true, singleLineValue = true) }
    }
    Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).background(pal.soft).clickable {
        if (tasks.size == 1) nav.task(next.machine, next.id) else choosing = true
    }.padding(horizontal = 16.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Ic(Lucide.Clock, 13.dp, pal.muted)
        Text(text, Modifier.weight(1f).padding(start = 6.dp), style = T.meta, color = pal.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
        ArrowText("管理", pal.blue, T.meta)
    }
}
