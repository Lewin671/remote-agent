package io.github.lewin671.remoteagent.ui

import android.content.Context
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.Orientation
import androidx.compose.foundation.gestures.draggable
import androidx.compose.foundation.gestures.rememberDraggableState
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.luminance
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.ui.window.DialogWindowProvider
import androidx.core.view.WindowCompat
import androidx.lifecycle.compose.LifecycleStartEffect
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import java.time.LocalDate
import java.time.LocalTime
import kotlin.math.roundToInt

private enum class ScheduleConfigFocus { ALL, MODE, MODEL }

private fun schedItemKey(prefix: String, item: ScheduleTaskItemView): String =
    listOf(prefix, item.machineId, item.taskId).joinToString("/")

/** 定时 (design-system §6 定时任务): renders Core's [SchedulesViewModel] directly. */
@Composable
fun SchedulesScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val vm by remote.schedulesView.collectAsState()
    val prefs = LocalContext.current.let {
        remember(it) { it.getSharedPreferences("schedule-display", Context.MODE_PRIVATE) }
    }
    LaunchedEffect(remote) {
        if (prefs.contains("ended-open")) {
            val saved = prefs.getBoolean("ended-open", false)
            if (saved != vm.endedExpanded) remote.coreHost.sendEvent {
                setScheduleToggleEnded(ScheduleBoolAction.newBuilder().setValue(saved))
            }
        }
    }
    LifecycleStartEffect(remote) {
        remote.coreHost.openScheduleList(true)
        onStopOrDispose {}
    }
    SchedulesScreenContent(
        vm = vm,
        bottom = {
            ShellBottom(ShellTab.SCHEDULES, uiText(UiMessages.homeNewSchedule()), Lucide.Plus, vm.canCreate) {
                nav.newTask("")
            }
        },
        connBanner = { if (!state.loaded) ConnStrip(state) },
        onEvent = { block ->
            val ev = CoreEvent.newBuilder().apply(block).build()
            if (ev.hasScheduleToggleEnded()) prefs.edit().putBoolean("ended-open", ev.scheduleToggleEnded.value).apply()
            remote.coreHost.dispatch(ev)
        },
        onOpenTask = { item ->
            remote.coreHost.selectScheduleTask(item.machineId, item.taskId)
            nav.task(item.machineId, item.taskId)
        },
    )
}

@Composable
internal fun SchedulesScreenContent(
    vm: SchedulesViewModel,
    bottom: @Composable () -> Unit = {},
    connBanner: @Composable () -> Unit = {},
    onEvent: (CoreEvent.Builder.() -> Unit) -> Unit = {},
    onOpenTask: (ScheduleTaskItemView) -> Unit = {},
) {
    val copy = uiText()
    val headline = vm.headline
    val attention = vm.attention14List
    val fresh = vm.freshList
    val others = vm.othersList
    val ended = vm.endedList

    fun markRead(item: ScheduleTaskItemView) = onEvent {
        setScheduleMarkRead(
            ScheduleMarkReadAction.newBuilder().addTasks(
                ScheduleTaskRef.newBuilder().setMachineId(item.machineId).setTaskId(item.taskId),
            ),
        )
    }

    fun markUnread(item: ScheduleTaskItemView) = onEvent {
        setScheduleMarkUnread(
            ScheduleTaskBoolAction.newBuilder().setMachineId(item.machineId).setTaskId(item.taskId).setValue(true),
        )
    }

    ShellPage(Modifier.fillMaxSize(), bottom = bottom) {
        LazyColumn(Modifier.fillMaxSize().readableColumn(), contentPadding = shellPadding(top = false)) {
            item("headline") {
                Text(
                    buildAnnotatedString {
                        if (headline.kind == ScheduleHeadlineKind.SCHEDULE_HEADLINE_KIND_STOPPED && headline.highlightCount > 0) {
                            val tpl = copy(UiMessages.scheduleStoppedCount(headline.highlightCount, "\uFFFC"))
                            append(tpl.substringBefore('\uFFFC'))
                            withStyle(SpanStyle(color = pal.orange)) { append(headline.highlightCount.toString()) }
                            append(tpl.substringAfter('\uFFFC'))
                        } else {
                            append(uiText(headline.title))
                        }
                    },
                    style = T.title, color = pal.ink,
                    modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 20.dp),
                )
                if (headline.sub.isCoreTextNotEmpty()) {
                    Text(uiText(headline.sub), style = T.s17, color = pal.ink2, modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 7.dp))
                }
                Column(Modifier.padding(start = 16.dp, end = 16.dp, top = 10.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    connBanner()
                    if (vm.unsupportedNotice.isCoreTextNotEmpty()) Notice(uiText(vm.unsupportedNotice))
                    if (vm.offlineNotice.isCoreTextNotEmpty()) Notice(uiText(vm.offlineNotice))
                    vm.machineErrorsList.forEach { err ->
                        Notice(
                            uiText(err.error),
                            action = if (err.canRetry) copy(UiMessages.commonRetry()) else null,
                            onAction = { onEvent { setScheduleRefresh(ScheduleStringAction.newBuilder().setValue(err.machineId)) } },
                        )
                    }
                }
                if (vm.showEmptyHint && vm.emptyHint.isCoreTextNotEmpty()) {
                    Text(uiText(vm.emptyHint), style = T.s17, color = pal.ink2, modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 16.dp))
                }
            }
            items(attention, key = { schedItemKey("stop", it) }) { item ->
                ScheduleCard(item, warn = true, onMark = { if (item.actions.canMarkRead) markRead(item) else markUnread(item) }) { onOpenTask(item) }
            }
            if (fresh.isNotEmpty()) {
                item("fresh") {
                    ScheduleSection(
                        copy(UiMessages.scheduleNewResultsSchedules()),
                        if (vm.canMarkAllRead) copy(UiMessages.scheduleMarkAllAsRead()) else null,
                    ) { onEvent { setScheduleMarkRead(ScheduleMarkReadAction.newBuilder().setAllFresh(true)) } }
                }
            }
            items(fresh, key = { schedItemKey("new", it) }) { item ->
                ScheduleCard(item, warn = false, onMark = { markRead(item) }) { onOpenTask(item) }
            }
            if (others.isNotEmpty()) item("others") { ScheduleSection(uiText(vm.othersSectionTitle)) }
            items(others, key = { schedItemKey("o", it) }) { item ->
                TaskRow(item, last = item == others.last(), onMark = { markUnread(item) }) { onOpenTask(item) }
            }
            if (vm.totalEnded > 0 || ended.isNotEmpty()) {
                item("ended") {
                    Row(
                        Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable {
                            onEvent { setScheduleToggleEnded(ScheduleBoolAction.newBuilder().setValue(!vm.endedExpanded)) }
                        }.padding(horizontal = 24.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) { ArrowText(uiText(vm.endedLabel), pal.ink2, T.s15, down = !vm.endedExpanded, up = vm.endedExpanded) }
                }
                if (vm.endedExpanded) {
                    items(ended, key = { schedItemKey("end", it) }) { item ->
                        TaskRow(item, last = item == ended.last(), onMark = { markUnread(item) }) { onOpenTask(item) }
                    }
                }
            }
        }
    }
}

@Composable
private fun ScheduleSection(text: String, action: String? = null, onAction: () -> Unit = {}) {
    Row(Modifier.fillMaxWidth().padding(start = 24.dp, end = 12.dp, top = 22.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text, style = T.s15b, color = pal.ink, modifier = Modifier.weight(1f))
        if (action != null) TextAction(action, onAction, style = T.s15, alignment = Alignment.Center, modifier = Modifier.padding(horizontal = 12.dp))
    }
}

