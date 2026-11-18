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
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
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
import androidx.compose.ui.platform.LocalWindowInfo
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
import java.time.Instant
import java.time.LocalDate
import java.time.LocalDateTime
import java.time.LocalTime
import java.time.ZoneId
import kotlin.math.roundToInt

private const val SCHEDULE_MESSAGE_MAX_BYTES = 32768
private const val SCHEDULE_NAME_MAX_CHARS = 80

private enum class ScheduleConfigFocus { ALL, MODE, MODEL }

private fun schedItemKey(prefix: String, item: ScheduleTaskItemView): String =
    listOf(prefix, item.machineId, item.taskId).joinToString("/")

private fun filterScheduleNameInput(raw: String): String {
    val clean = raw.replace('\n', ' ').replace('\r', ' ')
    return if (clean.length <= SCHEDULE_NAME_MAX_CHARS) clean else clean.substring(0, SCHEDULE_NAME_MAX_CHARS)
}

/**
 * 定时 (design-system §6 定时任务): renders Core's [SchedulesViewModel] directly.
 */
@Composable
fun SchedulesScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val vm by remote.schedulesView.collectAsState()
    val context = LocalContext.current
    val preferences = remember(context) { context.getSharedPreferences("schedule-display", Context.MODE_PRIVATE) }

    LaunchedEffect(remote) {
        if (preferences.contains("ended-open")) {
            val saved = preferences.getBoolean("ended-open", false)
            if (saved != vm.endedExpanded) remote.coreHost.toggleScheduleEnded(saved)
        }
    }

    LifecycleStartEffect(remote) {
        remote.coreHost.openScheduleList(true)
        onStopOrDispose {}
    }

    SchedulesScreenContent(
        vm = vm,
        bottom = {
            ShellBottom(
                ShellTab.SCHEDULES,
                uiText(UiMessages.homeNewSchedule()),
                Lucide.Plus,
                enabled = vm.canCreate,
                onAction = { nav.newTask("") },
            )
        },
        connBanner = { if (!state.loaded) ConnStrip(state) },
        onRetryMachine = { remote.coreHost.refreshSchedules(it) },
        onMarkAllRead = { remote.coreHost.markScheduleRead(allFresh = true) },
        onMarkRead = { item ->
            remote.coreHost.markScheduleRead(
                listOf(ScheduleTaskRef.newBuilder().setMachineId(item.machineId).setTaskId(item.taskId).build()),
            )
        },
        onMarkUnread = { item -> remote.coreHost.markScheduleUnread(item.machineId, item.taskId, true) },
        onToggleEnded = { expanded ->
            preferences.edit().putBoolean("ended-open", expanded).apply()
            remote.coreHost.toggleScheduleEnded(expanded)
        },
        onOpenTask = { item -> nav.task(item.machineId, item.taskId) },
    )
}

