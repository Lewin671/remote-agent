package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.core.animateFloat

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.disabled
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.lewin671.remoteagent.core.Status
import io.github.lewin671.remoteagent.core.projectName

/*
 * The shared pieces of docs/design/client-v7.html, sized as the mockup's CSS
 * (1 CSS px = 1 dp). What each is for is in docs/design-system.md §6.
 */

/** 1dp line at the bottom of the element, as a CSS border-bottom. */
fun Modifier.bottomLine(color: Color, startInset: Dp = 0.dp): Modifier = drawBehind {
    val y = size.height - 0.5.dp.toPx()
    drawLine(color, Offset(startInset.toPx(), y), Offset(size.width, y), 1.dp.toPx())
}

/** 1dp line at the top, as a CSS border-top, from [start] in. */
fun Modifier.topLine(color: Color, start: Dp = 0.dp): Modifier = drawBehind {
    drawLine(color, Offset(start.toPx(), 0.5.dp.toPx()), Offset(size.width, 0.5.dp.toPx()), 1.dp.toPx())
}

/** A tap target without ripple bounds changing the look; at least 44 dp in the caller. */
fun Modifier.tap(enabled: Boolean = true, role: Role? = Role.Button, onClick: () -> Unit): Modifier =
    clickable(enabled = enabled, role = role, onClick = onClick)

/** An arrow as tall as text in [style]: to the right it goes to another page; [down] / [up] open and close in place (design-system §5). */
@Composable
fun Arrow(color: Color, style: TextStyle = T.s13, down: Boolean = false, up: Boolean = false) {
    Ic(if (down) Lucide.ChevronDown else if (up) Lucide.ChevronUp else Lucide.ChevronRight, iconSize(style), color)
}

/** Text with a trailing arrow, as 处理 with one down or 管理 with one to the right. */
@Composable
fun ArrowText(text: String, color: Color, style: TextStyle = T.s13, down: Boolean = false, up: Boolean = false) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text(text, style = style, color = color, maxLines = 1)
        Spacer(Modifier.width(4.dp))
        Arrow(color, style, down, up)
    }
}

/** Text after an icon of its height, as the plus of 添加设备 or the turning circle of 正在连接…. */
@Composable
fun IconText(icon: ImageVector, text: String, color: Color, style: TextStyle = T.s13, modifier: Modifier = Modifier) {
    Row(modifier, verticalAlignment = Alignment.CenterVertically) {
        Mark(icon, color, iconSize(style))
        Spacer(Modifier.width(6.dp))
        Text(text, style = style, color = color)
    }
}

/**
 * The top of a second-level page (design-system §6 顶部): the glass back
 * button, the title in the middle (17 / 600, one line) with an optional
 * line under it, and at the right the glass ⋯ ([more]) or [right].
 */
