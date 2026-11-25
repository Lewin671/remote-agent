package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.isCtrlPressed
import androidx.compose.ui.input.key.isMetaPressed
import androidx.compose.ui.input.key.isShiftPressed
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.*
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.*
import io.github.lewin671.remoteagent.core.proto.AlwaysAllowKind
import io.github.lewin671.remoteagent.core.proto.AttentionCardView
import io.github.lewin671.remoteagent.core.proto.HomeFocusPhase
import io.github.lewin671.remoteagent.core.proto.HomeQuestionCardView
import io.github.lewin671.remoteagent.core.proto.SessionCommandState
import io.github.lewin671.remoteagent.core.proto.SessionCommandStatus
import kotlinx.coroutines.delay

/*
 * The dark card (design-system §6 终端卡): what waits for the user, dark in
 * both themes. The same shell carries an approval on the home page, and,
 * once the session is redrawn, every item docked above the composer. The
 * pieces here are that shell, its head, the black command block, its
 * buttons and field, and the one line a submitted card shrinks to;
 * [ApprovalCard] puts them together for the home page.
 */

/** Whole lines of a command the home's card shows before 看完整命令. */
const val CARD_LINES = 5

/** The shell: tc, radius 24, the thin edge in the dark theme; [float] when it floats over the page (docked). */
@Composable
fun DarkCard(modifier: Modifier = Modifier, float: Boolean = false, content: @Composable ColumnScope.() -> Unit) {
    val p = pal
    Column(
        modifier.fillMaxWidth().floating(p, Radii.R24, float).clip(Radii.R24).background(p.tc).border(0.5.dp, p.tcLine, Radii.R24).padding(18.dp),
        content = content,
    )
}

/** The card's head: the orange dot, what kind of thing waits (13 / 600), and at the right [right], orange when [hot]. */
@Composable
fun DarkCardHead(kind: String, right: String = "", hot: Boolean = false, trailing: (@Composable () -> Unit)? = null) {
    val kindRow: @Composable (Modifier) -> Unit = { m ->
        Row(m, verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.size(8.dp).clip(CircleShape).background(CardColors.orange))
            Spacer(Modifier.width(7.dp))
            Text(kind, style = T.s13b, color = CardColors.orange)
        }
    }
    val rightText: @Composable () -> Unit = {
        if (right.isNotEmpty()) Text(right, style = T.s13, color = if (hot) CardColors.orange else CardColors.text2, textAlign = TextAlign.End)
    }
    // Keep the compact row when its actual contents fit. Otherwise give the
    // title a full row; metadata and actions remain together below it.
    val largeType = LocalDensity.current.fontScale >= 1.5f
    androidx.compose.ui.layout.Layout(
        content = {
            kindRow(Modifier)
            FlowRow(horizontalArrangement = Arrangement.End, itemVerticalAlignment = Alignment.CenterVertically) {
                rightText()
                trailing?.invoke()
            }
        },
        modifier = Modifier.fillMaxWidth(),
    ) { measurables, constraints ->
        val loose = constraints.copy(minWidth = 0, minHeight = 0)
        val kind = measurables[0].measure(loose)
        val details = measurables[1].measure(loose)
        val gap = 8.dp.roundToPx()
        val stacked = largeType || kind.width + gap + details.width > constraints.maxWidth
        val rowGap = if (stacked && !largeType && details.height > 0) 4.dp.roundToPx() else 0
        val height = if (stacked) kind.height + rowGap + details.height else maxOf(kind.height, details.height)
        layout(constraints.maxWidth, constraints.constrainHeight(height)) {
            kind.placeRelative(0, if (stacked) 0 else (height - kind.height + 1) / 2)
            details.placeRelative(constraints.maxWidth - details.width,
                if (stacked) kind.height + rowGap else (height - details.height + 1) / 2)
        }
    }
}

/**
 * The command, black in both themes, mono 13 after an orange ❯. It wraps
 * anywhere and never scrolls on its own. With [maxLines] it shows that many
 * whole lines; [onLines] hears how many it takes at full length, as laid out
 * at this width.
 */
