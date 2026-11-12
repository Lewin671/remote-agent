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

@Composable
fun uiText(text: io.github.lewin671.remoteagent.core.MessageText): String {
    val context = LocalContext.current
    val locale = LocalMessageLocale.current
    return text.resolve { resolveUiMessage(context, it, locale) }
}

/** Capture display language before entering callbacks or the chat projection worker. */
@Composable
fun uiText(): (UiMessage) -> String {
    val context = LocalContext.current
    val locale = LocalMessageLocale.current
    return androidx.compose.runtime.remember(context, locale) {
        { message -> resolveUiMessage(context, message, locale) }
    }
}

// Resource-only JVM fixtures must not create RaApp: that also enables asynchronous disk work.
internal var sessionMessageContextForTesting: android.content.Context? = null

/** Background UI feedback uses the installed app's current language (stage 0: Chinese). */
internal fun sessionText(message: UiMessage): String =
    resolveUiMessage(sessionMessageContextForTesting ?: io.github.lewin671.remoteagent.RaApp.instance, message)