@Composable
internal fun ScheduleCard(item: ScheduleTaskItemView, warn: Boolean, onMark: () -> Unit, onClick: () -> Unit) {
    val copy = uiText()
    val p = pal
    SwipeToMark(item.actions.canMarkRead || item.actions.canMarkUnread, item.actions.canMarkRead, onMark, onClick,
        Modifier.padding(start = 16.dp, end = 16.dp, top = 12.dp).clip(Radii.R28)) {
        Column(Modifier.fillMaxWidth().background(p.gbg).padding(18.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                ScheduleResultDot(if (warn) p.orange else p.blue, if (warn) copy(UiMessages.scheduleNeedsYourAttention()) else copy(UiMessages.scheduleAndroidNewResult()))
                Text(item.title, Modifier.weight(1f), style = T.s15b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(uiText(item.timeText), style = T.s13, color = p.ink2, maxLines = 1)
            }
            if (warn && item.attentionLead.isCoreTextNotEmpty()) {
                Text(
                    buildAnnotatedString {
                        withStyle(SpanStyle(color = p.orangeInk, fontWeight = T.s15b.fontWeight)) { append(uiText(item.attentionLead)) }
                        append(uiText(item.attentionReason))
                    },
                    style = T.s15, color = p.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 10.dp),
                )
            } else {
                Text(
                    uiText(item.preview),
                    style = if (warn) T.s15 else T.s17.copy(lineHeight = 25.5.sp),
                    color = if (warn) p.ink2 else p.ink,
                    maxLines = if (warn) 2 else 4, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 10.dp),
                )
            }
            Row(Modifier.padding(top = 12.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                Text(uiText(item.metadata), style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                ArrowText(if (warn) copy(UiMessages.scheduleTakeAction()) else copy(UiMessages.scheduleReadFullText()), p.blue, T.s15)
            }
        }
    }
}

@Composable
private fun SwipeToMark(
    canMark: Boolean,
    markAsRead: Boolean,
    onMark: () -> Unit,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    content: @Composable () -> Unit,
) {
    val copy = uiText()
    var offset by remember(canMark, markAsRead) { mutableFloatStateOf(0f) }
    var width by remember { mutableIntStateOf(1) }
    val reveal = with(LocalDensity.current) { 88.dp.toPx() }
    val action = if (markAsRead) copy(UiMessages.scheduleMarkAsRead()) else copy(UiMessages.scheduleMarkAsUnread())
    Box(modifier.fillMaxWidth().onSizeChanged { width = it.width }) {
        if (canMark && offset < 0) {
            Row(Modifier.matchParentSize().background(pal.blue).padding(end = 16.dp), Arrangement.End, Alignment.CenterVertically) {
                Row(
                    Modifier.width(72.dp).fillMaxHeight().clickable { offset = 0f; onMark() },
                    Arrangement.Center, Alignment.CenterVertically,
                ) {
                    if (markAsRead) { Ic(Lucide.Check, 16.dp, Color.White); Spacer(Modifier.width(4.dp)) }
                    Text(if (markAsRead) copy(UiMessages.scheduleAndroidRead()) else copy(UiMessages.scheduleAndroidUnread()), style = T.s15b, color = Color.White)
                }
            }
        }
        Box(
            Modifier.offset { IntOffset(offset.roundToInt(), 0) }.fillMaxWidth().background(pal.paper)
                .then(
                    if (!canMark) Modifier else Modifier.draggable(
                        rememberDraggableState { delta -> offset = (offset + delta).coerceIn(-width.toFloat(), 0f) },
                        Orientation.Horizontal,
                        onDragStopped = {
                            if (-offset >= width * .65f) { offset = 0f; onMark() }
                            else offset = if (-offset >= reveal / 2) -reveal else 0f
                        },
                    ),
                )
                .semantics { if (canMark) customActions = listOf(CustomAccessibilityAction(action) { onMark(); true }) }
                .clickable { if (offset != 0f) offset = 0f else onClick() },
        ) { content() }
    }
}

@Composable
internal fun TaskRow(item: ScheduleTaskItemView, last: Boolean = false, onMark: () -> Unit = {}, onClick: () -> Unit) {
    val copy = uiText()
    val p = pal
    val attention = item.group == ScheduleDisplayGroup.SCHEDULE_DISPLAY_GROUP_ATTENTION
    SwipeToMark(item.actions.canMarkRead || item.actions.canMarkUnread, item.actions.canMarkRead, onMark, onClick) {
        Row(Modifier.fillMaxWidth().padding(start = 24.dp), verticalAlignment = Alignment.Top) {
            if (attention || item.hasUnread) {
                Box(Modifier.width(26.dp).padding(top = 20.dp)) {
                    ScheduleResultDot(if (attention) p.orange else p.blue, if (attention) copy(UiMessages.scheduleNeedsYourAttention()) else copy(UiMessages.scheduleAndroidNewResult0653C4()))
                }
            }
            Column(Modifier.weight(1f).then(if (last) Modifier else Modifier.bottomLine(p.sep)).padding(end = 24.dp, top = 13.dp, bottom = 13.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(item.title, Modifier.weight(1f), style = T.s17b, color = if (item.paused) p.ink3 else p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(uiText(item.timeText), style = T.s13, color = p.ink2, maxLines = 1)
                }
                val preview = uiText(item.preview)
                if (preview.isNotEmpty()) Text(preview, Modifier.padding(top = 2.dp), style = T.s15, color = p.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
                Row(Modifier.padding(top = 3.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                    if (item.inProgress) {
                        Spinner(p.ink2, size = 12.dp)
                        Text(copy(UiMessages.scheduleAndroidRunningLabel()), style = T.s13, color = p.ink2)
                    }
                    Text(uiText(item.metadata), style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
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
fun ScheduleDetailScreen(machine: String, id: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val initialVm = remember(remote, machine, id) { remote.coreHost.snapshotScheduleDetailView(machine, id) }
    val rawVm by remote.scheduleDetailView.collectAsState()
    val vm = if (rawVm.machineId == machine && rawVm.taskId == id) rawVm else initialVm
    val formVm by remote.scheduleFormView.collectAsState()

    LifecycleStartEffect(remote, machine, id) {
        remote.coreHost.openScheduleDetail(machine, id)
        onStopOrDispose { remote.coreHost.sendEvent { setScheduleDetailHide(ScheduleTaskRef.newBuilder().setMachineId(machine).setTaskId(id)) } }
    }
    DisposableEffect(remote, machine, id) {
        onDispose { remote.coreHost.sendEvent { setScheduleDetailClose(ScheduleTaskRef.newBuilder().setMachineId(machine).setTaskId(id)) } }
    }
    val activeEdit = formVm.takeIf {
        it.open && it.machineId == machine && it.taskId == id &&
            it.mode != ScheduleFormMode.SCHEDULE_FORM_MODE_CREATE_NEW &&
            it.mode != ScheduleFormMode.SCHEDULE_FORM_MODE_CREATE_FOR_SESSION
    }
    ScheduleDetailScreenContent(
        vm = vm,
        editForm = activeEdit,
        connBanner = { if (!state.loaded) ConnBanner(state) },
        onBack = nav.back,
        onEvent = remote.coreHost::sendEvent,
        onOpenSession = { sid, seq -> if (seq > 0L) nav.scheduledSession(sid, seq) else nav.session(sid) },
        onOpenTargetSession = nav.session,
        onOpenEdit = { mode -> remote.coreHost.openScheduleForm(mode, machineId = machine, taskId = id) },
        onMarkUnreadAndBackToInbox = {
            remote.coreHost.sendEvent { setScheduleMarkUnread(ScheduleTaskBoolAction.newBuilder().setMachineId(machine).setTaskId(id).setValue(true)) }
            nav.schedules()
        },
        onOpenMachineSettings = nav.device,
    )
}

@Composable
internal fun ScheduleDetailScreenContent(
    vm: ScheduleDetailViewModel,
    editForm: ScheduleFormViewModel? = null,
    connBanner: @Composable () -> Unit = {},
    onBack: () -> Unit = {},
    onEvent: (CoreEvent.Builder.() -> Unit) -> Unit = {},
    onOpenSession: (sessionId: String, firstSeq: Long) -> Unit = { _, _ -> },
    onOpenTargetSession: (String) -> Unit = {},
    onOpenEdit: (ScheduleFormMode) -> Unit = {},
    onMarkUnreadAndBackToInbox: () -> Unit = {},
    onOpenMachineSettings: (String) -> Unit = {},
) {
    val copy = uiText()
    var menu by remember { mutableStateOf(false) }
    var deleting by remember { mutableStateOf(false) }
    var renaming by remember { mutableStateOf(false) }
    val taskRef = ScheduleTaskRef.newBuilder().setMachineId(vm.machineId).setTaskId(vm.taskId)
    fun operate(op: ScheduleOperationKind) = onEvent {
        setScheduleOperate(ScheduleOperateAction.newBuilder().setMachineId(vm.machineId).setTaskId(vm.taskId).setOperation(op))
    }

    Column(Modifier.fillMaxSize().background(pal.paper)) {
        PageBar(if (vm.found) vm.title else copy(UiMessages.queueAndroidScheduled()), onBack, if (vm.found) uiText(vm.subtitle) else "", if (vm.found) ({ menu = true }) else null)
        connBanner()
        if (!vm.found) {
            Notice(
                uiText(vm.machineError).ifEmpty {
                    uiText(vm.notFoundText).ifEmpty {
                        if (vm.loading) copy(UiMessages.scheduleReadingTasks()) else copy(UiMessages.scheduleAndroidTaskNotFoundItMayHaveBeenDeletedOnAnother())
                    }
                },
            )
            return@Column
        }
        Column(Modifier.weight(1f).fillMaxWidth().readableColumn().verticalScrollFaded()) {
            if (vm.resultCard.hasResult && vm.resultCard.resultLoading) {
                var showSkeleton by remember(vm.machineId, vm.taskId) { mutableStateOf(false) }
                LaunchedEffect(vm.machineId, vm.taskId) {
                    kotlinx.coroutines.delay(300)
                    showSkeleton = true
                }
                if (showSkeleton) Skeleton()
                return@Column
            }
            if (vm.machineError.isCoreTextNotEmpty()) {
                Notice(
                    uiText(vm.machineError),
                    tone = NoticeTone.WARN,
                    action = if (vm.canRetryMachineList) copy(UiMessages.commonRetry()) else null,
                    onAction = { onEvent { setScheduleRefresh(ScheduleStringAction.newBuilder().setValue(vm.machineId)) } },
                )
            }
            ScheduleMutationBanner(vm.mutation, Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
                { onEvent { setScheduleRetryMutation(ScheduleMutationRef.newBuilder().setMachineId(vm.machineId).setTaskId(vm.taskId)) } },
                { onEvent { setScheduleAbandonMutation(ScheduleMutationRef.newBuilder().setMachineId(vm.machineId).setTaskId(vm.taskId)) } },
            )
            if (vm.attention.show) {
                val disabledBanner =
                    vm.attention.message.key == "schedule.agent_disabled_pre_run" ||
                        vm.attention.message.key == "schedule.agent_disabled_stopped"
                ScheduleAttentionBanner(
                    uiText(vm.attention.message),
                    action = if (disabledBanner && vm.machineId.isNotEmpty()) copy(UiMessages.scheduleEnableCli()) else null,
                    onAction = if (disabledBanner && vm.machineId.isNotEmpty()) ({ onOpenMachineSettings(vm.machineId) }) else null,
                )
                if (vm.attention.showPrimaryAction) {
                    PillButton(uiText(vm.attention.primaryActionLabel), { operate(ScheduleOperationKind.SCHEDULE_OPERATION_KIND_RECOVER) },
                        Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp), ButtonKind.INK, vm.attention.primaryActionEnabled)
                }
            }
            if (vm.resultCard.hasResult && vm.resultCard.hasNewerResult) {
                TextAction(copy(UiMessages.scheduleAndroidNewResult0653C4()), { onEvent { setScheduleDetailShowLatestResult(taskRef) } },
                    Modifier.fillMaxWidth().padding(horizontal = 16.dp), style = T.s13)
            }
            if (vm.activeRun.hasActive) {
                Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                    Row(Modifier.fillMaxWidth().padding(start = 14.dp, end = 6.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        Mark(Lucide.LoaderCircle, pal.blue, 14.dp)
                        Text(uiText(vm.activeRun.statusText), Modifier.weight(1f), style = T.s13, color = pal.blue)
                        if (vm.activeRun.canOpenSession && vm.activeRun.sessionId.isNotEmpty()) {
                            TextAction(copy(UiMessages.scheduleConversation()), { onOpenSession(vm.activeRun.sessionId, vm.activeRun.firstSeq) }, style = T.s13, arrow = true)
                        }
                    }
                }
            }
            if (vm.resultCard.hasResult) ScheduleLatestResult(vm.resultCard) { onOpenSession(vm.resultCard.sessionId, vm.resultCard.firstSeq) }
            if (vm.earlierGroupsList.isNotEmpty()) {
                Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp).heightIn(min = 44.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(copy(UiMessages.scheduleEarlier()), Modifier.weight(1f), style = T.s13b, color = pal.ink2)
                    Text(uiText(vm.earlierCountText), style = T.s13, color = pal.ink2)
                }
                Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                    Column {
                        vm.earlierGroupsList.forEach { group ->
                            ScheduleRunGroup(
                                group,
                                onToggleQuiet = { onEvent { setScheduleDetailToggleQuietGroup(ScheduleTaskStringAction.newBuilder().setMachineId(vm.machineId).setTaskId(vm.taskId).setValue(group.groupKey)) } },
                                openRun = { run -> onEvent { setScheduleDetailOpenRun(ScheduleTaskStringAction.newBuilder().setMachineId(vm.machineId).setTaskId(vm.taskId).setValue(run.runId)) } },
                            )
                        }
                        if (vm.hasMoreEarlierRuns) TextAction(uiText(vm.viewAllText), { onEvent { setScheduleDetailShowAllRuns(ScheduleTaskBoolAction.newBuilder().setMachineId(vm.machineId).setTaskId(vm.taskId).setValue(true)) } }, Modifier.fillMaxWidth())
                    }
                }
            } else if (vm.emptyHistory && vm.emptyHistoryText.isCoreTextNotEmpty()) {
                Hint(uiText(vm.emptyHistoryText))
            }
            Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                Column {
                    Row(Modifier.fillMaxWidth().bottomLine(pal.sep).padding(horizontal = 14.dp), verticalAlignment = Alignment.CenterVertically) {
                        Text(copy(UiMessages.scheduleWhatShouldTheAgentDo()), Modifier.weight(1f), style = T.s15, color = pal.ink)
                        TextAction(copy(UiMessages.toolsAndroidEdit()), { onOpenEdit(ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_MESSAGE) }, enabled = vm.actions.canEditDefinition, style = T.s13)
                    }
                    ScheduleDescription(vm.message, Modifier.padding(horizontal = 14.dp, vertical = 12.dp))
                }
            }
            Label(copy(UiMessages.commonSettings()))
            Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                Column {
                    ScheduleSetting(copy(UiMessages.scheduleWhen()), uiText(vm.timingText), vm.actions.canEditDefinition) { onOpenEdit(ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_TIMING) }
                    ToggleRow(copy(UiMessages.scheduleNotifyOnCompletion()), "", vm.notifyOnFinish, vm.actions.canToggleNotify) { on ->
                        onEvent { setScheduleSetNotify(ScheduleTaskBoolAction.newBuilder().setMachineId(vm.machineId).setTaskId(vm.taskId).setValue(on)) }
                    }
                    if (vm.hasLimits && vm.limitsText.isCoreTextNotEmpty()) {
                        ScheduleSetting(copy(UiMessages.sessionEnd()), uiText(vm.limitsText), vm.actions.canEditDefinition) { onOpenEdit(ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_TIMING) }
                    }
                    val resume = vm.targetType == ScheduleTargetType.SCHEDULE_TARGET_TYPE_RESUME_SESSION
                    ScheduleSetting(uiText(vm.targetLabel), uiText(vm.targetValue), if (resume) vm.canOpenTargetSession else vm.actions.canEditDefinition) {
                        if (resume) onOpenTargetSession(vm.targetSessionId) else onOpenEdit(ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_TARGET)
                    }
                    Box(Modifier.bottomLine(pal.sep)) {
                        GRow(copy(UiMessages.newSessionPermissions()), uiText(vm.permissionsText),
                            onClick = if (vm.canEditPermissions) ({ onOpenEdit(ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_MODE) }) else null,
                            chevron = vm.canEditPermissions, enabled = vm.canEditPermissions, singleLineValue = true)
                    }
                    ScheduleSetting(copy(UiMessages.scheduleTaskName()), vm.taskName, vm.actions.canRename) { renaming = true }
                }
            }
            Hint(uiText(vm.creationFooter))
            if (vm.readOnlyNotice.isCoreTextNotEmpty()) Hint(uiText(vm.readOnlyNotice), Modifier.padding(bottom = 8.dp))
            if (vm.runOnceFooter.isCoreTextNotEmpty()) Hint(uiText(vm.runOnceFooter), Modifier.padding(bottom = 8.dp))
        }
    }

    if (vm.found) {
        if (menu) {
            ActionSheet(
                info = { SheetInfo(vm.title, uiText(vm.menuSubtitle)) },
                groups = listOf(
                    buildList {
                        add(SheetAction(copy(UiMessages.scheduleRunOnceNow()), enabled = vm.actions.canRunNow) { operate(ScheduleOperationKind.SCHEDULE_OPERATION_KIND_RUN) })
                        add(SheetAction(copy(UiMessages.scheduleMarkAsUnread()), enabled = vm.actions.canMarkUnread, onClick = onMarkUnreadAndBackToInbox))
                        if (vm.actions.showToggleAction) add(SheetAction(uiText(vm.actions.toggleLabel), enabled = vm.actions.canToggle) {
                            if (vm.actions.isResumeAction) operate(ScheduleOperationKind.SCHEDULE_OPERATION_KIND_RECOVER)
                            else operate(ScheduleOperationKind.SCHEDULE_OPERATION_KIND_PAUSE)
                        })
                    },
                    listOf(SheetAction(copy(UiMessages.scheduleDeleteTask()), danger = true, enabled = vm.actions.canDelete) { deleting = true }),
                ),
                onDismiss = { menu = false },
            )
        }
        if (renaming) ScheduleRenameDialog(vm.taskName, vm.mutation, vm.actions.canRename, { renaming = false }) { name ->
            onEvent { setScheduleRename(ScheduleTaskStringAction.newBuilder().setMachineId(vm.machineId).setTaskId(vm.taskId).setValue(name)) }
        }
        if (editForm != null) {
            fun closeEdit() = onEvent { setScheduleFormClose(ScheduleFormBoolAction.newBuilder().setFormKey(editForm.formKey).setValue(true)) }
            when (editForm.mode) {
                ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_MESSAGE -> ScheduleMessageEditor(editForm, onEvent, ::closeEdit)
                ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_TIMING -> ScheduleTimeSheet(
                    editForm.timing, editForm.mutation, editForm.formKey, editForm.editable, submitOnApply = true,
                    onEvent = onEvent,
                    onAppliedSubmit = { onEvent { setScheduleFormSubmit(ScheduleFormBoolAction.newBuilder().setFormKey(editForm.formKey).setValue(false)) } },
                    close = {
                        onEvent { setScheduleFormUpdateTiming(ScheduleFormUpdateTimingAction.newBuilder().setFormKey(editForm.formKey).setResetDraft(true)) }
                        closeEdit()
                    },
                )
                ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_TARGET, ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_MODE -> ScheduleConfigSheet(
                    editForm, if (editForm.mode == ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_MODE) ScheduleConfigFocus.MODE else ScheduleConfigFocus.ALL,
                    showSaveButton = true, onEvent = onEvent, onClose = ::closeEdit,
                )
                else -> {}
            }
        }
        if (deleting) {
            Confirm(copy(UiMessages.scheduleAndroidDeleteThisTask()), copy(UiMessages.scheduleAndroidStopFutureSchedulingSentMessagesAndOpenedConversationsAreKept()),
                copy(UiMessages.scheduleDelete()), true, { deleting = false }) {
                deleting = false
                operate(ScheduleOperationKind.SCHEDULE_OPERATION_KIND_CANCEL)
            }
        }
    }
}

@Composable
internal fun ScheduleMutationBanner(mutation: ScheduleMutationView, modifier: Modifier = Modifier, onRetry: () -> Unit = {}, onAbandon: () -> Unit = {}) {
    val copy = uiText()
    if (!mutation.error.isCoreTextNotEmpty() && !mutation.notice.isCoreTextNotEmpty() && !mutation.frozenHint.isCoreTextNotEmpty() && !mutation.canRetry && !mutation.canAbandon) return
    Column(modifier, verticalArrangement = Arrangement.spacedBy(6.dp)) {
        if (mutation.error.isCoreTextNotEmpty()) Notice(uiText(mutation.error), tone = NoticeTone.ERROR)
        if (mutation.notice.isCoreTextNotEmpty()) Notice(uiText(mutation.notice))
        if (mutation.frozen && mutation.frozenHint.isCoreTextNotEmpty()) Notice(uiText(mutation.frozenHint), tone = NoticeTone.WARN)
        if (mutation.canRetry || mutation.canAbandon) Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            if (mutation.canRetry) TextAction(copy(UiMessages.commonRetry()), onRetry)
            if (mutation.canAbandon) TextAction(copy(UiMessages.newSessionAndroidAbandon()), onAbandon)
        }
    }
}

@Composable
internal fun ScheduleLatestResult(card: ScheduleResultCardView, open: () -> Unit) {
    val copy = uiText()
    Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
        Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 8.dp)) {
            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                if (card.unreadOnEntry) ScheduleResultDot()
                Text(uiText(card.timeText), Modifier.weight(1f), style = T.s12, color = pal.ink2)
                if (card.showConversationAction && card.sessionId.isNotEmpty()) TextAction(copy(UiMessages.scheduleConversation()), open, style = T.s13, arrow = true)
            }
            if (card.resultTitle.isNotBlank()) Text(card.resultTitle, style = T.s15b, color = pal.ink, modifier = Modifier.padding(bottom = 8.dp))
            val body = card.markdownBody.ifBlank { uiText(card.fallbackReasonText) }
            if (body.isNotBlank()) ScheduleResultMarkdown(body)
        }
    }
}

