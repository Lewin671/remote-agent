package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
import androidx.compose.animation.Crossfade
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.foundation.LocalIndication
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.indication
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.isAltPressed
import androidx.compose.ui.input.key.isCtrlPressed
import androidx.compose.ui.input.key.isMetaPressed
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.boundsInWindow
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.IntRect
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.PopupPositionProvider

/** Places a menu above the message box, at its left (the ＋ menu). */
private class AboveProvider(private val gap: Int, private val right: Boolean) : PopupPositionProvider {
    override fun calculatePosition(anchorBounds: IntRect, windowSize: IntSize, layoutDirection: LayoutDirection, popupContentSize: IntSize): IntOffset {
        val x = if (right) anchorBounds.right - popupContentSize.width else anchorBounds.left
        return IntOffset(x.coerceIn(0, (windowSize.width - popupContentSize.width).coerceAtLeast(0)), (anchorBounds.top - popupContentSize.height - gap).coerceAtLeast(0))
    }
}

/**
 * The shared message box (design-system §6 输入框): a glass card optionally
 * hosting [pendingTray] on top, [top] (draft attachments) above the field,
 * ＋ at the left, one to six lines of 17 text, and up to two action buttons at
 * the right ("做完再发" + ↑ when running with draft, or ■ interrupt while empty).
 *
 * With [tools] it is the new task's box (design-system §6 新任务).
 */