@Composable
fun CommandBlock(text: String, modifier: Modifier = Modifier, maxLines: Int = Int.MAX_VALUE, onLines: (Int) -> Unit = {}) {
    val shown: AnnotatedString = remember(text) {
        buildAnnotatedString {
            withStyle(SpanStyle(color = CardColors.orange)) { append("❯ ") }
            append(text)
        }
    }
    val measurer = rememberTextMeasurer()
    var width by remember { mutableIntStateOf(0) }
    if (width > 0) {
        val lines = remember(shown, width) { measurer.measure(shown, T.mono13, constraints = Constraints(maxWidth = width)).lineCount }
        onLines(lines)
    }
    Box(
        modifier.fillMaxWidth().clip(Radii.R12).background(CardColors.cmd).padding(horizontal = 14.dp, vertical = 12.dp),
    ) {
        Text(
            shown, style = T.mono13, color = CardColors.cmdText, maxLines = maxLines, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.fillMaxWidth().onSizeChanged { width = it.width }.testTag("command"),
        )
    }
}

/** A button on the dark card: white with black text for the one thing to do, half-clear white otherwise; 48, 17 / 600. */
@Composable
fun CardButton(text: String, onClick: () -> Unit, modifier: Modifier = Modifier, primary: Boolean = false, enabled: Boolean = true) {
    Box(
        modifier.heightIn(min = 48.dp).disabledAlpha(enabled).clip(Radii.Pill).background(if (primary) CardColors.primary else CardColors.secondary)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick).padding(horizontal = 16.dp, vertical = 6.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(text, style = T.s17b, color = if (primary) CardColors.onPrimary else CardColors.text, textAlign = TextAlign.Center, maxLines = 2)
    }
}

/** A field on the dark card, radius 12, 17 text; the caret is orange. */
@Composable
fun CardField(
    value: String,
    onValue: (String) -> Unit,
    placeholder: String,
    modifier: Modifier = Modifier,
    label: String = placeholder,
    readOnly: Boolean = false,
    onSubmit: (() -> Unit)? = null,
    onCancel: (() -> Unit)? = null,
) {
    BasicTextField(
        value, onValue,
        modifier.fillMaxWidth()
            .onPreviewKeyEvent { ev ->
                if (ev.type != KeyEventType.KeyDown) return@onPreviewKeyEvent false
                when {
                    ev.key == Key.Escape && onCancel != null -> {
                        onCancel()
                        true
                    }
                    ev.key == Key.Enter && (!ev.isShiftPressed || ev.isCtrlPressed || ev.isMetaPressed) && onSubmit != null -> {
                        onSubmit()
                        true
                    }
                    else -> false
                }
            }
            .semantics { contentDescription = label },
        readOnly = readOnly,
        textStyle = T.s17.copy(color = if (readOnly) CardColors.text2 else CardColors.text),
        cursorBrush = SolidColor(CardColors.orange),
        maxLines = 4,
        decorationBox = { inner ->
            Box(Modifier.fillMaxWidth().heightIn(min = 48.dp).clip(Radii.R12).background(CardColors.field).padding(horizontal = 14.dp, vertical = 13.dp)) {
                if (value.isEmpty()) Text(placeholder, style = T.s17, color = CardColors.text2.copy(alpha = .5f))
                inner()
            }
        },
    )
}

/**
 * A card shrunk to one line once it was answered (「已允许」「已拒绝」), or,
 * when the answer did not reach the computer, why, in red, and 重试.
 */
@Composable
fun DarkCardLine(kind: String, status: String, modifier: Modifier = Modifier, error: Boolean = false, retry: (() -> Unit)? = null) {
    val copy = uiText()
    val p = pal
    Row(
        modifier.fillMaxWidth().clip(Radii.R24).background(p.tc).border(0.5.dp, p.tcLine, Radii.R24).heightIn(min = 48.dp)
            .padding(start = 16.dp, end = if (retry != null) 6.dp else 16.dp, top = 6.dp, bottom = 6.dp)
            .semantics(mergeDescendants = true) {},
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.size(8.dp).clip(CircleShape).background(CardColors.orange))
        Spacer(Modifier.width(7.dp))
        Text(kind, style = T.s13b, color = CardColors.orange, maxLines = 1)
        Spacer(Modifier.width(8.dp))
        Text(status, style = T.s13, color = if (error) CardColors.red else CardColors.text2, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        if (retry != null) Box(Modifier.heightIn(min = 44.dp).clickable(role = Role.Button, onClick = retry).padding(horizontal = 10.dp), contentAlignment = Alignment.Center) {
            Text(copy(UiMessages.commonRetry()), style = T.s13b, color = CardColors.link)
        }
    }
}

/**
 * Core-backed approval reply state on [SessionCommandState] for Home, Dock and ApprovalPage.
 */
