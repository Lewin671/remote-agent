package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.projectName

/*
 * The buttons, notices and titles of design-system §6, for every screen of
 * client-v58. A screen has at most one [InkButton]: the one thing it is
 * there to do.
 */

/** The kinds of button (design-system §6 按钮). The white one on the dark card is [CardButton]. */
enum class ButtonKind {
    /** Ink, the screen's one main action. */
    INK,
    /** fill2 with ink text: 取消, 再试一次, 拒绝. */
    SECONDARY,
    /** Solid red: only in a confirmation. */
    DANGER,
}

/**
 * A pill button: 17 / 600, 48 tall, or 15 and 38 when [small]. Greyed to
 * 40% when not [enabled]. [icon] goes before the words.
 */
@Composable
fun PillButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    kind: ButtonKind = ButtonKind.SECONDARY,
    enabled: Boolean = true,
    small: Boolean = false,
    icon: ImageVector? = null,
) {
    val p = pal
    val (back, fore) = when (kind) {
        ButtonKind.INK -> p.btn to p.btnFg
        ButtonKind.SECONDARY -> p.fill2 to p.ink
        ButtonKind.DANGER -> p.red to Color.White
    }
    Row(
        modifier.heightIn(min = if (small) 38.dp else 48.dp).disabledAlpha(enabled).clip(Radii.Pill).background(back)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick).padding(horizontal = if (small) 18.dp else 22.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.CenterHorizontally),
    ) {
        if (icon != null) Ic(icon, if (small) 18.dp else 20.dp, fore)
        Text(text, style = if (small) T.s15b else T.s17b, color = fore, textAlign = TextAlign.Center, maxLines = 2)
    }
}

/** The ink new button at the bottom right: 「新任务」「新定时」, 56 tall and floating; greyed and flat when it cannot be used. */
@Composable
fun InkFab(text: String, icon: ImageVector, onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true) {
    val p = pal
    Row(
        modifier.heightIn(min = 56.dp).floating(p, Radii.Pill, enabled).disabledAlpha(enabled, .35f).clip(Radii.Pill).background(p.btn)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick).padding(start = 18.dp, end = 22.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Ic(icon, 20.dp, p.btnFg)
        Text(text, style = T.s17b, color = p.btnFg, maxLines = 1)
    }
}

/**
 * A text action (design-system §6 文字操作): blue, or red when it loses
 * something; 44 tall. [chevron] ends one that goes elsewhere (right) or opens
 * in place (down / up).
 */