@Composable
fun ComposeBox(
    text: String,
    onText: (String) -> Unit,
    placeholder: String,
    canSend: Boolean,
    onSend: () -> Unit,
    onPlus: () -> Unit,
    modifier: Modifier = Modifier,
    plusEnabled: Boolean = true,
    onFile: (() -> Unit)? = null,
    fileMetadataNotice: String = "",
    sendLabel: String = uiText(UiMessages.composerSend()),
    focusRequester: FocusRequester? = null,
    onFocus: (Boolean) -> Unit = {},
    top: (@Composable () -> Unit)? = null,
    pendingTray: (@Composable () -> Unit)? = null,
    hasPendingTray: Boolean = false,
    onQueue: (() -> Unit)? = null,
    onInterrupt: (() -> Unit)? = null,
    canInterrupt: Boolean = false,
    hasDraft: Boolean = text.isNotBlank(),
    demoted: Boolean = false,
    tools: (@Composable androidx.compose.foundation.layout.RowScope.() -> Unit)? = null,
) {
    val p = pal
    val menu = remember { ComposerPicker() }
    ComposerPickerBack(menu)
    val canChooseMode = tools == null && onQueue != null
    val showSendAfter = canChooseMode && hasDraft
    val useTwoRow = tools == null && (showSendAfter || text.length > 20 || text.contains('\n'))
    val interrupt = !hasDraft && onInterrupt != null
    val on = if (interrupt) canInterrupt else canSend
    val shape = if (tools != null || hasPendingTray || useTwoRow) Radii.R24 else RoundedCornerShape(27.dp)

    Box(modifier.fillMaxWidth().composerPickerEvents(menu), contentAlignment = Alignment.TopCenter) {
        Box(Modifier.widthIn(max = ChatMax).fillMaxWidth().padding(start = 12.dp, end = 12.dp, top = 4.dp, bottom = 12.dp)) {
            val field: @Composable (Modifier, Int) -> Unit = { m, lines ->
                BasicTextField(
                    text, onText,
                    m.then(if (focusRequester != null) Modifier.focusRequester(focusRequester) else Modifier)
                        .onFocusChanged { onFocus(it.isFocused) }
                        .onPreviewKeyEvent { ev ->
                            if (ev.type != KeyEventType.KeyDown || ev.key != Key.Enter) return@onPreviewKeyEvent false
                            when {
                                ev.isAltPressed && !ev.isCtrlPressed && !ev.isMetaPressed && onQueue != null -> {
                                    if (canSend) onQueue()
                                    true
                                }
                                (ev.isCtrlPressed || ev.isMetaPressed) && !ev.isAltPressed && !interrupt -> {
                                    if (canSend) onSend()
                                    true
                                }
                                else -> false
                            }
                        },
                    textStyle = T.s17.copy(color = p.ink),
                    cursorBrush = SolidColor(p.blue),
                    maxLines = lines,
                    decorationBox = { inner ->
                        Box {
                            if (text.isEmpty()) Text(placeholder, style = T.s17, color = p.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            inner()
                        }
                    },
                )
            }
            val triggerSend: () -> Unit = {
                if (interrupt) onInterrupt?.invoke() else onSend()
            }
            val send = @Composable {
                Box(Modifier.onGloballyPositioned { menu.send = it.boundsInWindow() }) {
                    SendButton(interrupt, on, sendLabel, triggerSend, demoted = demoted)
                }
            }

            if (tools != null) {
                Column(Modifier.fillMaxWidth().glass(p, Radii.R22, onGrey = LocalOnSoft.current).animateContentSize(Motion.enter(Motion.BASE), Alignment.BottomCenter).padding(start = 7.dp, end = 7.dp, top = 10.dp, bottom = 7.dp)) {
                    field(Modifier.fillMaxWidth().heightIn(min = 26.dp).padding(horizontal = 8.dp, vertical = 2.dp), 8)
                    top?.invoke()
                    Row(Modifier.fillMaxWidth().padding(top = 8.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(2.dp)) {
                        AttachButton(onPlus, onFile, plusEnabled, fileMetadataNotice, menu)
                        Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(2.dp)) { tools() }
                        send()
                    }
                }
            } else {
                Column(
                    Modifier
                        .fillMaxWidth()
                        .heightIn(min = 54.dp)
                        .glass(p, shape, onGrey = LocalOnSoft.current)
                        .animateContentSize(Motion.enter(Motion.BASE), Alignment.BottomCenter),
                ) {
                    pendingTray?.invoke()
                    Column(
                        Modifier
                            .fillMaxWidth()
                            .padding(start = 7.dp, end = 7.dp, top = if (useTwoRow) 10.dp else 7.dp, bottom = 7.dp),
                    ) {
                        top?.invoke()
                        ComposerInputLayout(
                            useTwoRow = useTwoRow,
                            attach = { AttachButton(onPlus, onFile, plusEnabled, fileMetadataNotice, menu) },
                            field = {
                                val fieldMod = if (useTwoRow) {
                                    Modifier.fillMaxWidth().heightIn(min = 32.dp).padding(horizontal = 8.dp, vertical = 2.dp)
                                } else {
                                    Modifier.fillMaxWidth().heightIn(min = 40.dp).padding(horizontal = 4.dp, vertical = 8.dp)
                                }
                                field(fieldMod, 6)
                            },
                            trailing = {
                                Row(
                                    verticalAlignment = Alignment.CenterVertically,
                                    horizontalArrangement = Arrangement.spacedBy(6.dp),
                                ) {
                                    if (showSendAfter) {
                                        onQueue?.let { SendAfterButton(enabled = canSend, onClick = it) }
                                    }
                                    send()
                                }
                            },
                        )
                    }
                }
            }
        }
    }
}

/**
 * Places attach (`+`), the text field, and trailing action buttons in either one row or two rows
 * without moving the text field across composition branches, so focus and IME stay intact.
 */
@Composable
private fun ComposerInputLayout(
    useTwoRow: Boolean,
    attach: @Composable () -> Unit,
    field: @Composable () -> Unit,
    trailing: @Composable () -> Unit,
) {
    Layout(
        content = {
            attach()
            field()
            trailing()
        },
        modifier = Modifier.fillMaxWidth(),
    ) { measurables, constraints ->
        val loose = constraints.copy(minWidth = 0, minHeight = 0)
        val attachPlaceable = measurables[0].measure(loose)
        val trailingPlaceable = measurables[2].measure(loose)
        if (!useTwoRow) {
            val gapPx = 8.dp.roundToPx()
            val fieldConstraints = if (constraints.hasBoundedWidth) {
                val fieldW = (constraints.maxWidth - attachPlaceable.width - trailingPlaceable.width - gapPx * 2).coerceAtLeast(0)
                constraints.copy(minWidth = fieldW, maxWidth = fieldW, minHeight = 0)
            } else {
                loose
            }
            val fieldPlaceable = measurables[1].measure(fieldConstraints)
            val width = if (constraints.hasBoundedWidth) {
                constraints.maxWidth
            } else {
                attachPlaceable.width + gapPx + fieldPlaceable.width + gapPx + trailingPlaceable.width
            }
            val height = maxOf(attachPlaceable.height, fieldPlaceable.height, trailingPlaceable.height)
            layout(width, height) {
                attachPlaceable.placeRelative(0, height - attachPlaceable.height)
                fieldPlaceable.placeRelative(attachPlaceable.width + gapPx, height - fieldPlaceable.height)
                trailingPlaceable.placeRelative(width - trailingPlaceable.width, height - trailingPlaceable.height)
            }
        } else {
            val rowGapPx = 6.dp.roundToPx()
            val fieldConstraints = if (constraints.hasBoundedWidth) {
                constraints.copy(minWidth = constraints.maxWidth, maxWidth = constraints.maxWidth, minHeight = 0)
            } else {
                loose
            }
            val fieldPlaceable = measurables[1].measure(fieldConstraints)
            val width = if (constraints.hasBoundedWidth) {
                constraints.maxWidth
            } else {
                maxOf(fieldPlaceable.width, attachPlaceable.width + trailingPlaceable.width)
            }
            val bottomRowHeight = maxOf(attachPlaceable.height, trailingPlaceable.height)
            val height = fieldPlaceable.height + rowGapPx + bottomRowHeight
            layout(width, height) {
                fieldPlaceable.placeRelative(0, 0)
                val bottomY = fieldPlaceable.height + rowGapPx
                attachPlaceable.placeRelative(0, bottomY + (bottomRowHeight - attachPlaceable.height) / 2)
                trailingPlaceable.placeRelative(width - trailingPlaceable.width, bottomY + (bottomRowHeight - trailingPlaceable.height) / 2)
            }
        }
    }
}

@Composable
private fun SendAfterButton(enabled: Boolean, onClick: () -> Unit) {
    val copy = uiText()
    val p = pal
    val source = remember { androidx.compose.foundation.interaction.MutableInteractionSource() }
    val pressed by source.collectIsPressedAsState()
    val scale by animateFloatAsState(
        if (pressed && enabled) 0.98f else 1f,
        if (Motion.reduced) snap() else tween(Motion.FAST, easing = Motion.Decelerate),
        label = "later-press",
    )
    val label = copy(UiMessages.composerSendAfter())
    Box(
        Modifier
            .heightIn(min = 36.dp)
            .graphicsLayer { scaleX = scale; scaleY = scale }
            .disabledAlpha(enabled)
            .clip(RoundedCornerShape(18.dp))
            .background(p.fill2)
            .clickable(
                interactionSource = source,
                indication = LocalIndication.current,
                enabled = enabled,
                role = Role.Button,
                onClickLabel = label,
                onClick = onClick,
            )
            .padding(horizontal = 14.dp, vertical = 8.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            label,
            style = T.s15.copy(fontSize = 14.sp, fontWeight = platformWeight(FontWeight.Medium)),
            color = p.ink,
            maxLines = 1,
        )
    }
}

/**
 * The round ink button at the right of the message box: ↑ sends, ■ interrupts while the box is empty in
 * a running session. Pressed, it shrinks a little and comes back; ink or grey, and ↑ or ■, change by
 * cross-fading (design-system §4 动效, 快).
 */
@Composable
private fun SendButton(interrupt: Boolean, on: Boolean, sendLabel: String, onClick: () -> Unit, demoted: Boolean = false) {
    val copy = uiText()
    val p = pal
    val source = remember { androidx.compose.foundation.interaction.MutableInteractionSource() }
    val pressed by source.collectIsPressedAsState()
    val scale by animateFloatAsState(if (pressed && on) Motion.PRESS_SCALE else 1f, if (Motion.reduced) snap() else tween(Motion.FAST, easing = Motion.Decelerate), label = "send-press")
    val fade = tween<Color>(Motion.FAST, easing = Motion.Decelerate)
    val bg by animateColorAsState(if (interrupt || on) p.btn else p.fill2, fade, label = "send-bg")
    val fg by animateColorAsState(if (interrupt || on) p.btnFg else p.ink3, fade, label = "send-fg")
    Box(
        // Keep the touch target fixed while only the 40dp ink circle shrinks on press.
        Modifier.size(44.dp).disabledAlpha(!interrupt || on).graphicsLayer { alpha = if (demoted && !interrupt) 0.5f else 1f }
            .clickable(interactionSource = source, indication = null, enabled = on, role = Role.Button,
                onClickLabel = if (interrupt) copy(UiMessages.sessionInterruptTurn()) else sendLabel,
                onClick = onClick)
            .semantics { contentDescription = if (interrupt) copy(UiMessages.sessionInterruptTurn()) else sendLabel },
        contentAlignment = Alignment.Center,
    ) {
        Box(Modifier.size(40.dp).graphicsLayer { scaleX = scale; scaleY = scale }.clip(CircleShape)
            .background(bg).indication(source, LocalIndication.current), contentAlignment = Alignment.Center) {
            Crossfade(interrupt, animationSpec = Motion.fadeInSpec(Motion.FAST), label = "send-icon") { stop ->
                if (stop) Ic(Lucide.Square, 14.dp, fg) else Ic(Lucide.ArrowUp, 20.dp, fg)
            }
        }
    }
}

/**
 * ＋ (design-system §6 输入框): a fill circle, 40. It opens the library at
 * once, or, when the computer takes files ([onFile]), a glass menu above it.
 */
@Composable
private fun AttachButton(onPlus: () -> Unit, onFile: (() -> Unit)?, enabled: Boolean, fileMetadataNotice: String, menu: ComposerPicker) {
    val copy = uiText()
    val p = pal
    val open = menu.open == "attach"
    val gap = with(LocalDensity.current) { 8.dp.roundToPx() }
    val label = if (onFile != null) copy(UiMessages.newSessionIosAttach()) else copy(UiMessages.imageAdd())
    Box(
        Modifier.size(40.dp).onGloballyPositioned { menu.attach = it.boundsInWindow() }.disabledAlpha(enabled).clip(CircleShape).background(if (open) p.fill2 else p.fill)
            .clickable(enabled = enabled, role = Role.Button, onClickLabel = label) { if (onFile != null) menu.toggle("attach") else onPlus() }
            .semantics { contentDescription = label },
        contentAlignment = Alignment.Center,
    ) {
        Ic(Lucide.Plus, 22.dp, p.ink)
        if (open && onFile != null) ComposerMenuPopup(menu, AboveProvider(gap, right = false)) {
            AttachMenu(onMedia = { menu.close(); onPlus() }, onFile = { menu.close(); onFile() }, fileMetadataNotice = fileMetadataNotice)
        }
    }
}

/** The ＋ menu when the computer takes files: 照片和视频, 文件, and that files go as they are. */
@Composable
private fun AttachMenu(onMedia: () -> Unit, onFile: () -> Unit, fileMetadataNotice: String) {
    val copy = uiText()
    val p = pal
    Column(Modifier.padding(12.dp).width(248.dp).floating(p, Radii.R24).glass(p, Radii.R24).padding(6.dp)) {
        listOf(Triple(Lucide.Image, copy(UiMessages.attachmentPhotosVideos()), onMedia), Triple(Lucide.File, copy(UiMessages.attachmentFile()), onFile)).forEachIndexed { i, (icon, label, action) ->
            Row(
                Modifier.fillMaxWidth().heightIn(min = 48.dp).then(if (i > 0) Modifier.topLine(p.sep) else Modifier)
                    .clip(Radii.R18).clickable(role = Role.Button, onClick = action).padding(horizontal = 12.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                Ic(icon, 22.dp, p.ink2)
                Text(label, style = T.s17, color = p.ink)
            }
        }
        if (fileMetadataNotice.isNotEmpty()) {
            Text(fileMetadataNotice, style = T.s13, color = p.ink2, modifier = Modifier.padding(start = 12.dp, end = 12.dp, top = 4.dp, bottom = 8.dp))
        }
    }
}
