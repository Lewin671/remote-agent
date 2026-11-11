package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.Crossfade
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.foundation.LocalIndication
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
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
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
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
import androidx.compose.ui.window.Popup
import androidx.compose.ui.window.PopupPositionProvider
import androidx.compose.ui.window.PopupProperties

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
    sendLabel: String = "发送",
    focusRequester: FocusRequester? = null,
    onFocus: (Boolean) -> Unit = {},
    top: (@Composable () -> Unit)? = null,
    onQueue: (() -> Unit)? = null,
    onInterrupt: (() -> Unit)? = null,
    canInterrupt: Boolean = false,
    hasDraft: Boolean = text.isNotBlank(),
) {
    val p = pal
    var sendMenu by remember { mutableStateOf(false) }
    val gap = with(androidx.compose.ui.platform.LocalDensity.current) { 8.dp.roundToPx() }
    val shape = RoundedCornerShape(27.dp)
    Box(modifier.fillMaxWidth(), contentAlignment = Alignment.TopCenter) {
        Box(Modifier.widthIn(max = ChatMax).fillMaxWidth().padding(start = 12.dp, end = 12.dp, top = 4.dp, bottom = 12.dp)) {
            // The box grows and shrinks with its lines and its tray of attachments (§4 动效, 常), the buttons
            // staying at the bottom; the glass outside the size animation so its shadow is not cut by it.
            Column(Modifier.fillMaxWidth().heightIn(min = 54.dp).glass(p, shape, onGrey = LocalOnSoft.current).animateContentSize(Motion.enter(Motion.BASE), Alignment.BottomCenter).padding(7.dp)) {
                top?.invoke()
                Row(verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    AttachButton(onPlus, onFile, plusEnabled)
                    BasicTextField(
                        text, onText,
                        Modifier.weight(1f).heightIn(min = 40.dp).padding(horizontal = 4.dp, vertical = 8.dp)
                            .then(if (focusRequester != null) Modifier.focusRequester(focusRequester) else Modifier)
                            .onFocusChanged { onFocus(it.isFocused) },
                        textStyle = T.s17.copy(color = p.ink),
                        cursorBrush = SolidColor(p.blue),
                        maxLines = 6,
                        decorationBox = { inner ->
                            Box {
                                if (text.isEmpty()) Text(placeholder, style = T.s17, color = p.ink3, maxLines = 1, overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis)
                                inner()
                            }
                        },
                    )
                    val interrupt = !hasDraft && onInterrupt != null
                    val on = if (interrupt) canInterrupt else canSend
                    SendButton(interrupt, on, sendLabel, { if (interrupt) onInterrupt?.invoke() else onSend() }, if (!interrupt && onQueue != null) ({ sendMenu = true }) else null)
                }
            }
            if (sendMenu) Popup(popupPositionProvider = AboveProvider(gap, right = true), onDismissRequest = { sendMenu = false }, properties = PopupProperties(focusable = true)) {
                Column(Modifier.padding(12.dp).width(IntrinsicSize.Max).widthIn(min = 200.dp, max = 260.dp).floating(p, Radii.R24).glass(p, Radii.R24).padding(6.dp)) {
                    listOf(
                        Triple("现在插入这一轮", "点 ↑ 就是这个", onSend),
                        Triple("这一轮结束后再发", "先排队，结束后按顺序发出", { onQueue?.invoke() }),
                    ).forEachIndexed { i, (label, detail, action) ->
                        Column(
                            Modifier.fillMaxWidth().heightIn(min = 52.dp).then(if (i > 0) Modifier.topLine(p.sep) else Modifier)
                                // The press follows the menu's corners: the menu's 24 less its 6 of padding.
                                .clip(Radii.R18).clickable { sendMenu = false; action() }.padding(horizontal = 12.dp, vertical = 6.dp),
                            verticalArrangement = Arrangement.Center,
                        ) {
                            Text(label, style = T.s17, color = p.ink)
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
private fun SendButton(interrupt: Boolean, on: Boolean, sendLabel: String, onClick: () -> Unit, onLongClick: (() -> Unit)?) {
    val p = pal
    val source = remember { androidx.compose.foundation.interaction.MutableInteractionSource() }
    val pressed by source.collectIsPressedAsState()
    val scale by animateFloatAsState(if (pressed && on) Motion.PRESS_SCALE else 1f, if (Motion.reduced) snap() else tween(Motion.FAST, easing = Motion.Decelerate), label = "send-press")
    val fade = tween<Color>(Motion.FAST, easing = Motion.Decelerate)
    val bg by animateColorAsState(if (interrupt || on) p.btn else p.fill2, fade, label = "send-bg")
    val fg by animateColorAsState(if (interrupt || on) p.btnFg else p.ink3, fade, label = "send-fg")
    Box(
        Modifier.size(40.dp).graphicsLayer { scaleX = scale; scaleY = scale }.clip(CircleShape)
            .background(bg)
            .disabledAlpha(!interrupt || on)
            .combinedClickable(interactionSource = source, indication = LocalIndication.current, enabled = on, role = Role.Button,
                onClickLabel = if (interrupt) "中断本轮" else sendLabel,
                onClick = onClick,
                onLongClick = onLongClick)
            .semantics { contentDescription = if (interrupt) "中断本轮" else sendLabel },
        contentAlignment = Alignment.Center,
    ) {
        Crossfade(interrupt, animationSpec = Motion.fadeInSpec(Motion.FAST), label = "send-icon") { stop ->
            if (stop) Ic(Lucide.Square, 14.dp, fg) else Ic(Lucide.ArrowUp, 20.dp, fg)
        }
    }
}

/**
 * ＋ (design-system §6 输入框): a fill circle, 40. It opens the library at
 * once, or, when the computer takes files ([onFile]), a glass menu above it.
 */
@Composable
fun AttachButton(onPlus: () -> Unit, onFile: (() -> Unit)?, enabled: Boolean) {
    val p = pal
    var open by remember { mutableStateOf(false) }
    val gap = with(androidx.compose.ui.platform.LocalDensity.current) { 8.dp.roundToPx() }
    val label = if (onFile != null) "添加附件" else "添加图片"
    Box(
        Modifier.size(40.dp).disabledAlpha(enabled).clip(CircleShape).background(if (open) p.fill2 else p.fill)
            .clickable(enabled = enabled, role = Role.Button, onClickLabel = label) { if (onFile != null) open = true else onPlus() }
            .semantics { contentDescription = label },
        contentAlignment = Alignment.Center,
    ) {
        Ic(Lucide.Plus, 22.dp, p.ink)
        if (open && onFile != null) Popup(popupPositionProvider = AboveProvider(gap, right = false), onDismissRequest = { open = false }, properties = PopupProperties(focusable = true)) {
            AttachMenu(onMedia = { open = false; onPlus() }, onFile = { open = false; onFile() })
        }
    }
}

/** The ＋ menu when the computer takes files: 照片和视频, 文件, and that files go as they are. */
@Composable
private fun AttachMenu(onMedia: () -> Unit, onFile: () -> Unit) {
    val p = pal
    Column(Modifier.padding(12.dp).width(248.dp).floating(p, Radii.R24).glass(p, Radii.R24).padding(6.dp)) {
        listOf(Triple(Lucide.Image, "照片和视频", onMedia), Triple(Lucide.File, "文件", onFile)).forEachIndexed { i, (icon, label, action) ->
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
        Text(FILES_AS_THEY_ARE, style = T.s13, color = p.ink2, modifier = Modifier.padding(start = 12.dp, end = 12.dp, top = 4.dp, bottom = 8.dp))
    }
}

/** Said wherever files are offered or waiting to go. */
const val FILES_AS_THEY_ARE = "文件原样发送，包含其中的元数据。"
