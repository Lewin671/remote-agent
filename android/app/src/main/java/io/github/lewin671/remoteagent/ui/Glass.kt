package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.asPaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

/*
 * Material (design-system §4 材质): there is no top bar and no tab bar. The
 * content fills the screen and fades out at the top and the bottom edge in
 * the page colour ([EdgeFade]); what floats over it is small pieces of glass
 * ([glass]): the round buttons, the scope capsule, the bottom tabs.
 *
 * Compose cannot blur what is drawn behind a composable, and the system's
 * blur only blurs a window's own content, so on Android the glass is the
 * opaque stand-in the design system gives for platforms that cannot blur:
 * the surface one step above the page ([Palette.gbg] on paper), with the
 * thin edge and the shadow kept.
 */

/** The shadows (design-system §4): small for glass, larger for what floats (the ink new button, the docked dark card, menus). */
private val GlassElevation = 6.dp
val FloatElevation = 16.dp

/**
 * A piece of glass in [shape]: the surface one step above the page it is on
 * ([onGrey] for a gbg page); [dark] is the terminal's, the same in both themes.
 */
fun Modifier.glass(p: Palette, shape: Shape, dark: Boolean = false, onGrey: Boolean = false): Modifier =
    shadow(GlassElevation, shape, clip = false, ambientColor = p.glassShadow, spotColor = p.glassShadow)
        .clip(shape)
        .background(if (dark) Color(0xFF3C3C40) else if (onGrey) p.raised else p.gbg)
        .border(0.5.dp, if (dark) Color(0x21FFFFFF) else p.glassLine, shape)

/** The shadow of something floating over the page. */
fun Modifier.floating(p: Palette, shape: Shape, enabled: Boolean = true): Modifier =
    if (enabled) shadow(FloatElevation, shape, clip = false, ambientColor = p.float, spotColor = p.float) else this

/** A round glass button, 44 (design-system §4): back, ⋯, the gear. [label] is what a screen reader says; [dark] on the terminal. */
@Composable
fun GlassButton(icon: ImageVector, label: String, onClick: () -> Unit, modifier: Modifier = Modifier, dot: Boolean = false, dark: Boolean = false) {
    val p = pal
    Box(
        modifier.size(44.dp).glass(p, CircleShape, dark = dark, onGrey = LocalOnSoft.current).tap(onClick = onClick).semantics { contentDescription = label },
        contentAlignment = Alignment.Center,
    ) {
        Ic(icon, 22.dp, if (dark) CardColors.text else p.ink)
        if (dot) Box(Modifier.align(Alignment.TopEnd).padding(top = 9.dp, end = 9.dp).size(8.dp).clip(CircleShape).background(p.orange))
    }
}

/** A glass capsule, 44 tall: the scope capsule and other chips over the content. */
@Composable
fun GlassCapsule(
    onClick: (() -> Unit)?,
    modifier: Modifier = Modifier,
    onClickLabel: String? = null,
    content: @Composable RowScope.() -> Unit,
) {
    val p = pal
    Row(
        modifier.heightIn(min = 44.dp).glass(p, Radii.Pill, onGrey = LocalOnSoft.current)
            .then(if (onClick != null) Modifier.clickable(onClickLabel = onClickLabel, role = Role.Button, onClick = onClick) else Modifier)
            .padding(start = 8.dp, end = 14.dp),
        verticalAlignment = Alignment.CenterVertically,
        content = content,
    )
}

/**
 * The fade at an edge of a screen whose content runs under it: the page
 * colour, solid over the system bar and the glass on it, then clear. Not a
 * blur (design-system §4). [extra] is how far it reaches past the system
 * bar.
 */
@Composable
fun BoxScope.EdgeFade(top: Boolean, extra: Dp, color: Color = pal.paper) {
    val bar = (if (top) WindowInsets.statusBars else WindowInsets.navigationBars).asPaddingValues()
    val inset = if (top) bar.calculateTopPadding() else bar.calculateBottomPadding()
    val height = inset + extra
    val solid = if (height.value <= 0f) 0f else ((inset.value + extra.value * .55f) / height.value).coerceIn(0f, 1f)
    val stops = if (top) arrayOf(0f to color, solid to color, 1f to color.copy(alpha = 0f))
    else arrayOf(0f to color.copy(alpha = 0f), 1f - solid to color, 1f to color)
    Spacer(
        Modifier.align(if (top) Alignment.TopCenter else Alignment.BottomCenter).fillMaxWidth().height(height)
            .background(Brush.verticalGradient(*stops)),
    )
}

/** The whole of a control greys to 40% when it cannot be used (design-system §6), its colours kept. */
fun Modifier.disabledAlpha(enabled: Boolean, alpha: Float = .4f): Modifier = if (enabled) this else alpha(alpha)
