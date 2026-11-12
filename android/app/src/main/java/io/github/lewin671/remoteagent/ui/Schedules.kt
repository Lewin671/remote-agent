package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

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
import androidx.compose.ui.unit.sp
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
    val copy = uiText()
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
    val loading = snapshots.values.any { it.loading }
    val isNew = { task: ScheduledTask -> unread[ScheduleTaskKey(task.machine, task.id)] == true }
    val stopped = scheduleSorted(tasks, ScheduleInboxGroup.ATTENTION, isNew)
    val recent = scheduleSorted(tasks, ScheduleInboxGroup.RECENT, isNew)
    val fresh = recent.filter(isNew)
    val others = recent.filterNot(isNew)
    val ended = scheduleSorted(tasks, ScheduleInboxGroup.ENDED, isNew)
    val headline = scheduleHeadline(stopped.size, fresh.size, tasks.isEmpty(), loading, copy = copy)
    // In the shell (client-v58): the bottom tabs and 新定时 float over the page, and 新定时 is the only way to start one.
    ShellPage(
        Modifier.fillMaxSize(),
        bottom = { ShellBottom(ShellTab.SCHEDULES, copy(UiMessages.homeNewSchedule()), Lucide.Plus, enabled = !state.unlinked && state.machines.isNotEmpty()) { nav.newTask("") } },
    ) {
        // No glass row on this page, so the title sits just under the status bar.
        LazyColumn(Modifier.fillMaxSize().readableColumn(), contentPadding = shellPadding(top = false)) {
            item("headline") {
                Text(
                    buildAnnotatedString {
                        val count = headline.count
                        if (count == null) append(headline.text) else {
                            val template = copy(UiMessages.scheduleStoppedCount(count, "\uFFFC"))
                            append(template.substringBefore('\uFFFC'))
                            withStyle(SpanStyle(color = pal.orange)) { append(count.toString()) }
                            append(template.substringAfter('\uFFFC'))
                        }
                    },
                    style = T.title, color = pal.ink, modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 20.dp),
                )
                if (headline.sub.isNotEmpty()) Text(headline.sub, style = T.s17, color = pal.ink2, modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 7.dp))
                Column(Modifier.padding(start = 16.dp, end = 16.dp, top = 10.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    if (!state.loaded) ConnStrip(state) else ScheduleOffline(state, tasks)
                    snapshots.forEach { (machine, snapshot) ->
                        if (snapshot.error.isNotEmpty()) Notice(uiText(snapshot.error), action = copy(UiMessages.commonRetry()), onAction = { scope.launch(Dispatchers.Main.immediate) { remote.schedules.refresh(machine) } })
                    }
                }
                if (tasks.isEmpty() && !loading) Text(copy(SCHEDULE_EMPTY), style = T.s17, color = pal.ink2, modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 16.dp))
            }
            items(stopped, key = { "stop/${it.machine}/${it.id}" }) { task ->
                ScheduleCard(task, state, t, warn = true, hasNew = isNew(task), tasks = tasks,
                    onMark = { if (isNew(task)) remote.schedules.markRead(listOf(task)) else remote.schedules.markUnread(task) }) { nav.task(task.machine, task.id) }
            }
            if (fresh.isNotEmpty()) item("fresh") {
                ScheduleSection(copy(UiMessages.scheduleNewResultsSchedules()), if (fresh.size > 1) copy(UiMessages.scheduleMarkAllAsRead()) else null) { remote.schedules.markRead(fresh) }
            }
            items(fresh, key = { "new/${it.machine}/${it.id}" }) { task ->
                ScheduleCard(task, state, t, warn = false, hasNew = true, tasks = tasks, onMark = { remote.schedules.markRead(listOf(task)) }) { nav.task(task.machine, task.id) }
            }
            if (others.isNotEmpty()) item("others") { ScheduleSection(if (stopped.isEmpty() && fresh.isEmpty()) copy(UiMessages.scheduleAndroidTask()) else copy(UiMessages.scheduleOtherTasks())) }
            items(others, key = { "row/${it.machine}/${it.id}" }) { task ->
                TaskRow(task, state, t, last = task == others.last(), tasks = tasks, onMark = { remote.schedules.markUnread(task) }) { nav.task(task.machine, task.id) }
            }
            if (ended.isNotEmpty()) {
                item("ended") {
                    Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable {
                        endedOpen = !endedOpen
                        preferences.edit().putBoolean("ended-open", endedOpen).apply()
                    }.padding(horizontal = 24.dp), verticalAlignment = Alignment.CenterVertically) {
                        ArrowText(copy(UiMessages.scheduleAndroidEndedValue1((ended.size).toString())), pal.ink2, T.s15, down = !endedOpen, up = endedOpen)
                    }
                }
                if (endedOpen) items(ended, key = { "end/${it.machine}/${it.id}" }) { task ->
                    TaskRow(task, state, t, last = task == ended.last(), tasks = tasks, onMark = { remote.schedules.markUnread(task) }) { nav.task(task.machine, task.id) }
                }
            }
        }
    }
}

/** A section title on the schedules page, 15 / 600, with at most one link at the right. */
@Composable
private fun ScheduleSection(text: String, action: String? = null, onAction: () -> Unit = {}) {
    Row(Modifier.fillMaxWidth().padding(start = 24.dp, end = 12.dp, top = 22.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text, style = T.s15b, color = pal.ink, modifier = Modifier.weight(1f))
        if (action != null) TextAction(action, onAction, style = T.s15, alignment = Alignment.Center, modifier = Modifier.padding(horizontal = 12.dp))
    }
}

