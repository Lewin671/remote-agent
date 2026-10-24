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
import io.github.lewin671.remoteagent.core.avatarIndex
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

/** 1dp line at the top, as a CSS border-top. */
fun Modifier.topLine(color: Color): Modifier = drawBehind {
    drawLine(color, Offset(0f, 0.5.dp.toPx()), Offset(size.width, 0.5.dp.toPx()), 1.dp.toPx())
}

/** A tap target without ripple bounds changing the look; at least 44 dp in the caller. */
fun Modifier.tap(enabled: Boolean = true, role: Role? = Role.Button, onClick: () -> Unit): Modifier =
    clickable(enabled = enabled, role = role, onClick = onClick)

@Composable
fun Dot(color: Color, size: Int = 8) {
    Box(Modifier.size(size.dp).clip(CircleShape).background(color))
}

/** An arrow as tall as text in [style]: to the right it goes to another page; [down] / [up] open and close in place (design-system §5). */
@Composable
fun Arrow(color: Color, style: TextStyle = T.sm, down: Boolean = false, up: Boolean = false) {
    Ic(if (down) Lucide.ChevronDown else if (up) Lucide.ChevronUp else Lucide.ChevronRight, iconSize(style), color)
}

/** Text with a trailing arrow, as 处理 with one down or 管理 with one to the right. */
@Composable
fun ArrowText(text: String, color: Color, style: TextStyle = T.sm, down: Boolean = false, up: Boolean = false) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text(text, style = style, color = color, maxLines = 1)
        Spacer(Modifier.width(4.dp))
        Arrow(color, style, down, up)
    }
}

/** Text after an icon of its height, as the plus of 添加设备 or the turning circle of 正在连接…. */
@Composable
fun IconText(icon: ImageVector, text: String, color: Color, style: TextStyle = T.sm, modifier: Modifier = Modifier) {
    Row(modifier, verticalAlignment = Alignment.CenterVertically) {
        Mark(icon, color, iconSize(style))
        Spacer(Modifier.width(6.dp))
        Text(text, style = style, color = color)
    }
}

/** A project's letter on a gradient picked by its name, as on the web: 40 on lists, 22 in sheets, 52 on the project page, 16 in chips. */
@Composable
fun Avatar(key: String, size: Int = 40) {
    val (radius, font) = when {
        size >= 52 -> 15 to 22
        size >= 36 -> 11 to 17
        size >= 22 -> 6 to 12
        else -> 4 to 9
    }
    Box(
        Modifier.size(size.dp).clip(RoundedCornerShape(radius.dp)).background(avatarGradients[avatarIndex(projectName(key)) % avatarGradients.size]),
        contentAlignment = Alignment.Center,
    ) {
        Text(projectName(key).take(1).lowercase(), color = Color.White, style = ts(font.sp, FontWeight.Bold, 1.2f))
    }
}

/**
 * The navigation bar: back and more in 44 dp, the title centered with the CLI
 * beside it, and a second line for the status, project and machine.
 */
