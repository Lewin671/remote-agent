package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.LocalRippleConfiguration
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.RippleConfiguration
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.LineHeightStyle
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.R
import io.github.lewin671.remoteagent.data.ThemeMode

/**
 * The colour tokens of docs/design-system.md §2, one meaning each, with the
 * values of docs/design/client-v58.html (:root and [data-theme="dark"]). The
 * names are the design system's: `orange-ink` is [orangeInk], `btn-fg`
 * [btnFg], `tc-line` [tcLine], `glass-line` [glassLine]. Text to be read is
 * [ink] or [ink2] only; [ink3] and [faint] are for graphics.
 *
 * Two are not in the mockup's list but in its rules: [raised], the surface
 * one step above [gbg] (the groups inside a sheet, a dialog and the chosen
 * side of a segmented control in the dark), and [glassShadow] / [float],
 * the colours of the two shadows.
 */
@Immutable
data class Palette(
    /** The page. */
    val paper: Color,
    /** The grey surface on paper: cards, grouped lists, the list of what waits, code blocks, notices, sheets. */
    val gbg: Color,
    val ink: Color,
    val ink2: Color,
    val ink3: Color,
    val faint: Color,
    val sep: Color,
    val fill: Color,
    val fill2: Color,
    /** The ink button and its text. */
    val btn: Color,
    val btnFg: Color,
    val bubble: Color,
    val blue: Color,
    val orange: Color,
    val orangeInk: Color,
    val orangeSoft: Color,
    val green: Color,
    val red: Color,
    /** The dark card, dark in both themes, and its thin edge in the dark one. */
    val tc: Color,
    val tcLine: Color,
    val term: Color,
    val glass: Color,
    val glassLine: Color,
    val glassShadow: Color,
    val float: Color,
    val raised: Color,
    val dark: Boolean,
)

val LightPalette = Palette(
    paper = Color(0xFFFFFFFF), gbg = Color(0xFFF5F5F7), ink = Color(0xFF1D1D1F), ink2 = Color(0xFF6E6E73), ink3 = Color(0xFFA1A1A6),
    faint = Color(0xFFD2D2D7), sep = Color(0x17000000), fill = Color(0x0D000000), fill2 = Color(0x17000000),
    btn = Color(0xFF1D1D1F), btnFg = Color(0xFFFFFFFF), bubble = Color(0xFFF0F0F3),
    blue = Color(0xFF0066CC), orange = Color(0xFFE06C00), orangeInk = Color(0xFFB55A00), orangeSoft = Color(0xFFFFF4E6),
    green = Color(0xFF248A3D), red = Color(0xFFD70015),
    tc = Color(0xFF1D1D1F), tcLine = Color.Transparent, term = Color(0xFF131315),
    glass = Color(0xB8FFFFFF), glassLine = Color(0x12000000), glassShadow = Color(0x1F000000), float = Color(0x2E000000),
    raised = Color(0xFFFFFFFF), dark = false,
)

val DarkPalette = Palette(
    paper = Color(0xFF000000), gbg = Color(0xFF1C1C1E), ink = Color(0xFFF5F5F7), ink2 = Color(0xFFA1A1A6), ink3 = Color(0xFF6E6E73),
    faint = Color(0xFF48484A), sep = Color(0x1FFFFFFF), fill = Color(0x17FFFFFF), fill2 = Color(0x26FFFFFF),
    btn = Color(0xFFF5F5F7), btnFg = Color(0xFF000000), bubble = Color(0xFF2C2C2E),
    blue = Color(0xFF4DA3FF), orange = Color(0xFFFF9F2E), orangeInk = Color(0xFFFFB25C), orangeSoft = Color(0x24FF9F2E),
    green = Color(0xFF32D15D), red = Color(0xFFFF5A50),
    tc = Color(0xFF242426), tcLine = Color(0x24FFFFFF), term = Color(0xFF0D0D0F),
    glass = Color(0xA8323236), glassLine = Color(0x21FFFFFF), glassShadow = Color(0x80000000), float = Color(0x99000000),
    raised = Color(0xFF2C2C2E), dark = true,
)

val LocalPalette = staticCompositionLocalOf { LightPalette }

val pal: Palette @Composable get() = LocalPalette.current

/**
 * What is drawn on the dark card (design-system §6 终端卡): the same in both
 * themes, since the card is.
 */