/**
 * A task that stopped ([warn]: the orange dot and why, two lines) or has a
 * new result (the blue dot and its summary, four lines), as a gbg card,
 * radius 28 (design-system §6 定时任务). Swiped left it is marked read.
 */
@Composable
fun ScheduleCard(task: ScheduledTask, state: RemoteState, now: Instant, warn: Boolean, hasNew: Boolean, tasks: List<ScheduledTask>, onMark: () -> Unit, onClick: () -> Unit) {
    val copy = uiText()
    val p = pal
    SwipeToMark(scheduleCanMark(task, hasNew), hasNew, onMark, onClick, Modifier.padding(start = 16.dp, end = 16.dp, top = 12.dp).clip(Radii.R28)) {
        Column(Modifier.fillMaxWidth().background(p.gbg).padding(18.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                ScheduleResultDot(if (warn) p.orange else p.blue, if (warn) copy(UiMessages.scheduleNeedsYourAttention()) else copy(UiMessages.scheduleAndroidNewResult()))
                Text(task.title, Modifier.weight(1f), style = T.s15b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(scheduleNotificationTime(task, now, copy = copy), style = T.s13, color = p.ink2, maxLines = 1)
            }
            val preview = schedulePreview(task, now, copy = copy)
            if (warn) {
                val word = scheduleAttentionWord(task, copy = copy)
                Text(
                    buildAnnotatedString {
                        if (!scheduleInProgress(task)) {
                            withStyle(SpanStyle(color = p.orangeInk, fontWeight = T.s15b.fontWeight)) { append(word) }
                            append(preview.removePrefix(word))
                        } else append(preview)
                    },
                    style = T.s15, color = p.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 10.dp),
                )
            } else Text(preview, style = T.s17.copy(lineHeight = 25.5.sp), color = p.ink, maxLines = 4, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 10.dp))
            Row(Modifier.padding(top = 12.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                Text(scheduleRowMetadata(task, state, tasks, copy = copy, locale = LocalMessageLocale.current), style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                ArrowText(if (warn) copy(UiMessages.scheduleTakeAction()) else copy(UiMessages.scheduleReadFullText()), p.blue, T.s15)
            }
        }
    }
}

/**
 * Swiped left, a task shows a blue 已读 (or 未读) and, swiped far, does it
 * ([canMark]); a tap opens it. A screen reader has the same as an action.
 */
@Composable
private fun SwipeToMark(canMark: Boolean, hasNew: Boolean, onMark: () -> Unit, onClick: () -> Unit, modifier: Modifier = Modifier, content: @Composable () -> Unit) {
    val copy = uiText()
    var offset by remember(canMark, hasNew) { mutableFloatStateOf(0f) }
    var width by remember { mutableIntStateOf(1) }
    val reveal = with(LocalDensity.current) { 88.dp.toPx() }
    val action = if (hasNew) copy(UiMessages.scheduleMarkAsRead()) else copy(UiMessages.scheduleMarkAsUnread())
    Box(modifier.fillMaxWidth().onSizeChanged { width = it.width }) {
        if (canMark && offset < 0) Row(Modifier.matchParentSize().background(pal.blue).padding(end = 16.dp),
            horizontalArrangement = Arrangement.End, verticalAlignment = Alignment.CenterVertically) {
            Row(Modifier.width(72.dp).fillMaxHeight().clickable { offset = 0f; onMark() }, horizontalArrangement = Arrangement.Center, verticalAlignment = Alignment.CenterVertically) {
                if (hasNew) { Ic(Lucide.Check, 16.dp, Color.White); Spacer(Modifier.width(4.dp)) }
                Text(if (hasNew) copy(UiMessages.scheduleAndroidRead()) else copy(UiMessages.scheduleAndroidUnread()), style = T.s15b, color = Color.White)
            }
        }
        Box(Modifier.offset { IntOffset(offset.roundToInt(), 0) }.fillMaxWidth().background(pal.paper)
            .then(if (canMark) Modifier.draggable(rememberDraggableState { delta -> offset = (offset + delta).coerceIn(-width.toFloat(), 0f) }, Orientation.Horizontal,
                onDragStopped = {
                    if (-offset >= width * .65f) { offset = 0f; onMark() }
                    else offset = if (-offset >= reveal / 2) -reveal else 0f
                }) else Modifier)
            .semantics { if (canMark) customActions = listOf(CustomAccessibilityAction(action) { onMark(); true }) }
            .clickable { if (offset != 0f) offset = 0f else onClick() }) { content() }
    }
}

/** A task in 其他任务 or 已结束, one row (design-system §6 会话行): title and time, two lines of summary, then how often and where. */
@Composable
fun TaskRow(task: ScheduledTask, state: RemoteState, now: Instant = Instant.now(), last: Boolean = false, hasNew: Boolean = false,
    tasks: List<ScheduledTask> = listOf(task), onMark: () -> Unit = {}, onClick: () -> Unit) {
    val copy = uiText()
    val p = pal
    val attention = taskGroup(task) == TaskGroup.ATTENTION
    val running = scheduleInProgress(task)
    val paused = task.status == "paused"
    SwipeToMark(scheduleCanMark(task, hasNew), hasNew, onMark, onClick) {
        Row(Modifier.fillMaxWidth().padding(start = 24.dp), verticalAlignment = Alignment.Top) {
            if (attention || hasNew) Box(Modifier.width(26.dp).padding(top = 20.dp)) { ScheduleResultDot(if (attention) p.orange else p.blue, if (attention) copy(UiMessages.scheduleNeedsYourAttention()) else copy(UiMessages.scheduleAndroidNewResult0653C4())) }
            Column(Modifier.weight(1f).then(if (last) Modifier else Modifier.bottomLine(p.sep)).padding(end = 24.dp, top = 13.dp, bottom = 13.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(task.title, Modifier.weight(1f), style = T.s17b, color = if (paused) p.ink3 else p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(scheduleNotificationTime(task, now, copy = copy), style = T.s13, color = p.ink2, maxLines = 1)
                }
                val preview = schedulePreview(task, now, copy = copy)
                if (preview.isNotEmpty()) Text(preview, modifier = Modifier.padding(top = 2.dp), style = T.s15, color = p.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
                Row(Modifier.padding(top = 3.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                    if (running) { Spinner(p.ink2, size = 12.dp); Text(copy(UiMessages.scheduleAndroidRunningLabel()), style = T.s13, color = p.ink2) }
                    Text(scheduleRowMetadata(task, state, tasks, copy = copy, locale = LocalMessageLocale.current), style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
        }
    }
}

@Composable
internal fun ScheduleResultDot(color: Color = pal.blue, description: String? = null) {
    val label = description ?: uiText(UiMessages.scheduleUnreadOnEntry())
    Box(Modifier.size(8.dp).background(color, CircleShape).semantics { contentDescription = label })
}

@Composable
private fun ScheduleOffline(state: RemoteState, tasks: List<ScheduledTask>) {
    val copy = uiText()
    // The connection bar speaks for a lost relay; this names the computers that are away.
    val away = tasks.map { it.machine }.distinct().filter { state.machines[it]?.online != true }
    if (state.conn == ConnState.ONLINE && away.isNotEmpty())
        Notice(away.joinToString("、") { machineLabel(state, it) } + " " + copy(SCHEDULE_OFFLINE))
}

@Composable
fun ScheduleDetailScreen(machine: String, id: String) {
    val copy = uiText()
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
        if (task == null) return@LaunchedEffect
        if (!resultCaptured && capturedDetail == ScheduleTaskKey(machine, id)) {
            selectedResult = task.latestNotification; resultCaptured = true
        } else if (resultCaptured && selectedResult == null) {
            // Entered with no result: nothing is held to keep, so the first one that comes is shown as it is.
            selectedResult = task.latestNotification
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
            catch (e: Exception) { error = displayError(e); uncertain = (e as? CommandException)?.unknown == true; if (e is ScheduleConflictException) { edit = ""; editBase = null } }
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
    val pinned = if (resultCaptured) selectedResult else null
    val result = pinned?.let { selected -> task?.allRuns?.firstOrNull { it.id == selected.id } ?: selected } ?: task?.latestNotification
    val resultText = result?.let { rememberScheduleResultText(it) }
    val enabled = writable && !busy && !uncertain
    val definitionEnabled = enabled && task?.let(::scheduleDefinitionAllowed) == true
    Column(Modifier.fillMaxSize().background(pal.paper)) {
        PageBar(task?.title ?: copy(UiMessages.queueAndroidScheduled()), nav.back, subtitle = task?.let { scheduleDetailSubtitle(it, state, t, copy = copy, locale = LocalMessageLocale.current) }, more = { menu = true })
        if (!state.loaded) ConnBanner(state)
        if (task == null) {
            Notice(snapshots[machine]?.error?.takeIf { it.isNotEmpty() }?.let { uiText(it) } ?: if (snapshots[machine]?.loading == true) copy(UiMessages.scheduleReadingTasks()) else copy(UiMessages.scheduleAndroidTaskNotFoundItMayHaveBeenDeletedOnAnother()))
            return@Column
        }
        Column(Modifier.weight(1f).fillMaxWidth().readableColumn().verticalScrollFaded()) {
            // Reveal the card and earlier runs together, never an interim summary that changes height.
            if (result != null && resultText == null) {
                Skeleton()
                return@Column
            }
            if (error.isNotEmpty()) Notice(error, tone = NoticeTone.ERROR)
            if (uncertain) TextAction(copy(UiMessages.scheduleAndroidReadAgain()), { scope.launch(Dispatchers.Main.immediate) { remote.schedules.refresh(machine); if (remote.schedules.state.value[machine]?.error.isNullOrEmpty()) uncertain = false } }, Modifier.padding(horizontal = 16.dp))
            if (attention) {
                ScheduleAttentionBanner(scheduleAttentionText(task, t, copy = copy))
                if (!targetMissing) PillButton(if (onceFailed) copy(UiMessages.scheduleRunOnceNow()) else copy(UiMessages.scheduleResume()), { recover() }, Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp), ButtonKind.INK, enabled)
            }
            if (pinned != null && task.latestNotification != null && task.latestNotification?.id != pinned.id) {
                TextAction(copy(UiMessages.scheduleAndroidNewResult0653C4()), { selectedResult = task.latestNotification }, Modifier.fillMaxWidth().padding(horizontal = 16.dp), style = T.s13)
            }
            task.active?.let { run ->
                Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                    Row(Modifier.fillMaxWidth().padding(start = 14.dp, end = 6.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        Mark(Lucide.LoaderCircle, pal.blue, 14.dp)
                        Text(copy(UiMessages.scheduleRunningSince((scheduleClock(run.time)).toString())), Modifier.weight(1f), style = T.s13, color = pal.blue)
                        if (run.session.isNotEmpty()) TextAction(copy(UiMessages.scheduleConversation()), { nav.scheduledSession(run.session, run.seq) }, style = T.s13, arrow = true)
                    }
                }
            }
            if (result != null) ScheduleLatestResult(result, resultText.orEmpty(), t, capturedDetail == ScheduleTaskKey(machine, id) && result.id in entryUnread,
                showConversation = !(task.target.text("type") == "resume_session" && task.active != null)) {
                nav.scheduledSession(result.session, result.seq)
            }
            val earlier = task.allRuns.filter { it.id != result?.id && it.id != task.active?.id &&
                (!it.notified || result != null && it.notificationTime <= result.notificationTime) }.sortedByDescending { it.notificationTime }
            var allRuns by remember(task.id) { mutableStateOf(false) }
            if (earlier.isNotEmpty()) {
                Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp).heightIn(min = 44.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(copy(UiMessages.scheduleEarlier()), Modifier.weight(1f), style = T.s13b, color = pal.ink2)
                    Text(copy(UiMessages.scheduleRunsCount((earlier.size).toInt())), style = T.s13, color = pal.ink2)
                }
                Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                    Column {
                        scheduleRunGroups(if (allRuns) earlier else earlier.take(5)).forEach { group -> ScheduleRunGroup(group, t, entryUnread) { run -> if (run.session.isNotEmpty()) nav.scheduledSession(run.session, run.seq) } }
                        if (!allRuns && earlier.size > 5) TextAction(copy(UiMessages.scheduleViewAllRuns((earlier.size).toInt())), { allRuns = true }, Modifier.fillMaxWidth())
                    }
                }
            } else if (result == null && task.active == null) Hint(copy(UiMessages.scheduleHasNotRunYet()))
            Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                Column {
                    Row(Modifier.fillMaxWidth().bottomLine(pal.sep).padding(horizontal = 14.dp), verticalAlignment = Alignment.CenterVertically) {
                        Text(copy(UiMessages.scheduleWhatShouldTheAgentDo()), Modifier.weight(1f), style = T.s15, color = pal.ink)
                        TextAction(copy(UiMessages.toolsAndroidEdit()), { beginEdit("message") }, enabled = definitionEnabled, style = T.s13)
                    }
                    ScheduleDescription(task.message, Modifier.padding(horizontal = 14.dp, vertical = 12.dp))
                }
            }
            Label(copy(UiMessages.commonSettings()))
            Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                Column {
                    ScheduleSetting(copy(UiMessages.scheduleWhen()), scheduleTiming(task.trigger, copy = copy, locale = LocalMessageLocale.current), definitionEnabled) { beginEdit("time") }
                    ToggleRow(copy(UiMessages.scheduleNotifyOnCompletion()), "", task.notifyOnFinish, enabled = enabled, onChange = { on -> if (enabled) { editBase = task; operate("update", JsonObject(task.spec + ("notify" to JsonPrimitive(if (on) "finish" else "agent_only")))) } })
                    val end = scheduleLimits(task, t, copy = copy)
                    if (end.isNotEmpty()) ScheduleSetting(copy(UiMessages.sessionEnd()), end, definitionEnabled) { beginEdit("time") }
                    if (task.target.text("type") == "resume_session") {
                        ScheduleSetting(copy(UiMessages.scheduleSendToSchedules()), scheduleDestination(task, state, copy = copy), enabled && state.sessions[task.target.text("session")]?.info != null) { nav.session(task.target.text("session")) }
                    } else ScheduleSetting(copy(UiMessages.scheduleRunIn()), taskLocation(task, state, copy = copy), definitionEnabled) { beginEdit("config") }
                    Box(Modifier.bottomLine(pal.sep)) {
                        GRow(copy(UiMessages.newSessionPermissions()), taskPermission(task, state, copy = copy), onClick = if (task.target.text("type") == "new_session" && definitionEnabled) ({ beginEdit("mode") }) else null,
                            chevron = task.target.text("type") == "new_session" && definitionEnabled, enabled = definitionEnabled, singleLineValue = true)
                    }
                    ScheduleSetting(copy(UiMessages.scheduleTaskName()), task.title, scheduleRenameAllowed(writable, busy || uncertain)) { beginEdit("name") }
                }
            }
            Hint(scheduleCreation(task, t, copy = copy))
            if (!writable) Hint(copy(UiMessages.scheduleAndroidConnectValue1ToEditRunOrDelete((machineLabel(state, machine)).toString())), Modifier.padding(bottom = 8.dp))
            Hint(copy(UiMessages.scheduleAndroidRunOnceNowDoesNotCountTowardAutomaticRunsOr()), Modifier.padding(bottom = 8.dp))
        }
    }
    if (task != null) {
        if (menu) ActionSheet(info = { SheetInfo(task.title, taskLocation(task, state, copy = copy) + " · " + scheduleCreation(task, t, copy = copy)) }, groups = listOf(buildList {
            add(SheetAction(copy(UiMessages.scheduleRunOnceNow()), enabled = enabled && (scheduleRunAllowed(task) || scheduleFailedOnce(task))) { operate("run") })
            add(SheetAction(copy(UiMessages.scheduleMarkAsUnread()), enabled = task.latestNotification != null) { remote.schedules.markUnread(machine, id); nav.schedules() })
            if (taskGroup(task) != TaskGroup.ENDED && !onceFailed) add(SheetAction(if (task.status == "paused" || attention) copy(UiMessages.scheduleResume()) else copy(UiMessages.schedulePause()), enabled = enabled) {
                if (task.status == "paused" || attention) recover() else operate("pause")
            })
        }, listOf(SheetAction(copy(UiMessages.scheduleDeleteTask()), danger = true, enabled = enabled) { deleting = true })), onDismiss = { menu = false })
        val base = editBase ?: task
        if (edit == "name") ScheduleRenameDialog(base, { edit = "" }, enabled = scheduleRenameAllowed(writable, busy || uncertain)) { name ->
            check(scheduleRenameAllowed(writable, busy || uncertain)) { copy(UiMessages.scheduleAndroidThisTaskCannotBeRenamedSeparately()) }
            busy = true
            try { remote.schedules.rename(base, name) }
            catch (e: Exception) { uncertain = (e as? CommandException)?.unknown == true; if (e is ScheduleConflictException) { edit = ""; editBase = null; error = displayError(e) }; throw e }
            finally { busy = false }
        }
        if (edit == "time") ScheduleTimeSheet(base.trigger, { operate("update", JsonObject(base.spec + ("trigger" to it))) }, { edit = "" }, enabled = definitionEnabled)
        if (edit == "message") ScheduleMessageEditor(base.message, busy, error, { edit = "" }, { text -> operate("update", JsonObject(base.spec + ("message" to JsonPrimitive(text)))) }, writable = definitionEnabled)
        if (edit == "config" || edit == "mode") {
            val cfg = scheduleConfig(base)
            NewSettingsSheet(scheduleEditingState(state, machine), cfg.cwd, projectName(cfg.cwd, copy = copy), machine, cfg.agent, if (edit == "mode") "mode" else "",
                onDone = { op ->
                    val picked = op?.sets?.firstOrNull { it.agent == op.agent }
                    if (picked == null) edit = "" else operate("update", JsonObject(base.spec + ("target" to scheduleTarget(agentConfig(machine, cfg.cwd, picked), base.target))))
                }, onClose = { edit = "" }, initial = cfg, scheduling = true, lockMachine = true, chooseAgent = true)
        }
        if (deleting) Confirm(copy(UiMessages.scheduleAndroidDeleteThisTask()), copy(UiMessages.scheduleAndroidStopFutureSchedulingSentMessagesAndOpenedConversationsAreKept()), copy(UiMessages.scheduleDelete()), true, { deleting = false }) { deleting = false; operate("cancel") }
    }
}

/** Shares the result cards' content inset, inside the same constrained detail column. */
@Composable
internal fun ScheduleAttentionBanner(text: String, modifier: Modifier = Modifier) {
    Box(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 8.dp).then(modifier)
        .clip(Radii.R14).background(pal.gbg).padding(horizontal = 14.dp, vertical = 12.dp)) {
        Text(text, style = T.s13, color = pal.orangeInk)
    }
}

/** Expands only for this visit; a complete description remains selectable. */
@Composable
fun ScheduleDescription(message: String, modifier: Modifier = Modifier) {
    val copy = uiText()
    var expanded by remember(message) { mutableStateOf(false) }
    var overflow by remember(message) { mutableStateOf(false) }
    Column(modifier) {
        Text(message, style = T.s15, color = pal.ink, maxLines = if (expanded) Int.MAX_VALUE else 3,
            overflow = TextOverflow.Ellipsis, onTextLayout = { if (!expanded) overflow = it.hasVisualOverflow })
        if (expanded || overflow) Row(Modifier.heightIn(min = 44.dp).clickable { expanded = !expanded }, verticalAlignment = Alignment.CenterVertically) {
            Text(if (expanded) copy(UiMessages.markdownCollapse()) else copy(UiMessages.markdownExpand()), style = T.s13b, color = pal.blue)
            Ic(if (expanded) Lucide.ChevronUp else Lucide.ChevronDown, 14.dp, pal.blue)
        }
    }
}

@Composable
fun ScheduleMessageEditor(initial: String, busy: Boolean, error: String, close: () -> Unit, save: (String) -> Unit, writable: Boolean = true) {
    val copy = uiText()
    var text by remember { mutableStateOf(initial) }
    var discard by remember { mutableStateOf(false) }
    fun cancel() { if (text != initial) discard = true else close() }
    Dialog({ if (!busy) cancel() }, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        val view = LocalView.current
        val light = pal.ink.luminance() < .5f
        SideEffect {
            (view.parent as? DialogWindowProvider)?.window?.let { window ->
                WindowCompat.getInsetsController(window, view).apply { isAppearanceLightStatusBars = light; isAppearanceLightNavigationBars = light }
            }
        }
        BackHandler { if (!busy) cancel() }
        Column(Modifier.fillMaxSize().background(pal.paper).safeDrawingPadding().imePadding()) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp), verticalAlignment = Alignment.CenterVertically) {
                TextAction(copy(UiMessages.commonCancel()), { cancel() }, enabled = !busy)
                Text(copy(UiMessages.scheduleWhatShouldTheAgentDo()), Modifier.weight(1f), style = T.s15b, color = pal.ink, textAlign = androidx.compose.ui.text.style.TextAlign.Center)
                TextAction(if (busy) copy(UiMessages.scheduleAndroidSaving()) else copy(UiMessages.commonSave()), { save(text) }, enabled = !busy && writable && scheduleMessageValid(text))
            }
            if (error.isNotEmpty()) Notice(error, tone = NoticeTone.ERROR)
            BasicTextField(text, { text = it }, Modifier.weight(1f).fillMaxWidth().padding(16.dp),
                textStyle = T.s15.copy(color = pal.ink), cursorBrush = SolidColor(pal.blue), readOnly = busy || !writable)
            if (text.toByteArray(Charsets.UTF_8).size > SCHEDULE_MESSAGE_MAX_BYTES) Hint(copy(UiMessages.scheduleTooLongMaximum32Kb()), Modifier.padding(vertical = 16.dp), inset = 16.dp)
        }
        if (discard) Confirm(copy(UiMessages.scheduleDiscardChanges()), copy(UiMessages.scheduleAndroidChangesHaveNotBeenSaved()), copy(UiMessages.newSessionAndroidAbandon()), true, { discard = false }) { discard = false; close() }
    }
}

