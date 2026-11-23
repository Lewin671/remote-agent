package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.SessionPendingTrayView
import io.github.lewin671.remoteagent.core.proto.SessionTrayItemView
import io.github.lewin671.remoteagent.core.proto.SessionTrayRowActivity
import io.github.lewin671.remoteagent.core.proto.SessionTrayTone

private fun trayToneColor(tone: SessionTrayTone, p: Palette): Color = when (tone) {
    SessionTrayTone.SESSION_TRAY_TONE_RED -> p.red
    SessionTrayTone.SESSION_TRAY_TONE_ORANGE -> p.orangeInk
    else -> p.ink2
}

@Composable
private fun trayRowBodyText(it: SessionTrayItemView): String {
    if (it.isSkeleton) return uiText(UiMessages.sessionSkeleton())
    if (it.text.isNotEmpty()) return it.text
    if (it.foreignDimmed) return uiText(UiMessages.queueOtherMessage())
    if (it.imageCount > 0) return uiText(UiMessages.imageCount(count = it.imageCount))
    if (it.fileCount > 0) return ""
    return uiText(UiMessages.queueOtherMessage())
}

@Composable
internal fun PendingTray(
    tray: SessionPendingTrayView,
    keyboardCollapsed: Boolean = false,
    onExpandKeyboard: () -> Unit = {},
    onTerminal: (() -> Unit)? = null,
    onCancelItem: (String) -> Unit = {},
    onIgnoreItem: (String) -> Unit = {},
    onPromoteItem: (String) -> Unit = {},
    onRestoreItem: (String) -> Unit = {},
    onRetryItem: (String) -> Unit = {},
    onResumeItem: (String) -> Unit = {},
    onCancelAllPaused: () -> Unit = {},
    onUndo: () -> Unit = {},
    defaultExpandedAll: Boolean = false,
    modifier: Modifier = Modifier,
) {
    val copy = uiText()
    val p = pal
    val totalCount = tray.totalCount
    val undoVisible = tray.hasUndo() && tray.undo.visible
    if (totalCount == 0 && !undoVisible) return

    var expandedAll by remember(defaultExpandedAll) { mutableStateOf(defaultExpandedAll) }
    var openId by remember { mutableStateOf<String?>(null) }
    var kbExpanded by remember { mutableStateOf(false) }

    LaunchedEffect(keyboardCollapsed, totalCount) {
        if (!keyboardCollapsed || totalCount == 0) kbExpanded = false
    }
    LaunchedEffect(openId, tray.orderedItemsList) {
        if (openId != null && tray.orderedItemsList.none { it.requestId == openId }) {
            openId = null
        }
    }

    val halfScreen = (LocalConfiguration.current.screenHeightDp * 0.5f).coerceAtLeast(180f).dp
    Column(
        modifier
            .fillMaxWidth()
            .heightIn(max = halfScreen)
            .verticalScroll(rememberScrollState())
            .padding(vertical = 4.dp),
    ) {
        if (keyboardCollapsed && !kbExpanded && totalCount > 0) {
            Row(
                Modifier
                    .fillMaxWidth()
                    .heightIn(min = 36.dp)
                    .clickable(role = Role.Button) {
                        kbExpanded = true
                        onExpandKeyboard()
                    }
                    .padding(start = 16.dp, end = 14.dp, top = 6.dp, bottom = 6.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.SpaceBetween,
            ) {
                Text(
                    buildAnnotatedString {
                        withStyle(SpanStyle(color = p.ink2)) {
                            append(copy(UiMessages.queueKeyboardSummary(count = totalCount)))
                        }
                        if (tray.failedCount > 0) {
                            withStyle(SpanStyle(color = p.red)) {
                                append(" · ")
                                append(copy(UiMessages.queueKeyboardFailed(count = tray.failedCount)))
                            }
                        }
                    },
                    style = T.s13,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f),
                )
                Ic(Lucide.ChevronUp, 15.dp, p.ink2)
            }
            Box(Modifier.fillMaxWidth().height(0.5.dp).background(p.sep))
            return@Column
        }

        val ordered = tray.orderedItemsList
        val limit = if (ordered.size > 3 && !expandedAll) 2 else ordered.size
        val visibleIds = ordered.take(limit).map { it.requestId }.toSet()

        for (group in tray.groupsList) {
            val visibleItems = group.itemsList.filter { it.requestId in visibleIds }
            if (visibleItems.isEmpty()) continue
            val gColor = trayToneColor(group.tone, p)
            Row(
                Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 2.dp),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    uiText(group.headerText),
                    style = T.s12.copy(fontWeight = platformWeight(FontWeight.Medium), fontSize = 11.5.sp),
                    color = gColor,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                if (group.canCancelAll) {
                    Text(
                        copy(UiMessages.queueCancelAll()),
                        style = T.s12,
                        color = p.blue,
                        modifier = Modifier
                            .alpha(if (tray.offline) 0.35f else 1f)
                            .clickable(enabled = !tray.offline, role = Role.Button) { onCancelAllPaused() }
                            .padding(horizontal = 4.dp, vertical = 2.dp),
                    )
                }
            }

            for (it in visibleItems) {
                val id = it.requestId
                val isOpen = openId == id
                val reason = if (it.hasReasonText()) uiText(it.reasonText) else ""
                val src = if (it.hasSourceLabel()) uiText(it.sourceLabel) else ""
                val rColor = trayToneColor(it.tone, p)
                val body = trayRowBodyText(it)
                val hasExpandOpts = isOpen && it.activity == SessionTrayRowActivity.SESSION_TRAY_ROW_ACTIVITY_IDLE && (it.canPromote || it.canRestore)
                val bodyStyle = T.s15.copy(fontSize = 14.5.sp, lineHeight = 19.6.sp)

                Row(
                    Modifier
                        .fillMaxWidth()
                        .background(if (isOpen) p.fill else Color.Transparent)
                        .animateContentSize(Motion.enter(Motion.BASE))
                        .heightIn(min = 38.dp)
                        .padding(start = 14.dp, end = 4.dp, top = if (isOpen) 8.dp else if (reason.isNotEmpty()) 6.dp else 0.dp),
                    verticalAlignment = if (isOpen) Alignment.Top else Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    Box(
                        Modifier.width(18.dp).padding(top = if (isOpen) 3.dp else 0.dp),
                        contentAlignment = Alignment.Center,
                    ) {
                        when {
                            it.showSpinner -> Spinner(p.ink2, size = 13.dp)
                            it.queueNumber > 0 -> Text(
                                it.queueNumber.toString(),
                                style = T.s12.copy(fontFeatureSettings = "tnum"),
                                color = p.ink2,
                                textAlign = TextAlign.Center,
                            )
                        }
                    }

                    Column(
                        Modifier
                            .weight(1f)
                            .clickable(role = Role.Button) {
                                openId = if (openId == id) null else id
                            }
                            .padding(
                                top = if (isOpen) 0.dp else 8.dp,
                                bottom = if (isOpen || reason.isNotEmpty() || hasExpandOpts) 4.dp else 8.dp,
                            ),
                    ) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            if (src.isNotEmpty() || body.isNotEmpty()) {
                                Text(
                                    buildAnnotatedString {
                                        if (src.isNotEmpty()) {
                                            withStyle(SpanStyle(color = p.ink2)) { append("$src · ") }
                                        }
                                        withStyle(SpanStyle(color = if (it.foreignDimmed) p.ink2 else p.ink)) {
                                            append(body)
                                        }
                                    },
                                    style = bodyStyle,
                                    maxLines = if (isOpen) 6 else 1,
                                    overflow = TextOverflow.Ellipsis,
                                    modifier = Modifier.weight(1f, fill = false),
                                )
                            }
                            if (it.attachmentCount > 0) {
                                val a11y = copy(UiMessages.queueAttachmentsA11Y(count = it.attachmentCount))
                                Row(
                                    Modifier
                                        .padding(start = if (src.isNotEmpty() || body.isNotEmpty()) 6.dp else 0.dp)
                                        .semantics(mergeDescendants = true) { contentDescription = a11y },
                                    verticalAlignment = Alignment.CenterVertically,
                                    horizontalArrangement = Arrangement.spacedBy(2.dp),
                                ) {
                                    Ic(Lucide.Paperclip, 12.dp, p.ink2)
                                    Text(it.attachmentCount.toString(), style = T.s12, color = p.ink2)
                                }
                            }
                        }

                        if (reason.isNotEmpty()) {
                            Text(
                                reason,
                                style = T.s12.copy(lineHeight = 15.6.sp),
                                color = rColor,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                                modifier = Modifier.padding(top = 2.dp, bottom = 2.dp),
                            )
                        }

                        if (hasExpandOpts) {
                            Row(
                                Modifier.padding(top = 6.dp, bottom = 6.dp),
                                horizontalArrangement = Arrangement.spacedBy(18.dp),
                                verticalAlignment = Alignment.CenterVertically,
                            ) {
                                if (it.canPromote) {
                                    val enabled = !it.networkActionsDisabled
                                    Text(
                                        copy(UiMessages.queueSendNow()),
                                        style = T.s13.copy(fontSize = 13.5.sp),
                                        color = p.blue,
                                        modifier = Modifier
                                            .alpha(if (enabled) 1f else 0.35f)
                                            .clickable(enabled = enabled, role = Role.Button) {
                                                openId = null
                                                onPromoteItem(id)
                                            }
                                            .padding(vertical = 4.dp),
                                    )
                                }
                                if (it.canRestore) {
                                    val enabled = !(it.serverBacked && it.networkActionsDisabled)
                                    Text(
                                        copy(UiMessages.composerRestore()),
                                        style = T.s13.copy(fontSize = 13.5.sp),
                                        color = p.blue,
                                        modifier = Modifier
                                            .alpha(if (enabled) 1f else 0.35f)
                                            .clickable(enabled = enabled, role = Role.Button) {
                                                openId = null
                                                onRestoreItem(id)
                                            }
                                            .padding(vertical = 4.dp),
                                    )
                                }
                            }
                        }
                    }

                    Row(verticalAlignment = Alignment.CenterVertically) {
                        when (it.activity) {
                            SessionTrayRowActivity.SESSION_TRAY_ROW_ACTIVITY_CANCELING -> {
                                Text(
                                    copy(UiMessages.queueCanceling()),
                                    style = T.s12.copy(fontSize = 12.5.sp),
                                    color = p.ink2,
                                    maxLines = 1,
                                    modifier = Modifier.padding(start = 4.dp, end = 10.dp),
                                )
                            }
                            SessionTrayRowActivity.SESSION_TRAY_ROW_ACTIVITY_SENDING -> {
                                Text(
                                    copy(UiMessages.queueResuming()),
                                    style = T.s12.copy(fontSize = 12.5.sp),
                                    color = p.ink2,
                                    maxLines = 1,
                                    modifier = Modifier.padding(start = 4.dp, end = 10.dp),
                                )
                            }
                            else -> {
                                if (it.canIgnore) {
                                    if (onTerminal != null) {
                                        val termEnabled = !it.networkActionsDisabled
                                        Text(
                                            copy(UiMessages.sessionGoTerminal()),
                                            style = T.s13.copy(fontSize = 13.5.sp),
                                            color = p.blue,
                                            modifier = Modifier
                                                .alpha(if (termEnabled) 1f else 0.35f)
                                                .clickable(enabled = termEnabled, role = Role.Button) { onTerminal() }
                                                .padding(horizontal = 6.dp, vertical = 8.dp),
                                        )
                                    }
                                    Text(
                                        copy(UiMessages.queueIgnore()),
                                        style = T.s13.copy(fontSize = 13.5.sp),
                                        color = p.blue,
                                        modifier = Modifier
                                            .clickable(role = Role.Button) { onIgnoreItem(id) }
                                            .padding(horizontal = 6.dp, vertical = 8.dp),
                                    )
                                } else {
                                    if (it.canResume) {
                                        val enabled = !it.networkActionsDisabled
                                        Text(
                                            copy(UiMessages.composerSend()),
                                            style = T.s13.copy(fontSize = 13.5.sp),
                                            color = p.blue,
                                            modifier = Modifier
                                                .alpha(if (enabled) 1f else 0.35f)
                                                .clickable(enabled = enabled, role = Role.Button) { onResumeItem(id) }
                                                .padding(horizontal = 6.dp, vertical = 8.dp),
                                        )
                                    }
                                    if (it.canRetry) {
                                        val enabled = !it.networkActionsDisabled
                                        Text(
                                            copy(UiMessages.commonRetry()),
                                            style = T.s13.copy(fontSize = 13.5.sp),
                                            color = p.blue,
                                            modifier = Modifier
                                                .alpha(if (enabled) 1f else 0.35f)
                                                .clickable(enabled = enabled, role = Role.Button) { onRetryItem(id) }
                                                .padding(horizontal = 6.dp, vertical = 8.dp),
                                        )
                                    }
                                    if (it.canCancel) {
                                        val enabled = !(it.serverBacked && it.networkActionsDisabled)
                                        val cancelLabel = copy(UiMessages.commonCancel())
                                        Box(
                                            Modifier
                                                .width(36.dp)
                                                .height(if (isOpen) 26.dp else 38.dp)
                                                .alpha(if (enabled) 1f else 0.35f)
                                                .clip(RoundedCornerShape(18.dp))
                                                .clickable(enabled = enabled, role = Role.Button, onClickLabel = cancelLabel) {
                                                    onCancelItem(id)
                                                }
                                                .semantics { contentDescription = cancelLabel },
                                            contentAlignment = Alignment.Center,
                                        ) {
                                            Ic(Lucide.X, 15.dp, p.ink2)
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }

        if (limit < ordered.size) {
            Row(
                Modifier
                    .fillMaxWidth()
                    .clickable(role = Role.Button) { expandedAll = true }
                    .padding(start = 40.dp, end = 16.dp, top = 7.dp, bottom = 8.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(4.dp),
            ) {
                Text(copy(UiMessages.queueMoreItems(count = ordered.size - limit)), style = T.s13, color = p.blue)
                Ic(Lucide.ChevronDown, 13.dp, p.blue)
            }
        } else if (expandedAll && ordered.size > 3) {
            Row(
                Modifier
                    .fillMaxWidth()
                    .clickable(role = Role.Button) { expandedAll = false }
                    .padding(start = 40.dp, end = 16.dp, top = 7.dp, bottom = 8.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(4.dp),
            ) {
                Text(copy(UiMessages.queueCollapse()), style = T.s13, color = p.blue)
                Ic(Lucide.ChevronUp, 13.dp, p.blue)
            }
        }

        if (undoVisible) {
            Row(
                Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp, vertical = 8.dp),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(uiText(tray.undo.label), style = T.s13, color = p.ink2)
                val canUndo = tray.undo.canUndo
                Text(
                    copy(UiMessages.queueUndo()),
                    style = T.s13,
                    color = p.blue,
                    modifier = Modifier
                        .alpha(if (canUndo) 1f else 0.35f)
                        .clickable(enabled = canUndo, role = Role.Button) { onUndo() }
                        .padding(horizontal = 4.dp, vertical = 2.dp),
                )
            }
        }
        Box(Modifier.fillMaxWidth().height(0.5.dp).background(p.sep))
    }
}