@Composable
internal fun SchedulesScreenContent(
    vm: SchedulesViewModel,
    bottom: @Composable () -> Unit = {},
    connBanner: @Composable () -> Unit = {},
    onRetryMachine: (String) -> Unit = {},
    onMarkAllRead: () -> Unit = {},
    onMarkRead: (ScheduleTaskItemView) -> Unit = {},
    onMarkUnread: (ScheduleTaskItemView) -> Unit = {},
    onToggleEnded: (Boolean) -> Unit = {},
    onOpenTask: (ScheduleTaskItemView) -> Unit = {},
) {
    val copy = uiText()
    val headline = vm.headline
    val attention = vm.attention14List
    val fresh = vm.freshList
    val others = vm.othersList
    val ended = vm.endedList

    ShellPage(Modifier.fillMaxSize(), bottom = bottom) {
        // No glass row on this page, so the title sits just under the status bar.
        LazyColumn(Modifier.fillMaxSize().readableColumn(), contentPadding = shellPadding(top = false)) {
            item("headline") {
                Text(
                    buildAnnotatedString {
                        if (headline.kind == ScheduleHeadlineKind.SCHEDULE_HEADLINE_KIND_STOPPED &&
                            headline.highlightCount > 0
                        ) {
                            val template = copy(UiMessages.scheduleStoppedCount(headline.highlightCount, "\uFFFC"))
                            append(template.substringBefore('\uFFFC'))
                            withStyle(SpanStyle(color = pal.orange)) { append(headline.highlightCount.toString()) }
                            append(template.substringAfter('\uFFFC'))
                        } else {
                            append(uiText(headline.title))
                        }
                    },
                    style = T.title,
                    color = pal.ink,
                    modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 20.dp),
                )
                if (headline.sub.isCoreTextNotEmpty()) {
                    Text(
                        uiText(headline.sub),
                        style = T.s17,
                        color = pal.ink2,
                        modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 7.dp),
                    )
                }
                Column(Modifier.padding(start = 16.dp, end = 16.dp, top = 10.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    connBanner()
                    if (vm.unsupportedNotice.isCoreTextNotEmpty()) Notice(uiText(vm.unsupportedNotice))
                    if (vm.offlineNotice.isCoreTextNotEmpty()) Notice(uiText(vm.offlineNotice))
                    vm.machineErrorsList.forEach { err ->
                        Notice(
                            uiText(err.error),
                            action = if (err.canRetry) copy(UiMessages.commonRetry()) else null,
                            onAction = { onRetryMachine(err.machineId) },
                        )
                    }
                }
                if (vm.showEmptyHint && vm.emptyHint.isCoreTextNotEmpty()) {
                    Text(
                        uiText(vm.emptyHint),
                        style = T.s17,
                        color = pal.ink2,
                        modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 16.dp),
                    )
                }
            }
            items(attention, key = { schedItemKey("stop", it) }) { item ->
                ScheduleCard(
                    item = item,
                    warn = true,
                    onMark = { if (item.actions.canMarkRead) onMarkRead(item) else onMarkUnread(item) },
                    onClick = { onOpenTask(item) },
                )
            }
            if (fresh.isNotEmpty()) {
                item("fresh") {
                    ScheduleSection(
                        copy(UiMessages.scheduleNewResultsSchedules()),
                        if (vm.canMarkAllRead) copy(UiMessages.scheduleMarkAllAsRead()) else null,
                        onAction = onMarkAllRead,
                    )
                }
            }
            items(fresh, key = { schedItemKey("new", it) }) { item ->
                ScheduleCard(
                    item = item,
                    warn = false,
                    onMark = { onMarkRead(item) },
                    onClick = { onOpenTask(item) },
                )
            }
            if (others.isNotEmpty()) {
                item("others") {
                    ScheduleSection(uiText(vm.othersSectionTitle))
                }
            }
            items(others, key = { schedItemKey("row", it) }) { item ->
                TaskRow(
                    item = item,
                    last = item == others.last(),
                    onMark = { onMarkUnread(item) },
                    onClick = { onOpenTask(item) },
                )
            }
            if (vm.totalEnded > 0 || ended.isNotEmpty()) {
                item("ended") {
                    Row(
                        Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { onToggleEnded(!vm.endedExpanded) }
                            .padding(horizontal = 24.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        ArrowText(uiText(vm.endedLabel), pal.ink2, T.s15, down = !vm.endedExpanded, up = vm.endedExpanded)
                    }
                }
                if (vm.endedExpanded) {
                    items(ended, key = { schedItemKey("end", it) }) { item ->
                        TaskRow(
                            item = item,
                            last = item == ended.last(),
                            onMark = { onMarkUnread(item) },
                            onClick = { onOpenTask(item) },
                        )
                    }
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
internal fun ScheduleCard(
    item: ScheduleTaskItemView,
    warn: Boolean,
    onMark: () -> Unit,
    onClick: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val canMark = item.actions.canMarkRead || item.actions.canMarkUnread
    val markAsRead = item.actions.canMarkRead
    SwipeToMark(canMark, markAsRead, onMark, onClick, Modifier.padding(start = 16.dp, end = 16.dp, top = 12.dp).clip(Radii.R28)) {
        Column(Modifier.fillMaxWidth().background(p.gbg).padding(18.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                ScheduleResultDot(
                    if (warn) p.orange else p.blue,
                    if (warn) copy(UiMessages.scheduleNeedsYourAttention()) else copy(UiMessages.scheduleAndroidNewResult()),
                )
                Text(item.title, Modifier.weight(1f), style = T.s15b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(uiText(item.timeText), style = T.s13, color = p.ink2, maxLines = 1)
            }
            if (warn && item.attentionLead.isCoreTextNotEmpty()) {
                Text(
                    buildAnnotatedString {
                        withStyle(SpanStyle(color = p.orangeInk, fontWeight = T.s15b.fontWeight)) {
                            append(uiText(item.attentionLead))
                        }
                        val reason = uiText(item.attentionReason)
                        if (reason.isNotEmpty()) append(reason)
                    },
                    style = T.s15,
                    color = p.ink2,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(top = 10.dp),
                )
            } else if (warn) {
                Text(
                    uiText(item.preview),
                    style = T.s15,
                    color = p.ink2,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(top = 10.dp),
                )
            } else {
                Text(
                    uiText(item.preview),
                    style = T.s17.copy(lineHeight = 25.5.sp),
                    color = p.ink,
                    maxLines = 4,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(top = 10.dp),
                )
            }
            Row(Modifier.padding(top = 12.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                Text(
                    uiText(item.metadata),
                    style = T.s13,
                    color = p.ink2,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f),
                )
                ArrowText(
                    if (warn) copy(UiMessages.scheduleTakeAction()) else copy(UiMessages.scheduleReadFullText()),
                    p.blue,
                    T.s15,
                )
            }
        }
    }
}

/**
 * Swiped left, a task shows a blue 已读 (or 未读) and, swiped far, does it
 * ([canMark]); a tap opens it. A screen reader has the same as an action.
 */
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
            Row(
                Modifier.matchParentSize().background(pal.blue).padding(end = 16.dp),
                horizontalArrangement = Arrangement.End,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Row(
                    Modifier.width(72.dp).fillMaxHeight().clickable { offset = 0f; onMark() },
                    horizontalArrangement = Arrangement.Center,
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    if (markAsRead) {
                        Ic(Lucide.Check, 16.dp, Color.White)
                        Spacer(Modifier.width(4.dp))
                    }
                    Text(
                        if (markAsRead) copy(UiMessages.scheduleAndroidRead()) else copy(UiMessages.scheduleAndroidUnread()),
                        style = T.s15b,
                        color = Color.White,
                    )
                }
            }
        }
        Box(
            Modifier.offset { IntOffset(offset.roundToInt(), 0) }
                .fillMaxWidth()
                .background(pal.paper)
                .then(
                    if (canMark) {
                        Modifier.draggable(
                            rememberDraggableState { delta -> offset = (offset + delta).coerceIn(-width.toFloat(), 0f) },
                            Orientation.Horizontal,
                            onDragStopped = {
                                if (-offset >= width * .65f) {
                                    offset = 0f
                                    onMark()
                                } else {
                                    offset = if (-offset >= reveal / 2) -reveal else 0f
                                }
                            },
                        )
                    } else {
                        Modifier
                    },
                )
                .semantics { if (canMark) customActions = listOf(CustomAccessibilityAction(action) { onMark(); true }) }
                .clickable { if (offset != 0f) offset = 0f else onClick() },
        ) {
            content()
        }
    }
}

/** A task in 其他任务 or 已结束, one row (design-system §6 会话行): title and time, two lines of summary, then how often and where. */
@Composable
internal fun TaskRow(
    item: ScheduleTaskItemView,
    last: Boolean = false,
    onMark: () -> Unit = {},
    onClick: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val attention = item.group == ScheduleDisplayGroup.SCHEDULE_DISPLAY_GROUP_ATTENTION
    val canMark = item.actions.canMarkRead || item.actions.canMarkUnread
    val markAsRead = item.actions.canMarkRead
    SwipeToMark(canMark, markAsRead, onMark, onClick) {
        Row(Modifier.fillMaxWidth().padding(start = 24.dp), verticalAlignment = Alignment.Top) {
            if (attention || item.hasUnread) {
                Box(Modifier.width(26.dp).padding(top = 20.dp)) {
                    ScheduleResultDot(
                        if (attention) p.orange else p.blue,
                        if (attention) copy(UiMessages.scheduleNeedsYourAttention()) else copy(UiMessages.scheduleAndroidNewResult0653C4()),
                    )
                }
            }
            Column(
                Modifier.weight(1f)
                    .then(if (last) Modifier else Modifier.bottomLine(p.sep))
                    .padding(end = 24.dp, top = 13.dp, bottom = 13.dp),
            ) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(
                        item.title,
                        Modifier.weight(1f),
                        style = T.s17b,
                        color = if (item.paused) p.ink3 else p.ink,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                    Text(uiText(item.timeText), style = T.s13, color = p.ink2, maxLines = 1)
                }
                val preview = uiText(item.preview)
                if (preview.isNotEmpty()) {
                    Text(
                        preview,
                        modifier = Modifier.padding(top = 2.dp),
                        style = T.s15,
                        color = p.ink2,
                        maxLines = 2,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
                Row(
                    Modifier.padding(top = 3.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    if (item.inProgress) {
                        Spinner(p.ink2, size = 12.dp)
                        Text(copy(UiMessages.scheduleAndroidRunningLabel()), style = T.s13, color = p.ink2)
                    }
                    Text(
                        uiText(item.metadata),
                        style = T.s13,
                        color = p.ink2,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
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
    val vm by remote.scheduleDetailView.collectAsState()
    val formVm by remote.scheduleFormView.collectAsState()

    LifecycleStartEffect(remote, machine, id) {
        remote.coreHost.openScheduleDetail(machine, id)
        onStopOrDispose { remote.coreHost.hideScheduleDetail(machine, id) }
    }
    DisposableEffect(remote, machine, id) {
        onDispose { remote.coreHost.closeScheduleDetail(machine, id) }
    }

    val activeEditForm = formVm.takeIf {
        it.open && it.machineId == machine && it.taskId == id &&
            it.mode != ScheduleFormMode.SCHEDULE_FORM_MODE_CREATE_NEW &&
            it.mode != ScheduleFormMode.SCHEDULE_FORM_MODE_CREATE_FOR_SESSION
    }

    ScheduleDetailScreenContent(
        vm = vm,
        editForm = activeEditForm,
        connBanner = { if (!state.loaded) ConnBanner(state) },
        onBack = nav.back,
        onRetryList = { remote.coreHost.refreshSchedules(machine) },
        onRetryMutation = { remote.coreHost.retryScheduleMutation(machineId = machine, taskId = id) },
        onAbandonMutation = { remote.coreHost.abandonScheduleMutation(machineId = machine, taskId = id) },
        onPrimaryRecovery = { remote.coreHost.operateSchedule(machine, id, ScheduleOperationKind.SCHEDULE_OPERATION_KIND_RECOVER) },
        onShowLatestResult = { remote.coreHost.showLatestScheduleResult(machine, id) },
        onOpenSession = { sid, firstSeq ->
            if (firstSeq > 0L) nav.scheduledSession(sid, firstSeq) else nav.session(sid)
        },
        onToggleQuietGroup = { key -> remote.coreHost.toggleScheduleQuietGroup(machine, id, key) },
        onShowAllRuns = { remote.coreHost.showAllScheduleRuns(machine, id, true) },
        onOpenRun = { run -> remote.coreHost.openScheduleRun(machine, id, run.runId) },
        onToggleNotify = { on -> remote.coreHost.setScheduleNotify(machine, id, on) },
        onOpenTargetSession = { sid -> nav.session(sid) },
        onOpenEdit = { mode -> remote.coreHost.openScheduleForm(mode, machineId = machine, taskId = id) },
        onOperate = { op -> remote.coreHost.operateSchedule(machine, id, op) },
        onMarkUnreadAndBackToInbox = {
            remote.coreHost.markScheduleUnread(machine, id, true)
            nav.schedules()
        },
        onRename = { name -> remote.coreHost.renameSchedule(machine, id, name) },
        onSetFormMessage = { fk, msg -> remote.coreHost.setScheduleFormMessage(fk, msg) },
        onSelectFormAgent = { fk, agent -> remote.coreHost.selectScheduleFormAgent(fk, agent) },
        onSelectFormModel = { fk, model, effort, mode ->
            remote.coreHost.selectScheduleFormModel(fk, model, effort, mode)
        },
        onRetryFormModels = { fk -> remote.coreHost.retryScheduleFormModels(fk) },
        onUpdateFormTiming = { remote.coreHost.updateScheduleFormTiming(it) },
        onSubmitForm = { fk, confirmed -> remote.coreHost.submitScheduleForm(fk, confirmed) },
        onRetryFormMutation = { fk -> remote.coreHost.retryScheduleMutation(formKey = fk) },
        onAbandonFormMutation = { fk -> remote.coreHost.abandonScheduleMutation(formKey = fk) },
        onCloseEditForm = { fk -> remote.coreHost.closeScheduleForm(fk, force = true) },
    )
}

@Composable
internal fun ScheduleDetailScreenContent(
    vm: ScheduleDetailViewModel,
    editForm: ScheduleFormViewModel? = null,
    connBanner: @Composable () -> Unit = {},
    onBack: () -> Unit = {},
    onRetryList: () -> Unit = {},
    onRetryMutation: () -> Unit = {},
    onAbandonMutation: () -> Unit = {},
    onPrimaryRecovery: () -> Unit = {},
    onShowLatestResult: () -> Unit = {},
    onOpenSession: (sessionId: String, firstSeq: Long) -> Unit = { _, _ -> },
    onToggleQuietGroup: (String) -> Unit = {},
    onShowAllRuns: () -> Unit = {},
    onOpenRun: (ScheduleRunRowView) -> Unit = {},
    onToggleNotify: (Boolean) -> Unit = {},
    onOpenTargetSession: (String) -> Unit = {},
    onOpenEdit: (ScheduleFormMode) -> Unit = {},
    onOperate: (ScheduleOperationKind) -> Unit = {},
    onMarkUnreadAndBackToInbox: () -> Unit = {},
    onRename: (String) -> Unit = {},
    onSetFormMessage: (formKey: String, message: String) -> Unit = { _, _ -> },
    onSelectFormAgent: (formKey: String, agent: io.github.lewin671.remoteagent.proto.Agent) -> Unit = { _, _ -> },
    onSelectFormModel: (formKey: String, model: String, effort: String, mode: io.github.lewin671.remoteagent.proto.ApprovalMode?) -> Unit = { _, _, _, _ -> },
    onRetryFormModels: (formKey: String) -> Unit = {},
    onUpdateFormTiming: (ScheduleFormUpdateTimingAction) -> Unit = {},
    onSubmitForm: (formKey: String, confirmed: Boolean) -> Unit = { _, _ -> },
    onRetryFormMutation: (formKey: String) -> Unit = {},
    onAbandonFormMutation: (formKey: String) -> Unit = {},
    onCloseEditForm: (formKey: String) -> Unit = {},
) {
    val copy = uiText()
    var menu by remember { mutableStateOf(false) }
    var deleting by remember { mutableStateOf(false) }
    var renaming by remember { mutableStateOf(false) }

    Column(Modifier.fillMaxSize().background(pal.paper)) {
        PageBar(
            title = if (vm.found) vm.title else copy(UiMessages.queueAndroidScheduled()),
            onBack = onBack,
            subtitle = if (vm.found) uiText(vm.subtitle) else "",
            more = if (vm.found) ({ menu = true }) else null,
        )
        connBanner()
        if (!vm.found) {
            val missingText = if (vm.machineError.isCoreTextNotEmpty()) {
                uiText(vm.machineError)
            } else {
                uiText(vm.notFoundText).ifEmpty {
                    if (vm.loading) copy(UiMessages.scheduleReadingTasks())
                    else copy(UiMessages.scheduleAndroidTaskNotFoundItMayHaveBeenDeletedOnAnother())
                }
            }
            Notice(missingText)
            return@Column
        }
        Column(Modifier.weight(1f).fillMaxWidth().readableColumn().verticalScrollFaded()) {
            // Reveal the card and earlier runs together, never an interim summary that changes height.
            if (vm.resultCard.hasResult && vm.resultCard.resultLoading) {
                Skeleton()
                return@Column
            }
            if (vm.machineError.isCoreTextNotEmpty()) {
                Notice(
                    uiText(vm.machineError),
                    tone = NoticeTone.WARN,
                    action = if (vm.canRetryMachineList) copy(UiMessages.commonRetry()) else null,
                    onAction = onRetryList,
                )
            }
            ScheduleMutationBanner(
                mutation = vm.mutation,
                onRetry = onRetryMutation,
                onAbandon = onAbandonMutation,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
            )
            if (vm.attention.show) {
                ScheduleAttentionBanner(uiText(vm.attention.message))
                if (vm.attention.showPrimaryAction) {
                    PillButton(
                        uiText(vm.attention.primaryActionLabel),
                        onPrimaryRecovery,
                        Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp),
                        ButtonKind.INK,
                        vm.attention.primaryActionEnabled,
                    )
                }
            }
            if (vm.resultCard.hasResult && vm.resultCard.hasNewerResult) {
                TextAction(
                    copy(UiMessages.scheduleAndroidNewResult0653C4()),
                    onShowLatestResult,
                    Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                    style = T.s13,
                )
            }
            if (vm.activeRun.hasActive) {
                Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                    Row(
                        Modifier.fillMaxWidth().padding(start = 14.dp, end = 6.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(6.dp),
                    ) {
                        Mark(Lucide.LoaderCircle, pal.blue, 14.dp)
                        Text(uiText(vm.activeRun.statusText), Modifier.weight(1f), style = T.s13, color = pal.blue)
                        if (vm.activeRun.canOpenSession && vm.activeRun.sessionId.isNotEmpty()) {
                            TextAction(
                                copy(UiMessages.scheduleConversation()),
                                { onOpenSession(vm.activeRun.sessionId, vm.activeRun.firstSeq) },
                                style = T.s13,
                                arrow = true,
                            )
                        }
                    }
                }
            }
            if (vm.resultCard.hasResult) {
                ScheduleLatestResult(vm.resultCard) {
                    onOpenSession(vm.resultCard.sessionId, vm.resultCard.firstSeq)
                }
            }
            if (vm.earlierGroupsList.isNotEmpty()) {
                Row(
                    Modifier.fillMaxWidth().padding(horizontal = 16.dp).heightIn(min = 44.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text(copy(UiMessages.scheduleEarlier()), Modifier.weight(1f), style = T.s13b, color = pal.ink2)
                    Text(uiText(vm.earlierCountText), style = T.s13, color = pal.ink2)
                }
                Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                    Column {
                        vm.earlierGroupsList.forEach { group ->
                            ScheduleRunGroup(
                                group = group,
                                onToggleQuiet = { onToggleQuietGroup(group.groupKey) },
                                openRun = onOpenRun,
                            )
                        }
                        if (vm.hasMoreEarlierRuns) {
                            TextAction(uiText(vm.viewAllText), onShowAllRuns, Modifier.fillMaxWidth())
                        }
                    }
                }
            } else if (vm.emptyHistory && vm.emptyHistoryText.isCoreTextNotEmpty()) {
                Hint(uiText(vm.emptyHistoryText))
            }
            Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                Column {
                    Row(
                        Modifier.fillMaxWidth().bottomLine(pal.sep).padding(horizontal = 14.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Text(copy(UiMessages.scheduleWhatShouldTheAgentDo()), Modifier.weight(1f), style = T.s15, color = pal.ink)
                        TextAction(
                            copy(UiMessages.toolsAndroidEdit()),
                            { onOpenEdit(ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_MESSAGE) },
                            enabled = vm.actions.canEditDefinition,
                            style = T.s13,
                        )
                    }
                    ScheduleDescription(vm.message, Modifier.padding(horizontal = 14.dp, vertical = 12.dp))
                }
            }
            Label(copy(UiMessages.commonSettings()))
            Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                Column {
                    ScheduleSetting(
                        copy(UiMessages.scheduleWhen()),
                        uiText(vm.timingText),
                        vm.actions.canEditDefinition,
                    ) { onOpenEdit(ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_TIMING) }
                    ToggleRow(
                        copy(UiMessages.scheduleNotifyOnCompletion()),
                        "",
                        vm.notifyOnFinish,
                        enabled = vm.actions.canToggleNotify,
                        onChange = { on -> if (vm.actions.canToggleNotify) onToggleNotify(on) },
                    )
                    if (vm.hasLimits && vm.limitsText.isCoreTextNotEmpty()) {
                        ScheduleSetting(
                            copy(UiMessages.sessionEnd()),
                            uiText(vm.limitsText),
                            vm.actions.canEditDefinition,
                        ) { onOpenEdit(ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_TIMING) }
                    }
                    if (vm.targetType == ScheduleTargetType.SCHEDULE_TARGET_TYPE_RESUME_SESSION) {
                        ScheduleSetting(
                            uiText(vm.targetLabel),
                            uiText(vm.targetValue),
                            vm.canOpenTargetSession,
                        ) { onOpenTargetSession(vm.targetSessionId) }
                    } else {
                        ScheduleSetting(
                            uiText(vm.targetLabel),
                            uiText(vm.targetValue),
                            vm.actions.canEditDefinition,
                        ) { onOpenEdit(ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_TARGET) }
                    }
                    Box(Modifier.bottomLine(pal.sep)) {
                        GRow(
                            copy(UiMessages.newSessionPermissions()),
                            uiText(vm.permissionsText),
                            onClick = if (vm.canEditPermissions) ({ onOpenEdit(ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_MODE) }) else null,
                            chevron = vm.canEditPermissions,
                            enabled = vm.canEditPermissions,
                            singleLineValue = true,
                        )
                    }
                    ScheduleSetting(
                        copy(UiMessages.scheduleTaskName()),
                        vm.taskName,
                        vm.actions.canRename,
                    ) { renaming = true }
                }
            }
            Hint(uiText(vm.creationFooter))
            if (vm.readOnlyNotice.isCoreTextNotEmpty()) {
                Hint(uiText(vm.readOnlyNotice), Modifier.padding(bottom = 8.dp))
            }
            if (vm.runOnceFooter.isCoreTextNotEmpty()) {
                Hint(uiText(vm.runOnceFooter), Modifier.padding(bottom = 8.dp))
            }
        }
    }

    if (vm.found) {
        if (menu) {
            ActionSheet(
                info = { SheetInfo(vm.title, uiText(vm.menuSubtitle)) },
                groups = listOf(
                    buildList {
                        add(
                            SheetAction(copy(UiMessages.scheduleRunOnceNow()), enabled = vm.actions.canRunNow) {
                                onOperate(ScheduleOperationKind.SCHEDULE_OPERATION_KIND_RUN)
                            },
                        )
                        add(
                            SheetAction(copy(UiMessages.scheduleMarkAsUnread()), enabled = vm.actions.canMarkUnread) {
                                onMarkUnreadAndBackToInbox()
                            },
                        )
                        if (vm.actions.showToggleAction) {
                            add(
                                SheetAction(uiText(vm.actions.toggleLabel), enabled = vm.actions.canToggle) {
                                    if (vm.actions.isResumeAction) onPrimaryRecovery()
                                    else onOperate(ScheduleOperationKind.SCHEDULE_OPERATION_KIND_PAUSE)
                                },
                            )
                        }
                    },
                    listOf(
                        SheetAction(copy(UiMessages.scheduleDeleteTask()), danger = true, enabled = vm.actions.canDelete) {
                            deleting = true
                        },
                    ),
                ),
                onDismiss = { menu = false },
            )
        }

        if (renaming) {
            ScheduleRenameDialog(
                initialTitle = vm.taskName,
                mutation = vm.mutation,
                enabled = vm.actions.canRename,
                onClose = { renaming = false },
                onSave = onRename,
            )
        }

        if (editForm != null) {
            when (editForm.mode) {
                ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_MESSAGE -> {
                    ScheduleMessageEditor(
                        formKey = editForm.formKey,
                        initial = editForm.message,
                        mutation = editForm.mutation,
                        writable = editForm.editable,
                        onSetMessage = { onSetFormMessage(editForm.formKey, it) },
                        close = { onCloseEditForm(editForm.formKey) },
                        save = { onSubmitForm(editForm.formKey, false) },
                        onRetryMutation = { onRetryFormMutation(editForm.formKey) },
                        onAbandonMutation = { onAbandonFormMutation(editForm.formKey) },
                    )
                }
                ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_TIMING -> {
                    ScheduleTimeSheet(
                        draft = editForm.timing,
                        mutation = editForm.mutation,
                        enabled = editForm.editable,
                        submitOnApply = true,
                        onUpdate = { b -> onUpdateFormTiming(b.setFormKey(editForm.formKey).build()) },
                        onAppliedSubmit = { onSubmitForm(editForm.formKey, false) },
                        onRetryMutation = { onRetryFormMutation(editForm.formKey) },
                        onAbandonMutation = { onAbandonFormMutation(editForm.formKey) },
                        close = {
                            onUpdateFormTiming(
                                ScheduleFormUpdateTimingAction.newBuilder()
                                    .setFormKey(editForm.formKey)
                                    .setResetDraft(true)
                                    .build(),
                            )
                            onCloseEditForm(editForm.formKey)
                        },
                    )
                }
                ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_TARGET,
                ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_MODE -> {
                    ScheduleConfigSheet(
                        vm = editForm,
                        focus = if (editForm.mode == ScheduleFormMode.SCHEDULE_FORM_MODE_EDIT_MODE) ScheduleConfigFocus.MODE else ScheduleConfigFocus.ALL,
                        showSaveButton = true,
                        onSelectMachine = { _ -> },
                        onSelectAgent = { agent -> onSelectFormAgent(editForm.formKey, agent) },
                        onSelectModel = { model, effort, mode ->
                            onSelectFormModel(editForm.formKey, model, effort, mode)
                        },
                        onRetryModels = { onRetryFormModels(editForm.formKey) },
                        onSave = { confirmed -> onSubmitForm(editForm.formKey, confirmed) },
                        onRetryMutation = { onRetryFormMutation(editForm.formKey) },
                        onAbandonMutation = { onAbandonFormMutation(editForm.formKey) },
                        onClose = { onCloseEditForm(editForm.formKey) },
                    )
                }
                else -> {}
            }
        }

        if (deleting) {
            Confirm(
                copy(UiMessages.scheduleAndroidDeleteThisTask()),
                copy(UiMessages.scheduleAndroidStopFutureSchedulingSentMessagesAndOpenedConversationsAreKept()),
                copy(UiMessages.scheduleDelete()),
                true,
                { deleting = false },
            ) {
                deleting = false
                onOperate(ScheduleOperationKind.SCHEDULE_OPERATION_KIND_CANCEL)
            }
        }
    }
}

@Composable
internal fun ScheduleMutationBanner(
    mutation: ScheduleMutationView,
    onRetry: () -> Unit,
    onAbandon: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val copy = uiText()
    if (!mutation.error.isCoreTextNotEmpty() && !mutation.notice.isCoreTextNotEmpty() && !mutation.frozenHint.isCoreTextNotEmpty()) {
        if (mutation.canRetry || mutation.canAbandon) {
            Row(modifier, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                if (mutation.canRetry) TextAction(copy(UiMessages.commonRetry()), onRetry)
                if (mutation.canAbandon) TextAction(copy(UiMessages.newSessionAndroidAbandon()), onAbandon)
            }
        }
        return
    }
    Column(modifier, verticalArrangement = Arrangement.spacedBy(6.dp)) {
        if (mutation.error.isCoreTextNotEmpty()) Notice(uiText(mutation.error), tone = NoticeTone.ERROR)
        if (mutation.notice.isCoreTextNotEmpty()) Notice(uiText(mutation.notice), tone = NoticeTone.PLAIN)
        if (mutation.frozen && mutation.frozenHint.isCoreTextNotEmpty()) Notice(uiText(mutation.frozenHint), tone = NoticeTone.WARN)
        if (mutation.canRetry || mutation.canAbandon) {
            Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                if (mutation.canRetry) TextAction(copy(UiMessages.commonRetry()), onRetry)
                if (mutation.canAbandon) TextAction(copy(UiMessages.newSessionAndroidAbandon()), onAbandon)
            }
        }
    }
}

@Composable
internal fun ScheduleLatestResult(card: ScheduleResultCardView, open: () -> Unit) {
    val copy = uiText()
    Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
        Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 8.dp)) {
            Row(
                Modifier.fillMaxWidth().heightIn(min = 44.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                if (card.unreadOnEntry) ScheduleResultDot()
                Text(uiText(card.timeText), Modifier.weight(1f), style = T.s12, color = pal.ink2)
                if (card.showConversationAction && card.sessionId.isNotEmpty()) {
                    TextAction(copy(UiMessages.scheduleConversation()), open, style = T.s13, arrow = true)
                }
            }
            if (card.resultTitle.isNotBlank()) {
                Text(card.resultTitle, style = T.s15b, color = pal.ink, modifier = Modifier.padding(bottom = 8.dp))
            }
            val body = card.markdownBody.ifBlank { uiText(card.fallbackReasonText) }
            if (body.isNotBlank()) ScheduleResultMarkdown(body)
        }
    }
}

/** Shares the result cards' content inset, inside the same constrained detail column. */
@Composable
internal fun ScheduleAttentionBanner(text: String, modifier: Modifier = Modifier) {
    Box(
        Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 8.dp).then(modifier)
            .clip(Radii.R14).background(pal.gbg).padding(horizontal = 14.dp, vertical = 12.dp),
    ) {
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
        Text(
            message,
            style = T.s15,
            color = pal.ink,
            maxLines = if (expanded) Int.MAX_VALUE else 3,
            overflow = TextOverflow.Ellipsis,
            onTextLayout = { if (!expanded) overflow = it.hasVisualOverflow },
        )
        if (expanded || overflow) {
            Row(
                Modifier.heightIn(min = 44.dp).clickable { expanded = !expanded },
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(if (expanded) copy(UiMessages.markdownCollapse()) else copy(UiMessages.markdownExpand()), style = T.s13b, color = pal.blue)
                Ic(if (expanded) Lucide.ChevronUp else Lucide.ChevronDown, 14.dp, pal.blue)
            }
        }
    }
}

@Composable
internal fun ScheduleRenameDialog(
    initialTitle: String,
    mutation: ScheduleMutationView,
    enabled: Boolean,
    onClose: () -> Unit,
    onSave: (String) -> Unit,
) {
    val copy = uiText()
    var value by remember { mutableStateOf(TextFieldValue(initialTitle, TextRange(0, initialTitle.length))) }
    var submitted by remember { mutableStateOf(false) }
    var sawBusy by remember { mutableStateOf(false) }
    val busy = mutation.busy
    val hasError = mutation.error.isCoreTextNotEmpty()
    val focus = remember { FocusRequester() }

    LaunchedEffect(submitted, busy, hasError, mutation.frozen) {
        if (!submitted) return@LaunchedEffect
        if (busy) {
            sawBusy = true
        } else if (sawBusy || (!hasError && !mutation.frozen)) {
            if (!hasError && !mutation.frozen) {
                submitted = false
                onClose()
            } else {
                submitted = false
            }
        }
    }

    fun save() {
        if (busy || mutation.frozen || !enabled) return
        if (value.text == initialTitle) {
            onClose()
            return
        }
        submitted = true
        sawBusy = false
        onSave(value.text)
    }

    Dialog({ if (!busy) onClose() }, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        val window = LocalWindowInfo.current
        LaunchedEffect(window) { snapshotFlow { window.isWindowFocused }.collect { if (it) focus.requestFocus() } }
        Box(Modifier.fillMaxWidth().padding(horizontal = 30.dp).imePadding(), contentAlignment = Alignment.Center) {
            Column(
                Modifier.widthIn(max = 320.dp).fillMaxWidth().clip(Radii.R20).background(pal.paper)
                    .padding(start = 16.dp, end = 16.dp, top = 18.dp, bottom = 12.dp),
            ) {
                Text(
                    copy(UiMessages.scheduleRenameTask()),
                    style = T.s17b,
                    color = pal.ink,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.fillMaxWidth().padding(bottom = 12.dp),
                )
                BasicTextField(
                    value,
                    { value = it.copy(text = filterScheduleNameInput(it.text)) },
                    Modifier.fillMaxWidth().padding(bottom = 8.dp).focusRequester(focus),
                    textStyle = T.s15.copy(color = pal.ink),
                    cursorBrush = SolidColor(pal.blue),
                    singleLine = true,
                    readOnly = !enabled || busy,
                    keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
                    keyboardActions = KeyboardActions(onDone = { save() }),
                    decorationBox = { inner -> FieldBox { inner() } },
                )
                val caption = if (hasError) uiText(mutation.error) else copy(UiMessages.scheduleSavingBlankUsesTheMessageSFirstSentence())
                Text(
                    caption,
                    style = T.s13,
                    color = if (hasError) pal.red else pal.ink2,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.fillMaxWidth().padding(bottom = 14.dp),
                )
                Btns {
                    PillButton(copy(UiMessages.commonCancel()), onClose, Modifier.weight(1f), enabled = !busy)
                    PillButton(
                        if (busy) copy(UiMessages.scheduleAndroidSavingFf509C()) else copy(UiMessages.commonSave()),
                        { save() },
                        Modifier.weight(1f),
                        ButtonKind.INK,
                        enabled = enabled && !busy && !mutation.frozen,
                    )
                }
            }
        }
    }
}

@Composable
internal fun ScheduleMessageEditor(
    formKey: String,
    initial: String,
    mutation: ScheduleMutationView,
    writable: Boolean,
    onSetMessage: (String) -> Unit,
    close: () -> Unit,
    save: () -> Unit,
    onRetryMutation: () -> Unit = {},
    onAbandonMutation: () -> Unit = {},
) {
    val copy = uiText()
    var savedInitial by remember(formKey) { mutableStateOf(initial) }
    var text by remember(formKey) { mutableStateOf(initial) }
    LaunchedEffect(formKey) { savedInitial = initial }
    var discard by remember { mutableStateOf(false) }
    val busy = mutation.busy
    val valid = text.isNotBlank() && text.toByteArray(Charsets.UTF_8).size <= SCHEDULE_MESSAGE_MAX_BYTES
    fun cancel() {
        if (text != savedInitial) discard = true else close()
    }
    Dialog({ if (!busy) cancel() }, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        val view = LocalView.current
        val light = pal.ink.luminance() < .5f
        SideEffect {
            (view.parent as? DialogWindowProvider)?.window?.let { window ->
                WindowCompat.getInsetsController(window, view).apply {
                    isAppearanceLightStatusBars = light
                    isAppearanceLightNavigationBars = light
                }
            }
        }
        BackHandler { if (!busy) cancel() }
        Column(Modifier.fillMaxSize().background(pal.paper).safeDrawingPadding().imePadding()) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp), verticalAlignment = Alignment.CenterVertically) {
                TextAction(copy(UiMessages.commonCancel()), { cancel() }, enabled = !busy)
                Text(
                    copy(UiMessages.scheduleWhatShouldTheAgentDo()),
                    Modifier.weight(1f),
                    style = T.s15b,
                    color = pal.ink,
                    textAlign = TextAlign.Center,
                )
                TextAction(
                    if (busy) copy(UiMessages.scheduleAndroidSaving()) else copy(UiMessages.commonSave()),
                    save,
                    enabled = !busy && writable && valid,
                )
            }
            ScheduleMutationBanner(
                mutation = mutation,
                onRetry = onRetryMutation,
                onAbandon = onAbandonMutation,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
            )
            BasicTextField(
                text,
                {
                    text = it
                    onSetMessage(it)
                },
                Modifier.weight(1f).fillMaxWidth().padding(16.dp),
                textStyle = T.s15.copy(color = pal.ink),
                cursorBrush = SolidColor(pal.blue),
                readOnly = busy || !writable,
            )
            if (text.toByteArray(Charsets.UTF_8).size > SCHEDULE_MESSAGE_MAX_BYTES) {
                Hint(copy(UiMessages.scheduleTooLongMaximum32Kb()), Modifier.padding(vertical = 16.dp), inset = 16.dp)
            }
        }
        if (discard) {
            Confirm(
                copy(UiMessages.scheduleDiscardChanges()),
                copy(UiMessages.scheduleAndroidChangesHaveNotBeenSaved()),
                copy(UiMessages.newSessionAndroidAbandon()),
                true,
                { discard = false },
            ) {
                discard = false
                close()
            }
        }
    }
}