@Composable
internal fun ScheduleRunGroup(group: RunGroup, now: Instant, entryUnread: Set<String> = emptySet(), open: (ScheduledRun) -> Unit) {
    val copy = uiText()
    var expanded by rememberSaveable(group.key) { mutableStateOf(false) }
    if (group.quiet && group.runs.size > 1) {
        Box(Modifier.bottomLine(pal.sep)) {
            GRow(copy(UiMessages.scheduleQuietRuns((group.runs.size).toInt(), (scheduleDay(group.runs.first().notificationTime, now, copy = copy)).toString())), singleLineValue = true, onClick = { expanded = !expanded }, trailing = {
                Ic(if (expanded) Lucide.ChevronUp else Lucide.ChevronDown, 16.dp, pal.ink2)
            })
        }
        val latest = group.runs.first()
        Text(copy(UiMessages.scheduleLatestSummary(uiText(latest.foldedSummary), (scheduleClock(latest.notificationTime)).toString())), style = T.s12, color = pal.ink2, modifier = Modifier.padding(start = 14.dp, end = 14.dp, bottom = 12.dp))
        if (!expanded) return
    }
    group.runs.forEach { run ->
        Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).then(if (run.session.isNotEmpty()) Modifier.clickable { open(run) } else Modifier)
            .bottomLine(pal.sep).padding(horizontal = 14.dp, vertical = 12.dp), verticalAlignment = Alignment.Top) {
            Box(Modifier.width(22.dp).padding(top = 5.dp), contentAlignment = Alignment.TopCenter) { if (run.id in entryUnread) ScheduleResultDot() }
            Column(Modifier.weight(1f)) {
                Text(scheduleWhen(run.notificationTime, now, copy = copy), style = T.s13, color = pal.ink2)
                Text(uiText(run.summary.ifEmpty { run.foldedSummary }), style = T.s15, color = pal.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
            if (run.session.isNotEmpty()) Ic(Lucide.ChevronRight, 16.dp, pal.ink3)
        }
    }
}