@Composable
fun PageBar(
    title: String,
    onBack: (() -> Unit)?,
    subtitle: String? = null,
    more: (() -> Unit)? = null,
    right: (@Composable () -> Unit)? = null,
    subtitleColor: Color? = null,
) {
    val p = pal
    Row(
        Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 6.dp, bottom = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Box(Modifier.widthIn(min = 44.dp)) { if (onBack != null) GlassButton(Lucide.ChevronLeft, "返回", onBack) }
        Column(Modifier.weight(1f), horizontalAlignment = Alignment.CenterHorizontally) {
            if (title.isNotEmpty()) Text(title, style = T.s17b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (subtitle != null) Text(subtitle, style = T.s12, color = subtitleColor ?: p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        Box(Modifier.widthIn(min = 44.dp), contentAlignment = Alignment.CenterEnd) {
            if (right != null) right()
            else if (more != null) GlassButton(Lucide.Ellipsis, "更多", more)
        }
    }
}

/** A first-level page title: 设置, 34 / 700 (design-system §6 大标题). */
@Composable
fun LargeTitle(text: String, modifier: Modifier = Modifier) {
    Text(text, style = T.title, color = pal.ink, modifier = modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, top = 6.dp, bottom = 6.dp))
}

/** A section label: 13 semibold muted, a count or one link at the right. */
@Composable
fun Label(text: String, count: Int? = null, modifier: Modifier = Modifier, action: String? = null, onAction: (() -> Unit)? = null) {
    val p = pal
    if (action != null && onAction != null) return LabelWithLink(text, action, onAction)
    Row(modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, top = 14.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text, style = T.s13, color = p.ink2, modifier = Modifier.weight(1f))
        if (count != null) Text("$count", style = T.s13, color = p.ink2)
    }
}

/**
 * The same with the link's 44 dp target drawn over the padding, so the row
 * keeps its 13 dp height as in the mockup.
 */
@Composable
fun LabelWithLink(text: String, action: String, onAction: () -> Unit) {
    val p = pal
    Box(Modifier.fillMaxWidth()) {
        Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 14.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(text, style = T.s13b, color = p.ink2, modifier = Modifier.weight(1f))
            ArrowText(action, p.blue, T.s13b)
        }
        Box(Modifier.align(Alignment.CenterEnd).width(88.dp).height(44.dp).tap { onAction() })
    }
}

/** A row of buttons sharing the width, 8 dp apart. */
@Composable
fun Btns(modifier: Modifier = Modifier, content: @Composable RowScope.() -> Unit) {
    Row(modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp), content = content)
}

/** Set on grey (soft) pages, where notices and code use the card surface instead (design-system §2). */
val LocalOnSoft = androidx.compose.runtime.staticCompositionLocalOf { false }

/** A text action: blue, or red for 中断; 44 dp tall. One that goes to another page ends in an [arrow]. */
@Composable
fun TextAction(text: String, onClick: () -> Unit, modifier: Modifier = Modifier, color: Color? = null, style: TextStyle = T.s13b, enabled: Boolean = true, alignment: Alignment = Alignment.CenterStart, arrow: Boolean = false) {
    Box(modifier.heightIn(min = 44.dp).widthIn(min = 44.dp).clickable(enabled = enabled, role = Role.Button, onClick = onClick).alpha(if (enabled) 1f else .45f), contentAlignment = alignment) {
        if (arrow) ArrowText(text, color ?: pal.blue, style) else Text(text, style = style, color = color ?: pal.blue, maxLines = 1)
    }
}

/** A grouped list (design-system §6 分组圆角列表): rows on one card, radius 20, 16 in from the sides, thin lines between them; paper on a grey page, gbg on paper. */
@Composable
fun Group(modifier: Modifier = Modifier, rows: List<@Composable () -> Unit>) {
    val p = pal
    Column(modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, bottom = 12.dp).clip(Radii.R20).background(if (LocalOnSoft.current) p.paper else p.gbg)) {
        rows.forEachIndexed { i, r ->
            Box(if (i > 0) Modifier.topLine(p.sep, 18.dp) else Modifier) { r() }
        }
    }
}

/**
 * A row in a group: 15 with a value at the right. A row that goes somewhere
 * has a [chevron], after its value, or at its end without one; one that does
 * something is blue ([action]) or red ([danger]), its chevron right after
 * the text and an [icon] such as a plus before it. [singleLineValue] keeps compact
 * settings on one line, including at large font sizes, and truncates the trailing value.
 */