@Composable
internal fun ScheduleRunGroup(
    group: ScheduleHistoryGroupView,
    onToggleQuiet: () -> Unit,
    openRun: (ScheduleRunRowView) -> Unit,
) {
    if (group.foldedQuiet && group.runsCount > 1) {
        Box(Modifier.bottomLine(pal.sep)) {
            GRow(
                uiText(group.foldedTitle),
                singleLineValue = true,
                onClick = onToggleQuiet,
                trailing = { Ic(if (group.expanded) Lucide.ChevronUp else Lucide.ChevronDown, 16.dp, pal.ink2) },
            )
        }
        if (group.foldedSummary.isCoreTextNotEmpty()) {
            Text(
                uiText(group.foldedSummary),
                style = T.s12,
                color = pal.ink2,
                modifier = Modifier.padding(start = 14.dp, end = 14.dp, bottom = 12.dp),
            )
        }
        if (!group.expanded) return
    }
    group.runsList.forEach { run ->
        Row(
            Modifier.fillMaxWidth().heightIn(min = 44.dp)
                .then(if (run.canOpenSession && run.sessionId.isNotEmpty()) Modifier.clickable { openRun(run) } else Modifier)
                .bottomLine(pal.sep).padding(horizontal = 14.dp, vertical = 12.dp),
            verticalAlignment = Alignment.Top,
        ) {
            Box(Modifier.width(22.dp).padding(top = 5.dp), contentAlignment = Alignment.TopCenter) {
                if (run.unreadOnEntry) ScheduleResultDot()
            }
            Column(Modifier.weight(1f)) {
                Text(uiText(run.timeText), style = T.s13, color = pal.ink2)
                Text(uiText(run.summaryText), style = T.s15, color = pal.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
            if (run.canOpenSession && run.sessionId.isNotEmpty()) Ic(Lucide.ChevronRight, 16.dp, pal.ink3)
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
internal fun ScheduleAgentSetting(
    agents: List<AgentOptionView>,
    selected: io.github.lewin671.remoteagent.proto.Agent,
    enabled: Boolean,
    onSelect: (io.github.lewin671.remoteagent.proto.Agent) -> Unit,
) {
    if (agents.size < 2) return
    val idx = agents.indexOfFirst { it.agent == selected || it.selected }.coerceAtLeast(0)
    Box(Modifier.bottomLine(pal.sep)) {
        GRow("CLI", trailing = {
            SegPick(agents.map { it.displayName }, idx, enabled) { onSelect(agents[it].agent) }
        })
    }
}

@Composable
fun NewScheduleScreen(session: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val home by remote.homeView.collectAsState()
    val projectView by remote.projectView.collectAsState()
    val vm by remote.scheduleFormView.collectAsState()
    val current = session.isNotEmpty()
    val mode = if (current) ScheduleFormMode.SCHEDULE_FORM_MODE_CREATE_FOR_SESSION else ScheduleFormMode.SCHEDULE_FORM_MODE_CREATE_NEW

    LaunchedEffect(remote, session) {
        remote.coreHost.openScheduleForm(mode = mode, sessionId = session)
    }

    fun close() {
        if (vm.formKey.isNotEmpty()) remote.coreHost.closeScheduleForm(vm.formKey, force = true)
        nav.back()
    }

    val content: @Composable () -> Unit = {
        ScheduleEditorContent(
            vm = vm,
            sidebarProjects = home.sidebarProjects,
            dirPicker = projectView.manage.dirPicker,
            connBanner = { ConnBanner(state) },
            onClose = ::close,
            onSetMessage = { remote.coreHost.setScheduleFormMessage(vm.formKey, it) },
            onSetTitle = { remote.coreHost.setScheduleFormTitle(vm.formKey, it) },
            onSetNotify = { remote.coreHost.setScheduleFormNotify(vm.formKey, it) },
            onOpenDirPicker = { remote.coreHost.openDirPicker(forNewTask = false) },
            onCloseDirPicker = { remote.coreHost.closeDirPicker() },
            onSelectProject = { key, machineId -> remote.coreHost.selectScheduleFormProject(vm.formKey, key, machineId) },
            onSelectMachine = { machineId -> remote.coreHost.selectScheduleFormMachine(vm.formKey, machineId) },
            onSelectAgent = { agent -> remote.coreHost.selectScheduleFormAgent(vm.formKey, agent) },
            onSelectModel = { model, effort, approval ->
                remote.coreHost.selectScheduleFormModel(vm.formKey, model, effort, approval)
            },
            onRetryModels = { remote.coreHost.retryScheduleFormModels(vm.formKey) },
            onUpdateTiming = { b -> remote.coreHost.updateScheduleFormTiming(b.setFormKey(vm.formKey).build()) },
            onSubmit = { confirmed -> remote.coreHost.submitScheduleForm(vm.formKey, confirmed) },
            onRetryMutation = { remote.coreHost.retryScheduleMutation(formKey = vm.formKey) },
            onAbandonMutation = { remote.coreHost.abandonScheduleMutation(formKey = vm.formKey) },
        )
    }

    if (session.isEmpty()) content()
    else Sheet(::close, scroll = false) { Box(Modifier.fillMaxHeight(.9f)) { content() } }
}

@Composable
internal fun ScheduleEditorContent(
    vm: ScheduleFormViewModel,
    sidebarProjects: ProjectListingView = ProjectListingView.getDefaultInstance(),
    dirPicker: DirPickerView = DirPickerView.getDefaultInstance(),
    connBanner: @Composable () -> Unit = {},
    onClose: () -> Unit = {},
    onSetMessage: (String) -> Unit = {},
    onSetTitle: (String) -> Unit = {},
    onSetNotify: (Boolean) -> Unit = {},
    onOpenDirPicker: () -> Unit = {},
    onCloseDirPicker: () -> Unit = {},
    onSelectProject: (projectKey: String, machineId: String) -> Unit = { _, _ -> },
    onSelectMachine: (machineId: String) -> Unit = {},
    onSelectAgent: (io.github.lewin671.remoteagent.proto.Agent) -> Unit = {},
    onSelectModel: (model: String, effort: String, mode: io.github.lewin671.remoteagent.proto.ApprovalMode?) -> Unit = { _, _, _ -> },
    onRetryModels: () -> Unit = {},
    onUpdateTiming: (ScheduleFormUpdateTimingAction.Builder) -> Unit = {},
    onSubmit: (fullAccessConfirmed: Boolean) -> Unit = {},
    onRetryMutation: () -> Unit = {},
    onAbandonMutation: () -> Unit = {},
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
        if (current) {
            SheetHead(copy(UiMessages.scheduleScheduleMessagesToThisConversation()), copy(UiMessages.commonCancel()), onClose)
        } else {
            PageBar(copy(UiMessages.homeNewTask()), onClose)
        }
        connBanner()
        FormColumn(Modifier.weight(1f).fillMaxWidth().readableColumn().imePadding().verticalScrollFaded()) {
            ScheduleMutationBanner(vm.mutation, onRetryMutation, onAbandonMutation)
            vm.noticesList.forEach { n ->
                when (n.severity) {
                    ScheduleFormNoticeSeverity.SCHEDULE_FORM_NOTICE_SEVERITY_ERROR ->
                        Notice(uiText(n.text), tone = NoticeTone.ERROR)
                    ScheduleFormNoticeSeverity.SCHEDULE_FORM_NOTICE_SEVERITY_WARN ->
                        Notice(uiText(n.text), tone = NoticeTone.WARN)
                    else -> Hint(uiText(n.text))
                }
            }
            FieldLabel(copy(UiMessages.scheduleWhatShouldTheAgentDo()))
            InputField(
                message,
                {
                    if (vm.editable) {
                        message = it
                        onSetMessage(it)
                    }
                },
                copy(UiMessages.scheduleAndroidCheckProjectUpdatesAndNotifyMeOfNoteworthyChanges()),
                maxLines = if (current) 4 else 8,
            )
            if (vm.messageTooLong && vm.messageError.isCoreTextNotEmpty()) {
                Hint(uiText(vm.messageError))
            }
            FieldLabel(copy(UiMessages.scheduleTaskName()))
            InputField(
                title,
                {
                    if (vm.editable) {
                        val filtered = filterScheduleNameInput(it)
                        title = filtered
                        onSetTitle(filtered)
                    }
                },
                uiText(vm.titlePlaceholder),
                singleLine = true,
            )
            ToggleRow(copy(UiMessages.scheduleNotifyOnCompletion()), "", vm.notifyOnFinish, enabled = vm.editable) {
                onSetNotify(it)
            }
            ScheduleSetting(copy(UiMessages.scheduleWhen()), uiText(vm.timingSummary), vm.editable) {
                timeOpen = true
            }
            if (!current) {
                val projLabel = if (vm.hasProject) uiText(vm.projectName) else copy(UiMessages.scheduleAndroidChooseAProject())
                ScheduleSetting(copy(UiMessages.scheduleProject()), projLabel, vm.editable) {
                    pickProject = true
                }
                if (vm.hasProject) {
                    ScheduleAgentSetting(vm.availableAgentsList, vm.agent, vm.editable, onSelectAgent)
                    ScheduleSetting(copy(UiMessages.newSessionPermissions()), uiText(vm.approvalModeLabel), vm.editable) {
                        settingsFocus = ScheduleConfigFocus.MODE
                    }
                    val modelPart = uiText(vm.modelLabel).ifEmpty { copy(UiMessages.newSessionDefaultModel()) }
                    val modelDisplay = listOf(modelPart, vm.effortLabel).filter { it.isNotEmpty() }.joinToString(" · ")
                    val multiMachine = vm.machineChoicesCount > 1
                    ScheduleSetting(
                        if (multiMachine) copy(UiMessages.scheduleComputerAndModel()) else copy(UiMessages.modelsTitle()),
                        if (multiMachine && vm.machineName.isNotEmpty()) listOf(vm.machineName, modelDisplay).joinToString(" · ") else modelDisplay,
                        vm.editable,
                    ) {
                        settingsFocus = if (multiMachine) ScheduleConfigFocus.ALL else ScheduleConfigFocus.MODEL
                    }
                }
                Hint(copy(UiMessages.scheduleAndroidEachRunOpensANewConversationUnderThisTaskOutside()))
            } else {
                Hint(copy(UiMessages.scheduleAndroidTheMessageIsSentAfterTheCurrentTurnEndsWithout()))
            }
            if (vm.submitDisabledReason.isCoreTextNotEmpty() && !vm.canSubmit) {
                Hint(uiText(vm.submitDisabledReason))
            }
            PillButton(
                uiText(vm.submitLabel),
                {
                    if (!vm.canSubmit) return@PillButton
                    if (vm.requiresFullAccessConfirm) confirmFullAccess = true
                    else onSubmit(false)
                },
                Modifier.fillMaxWidth().padding(vertical = 16.dp),
                ButtonKind.INK,
                vm.canSubmit,
            )
        }
    }

    if (confirmFullAccess) {
        Confirm(
            copy(UiMessages.scheduleRunWithFullAccess()),
            copy(
                if (current) UiMessages.scheduleNoApprovalWillBeRequiredForFutureRunsExisting()
                else UiMessages.scheduleNoApprovalIsRequiredForAnyOperationWheneverThis(),
            ),
            copy(UiMessages.modesFullAccess()),
            danger = true,
            onDismiss = { confirmFullAccess = false },
            onConfirm = {
                confirmFullAccess = false
                onSubmit(true)
            },
        )
    }

    if (timeOpen) {
        ScheduleTimeSheet(
            draft = vm.timing,
            mutation = ScheduleMutationView.getDefaultInstance(),
            enabled = vm.editable,
            submitOnApply = false,
            onUpdate = onUpdateTiming,
            onAppliedSubmit = { timeOpen = false },
            close = {
                onUpdateTiming(ScheduleFormUpdateTimingAction.newBuilder().setResetDraft(true))
                timeOpen = false
            },
        )
    }

    if (pickProject) {
        Sheet({ pickProject = false }) {
            SheetHead(copy(UiMessages.projectsChoose()), copy(UiMessages.commonCancel()), { pickProject = false })
            if (sidebarProjects.pinnedCount + sidebarProjects.othersCount > 0 || vm.projectChoicesCount == 0) {
                CoreProjectList(
                    sidebarProjects,
                    onSearch = {},
                    onNew = {
                        onOpenDirPicker()
                        pickProject = false
                        pickDirectory = true
                    },
                ) { pr ->
                    CoreProjectRow(pr, retryEnabled = false, onClick = {
                        onSelectProject(pr.key, "")
                        pickProject = false
                    }) { Arrow(pal.ink3, ts(18.sp)) }
                }
            } else {
                vm.projectChoicesList.forEach { ch ->
                    GRow(
                        uiText(ch.name),
                        ch.shortPath,
                        onClick = {
                            onSelectProject(ch.projectKey, "")
                            pickProject = false
                        },
                        chevron = true,
                    )
                }
            }
        }
    }

    if (pickDirectory) {
        Sheet({ onCloseDirPicker(); pickDirectory = false }, scroll = false) {
            CoreDirPickerBody(
                dirPicker,
                title = copy(UiMessages.scheduleAndroidChooseDirectory()),
                onBack = { onCloseDirPicker(); pickDirectory = false },
            ) { m, path ->
                onCloseDirPicker()
                onSelectProject(path, m)
                pickDirectory = false
            }
        }
    }

    val focus = settingsFocus
    if (focus != null) {
        ScheduleConfigSheet(
            vm = vm,
            focus = focus,
            showSaveButton = false,
            onSelectMachine = onSelectMachine,
            onSelectAgent = onSelectAgent,
            onSelectModel = onSelectModel,
            onRetryModels = onRetryModels,
            onSave = { settingsFocus = null },
            onClose = { settingsFocus = null },
        )
    }
}

@Composable
private fun ScheduleConfigSheet(
    vm: ScheduleFormViewModel,
    focus: ScheduleConfigFocus,
    showSaveButton: Boolean,
    onSelectMachine: (String) -> Unit,
    onSelectAgent: (io.github.lewin671.remoteagent.proto.Agent) -> Unit,
    onSelectModel: (model: String, effort: String, mode: io.github.lewin671.remoteagent.proto.ApprovalMode?) -> Unit,
    onRetryModels: () -> Unit,
    onSave: (fullAccessConfirmed: Boolean) -> Unit,
    onRetryMutation: () -> Unit = {},
    onAbandonMutation: () -> Unit = {},
    onClose: () -> Unit,
) {
    val copy = uiText()
    var confirmFullAccess by remember { mutableStateOf(false) }
    val enabled = vm.editable && !vm.mutation.busy && !vm.mutation.frozen

    fun done() {
        if (!showSaveButton) {
            onClose()
            return
        }
        if (vm.requiresFullAccessConfirm) confirmFullAccess = true
        else onSave(false)
    }

    Sheet({ if (!vm.mutation.busy) onClose() }) {
        val actionText = if (showSaveButton) copy(UiMessages.commonSave()) else copy(UiMessages.commonDone())
        val actionEnabled = if (showSaveButton) vm.canSubmit else true
        Row(Modifier.fillMaxWidth().padding(bottom = 10.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(
                if (focus == ScheduleConfigFocus.MODE) copy(UiMessages.newSessionPermissions()) else copy(UiMessages.modelsTitle()),
                Modifier.weight(1f),
                style = T.s17b,
                color = pal.ink,
            )
            TextAction(copy(UiMessages.commonCancel()), { if (!vm.mutation.busy) onClose() }, enabled = !vm.mutation.busy)
            TextAction(actionText, ::done, enabled = actionEnabled)
        }
        if (showSaveButton) {
            ScheduleMutationBanner(vm.mutation, onRetryMutation, onAbandonMutation)
        }
        if (focus != ScheduleConfigFocus.MODE && !vm.lockMachine && vm.machineSelectable && vm.machineChoicesCount > 1) {
            FieldLabel(copy(UiMessages.commonMachine()))
            Column(Modifier.selectableGroup()) {
                vm.machineChoicesList.forEach { m ->
                    val rowEnabled = enabled && m.supported
                    Row(
                        Modifier.fillMaxWidth().heightIn(min = 44.dp).clip(Radii.R14)
                            .selectable(selected = m.selected, enabled = rowEnabled, role = Role.RadioButton) {
                                onSelectMachine(m.machineId)
                            },
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        Column(Modifier.weight(1f)) {
                            Text(m.name, style = T.s15, color = if (rowEnabled) pal.ink else pal.ink3, maxLines = 1)
                            if (m.note.isCoreTextNotEmpty()) {
                                Text(uiText(m.note), style = T.s12, color = pal.ink2, maxLines = 1)
                            }
                        }
                        if (m.selected) Ic(Lucide.Check, 18.dp, pal.blue)
                    }
                }
            }
        }
        if (focus != ScheduleConfigFocus.MODE && vm.availableAgentsCount > 1) {
            ScheduleAgentSetting(vm.availableAgentsList, vm.agent, enabled, onSelectAgent)
        }
        if (focus == ScheduleConfigFocus.MODE || focus == ScheduleConfigFocus.ALL) {
            FieldLabel(copy(UiMessages.newSessionPermissions()))
            Column(Modifier.selectableGroup()) {
                vm.modeChoicesList.forEach { opt ->
                    val rowEnabled = enabled && opt.enabled
                    val color = when {
                        !rowEnabled -> pal.ink3
                        opt.isDanger -> pal.red
                        else -> pal.ink
                    }
                    Row(
                        Modifier.fillMaxWidth().heightIn(min = 44.dp).clip(Radii.R14)
                            .selectable(selected = opt.selected, enabled = rowEnabled, role = Role.RadioButton) {
                                onSelectModel(vm.model, vm.effort, opt.mode)
                            },
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        Column(Modifier.weight(1f)) {
                            Text(uiText(opt.label), style = T.s15, color = color, maxLines = 1)
                            if (!opt.enabled && opt.disabledReason.isCoreTextNotEmpty()) {
                                Text(uiText(opt.disabledReason), style = T.s12, color = pal.ink2)
                            }
                        }
                        if (opt.selected) Ic(Lucide.Check, 18.dp, if (opt.isDanger) pal.red else pal.blue)
                    }
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
                    onAction = onRetryModels,
                )
                panel.modelsLoading && panel.modelsCount == 0 -> Text(
                    uiText(UiMessages.modelsAndroidReadingMachine(name = vm.machineName)),
                    style = T.s13,
                    color = pal.ink2,
                    modifier = Modifier.padding(vertical = 8.dp),
                )
                else -> {
                    val activeOption = panel.modelsList.firstOrNull { it.checked }
                    if (activeOption != null && activeOption.fixedEffortNote.isCoreTextNotEmpty()) {
                        Text(uiText(activeOption.fixedEffortNote), style = T.s13, color = pal.ink2, modifier = Modifier.padding(vertical = 4.dp))
                    } else if (panel.effortChoicesCount > 1) {
                        Text(copy(UiMessages.modelsEffort()), style = T.s12, color = pal.ink2)
                        ModelSegments(panel.effortChoicesList, panel.selectedEffort, panelEnabled) { effort ->
                            onSelectModel(vm.model, effort, null)
                        }
                    }
                    val shown = panelVisibleModels(panel.modelsList, showAllModels)
                    Column(Modifier.selectableGroup()) {
                        ModelPanelRow(
                            uiText(panel.defaultModelLabel).ifEmpty { copy(UiMessages.modelsDefault()) },
                            panel.defaultModelChecked,
                            panelEnabled,
                        ) {
                            onSelectModel("", "", null)
                        }
                        shown.forEach { m ->
                            ModelPanelRow(m.displayName.ifEmpty { m.slug }, m.checked, panelEnabled) {
                                onSelectModel(m.slug, "", null)
                            }
                        }
                    }
                    if (!showAllModels && shown.size < panel.modelsCount) {
                        Row(
                            Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable(role = Role.Button) { showAllModels = true },
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Text(copy(UiMessages.modelsAll()), style = T.s15, color = pal.ink2, modifier = Modifier.weight(1f))
                            Ic(Lucide.ChevronRight, 18.dp, pal.ink3)
                        }
                    }
                }
            }
        }
    }

    if (confirmFullAccess) {
        Confirm(
            copy(UiMessages.scheduleRunWithFullAccess()),
            copy(UiMessages.scheduleNoApprovalIsRequiredForAnyOperationWheneverThis()),
            copy(UiMessages.modesFullAccess()),
            danger = true,
            onDismiss = { confirmFullAccess = false },
            onConfirm = {
                confirmFullAccess = false
                onSave(true)
            },
        )
    }
}

private data class TimingEpochEval(
    val zoneValid: Boolean,
    val atMs: Long?,
    val atDstGap: Boolean,
    val untilMs: Long?,
    val untilDstGap: Boolean,
)

private fun resolveTimingEpoch(
    zoneStr: String,
    dateStr: String,
    timeStr: String,
    untilDateStr: String,
): TimingEpochEval {
    val trimmed = zoneStr.trim()
    val zoneId = if (trimmed.isEmpty()) ZoneId.systemDefault() else runCatching { ZoneId.of(trimmed) }.getOrNull()
    if (zoneId == null) {
        return TimingEpochEval(zoneValid = false, atMs = null, atDstGap = false, untilMs = null, untilDstGap = false)
    }
    val date = runCatching { LocalDate.parse(dateStr) }.getOrNull()
    val time = runCatching { LocalTime.parse(timeStr) }.getOrNull()
    var atMs: Long? = null
    var atGap = false
    if (date != null && time != null) {
        val ldt = LocalDateTime.of(date, time)
        atGap = zoneId.rules.getValidOffsets(ldt).isEmpty()
        atMs = ldt.atZone(zoneId).toInstant().toEpochMilli()
    }
    val untilDate = runCatching { LocalDate.parse(untilDateStr) }.getOrNull()
    var untilMs: Long? = null
    var untilGap = false
    if (untilDate != null) {
        val udt = LocalDateTime.of(untilDate, LocalTime.of(23, 59, 59))
        untilGap = zoneId.rules.getValidOffsets(udt).isEmpty()
        untilMs = udt.atZone(zoneId).toInstant().toEpochMilli()
    }
    return TimingEpochEval(zoneValid = true, atMs = atMs, atDstGap = atGap, untilMs = untilMs, untilDstGap = untilGap)
}

@Composable
internal fun ScheduleTimeSheet(
    draft: ScheduleTimingDraftView,
    mutation: ScheduleMutationView,
    enabled: Boolean,
    submitOnApply: Boolean,
    onUpdate: (ScheduleFormUpdateTimingAction.Builder) -> Unit,
    onAppliedSubmit: () -> Unit,
    onRetryMutation: () -> Unit = {},
    onAbandonMutation: () -> Unit = {},
    close: () -> Unit,
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
        if (draft.timingError.isCoreTextNotEmpty()) {
            pendingApply = false
        } else if (draft.timingApplied) {
            pendingApply = false
            onAppliedSubmit()
        }
    }

    fun date(current: String, picked: (String) -> Unit) {
        val d = runCatching { LocalDate.parse(current) }.getOrElse { LocalDate.now() }
        android.app.DatePickerDialog(context, { _, y, m, day -> picked(LocalDate.of(y, m + 1, day).toString()) }, d.year, d.monthValue - 1, d.dayOfMonth).show()
    }

    val kindSegs = listOf(
        ScheduleTimingKind.SCHEDULE_TIMING_KIND_ONCE,
        ScheduleTimingKind.SCHEDULE_TIMING_KIND_DAILY,
        ScheduleTimingKind.SCHEDULE_TIMING_KIND_WEEKLY,
        ScheduleTimingKind.SCHEDULE_TIMING_KIND_INTERVAL,
    )
    val unitSegs = listOf(
        ScheduleIntervalUnit.SCHEDULE_INTERVAL_UNIT_MINUTES,
        ScheduleIntervalUnit.SCHEDULE_INTERVAL_UNIT_HOURS,
        ScheduleIntervalUnit.SCHEDULE_INTERVAL_UNIT_DAYS,
        ScheduleIntervalUnit.SCHEDULE_INTERVAL_UNIT_SECONDS,
    )
    val endSegs = listOf(
        ScheduleTimingEnd.SCHEDULE_TIMING_END_NEVER,
        ScheduleTimingEnd.SCHEDULE_TIMING_END_COUNT,
        ScheduleTimingEnd.SCHEDULE_TIMING_END_DATE,
    )
    val selectedKindIdx = if (draft.afterTurnUnselected) -1 else kindSegs.indexOf(draft.kind)
    val busy = mutation.busy
    val sheetEnabled = enabled && !busy

    fun applyNow() {
        if (!sheetEnabled) return
        if (draft.afterTurnUnselected) {
            close()
            return
        }
        val eval = resolveTimingEpoch(zoneInput, draft.date, draft.time, draft.untilDate)
        val b = ScheduleFormUpdateTimingAction.newBuilder()
            .setZone(zoneInput.trim())
            .setZoneValid(eval.zoneValid)
            .setAtDstGap(eval.atDstGap)
            .setUntilDstGap(eval.untilDstGap)
            .setApply(true)
        if (draft.kind == ScheduleTimingKind.SCHEDULE_TIMING_KIND_INTERVAL) b.setInterval(intervalInput)
        if (draft.end == ScheduleTimingEnd.SCHEDULE_TIMING_END_COUNT) b.setCount(countInput)
        eval.atMs?.let { b.setAtMs(it) }
        eval.untilMs?.let { b.setUntilMs(it) }
        pendingApply = true
        onUpdate(b)
    }

    Sheet({ if (!busy) close() }) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Text(copy(UiMessages.scheduleWhen()), Modifier.weight(1f), style = T.s17b, color = pal.ink)
            TextAction(
                if (submitOnApply && busy) copy(UiMessages.scheduleAndroidSaving()) else copy(UiMessages.commonDone()),
                ::applyNow,
                enabled = sheetEnabled && draft.timingDoneEnabled,
            )
        }
        if (submitOnApply) {
            ScheduleMutationBanner(mutation, onRetryMutation, onAbandonMutation)
        }
        SegPick(
            listOf(
                copy(UiMessages.scheduleOnceSchedules()),
                copy(UiMessages.scheduleDaily()),
                copy(UiMessages.scheduleWeeklyOn()),
                copy(UiMessages.scheduleEvery()),
            ),
            selectedKindIdx,
            sheetEnabled,
        ) { idx ->
            onUpdate(ScheduleFormUpdateTimingAction.newBuilder().setKind(kindSegs[idx]))
        }
        if (draft.afterTurnUnselected && draft.afterTurnHint.isCoreTextNotEmpty()) {
            Hint(uiText(draft.afterTurnHint))
        } else {
            if (draft.timingError.isCoreTextNotEmpty()) {
                Notice(uiText(draft.timingError), tone = NoticeTone.ERROR)
            }
            if (draft.kind == ScheduleTimingKind.SCHEDULE_TIMING_KIND_ONCE) {
                ScheduleSetting(copy(UiMessages.scheduleAndroidDate()), draft.date, sheetEnabled) {
                    date(draft.date) { picked ->
                        onUpdate(ScheduleFormUpdateTimingAction.newBuilder().setDate(picked))
                    }
                }
            }
            if (draft.kind != ScheduleTimingKind.SCHEDULE_TIMING_KIND_INTERVAL) {
                ScheduleSetting(copy(UiMessages.scheduleTime()), draft.time, sheetEnabled) {
                    val t = runCatching { LocalTime.parse(draft.time) }.getOrElse { LocalTime.of(8, 0) }
                    android.app.TimePickerDialog(context, { _, h, m ->
                        onUpdate(ScheduleFormUpdateTimingAction.newBuilder().setTime("%02d:%02d".format(h, m)))
                    }, t.hour, t.minute, true).show()
                }
            } else {
                FieldLabel(copy(UiMessages.scheduleAndroidInterval()))
                InputField(
                    intervalInput,
                    {
                        if (sheetEnabled) {
                            intervalInput = it
                            onUpdate(ScheduleFormUpdateTimingAction.newBuilder().setInterval(it))
                        }
                    },
                    copy(UiMessages.scheduleAndroidPositiveInteger()),
                )
                GRow(copy(UiMessages.scheduleAndroidUnit()), trailing = {
                    SegPick(
                        listOf(
                            copy(UiMessages.scheduleAndroidMinutes()),
                            copy(UiMessages.scheduleAndroidHours()),
                            copy(UiMessages.scheduleAndroidDays()),
                            copy(UiMessages.scheduleAndroidSeconds()),
                        ),
                        unitSegs.indexOf(draft.unit).coerceAtLeast(0),
                        sheetEnabled,
                    ) { idx ->
                        onUpdate(ScheduleFormUpdateTimingAction.newBuilder().setUnit(unitSegs[idx]))
                    }
                })
            }
            if (draft.kind == ScheduleTimingKind.SCHEDULE_TIMING_KIND_DAILY) {
                ToggleRow(
                    copy(UiMessages.scheduleWeekdaysOnly()),
                    copy(UiMessages.scheduleAndroidMondayToFriday()),
                    draft.workdays,
                    enabled = sheetEnabled,
                ) { checked ->
                    onUpdate(ScheduleFormUpdateTimingAction.newBuilder().setWorkdays(checked))
                }
            }
            if (draft.kind == ScheduleTimingKind.SCHEDULE_TIMING_KIND_WEEKLY) {
                val days = draft.weekdaysList.toSet()
                Row(
                    Modifier.fillMaxWidth().clip(Radii.R14).background(pal.fill2).padding(2.dp),
                    horizontalArrangement = Arrangement.spacedBy(2.dp),
                ) {
                    val dayLabels = listOf(
                        copy(UiMessages.scheduleMon()),
                        copy(UiMessages.scheduleTue()),
                        copy(UiMessages.scheduleWed()),
                        copy(UiMessages.scheduleThu()),
                        copy(UiMessages.scheduleFri()),
                        copy(UiMessages.scheduleSat()),
                        copy(UiMessages.scheduleSun()),
                    )
                    (1..7).forEach { day ->
                        val selected = day in days
                        Box(
                            Modifier.weight(1f).heightIn(min = 44.dp).clip(Radii.R14)
                                .background(
                                    if (selected) (if (pal.paper == Color.White) pal.paper else pal.fill2)
                                    else Color.Transparent,
                                )
                                .toggleable(value = selected, enabled = sheetEnabled, role = Role.Checkbox) {
                                    val next = if (selected) days - day else days + day
                                    onUpdate(
                                        ScheduleFormUpdateTimingAction.newBuilder()
                                            .setReplaceWeekdays(true)
                                            .addAllWeekdays(next.sorted()),
                                    )
                                },
                            contentAlignment = Alignment.Center,
                        ) {
                            Text(
                                dayLabels[day - 1],
                                style = if (selected) T.s13b else T.s13,
                                color = if (selected) pal.ink else pal.ink2,
                            )
                        }
                    }
                }
            }
            if (draft.kind != ScheduleTimingKind.SCHEDULE_TIMING_KIND_ONCE) {
                GRow(copy(UiMessages.sessionEnd()), trailing = {
                    SegPick(
                        listOf(
                            copy(UiMessages.scheduleNever()),
                            copy(UiMessages.scheduleAfterARunCount()),
                            copy(UiMessages.scheduleEndDate()),
                        ),
                        endSegs.indexOf(draft.end).coerceAtLeast(0),
                        sheetEnabled,
                    ) { idx ->
                        onUpdate(ScheduleFormUpdateTimingAction.newBuilder().setEnd(endSegs[idx]))
                    }
                })
                if (draft.end == ScheduleTimingEnd.SCHEDULE_TIMING_END_COUNT) {
                    InputField(
                        countInput,
                        {
                            if (sheetEnabled) {
                                countInput = it
                                onUpdate(ScheduleFormUpdateTimingAction.newBuilder().setCount(it))
                            }
                        },
                        copy(UiMessages.scheduleAndroidAutomaticRunCount()),
                    )
                }
                if (draft.end == ScheduleTimingEnd.SCHEDULE_TIMING_END_DATE) {
                    ScheduleSetting(copy(UiMessages.scheduleAndroidEndDate()), draft.untilDate, sheetEnabled) {
                        date(draft.untilDate) { picked ->
                            onUpdate(ScheduleFormUpdateTimingAction.newBuilder().setUntilDate(picked))
                        }
                    }
                }
            }
            if (draft.kind != ScheduleTimingKind.SCHEDULE_TIMING_KIND_INTERVAL) {
                FieldLabel(copy(UiMessages.scheduleTimeZone()))
                InputField(
                    zoneInput,
                    { next ->
                        if (sheetEnabled) {
                            zoneInput = next
                            val trimmed = next.trim()
                            val nextZone = runCatching { ZoneId.of(trimmed) }.getOrNull()
                            val b = ScheduleFormUpdateTimingAction.newBuilder().setZone(trimmed)
                            if (nextZone != null) {
                                b.setZoneValid(true)
                                val ms = draft.untilMs.takeIf { it > 0L }
                                    ?: runCatching { LocalDate.parse(draft.untilDate) }.getOrNull()
                                        ?.let { LocalDateTime.of(it, LocalTime.of(23, 59, 59)).atZone(ZoneId.systemDefault()).toInstant().toEpochMilli() }
                                if (ms != null && ms > 0L) {
                                    b.setUntilMs(ms)
                                    b.setUntilDate(Instant.ofEpochMilli(ms).atZone(nextZone).toLocalDate().toString())
                                }
                            } else if (trimmed.isNotEmpty()) {
                                b.setZoneValid(false)
                            }
                            onUpdate(b)
                        }
                    },
                    copy(UiMessages.scheduleAndroidForExampleAsiaShanghai()),
                )
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
    val strip by remember(remote, sid) { remote.coreHost.watchScheduleSessionStrip(sid) }
        .collectAsState(initial = ScheduleSessionStripViewModel.getDefaultInstance())
    SessionScheduleStripContent(
        vm = strip,
        modifier = modifier,
        onOpenTask = { machineId, taskId -> nav.task(machineId, taskId) },
    )
}

@Composable
internal fun SessionScheduleStripContent(
    vm: ScheduleSessionStripViewModel,
    modifier: Modifier = Modifier,
    onOpenTask: (machineId: String, taskId: String) -> Unit = { _, _ -> },
) {
    val copy = uiText()
    if (!vm.visible || vm.tasksCount == 0) return
    val tasks = vm.tasksList
    val first = tasks.first()
    var choosing by remember { mutableStateOf(false) }
    if (choosing) {
        Sheet({ choosing = false }) {
            SheetHead(copy(UiMessages.queueAndroidScheduled()), copy(UiMessages.commonCancel()), { choosing = false })
            tasks.forEach { t ->
                GRow(
                    t.title,
                    onClick = {
                        choosing = false
                        onOpenTask(t.machineId, t.taskId)
                    },
                    chevron = true,
                    singleLineValue = true,
                )
            }
        }
    }
    Strip(
        Lucide.Clock,
        uiText(vm.summaryText),
        modifier,
        action = copy(UiMessages.scheduleManage()),
        onClick = {
            if (tasks.size == 1) onOpenTask(first.machineId, first.taskId) else choosing = true
        },
    )
}
