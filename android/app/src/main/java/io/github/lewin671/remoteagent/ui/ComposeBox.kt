package io.github.lewin671.remoteagent.ui

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

/** Places the send menu above the composer, aligned with its send button. */
private class RightAboveProvider(private val gap: Int) : PopupPositionProvider {
    override fun calculatePosition(anchorBounds: IntRect, windowSize: IntSize, layoutDirection: LayoutDirection, popupContentSize: IntSize): IntOffset =
        IntOffset((anchorBounds.right - popupContentSize.width).coerceIn(0, (windowSize.width - popupContentSize.width).coerceAtLeast(0)),
            (anchorBounds.top - popupContentSize.height - gap).coerceAtLeast(0))
}

/**
 * The shared message box: a plus to add images, one to six lines of text, and the
 * send position. In a running session that position becomes interrupt when
 * empty; long-press send offers queue when supported.
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
    var attachMenu by remember { mutableStateOf(false) }
    var sendMenu by remember { mutableStateOf(false) }
    val gap = with(androidx.compose.ui.platform.LocalDensity.current) { 8.dp.roundToPx() }
    Box(modifier.fillMaxWidth().padding(start = 10.dp, end = 10.dp, top = 4.dp, bottom = 12.dp)) {
        Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(24.dp)).background(p.card).border(1.dp, p.line, RoundedCornerShape(24.dp)).padding(3.dp)) {
            top?.invoke()
            Row(verticalAlignment = Alignment.Bottom) {
                Box(
                    Modifier.size(44.dp).alpha(if (plusEnabled) 1f else .4f)
                        .clickable(enabled = plusEnabled, role = Role.Button, onClickLabel = if (onFile != null) "添加附件" else "添加图片") { if (onFile != null) attachMenu = true else onPlus() }
                        .semantics { contentDescription = if (onFile != null) "添加附件" else "添加图片" },
                    contentAlignment = Alignment.Center,
                ) {
                    Ic(Lucide.Plus, 22.dp, p.muted)
                    androidx.compose.material3.DropdownMenu(expanded = attachMenu && onFile != null, onDismissRequest = { attachMenu = false }) {
                        androidx.compose.material3.DropdownMenuItem(text = { Text("图片") }, onClick = { attachMenu = false; onPlus() })
                        androidx.compose.material3.DropdownMenuItem(text = { Text("文件") }, onClick = { attachMenu = false; onFile?.invoke() })
                    }
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
                val interrupt = !hasDraft && onInterrupt != null
                Box(
                    Modifier.size(44.dp).combinedClickable(enabled = if (interrupt) canInterrupt else canSend, role = Role.Button,
                        onClickLabel = if (interrupt) "中断本轮" else sendLabel,
                        onClick = { if (interrupt) onInterrupt?.invoke() else onSend() },
                        onLongClick = if (!interrupt && onQueue != null) ({ sendMenu = true }) else null)
                        .semantics { contentDescription = if (interrupt) "中断本轮" else sendLabel },
                    contentAlignment = Alignment.Center,
                ) {
                    Box(Modifier.size(32.dp).alpha(if (if (interrupt) canInterrupt else canSend) 1f else .4f).clip(CircleShape).background(if (interrupt) p.chip else p.blueBg), contentAlignment = Alignment.Center) {
                        if (interrupt) Ic(Lucide.Square, 14.dp, p.red) else Ic(Lucide.ArrowUp, 20.dp, Color.White)
                    }
                }
            }
        }
        if (sendMenu) Popup(popupPositionProvider = RightAboveProvider(gap), onDismissRequest = { sendMenu = false }, properties = PopupProperties(focusable = true)) {
            Column(Modifier.width(IntrinsicSize.Max).widthIn(min = 190.dp, max = 250.dp).shadow(8.dp, Radii.SM).clip(Radii.SM).background(p.card).border(1.dp, p.line, Radii.SM).padding(8.dp)) {
                for ((label, detail, action) in listOf(
                    Triple("现在插入这一轮", "点 ↑ 就是这个", onSend),
                    Triple("这一轮结束后再发", "先排队，结束后按顺序发出", { onQueue?.invoke() }),
                )) {
                    Column(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { sendMenu = false; action() }, verticalArrangement = Arrangement.Center) {
                        Text(label, style = T.smB, color = p.fg)
                        Text(detail, style = T.meta, color = p.muted)
                    }
                }
            }
        }
    }
}