@Composable
fun GRow(
    text: String,
    value: String? = null,
    onClick: (() -> Unit)? = null,
    chevron: Boolean = false,
    action: Boolean = false,
    danger: Boolean = false,
    enabled: Boolean = true,
    valueColor: Color? = null,
    lead: (@Composable () -> Unit)? = null,
    trailing: (@Composable () -> Unit)? = null,
    icon: ImageVector? = null,
    singleLineValue: Boolean = false,
) {
    val p = pal
    val ink = if (danger) p.red else if (action) p.blue else if (enabled) p.ink else p.ink2
    if (value != null && !singleLineValue && androidx.compose.ui.platform.LocalDensity.current.fontScale >= 1.8f) {
        Column(Modifier.fillMaxWidth().heightIn(min = 50.dp).then(if (onClick != null) Modifier.clickable(enabled = enabled, onClick = onClick) else Modifier)
            .alpha(if (enabled) 1f else .6f).padding(start = 18.dp, end = 16.dp, top = 10.dp, bottom = 10.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (lead != null) { lead(); Spacer(Modifier.width(10.dp)) }
                Text(text, style = T.s17, color = ink, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                trailing?.invoke()
            }
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp, Alignment.End)) {
                Text(value, style = T.s17, color = valueColor ?: p.ink2, textAlign = TextAlign.End, modifier = Modifier.weight(1f, fill = false))
                if (chevron) Arrow(p.ink3, T.s15)
            }
        }
        return
    }
    Row(
        Modifier.fillMaxWidth().heightIn(min = 50.dp).then(if (onClick != null) Modifier.clickable(enabled = enabled, onClick = onClick) else Modifier)
            .alpha(if (enabled) 1f else .6f).padding(start = 18.dp, end = 16.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        lead?.invoke()
        Row(Modifier.weight(1f).padding(vertical = 11.dp), verticalAlignment = Alignment.CenterVertically) {
            if (icon != null) {
                Ic(icon, iconSize(T.s17), ink)
                Spacer(Modifier.width(6.dp))
            }
            Text(text, style = T.s17, color = ink, maxLines = if (singleLineValue) 1 else Int.MAX_VALUE, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
            if (chevron && action) {
                Spacer(Modifier.width(4.dp))
                Arrow(ink, T.s17)
            }
        }
        if (value != null) Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
            Text(value, style = T.s17, color = valueColor ?: p.ink2, textAlign = TextAlign.End, maxLines = if (singleLineValue) 1 else 2, overflow = if (singleLineValue) TextOverflow.Ellipsis else TextOverflow.Clip, modifier = Modifier.widthIn(max = 220.dp))
            if (chevron) Arrow(p.ink3, T.s15)
        }
        trailing?.invoke()
        if (chevron && value == null && !action) Arrow(p.ink3, T.s15)
    }
}

/** A small title above a group, 13 ink2. */
@Composable
fun GLabel(text: String, action: String? = null, onAction: (() -> Unit)? = null) {
    val p = pal
    Row(Modifier.fillMaxWidth().padding(start = 28.dp, end = 28.dp, top = 6.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text, style = T.s13, color = p.ink2, modifier = Modifier.weight(1f))
        if (action != null) Box(Modifier.heightIn(min = 32.dp).tap { onAction?.invoke() }, contentAlignment = Alignment.Center) { ArrowText(action, p.blue, T.s13) }
    }
}

