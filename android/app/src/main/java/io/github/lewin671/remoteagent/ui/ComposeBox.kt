package io.github.lewin671.remoteagent.ui

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
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.IntRect
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Popup
import androidx.compose.ui.window.PopupPositionProvider
import androidx.compose.ui.window.PopupProperties

/** An entry of the ＋ menu. */
class MenuEntry(val text: String, val enabled: Boolean = true, val onClick: () -> Unit)

/** Places the ＋ menu 8 dp above the message box, at its left edge. */
private class AboveProvider(private val gap: Int) : PopupPositionProvider {
    override fun calculatePosition(anchorBounds: IntRect, windowSize: IntSize, layoutDirection: LayoutDirection, popupContentSize: IntSize): IntOffset =
        IntOffset(anchorBounds.left, (anchorBounds.top - popupContentSize.height - gap).coerceAtLeast(0))
}

/**
 * The one message box of client-v7, for a session and for a new one: ＋,
 * the text from one line up to about six, and send; the settings wait in
 * the ＋ menu. [warning] stays in sight (完全放行); [contextHigh] puts an
 * orange dot on ＋. [top] is what goes above the text, such as the images.
 */
@Composable
fun ComposeBox(
    text: String,
    onText: (String) -> Unit,
    placeholder: String,
    canSend: Boolean,
    onSend: () -> Unit,
    menu: List<MenuEntry>,
    modifier: Modifier = Modifier,
    menuEnabled: Boolean = true,
    contextHigh: Boolean = false,
    warning: String? = null,
    sendLabel: String = "发送",
    focusRequester: FocusRequester? = null,
    onFocus: (Boolean) -> Unit = {},
    top: (@Composable () -> Unit)? = null,
) {
    val p = pal
    var open by remember { mutableStateOf(false) }
    val gap = with(androidx.compose.ui.platform.LocalDensity.current) { 8.dp.roundToPx() }
    Box(modifier.fillMaxWidth().padding(start = 10.dp, end = 10.dp, top = 4.dp, bottom = 12.dp)) {
        Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(24.dp)).background(p.card).border(1.dp, p.line, RoundedCornerShape(24.dp)).padding(3.dp)) {
            top?.invoke()
            Row(verticalAlignment = Alignment.Bottom) {
                Box(
                    Modifier.size(44.dp).alpha(if (menuEnabled) 1f else .4f)
                        .clickable(enabled = menuEnabled, role = Role.Button, onClickLabel = "附件与会话设置") { open = !open }
                        .semantics { contentDescription = "附件与会话设置" },
                    contentAlignment = Alignment.Center,
                ) {
                    Text("＋", style = ts(22.sp, lineHeight = 1f), color = p.muted)
                    if (contextHigh) Box(Modifier.align(Alignment.TopStart).offset(32.dp, 12.dp).size(5.dp).clip(CircleShape).background(p.orange))
                }
                BasicTextField(
                    text, onText,
                    Modifier.weight(1f).heightIn(min = 44.dp).padding(horizontal = 4.dp, vertical = 10.dp)
                        .then(if (focusRequester != null) Modifier.focusRequester(focusRequester) else Modifier)
                        .onFocusChanged { onFocus(it.isFocused) },
                    textStyle = T.body.copy(color = p.fg),
                    cursorBrush = SolidColor(p.blue),
                    maxLines = 6,
                    decorationBox = { inner ->
                        Box {
                            if (text.isEmpty()) Text(placeholder, style = T.body, color = p.muted, maxLines = 1)
                            inner()
                        }
                    },
                )
                Box(
                    Modifier.size(44.dp).clickable(enabled = canSend, role = Role.Button, onClickLabel = sendLabel, onClick = onSend)
                        .semantics { contentDescription = sendLabel },
                    contentAlignment = Alignment.Center,
                ) {
                    Box(Modifier.size(32.dp).alpha(if (canSend) 1f else .4f).clip(CircleShape).background(p.blueBg), contentAlignment = Alignment.Center) {
                        Text("↑", style = ts(16.sp, FontWeight.SemiBold, 1f), color = Color.White)
                    }
                }
            }
            if (warning != null) Text(warning, style = T.meta, color = p.red, modifier = Modifier.padding(start = 12.dp, end = 12.dp, bottom = 6.dp))
        }
        if (open) Popup(popupPositionProvider = AboveProvider(gap), onDismissRequest = { open = false }, properties = PopupProperties(focusable = true)) {
            Column(
                Modifier.width(IntrinsicSize.Max).widthIn(min = 160.dp, max = 300.dp).shadow(8.dp, Radii.SM).clip(Radii.SM).background(p.card).border(1.dp, p.line, Radii.SM)
                    .padding(horizontal = 12.dp, vertical = 6.dp),
            ) {
                for (e in menu) Box(
                    Modifier.heightIn(min = 44.dp).fillMaxWidth().clickable(enabled = e.enabled) { open = false; e.onClick() }.alpha(if (e.enabled) 1f else .45f),
                    contentAlignment = Alignment.CenterStart,
                ) { Text(e.text, style = T.sm, color = p.fg) }
            }
        }
    }
}

/**
 * The line above the message box: the queue and why it waits, and 中断,
 * which stays in the same place with or without a draft.
 */
@Composable
fun ComposeStatus(queue: String?, why: String?, onQueue: () -> Unit, interrupt: String?, onInterrupt: () -> Unit, interruptEnabled: Boolean = true) {
    val p = pal
    Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).padding(horizontal = 14.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        if (queue != null) Box(Modifier.heightIn(min = 44.dp).clickable(onClick = onQueue).padding(end = 4.dp), contentAlignment = Alignment.Center) {
            ArrowText(queue, p.blue, T.meta)
        }
        if (why != null) Text(why, style = T.meta, color = p.muted, maxLines = 1, modifier = Modifier.weight(1f, fill = false))
        Box(Modifier.weight(1f))
        if (interrupt != null) Box(
            Modifier.heightIn(min = 44.dp).widthIn(min = 44.dp).clickable(enabled = interruptEnabled, role = Role.Button, onClick = onInterrupt).alpha(if (interruptEnabled) 1f else .45f),
            contentAlignment = Alignment.Center,
        ) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.size(8.dp).clip(RoundedCornerShape(1.dp)).background(p.red))
                Text(" $interrupt", style = T.sm, color = p.red)
            }
        }
    }
}
