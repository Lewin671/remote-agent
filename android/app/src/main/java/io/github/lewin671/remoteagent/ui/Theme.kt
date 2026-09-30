package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.LineHeightStyle
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.Tone
import io.github.lewin671.remoteagent.data.ThemeMode

/**
 * The palette of docs/design/client-v7.html (:root and the dark theme); what
 * each color is for is in docs/design-system.md. [code] is the surface of code
 * blocks outside the terminal: soft on a card.
 */
@Immutable
data class Palette(
    val bg: Color, val fg: Color, val fg2: Color, val muted: Color, val faint: Color, val line: Color,
    val card: Color, val soft: Color, val chip: Color, val seg: Color, val track: Color,
    val blue: Color, val blueBg: Color, val blueSoft: Color,
    val orange: Color, val orangeInk: Color, val orangeSoft: Color,
    val green: Color, val greenInk: Color, val red: Color, val redBg: Color, val redSoft: Color, val gray: Color,
    val code: Color, val dark: Boolean,
) {
    /** A status dot. */
    fun tone(t: Tone): Color = when (t) {
        Tone.RUN -> blueBg
        Tone.WAIT -> orange
        Tone.IDLE -> green
        Tone.OFF -> gray
    }

    /** The status word next to a dot. */
    fun toneInk(t: Tone): Color = when (t) {
        Tone.RUN -> blue
        Tone.WAIT -> orangeInk
        Tone.IDLE -> greenInk
        Tone.OFF -> muted
    }
}

val LightPalette = Palette(
    bg = Color(0xFFEEEEEC), fg = Color(0xFF16181C), fg2 = Color(0xFF3A3F46), muted = Color(0xFF626970), faint = Color(0xFF9AA0A7),
    line = Color(0xFFE6E8EB), card = Color.White, soft = Color(0xFFF5F5F7), chip = Color(0xFFEFF0F2), seg = Color(0xFFECEEF0), track = Color(0xFFD9DCE0),
    blue = Color(0xFF2563D9), blueBg = Color(0xFF2F6FEB), blueSoft = Color(0xFFEDF3FF),
    orange = Color(0xFFE07A12), orangeInk = Color(0xFF8A4A06), orangeSoft = Color(0xFFFFF3E3),
    green = Color(0xFF22A05A), greenInk = Color(0xFF177A43), red = Color(0xFFC62F2F), redBg = Color(0xFFC62F2F), redSoft = Color(0xFFFDECEC),
    gray = Color(0xFFB3B9C0), code = Color(0xFFF5F5F7), dark = false,
)

val DarkPalette = Palette(
    bg = Color(0xFF0B0C0E), fg = Color(0xFFE8EAED), fg2 = Color(0xFFC9CED4), muted = Color(0xFFA0A7AF), faint = Color(0xFF6B727A),
    line = Color(0xFF2A2D32), card = Color(0xFF17181B), soft = Color(0xFF1F2125), chip = Color(0xFF272A2F), seg = Color(0xFF212327), track = Color(0xFF3A3E44),
    blue = Color(0xFF7AA7FF), blueBg = Color(0xFF2F6FEB), blueSoft = Color(0x295A91FF),
    orange = Color(0xFFF0A04B), orangeInk = Color(0xFFF3BD82), orangeSoft = Color(0x24F0A04B),
    green = Color(0xFF3FBF73), greenInk = Color(0xFF5FD08B), red = Color(0xFFFF7B7B), redBg = Color(0xFFC62F2F), redSoft = Color(0x24FF6B6B),
    gray = Color(0xFF555B62), code = Color(0xFF1F2125), dark = true,
)

val LocalPalette = staticCompositionLocalOf { LightPalette }

val pal: Palette @Composable get() = LocalPalette.current

/** Corner radii (design-system §4). */
object Radii {
    val xs = 6.dp
    val sm = 10.dp
    val md = 14.dp
    val lg = 20.dp
    val XS = RoundedCornerShape(xs)
    val SM = RoundedCornerShape(sm)
    val MD = RoundedCornerShape(md)
    val LG = RoundedCornerShape(lg)
}

