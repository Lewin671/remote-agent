package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.SizeTransform
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.launch
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.plain
import io.github.lewin671.remoteagent.core.proto.SessionApprovalDockView
import io.github.lewin671.remoteagent.core.proto.SessionCommandStatus
import io.github.lewin671.remoteagent.core.proto.SessionDockKind
import io.github.lewin671.remoteagent.core.proto.SessionDockView
import io.github.lewin671.remoteagent.core.proto.SessionPlanChoiceOptionView
import io.github.lewin671.remoteagent.core.proto.SessionQuestionDockView
import io.github.lewin671.remoteagent.core.proto.SessionViewModel
import io.github.lewin671.remoteagent.proto.PlanChoice

/** Fold state per session & dock item for this run. */
object DockFolds {
    private val chosen = mutableStateMapOf<String, Boolean>()
    private val cameWhileDrafting = HashMap<String, Boolean>()

    fun isOpen(sid: String, itemId: String, drafting: () -> Boolean = { false }): Boolean {
        val key = "$sid/$itemId"
        chosen[key]?.let { return it }
        return !cameWhileDrafting.getOrPut(key, drafting)
    }

    fun set(sid: String, itemId: String, open: Boolean) {
        chosen["$sid/$itemId"] = open
    }

    fun clear() {
        chosen.clear()
        cameWhileDrafting.clear()
    }
}

private fun hasDraft(remote: io.github.lewin671.remoteagent.core.Remote, sid: String): Boolean {
    val draft = remote.currentSessionView(sid)?.composer?.takeIf { it.hasDraft() }?.draft ?: return false
    return draft.text.isNotBlank() || draft.itemsCount > 0 || draft.hasPendingPicks
}

val LocalDockMax = androidx.compose.runtime.compositionLocalOf { 240.dp }

val ChatMax = 720.dp

internal fun <S> androidx.compose.animation.AnimatedContentTransitionScope<S>.dockSwap(): androidx.compose.animation.ContentTransform =
    (fadeIn(Motion.fadeInSpec(Motion.BASE)) togetherWith fadeOut(Motion.fadeOutSpec(Motion.BASE))) using
        SizeTransform(clip = true) { _, _ -> Motion.enter(Motion.BASE) }

@Composable
fun DockShell(content: @Composable () -> Unit) {
    Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.TopCenter) {
        Box(Modifier.widthIn(max = ChatMax).fillMaxWidth().padding(start = 12.dp, end = 12.dp, bottom = 8.dp)) { content() }
    }
}

@Composable
private fun DockLine(label: String, rest: String, onOpen: () -> Unit) {
    val copy = uiText()
    val p = pal
    val shape = RoundedCornerShape(22.dp)
    Row(
        Modifier.fillMaxWidth().floating(p, shape).clip(shape).background(p.tc).border(0.5.dp, p.tcLine, shape)
            .heightIn(min = 48.dp).clickable(role = Role.Button, onClickLabel = copy(UiMessages.dockHandle()), onClick = onOpen)
            .padding(start = 16.dp, end = 14.dp, top = 6.dp, bottom = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(7.dp),
    ) {
        Box(Modifier.size(8.dp).clip(CircleShape).background(CardColors.orange))
        Text(
            buildAnnotatedString {
                withStyle(SpanStyle(color = CardColors.orange, fontWeight = T.s13b.fontWeight)) { append(label) }
                if (rest.isNotEmpty()) withStyle(SpanStyle(color = CardColors.text2)) { append(" · $rest") }
            },
            style = T.s13,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f),
        )
        ArrowText(copy(UiMessages.dockHandle()), CardColors.link, T.s13, down = true)
    }
}

@Composable
fun CardLink(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    up: Boolean = false,
    down: Boolean = false,
    arrow: Boolean = true,
    lead: androidx.compose.ui.unit.Dp = 12.dp,
) {
    Box(modifier.heightIn(min = 44.dp).clickable(role = Role.Button, onClick = onClick).padding(start = lead), contentAlignment = Alignment.CenterEnd) {
        if (arrow) ArrowText(text, CardColors.link, T.s13, down = down, up = up) else Text(text, style = T.s13, color = CardColors.link)
    }
}