@Composable
fun ScheduleSetting(label: String, value: String, enabled: Boolean = true, onClick: () -> Unit) {
    Box(Modifier.bottomLine(pal.sep)) {
        GRow(label, value, onClick = onClick, chevron = enabled, enabled = enabled, singleLineValue = true)
    }
}

/** A CLI setting uses the same grouped row and small segments as the settings page. */
@Composable
internal fun ScheduleAgentSetting(agents: List<Agent>, selected: Agent, enabled: Boolean, onSelect: (Agent) -> Unit) {
    val copy = uiText()
    if (agents.size < 2) return
    Box(Modifier.bottomLine(pal.sep)) {
        GRow("CLI", trailing = {
            SegPick(agents.map { agentName(it, copy = copy) }, agents.indexOf(selected), enabled) { onSelect(agents[it]) }
        })
    }
}

fun scheduleConfig(task: ScheduledTask): NewConfig = NewConfig(
    task.machine,
    task.target.text("cwd"),
    (task.target.text("agent").takeIf { it.isNotEmpty() }?.let(::agentById) ?: agentInfo()).agent,
    task.target.text("model"),
    task.target.text("effort"),
    when(task.target.text("mode")) { "ask" -> Mode.ASK; "auto" -> Mode.AUTO; "full" -> Mode.FULL; else -> null }
)
fun scheduleTarget(cfg: NewConfig, base: JsonObject = JsonObject(emptyMap())): JsonObject {
    val fields = base.toMutableMap()
    fields.putAll(buildJsonObject {
        put("type", "new_session"); put("cwd", cfg.cwd); put("agent", agentInfo(cfg.agent).id)
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
    val copy = uiText()
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
    val blocked = machine?.let { if (!scheduleSupported(state.machines[it])) copy(UiMessages.scheduleAndroidUpdateRemoteAgentOnThisComputerToManageScheduledTasks()) else if (!writable) copy(SCHEDULE_OFFLINE) else null }
    Column(Modifier.fillMaxSize().background(pal.paper)) {
        if (current) SheetHead(copy(UiMessages.scheduleScheduleMessagesToThisConversation()), copy(UiMessages.commonCancel()), onClose) else PageBar(copy(UiMessages.homeNewTask()), onClose)
        ConnBanner(state)
        FormColumn(Modifier.weight(1f).fillMaxWidth().readableColumn().imePadding().verticalScrollFaded()) {
            if (error.isNotEmpty()) Notice(error, tone = NoticeTone.ERROR)
            if (blocked != null) Notice(blocked, tone = NoticeTone.WARN)
            FieldLabel(copy(UiMessages.scheduleWhatShouldTheAgentDo()))
            InputField(message, { if (editable) message = it }, copy(UiMessages.scheduleAndroidCheckProjectUpdatesAndNotifyMeOfNoteworthyChanges()), maxLines = if (current) 4 else 8)
            if (message.toByteArray(Charsets.UTF_8).size > SCHEDULE_MESSAGE_MAX_BYTES) Hint(copy(UiMessages.scheduleTooLongMaximum32Kb()))
            FieldLabel(copy(UiMessages.scheduleTaskName()))
            InputField(title, { if (editable) title = scheduleNameInput(it) }, if (message.isBlank()) copy(UiMessages.scheduleLeaveBlankToUseTheMessageSFirstSentence()) else copy(UiMessages.scheduleIfBlankUses((scheduleName("", message)).toString())), singleLine = true)
            ToggleRow(copy(UiMessages.scheduleNotifyOnCompletion()), "", notifyFinish, enabled = editable) { notifyFinish = it }
            ScheduleSetting(copy(UiMessages.scheduleWhen()), scheduleTiming(trigger, copy = copy, locale = LocalMessageLocale.current), editable) { timeOpen = true }
            if (!current) {
                ScheduleSetting(copy(UiMessages.scheduleProject()), project.takeIf { it.isNotEmpty() }?.let { projectName(it, copy = copy) } ?: copy(UiMessages.scheduleAndroidChooseAProject()), editable) { pickProject = true }
                if (cfg != null) {
                    ScheduleAgentSetting(machineAgents(state.machines[cfg.machineId]), cfg.agent, editable) { agent -> config = projectConfig(state, project, agent, cfg.machineId) }
                    val permission = effectiveModeText(cfg.mode, defaultModeOf(state.machines[cfg.machineId], cfg.agent), copy = copy)
                    ScheduleSetting(copy(UiMessages.newSessionPermissions()), permission, editable) { settings = "mode" }
                    val model = listOf(cfg.model.ifEmpty { copy(UiMessages.newSessionDefaultModel()) }, cfg.effort).filter { it.isNotEmpty() }.joinToString(" · ")
                    ScheduleSetting(if (state.machines.size > 1) copy(UiMessages.scheduleComputerAndModel()) else copy(UiMessages.modelsTitle()), if (state.machines.size > 1) "${machineLabel(state, cfg.machineId)} · $model" else model, editable) { settings = if (state.machines.size > 1) "" else "model" }
                }
                Hint(copy(UiMessages.scheduleAndroidEachRunOpensANewConversationUnderThisTaskOutside()))
                if (machine == null && state.machines.values.any { !scheduleSupported(it) }) Hint(copy(UiMessages.scheduleAndroidUpdateRemoteAgentOnTheComputerToCreateScheduledTasks()))
            } else Hint(copy(UiMessages.scheduleAndroidTheMessageIsSentAfterTheCurrentTurnEndsWithout()))
            val plan = schedulePlanBlocked(current,cfg,state)
            if (plan) Hint(copy(UiMessages.schedulePlanModeCannotCreateExecutionTasksChooseOtherPermissions()))
            if (!current && cfg != null && resolvedScheduleConfig(cfg, state).mode == null) Hint(copy(UiMessages.scheduleSelectExplicitPermissionsBeforeCreatingAScheduledTask()))
            val ready = !plan && (current || cfg?.let { resolvedScheduleConfig(it, state).mode != null } == true) && writable && !busy && (scheduleMessageValid(message) || frozen != null) && (current || cfg != null && (config != null || start?.settled == true))
            PillButton(if (busy) copy(UiMessages.scheduleAndroidCreating()) else if (frozen != null) copy(UiMessages.scheduleRetryCreation()) else copy(UiMessages.scheduleCreate()), {
                if (!ready) return@PillButton
                val spec = frozen?.let { Json.parseToJsonElement(it).jsonObject } ?: taskSpec(scheduleName(title, message), message, trigger,
                    if (current) buildJsonObject { put("type", "resume_session"); put("session", session) } else scheduleTarget(resolvedScheduleConfig(cfg!!, state))).let { JsonObject(it + ("notify" to JsonPrimitive(if (notifyFinish) "finish" else "agent_only"))) }.also { frozen = it.toString() }
                busy = true; error = ""
                scope.launch(Dispatchers.Main.immediate) {
                    try {
                        val result = remote.schedules.operate(machine!!, "create", spec = spec, requestId = request)
                        onClose()
                        if (result != null) nav.task(machine, result.id)
                    } catch (e: kotlinx.coroutines.CancellationException) { throw e }
                    catch (e: Exception) { error = displayError(e); if (e is CommandException && e.fromDaemon && !e.unknown) frozen = null }
                    finally { busy = false }
                }
            }, Modifier.fillMaxWidth().padding(vertical = 16.dp), ButtonKind.INK, ready)
            if (frozen != null) Hint(copy(UiMessages.scheduleAndroidCreationIsUnconfirmedRetryingUsesTheSameRequestYouCan()))
        }
    }
    if (timeOpen) ScheduleTimeSheet(trigger, { triggerText = it.toString(); timeOpen = false }, { timeOpen = false })
    if (pickProject) Sheet({ pickProject = false }) {
        SheetHead(copy(UiMessages.projectsChoose()), copy(UiMessages.commonCancel()), { pickProject = false })
        ProjectPickList(state, { path -> project = path; config = null; pickProject = false }, { pickProject = false; pickDirectory = true })
    }
    if (pickDirectory) Sheet({ pickDirectory = false }, scroll = false) {
        DirPickerBody(state, copy(UiMessages.scheduleAndroidChooseDirectory())) { m, path -> project = path; config = NewConfig(m, path, machineAgents(state.machines[m]).first()); pickDirectory = false }
    }
    val settingsPage = settings
    if (settingsPage != null && cfg != null) NewSettingsSheet(state, project, projectName(project, copy = copy), cfg.machineId, cfg.agent, settingsPage,
        onDone = { op ->
            if (op != null) {
                config = agentConfig(op.machineId, project, op.sets.single { it.agent == op.agent })
            }
            settings = null
        }, onClose = { settings = null }, initial = cfg, scheduling = true)
}

@Composable
fun ScheduleTimeSheet(initial: JsonObject, done: (JsonObject) -> Unit, close: () -> Unit, enabled: Boolean = true) {
    val copy = uiText()
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
            Text(copy(UiMessages.scheduleWhen()), Modifier.weight(1f), style = T.s17b, color = pal.ink)
            TextAction(copy(UiMessages.commonDone()), {
                try { done(if (keepTurnDelay || value == original && !replaced) initial else value.trigger()) } catch (e: Exception) { error = if (e is io.github.lewin671.remoteagent.core.MessageFailure) e.display.resolve(copy) else e.message ?: copy(UiMessages.scheduleAndroidCheckTheTime()) }
            }, enabled = enabled && (keepTurnDelay || value.kind != 2 || value.weekdays.isNotEmpty()))
        }
        SegPick(listOf(copy(UiMessages.scheduleOnceSchedules()), copy(UiMessages.scheduleDaily()), copy(UiMessages.scheduleWeeklyOn()), copy(UiMessages.scheduleEvery())), if (keepTurnDelay) -1 else value.kind) { replaced = true; value = value.copy(kind = it) }
        if (keepTurnDelay) {
            Hint(copy(UiMessages.scheduleAndroidCurrentlyValue1FinishingDirectlyPreservesTheOriginalRuleChooseA((scheduleFrequency(initial, copy = copy)).toString())))
        } else {
        if (error.isNotEmpty()) Notice(error, tone = NoticeTone.ERROR)
        if (value.kind == 0) ScheduleSetting(copy(UiMessages.scheduleAndroidDate()), value.date) { date(value.date) { value = value.copy(date = it) } }
        if (value.kind != 3) {
            ScheduleSetting(copy(UiMessages.scheduleTime()), value.time) {
                val t = runCatching { LocalTime.parse(value.time) }.getOrElse { LocalTime.of(8, 0) }
                android.app.TimePickerDialog(context, { _, h, m -> value = value.copy(time = "%02d:%02d".format(h, m)) }, t.hour, t.minute, true).show()
            }
        } else {
            FieldLabel(copy(UiMessages.scheduleAndroidInterval()))
            InputField(value.interval, { value = value.copy(interval = it) }, copy(UiMessages.scheduleAndroidPositiveInteger()))
            GRow(copy(UiMessages.scheduleAndroidUnit()), trailing = { SegPick(listOf(copy(UiMessages.scheduleAndroidMinutes()), copy(UiMessages.scheduleAndroidHours()), copy(UiMessages.scheduleAndroidDays()), copy(UiMessages.scheduleAndroidSeconds())), value.unit) { value = value.copy(unit = it) } })
        }
        if (value.kind == 1) ToggleRow(copy(UiMessages.scheduleWeekdaysOnly()), copy(UiMessages.scheduleAndroidMondayToFriday()), value.workdays) { value = value.copy(workdays = it) }
        if (value.kind == 2) {
            Row(Modifier.fillMaxWidth().clip(Radii.R14).background(pal.fill2).padding(2.dp), horizontalArrangement = Arrangement.spacedBy(2.dp)) {
                (1..7).forEach { day ->
                    val selected = day in value.weekdays
                    Box(Modifier.weight(1f).heightIn(min = 44.dp).clip(Radii.R14).background(if (selected) (if (pal.paper == androidx.compose.ui.graphics.Color.White) pal.paper else pal.fill2) else androidx.compose.ui.graphics.Color.Transparent)
                        .toggleable(value = selected, role = Role.Checkbox) { value = value.copy(weekdays = if (day in value.weekdays) value.weekdays - day else value.weekdays + day) }, contentAlignment = Alignment.Center) {
                        Text(listOf(copy(UiMessages.scheduleMon()), copy(UiMessages.scheduleTue()), copy(UiMessages.scheduleWed()), copy(UiMessages.scheduleThu()), copy(UiMessages.scheduleFri()), copy(UiMessages.scheduleSat()), copy(UiMessages.scheduleSun()))[day-1], style = if (selected) T.s13b else T.s13, color = if (selected) pal.ink else pal.ink2)
                    }
                }
            }
        }
        if (value.kind != 0) {
            GRow(copy(UiMessages.sessionEnd()), trailing = { SegPick(listOf(copy(UiMessages.scheduleNever()), copy(UiMessages.scheduleAfterARunCount()), copy(UiMessages.scheduleEndDate())), value.end) { value = value.copy(end = it) } })
            if (value.end == 1) InputField(value.count, { value = value.copy(count = it) }, copy(UiMessages.scheduleAndroidAutomaticRunCount()))
            if (value.end == 2) ScheduleSetting(copy(UiMessages.scheduleAndroidEndDate()), value.until) { date(value.until) { value = value.copy(until = it, deadline = "") } }
        }
        if (value.kind != 3) {
            FieldLabel(copy(UiMessages.scheduleTimeZone()))
            InputField(value.zone, { value = value.inZone(it.trim()) }, copy(UiMessages.scheduleAndroidForExampleAsiaShanghai()))
        } else Hint(copy(SCHEDULE_INTERVAL_HINT))
        }
    }
}

