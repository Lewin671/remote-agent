package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.animation.SizeTransform
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.wrapContentHeight
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.layout
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.role
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.SessionInputNoticeKind
import io.github.lewin671.remoteagent.core.proto.SessionMessageTextSegment
import io.github.lewin671.remoteagent.core.proto.SessionNoteKind
import io.github.lewin671.remoteagent.core.proto.SessionNoteTone
import io.github.lewin671.remoteagent.core.proto.SessionNotifyReceiptItem
import io.github.lewin671.remoteagent.core.proto.SessionTimelineItem
import io.github.lewin671.remoteagent.core.proto.SessionToolStatus
import kotlin.math.roundToInt

/** Side gutter of rows inside a chat (design-system §3). */
val ChatSide = 16.dp

enum class CaptionAction { RETRY, CANCEL, RESUME, RESTORE, DROP }

class CaptionLink(val action: CaptionAction, val label: String, val onClick: () -> Unit)

private data class CaptionLinkLabel(val action: CaptionAction, val label: String)

private data class CaptionState(
    val caption: String?,
    val color: Color?,
    val icon: ImageVector?,
    val links: List<CaptionLinkLabel>,
)

@Composable
fun MeBubble(
    text: String?,
    caption: String? = null,
    captionColor: Color? = null,
    captionIcon: ImageVector? = null,
    failed: Boolean = false,
    links: List<CaptionLink> = emptyList(),
    padded: Boolean = true,
    top: (@Composable () -> Unit)? = null,
    foldScheduled: Boolean = false,
    animated: Boolean = false,
    settling: Boolean = false,
    richText: AnnotatedString? = null,
) {
    val p = pal
    val body = richText ?: text?.let { AnnotatedString(it) }
    var expanded by remember(body) { mutableStateOf(false) }
    var long by remember(body) { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth().padding(horizontal = if (padded) ChatSide else 0.dp, vertical = 8.dp), horizontalAlignment = Alignment.End) {
        top?.invoke()
        if (body != null) Box(Modifier.fillMaxWidth(0.84f), contentAlignment = Alignment.CenterEnd) {
            val shape = Radii.R22
            Box(
                Modifier.clip(shape).background(if (failed) p.fill2 else p.bubble)
                    .then(if (failed) Modifier.border(1.5.dp, p.red, shape) else Modifier).padding(horizontal = 16.dp, vertical = 10.dp),
            ) {
                SelectionContainer {
                    Text(
                        body,
                        style = T.s17,
                        color = p.ink,
                        maxLines = if (foldScheduled && !expanded) 6 else Int.MAX_VALUE,
                        overflow = TextOverflow.Ellipsis,
                        onTextLayout = { if (!expanded) long = it.hasVisualOverflow },
                    )
                }
            }
        }
        val shown = caption != null || links.isNotEmpty()
        if (animated) {
            val state = if (shown) CaptionState(caption, captionColor, captionIcon, links.map { CaptionLinkLabel(it.action, it.label) }) else null
            AnimatedContent(
                state,
                contentAlignment = Alignment.TopEnd,
                transitionSpec = {
                    (fadeIn(Motion.fadeInSpec(Motion.FAST)) togetherWith fadeOut(Motion.fadeOutSpec(Motion.FAST))) using
                        SizeTransform(clip = true) { _, _ -> Motion.enter(Motion.BASE) }
                },
                label = "status-line",
            ) { st ->
                if (st != null) CaptionRow(
                    st.caption,
                    st.color,
                    st.icon,
                    st.links.map { l -> CaptionLink(l.action, l.label, links.firstOrNull { it.action == l.action }?.onClick ?: {}) },
                    foldScheduled,
                    long,
                    expanded,
                ) { expanded = !expanded }
            }
        } else if (shown) {
            CaptionRow(caption, captionColor, captionIcon, links, foldScheduled, long, expanded) { expanded = !expanded }
        } else if (settling) {
            val room = remember { Animatable(1f) }
            LaunchedEffect(Unit) { room.animateTo(0f, Motion.enter(Motion.BASE)) }
            val px = with(LocalDensity.current) { (6.dp + T.s13.lineHeight.toDp()).toPx() }
            Spacer(Modifier.layout { m, c -> val pl = m.measure(c); layout(pl.width, (px * room.value).roundToInt()) {} })
        }
    }
}