@Composable
internal fun ScheduleAttentionBanner(
    text: String,
    modifier: Modifier = Modifier,
    action: String? = null,
    onAction: (() -> Unit)? = null,
) {
    Notice(
        text,
        Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp).then(modifier),
        tone = NoticeTone.WARN,
        action = action,
        onAction = onAction,
    )
}

@Composable
fun ScheduleDescription(message: String, modifier: Modifier = Modifier) {
    val copy = uiText()
    var expanded by remember(message) { mutableStateOf(false) }
    var overflow by remember(message) { mutableStateOf(false) }
    Column(modifier) {
        Text(message, style = T.s15, color = pal.ink, maxLines = if (expanded) Int.MAX_VALUE else 3, overflow = TextOverflow.Ellipsis,
            onTextLayout = { if (!expanded) overflow = it.hasVisualOverflow })
        if (expanded || overflow) Row(Modifier.heightIn(min = 44.dp).clickable { expanded = !expanded }, verticalAlignment = Alignment.CenterVertically) {
            Text(if (expanded) copy(UiMessages.markdownCollapse()) else copy(UiMessages.markdownExpand()), style = T.s13b, color = pal.blue)
            Ic(if (expanded) Lucide.ChevronUp else Lucide.ChevronDown, 14.dp, pal.blue)
        }
    }
}