/** The grab handle and title row of a bottom sheet. */
@Composable
fun SheetHead(title: String, action: String? = null, onAction: (() -> Unit)? = null, onBack: (() -> Unit)? = null) {
    val p = pal
    Row(Modifier.fillMaxWidth().padding(bottom = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        if (onBack != null) Box(Modifier.width(28.dp).height(44.dp).tap { onBack() }, contentAlignment = Alignment.CenterStart) { Ic(Lucide.ChevronLeft, 22.dp, p.blue, contentDescription = "返回") }
        Text(title, style = T.s17b, color = p.ink, modifier = Modifier.weight(1f))
        // As wide as its words: 完成 and 取消 fit in 44 dp, 以后再说 does not.
        if (action != null) Box(Modifier.heightIn(min = 44.dp).widthIn(min = 44.dp).tap { onAction?.invoke() }, contentAlignment = Alignment.Center) { Text(action, style = T.s15, color = p.blue, maxLines = 1) }
    }
}

@Composable
private fun Grab() {
    Box(Modifier.fillMaxWidth().padding(top = 10.dp, bottom = 12.dp), contentAlignment = Alignment.Center) {
        Box(Modifier.width(36.dp).height(5.dp).clip(RoundedCornerShape(3.dp)).background(pal.fill2))
    }
}

/** A bottom sheet: card, top corners 20, grab handle, 16 dp sides (design-system §6). */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun Sheet(onClose: () -> Unit, scroll: Boolean = true, canDismiss: Boolean = true, color: Color? = null, content: @Composable ColumnScope.() -> Unit) {
    val p = pal
    ModalBottomSheet(
        onDismissRequest = onClose,
        sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true, confirmValueChange = { canDismiss || it != androidx.compose.material3.SheetValue.Hidden }),
        containerColor = color ?: p.paper,
        shape = Radii.sheet,
        dragHandle = { Grab() },
        scrimColor = Color.Black.copy(alpha = .35f),
        tonalElevation = 0.dp,
    ) {
        Column(
            Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, bottom = 24.dp).then(if (scroll) Modifier.verticalScroll(rememberScrollState()) else Modifier),
            content = content,
        )
    }
}

/** 13 grey caption under a group or a sheet's content. */
@Composable
fun Hint(text: String, modifier: Modifier = Modifier, color: Color? = null) {
    Text(text, style = T.s13, color = color ?: pal.ink2, modifier = modifier.padding(top = 8.dp))
}

@Composable
fun FieldLabel(text: String, modifier: Modifier = Modifier) {
    Text(text, style = T.s13, color = pal.ink2, modifier = modifier.padding(top = 12.dp, bottom = 6.dp))
}

/** A choice in a sheet's list: title, optional description, a check when chosen; [icon] goes before the title. */
@Composable
fun ListItem(title: String, desc: String? = null, checked: Boolean = false, onClick: (() -> Unit)?, enabled: Boolean = true, danger: Boolean = false, small: String? = null, last: Boolean = false, trailing: (@Composable () -> Unit)? = null, icon: ImageVector? = null) {
    val p = pal
    val ink = if (!enabled) p.ink2 else if (danger) p.red else p.ink
    Row(
        Modifier.fillMaxWidth().heightIn(min = 50.dp).then(if (!last) Modifier.bottomLine(p.sep) else Modifier)
            .then(if (onClick != null) Modifier.clickable(enabled = enabled, onClick = onClick) else Modifier).padding(horizontal = 2.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Column(Modifier.weight(1f).padding(vertical = 8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (icon != null) {
                    Ic(icon, iconSize(T.s17), ink)
                    Spacer(Modifier.width(4.dp))
                }
                Text(title, style = if (desc != null) T.s17b else T.s17, color = ink)
                if (small != null) Text(small, style = T.s13, color = p.ink2, modifier = Modifier.padding(start = 4.dp))
            }
            if (desc != null) Text(desc, style = T.s13, color = p.ink2)
        }
        trailing?.invoke()
        if (checked) Mark(Lucide.Check, p.blue, 16.dp)
    }
}

/** A dialog (design-system §6 对话框): gbg, radius 30, floating; 取消 at the left, the confirmation at the right, ink or red. */
@Composable
fun Alert(title: String, text: String, confirm: String, danger: Boolean = false, onDismiss: () -> Unit, onConfirm: () -> Unit) {
    DialogCard(onDismiss) {
        Text(title, style = T.s17b, color = pal.ink, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(bottom = 6.dp))
        Text(text, style = T.s15, color = pal.ink2, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(bottom = 18.dp))
        Btns {
            PillButton("取消", onDismiss, Modifier.weight(1f))
            PillButton(confirm, { onDismiss(); onConfirm() }, Modifier.weight(1f), if (danger) ButtonKind.DANGER else ButtonKind.INK)
        }
    }
}

/** The shell of a dialog: centred, at most 340 wide, gbg, radius 30, the float shadow. */
@Composable
fun DialogCard(onDismiss: () -> Unit, content: @Composable ColumnScope.() -> Unit) {
    val p = pal
    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Box(Modifier.fillMaxWidth().padding(horizontal = 28.dp).imePadding(), contentAlignment = Alignment.Center) {
            Column(Modifier.widthIn(max = 340.dp).fillMaxWidth().floating(p, Radii.R30).clip(Radii.R30).background(p.gbg).padding(22.dp), content = content)
        }
    }
}

