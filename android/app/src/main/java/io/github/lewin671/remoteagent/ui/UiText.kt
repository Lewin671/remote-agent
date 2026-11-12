package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.platform.LocalContext
import io.github.lewin671.remoteagent.core.UiMessage

// Stage 0 stays Chinese. Tests/previews can provide an explicit language.
val LocalMessageLocale = staticCompositionLocalOf { "zh-CN" }

@Composable
fun uiText(message: UiMessage): String = resolveUiMessage(LocalContext.current, message, LocalMessageLocale.current)

/** Local-only preference scaffold; stage 0 never enables automatic selection. */
class MessagePreferences(context: android.content.Context) {
    private val storage = context.getSharedPreferences("message-language", android.content.Context.MODE_PRIVATE)
    var preference: String
        get() = storage.getString("language", "auto") ?: "auto"
        set(value) {
            require(value == "auto" || value in io.github.lewin671.remoteagent.core.MessageLocale.appleTags)
            storage.edit().putString("language", value).apply()
        }
    fun current(): String = "zh-CN"
}