@Composable
internal fun ScheduleRenameDialog(initialTitle: String, mutation: ScheduleMutationView, enabled: Boolean, onClose: () -> Unit, onSave: (String) -> Unit) {
    val copy = uiText()
    var value by remember { mutableStateOf(TextFieldValue(initialTitle, TextRange(0, initialTitle.length))) }
    var submitted by remember { mutableStateOf(false) }
    var sawBusy by remember { mutableStateOf(false) }
    val busy = mutation.busy
    val hasError = mutation.error.isCoreTextNotEmpty()
    val focus = remember { FocusRequester() }
    LaunchedEffect(submitted, busy, hasError, mutation.frozen) {
        if (!submitted) return@LaunchedEffect
        if (busy) sawBusy = true
        else if (sawBusy || (!hasError && !mutation.frozen)) {
            submitted = false
            if (!hasError && !mutation.frozen) onClose()
        }
    }
    fun save() {
        if (busy || mutation.frozen || !enabled) return
        if (value.text == initialTitle) { onClose(); return }
        submitted = true; sawBusy = false; onSave(value.text)
    }
    DialogCard({ if (!busy) onClose() }) {
        LaunchedEffect(Unit) { focus.requestFocus() }
        Text(copy(UiMessages.scheduleRenameTask()), style = T.s17b, color = pal.ink, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(bottom = 12.dp))
        BasicTextField(
            value, { value = it.copy(text = it.text.replace('\n', ' ').replace('\r', ' ')) },
            Modifier.fillMaxWidth().padding(bottom = 8.dp).focusRequester(focus),
            textStyle = T.s15.copy(color = pal.ink), cursorBrush = SolidColor(pal.blue), singleLine = true,
            readOnly = !enabled || busy, keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
            keyboardActions = KeyboardActions(onDone = { save() }), decorationBox = { inner -> FieldBox { inner() } },
        )
        Text(if (hasError) uiText(mutation.error) else copy(UiMessages.scheduleSavingBlankUsesTheMessageSFirstSentence()),
            style = T.s13, color = if (hasError) pal.red else pal.ink2, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(bottom = 14.dp))
        Btns {
            PillButton(copy(UiMessages.commonCancel()), onClose, Modifier.weight(1f), enabled = !busy)
            PillButton(if (busy) copy(UiMessages.scheduleAndroidSavingFf509C()) else copy(UiMessages.commonSave()), ::save, Modifier.weight(1f), ButtonKind.INK, enabled && !busy && !mutation.frozen)
        }
    }
}