/**
 * One choice of an action sheet. Not [enabled]: greyed and not tappable.
 * [note]: a small line under it, why it is not available or ([noteError])
 * how it failed. [busy]: it is running, shown with a spinner; the other
 * choices are then given not enabled and the sheet stays until it ends.
 * [keepOpen]: tapping it leaves the sheet open.
 */
class SheetAction(
    val text: String,
    val danger: Boolean = false,
    val warn: Boolean = false,
    val enabled: Boolean = true,
    val note: String = "",
    val noteError: Boolean = false,
    val busy: Boolean = false,
    val keepOpen: Boolean = false,
    val value: String? = null,
    val onClick: () -> Unit,
)

/**
 * The ⋯ menu (design-system §6 弹层与对话框): a gbg bottom sheet with [info]
 * at the top, what it is about, then each of [groups] as a rounded list of
 * its own. No centred buttons and no separate 取消.
 */
@Composable
fun ActionSheet(info: (@Composable () -> Unit)?, groups: List<List<SheetAction>>, onDismiss: () -> Unit) {
    val p = pal
    val busy = groups.any { g -> g.any { it.busy } }
    Sheet(onClose = { if (!busy) onDismiss() }, canDismiss = !busy, color = p.gbg) {
        if (info != null) Box(Modifier.fillMaxWidth().padding(start = 8.dp, end = 8.dp, bottom = 16.dp), contentAlignment = Alignment.CenterStart) { info() }
        groups.filter { it.isNotEmpty() }.forEachIndexed { gi, g ->
            Column(Modifier.fillMaxWidth().padding(top = if (gi > 0) 12.dp else 0.dp).clip(Radii.R20).background(p.raised)) {
                g.forEachIndexed { i, a -> ActionSheetRow(a, first = i == 0) { if (!a.keepOpen) onDismiss(); a.onClick() } }
            }
        }
    }
}

/** The top of an [ActionSheet]: what it is about in bold, and a second line ([mono] for a path). */
@Composable
fun SheetInfo(title: String, sub: String, mono: Boolean = false) {
    val p = pal
    Column(horizontalAlignment = Alignment.Start) {
        Text(title, style = T.s17b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (sub.isNotEmpty()) Text(sub, style = if (mono) T.mono12.copy(fontSize = 13.sp) else T.s13, color = p.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
    }
}

/** One row of an [ActionSheet] group: 17, its value at the end in ink2 and a chevron; a thin line above all but the [first]. */
@Composable
internal fun ActionSheetRow(a: SheetAction, first: Boolean = true, onClick: () -> Unit) {
    val p = pal
    val on = a.enabled && !a.busy
    Column(
        Modifier.fillMaxWidth().heightIn(min = 50.dp).then(if (first) Modifier else Modifier.topLine(p.sep, 18.dp))
            .clickable(enabled = on, onClick = onClick).padding(start = 18.dp, end = 16.dp, top = 12.dp, bottom = 12.dp),
        horizontalAlignment = Alignment.Start,
        verticalArrangement = Arrangement.Center,
    ) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            if (a.busy) Spinner(p.ink2, size = 15.dp)
            Text(a.text, style = T.s17, color = if (!a.enabled) p.ink3 else if (a.danger && a.value == null) p.red else p.ink, modifier = Modifier.weight(1f))
            if (a.value != null) {
                Text(a.value, style = T.s17, color = if (a.danger) p.red else if (a.warn) p.orangeInk else p.ink2,
                    textAlign = TextAlign.End, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.widthIn(max = 220.dp))
                Arrow(p.ink3, T.s15)
            }
        }
        if (a.note.isNotEmpty()) Text(a.note, style = T.s13, color = if (a.noteError) p.red else p.ink2, modifier = Modifier.padding(top = 2.dp))
    }
}