@Composable
private fun CaptionRow(
    caption: String?,
    captionColor: Color?,
    captionIcon: ImageVector?,
    links: List<CaptionLink>,
    foldScheduled: Boolean,
    long: Boolean,
    expanded: Boolean,
    onToggle: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    Row(Modifier.padding(top = 6.dp, end = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        if (captionIcon != null) {
            Mark(captionIcon, captionColor ?: p.ink2, iconSize(T.s13))
            Spacer(Modifier.width(4.dp))
        }
        if (caption != null) {
            val suffix = if (foldScheduled && long) " · " + if (expanded) copy(UiMessages.markdownCollapse()) else copy(UiMessages.markdownExpand()) else ""
            Text(
                caption + suffix,
                style = T.s13,
                color = captionColor ?: p.ink2,
                textAlign = TextAlign.End,
                modifier = Modifier.weight(1f, fill = false).then(if (foldScheduled && long) Modifier.heightIn(min = 44.dp).clickable(onClick = onToggle) else Modifier),
            )
        }
        for (link in links) {
            Text(
                link.label,
                style = T.s13,
                color = p.blue,
                modifier = Modifier.padding(start = 12.dp).heightIn(min = 32.dp).clickable(onClick = link.onClick).wrapContentHeight(),
            )
        }
    }
}

@Composable
fun SepLine(text: String) {
    val p = pal
    Row(Modifier.fillMaxWidth().padding(horizontal = ChatSide, vertical = 14.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.weight(1f).height(0.5.dp).background(p.sep))
        Text(text, style = T.s13, color = p.ink2, modifier = Modifier.padding(horizontal = 10.dp))
        Box(Modifier.weight(1f).height(0.5.dp).background(p.sep))
    }
}

@Composable
fun ForkLine(label: String, title: String, onOpen: (() -> Unit)?) {
    val p = pal
    BoxWithConstraints(Modifier.fillMaxWidth().padding(horizontal = ChatSide)) {
        val room = (maxWidth - 32.dp).coerceAtLeast(0.dp)
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.weight(1f).height(0.5.dp).background(p.sep))
            Row(Modifier.widthIn(max = room).padding(horizontal = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                if (title.isEmpty()) {
                    Text(label, style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                } else {
                    Text(label, style = T.s13, color = p.ink2, maxLines = 1, softWrap = false)
                    Spacer(Modifier.width(4.dp))
                    Row(
                        Modifier.weight(1f, fill = false).then(if (onOpen != null) Modifier.tap(onClick = onOpen) else Modifier).padding(vertical = 12.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Text(title, style = T.s13, color = if (onOpen != null) p.blue else p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                        if (onOpen != null) {
                            Spacer(Modifier.width(4.dp))
                            Arrow(p.blue, T.s13)
                        }
                    }
                }
            }
            Box(Modifier.weight(1f).height(0.5.dp).background(p.sep))
        }
    }
}

@Composable
fun ToolLine(
    mark: ImageVector,
    markColor: Color,
    title: String,
    modifier: Modifier = Modifier,
    meta: String = "",
    metaColor: Color? = null,
    onClick: (() -> Unit)? = null,
    bold: Boolean = false,
    trailing: (@Composable () -> Unit)? = null,
) {
    val p = pal
    Row(
        modifier.fillMaxWidth().then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier).padding(vertical = 5.dp).heightIn(min = 20.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Mark(mark, markColor, 15.dp)
        Spacer(Modifier.width(8.dp))
        Row(
            Modifier.weight(1f),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(if (trailing != null) 4.dp else 8.dp),
        ) {
            if (bold) Text(title, style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
            else Text(title, style = T.mono13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
            trailing?.invoke()
        }
        if (meta.isNotEmpty()) {
            Spacer(Modifier.width(8.dp))
            Text(meta, style = T.s13, color = metaColor ?: p.ink2, maxLines = 1)
        }
    }
}

fun planFolds(text: String, lines: Int): Boolean = text.lines().size > lines

@Composable
fun Expandable(title: String, modifier: Modifier = Modifier, color: Color? = null, saveKey: String = title, content: @Composable () -> Unit) {
    val copy = uiText()
    val p = pal
    var open by rememberSaveable(saveKey) { mutableStateOf(false) }
    Column(modifier.animateContentSize()) {
        Box(Modifier.fillMaxWidth().clickable { open = !open }.padding(vertical = 6.dp)) {
            ArrowText(if (open) copy(UiMessages.sessionAndroidCollapseTitle(title = title)) else title, color ?: p.ink2, T.s13, down = !open, up = open)
        }
        if (open) content()
    }
}

/**
 * A scheduled run's push, left as one grey line (design-system §7): Core
 * says what it shows; the agent's longer texts open in place. Two receipts
 * in a row sit 4 apart, so the row is told whether one comes before or after.
 */
@Composable
private fun NotifyReceiptRow(id: String, r: SessionNotifyReceiptItem, receiptBefore: Boolean, receiptAfter: Boolean) {
    val copy = uiText()
    val p = pal
    var open by rememberSaveable(id) { mutableStateOf(false) }
    val turn by animateFloatAsState(if (open) 180f else 0f, tween(160, easing = Motion.Decelerate), label = "receipt-chevron")
    val line = uiText(r.label) + " · " + if (open || r.preview.isEmpty()) uiText(r.time) else r.preview
    val bleed = 12.dp
    Column(Modifier.fillMaxWidth().padding(start = ChatSide, end = ChatSide, top = if (receiptBefore) 0.dp else 6.dp, bottom = if (receiptAfter) 4.dp else 6.dp)) {
        Row(
            Modifier
                .then(
                    if (r.expandable) {
                        // The whole line answers a tap, 12 more above and below, without taking that room.
                        Modifier.layout { m, c ->
                            val px = bleed.roundToPx()
                            val pl = m.measure(c)
                            layout(pl.width, (pl.height - 2 * px).coerceAtLeast(0)) { pl.place(0, -px) }
                        }
                            .clearAndSetSemantics {
                                role = Role.Button
                                contentDescription = if (open) copy(UiMessages.markdownCollapse()) else copy(UiMessages.sessionNotifyReceiptExpand())
                                onClick { open = !open; true }
                            }
                            .clickable { open = !open }
                            .padding(vertical = bleed)
                    } else Modifier,
                )
                .heightIn(min = 20.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Mark(Lucide.Bell, p.ink3, 15.dp)
            Spacer(Modifier.width(8.dp))
            Text(line, style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
            if (r.expandable) {
                Spacer(Modifier.width(4.dp))
                Box(Modifier.rotate(turn)) { Arrow(p.ink3, T.s13, down = true) }
            }
        }
        AnimatedVisibility(open && r.expandable, enter = fadeIn(tween(Motion.FAST)), exit = ExitTransition.None) {
            Row(Modifier.padding(top = 8.dp, bottom = 4.dp, start = 6.dp).height(IntrinsicSize.Min)) {
                Box(Modifier.width(2.dp).fillMaxHeight().background(p.faint))
                Text(r.body, style = T.s15.copy(lineHeight = 22.5.sp), color = p.ink2, modifier = Modifier.padding(start = 15.dp))
            }
        }
    }
}

/** Renders one [SessionTimelineItem] from Core's [SessionViewModel.timeline]. */
@Composable
internal fun TimelineItemView(
    item: SessionTimelineItem,
    settling: Boolean = false,
    onRestoreInput: ((requestId: String, text: String) -> Unit)? = null,
    onTerminalPopup: (() -> Unit)? = null,
    receiptBefore: Boolean = false,
    receiptAfter: Boolean = false,
) {
    val copy = uiText()
    val p = pal
    val chat = LocalChat.current
    when (item.kindCase) {
        SessionTimelineItem.KindCase.QUIET_SCHEDULED_GROUP -> {
            val g = item.quietScheduledGroup
            var expanded by rememberSaveable(item.id) { mutableStateOf(g.expanded) }
            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { expanded = !expanded }.padding(horizontal = ChatSide), verticalAlignment = Alignment.CenterVertically) {
                Text(uiText(g.title), Modifier.weight(1f), style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Arrow(p.ink2, T.s13, down = !expanded, up = expanded)
            }
            if (expanded) g.itemsList.forEach { TimelineItemView(it, onRestoreInput = onRestoreInput, onTerminalPopup = onTerminalPopup) }
        }
        SessionTimelineItem.KindCase.USER_MESSAGE -> {
            val um = item.userMessage
            val richBody = if (um.textSegmentsCount > 0) {
                buildAnnotatedString {
                    for (seg in um.textSegmentsList) {
                        when (seg.kindCase) {
                            SessionMessageTextSegment.KindCase.IMAGE_INDEX -> {
                                val label = if (seg.hasImageLabel()) uiText(seg.imageLabel) else "[Image #${seg.imageIndex}]"
                                pushStyle(SpanStyle(color = p.ink2, fontWeight = platformWeight(FontWeight.Medium)))
                                append(label)
                                pop()
                            }
                            SessionMessageTextSegment.KindCase.TEXT -> append(seg.text)
                            else -> Unit
                        }
                    }
                }.takeIf { it.isNotEmpty() }
            } else null
            MeBubble(
                text = if (richBody != null) null else um.text.ifEmpty { null },
                richText = richBody,
                caption = if (um.hasOriginCaption()) uiText(um.originCaption) else null,
                captionIcon = if (um.isScheduled) Lucide.Clock else null,
                foldScheduled = um.isScheduled,
                settling = settling,
                top = if (um.imagesCount > 0 || um.filesCount > 0) {
                    {
                        if (um.imagesCount > 0) ImageRow(um.imagesList, { i -> chat?.onImage?.invoke(item.seq, i) })
                        if (um.filesCount > 0) {
                            FileChips(um.filesList)
                        }
                    }
                } else null,
            )
        }
        SessionTimelineItem.KindCase.AGENT_MESSAGE ->
            Markdown(item.agentMessage.markdown, Modifier.fillMaxWidth().padding(horizontal = ChatSide, vertical = 8.dp), bleed = ChatSide)
        SessionTimelineItem.KindCase.REASONING -> Expandable(copy(UiMessages.sessionAndroidThinking()), Modifier.padding(horizontal = ChatSide), saveKey = item.id) {
            Markdown(item.reasoning.markdown, Modifier.padding(top = 4.dp, bottom = 6.dp))
        }
        SessionTimelineItem.KindCase.TOOL_CALL -> ToolCallRow(item.toolCall, item.id, item.seq)
        SessionTimelineItem.KindCase.TOOL_GROUP -> {
            val g = item.toolGroup
            var expanded by rememberSaveable(item.id) { mutableStateOf(false) }
            var commands by rememberSaveable(item.id) { mutableStateOf(emptyList<String>()) }
            val groupIcon = when {
                g.callsList.any { it.status == SessionToolStatus.SESSION_TOOL_STATUS_ERROR || it.status == SessionToolStatus.SESSION_TOOL_STATUS_DENIED } -> Lucide.X
                g.callsList.any {
                    it.status == SessionToolStatus.SESSION_TOOL_STATUS_RUNNING ||
                        it.status == SessionToolStatus.SESSION_TOOL_STATUS_UNCONFIRMED ||
                        it.status == SessionToolStatus.SESSION_TOOL_STATUS_BACKGROUND
                } -> Lucide.LoaderCircle
                else -> Lucide.Check
            }
            Column(Modifier.padding(horizontal = ChatSide).animateContentSize(Motion.enter(Motion.BASE))) {
                ToolLine(
                    groupIcon,
                    p.ink3,
                    uiText(g.summary),
                    onClick = { expanded = !expanded },
                    bold = true,
                    trailing = { Arrow(p.ink2, T.s13, down = !expanded, up = expanded) },
                )
                if (expanded) g.callsList.forEachIndexed { idx, call ->
                    val cid = call.callId.ifEmpty { idx.toString() }
                    androidx.compose.runtime.key(cid) {
                        ToolCallRow(
                            call,
                            "${item.id}:$cid",
                            call.seq.takeIf { it > 0L } ?: item.seq,
                            expanded = cid in commands,
                            onToggle = { commands = if (cid in commands) commands - cid else commands + cid },
                            modifier = Modifier.padding(start = 10.dp),
                        )
                    }
                }
            }
        }
        SessionTimelineItem.KindCase.SHARED_FILES -> if (chat != null) {
            SharedFilesBlock(item.sharedFiles.filesList, chat.sid)
        }
        SessionTimelineItem.KindCase.GENERATED_IMAGES -> {
            val g = item.generatedImages
            GeneratedCard(item.id, g.imagesList, g.revisedPrompt) { i -> chat?.onImage?.invoke(item.seq, i) }
        }
        SessionTimelineItem.KindCase.ASYNC_QUESTION -> SelectionContainer {
            Column(Modifier.fillMaxWidth().padding(horizontal = ChatSide, vertical = 8.dp).clip(Radii.R18).background(p.gbg).padding(14.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(copy(UiMessages.listStateQuestion()), style = T.s13b, color = p.ink2)
                for (q in item.asyncQuestion.questionsList) {
                    if (q.questionText.isNotEmpty()) Text(q.questionText, style = T.s15, color = p.ink)
                    for (o in q.optionsList) {
                        val line = listOfNotNull("${o.optionNum}. ${o.label}", o.description.takeIf { it.isNotEmpty() }).joinToString(" — ")
                        Text(line, style = T.s13, color = p.ink2)
                    }
                }
            }
        }
        SessionTimelineItem.KindCase.PLAN -> {
            var full by rememberSaveable(item.id) { mutableStateOf(false) }
            val md = item.plan.markdown
            val cut = planFolds(md, 14) && !full
            Column(Modifier.padding(horizontal = ChatSide, vertical = 8.dp).fillMaxWidth().clip(Radii.R18).background(p.gbg).padding(14.dp)) {
                Text(copy(UiMessages.modesPlan()), style = T.s13b, color = p.ink2, modifier = Modifier.padding(bottom = 6.dp))
                Markdown(if (cut) md.lines().take(14).joinToString("\n") else md)
                if (planFolds(md, 14)) Box(Modifier.clickable(role = Role.Button) { full = !full }.padding(top = 8.dp)) {
                    ArrowText(if (full) copy(UiMessages.markdownCollapse()) else copy(UiMessages.markdownExpand()), p.blue, T.s13, down = !full, up = full)
                }
            }
        }
        SessionTimelineItem.KindCase.NOTE -> {
            val n = item.note
            val color = when (n.tone) {
                SessionNoteTone.SESSION_NOTE_TONE_POSITIVE -> p.green
                SessionNoteTone.SESSION_NOTE_TONE_NEGATIVE -> p.red
                SessionNoteTone.SESSION_NOTE_TONE_WARNING -> p.orange
                else -> p.ink2
            }
            val meta = if (n.hasMeta()) uiText(n.meta) else ""
            val emph = if (n.hasEmphasis()) uiText(n.emphasis) else ""
            val click = if (n.kind == SessionNoteKind.SESSION_NOTE_KIND_TERMINAL_CHANGES && onTerminalPopup != null) onTerminalPopup else null
            Column(Modifier.fillMaxWidth().then(if (click != null) Modifier.clickable(onClick = click) else Modifier).padding(horizontal = ChatSide, vertical = 5.dp)) {
                Text(uiText(n.text), style = T.s13, color = color)
                if (emph.isNotEmpty()) Text(emph, style = T.s15b, color = p.ink, modifier = Modifier.padding(top = 2.dp))
                if (meta.isNotEmpty()) Text(meta, style = T.s13, color = p.ink2, modifier = Modifier.padding(top = 2.dp))
            }
        }
        SessionTimelineItem.KindCase.NOTIFY_RECEIPT -> NotifyReceiptRow(item.id, item.notifyReceipt, receiptBefore, receiptAfter)
        SessionTimelineItem.KindCase.TURN_END -> SepLine(uiText(item.turnEnd.label))
        SessionTimelineItem.KindCase.ERROR_LINE -> Text(
            uiText(item.errorLine.text),
            style = T.s13,
            color = p.red,
            modifier = Modifier.fillMaxWidth().padding(horizontal = ChatSide, vertical = 6.dp),
        )
        SessionTimelineItem.KindCase.FORK_MARKER -> {
            val f = item.forkMarker
            val src = f.sourceSessionId.takeIf { it.isNotEmpty() }
            ForkLine(
                uiText(f.text),
                if (f.hasSourceTitle()) uiText(f.sourceTitle) else "",
                if (chat != null && src != null) ({ chat.onSession(src) }) else null,
            )
        }
        SessionTimelineItem.KindCase.INPUT_STATE -> {
            val st = item.inputState
            val cap = listOfNotNull(
                if (st.hasStatusText()) uiText(st.statusText).ifEmpty { null } else null,
                if (st.hasReasonText()) uiText(st.reasonText).ifEmpty { null } else null,
            ).joinToString(" · ").ifEmpty { null }
            val links = if (st.preview.isNotEmpty() && onRestoreInput != null) {
                listOf(CaptionLink(CaptionAction.RESTORE, copy(UiMessages.composerRestore())) { onRestoreInput(st.requestId, st.preview) })
            } else emptyList()
            MeBubble(
                text = st.preview.ifEmpty { null },
                caption = cap,
                captionColor = if (st.kind == SessionInputNoticeKind.SESSION_INPUT_NOTICE_KIND_CANCELED) p.ink2 else p.red,
                failed = st.kind != SessionInputNoticeKind.SESSION_INPUT_NOTICE_KIND_CANCELED,
                links = links,
            )
        }
        SessionTimelineItem.KindCase.SKELETON ->
            Text(uiText(item.skeleton.text), style = T.s13, color = p.ink3, modifier = Modifier.padding(horizontal = ChatSide, vertical = 6.dp))
        else -> {}
    }
}
