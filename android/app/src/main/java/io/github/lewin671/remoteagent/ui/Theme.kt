package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.Tone
import io.github.lewin671.remoteagent.data.ThemeMode

/** The design's palette (client-v1, :root and the dark theme). */
@Immutable
data class Palette(
    val bg: Color, val fg: Color, val fg2: Color, val muted: Color, val faint: Color, val line: Color,
    val card: Color, val soft: Color, val chip: Color, val seg: Color, val track: Color,
    val blue: Color, val blueBg: Color, val blueSoft: Color,
    val orange: Color, val orangeInk: Color, val orangeSoft: Color,
    val green: Color, val greenInk: Color, val red: Color, val redBg: Color, val redSoft: Color, val gray: Color,
    val purple: Color, val purpleSoft: Color, val code: Color, val dark: Boolean,
) {
    fun tone(t: Tone): Color = when (t) {
        Tone.RUN -> blue
        Tone.WAIT -> orange
        Tone.IDLE -> green
        Tone.OFF -> gray
    }
}

val LightPalette = Palette(
    bg = Color(0xFFEEEEEC), fg = Color(0xFF16181C), fg2 = Color(0xFF3A3F46), muted = Color(0xFF626970), faint = Color(0xFF9AA0A7),
    line = Color(0xFFE6E8EB), card = Color.White, soft = Color(0xFFF5F5F7), chip = Color(0xFFEFF0F2), seg = Color(0xFFECEEF0), track = Color(0xFFD9DCE0),
    blue = Color(0xFF2563D9), blueBg = Color(0xFF2F6FEB), blueSoft = Color(0xFFEDF3FF),
    orange = Color(0xFFE07A12), orangeInk = Color(0xFF8A4A06), orangeSoft = Color(0xFFFFF3E3),
    green = Color(0xFF22A05A), greenInk = Color(0xFF177A43), red = Color(0xFFC62F2F), redBg = Color(0xFFC62F2F), redSoft = Color(0xFFFDECEC),
    gray = Color(0xFFB3B9C0), purple = Color(0xFF6D43C4), purpleSoft = Color(0xFFF1EBFD), code = Color(0xFFF5F5F7), dark = false,
)

val DarkPalette = Palette(
    bg = Color(0xFF0B0C0E), fg = Color(0xFFE8EAED), fg2 = Color(0xFFC9CED4), muted = Color(0xFFA0A7AF), faint = Color(0xFF6B727A),
    line = Color(0xFF2A2D32), card = Color(0xFF17181B), soft = Color(0xFF1F2125), chip = Color(0xFF272A2F), seg = Color(0xFF212327), track = Color(0xFF3A3E44),
    blue = Color(0xFF7AA7FF), blueBg = Color(0xFF2F6FEB), blueSoft = Color(0x295A91FF),
    orange = Color(0xFFF0A04B), orangeInk = Color(0xFFF3BD82), orangeSoft = Color(0x24F0A04B),
    green = Color(0xFF3FBF73), greenInk = Color(0xFF5FD08B), red = Color(0xFFFF7B7B), redBg = Color(0xFFC62F2F), redSoft = Color(0x24FF6B6B),
    gray = Color(0xFF555B62), purple = Color(0xFFB79CF5), purpleSoft = Color(0x29A98BF0), code = Color(0xFF0D1117), dark = true,
)

val LocalPalette = staticCompositionLocalOf { LightPalette }

val pal: Palette @Composable get() = LocalPalette.current

// Six sizes: 12 / 13 / 15 / 17 / 22 / 30.
private val type = Typography(
    displaySmall = TextStyle(fontSize = 30.sp, fontWeight = FontWeight.Bold, lineHeight = 36.sp),
    headlineSmall = TextStyle(fontSize = 22.sp, fontWeight = FontWeight.Bold, lineHeight = 28.sp),
    titleMedium = TextStyle(fontSize = 17.sp, fontWeight = FontWeight.SemiBold, lineHeight = 22.sp),
    titleSmall = TextStyle(fontSize = 15.sp, fontWeight = FontWeight.SemiBold, lineHeight = 20.sp),
    bodyLarge = TextStyle(fontSize = 15.sp, lineHeight = 22.sp),
    bodyMedium = TextStyle(fontSize = 13.sp, lineHeight = 18.sp),
    bodySmall = TextStyle(fontSize = 12.sp, lineHeight = 16.sp),
    labelLarge = TextStyle(fontSize = 15.sp, fontWeight = FontWeight.SemiBold),
    labelMedium = TextStyle(fontSize = 13.sp, fontWeight = FontWeight.Medium),
    labelSmall = TextStyle(fontSize = 12.sp, fontWeight = FontWeight.Medium),
)

@Composable
fun RaTheme(mode: ThemeMode, content: @Composable () -> Unit) {
    val dark = when (mode) {
        ThemeMode.SYSTEM -> isSystemInDarkTheme()
        ThemeMode.LIGHT -> false
        ThemeMode.DARK -> true
    }
    val p = if (dark) DarkPalette else LightPalette
    val scheme = if (dark) {
        darkColorScheme(
            primary = p.blueBg, onPrimary = Color.White, background = p.bg, onBackground = p.fg, surface = p.card, onSurface = p.fg,
            surfaceVariant = p.soft, onSurfaceVariant = p.muted, outline = p.line, outlineVariant = p.line, error = p.red,
            surfaceContainer = p.card, surfaceContainerHigh = p.soft, surfaceContainerLow = p.card, surfaceContainerHighest = p.chip,
            secondaryContainer = p.blueSoft, onSecondaryContainer = p.blue,
        )
    } else {
        lightColorScheme(
            primary = p.blueBg, onPrimary = Color.White, background = p.bg, onBackground = p.fg, surface = p.card, onSurface = p.fg,
            surfaceVariant = p.soft, onSurfaceVariant = p.muted, outline = p.line, outlineVariant = p.line, error = p.red,
            surfaceContainer = p.card, surfaceContainerHigh = p.soft, surfaceContainerLow = p.card, surfaceContainerHighest = p.chip,
            secondaryContainer = p.blueSoft, onSecondaryContainer = p.blue,
        )
    }
    CompositionLocalProvider(LocalPalette provides p) {
        MaterialTheme(colorScheme = scheme, typography = type, content = content)
    }
}