/** A code or command block outside the chat: gbg, radius 14, mono 13 ink2 (design-system §6 代码块). */
@Composable
fun Code(text: String, modifier: Modifier = Modifier, maxLines: Int = Int.MAX_VALUE, bg: Color? = null) {
    val p = pal
    Box(modifier.fillMaxWidth().clip(Radii.R14).background(bg ?: if (LocalOnSoft.current) p.paper else p.gbg).padding(horizontal = 14.dp, vertical = 10.dp)) {
        Text(text, style = T.mono13, color = p.ink2, maxLines = maxLines, overflow = TextOverflow.Ellipsis)
    }
}

/** A field's box: fill, radius 14, at least 48 tall (design-system §2 fill). */
@Composable
fun FieldBox(modifier: Modifier = Modifier, content: @Composable BoxScope.() -> Unit) {
    Box(modifier.fillMaxWidth().heightIn(min = 48.dp).clip(Radii.R14).background(pal.fill).padding(horizontal = 14.dp, vertical = 12.dp), contentAlignment = Alignment.CenterStart, content = content)
}

/** A page: navigation bar and content; card background, or soft for grouped settings pages ([gray]). */
@Composable
fun Page(
    title: String,
    onBack: (() -> Unit)?,
    gray: Boolean = false,
    subtitle: String? = null,
    more: (() -> Unit)? = null,
    right: (@Composable () -> Unit)? = null,
    content: @Composable ColumnScope.() -> Unit,
) {
    val p = pal
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides gray) {
        Column(Modifier.fillMaxSize().background(if (gray) p.gbg else p.paper).imePadding()) {
            PageBar(title, onBack, subtitle = subtitle, more = more, right = right)
            content()
        }
    }
}

internal val WIDE_WINDOW_MIN_WIDTH = 840.dp

@Composable
fun InputAlert(title: String, initial: String, placeholder: String, confirm: String, onDismiss: () -> Unit, dismiss: String = "取消", text: String? = null, onConfirm: (String) -> Unit) {
    var value by remember(initial) { androidx.compose.runtime.mutableStateOf(initial) }
    DialogCard(onDismiss) {
        Text(title, style = T.s17b, color = pal.ink, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(bottom = 14.dp))
        InputField(value, { value = it.take(40) }, placeholder)
        if (text != null) Text(text, style = T.s13, color = pal.ink2, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(top = 4.dp))
        Btns(Modifier.padding(top = 14.dp)) {
            PillButton(dismiss, onDismiss, Modifier.weight(1f))
            PillButton(confirm, { onDismiss(); onConfirm(value) }, Modifier.weight(1f), ButtonKind.INK)
        }
    }
}

/**
 * A status mark (design-system §5): check for done, x for failed, minus for
 * canceled, and loader-circle for in progress, which turns once in 0.9 s.
 * With the system's animations off the transition does not run and it
 * stays still.
 */
@Composable
fun Mark(icon: ImageVector, color: Color, size: Dp = 12.dp) {
    if (icon !== Lucide.LoaderCircle) return Ic(icon, size, color)
    val transition = androidx.compose.animation.core.rememberInfiniteTransition(label = "spinner")
    val angle by transition.animateFloat(0f, 360f,
        androidx.compose.animation.core.infiniteRepeatable(androidx.compose.animation.core.tween(900, easing = androidx.compose.animation.core.LinearEasing)),
        label = "rotation")
    Ic(icon, size, color, Modifier.rotate(angle))
}