class ApprovalReply internal constructor(
    val cmdState: SessionCommandState?,
    private val send: (allow: Boolean, message: String, always: Boolean) -> Unit,
    val retryAction: (() -> Unit)? = null,
    val abandonAction: (() -> Unit)? = null,
) {
    val running: Boolean get() = cmdState?.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING || cmdState?.busy == true
    val failed: Boolean get() = cmdState != null && !running && (
        cmdState.status == SessionCommandStatus.SESSION_COMMAND_STATUS_FAILED ||
            cmdState.status == SessionCommandStatus.SESSION_COMMAND_STATUS_UNKNOWN ||
            cmdState.frozen
        )
    val submitted: Boolean get() = running || failed || cmdState?.frozen == true

    fun resolve(allow: Boolean, message: String = "", always: Boolean = false) = send(allow, message, always)
}

@Composable
fun rememberApprovalReply(
    sessionId: String,
    approvalId: String,
    cmdState: SessionCommandState?,
    isHome: Boolean = false,
    skeleton: Boolean = false,
): ApprovalReply {
    val remote = LocalRemote.current
    val view = androidx.compose.ui.platform.LocalView.current
    val slotKey = cmdState?.slotKey.orEmpty()
    val retry: (() -> Unit)? = when {
        isHome -> ({ remote.homeRetryAttention(sessionId, approvalId) })
        slotKey.isNotEmpty() -> ({ remote.retrySessionCommand(sessionId, slotKey) })
        else -> null
    }
    val abandon: (() -> Unit)? = when {
        isHome -> ({ remote.homeAbandonAttention(sessionId, approvalId) })
        slotKey.isNotEmpty() -> ({ remote.abandonSessionCommand(sessionId, slotKey) })
        else -> null
    }
    return ApprovalReply(
        cmdState = cmdState,
        send = { allow, message, always ->
            if (skeleton) return@ApprovalReply
            Motion.tap(view)
            if (isHome) remote.homeResolveApproval(sessionId, approvalId, allow, always, message)
            else remote.resolveApproval(sessionId, approvalId, allow, always, message)
        },
        retryAction = retry,
        abandonAction = abandon,
    )
}

/**
 * The one line a submitted approval shrinks to: 已允许 / 已拒绝, or why in red with 重试.
 */
@Composable
fun ApprovalSubmitted(kind: String, d: ApprovalReply, modifier: Modifier = Modifier) {
    val copy = uiText()
    val st = d.cmdState
    when {
        !d.failed -> {
            val summary = st?.takeIf { it.hasSubmittedSummary() }?.let { uiText(it.submittedSummary) }
                ?.ifEmpty { null }
                ?: copy(UiMessages.dockWaitingConfirmation())
            DarkCardLine(kind, summary, modifier)
        }
        st?.expired == true -> {
            val status = st.takeIf { it.hasStatusText() }?.let { uiText(it.statusText) }
                ?.ifEmpty { null }
                ?: copy(UiMessages.approvalAndroidReplyExpired(decision = copy(UiMessages.approvalAllow())))
            DarkCardLine(kind, status, modifier, error = true)
        }
        else -> {
            val reason = st?.takeIf { it.hasErrorText() }?.let { uiText(it.errorText) }
                ?.ifEmpty { st.errorCode }
                ?.ifEmpty { copy(UiMessages.approvalAndroidUnknownResult()) }
                ?: copy(UiMessages.approvalAndroidUnknownResult())
            val retry: (() -> Unit)? = if (st?.canRetry == true) d.retryAction else null
            DarkCardLine(kind, copy(UiMessages.approvalDeliveryFailed(reason = reason)), modifier, error = true, retry = retry)
        }
    }
}

/**
 * An approval's buttons on the dark card (home card, dock, and approval page).
 */