@Composable
internal fun ScheduleMessageEditor(vm: ScheduleFormViewModel, onEvent: (CoreEvent.Builder.() -> Unit) -> Unit = {}, close: () -> Unit = {}) {
    val copy = uiText()
    var savedInitial by remember(vm.formKey) { mutableStateOf(vm.message) }
    var text by remember(vm.formKey) { mutableStateOf(vm.message) }
    LaunchedEffect(vm.formKey) { savedInitial = vm.message }
    var discard by remember { mutableStateOf(false) }
    val busy = vm.mutation.busy
    fun cancel() { if (text != savedInitial) discard = true else close() }
    Dialog({ if (!busy) cancel() }, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        val view = LocalView.current
        val light = pal.ink.luminance() < .5f
        SideEffect {
            (view.parent as? DialogWindowProvider)?.window?.let { w ->
                WindowCompat.getInsetsController(w, view).apply { isAppearanceLightStatusBars = light; isAppearanceLightNavigationBars = light }
            }
        }
        BackHandler { if (!busy) cancel() }
        Column(Modifier.fillMaxSize().background(pal.paper).safeDrawingPadding().imePadding()) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp), verticalAlignment = Alignment.CenterVertically) {
                TextAction(copy(UiMessages.commonCancel()), ::cancel, enabled = !busy)
                Text(copy(UiMessages.scheduleWhatShouldTheAgentDo()), Modifier.weight(1f), style = T.s15b, color = pal.ink, textAlign = TextAlign.Center)
                TextAction(if (busy) copy(UiMessages.scheduleAndroidSaving()) else copy(UiMessages.commonSave()),
                    { onEvent { setScheduleFormSubmit(ScheduleFormBoolAction.newBuilder().setFormKey(vm.formKey).setValue(false)) } },
                    enabled = !busy && vm.editable && vm.canSubmit)
            }
            ScheduleMutationBanner(vm.mutation, Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
                { onEvent { setScheduleRetryMutation(ScheduleMutationRef.newBuilder().setFormKey(vm.formKey)) } },
                { onEvent { setScheduleAbandonMutation(ScheduleMutationRef.newBuilder().setFormKey(vm.formKey)) } },
            )
            BasicTextField(text, { text = it; onEvent { setScheduleFormSetMessage(ScheduleFormStringAction.newBuilder().setFormKey(vm.formKey).setValue(it)) } },
                Modifier.weight(1f).fillMaxWidth().padding(16.dp), textStyle = T.s15.copy(color = pal.ink), cursorBrush = SolidColor(pal.blue), readOnly = busy || !vm.editable)
            if (vm.messageTooLong && vm.messageError.isCoreTextNotEmpty()) Hint(uiText(vm.messageError), Modifier.padding(vertical = 16.dp), inset = 16.dp)
        }
        if (discard) Confirm(copy(UiMessages.scheduleDiscardChanges()), copy(UiMessages.scheduleAndroidChangesHaveNotBeenSaved()),
            copy(UiMessages.newSessionAndroidAbandon()), true, { discard = false }) { discard = false; close() }
    }
}