object CardColors {
    val text = Color(0xFFF5F5F7)
    /** Its second lines: rgba(245,245,247,.62). */
    val text2 = Color(0x9EF5F5F7)
    val orange = Color(0xFFFF9F2E)
    val link = Color(0xFF6CB4FF)
    /** The command block, black in both themes, and the command in it. */
    val cmd = Color(0xFF000000)
    val cmdText = Color(0xFFE8E8ED)
    /** A failure on the card: the dark theme's red, which reads on the card in both. */
    val red = Color(0xFFFF5A50)
    /** The white button of the one thing to do, and the others on half-clear white. */
    val primary = Color(0xFFF5F5F7)
    val onPrimary = Color(0xFF000000)
    val secondary = Color(0x21FFFFFF)
    /** A field on the card, such as the reason for 不允许. */
    val field = Color(0x1AFFFFFF)
}

/** Corner radii (design-system §4): only these values. */
object Radii {
    val R6 = RoundedCornerShape(6.dp)
    val R14 = RoundedCornerShape(14.dp)
    val R16 = RoundedCornerShape(16.dp)
    val R18 = RoundedCornerShape(18.dp)
    val R22 = RoundedCornerShape(22.dp)
    val R20 = RoundedCornerShape(20.dp)
    val R24 = RoundedCornerShape(24.dp)
    val R28 = RoundedCornerShape(28.dp)
    val R30 = RoundedCornerShape(30.dp)
    /** The top corners of a bottom sheet. */
    val sheet = RoundedCornerShape(topStart = 38.dp, topEnd = 38.dp)
    /** Half the height: buttons, glass, segmented controls, tabs, bubbles. */
    val Pill = RoundedCornerShape(50)
}

/** The terminal keeps its own dark colors in either theme. */
object TermColors {
    val bg = Color(0xFF0D1117)
    val key = Color(0xFF2D333B)
    val fg = Color(0xFFE6EDF3)
    val text = Color(0xFFC9D1D9)
    val dim = Color(0xFF9DA7B3)
    val tool = Color(0xFF8B949E)
    val link = Color(0xFF79C0FF)
    val line = Color(0xFF30363D)
}

private val centered = LineHeightStyle(LineHeightStyle.Alignment.Center, LineHeightStyle.Trim.None)

/**
 * The mockup's weights as they look on a Mac (design-system §3): Latin in
 * SF at the nominal weight, Chinese in PingFang, whose heaviest weight is
 * Semibold. Noto Sans CJK on Android draws each weight heavier than
 * PingFang, so a requested weight falls one step for Chinese, while
 * [UiFamily] registers each Inter file at that lowered weight so Latin keeps
 * the nominal one: 500 → 450, 600 → 500, 700 → 600.
 */
fun platformWeight(w: FontWeight): FontWeight = when {
    w >= FontWeight.Bold -> FontWeight.SemiBold
    w >= FontWeight.SemiBold -> FontWeight.Medium
    w >= FontWeight.Medium -> FontWeight(450)
    else -> w
}

/** Inter (SIL OFL 1.1, Latin subset) for Latin and digits; Chinese falls back to the system font. */
val UiFamily = FontFamily(
    Font(R.font.inter_regular, FontWeight.Normal),
    Font(R.font.inter_medium, FontWeight(450)),
    Font(R.font.inter_semibold, FontWeight.Medium),
    Font(R.font.inter_bold, FontWeight.SemiBold),
)

/** JetBrains Mono (SIL OFL 1.1) for code, paths and commands, instead of the system's DroidSansMono. */
val MonoFamily = FontFamily(
    Font(R.font.jetbrains_mono_regular, FontWeight.Normal),
    Font(R.font.jetbrains_mono_medium, FontWeight.Medium),
)

/** A size of design-system §3 at a line height of [lineHeight] times it. */
fun ts(size: TextUnit, weight: FontWeight = FontWeight.Normal, lineHeight: Float = 1.4f): TextStyle =
    TextStyle(fontFamily = UiFamily, fontSize = size, fontWeight = platformWeight(weight), lineHeight = (size.value * lineHeight).sp, lineHeightStyle = centered)

private fun mono(size: TextUnit, lineHeight: Float = 1.5f): TextStyle =
    TextStyle(fontFamily = MonoFamily, fontSize = size, lineHeight = (size.value * lineHeight).sp, lineHeightStyle = centered)

/**
 * The type scale of design-system §3, named by size: 34 / 22 / 20 / 17 / 15
 * / 13 / 12 / 10; `b` is 600, `m` 500 (as they look: [platformWeight]).
 */