@Composable
fun ApprovalButtons(
    approvalId: String,
    d: ApprovalReply,
    denying: Boolean,
    onDenying: (Boolean) -> Unit,
    locked: Boolean,
    truncated: Boolean,
    always: Boolean,
    alwaysLabel: String,
    denyPlaceholder: String = uiText(UiMessages.approvalReasonPlaceholder()),
    cut: Boolean = false,
    onFull: () -> Unit = {},
    scrollToBottomNeeded: Boolean = false,
    onScrollToBottom: () -> Unit = {},
) {
    val copy = uiText()
    var reason by Drafts.of("deny:$approvalId")
    val sendingDeny = denying && d.running
    val failedDeny = denying && d.failed
    val submitDeny = {
        d.resolve(false, reason.trim())
    }
    if (denying) {
        CardField(
            reason,
            { if (!sendingDeny) reason = it },
            denyPlaceholder,
            Modifier.padding(top = 12.dp),
            label = copy(UiMessages.approvalDenialReason()),
            readOnly = sendingDeny,
            onSubmit = { if (!locked && !sendingDeny) submitDeny() },
            onCancel = { if (!sendingDeny) onDenying(false) },
        )
        Row(Modifier.fillMaxWidth().padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            CardButton(copy(UiMessages.commonCancel()), { onDenying(false) }, Modifier.weight(1f), enabled = !sendingDeny)
            val denyBtnLabel = when {
                sendingDeny -> copy(UiMessages.approvalDenying())
                failedDeny -> copy(UiMessages.approvalRetryDeny())
                else -> copy(UiMessages.approvalDeny())
            }
            CardButton(denyBtnLabel, submitDeny, Modifier.weight(1f), primary = true, enabled = !locked && !sendingDeny)
        }
        if (failedDeny) {
            val st = d.cmdState
            val errReason = st?.takeIf { it.hasErrorText() }?.let { uiText(it.errorText) }
                ?.ifEmpty { st.errorCode }
                ?.ifEmpty { copy(UiMessages.approvalAndroidUnknownResult()) }
                ?: copy(UiMessages.approvalAndroidUnknownResult())
            CardNote(copy(UiMessages.approvalDeliveryFailed(reason = errReason)))
        }
        return
    }
    Row(Modifier.fillMaxWidth().padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        CardButton(copy(UiMessages.approvalDeny()), { onDenying(true) }, Modifier.weight(1f), enabled = !locked)
        if (cut) CardButton(copy(UiMessages.approvalFullCommand()), onFull, Modifier.weight(1f), primary = true)
        else if (scrollToBottomNeeded) CardButton(copy(UiMessages.approvalScrollToView()), onScrollToBottom, Modifier.weight(1f), primary = true)
        else CardButton(copy(UiMessages.approvalAllow()), { d.resolve(true) }, Modifier.weight(1f), primary = true, enabled = !locked && !truncated)
    }
    if (always && !cut && !scrollToBottomNeeded && !locked && !truncated) Box(
        Modifier.fillMaxWidth().heightIn(min = 44.dp).padding(top = 4.dp).clickable(role = Role.Button) { d.resolve(true, always = true) },
        contentAlignment = Alignment.Center,
    ) { Text(alwaysLabel, style = T.s15, color = CardColors.link) }
}