@Composable
fun CardNote(text: String, modifier: Modifier = Modifier, action: String? = null, onAction: () -> Unit = {}) {
    Row(modifier.fillMaxWidth().padding(top = 10.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text, style = T.s13, color = CardColors.red, modifier = Modifier.weight(1f))
        if (action != null) CardLink(action, onAction, arrow = false)
    }
}

@Composable
private fun DockSkeleton() = DockShell {
    DarkCard(float = true) { Text(uiText(UiMessages.sessionSkeleton()), style = T.s15, color = CardColors.text2) }
}

/** Main entry point for Session's docked pending action card (`SessionViewModel.dock`). */
@Composable
internal fun SessionDock(
    vm: SessionViewModel,
    onTerminal: () -> Unit,
    onPlan: () -> Unit,
    onQuestion: (qid: String) -> Unit,
    onApproval: (aid: String) -> Unit,
    onDenyingChange: (Boolean) -> Unit = {},
    onOpenChange: (Boolean) -> Unit = {},
) {
    val dock = vm.dock
    val externalText = if (dock.hasExternalResolution() && dock.externalResolution.hasText()) {
        uiText(dock.externalResolution.text)
    } else ""

    if (!dock.hasItem) {
        DisposableEffect(Unit) {
            onOpenChange(false)
            onDenyingChange(false)
            onDispose { }
        }
        if (externalText.isNotEmpty()) {
            DockShell {
                DarkCardLine(externalText, "")
            }
        }
        return
    }
    if (externalText.isNotEmpty()) {
        DockShell {
            DarkCardLine(externalText, "")
        }
    }
    val sid = vm.sessionId
    when (dock.kind) {
        SessionDockKind.SESSION_DOCK_KIND_APPROVAL -> ApprovalDock(
            sid = sid,
            vm = vm,
            onDenyingChange = onDenyingChange,
            onOpenChange = onOpenChange,
            onFull = onApproval,
        )
        SessionDockKind.SESSION_DOCK_KIND_QUESTION -> {
            DisposableEffect(Unit) {
                onDenyingChange(false)
                onDispose {}
            }
            QuestionDock(sid, vm, onTerminal, onQuestion, onOpenChange = onOpenChange)
        }
        SessionDockKind.SESSION_DOCK_KIND_PLAN -> {
            DisposableEffect(Unit) {
                onDenyingChange(false)
                onDispose {}
            }
            PlanDock(sid = sid, vm = vm, onOpenChange = onOpenChange, onFull = onPlan)
        }
        SessionDockKind.SESSION_DOCK_KIND_TERMINAL_POPUP -> {
            DisposableEffect(Unit) {
                onDenyingChange(false)
                onDispose {}
            }
            PopupDock(sid, dock, onTerminal, onOpenChange = onOpenChange)
        }
        else -> {}
    }
}