/** The terminal keeps its own dark colors in either theme. */
object TermColors {
    val bg = Color(0xFF0D1117)
    val bar = Color(0xFF161B22)
    val key = Color(0xFF2D333B)
    val fg = Color(0xFFE6EDF3)
    val text = Color(0xFFC9D1D9)
    val dim = Color(0xFF9DA7B3)
    val tool = Color(0xFF8B949E)
    val link = Color(0xFF79C0FF)
    val line = Color(0xFF30363D)
    val rule = Color(0xFF21262D)
}

/** Project avatars: one fixed gradient per project, the same in both themes. */
val avatarGradients = listOf(
    Brush.linearGradient(listOf(Color(0xFF5B8DEF), Color(0xFF3A63D8))),
    Brush.linearGradient(listOf(Color(0xFFF0A35A), Color(0xFFE0772B))),
    Brush.linearGradient(listOf(Color(0xFF6CC28A), Color(0xFF34A061))),
    Brush.linearGradient(listOf(Color(0xFFB28BE8), Color(0xFF8A5FD6))),
)

private val centered = LineHeightStyle(LineHeightStyle.Alignment.Center, LineHeightStyle.Trim.None)

/** A size of design-system §3 at the mockup's line height (1.5 unless said). */
fun ts(size: TextUnit, weight: FontWeight = FontWeight.Normal, lineHeight: Float = 1.5f): TextStyle =
    TextStyle(fontSize = size, fontWeight = weight, lineHeight = (size.value * lineHeight).sp, lineHeightStyle = centered)

/** The type scale: 30 / 22 / 17 / 15 / 13 / 12, and 11 for tab labels and badges. */
object T {
    val title30 = ts(30.sp, FontWeight.Bold, 1.2f).copy(letterSpacing = (-0.5).sp)
    val h22 = ts(22.sp, FontWeight.Bold, 1.3f).copy(letterSpacing = (-0.3).sp)
    val h17 = ts(17.sp, FontWeight.SemiBold, 1.35f)
    val body = ts(15.sp)
    val bodyB = ts(15.sp, FontWeight.SemiBold)
    val chat = ts(15.sp, lineHeight = 1.55f)
    val sm = ts(13.sp)
    val smB = ts(13.sp, FontWeight.SemiBold)
    val meta = ts(12.sp)
    val metaB = ts(12.sp, FontWeight.SemiBold)
    val tab = ts(11.sp)
    val mono = TextStyle(fontFamily = FontFamily.Monospace, fontSize = 12.sp, lineHeight = 18.sp, lineHeightStyle = centered)
    val mono13 = TextStyle(fontFamily = FontFamily.Monospace, fontSize = 13.sp, lineHeight = 19.5.sp, lineHeightStyle = centered)
}

private val type = Typography(
    displaySmall = T.title30,
    headlineSmall = T.h22,
    titleMedium = T.h17,
    titleSmall = T.bodyB,
    bodyLarge = T.body,
    bodyMedium = T.sm,
    bodySmall = T.meta,
    labelLarge = T.bodyB,
    labelMedium = T.smB,
    labelSmall = ts(12.sp, FontWeight.Medium),
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

@Composable
fun RaTheme(p: Palette, content: @Composable () -> Unit) {
    val scheme = if (p.dark) {
        darkColorScheme(
            primary = p.blueBg, onPrimary = Color.White, background = p.card, onBackground = p.fg, surface = p.card, onSurface = p.fg,
            surfaceVariant = p.soft, onSurfaceVariant = p.muted, outline = p.line, outlineVariant = p.line, error = p.red,
            surfaceContainer = p.card, surfaceContainerHigh = p.card, surfaceContainerLow = p.card, surfaceContainerHighest = p.chip,
            secondaryContainer = p.blueSoft, onSecondaryContainer = p.blue,
        )
    } else {
        lightColorScheme(
            primary = p.blueBg, onPrimary = Color.White, background = p.card, onBackground = p.fg, surface = p.card, onSurface = p.fg,
            surfaceVariant = p.soft, onSurfaceVariant = p.muted, outline = p.line, outlineVariant = p.line, error = p.red,
            surfaceContainer = p.card, surfaceContainerHigh = p.card, surfaceContainerLow = p.card, surfaceContainerHighest = p.chip,
            secondaryContainer = p.blueSoft, onSecondaryContainer = p.blue,
        )
    }
    CompositionLocalProvider(LocalPalette provides p) {
        MaterialTheme(colorScheme = scheme, typography = type, content = content)
    }
}