@Composable
fun NavBar(
    title: String,
    onBack: (() -> Unit)?,
    agent: String? = null,
    subtitle: String? = null,
    dot: Color? = null,
    more: (() -> Unit)? = null,
    dark: Boolean = false,
    right: (@Composable () -> Unit)? = null,
    subtitleColor: Color? = null,
    subtitleRich: androidx.compose.ui.text.AnnotatedString? = null,
    moreDot: Boolean = false,
) {
    val p = pal
    val link = if (dark) TermColors.link else p.blue
    Row(Modifier.fillMaxWidth().padding(start = 4.dp, end = 4.dp, bottom = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(44.dp).then(if (onBack != null) Modifier.tap { onBack() } else Modifier), contentAlignment = Alignment.Center) {
            if (onBack != null) Ic(Lucide.ChevronLeft, 22.dp, link, contentDescription = "返回")
        }
        Column(Modifier.weight(1f), horizontalAlignment = Alignment.CenterHorizontally) {
            if (title.isNotEmpty()) Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                Text(title, style = T.bodyB, color = if (dark) TermColors.fg else p.fg, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                if (agent != null) Text(agent, style = ts(12.sp, FontWeight.Medium), color = if (dark) TermColors.dim else p.muted, maxLines = 1)
            }
            if (subtitle != null) Row(verticalAlignment = Alignment.CenterVertically) {
                if (dot != null) {
                    Dot(dot)
                    Spacer(Modifier.width(5.dp))
                }
                if (subtitleRich != null) Text(subtitleRich, style = T.meta, color = if (dark) TermColors.dim else p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
                else Text(subtitle, style = T.meta, color = subtitleColor ?: if (dark) TermColors.dim else p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
        Box(Modifier.size(44.dp).then(if (more != null) Modifier.tap { more() } else Modifier), contentAlignment = Alignment.Center) {
            if (right != null) right()
            else if (more != null) Ic(Lucide.Ellipsis, 22.dp, link, contentDescription = "更多")
            if (moreDot) Box(Modifier.align(Alignment.TopEnd).offset((-6).dp, 8.dp).size(5.dp).clip(CircleShape).background(p.orange))
        }
    }
}

/** A navigation bar with a status line for a session or tool page. */
@Composable
fun StatusNavBar(title: String, agent: String?, status: Status, rest: String, onBack: () -> Unit, more: (() -> Unit)?, dark: Boolean = false) {
    val p = pal
    NavBar(title, onBack, agent, status.text + if (rest.isNotEmpty()) " · $rest" else "", p.tone(status.tone), more, dark)
}

/** A first-level page title: 设置. The home page's title is its scope (ScopeTitle in Home.kt). */
@Composable
fun LargeTitle(text: String, modifier: Modifier = Modifier, action: (() -> Unit)? = null, actionLabel: String = "新会话") {
    val p = pal
    Row(modifier.fillMaxWidth().padding(start = 16.dp, end = 12.dp, top = 4.dp, bottom = 2.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text, style = if (androidx.compose.ui.platform.LocalConfiguration.current.screenWidthDp >= 840) T.h22 else T.title30, color = p.fg, modifier = Modifier.weight(1f).padding(vertical = 3.dp))
        if (action != null) NewSessionButton(action, actionLabel)
    }
}

/** The new-session button at the right of a first-level title: a pencil on a sheet, 44 dp. */
@Composable
fun NewSessionButton(onClick: () -> Unit, label: String = "新会话") {
    val p = pal
    Box(
        Modifier.size(44.dp).tap { onClick() }.semantics { contentDescription = label },
        contentAlignment = Alignment.Center,
    ) {
        Ic(Lucide.SquarePen, 22.dp, p.blue)
    }
}

/** A section label: 13 semibold muted, a count or one link at the right. */
@Composable
fun Label(text: String, count: Int? = null, modifier: Modifier = Modifier, action: String? = null, onAction: (() -> Unit)? = null) {
    val p = pal
    if (action != null && onAction != null) return LabelWithLink(text, action, onAction)
    Row(modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 14.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text, style = T.smB, color = p.muted, modifier = Modifier.weight(1f))
        if (count != null) Text("$count", style = T.sm, color = p.muted)
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
            Text(text, style = T.smB, color = p.muted, modifier = Modifier.weight(1f))
            ArrowText(action, p.blue, T.smB)
        }
        Box(Modifier.align(Alignment.CenterEnd).width(88.dp).height(44.dp).tap { onAction() })
    }
}

/**
 * Tabs that switch a view (聊天 / 终端): underlined, on phones and wide
 * screens alike (design-system §6). [dot] marks a tab that needs you;
 * [inset] is the space at the sides, none inside a sheet.
 */
@Composable
fun ViewTabs(items: List<String>, selected: Int, onSelect: (Int) -> Unit, enabled: (Int) -> Boolean = { true }, dot: Int = -1, dark: Boolean = false, modifier: Modifier = Modifier, inset: androidx.compose.ui.unit.Dp = 16.dp) {
    val p = pal
    val line = if (dark) TermColors.line else p.line
    Row(modifier.fillMaxWidth().padding(start = inset, end = inset, bottom = 4.dp).bottomLine(line).selectableGroup()) {
        items.forEachIndexed { i, title ->
            val on = i == selected
            val ok = enabled(i)
            val under = if (dark) TermColors.fg else p.fg2
            Row(
                Modifier.weight(1f).heightIn(min = 44.dp)
                    .then(if (on) Modifier.drawBehind { drawLine(under, Offset(0f, size.height - 1.dp.toPx()), Offset(size.width, size.height - 1.dp.toPx()), 2.dp.toPx()) } else Modifier)
                    .selectable(selected = on, enabled = ok, role = Role.Tab, onClick = { onSelect(i) })
                    .alpha(if (ok) 1f else .4f),
                verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.Center,
            ) {
                Text(title, style = if (on) T.smB else T.sm, color = if (dark) (if (on) TermColors.fg else TermColors.dim) else if (on) p.fg else p.muted)
                if (i == dot) {
                    Spacer(Modifier.width(3.dp))
                    Dot(p.orange)
                }
            }
        }
    }
}

enum class BtnKind { PRIMARY, SECONDARY, NEGATIVE, DANGER }

/**
 * A button (design-system §6): primary blue, secondary grey, negative grey
 * with red text (拒绝), danger red only in confirmations. 44 dp, radius 10.
 */
@Composable
fun Btn(text: String, onClick: () -> Unit, modifier: Modifier = Modifier, kind: BtnKind = BtnKind.SECONDARY, enabled: Boolean = true, bg: Color? = null, style: TextStyle = T.bodyB) {
    val p = pal
    val (back, fore) = when (kind) {
        BtnKind.PRIMARY -> p.blueBg to Color.White
        BtnKind.SECONDARY -> p.chip to p.fg
        BtnKind.NEGATIVE -> p.chip to p.red
        BtnKind.DANGER -> p.redBg to Color.White
    }
    Box(
        // The whole button dims when it is not enabled (design-system §6), its surface with its text.
        modifier.heightIn(min = 44.dp).alpha(if (enabled) 1f else .45f).clip(Radii.SM).background(bg ?: back)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick).padding(10.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(text, style = style, color = fore, textAlign = TextAlign.Center, maxLines = 2)
    }
}

/** A row of buttons sharing the width, 8 dp apart. */
@Composable
fun Btns(modifier: Modifier = Modifier, content: @Composable RowScope.() -> Unit) {
    Row(modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp), content = content)
}

enum class Tone2 { PLAIN, WARN, ERROR }

/** Set on grey (soft) pages, where notices and code use the card surface instead (design-system §2). */
val LocalOnSoft = androidx.compose.runtime.staticCompositionLocalOf { false }

/** A notice: soft, 13, at most one text action at the right (design-system §6). */
@Composable
fun Banner(text: String, modifier: Modifier = Modifier, tone: Tone2 = Tone2.PLAIN, action: String? = null, onAction: (() -> Unit)? = null, lead: (@Composable () -> Unit)? = null) {
    val p = pal
    Row(
        modifier.fillMaxWidth().clip(Radii.SM).background(if (LocalOnSoft.current) p.card else p.soft).heightIn(min = 38.dp).padding(start = 12.dp, end = if (action != null) 4.dp else 12.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        lead?.invoke()
        Text(
            text, style = T.sm, modifier = Modifier.weight(1f).padding(vertical = 9.dp),
            color = when (tone) { Tone2.PLAIN -> p.fg2; Tone2.WARN -> p.orangeInk; Tone2.ERROR -> p.red },
        )
        if (action != null) Box(Modifier.widthIn(min = 44.dp).heightIn(min = 44.dp).tap { onAction?.invoke() }.padding(horizontal = 8.dp), contentAlignment = Alignment.Center) {
            Text(action, style = T.smB, color = p.blue, maxLines = 1)
        }
    }
}

/** A text action: blue, or red for 中断; 44 dp tall. One that goes to another page ends in an [arrow]. */
@Composable
fun TextAction(text: String, onClick: () -> Unit, modifier: Modifier = Modifier, color: Color? = null, style: TextStyle = T.smB, enabled: Boolean = true, alignment: Alignment = Alignment.CenterStart, arrow: Boolean = false) {
    Box(modifier.heightIn(min = 44.dp).widthIn(min = 44.dp).clickable(enabled = enabled, role = Role.Button, onClick = onClick).alpha(if (enabled) 1f else .45f), contentAlignment = alignment) {
        if (arrow) ArrowText(text, color ?: pal.blue, style) else Text(text, style = style, color = color ?: pal.blue, maxLines = 1)
    }
}

/** A grouped list on a grey page: card, radius 14, 16 dp in from the sides. */
@Composable
fun Group(modifier: Modifier = Modifier, rows: List<@Composable () -> Unit>) {
    val p = pal
    Column(modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, bottom = 12.dp).clip(Radii.MD).background(p.card)) {
        rows.forEachIndexed { i, r ->
            if (i > 0) HorizontalDivider(color = p.line, thickness = 1.dp)
            r()
        }
    }
}

/**
 * A row in a group: 15 with a value at the right. A row that goes somewhere
 * has a [chevron], after its value, or at its end without one; one that does
 * something is blue ([action]) or red ([danger]), its chevron right after
 * the text and an [icon] such as a plus before it.
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
) {
    val p = pal
    val ink = if (danger) p.red else if (action) p.blue else if (enabled) p.fg else p.muted
    if (value != null && androidx.compose.ui.platform.LocalDensity.current.fontScale >= 1.8f) {
        Column(Modifier.fillMaxWidth().heightIn(min = 44.dp).then(if (onClick != null) Modifier.clickable(enabled = enabled, onClick = onClick) else Modifier)
            .alpha(if (enabled) 1f else .6f).padding(horizontal = 14.dp, vertical = 8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (lead != null) { lead(); Spacer(Modifier.width(10.dp)) }
                Text(text, style = T.body, color = ink, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                trailing?.invoke()
            }
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp, Alignment.End)) {
                Text(value, style = T.sm, color = valueColor ?: p.muted, textAlign = TextAlign.End, modifier = Modifier.weight(1f, fill = false))
                if (chevron) Arrow(p.muted, T.sm)
            }
        }
        return
    }
    Row(
        Modifier.fillMaxWidth().heightIn(min = 44.dp).then(if (onClick != null) Modifier.clickable(enabled = enabled, onClick = onClick) else Modifier)
            .alpha(if (enabled) 1f else .6f).padding(horizontal = 14.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        lead?.invoke()
        Row(Modifier.weight(1f).padding(vertical = 11.dp), verticalAlignment = Alignment.CenterVertically) {
            if (icon != null) {
                Ic(icon, iconSize(T.body), ink)
                Spacer(Modifier.width(6.dp))
            }
            Text(text, style = T.body, color = ink, modifier = Modifier.weight(1f, fill = false))
            if (chevron && action) {
                Spacer(Modifier.width(4.dp))
                Arrow(ink, T.body)
            }
        }
        if (value != null) Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
            Text(value, style = T.sm, color = valueColor ?: p.muted, textAlign = TextAlign.End, maxLines = 2, modifier = Modifier.widthIn(max = 200.dp))
            if (chevron) Arrow(p.muted, T.sm)
        }
        trailing?.invoke()
        if (chevron && value == null && !action) Arrow(p.muted, T.sm)
    }
}

/** A label above a group on a grey page. */
@Composable
fun GLabel(text: String, action: String? = null, onAction: (() -> Unit)? = null) {
    val p = pal
    Row(Modifier.fillMaxWidth().padding(start = 30.dp, end = 30.dp, top = 4.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text, style = T.sm, color = p.muted, modifier = Modifier.weight(1f))
        if (action != null) Box(Modifier.height(20.dp).tap { onAction?.invoke() }, contentAlignment = Alignment.Center) { ArrowText(action, p.blue, T.sm) }
    }
}

/** The grab handle and title row of a bottom sheet. */
@Composable
fun SheetHead(title: String, action: String? = null, onAction: (() -> Unit)? = null, onBack: (() -> Unit)? = null) {
    val p = pal
    Row(Modifier.fillMaxWidth().padding(bottom = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        if (onBack != null) Box(Modifier.width(28.dp).height(44.dp).tap { onBack() }, contentAlignment = Alignment.CenterStart) { Ic(Lucide.ChevronLeft, 22.dp, p.blue, contentDescription = "返回") }
        Text(title, style = T.h17, color = p.fg, modifier = Modifier.weight(1f))
        // As wide as its words: 完成 and 取消 fit in 44 dp, 以后再说 does not.
        if (action != null) Box(Modifier.heightIn(min = 44.dp).widthIn(min = 44.dp).tap { onAction?.invoke() }, contentAlignment = Alignment.Center) { Text(action, style = T.body, color = p.blue, maxLines = 1) }
    }
}

@Composable
private fun Grab() {
    Box(Modifier.fillMaxWidth().padding(top = 10.dp, bottom = 12.dp), contentAlignment = Alignment.Center) {
        Box(Modifier.width(36.dp).height(5.dp).clip(RoundedCornerShape(3.dp)).background(pal.track))
    }
}

/** A bottom sheet: card, top corners 20, grab handle, 16 dp sides (design-system §6). */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun Sheet(onClose: () -> Unit, scroll: Boolean = true, canDismiss: Boolean = true, content: @Composable ColumnScope.() -> Unit) {
    val p = pal
    ModalBottomSheet(
        onDismissRequest = onClose,
        sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true, confirmValueChange = { canDismiss || it != androidx.compose.material3.SheetValue.Hidden }),
        containerColor = p.card,
        shape = RoundedCornerShape(topStart = Radii.lg, topEnd = Radii.lg),
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

/** 12 grey caption under a sheet's content. */
@Composable
fun Hint(text: String, modifier: Modifier = Modifier, color: Color? = null) {
    Text(text, style = T.meta, color = color ?: pal.muted, modifier = modifier.padding(top = 8.dp))
}

/** A field title inside a sheet (模型, 推理强度). */
@Composable
fun FieldLabel(text: String, modifier: Modifier = Modifier) {
    Text(text, style = T.metaB, color = pal.muted, modifier = modifier.padding(top = 12.dp, bottom = 6.dp))
}

/** A choice in a sheet's list: title, optional description, a check when chosen; [icon] goes before the title. */
@Composable
fun ListItem(title: String, desc: String? = null, checked: Boolean = false, onClick: (() -> Unit)?, enabled: Boolean = true, danger: Boolean = false, small: String? = null, last: Boolean = false, trailing: (@Composable () -> Unit)? = null, icon: ImageVector? = null) {
    val p = pal
    val ink = if (!enabled) p.muted else if (danger) p.red else p.fg
    Row(
        Modifier.fillMaxWidth().heightIn(min = 44.dp).then(if (!last) Modifier.bottomLine(p.line) else Modifier)
            .then(if (onClick != null) Modifier.clickable(enabled = enabled, onClick = onClick) else Modifier).padding(horizontal = 2.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Column(Modifier.weight(1f).padding(vertical = 8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (icon != null) {
                    Ic(icon, iconSize(T.body), ink)
                    Spacer(Modifier.width(4.dp))
                }
                Text(title, style = if (desc != null) T.bodyB else T.body, color = ink)
                if (small != null) Text(small, style = T.meta, color = p.muted, modifier = Modifier.padding(start = 4.dp))
            }
            if (desc != null) Text(desc, style = ts(12.sp, lineHeight = 1.45f), color = p.muted)
        }
        trailing?.invoke()
        if (checked) Mark(Lucide.Check, p.blue, 16.dp)
    }
}

/**
 * A confirmation (design-system §6): title in the text color, the danger is
 * the button's. Cancel left, the action right.
 */
@Composable
fun Alert(title: String, text: String, confirm: String, danger: Boolean = false, onDismiss: () -> Unit, onConfirm: () -> Unit) {
    val p = pal
    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Box(Modifier.fillMaxWidth().padding(horizontal = 30.dp), contentAlignment = Alignment.Center) {
            Column(
                Modifier.widthIn(max = 320.dp).fillMaxWidth().clip(Radii.LG).background(p.card).padding(start = 16.dp, end = 16.dp, top = 18.dp, bottom = 12.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                Text(title, style = T.h17, color = p.fg, textAlign = TextAlign.Center, modifier = Modifier.padding(bottom = 6.dp))
                Text(text, style = T.sm, color = p.muted, textAlign = TextAlign.Center, modifier = Modifier.padding(bottom = 14.dp))
                Btns {
                    Btn("取消", onDismiss, Modifier.weight(1f))
                    Btn(confirm, { onDismiss(); onConfirm() }, Modifier.weight(1f), if (danger) BtnKind.DANGER else BtnKind.PRIMARY)
                }
            }
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

/** The more menu uses the same scrollable, left-aligned sheet as project selection. */
@Composable
fun ActionSheet(info: (@Composable () -> Unit)?, actions: List<SheetAction>, onDismiss: () -> Unit) {
    val busy = actions.any { it.busy }
    Sheet(onClose = { if (!busy) onDismiss() }, canDismiss = !busy) {
        if (info != null) Box(Modifier.fillMaxWidth().padding(vertical = 10.dp), contentAlignment = Alignment.CenterStart) { info() }
        actions.forEach { a -> ActionSheetRow(a) { if (!a.keepOpen) onDismiss(); a.onClick() } }
    }
}

@Composable
internal fun ActionSheetRow(a: SheetAction, onClick: () -> Unit) {
    val p = pal
    val on = a.enabled && !a.busy
    Column(
        Modifier.fillMaxWidth().heightIn(min = 44.dp).bottomLine(p.line)
            .clickable(enabled = on, onClick = onClick).padding(vertical = 10.dp),
        horizontalAlignment = Alignment.Start,
    ) {
        Row(Modifier.fillMaxWidth().alpha(if (a.enabled) 1f else .4f), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            if (a.busy) Mark(Lucide.LoaderCircle, p.muted, 14.dp)
            Text(a.text, style = T.body, color = if (a.danger) p.red else p.fg, modifier = Modifier.weight(1f))
            if (a.value != null) {
                Text(a.value, style = T.sm, color = if (a.danger) p.red else if (a.warn) p.orangeInk else p.muted,
                    textAlign = TextAlign.End, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(2f, fill = false))
                Arrow(p.faint, T.body)
            }
        }
        if (a.note.isNotEmpty()) Text(a.note, style = T.meta, color = if (a.noteError) p.red else p.muted, modifier = Modifier.padding(top = 4.dp))
    }
}

/** A code or command block outside the terminal: soft, radius 6, mono 12 (design-system §6). */
@Composable
fun Code(text: String, modifier: Modifier = Modifier, maxLines: Int = Int.MAX_VALUE, bg: Color? = null, border: Boolean = false) {
    val p = pal
    Box(
        modifier.fillMaxWidth().clip(Radii.XS).background(bg ?: p.code).then(if (border) Modifier.border(1.dp, p.line, Radii.XS) else Modifier)
            .padding(horizontal = 10.dp, vertical = 7.dp),
    ) {
        Text(text, style = T.mono, color = p.fg2, maxLines = maxLines, overflow = TextOverflow.Ellipsis)
    }
}

/** An input field on a soft surface. */
@Composable
fun FieldBox(modifier: Modifier = Modifier, content: @Composable BoxScope.() -> Unit) {
    Box(modifier.fillMaxWidth().clip(Radii.SM).background(pal.soft).padding(horizontal = 14.dp, vertical = 11.dp), content = content)
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
        Column(Modifier.fillMaxSize().background(if (gray) p.soft else p.card).imePadding()) {
            NavBar(title, onBack, subtitle = subtitle, more = more, right = right)
            content()
        }
    }
}

/** 会话 and 设置 at the bottom of the two first-level pages: 11 labels, blue when chosen, the waiting count on 会话. */
@Composable
fun TabBar(route: String?, waiting: Int, scheduledAttention: Int = 0, onSelect: (String) -> Unit) {
    val p = pal
    Row(Modifier.fillMaxWidth().background(p.card).topLine(p.line).padding(top = 6.dp, bottom = 8.dp)) {
        for ((r, label) in listOf("home" to "会话", "schedules" to "定时", "settings" to "设置")) {
            val on = route == r
            Column(Modifier.weight(1f).heightIn(min = 44.dp).clickable { if (!on) onSelect(r) }, horizontalAlignment = Alignment.CenterHorizontally) {
                Box {
                    Ic(when(r) { "home" -> Lucide.MessageSquare; "schedules" -> Lucide.Clock; else -> Lucide.Settings }, 22.dp, if (on) p.blue else p.muted)
                    val count = if (r == "home") waiting else if (r == "schedules") scheduledAttention else 0
                    if (count > 0) Text(
                        "$count", style = T.tab.copy(fontWeight = platformWeight(FontWeight.Bold), lineHeight = 16.sp), color = Color.White,
                        modifier = Modifier.align(Alignment.TopStart).offset(x = 17.dp, y = (-4).dp).clip(RoundedCornerShape(8.dp)).background(p.orange).padding(horizontal = 5.dp),
                    )
                }
                Text(label, style = T.tab, color = if (on) p.blue else p.muted, modifier = Modifier.padding(top = 2.dp))
            }
        }
    }
}

/** A confirmation with one line of text to enter, such as a name. */
@Composable
fun InputAlert(title: String, initial: String, placeholder: String, confirm: String, onDismiss: () -> Unit, dismiss: String = "取消", text: String? = null, onConfirm: (String) -> Unit) {
    val p = pal
    var value by remember(initial) { androidx.compose.runtime.mutableStateOf(initial) }
    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Box(Modifier.fillMaxWidth().padding(horizontal = 30.dp).imePadding(), contentAlignment = Alignment.Center) {
            Column(Modifier.widthIn(max = 320.dp).fillMaxWidth().clip(Radii.LG).background(p.card).padding(start = 16.dp, end = 16.dp, top = 18.dp, bottom = 12.dp)) {
                Text(title, style = T.h17, color = p.fg, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(bottom = 12.dp))
                ReasonField(value, { value = it.take(40) }, placeholder)
                if (text != null) Text(text, style = T.sm, color = p.muted, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(top = 8.dp))
                Btns(Modifier.padding(top = 6.dp)) {
                    Btn(dismiss, onDismiss, Modifier.weight(1f))
                    Btn(confirm, { onDismiss(); onConfirm(value) }, Modifier.weight(1f), BtnKind.PRIMARY)
                }
            }
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

/** Top-level destinations share the same labels, icons and badge counts on wide displays. */
@Composable
fun SidebarTabs(route: String?, waiting: Int, scheduledAttention: Int, onSelect: (String) -> Unit) {
    Column(Modifier.width(200.dp).fillMaxHeight().background(pal.soft).padding(top = 16.dp)) {
        Text("remote-agent", style = T.h17, color = pal.fg, modifier = Modifier.padding(16.dp))
        for ((r, label) in listOf("home" to "会话", "schedules" to "定时", "settings" to "设置")) {
            Row(Modifier.fillMaxWidth().heightIn(min = 48.dp).background(if (route == r) pal.blueSoft else Color.Transparent).clickable { onSelect(r) }.padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                val color = if (route == r) pal.blue else pal.muted
                Ic(when(r) { "home" -> Lucide.MessageSquare; "schedules" -> Lucide.Clock; else -> Lucide.Settings }, 22.dp, color)
                Text(label, Modifier.weight(1f).padding(start = 12.dp), style = T.body, color = color)
                val count = if (r == "home") waiting else if (r == "schedules") scheduledAttention else 0
                if (count > 0) Text("$count", style = T.metaB, color = pal.orangeInk)
            }
        }
    }
}

/** Shared by conversations and scheduled tasks; the caller supplies the lower lines. */
@Composable
fun SessionListRow(title: String, time: String, tone: io.github.lewin671.remoteagent.core.Tone, last: Boolean = false, selected: Boolean = false, onClick: () -> Unit, content: @Composable ColumnScope.() -> Unit) {
    Row(Modifier.fillMaxWidth().background(if (selected) pal.blueSoft else Color.Transparent).clickable(onClick = onClick).padding(start = 16.dp, end = 16.dp, top = 10.dp), horizontalArrangement = Arrangement.spacedBy(11.dp), verticalAlignment = Alignment.Top) {
        Box(Modifier.padding(top = 8.dp)) { Dot(pal.tone(tone)) }
        Column(Modifier.weight(1f).then(if (last) Modifier else Modifier.bottomLine(pal.line)).padding(bottom = 10.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(title, style = T.bodyB, color = pal.fg, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                Text(time, style = T.meta, color = pal.muted, maxLines = 1)
            }
            content()
        }
    }
}