@Composable
internal fun ScheduleRunGroup(group: ScheduleHistoryGroupView, onToggleQuiet: () -> Unit, openRun: (ScheduleRunRowView) -> Unit) {
    if (group.foldedQuiet && group.runsCount > 1) {
        Box(Modifier.bottomLine(pal.sep)) {
            GRow(uiText(group.foldedTitle), singleLineValue = true, onClick = onToggleQuiet,
                trailing = { Ic(if (group.expanded) Lucide.ChevronUp else Lucide.ChevronDown, 16.dp, pal.ink2) })
        }
        if (group.foldedSummary.isCoreTextNotEmpty()) {
            Text(uiText(group.foldedSummary), style = T.s12, color = pal.ink2, modifier = Modifier.padding(start = 14.dp, end = 14.dp, bottom = 12.dp))
        }
        if (!group.expanded) return
    }
    group.runsList.forEach { run ->
        val canOpen = run.canOpenSession && run.sessionId.isNotEmpty()
        Row(
            Modifier.fillMaxWidth().heightIn(min = 44.dp).then(if (canOpen) Modifier.clickable { openRun(run) } else Modifier)
                .bottomLine(pal.sep).padding(horizontal = 14.dp, vertical = 12.dp),
            verticalAlignment = Alignment.Top,
        ) {
            Box(Modifier.width(22.dp).padding(top = 5.dp), contentAlignment = Alignment.TopCenter) { if (run.unreadOnEntry) ScheduleResultDot() }
            Column(Modifier.weight(1f)) {
                Text(uiText(run.timeText), style = T.s13, color = pal.ink2)
                Text(uiText(run.summaryText), style = T.s15, color = pal.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
            if (canOpen) Ic(Lucide.ChevronRight, 16.dp, pal.ink3)
        }
    }
}

@Composable
fun ScheduleSetting(label: String, value: String, enabled: Boolean = true, onClick: () -> Unit) {
    Box(Modifier.bottomLine(pal.sep)) { GRow(label, value, onClick = onClick, chevron = enabled, enabled = enabled, singleLineValue = true) }
}

@Composable
internal fun ScheduleAgentSetting(agents: List<AgentOptionView>, selected: io.github.lewin671.remoteagent.proto.Agent, enabled: Boolean, onSelect: (io.github.lewin671.remoteagent.proto.Agent) -> Unit) {
    if (agents.size < 2) return
    val idx = agents.indexOfFirst { it.agent == selected || it.selected }.coerceAtLeast(0)
    Box(Modifier.bottomLine(pal.sep)) { GRow("CLI", trailing = { SegPick(agents.map { it.displayName }, idx, enabled) { onSelect(agents[it].agent) } }) }
}

@Composable
fun NewScheduleScreen(session: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val home by remote.homeView.collectAsState()
    val projectView by remote.projectView.collectAsState()
    val vm by remote.scheduleFormView.collectAsState()
    val mode = if (session.isNotEmpty()) ScheduleFormMode.SCHEDULE_FORM_MODE_CREATE_FOR_SESSION else ScheduleFormMode.SCHEDULE_FORM_MODE_CREATE_NEW
    LaunchedEffect(remote, session) { remote.coreHost.openScheduleForm(mode = mode, sessionId = session) }
    fun close() {
        if (vm.formKey.isNotEmpty()) remote.coreHost.sendEvent { setScheduleFormClose(ScheduleFormBoolAction.newBuilder().setFormKey(vm.formKey).setValue(true)) }
        nav.back()
    }
    val content: @Composable () -> Unit = {
        ScheduleEditorContent(
            vm, home.sidebarProjects, projectView.manage.dirPicker, { ConnBanner(state) }, ::close,
            { remote.coreHost.openDirPicker(forNewTask = false) }, { remote.coreHost.closeDirPicker() }, remote.coreHost::sendEvent,
        )
    }
    if (session.isEmpty()) content() else Sheet(::close, scroll = false) { Box(Modifier.fillMaxHeight(.9f)) { content() } }
}

@Composable
internal fun ScheduleEditorContent(
    vm: ScheduleFormViewModel,
    sidebarProjects: ProjectListingView = ProjectListingView.getDefaultInstance(),
    dirPicker: DirPickerView = DirPickerView.getDefaultInstance(),
    connBanner: @Composable () -> Unit = {},
    onClose: () -> Unit = {},
    onOpenDirPicker: () -> Unit = {},
    onCloseDirPicker: () -> Unit = {},
    onEvent: (CoreEvent.Builder.() -> Unit) -> Unit = {},
) {
    val copy = uiText()
    val current = vm.mode == ScheduleFormMode.SCHEDULE_FORM_MODE_CREATE_FOR_SESSION
    var message by remember(vm.formKey) { mutableStateOf(vm.message) }
    var title by remember(vm.formKey) { mutableStateOf(vm.title) }
    var pickProject by remember { mutableStateOf(false) }
    var pickDirectory by remember { mutableStateOf(false) }
    var settingsFocus by remember { mutableStateOf<ScheduleConfigFocus?>(null) }
    var timeOpen by remember { mutableStateOf(false) }
    var confirmFullAccess by remember { mutableStateOf(false) }

    Column(Modifier.fillMaxSize().background(pal.paper)) {
        if (current) SheetHead(copy(UiMessages.scheduleScheduleMessagesToThisConversation()), copy(UiMessages.commonCancel()), onClose)
        else PageBar(copy(UiMessages.homeNewTask()), onClose)
        connBanner()
        FormColumn(Modifier.weight(1f).fillMaxWidth().readableColumn().imePadding().verticalScrollFaded()) {
            ScheduleMutationBanner(vm.mutation, Modifier.fillMaxWidth(),
                { onEvent { setScheduleRetryMutation(ScheduleMutationRef.newBuilder().setFormKey(vm.formKey)) } },
                { onEvent { setScheduleAbandonMutation(ScheduleMutationRef.newBuilder().setFormKey(vm.formKey)) } },
            )
            vm.noticesList.forEach { n ->
                when (n.severity) {
                    ScheduleFormNoticeSeverity.SCHEDULE_FORM_NOTICE_SEVERITY_ERROR -> Notice(uiText(n.text), tone = NoticeTone.ERROR)
                    ScheduleFormNoticeSeverity.SCHEDULE_FORM_NOTICE_SEVERITY_WARN -> Notice(uiText(n.text), tone = NoticeTone.WARN)
                    else -> Hint(uiText(n.text))
                }
            }
            FieldLabel(copy(UiMessages.scheduleWhatShouldTheAgentDo()))
            InputField(message, { if (vm.editable) { message = it; onEvent { setScheduleFormSetMessage(ScheduleFormStringAction.newBuilder().setFormKey(vm.formKey).setValue(it)) } } },
                copy(UiMessages.scheduleAndroidCheckProjectUpdatesAndNotifyMeOfNoteworthyChanges()), maxLines = if (current) 4 else 8)
            if (vm.messageTooLong && vm.messageError.isCoreTextNotEmpty()) Hint(uiText(vm.messageError))
            FieldLabel(copy(UiMessages.scheduleTaskName()))
            InputField(title, {
                if (vm.editable) {
                    val clean = it.replace('\n', ' ').replace('\r', ' ')
                    title = clean
                    onEvent { setScheduleFormSetTitle(ScheduleFormStringAction.newBuilder().setFormKey(vm.formKey).setValue(clean)) }
                }
            }, uiText(vm.titlePlaceholder), singleLine = true)
            ToggleRow(copy(UiMessages.scheduleNotifyOnCompletion()), "", vm.notifyOnFinish, vm.editable) {
                onEvent { setScheduleFormSetNotify(ScheduleFormBoolAction.newBuilder().setFormKey(vm.formKey).setValue(it)) }
            }
            ScheduleSetting(copy(UiMessages.scheduleWhen()), uiText(vm.timingSummary), vm.editable) { timeOpen = true }
            if (!current) {
                ScheduleSetting(copy(UiMessages.scheduleProject()), if (vm.hasProject) uiText(vm.projectName) else copy(UiMessages.scheduleAndroidChooseAProject()), vm.editable) { pickProject = true }
                if (vm.hasProject) {
                    ScheduleAgentSetting(vm.availableAgentsList, vm.agent, vm.editable) { ag ->
                        onEvent { setScheduleFormSelectAgent(ScheduleFormSelectAgentAction.newBuilder().setFormKey(vm.formKey).setAgent(ag)) }
                    }
                    ScheduleSetting(copy(UiMessages.newSessionPermissions()), uiText(vm.approvalModeLabel), vm.editable) { settingsFocus = ScheduleConfigFocus.MODE }
                    val multiMachine = vm.machineChoicesCount > 1
                    val modelDisplay = listOfNotNull(
                        vm.machineName.takeIf { multiMachine && it.isNotEmpty() },
                        uiText(vm.modelLabel).ifEmpty { copy(UiMessages.newSessionDefaultModel()) },
                        vm.effortLabel.ifEmpty { null },
                    ).joinToString(" · ")
                    ScheduleSetting(if (multiMachine) copy(UiMessages.scheduleComputerAndModel()) else copy(UiMessages.modelsTitle()),
                        modelDisplay, vm.editable) { settingsFocus = if (multiMachine) ScheduleConfigFocus.ALL else ScheduleConfigFocus.MODEL }
                }
            }
            if (vm.submitDisabledReason.isCoreTextNotEmpty() && !vm.canSubmit) Hint(uiText(vm.submitDisabledReason))
            PillButton(uiText(vm.submitLabel), {
                if (!vm.canSubmit) return@PillButton
                if (vm.requiresFullAccessConfirm) confirmFullAccess = true
                else onEvent { setScheduleFormSubmit(ScheduleFormBoolAction.newBuilder().setFormKey(vm.formKey).setValue(false)) }
            }, Modifier.fillMaxWidth().padding(vertical = 16.dp), ButtonKind.INK, vm.canSubmit)
        }
    }

    if (confirmFullAccess) Confirm(copy(UiMessages.scheduleRunWithFullAccess()),
        copy(if (current) UiMessages.scheduleNoApprovalWillBeRequiredForFutureRunsExisting() else UiMessages.scheduleNoApprovalIsRequiredForAnyOperationWheneverThis()),
        copy(UiMessages.modesFullAccess()), true, { confirmFullAccess = false }) {
        confirmFullAccess = false
        onEvent { setScheduleFormSubmit(ScheduleFormBoolAction.newBuilder().setFormKey(vm.formKey).setValue(true)) }
    }
    if (timeOpen) ScheduleTimeSheet(vm.timing, formKey = vm.formKey, enabled = vm.editable, submitOnApply = false, onEvent = onEvent,
        onAppliedSubmit = { timeOpen = false },
        close = { onEvent { setScheduleFormUpdateTiming(ScheduleFormUpdateTimingAction.newBuilder().setFormKey(vm.formKey).setResetDraft(true)) }; timeOpen = false })
    if (pickProject) Sheet({ pickProject = false }) {
        SheetHead(copy(UiMessages.projectsChoose()), copy(UiMessages.commonCancel()), { pickProject = false })
        if (sidebarProjects.pinnedCount + sidebarProjects.othersCount > 0 || vm.projectChoicesCount == 0) {
            CoreProjectList(sidebarProjects, onSearch = {}, onNew = { onOpenDirPicker(); pickProject = false; pickDirectory = true }) { pr ->
                CoreProjectRow(pr, retryEnabled = false, onClick = {
                    onEvent { setScheduleFormSelectProject(ScheduleFormSelectProjectAction.newBuilder().setFormKey(vm.formKey).setProjectKey(pr.key)) }
                    pickProject = false
                }) { Arrow(pal.ink3, ts(18.sp)) }
            }
        } else vm.projectChoicesList.forEach { ch ->
            GRow(uiText(ch.name), ch.shortPath, chevron = true, onClick = {
                onEvent { setScheduleFormSelectProject(ScheduleFormSelectProjectAction.newBuilder().setFormKey(vm.formKey).setProjectKey(ch.projectKey)) }
                pickProject = false
            })
        }
    }
    if (pickDirectory) Sheet({ onCloseDirPicker(); pickDirectory = false }, scroll = false) {
        CoreDirPickerBody(dirPicker, title = copy(UiMessages.scheduleAndroidChooseDirectory()), onBack = { onCloseDirPicker(); pickDirectory = false }) { m, path ->
            onCloseDirPicker(); pickDirectory = false
            onEvent { setScheduleFormSelectProject(ScheduleFormSelectProjectAction.newBuilder().setFormKey(vm.formKey).setProjectKey(path).setMachineId(m)) }
        }
    }
    settingsFocus?.let { focus ->
        ScheduleConfigSheet(vm, focus, showSaveButton = false, onEvent = onEvent, onClose = { settingsFocus = null })
    }
}

@Composable
private fun ScheduleConfigSheet(
    vm: ScheduleFormViewModel,
    focus: ScheduleConfigFocus,
    showSaveButton: Boolean,
    onEvent: (CoreEvent.Builder.() -> Unit) -> Unit,
    onClose: () -> Unit,
) {
    val copy = uiText()
    var confirmFullAccess by remember { mutableStateOf(false) }
    val enabled = vm.editable && !vm.mutation.busy && !vm.mutation.frozen
    fun selectModel(model: String, effort: String, mode: io.github.lewin671.remoteagent.proto.ApprovalMode? = null) = onEvent {
        val b = ScheduleFormSelectModelAction.newBuilder().setFormKey(vm.formKey).setModel(model).setEffort(effort)
        mode?.let { b.setMode(it) }
        setScheduleFormSelectModel(b)
    }
    fun done() {
        if (!showSaveButton) { onClose(); return }
        if (vm.requiresFullAccessConfirm) confirmFullAccess = true
        else onEvent { setScheduleFormSubmit(ScheduleFormBoolAction.newBuilder().setFormKey(vm.formKey).setValue(false)) }
    }

    val initialSnapshot = remember(vm.formKey) { listOf(vm.machineId, vm.agent.name, vm.model, vm.effort, vm.mode.name) }
    val dirty = showSaveButton && initialSnapshot != listOf(vm.machineId, vm.agent.name, vm.model, vm.effort, vm.mode.name)

    Sheet({ if (!vm.mutation.busy) onClose() }, dirty = dirty) {
        Row(Modifier.fillMaxWidth().padding(bottom = 10.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(if (focus == ScheduleConfigFocus.MODE) copy(UiMessages.newSessionPermissions()) else copy(UiMessages.modelsTitle()), Modifier.weight(1f), style = T.s17b, color = pal.ink)
            TextAction(copy(UiMessages.commonCancel()), { if (!vm.mutation.busy) onClose() }, enabled = !vm.mutation.busy)
            TextAction(if (showSaveButton) copy(UiMessages.commonSave()) else copy(UiMessages.commonDone()), ::done, enabled = !showSaveButton || vm.canSubmit)
        }
        if (showSaveButton) ScheduleMutationBanner(vm.mutation, Modifier.fillMaxWidth(),
            { onEvent { setScheduleRetryMutation(ScheduleMutationRef.newBuilder().setFormKey(vm.formKey)) } },
            { onEvent { setScheduleAbandonMutation(ScheduleMutationRef.newBuilder().setFormKey(vm.formKey)) } },
        )
        if (focus != ScheduleConfigFocus.MODE && !vm.lockMachine && vm.machineSelectable && vm.machineChoicesCount > 1) {
            FieldLabel(copy(UiMessages.commonMachine()))
            Column(Modifier.selectableGroup()) {
                vm.machineChoicesList.forEach { m ->
                    ListItem(m.name, uiText(m.note).ifEmpty { null }, m.selected, enabled = enabled && m.supported, onClick = {
                        onEvent { setScheduleFormSelectMachine(ScheduleFormStringAction.newBuilder().setFormKey(vm.formKey).setValue(m.machineId)) }
                    })
                }
            }
        }
        if (focus != ScheduleConfigFocus.MODE && vm.availableAgentsCount > 1) ScheduleAgentSetting(vm.availableAgentsList, vm.agent, enabled) { ag ->
            onEvent { setScheduleFormSelectAgent(ScheduleFormSelectAgentAction.newBuilder().setFormKey(vm.formKey).setAgent(ag)) }
        }
        if (focus == ScheduleConfigFocus.MODE || focus == ScheduleConfigFocus.ALL) {
            FieldLabel(copy(UiMessages.newSessionPermissions()))
            Column(Modifier.selectableGroup()) {
                vm.modeChoicesList.forEach { opt ->
                    ListItem(uiText(opt.label), if (!opt.enabled) uiText(opt.disabledReason).ifEmpty { null } else null, opt.selected,
                        enabled = enabled && opt.enabled, danger = opt.isDanger, onClick = { selectModel(vm.model, vm.effort, opt.mode) })
                }
            }
        }
        if (focus != ScheduleConfigFocus.MODE) {
            val panel = vm.modelPanel
            val panelEnabled = enabled && panel.panelEnabled
            var showAllModels by remember { mutableStateOf(false) }
            FieldLabel(copy(UiMessages.modelsTitle()))
            when {
                panel.modelsError.isCoreTextNotEmpty() && panel.modelsCount == 0 -> Notice(
                    uiText(panel.modelsError),
                    tone = NoticeTone.WARN,
                    action = copy(UiMessages.commonRetry()),
                    onAction = { onEvent { setScheduleFormRetryModels(ScheduleFormStringAction.newBuilder().setFormKey(vm.formKey)) } },
                )
                panel.modelsLoading && panel.modelsCount == 0 -> Text(uiText(UiMessages.modelsAndroidReadingMachine(name = vm.machineName)), style = T.s13, color = pal.ink2, modifier = Modifier.padding(vertical = 8.dp))
                else -> {
                    val active = panel.modelsList.firstOrNull { it.checked }
                    if (active != null && active.fixedEffortNote.isCoreTextNotEmpty()) {
                        Text(uiText(active.fixedEffortNote), style = T.s13, color = pal.ink2, modifier = Modifier.padding(vertical = 4.dp))
                    } else if (panel.effortChoicesCount > 1) {
                        Text(copy(UiMessages.modelsEffort()), style = T.s12, color = pal.ink2)
                        ModelSegments(panel.effortChoicesList, panel.selectedEffort, panelEnabled) { selectModel(vm.model, it) }
                    }
                    val shown = panelVisibleModels(panel.modelsList, showAllModels)
                    Column(Modifier.selectableGroup()) {
                        ModelPanelRow(uiText(panel.defaultModelLabel).ifEmpty { copy(UiMessages.modelsDefault()) }, panel.defaultModelChecked, panelEnabled) { selectModel("", "") }
                        shown.forEach { m -> ModelPanelRow(m.displayName.ifEmpty { m.slug }, m.checked, panelEnabled) { selectModel(m.slug, "") } }
                    }
                    if (!showAllModels && shown.size < panel.modelsCount) {
                        Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable(role = Role.Button) { showAllModels = true }, verticalAlignment = Alignment.CenterVertically) {
                            Text(copy(UiMessages.modelsAll()), style = T.s15, color = pal.ink2, modifier = Modifier.weight(1f))
                            Ic(Lucide.ChevronRight, 18.dp, pal.ink3)
                        }
                    }
                }
            }
        }
    }
    if (confirmFullAccess) Confirm(copy(UiMessages.scheduleRunWithFullAccess()), copy(UiMessages.scheduleNoApprovalIsRequiredForAnyOperationWheneverThis()),
        copy(UiMessages.modesFullAccess()), true, { confirmFullAccess = false }) {
        confirmFullAccess = false
        onEvent { setScheduleFormSubmit(ScheduleFormBoolAction.newBuilder().setFormKey(vm.formKey).setValue(true)) }
    }
}

@Composable
internal fun ScheduleTimeSheet(
    draft: ScheduleTimingDraftView,
    mutation: ScheduleMutationView = ScheduleMutationView.getDefaultInstance(),
    formKey: String = "",
    enabled: Boolean = true,
    submitOnApply: Boolean = false,
    onEvent: (CoreEvent.Builder.() -> Unit) -> Unit = {},
    onAppliedSubmit: () -> Unit = {},
    close: () -> Unit = {},
) {
    val copy = uiText()
    val context = LocalContext.current
    var intervalInput by remember { mutableStateOf(draft.interval) }
    var countInput by remember { mutableStateOf(draft.count) }
    var zoneInput by remember { mutableStateOf(draft.zone) }
    var pendingApply by remember { mutableStateOf(false) }

    LaunchedEffect(draft.interval) { if (!pendingApply && draft.interval != intervalInput) intervalInput = draft.interval }
    LaunchedEffect(draft.count) { if (!pendingApply && draft.count != countInput) countInput = draft.count }
    LaunchedEffect(draft.zone) { if (!pendingApply && draft.zone != zoneInput) zoneInput = draft.zone }
    LaunchedEffect(pendingApply, draft.timingApplied, draft.timingError) {
        if (!pendingApply) return@LaunchedEffect
        if (draft.timingError.isCoreTextNotEmpty()) pendingApply = false
        else if (draft.timingApplied) { pendingApply = false; onAppliedSubmit() }
    }
    fun update(build: ScheduleFormUpdateTimingAction.Builder.() -> Unit) = onEvent {
        setScheduleFormUpdateTiming(ScheduleFormUpdateTimingAction.newBuilder().setFormKey(formKey).apply(build))
    }
    fun pickDate(current: String, picked: (String) -> Unit) {
        val d = runCatching { LocalDate.parse(current) }.getOrElse { LocalDate.now() }
        android.app.DatePickerDialog(context, { _, y, m, day -> picked(LocalDate.of(y, m + 1, day).toString()) }, d.year, d.monthValue - 1, d.dayOfMonth).show()
    }

    val kindSegs = listOf(ScheduleTimingKind.SCHEDULE_TIMING_KIND_ONCE, ScheduleTimingKind.SCHEDULE_TIMING_KIND_DAILY,
        ScheduleTimingKind.SCHEDULE_TIMING_KIND_WEEKLY, ScheduleTimingKind.SCHEDULE_TIMING_KIND_INTERVAL)
    val unitSegs = listOf(ScheduleIntervalUnit.SCHEDULE_INTERVAL_UNIT_MINUTES, ScheduleIntervalUnit.SCHEDULE_INTERVAL_UNIT_HOURS,
        ScheduleIntervalUnit.SCHEDULE_INTERVAL_UNIT_DAYS, ScheduleIntervalUnit.SCHEDULE_INTERVAL_UNIT_SECONDS)
    val endSegs = listOf(ScheduleTimingEnd.SCHEDULE_TIMING_END_NEVER, ScheduleTimingEnd.SCHEDULE_TIMING_END_COUNT, ScheduleTimingEnd.SCHEDULE_TIMING_END_DATE)
    val busy = mutation.busy
    val sheetEnabled = enabled && !busy
    val initialTiming = remember(formKey) {
        listOf(draft.kind.name, draft.date, draft.time, draft.interval, draft.unit.name, draft.end.name, draft.count, draft.zone, draft.workdays.toString())
    }
    val timingDirty = submitOnApply && initialTiming != listOf(
        draft.kind.name, draft.date, draft.time, intervalInput, draft.unit.name, draft.end.name, countInput, zoneInput, draft.workdays.toString(),
    )

    fun applyNow() {
        if (!sheetEnabled) return
        if (draft.afterTurnUnselected) { close(); return }
        pendingApply = true
        update {
            setZone(zoneInput.trim()).setApply(true)
            if (draft.kind == ScheduleTimingKind.SCHEDULE_TIMING_KIND_INTERVAL) setInterval(intervalInput)
            if (draft.end == ScheduleTimingEnd.SCHEDULE_TIMING_END_COUNT) setCount(countInput)
        }
    }

    Sheet({ if (!busy) close() }, dirty = timingDirty) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Text(copy(UiMessages.scheduleWhen()), Modifier.weight(1f), style = T.s17b, color = pal.ink)
            TextAction(if (submitOnApply && busy) copy(UiMessages.scheduleAndroidSaving()) else copy(UiMessages.commonDone()), ::applyNow, enabled = sheetEnabled && draft.timingDoneEnabled)
        }
        if (submitOnApply) ScheduleMutationBanner(mutation, Modifier.fillMaxWidth(),
            { onEvent { setScheduleRetryMutation(ScheduleMutationRef.newBuilder().setFormKey(formKey)) } },
            { onEvent { setScheduleAbandonMutation(ScheduleMutationRef.newBuilder().setFormKey(formKey)) } },
        )
        SegPick(listOf(copy(UiMessages.scheduleOnceSchedules()), copy(UiMessages.scheduleDaily()), copy(UiMessages.scheduleWeeklyOn()), copy(UiMessages.scheduleEvery())),
            if (draft.afterTurnUnselected) -1 else kindSegs.indexOf(draft.kind), sheetEnabled) { idx -> update { setKind(kindSegs[idx]) } }
        if (draft.afterTurnUnselected && draft.afterTurnHint.isCoreTextNotEmpty()) {
            Hint(uiText(draft.afterTurnHint))
        } else {
            if (draft.timingError.isCoreTextNotEmpty()) Notice(uiText(draft.timingError), tone = NoticeTone.ERROR)
            if (draft.kind == ScheduleTimingKind.SCHEDULE_TIMING_KIND_ONCE) {
                ScheduleSetting(copy(UiMessages.scheduleAndroidDate()), draft.date, sheetEnabled) { pickDate(draft.date) { d -> update { setDate(d) } } }
            }
            if (draft.kind != ScheduleTimingKind.SCHEDULE_TIMING_KIND_INTERVAL) {
                ScheduleSetting(copy(UiMessages.scheduleTime()), draft.time, sheetEnabled) {
                    val t = runCatching { LocalTime.parse(draft.time) }.getOrElse { LocalTime.of(8, 0) }
                    android.app.TimePickerDialog(context, { _, h, m -> update { setTime("%02d:%02d".format(h, m)) } }, t.hour, t.minute, true).show()
                }
            } else {
                FieldLabel(copy(UiMessages.scheduleAndroidInterval()))
                InputField(intervalInput, { if (sheetEnabled) { intervalInput = it; update { setInterval(it) } } }, copy(UiMessages.scheduleAndroidPositiveInteger()))
                GRow(copy(UiMessages.scheduleAndroidUnit()), trailing = {
                    SegPick(listOf(copy(UiMessages.scheduleAndroidMinutes()), copy(UiMessages.scheduleAndroidHours()), copy(UiMessages.scheduleAndroidDays()), copy(UiMessages.scheduleAndroidSeconds())),
                        unitSegs.indexOf(draft.unit).coerceAtLeast(0), sheetEnabled) { idx -> update { setUnit(unitSegs[idx]) } }
                })
            }
            if (draft.kind == ScheduleTimingKind.SCHEDULE_TIMING_KIND_DAILY) {
                ToggleRow(copy(UiMessages.scheduleWeekdaysOnly()), copy(UiMessages.scheduleAndroidMondayToFriday()), draft.workdays, sheetEnabled) {
                    update { setWorkdays(it) }
                }
            }
            if (draft.kind == ScheduleTimingKind.SCHEDULE_TIMING_KIND_WEEKLY) {
                val days = draft.weekdaysList.toSet()
                val dayLabels = listOf(copy(UiMessages.scheduleMon()), copy(UiMessages.scheduleTue()), copy(UiMessages.scheduleWed()),
                    copy(UiMessages.scheduleThu()), copy(UiMessages.scheduleFri()), copy(UiMessages.scheduleSat()), copy(UiMessages.scheduleSun()))
                Row(Modifier.fillMaxWidth().clip(Radii.R14).background(pal.fill2).padding(2.dp), horizontalArrangement = Arrangement.spacedBy(2.dp)) {
                    (1..7).forEach { day ->
                        val selected = day in days
                        Box(
                            Modifier.weight(1f).heightIn(min = 44.dp).clip(Radii.R14)
                                .background(if (selected) (if (pal.paper == Color.White) pal.paper else pal.fill2) else Color.Transparent)
                                .toggleable(selected, sheetEnabled, Role.Checkbox) {
                                    update { setReplaceWeekdays(true).addAllWeekdays((if (selected) days - day else days + day).sorted()) }
                                },
                            contentAlignment = Alignment.Center,
                        ) { Text(dayLabels[day - 1], style = if (selected) T.s13b else T.s13, color = if (selected) pal.ink else pal.ink2) }
                    }
                }
            }
            if (draft.kind != ScheduleTimingKind.SCHEDULE_TIMING_KIND_ONCE) {
                GRow(copy(UiMessages.sessionEnd()), trailing = {
                    SegPick(listOf(copy(UiMessages.scheduleNever()), copy(UiMessages.scheduleAfterARunCount()), copy(UiMessages.scheduleEndDate())),
                        endSegs.indexOf(draft.end).coerceAtLeast(0), sheetEnabled) { idx -> update { setEnd(endSegs[idx]) } }
                })
                if (draft.end == ScheduleTimingEnd.SCHEDULE_TIMING_END_COUNT) {
                    InputField(countInput, { if (sheetEnabled) { countInput = it; update { setCount(it) } } }, copy(UiMessages.scheduleAndroidAutomaticRunCount()))
                }
                if (draft.end == ScheduleTimingEnd.SCHEDULE_TIMING_END_DATE) {
                    ScheduleSetting(copy(UiMessages.scheduleAndroidEndDate()), draft.untilDate, sheetEnabled) { pickDate(draft.untilDate) { d -> update { setUntilDate(d) } } }
                }
            }
            if (draft.kind != ScheduleTimingKind.SCHEDULE_TIMING_KIND_INTERVAL) {
                FieldLabel(copy(UiMessages.scheduleTimeZone()))
                InputField(zoneInput, { if (sheetEnabled) { zoneInput = it; update { setZone(it.trim()) } } }, copy(UiMessages.scheduleAndroidForExampleAsiaShanghai()))
            } else if (draft.showIntervalHint && draft.intervalHint.isCoreTextNotEmpty()) {
                Hint(uiText(draft.intervalHint))
            }
        }
    }
}

