package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
import androidx.compose.animation.Crossfade
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.foundation.LocalIndication
import androidx.compose.foundation.indication
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.movableContentOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.layout.boundsInWindow
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.IntRect
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.PopupPositionProvider
import androidx.compose.foundation.layout.Spacer

/** Places a menu above the message box, at its left ([right] false: the ＋ menu) or its right (the send menu). */
private class AboveProvider(private val gap: Int, private val right: Boolean) : PopupPositionProvider {
    override fun calculatePosition(anchorBounds: IntRect, windowSize: IntSize, layoutDirection: LayoutDirection, popupContentSize: IntSize): IntOffset {
        val x = if (right) anchorBounds.right - popupContentSize.width else anchorBounds.left
        return IntOffset(x.coerceIn(0, (windowSize.width - popupContentSize.width).coerceAtLeast(0)), (anchorBounds.top - popupContentSize.height - gap).coerceAtLeast(0))
    }
}

/**
 * The shared message box (design-system §6 输入框): a glass capsule with ＋
 * at the left, one to six lines of 17 text, and one round ink button at the
 * right. ＋ opens the library at once, or, when the computer takes files
 * ([onFile]), a small menu above it: 照片和视频, 文件. In a running session
 * the button is ■ interrupt while the box is empty; long-press send offers
 * queue when supported.
 *
 * With [tools] it is the new task's box (design-system §6 新任务): a rounded card of the same glass, the
 * text across the top, [top] (the tray) under it, and a row below: ＋, [tools] (the CLI and the model),
 * a gap, and the round button.
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
    sendLabel: String = uiText(UiMessages.composerSend()),
    focusRequester: FocusRequester? = null,
    onFocus: (Boolean) -> Unit = {},
    top: (@Composable () -> Unit)? = null,
    onQueue: (() -> Unit)? = null,
    onInterrupt: (() -> Unit)? = null,
    canInterrupt: Boolean = false,
    hasDraft: Boolean = text.isNotBlank(),
    demoted: Boolean = false,
    tools: (@Composable androidx.compose.foundation.layout.RowScope.() -> Unit)? = null,
) {
    val copy = uiText()
    val p = pal
    val menu = remember { ComposerPicker() }
    ComposerPickerBack(menu)
    var pickedQueue by remember { mutableStateOf(false) }
    val canChooseMode = tools == null && onQueue != null
    LaunchedEffect(canChooseMode, hasDraft) {
        if (!canChooseMode || !hasDraft) {
            pickedQueue = false
            if (menu.open == "send") menu.close()
        }
    }
    val gap = with(androidx.compose.ui.platform.LocalDensity.current) { 8.dp.roundToPx() }
    val shape = RoundedCornerShape(27.dp)
    Box(modifier.fillMaxWidth().composerPickerEvents(menu), contentAlignment = Alignment.TopCenter) {
        Box(Modifier.widthIn(max = ChatMax).fillMaxWidth().padding(start = 12.dp, end = 12.dp, top = 4.dp, bottom = 12.dp)) {
            // The box grows and shrinks with its lines and its tray of attachments (§4 动效, 常), the buttons
            // staying at the bottom; the glass outside the size animation so its shadow is not cut by it.
            val interrupt = !hasDraft && onInterrupt != null
            val on = if (interrupt) canInterrupt else canSend
            val currentText = androidx.compose.runtime.rememberUpdatedState(text)
            val currentOnText = androidx.compose.runtime.rememberUpdatedState(onText)
            val currentPlaceholder = androidx.compose.runtime.rememberUpdatedState(placeholder)
            val currentOnFocus = androidx.compose.runtime.rememberUpdatedState(onFocus)
            val movableField = remember(focusRequester) {
                movableContentOf<Modifier, Int> { m, lines ->
                    BasicTextField(
                        currentText.value, currentOnText.value,
                        m.then(if (focusRequester != null) Modifier.focusRequester(focusRequester) else Modifier)
                            .onFocusChanged { currentOnFocus.value(it.isFocused) },
                        textStyle = T.s17.copy(color = p.ink),
                        cursorBrush = SolidColor(p.blue),
                        maxLines = lines,
                        decorationBox = { inner ->
                            Box {
                                if (currentText.value.isEmpty()) Text(currentPlaceholder.value, style = T.s17, color = p.ink3, maxLines = 1, overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis)
                                inner()
                            }
                        },
                    )
                }
            }
            val triggerSend: () -> Unit = {
                if (menu.open == "send") menu.close()
                if (interrupt) {
                    onInterrupt?.invoke()
                } else if (pickedQueue && onQueue != null) {
                    pickedQueue = false
                    onQueue()
                } else {
                    pickedQueue = false
                    onSend()
                }
            }
            val send = @Composable {
                Box(Modifier.onGloballyPositioned { if (!canChooseMode || !hasDraft) menu.send = it.boundsInWindow() }) {
                    SendButton(interrupt, on, sendLabel, triggerSend, demoted = demoted)
                }
            }
            if (tools != null) {
                // The new task's card: the text grows to eight lines, then scrolls inside.
                Column(Modifier.fillMaxWidth().glass(p, Radii.R22, onGrey = LocalOnSoft.current).animateContentSize(Motion.enter(Motion.BASE), Alignment.BottomCenter).padding(start = 7.dp, end = 7.dp, top = 10.dp, bottom = 7.dp)) {
                    movableField(Modifier.fillMaxWidth().heightIn(min = 26.dp).padding(horizontal = 8.dp, vertical = 2.dp), 8)
                    top?.invoke()
                    Row(Modifier.fillMaxWidth().padding(top = 8.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(2.dp)) {
                        AttachButton(onPlus, onFile, plusEnabled, menu)
                        // The tools take what they need of the room between ＋ and ↑, the gap the rest.
                        Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(2.dp)) { tools() }
                        send()
                    }
                }
            } else if (canChooseMode && hasDraft) {
                // Two-row running draft layout with explicit send-mode pill; movableField preserves BasicTextField identity.
                Column(Modifier.fillMaxWidth().glass(p, Radii.R22, onGrey = LocalOnSoft.current).animateContentSize(Motion.enter(Motion.BASE), Alignment.BottomCenter).padding(start = 7.dp, end = 7.dp, top = 10.dp, bottom = 7.dp)) {
                    movableField(Modifier.fillMaxWidth().heightIn(min = 32.dp).padding(horizontal = 8.dp, vertical = 2.dp), 6)
                    top?.invoke()
                    Row(Modifier.fillMaxWidth().padding(top = 6.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        AttachButton(onPlus, onFile, plusEnabled, menu)
                        Spacer(Modifier.weight(1f))
                        Box(
                            Modifier
                                .heightIn(min = 44.dp)
                                .onGloballyPositioned { menu.send = it.boundsInWindow() }
                                .clickable(role = Role.Button, onClickLabel = copy(UiMessages.composerModePickerLabel())) {
                                    menu.toggle("send")
                                },
                            contentAlignment = Alignment.Center,
                        ) {
                            Row(
                                Modifier
                                    .heightIn(min = 32.dp)
                                    .clip(RoundedCornerShape(8.dp))
                                    .background(if (pickedQueue) p.fill2 else p.fill)
                                    .padding(start = 10.dp, end = 8.dp),
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(3.dp),
                            ) {
                                Text(
                                    if (pickedQueue) copy(UiMessages.composerModeQueueShort()) else copy(UiMessages.composerModeInsertShort()),
                                    style = if (pickedQueue) T.s13b else T.s13,
                                    color = if (pickedQueue) p.ink else p.ink2,
                                    maxLines = 1,
                                )
                                Ic(Lucide.ChevronDown, 13.dp, p.ink2)
                            }
                        }
                        send()
                    }
                }
            } else {
                Column(Modifier.fillMaxWidth().heightIn(min = 54.dp).glass(p, shape, onGrey = LocalOnSoft.current).animateContentSize(Motion.enter(Motion.BASE), Alignment.BottomCenter).padding(7.dp)) {
                    top?.invoke()
                    Row(verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        AttachButton(onPlus, onFile, plusEnabled, menu)
                        movableField(Modifier.weight(1f).heightIn(min = 40.dp).padding(horizontal = 4.dp, vertical = 8.dp), 6)
                        send()
                    }
                }
            }
            if (menu.open == "send" && canChooseMode) ComposerMenuPopup(menu, AboveProvider(gap, right = true)) {
                Column(Modifier.padding(12.dp).width(IntrinsicSize.Max).widthIn(min = 210.dp, max = 270.dp).floating(p, Radii.R24).glass(p, Radii.R24).padding(6.dp)) {
                    listOf(
                        Triple(copy(UiMessages.composerModeInsertShort()), copy(UiMessages.composerInsertHint()), false),
                        Triple(copy(UiMessages.composerAfterTurn()), copy(UiMessages.composerQueueHint()), true),
                    ).forEachIndexed { i, (label, detail, queueMode) ->
                        val selected = pickedQueue == queueMode
                        Column(
                            Modifier.fillMaxWidth().heightIn(min = 52.dp).then(if (i > 0) Modifier.topLine(p.sep) else Modifier)
                                .clip(Radii.R18).clickable {
                                    pickedQueue = queueMode
                                    menu.close()
                                }.padding(horizontal = 12.dp, vertical = 6.dp),
                            verticalArrangement = Arrangement.Center,
                        ) {
                            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.SpaceBetween) {
                                Text(label, style = T.s17, color = p.ink, modifier = Modifier.weight(1f))
                                if (selected) Ic(Lucide.Check, 16.dp, p.ink)
                            }
                            Text(detail, style = T.s13, color = p.ink2)
                        }
                    }
                }
            }
        }
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
        Modifier.size(44.dp).disabledAlpha(!interrupt || on).alpha(if (demoted && !interrupt) 0.5f else 1f)
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
private fun AttachButton(onPlus: () -> Unit, onFile: (() -> Unit)?, enabled: Boolean, menu: ComposerPicker) {
    val copy = uiText()
    val p = pal
    val open = menu.open == "attach"
    val gap = with(androidx.compose.ui.platform.LocalDensity.current) { 8.dp.roundToPx() }
    val label = if (onFile != null) copy(UiMessages.newSessionIosAttach()) else copy(UiMessages.imageAdd())
    Box(
        Modifier.size(40.dp).onGloballyPositioned { menu.attach = it.boundsInWindow() }.disabledAlpha(enabled).clip(CircleShape).background(if (open) p.fill2 else p.fill)
            .clickable(enabled = enabled, role = Role.Button, onClickLabel = label) { if (onFile != null) menu.toggle("attach") else onPlus() }
            .semantics { contentDescription = label },
        contentAlignment = Alignment.Center,
    ) {
        Ic(Lucide.Plus, 22.dp, p.ink)
        if (open && onFile != null) ComposerMenuPopup(menu, AboveProvider(gap, right = false)) {
            AttachMenu(onMedia = { menu.close(); onPlus() }, onFile = { menu.close(); onFile() })
        }
    }
}

/** The ＋ menu when the computer takes files: 照片和视频, 文件, and that files go as they are. */
@Composable
private fun AttachMenu(onMedia: () -> Unit, onFile: () -> Unit) {
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
        Text(uiText(FILES_AS_THEY_ARE), style = T.s13, color = p.ink2, modifier = Modifier.padding(start = 12.dp, end = 12.dp, top = 4.dp, bottom = 8.dp))
    }
}

/** Said wherever files are offered or waiting to go. */
val FILES_AS_THEY_ARE get() = UiMessages.attachmentMetadata()