@Composable
private fun HomeResolvedLine(text: String, checked: Boolean, modifier: Modifier = Modifier) {
    Row(
        modifier.fillMaxWidth().clip(Radii.R24).background(pal.tc).border(0.5.dp, pal.tcLine, Radii.R24).heightIn(min = 48.dp)
            .padding(horizontal = 16.dp, vertical = 10.dp).semantics(mergeDescendants = true) {
                liveRegion = LiveRegionMode.Polite
            },
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        if (checked) Ic(Lucide.Check, 15.dp, pal.orangeInk)
        Text(text, style = T.s13, color = pal.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
    }
}

/**
 * The home page's dark card for the most urgent approval (design-system §6 终端卡), rendered purely from
 * Core's [AttentionCardView] without any hidden session subscription.
 */
@Composable
fun ApprovalCard(
    card: AttentionCardView,
    locked: Boolean = false,
    onOpen: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val copy = uiText()
    val remote = LocalRemote.current
    if (card.phase == HomeFocusPhase.HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT ||
        card.phase == HomeFocusPhase.HOME_FOCUS_PHASE_RESOLVED_ELSEWHERE
    ) {
        HomeResolvedLine(
            text = uiText(card.resolutionText),
            checked = card.phase == HomeFocusPhase.HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT,
            modifier = modifier,
        )
        return
    }
    val kind = uiText(card.kindText)
    if (card.isSkeleton) {
        DarkCard(modifier) {
            DarkCardHead(kind)
            Text(copy(UiMessages.sessionSkeleton()), style = T.s15, color = CardColors.text2, modifier = Modifier.padding(top = 12.dp))
        }
        return
    }
    val time = uiText(card.time)
    val cmdState = if (card.hasCommand()) card.command else null
    val d = rememberApprovalReply(card.sessionId, card.approvalId, cmdState, isHome = true, skeleton = card.isSkeleton)
    var full by rememberSaveable(card.approvalId) { mutableStateOf(false) }
    var lines by remember(card.approvalId) { mutableIntStateOf(0) }
    val denying = card.denyingOpen
    val showCompactSubmitted = d.submitted && !card.holdOpenCard
    if (showCompactSubmitted) return ApprovalSubmitted(kind, d, modifier)
    val command = card.body.trim()
    val more = lines - CARD_LINES
    val cut = command.isNotEmpty() && more > 0 && !full
    val always = card.alwaysKind != AlwaysAllowKind.ALWAYS_ALLOW_KIND_UNSPECIFIED
    DarkCard(modifier.semantics(mergeDescendants = false) {}) {
        DarkCardHead(kind, time, card.hot)
        Row(
            Modifier.padding(top = 12.dp).heightIn(min = 32.dp)
                .clickable(onClickLabel = copy(UiMessages.homeIosOpenSession()), role = Role.Button, onClick = onOpen)
                .semantics { heading() },
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(uiText(card.title), style = T.h20, color = CardColors.text, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
            Spacer(Modifier.width(2.dp))
            Ic(Lucide.ChevronRight, 20.dp, CardColors.text.copy(alpha = .5f))
        }
        Text(uiText(card.where), style = T.s13, color = CardColors.text2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 2.dp))
        if (command.isNotEmpty()) CommandBlock(
            command, Modifier.padding(top = 14.dp),
            maxLines = if (cut) CARD_LINES else Int.MAX_VALUE,
            onLines = { lines = it },
        )
        if (cut) Text(copy(UiMessages.markdownIosHiddenLines(count = more)), style = T.s13, color = CardColors.text2, modifier = Modifier.padding(top = 8.dp))
        else if (full && more > 0) Row(verticalAlignment = Alignment.CenterVertically) {
            Text(copy(UiMessages.markdownLineCount(count = lines)), style = T.s13, color = CardColors.text2)
            Box(Modifier.heightIn(min = 36.dp).clickable(role = Role.Button) { full = false }.padding(horizontal = 6.dp), contentAlignment = Alignment.Center) {
                Text(copy(UiMessages.markdownCollapse()), style = T.s13, color = CardColors.link)
            }
        }
        if (card.truncated) Text(copy(UiMessages.approvalTruncated()), style = T.s13, color = CardColors.red, modifier = Modifier.padding(top = 8.dp))
        if (!denying && card.description.isNotEmpty()) {
            Text(card.description.trim(), style = T.s15, color = CardColors.text2, modifier = Modifier.padding(top = 12.dp))
        }
        ApprovalButtons(
            approvalId = card.approvalId,
            d = d,
            denying = denying,
            onDenying = { remote.homeEditApprovalDeny(card.sessionId, card.approvalId, it) },
            locked = locked,
            truncated = card.truncated,
            always = always,
            alwaysLabel = uiText(card.alwaysLabel).ifEmpty { copy(UiMessages.approvalAlwaysAllow()) },
            denyPlaceholder = if (card.hasDenyReasonPlaceholder()) uiText(card.denyReasonPlaceholder) else copy(UiMessages.approvalReasonPlaceholder()),
            cut = cut,
            onFull = { full = true },
        )
    }
}

/**
 * Renders Home's inline single-select question dark focus card purely from Core's [HomeQuestionCardView].
 * Keeps only a 150ms pure visual spinner delay while SUBMITTING.
 */
@Composable
fun HomeQuestionSlot(
    question: HomeQuestionCardView?,
    onOpenSession: (String) -> Unit,
    onOpenTerminal: (String) -> Unit,
    modifier: Modifier = Modifier,
) {
    val active = question ?: return
    val copy = uiText()
    val remote = LocalRemote.current
    val view = androidx.compose.ui.platform.LocalView.current
    val phase = active.phase

    if (phase == HomeFocusPhase.HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT ||
        phase == HomeFocusPhase.HOME_FOCUS_PHASE_RESOLVED_ELSEWHERE
    ) {
        HomeResolvedLine(
            text = uiText(active.resolutionText),
            checked = phase == HomeFocusPhase.HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT,
            modifier = modifier,
        )
        return
    }

    val sending = phase == HomeFocusPhase.HOME_FOCUS_PHASE_SUBMITTING
    var showSpinner by remember(active.sessionId, active.questionId, active.pickedOptionNum) { mutableStateOf(false) }
    LaunchedEffect(sending, active.sessionId, active.questionId, active.pickedOptionNum) {
        if (sending) {
            showSpinner = false
            delay(150)
            showSpinner = true
        } else {
            showSpinner = false
        }
    }

    val errMsg = if (phase == HomeFocusPhase.HOME_FOCUS_PHASE_FAILED) {
        val cmd = if (active.hasCommand()) active.command else null
        cmd?.takeIf { it.hasErrorText() }?.let { uiText(it.errorText) }
            ?.ifEmpty { cmd.errorCode }
            ?.ifEmpty { copy(UiMessages.approvalAndroidUnknownResult()) }
            ?: copy(UiMessages.approvalAndroidUnknownResult())
    } else ""

    DarkCard(modifier.semantics(mergeDescendants = false) {}) {
        DarkCardHead(uiText(active.badgeText), uiText(active.timeText))
        Row(
            Modifier.padding(top = 12.dp).heightIn(min = 32.dp)
                .clickable(onClickLabel = copy(UiMessages.homeIosOpenSession()), role = Role.Button) { onOpenSession(active.sessionId) }
                .semantics { heading() },
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(uiText(active.title), style = T.h20, color = CardColors.text, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
            Spacer(Modifier.width(2.dp))
            Ic(Lucide.ChevronRight, 20.dp, CardColors.text.copy(alpha = .5f))
        }
        val where = uiText(active.where)
        if (where.isNotEmpty()) {
            Text(where, style = T.s13, color = CardColors.text2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 2.dp))
        }
        if (active.header.isNotEmpty()) {
            Text(active.header, style = T.s12b, color = CardColors.text2, modifier = Modifier.padding(top = 12.dp))
        }
        Text(
            active.questionText,
            style = T.s17,
            color = CardColors.text,
            modifier = Modifier.padding(top = if (active.header.isNotEmpty()) 4.dp else 12.dp),
        )
        Column(Modifier.fillMaxWidth().padding(top = 14.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            val recWord = copy(UiMessages.dockRecommended())
            active.optionsList.forEach { opt ->
                val isPicked = active.pickedOptionNum == opt.optionNum && sending
                val dimmed = sending && !isPicked
                val enabled = active.canAnswer && !sending
                Row(
                    Modifier.fillMaxWidth().heightIn(min = 44.dp)
                        .disabledAlpha(!dimmed)
                        .clip(Radii.R12)
                        .background(if (isPicked) CardColors.primary else Color(0x14FFFFFF))
                        .clickable(enabled = enabled, role = Role.Button) {
                            Motion.tap(view)
                            remote.homeAnswerQuestion(active.sessionId, active.questionId, opt.optionNum)
                        }
                        .padding(horizontal = 14.dp, vertical = 10.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    Column(Modifier.weight(1f)) {
                        val title = buildAnnotatedString {
                            append(opt.label)
                            if (opt.recommended && recWord.isNotEmpty()) {
                                withStyle(SpanStyle(color = if (isPicked) CardColors.onPrimary.copy(alpha = .62f) else CardColors.text2, fontWeight = T.s13.fontWeight)) {
                                    append(" · $recWord")
                                }
                            }
                        }
                        Text(title, style = T.s15b, color = if (isPicked) CardColors.onPrimary else CardColors.text)
                        if (opt.description.isNotEmpty()) {
                            Text(
                                opt.description,
                                style = T.s13,
                                color = if (isPicked) CardColors.onPrimary.copy(alpha = .62f) else CardColors.text2,
                                modifier = Modifier.padding(top = 2.dp),
                            )
                        }
                    }
                    if (isPicked && showSpinner) {
                        Spinner(CardColors.onPrimary, size = 16.dp)
                    }
                }
            }
        }
        if (phase == HomeFocusPhase.HOME_FOCUS_PHASE_FAILED && errMsg.isNotEmpty()) {
            CardNote(
                copy(UiMessages.homeQuestionSendFailed(reason = errMsg)),
                action = if (active.command.canRetry || active.pickedOptionNum > 0) copy(UiMessages.commonRetry()) else null,
                onAction = {
                    Motion.tap(view)
                    remote.homeRetryAttention(active.sessionId, active.questionId)
                },
            )
        }
        if (!active.canAnswer && active.hasOfflineNote()) {
            Text(uiText(active.offlineNote), style = T.s13, color = CardColors.text2, modifier = Modifier.padding(top = 8.dp))
        }
        Row(Modifier.fillMaxWidth().padding(top = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(copy(UiMessages.dockNoneFit()), style = T.s13, color = CardColors.text2)
            CardLink(copy(UiMessages.homeQuestionAnswerInTerminal()), { onOpenTerminal(active.sessionId) })
        }
    }
}