object T {
    /** The sentence at the top of the home and the tasks page, a first-level page's title. */
    val title = ts(34.sp, FontWeight.Bold, 1.14f).copy(letterSpacing = (-1).sp)
    /** A whole-page state's title, an empty state's, a single big number. */
    val h22 = ts(22.sp, FontWeight.Bold, 1.3f).copy(letterSpacing = (-0.4).sp)
    /** A card's title: the dark card's, a computer's. */
    val h20 = ts(20.sp, FontWeight.Bold, 1.3f).copy(letterSpacing = (-0.4).sp)
    /** Body, a list's first line, buttons, fields, sheet and dialog titles. */
    val s17 = ts(17.sp, lineHeight = 1.35f)
    val s17m = ts(17.sp, FontWeight.Medium, 1.35f)
    val s17b = ts(17.sp, FontWeight.SemiBold, 1.35f)
    /** A list's second line, the home's group titles, text actions, small buttons. */
    val s15 = ts(15.sp, lineHeight = 1.4f)
    val s15b = ts(15.sp, FontWeight.SemiBold, 1.4f)
    /** Meta, times, notes, notices, tool lines, date groups. */
    val s13 = ts(13.sp, lineHeight = 1.45f)
    val s13b = ts(13.sp, FontWeight.SemiBold, 1.45f)
    /** The status line under a session's title, badge numbers. */
    val s12 = ts(12.sp, lineHeight = 1.4f)
    val s12b = ts(12.sp, FontWeight.SemiBold, 1.4f)
    /** Only the bottom tabs' labels. */
    val s10 = ts(10.sp, FontWeight.SemiBold, 1.2f)
    /** Conversation text until the session is redrawn on client-v58. */
    val chat = ts(17.sp, lineHeight = 1.55f)
    /** Code blocks, command blocks and tables. */
    val mono13 = mono(13.sp)
    val mono12 = mono(12.sp)
}

private val type = Typography(
    displaySmall = T.title,
    headlineSmall = T.h22,
    titleLarge = T.h20,
    titleMedium = T.s17b,
    titleSmall = T.s15b,
    bodyLarge = T.s17,
    bodyMedium = T.s15,
    bodySmall = T.s13,
    labelLarge = T.s15b,
    labelMedium = T.s13b,
    labelSmall = T.s12b,
)

@Composable
fun RaTheme(mode: ThemeMode, content: @Composable () -> Unit) {
    val dark = when (mode) {
        ThemeMode.SYSTEM -> isSystemInDarkTheme()
        ThemeMode.LIGHT -> false
        ThemeMode.DARK -> true
    }
    RaTheme(if (dark) DarkPalette else LightPalette, content)
}

/**
 * The tokens, and Material's slots filled from them so that a Material
 * component never brings its own tonal surfaces or dynamic colour; a press
 * shows in the text colour, not in a tint.
 */
@OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)
@Composable
fun RaTheme(p: Palette, content: @Composable () -> Unit) {
    val base = if (p.dark) darkColorScheme() else lightColorScheme()
    val scheme = base.copy(
        primary = p.btn, onPrimary = p.btnFg, primaryContainer = p.fill2, onPrimaryContainer = p.ink,
        secondary = p.ink2, onSecondary = p.paper, secondaryContainer = p.fill2, onSecondaryContainer = p.ink,
        tertiary = p.blue, onTertiary = p.paper,
        background = p.paper, onBackground = p.ink, surface = p.paper, onSurface = p.ink,
        surfaceVariant = p.gbg, onSurfaceVariant = p.ink2, surfaceTint = Color.Transparent,
        outline = p.sep, outlineVariant = p.sep, error = p.red, onError = Color.White,
        surfaceBright = p.paper, surfaceDim = p.gbg, surfaceContainerLowest = p.paper, surfaceContainerLow = p.gbg,
        surfaceContainer = p.gbg, surfaceContainerHigh = p.gbg, surfaceContainerHighest = p.fill2,
        inverseSurface = p.ink, inverseOnSurface = p.paper, scrim = Color.Black,
    )
    val ripple = RippleConfiguration(color = p.ink)
    CompositionLocalProvider(LocalPalette provides p) {
        MaterialTheme(colorScheme = scheme, typography = type) {
            CompositionLocalProvider(LocalRippleConfiguration provides ripple, content = content)
        }
    }
}