@Composable
private fun PendingCoreDock(
    sid: String,
    dock: SessionDockView,
    headExtra: (@Composable () -> Unit)? = null,
    scrollState: ScrollState = rememberScrollState(),
    scrollMinHeight: androidx.compose.ui.unit.Dp = 0.dp,
    onOpenChange: (Boolean) -> Unit = {},
    onScrollMeasured: () -> Unit = {},
    body: @Composable ColumnScope.(close: () -> Unit) -> Unit,
    actions: @Composable ColumnScope.() -> Unit,
) {
    val copy = uiText()
    val remote = LocalRemote.current
    val itemId = dock.kind.name + ":" + dock.itemId
    val open = DockFolds.isOpen(sid, itemId) { hasDraft(remote, sid) }
    DisposableEffect(open) {
        onOpenChange(open)
        onDispose { onOpenChange(false) }
    }
    val keyboard = LocalSoftwareKeyboardController.current
    val focus = LocalFocusManager.current
    val label = uiText(dock.title)
    val summary = uiText(dock.summary)
    val meta = uiText(dock.meta)
    val left = uiText(dock.timeLeftPlain)
    val moreStr = if (dock.moreCount > 0) copy(UiMessages.dockMore(count = dock.moreCount)) else ""
    val rest = if (open) {
        listOf(meta, moreStr, left).filter { it.isNotEmpty() }.joinToString(" · ")
    } else {
        listOf(summary, moreStr).filter { it.isNotEmpty() }.joinToString(" · ")
    }
    val toggle = {
        if (!open) {
            keyboard?.hide()
            focus.clearFocus()
        }
        DockFolds.set(sid, itemId, !open)
    }
    AnimatedContent(open, transitionSpec = { dockSwap() }, label = "dock-fold") { isOpen ->
        DockShell {
            if (!isOpen) DockLine(label, rest, toggle)
            else DarkCard(float = true) {
                DarkCardHead(label, rest, trailing = {
                    headExtra?.invoke()
                    CardLink(copy(UiMessages.markdownCollapse()), toggle, up = true)
                })
                Column(Modifier.heightIn(max = LocalDockMax.current)) {
                    Column(
                        Modifier.weight(1f, fill = false)
                            .then(if (scrollMinHeight > 0.dp) Modifier.heightIn(min = scrollMinHeight) else Modifier)
                            .onGloballyPositioned { onScrollMeasured() }
                            .verticalScroll(scrollState),
                    ) {
                        body { DockFolds.set(sid, itemId, false) }
                    }
                    Column { actions() }
                }
            }
        }
    }
}

@Composable
fun ApprovalContent(a: SessionApprovalDockView) {
    val body = a.body.trim().ifEmpty { a.commandPreview.trim() }
    if (body.isNotEmpty()) CommandBlock(body, Modifier.padding(top = 14.dp))
    if (a.bodyTruncated) Text(uiText(UiMessages.approvalTruncated()), style = T.s13, color = CardColors.red, modifier = Modifier.padding(top = 8.dp))
    if (a.description.isNotEmpty()) {
        Text(a.description.trim(), style = T.s15, color = CardColors.text2, modifier = Modifier.padding(top = 12.dp))
    }
}