@Composable
fun SessionScheduleStrip(sid: String, modifier: Modifier = Modifier) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val strip by remember(remote, sid) { remote.coreHost.watchScheduleSessionStrip(sid) }.collectAsState(initial = ScheduleSessionStripViewModel.getDefaultInstance())
    SessionScheduleStripContent(strip, modifier) { m, id -> nav.task(m, id) }
}

@Composable
internal fun SessionScheduleStripContent(vm: ScheduleSessionStripViewModel, modifier: Modifier = Modifier, onOpenTask: (String, String) -> Unit = { _, _ -> }) {
    val copy = uiText()
    if (!vm.visible || vm.tasksCount == 0) return
    val tasks = vm.tasksList
    var choosing by remember { mutableStateOf(false) }
    if (choosing) Sheet({ choosing = false }) {
        SheetHead(copy(UiMessages.queueAndroidScheduled()), copy(UiMessages.commonCancel()), { choosing = false })
        tasks.forEach { t -> GRow(t.title, onClick = { choosing = false; onOpenTask(t.machineId, t.taskId) }, chevron = true, singleLineValue = true) }
    }
    Strip(Lucide.Clock, uiText(vm.summaryText), modifier, action = copy(UiMessages.scheduleManage()), onClick = {
        if (tasks.size == 1) tasks.first().let { onOpenTask(it.machineId, it.taskId) } else choosing = true
    })
}