@Composable
fun SessionScheduleStrip(sid: String, modifier: Modifier = Modifier) {
    val copy = uiText()
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val snapshots by remote.schedules.state.collectAsState()
    val tasks = snapshots.values.flatMap { it.tasks }.filter { scheduleSupported(state.machines[it.machine]) && it.target.text("type") == "resume_session" && it.target.text("session") == sid && taskGroup(it) != TaskGroup.ENDED }
    if (tasks.isEmpty()) return
    val nav = LocalNav.current
    val next = tasks.minByOrNull { it.next ?: Instant.MAX }!!
    val text = sessionScheduleText(tasks, state, Instant.ofEpochMilli(now()), copy = copy)
    var choosing by remember { mutableStateOf(false) }
    if (choosing) Sheet({ choosing = false }) {
        SheetHead(copy(UiMessages.queueAndroidScheduled()), copy(UiMessages.commonCancel()), { choosing = false })
        tasks.forEach { task -> GRow(task.title, onClick = { choosing = false; nav.task(task.machine, task.id) }, chevron = true, singleLineValue = true) }
    }
    Strip(Lucide.Clock, text, modifier, action = copy(UiMessages.scheduleManage()), onClick = {
        if (tasks.size == 1) nav.task(next.machine, next.id) else choosing = true
    })
}