@Composable
fun TextLink(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    style: TextStyle = T.s15,
    danger: Boolean = false,
    enabled: Boolean = true,
    chevron: ImageVector? = null,
    color: Color? = null,
) {
    val c = color ?: if (danger) pal.red else pal.blue
    Row(
        modifier.heightIn(min = 44.dp).widthIn(min = 44.dp).disabledAlpha(enabled).clickable(enabled = enabled, role = Role.Button, onClick = onClick),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(text, style = style, color = c, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
        if (chevron != null) {
            Spacer(Modifier.width(2.dp))
            Ic(chevron, iconSize(style), c)
        }
    }
}

/** How a notice reads: grey, red for an error, orange for what the user has to handle. */
enum class NoticeTone { PLAIN, ERROR, WARN }

/**
 * A notice on a page (design-system §6 提示条): gbg, radius 18, 13 ink2; the
 * words at the left and at most one text action at the right.
 */
@Composable
fun Notice(
    text: String,
    modifier: Modifier = Modifier,
    tone: NoticeTone = NoticeTone.PLAIN,
    action: String? = null,
    onAction: (() -> Unit)? = null,
    icon: ImageVector? = null,
) {
    val p = pal
    val ink = when (tone) {
        NoticeTone.PLAIN -> p.ink2
        NoticeTone.ERROR -> p.red
        NoticeTone.WARN -> p.orangeInk
    }
    Row(
        modifier.fillMaxWidth().clip(Radii.R18).background(if (tone == NoticeTone.WARN) p.orangeSoft else p.gbg)
            .padding(start = 14.dp, end = if (action != null) 6.dp else 14.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        if (icon != null) Ic(icon, 16.dp, ink)
        Text(text, style = T.s13, color = ink, modifier = Modifier.weight(1f).padding(vertical = 12.dp))
        if (action != null) Box(
            Modifier.heightIn(min = 44.dp).widthIn(min = 44.dp).clickable(role = Role.Button) { onAction?.invoke() }.padding(horizontal = 8.dp),
            contentAlignment = Alignment.Center,
        ) { Text(action, style = T.s13b, color = p.blue, maxLines = 1) }
    }
}

/**
 * A group's title on a page of rows (design-system §6 分组标题): 进行中 is 15
 * / 600 ink; a [date] group of ended sessions one step lighter, 13 ink2. At
 * the right a count, or [trailing] such as a text action.
 */
@Composable
fun GroupTitle(text: String, modifier: Modifier = Modifier, date: Boolean = false, count: Int? = null, trailing: (@Composable () -> Unit)? = null) {
    val p = pal
    Row(
        modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, top = if (date) 26.dp else 30.dp, bottom = 4.dp).semantics(mergeDescendants = true) {},
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(text, style = if (date) T.s13 else T.s15b, color = if (date) p.ink2 else p.ink, modifier = Modifier.weight(1f))
        if (count != null) Text("$count", style = T.s15, color = p.ink2)
        trailing?.invoke()
    }
}

/**
 * A project's avatar (design-system §2): its name's first letter, ink on a
 * fill2 circle, the same for every project: colour only says a state. [all]
 * is 全部项目's 全.
 */
@Composable
fun ProjectAvatar(key: String, size: Int = 28, all: Boolean = false) {
    val p = pal
    Box(Modifier.size(size.dp).clip(CircleShape).background(p.fill2), contentAlignment = Alignment.Center) {
        Text(
            if (all) "全" else projectName(key).take(1).lowercase(),
            // In the circle's own scale: the letter does not grow past it with the font size.
            style = ts(with(LocalDensity.current) { (size / 2).dp.toSp() }, FontWeight.Bold, 1.2f),
            color = p.ink,
        )
    }
}

/**
 * The title filter (design-system §6 筛选框): a fill pill 40 tall with ⌕ and
 * 按标题筛选, and ✕ to clear once something is typed.
 */
@Composable
fun FilterField(value: String, onChange: (String) -> Unit, modifier: Modifier = Modifier, placeholder: String = "按标题筛选") {
    val p = pal
    BasicTextField(
        value,
        onChange,
        modifier.fillMaxWidth().testTag("title-filter"),
        singleLine = true,
        textStyle = T.s17.copy(color = p.ink),
        cursorBrush = SolidColor(p.orange),
        decorationBox = { inner ->
            Row(
                Modifier.fillMaxWidth().heightIn(min = 40.dp).clip(Radii.Pill).background(p.fill).padding(start = 14.dp, end = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                Ic(Lucide.Search, 18.dp, p.ink3)
                Box(Modifier.weight(1f)) {
                    if (value.isEmpty()) Text(placeholder, style = T.s17, color = p.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    inner()
                }
                if (value.isNotEmpty()) Box(
                    Modifier.size(40.dp).tap { onChange("") }.semantics { contentDescription = "清空筛选" },
                    contentAlignment = Alignment.Center,
                ) { Ic(Lucide.X, 18.dp, p.ink3) }
            }
        },
    )
}

/**
 * The glass segmented control (design-system §6 分段控件), 38 tall: the
 * chosen item is a light slider (paper, or mid grey on the dark terminal),
 * never ink. [dot] puts an orange dot on that item; an item not [enabled]
 * greys and cannot be chosen.
 */
@Composable
fun Segmented(
    items: List<String>,
    selected: Int,
    onSelect: (Int) -> Unit,
    modifier: Modifier = Modifier,
    enabled: (Int) -> Boolean = { true },
    dot: Int = -1,
    dark: Boolean = false,
) {
    val p = pal
    Row(
        modifier.heightIn(min = 38.dp).glass(p, Radii.Pill, dark = dark, onGrey = LocalOnSoft.current).padding(3.dp)
            .selectableGroup(),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        items.forEachIndexed { i, title ->
            val on = i == selected
            val ok = enabled(i)
            val text = when {
                dark && on -> Color.White
                dark -> CardColors.text.copy(alpha = if (ok) .7f else .35f)
                on -> p.ink
                ok -> p.ink2
                else -> p.ink3
            }
            Row(
                Modifier.weight(1f).heightIn(min = 32.dp)
                    .then(if (on) Modifier.shadow(2.dp, Radii.Pill, clip = false, ambientColor = p.glassShadow, spotColor = p.glassShadow).clip(Radii.Pill).background(if (dark) Color(0xFF636366) else p.paper) else Modifier)
                    .selectable(selected = on, enabled = ok, role = Role.Tab) { onSelect(i) }
                    .padding(horizontal = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.Center,
            ) {
                Text(title, style = if (on) T.s13b else T.s13.copy(fontWeight = platformWeight(FontWeight.Medium)), color = text, maxLines = 1)
                if (i == dot) {
                    Spacer(Modifier.width(5.dp))
                    Box(Modifier.size(7.dp).clip(CircleShape).background(p.orange))
                }
            }
        }
    }
}

/**
 * A strip (design-system §6 提示条): a glass capsule under the session's
 * segmented control, 13 ink2 with an icon at the left, one sentence, and at
 * most one text action at the right. [warn] is the orange safety strip;
 * [error] writes the sentence in red. The whole strip takes the tap.
 */
@Composable
fun Strip(
    icon: ImageVector,
    text: String,
    modifier: Modifier = Modifier,
    action: String? = null,
    onClick: (() -> Unit)? = null,
    error: Boolean = false,
    warn: Boolean = false,
    spin: Boolean = false,
) {
    val p = pal
    val color = if (error) p.red else if (warn) p.orangeInk else p.ink2
    Row(
        modifier.fillMaxWidth().heightIn(min = 38.dp)
            .then(if (warn) Modifier.clip(Radii.Pill).background(p.orangeSoft) else Modifier.glass(p, Radii.Pill, onGrey = LocalOnSoft.current))
            .then(if (onClick != null) Modifier.clickable(role = Role.Button, onClick = onClick) else Modifier)
            .padding(horizontal = 14.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        if (spin) Spinner(color, size = 14.dp) else Ic(icon, 15.dp, color)
        Text(text, style = T.s13, color = color, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        if (action != null) Text(action, style = T.s13, color = p.blue, maxLines = 1)
    }
}

/**
 * A text box (design-system §2 fill): fill, radius 14, 17 text, the blue
 * caret; [placeholder] in ink3. One line with [singleLine], else up to
 * [maxLines].
 */
@Composable
fun InputField(
    value: String,
    onValue: (String) -> Unit,
    placeholder: String,
    modifier: Modifier = Modifier,
    singleLine: Boolean = false,
    maxLines: Int = 4,
) {
    val p = pal
    BasicTextField(
        value,
        onValue,
        modifier.fillMaxWidth().padding(bottom = 8.dp),
        textStyle = T.s17.copy(color = p.ink),
        cursorBrush = SolidColor(p.blue),
        maxLines = if (singleLine) 1 else maxLines,
        singleLine = singleLine,
        decorationBox = { inner ->
            FieldBox {
                if (value.isEmpty()) Text(placeholder, style = T.s17, color = p.ink3)
                inner()
            }
        },
    )
}