@Composable
internal fun ApprovalDock(
    sid: String,
    vm: SessionViewModel,
    onDenyingChange: (Boolean) -> Unit = {},
    onOpenChange: (Boolean) -> Unit = {},
    onFull: (String) -> Unit,
) {
    val copy = uiText()
    val remote = LocalRemote.current
    val dock = vm.dock
    val a = dock.approval
    if (dock.isSkeleton || a.isSkeleton) return DockSkeleton()
    val d = rememberApprovalReply(sid, a.approvalId, dock.command, isHome = false, skeleton = a.isSkeleton)
    val t = now(15_000)
    val locked = vm.header.computerUnlinked || (dock.deadlineMs > 0 && t >= dock.deadlineMs)
    val denying = a.denyingOpen
    var expandFull by rememberSaveable(a.approvalId) { mutableStateOf(false) }
    var dockOpen by remember { mutableStateOf(true) }
    val scrollState = rememberScrollState()
    var seenBottom by rememberSaveable(a.approvalId) { mutableStateOf(false) }
    var scrollMeasured by remember(a.approvalId) { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    LaunchedEffect(scrollMeasured, scrollState.value, scrollState.maxValue, a.approvalId) {
        if (scrollMeasured && (scrollState.maxValue == 0 || scrollState.value >= scrollState.maxValue - 4)) {
            seenBottom = true
        }
    }

    val showCompactSubmitted = d.submitted && !a.holdOpenCard
    val activeDenying = denying && dockOpen && !showCompactSubmitted
    DisposableEffect(activeDenying) {
        onDenyingChange(activeDenying)
        onDispose { onDenyingChange(false) }
    }

    AnimatedContent(showCompactSubmitted, transitionSpec = { dockSwap() }, label = "approval-answered") { submitted ->
        if (submitted) DockShell { ApprovalSubmitted(uiText(dock.title), d) }
        else PendingCoreDock(
            sid,
            dock,
            headExtra = if (a.body.length > 240) { { CardLink(copy(UiMessages.markdownExpand()), { onFull(a.approvalId) }) } } else null,
            scrollState = scrollState,
            scrollMinHeight = if (denying) 120.dp else 0.dp,
            onOpenChange = {
                dockOpen = it
                onOpenChange(it)
            },
            onScrollMeasured = { scrollMeasured = true },
            body = {
                val shown = a.body.trim().ifEmpty { a.commandPreview.trim() }
                if (shown.isNotEmpty()) {
                    CommandBlock(shown, Modifier.padding(top = 14.dp), maxLines = if (expandFull || denying) Int.MAX_VALUE else CARD_LINES)
                }
                if (a.bodyTruncated) Text(copy(UiMessages.approvalTruncated()), style = T.s13, color = CardColors.red, modifier = Modifier.padding(top = 8.dp))
                if (a.description.isNotEmpty()) Text(a.description.trim(), style = T.s15, color = CardColors.text2, modifier = Modifier.padding(top = 12.dp))
            },
        ) {
            ApprovalButtons(
                approvalId = a.approvalId,
                d = d,
                denying = denying,
                onDenying = { remote.editApprovalDeny(sid, a.approvalId, it) },
                locked = locked,
                truncated = a.bodyTruncated,
                always = a.canAlwaysAllow,
                alwaysLabel = uiText(a.alwaysLabel).ifEmpty { copy(UiMessages.approvalAlwaysAllow()) },
                denyPlaceholder = uiText(a.denyReasonPlaceholder).ifEmpty { copy(UiMessages.approvalReasonPlaceholder()) },
                scrollToBottomNeeded = !denying && scrollState.maxValue > 0 && !seenBottom,
                onScrollToBottom = { scope.launch { scrollState.animateScrollTo(scrollState.maxValue) } },
            )
        }
    }
}

private fun longQuestionStep(q: io.github.lewin671.remoteagent.core.proto.SessionQuestionStepView): Boolean =
    q.questionText.length > 120 ||
        q.optionsCount > 4 ||
        q.optionsList.sumOf { it.label.length + it.description.length } > 400

@Composable
internal fun QuestionBody(
    sid: String,
    vm: SessionViewModel,
    onTerminal: () -> Unit,
    full: Boolean,
    onLong: () -> Unit = {},
    part: Int = 0,
) {
    val copy = uiText()
    val remote = LocalRemote.current
    val dock = vm.dock
    val q = dock.question
    if (dock.isSkeleton && q.question.questionText.isEmpty()) {
        if (part != 2) Text(copy(UiMessages.sessionSkeleton()), style = T.s15, color = CardColors.text2, modifier = Modifier.padding(top = 12.dp))
        return
    }
    val cur = q.question
    val cmd = dock.command
    val busy = cmd.busy || cmd.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING
    val frozen = cmd.frozen
    val expired = cmd.expired
    val answered = q.answered
    val errorText = if (cmd.hasErrorText()) uiText(cmd.errorText) else cmd.errorCode
    val pickedMulti = q.draftedOptionNumsList
    val submittedNums = q.submittedOptionNumsList

    if (part != 2) {
        Text(
            cur.questionText,
            style = T.s17.copy(lineHeight = 24.6.sp),
            color = CardColors.text,
            maxLines = if (full) Int.MAX_VALUE else 4,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(top = 12.dp),
        )
        if (!full && longQuestionStep(cur)) CardLink(copy(UiMessages.markdownExpand()), onLong, lead = 0.dp)
        cur.optionsList.forEach { o ->
            val sel = if (cur.multiSelect) {
                if (pickedMulti.isNotEmpty()) o.optionNum in pickedMulti
                else (busy || frozen) && o.optionNum in submittedNums
            } else {
                o.optionNum in submittedNums && (busy || frozen)
            }
            val ink = if (sel) CardColors.onPrimary else CardColors.text
            val ink2 = if (sel) CardColors.onPrimary.copy(alpha = .6f) else CardColors.text2
            Row(
                Modifier.fillMaxWidth()
                    .padding(top = 8.dp)
                    .heightIn(min = 46.dp)
                    .clip(Radii.R14)
                    .background(if (sel) CardColors.primary else CardColors.field)
                    .clickable(enabled = !answered && !busy && !frozen, role = if (cur.multiSelect) Role.Checkbox else Role.Button) {
                        if (cur.multiSelect) {
                            remote.toggleQuestionOption(sid, q.questionId, o.optionNum)
                        } else {
                            remote.answerQuestion(sid, q.questionId, optionNum = o.optionNum)
                        }
                    }
                    .padding(horizontal = 14.dp, vertical = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(10.dp),
            ) {
                if (cur.multiSelect) {
                    Box(
                        Modifier.size(20.dp).clip(RoundedCornerShape(5.dp)).border(1.5.dp, ink2, RoundedCornerShape(5.dp)),
                        contentAlignment = Alignment.Center,
                    ) {
                        if (sel) Mark(Lucide.Check, ink, 14.dp)
                    }
                }
                Column(Modifier.weight(1f)) {
                    Text(o.label, style = T.s17.copy(lineHeight = 23.sp), color = ink)
                    if (o.description.isNotEmpty()) Text(o.description, style = T.s13, color = ink2)
                    if (sel && busy && !cur.multiSelect) Text(copy(UiMessages.dockSubmitting()), style = T.s13, color = ink2)
                }
                if (o.recommended) Text(
                    copy(UiMessages.dockRecommended()),
                    style = T.s12b,
                    color = if (sel) ink2 else CardColors.text.copy(alpha = .8f),
                    modifier = Modifier.clip(Radii.Pill).background(if (sel) Color(0x14000000) else Color(0x24FFFFFF)).padding(horizontal = 8.dp, vertical = 2.dp),
                )
            }
        }
        if (answered && q.hasAnsweredText()) {
            Text(uiText(q.answeredText), style = T.s13, color = CardColors.text2, modifier = Modifier.padding(top = 10.dp))
        } else if (!busy && frozen) {
            val shown = cmd.takeIf { it.hasSubmittedSummary() }?.let { uiText(it.submittedSummary) }.orEmpty()
                .ifEmpty {
                    submittedNums.joinToString("、") { num ->
                        cur.optionsList.firstOrNull { it.optionNum == num }?.label ?: num.toString()
                    }
                }
            CardNote(
                if (expired) copy(UiMessages.dockAndroidAnswerExpired(answer = shown))
                else copy(UiMessages.dockAndroidAnswerUnknown(answer = shown)),
                action = if (cmd.canAbandon) copy(UiMessages.newSessionAndroidAbandon()) else null,
            ) {
                remote.abandonSessionCommand(sid, cmd.slotKey)
            }
        } else if (errorText.isNotEmpty()) {
            CardNote(copy(UiMessages.dockAndroidQuestionChanged(reason = errorText)))
        }
    }
    if (part != 1) {
        val n = pickedMulti.size
        if (cur.multiSelect) {
            val btnLabel = if (q.hasSubmitButtonLabel()) uiText(q.submitButtonLabel)
            else if (busy) copy(UiMessages.dockSubmitting())
            else if (n == 0) copy(UiMessages.dockSelectThenSubmit())
            else copy(UiMessages.dockSubmitSelected(count = n))
            CardButton(
                btnLabel,
                { remote.answerQuestion(sid, q.questionId, optionNums = pickedMulti) },
                Modifier.fillMaxWidth().padding(top = 16.dp),
                primary = true,
                enabled = q.canSubmitMulti,
            )
        } else if (frozen && !expired && cmd.canRetry && cmd.slotKey.isNotEmpty()) {
            CardButton(
                if (busy) copy(UiMessages.dockSubmitting()) else copy(UiMessages.commonRetry()),
                { remote.retrySessionCommand(sid, cmd.slotKey) },
                Modifier.fillMaxWidth().padding(top = 16.dp),
                primary = true,
                enabled = !busy,
            )
        }
        Row(Modifier.fillMaxWidth().padding(top = if (cur.multiSelect) 4.dp else 8.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(if (cur.multiSelect && n > 0) copy(UiMessages.dockSelected(count = n)) else copy(UiMessages.dockNoneFit()), style = T.s13, color = CardColors.text2)
            CardLink(copy(UiMessages.dockOtherTerminal()), onTerminal)
        }
    }
}

@Composable
private fun QuestionDock(sid: String, vm: SessionViewModel, onTerminal: () -> Unit, onQuestion: (String) -> Unit, onOpenChange: (Boolean) -> Unit = {}) {
    val q = vm.dock.question
    PendingCoreDock(
        sid,
        vm.dock,
        onOpenChange = onOpenChange,
        body = { QuestionBody(sid, vm, onTerminal, full = false, onLong = { onQuestion(q.questionId) }, part = 1) },
    ) {
        QuestionBody(sid, vm, onTerminal, full = false, part = 2)
    }
}

@Composable
fun QuestionPage(sid: String, qid: String) {
    val copy = uiText()
    val remote = LocalRemote.current
    val nav = LocalNav.current
    LaunchedEffect(remote, sid) { remote.openSession(sid) }
    val vm by remote.watchSession(sid).collectAsState(initial = SessionViewModel.getDefaultInstance())
    val hasQuestion = vm.dock.hasItem && vm.dock.kind == SessionDockKind.SESSION_DOCK_KIND_QUESTION &&
        (qid.isEmpty() || vm.dock.question.questionId == qid)
    val initialSeq = remember(sid, qid) { vm.dock.command.succeededSeq }
    val succeededSeq = if (hasQuestion) vm.dock.command.succeededSeq else 0L
    val isLastStep = hasQuestion && vm.dock.question.isLastStep
    LaunchedEffect(isLastStep, succeededSeq) {
        if (isLastStep && succeededSeq > initialSeq) nav.back()
    }
    val subtitle = listOfNotNull(
        uiText(vm.header.title).ifEmpty { null },
        vm.header.agentName.ifEmpty { null },
    ).joinToString(" · ").ifEmpty { null }
    PageScaffold(copy(UiMessages.dockAnswerTitle()), subtitle = subtitle) {
        if (!hasQuestion) {
            PageNote(
                if (vm.timeline.loaded) copy(UiMessages.dockAndroidQuestionAnswered())
                else copy(UiMessages.projectsReadingDirectory()),
            )
            return@PageScaffold
        }
        val toTerminal = {
            nav.back()
            nav.sessionTerminal(sid)
        }
        DarkCard(Modifier.weight(1f, fill = false).padding(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 16.dp)) {
            DarkCardHead(uiText(vm.dock.title), uiText(vm.dock.question.progressText))
            Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState())) {
                QuestionBody(sid, vm, toTerminal, full = true, part = 1)
            }
            QuestionBody(sid, vm, toTerminal, full = true, part = 2)
        }
    }
}

@Composable
internal fun PlanDock(sid: String, vm: SessionViewModel, onOpenChange: (Boolean) -> Unit = {}, onFull: () -> Unit) {
    val copy = uiText()
    val dock = vm.dock
    val plan = dock.plan
    if (dock.isSkeleton && plan.markdown.isEmpty()) return DockSkeleton()
    PendingCoreDock(
        sid,
        dock,
        headExtra = { CardLink(copy(UiMessages.markdownExpand()), onFull) },
        onOpenChange = onOpenChange,
        body = {
            Text(
                plan.markdown.lines().map { plain(it) }.filter { it.isNotBlank() }.joinToString("\n"),
                style = T.s15,
                color = CardColors.text,
                modifier = Modifier.padding(top = 12.dp),
            )
        },
    ) {
        PlanButtons(sid, vm, full = false)
        if (plan.hasNote()) {
            Text(uiText(plan.note), style = T.s13, color = CardColors.text2, modifier = Modifier.padding(top = 8.dp))
        }
    }
}

@Composable
internal fun PlanButtons(
    sid: String,
    vm: SessionViewModel,
    full: Boolean,
    onCard: Boolean = true,
    onDone: () -> Unit = {},
) {
    val copy = uiText()
    val remote = LocalRemote.current
    val dock = vm.dock
    val plan = dock.plan
    val cmd = dock.command
    val busy = cmd.busy || cmd.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING
    val errorText = if (cmd.hasErrorText()) uiText(cmd.errorText) else cmd.errorCode
    val initialSeq = remember(sid, plan.screenHash) { cmd.succeededSeq }
    LaunchedEffect(cmd.succeededSeq) {
        if (cmd.succeededSeq > initialSeq) onDone()
    }

    fun answer(c: PlanChoice) {
        remote.answerPlan(sid, c)
    }

    @Composable
    fun Choice(text: String, onClick: () -> Unit, modifier: Modifier, primary: Boolean = false, enabled: Boolean = true) =
        if (onCard) CardButton(text, onClick, modifier, primary, enabled)
        else PillButton(text, onClick, modifier, if (primary) ButtonKind.INK else ButtonKind.SECONDARY, enabled)

    @Composable
    fun Note(text: String) =
        if (onCard) CardNote(text) else Notice(text, Modifier.padding(top = 10.dp), NoticeTone.ERROR)

    if (cmd.frozen) {
        Note(errorText.ifEmpty { copy(UiMessages.approvalAndroidUnknownResult()) })
        Row(Modifier.fillMaxWidth().padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            if (cmd.canAbandon) Choice(copy(UiMessages.newSessionAndroidAbandon()), { remote.abandonSessionCommand(sid, cmd.slotKey) }, Modifier.weight(1f), enabled = !busy)
            if (cmd.canRetry && cmd.slotKey.isNotEmpty()) {
                Choice(if (busy) copy(UiMessages.dockSubmitting()) else copy(UiMessages.commonRetry()), { remote.retrySessionCommand(sid, cmd.slotKey) }, Modifier.weight(1f), primary = true, enabled = !busy)
            }
        }
        return
    }

    val choices: List<SessionPlanChoiceOptionView> = plan.choicesList
    val primaryRow = choices.filter { it.choice != PlanChoice.PLAN_CHOICE_CLEAR_AND_IMPLEMENT }
    val clearChoice = choices.firstOrNull { it.choice == PlanChoice.PLAN_CHOICE_CLEAR_AND_IMPLEMENT }
    val submittedChoice = plan.submittedChoice

    Row(Modifier.fillMaxWidth().padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        primaryRow.forEach { opt ->
            val primary = opt.choice == PlanChoice.PLAN_CHOICE_IMPLEMENT || opt.recommended
            val label = if (busy && (submittedChoice == opt.choice || (submittedChoice == PlanChoice.PLAN_CHOICE_UNSPECIFIED && primary))) {
                copy(UiMessages.dockSubmitting())
            } else uiText(opt.label)
            Choice(label, { answer(opt.choice) }, Modifier.weight(1f), primary = primary, enabled = !busy)
        }
    }
    if (full && !busy && clearChoice != null) {
        val clear = { answer(clearChoice.choice) }
        val clearLabel = uiText(clearChoice.label).ifEmpty { copy(UiMessages.dockPlanClear()) }
        if (onCard) CardLink(clearLabel, clear, Modifier.fillMaxWidth(), arrow = false)
        else TextLink(clearLabel, clear, Modifier.fillMaxWidth())
    }
    if (errorText.isNotEmpty()) Note(copy(UiMessages.dockSubmitFailed(reason = errorText)))
}

@Composable
private fun PopupDock(sid: String, dock: SessionDockView, onTerminal: () -> Unit, onOpenChange: (Boolean) -> Unit = {}) {
    val copy = uiText()
    val popup = dock.terminalPopup
    PendingCoreDock(
        sid,
        dock,
        onOpenChange = onOpenChange,
        body = {
            if (popup.hasPopupTitle()) {
                Text(uiText(popup.popupTitle), style = T.mono13.copy(fontSize = 15.sp, lineHeight = 22.sp), color = CardColors.text, modifier = Modifier.padding(top = 12.dp))
            }
            if (popup.hasHint()) {
                Text(uiText(popup.hint), style = T.s13, color = CardColors.orange, modifier = Modifier.padding(top = 8.dp))
            }
        },
    ) {
        if (popup.canGoTerminal) {
            CardButton(copy(UiMessages.dockHandleTerminal()), onTerminal, Modifier.fillMaxWidth().padding(top = 16.dp), primary = true)
        }
    }
}
