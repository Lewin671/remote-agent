package io.github.lewin671.remoteagent.ui

import android.content.Context
import android.content.res.Configuration
import androidx.compose.runtime.Composable
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.withStyle
import io.github.lewin671.remoteagent.R
import io.github.lewin671.remoteagent.core.MessageLocale
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.coreTextToMessageText
import io.github.lewin671.remoteagent.core.proto.Text
import java.util.Locale

private val INT_PLACEHOLDER_RE = Regex("%(?:1\\$)?d")
private const val DEFAULT_CORE_LOCALE = "zh-CN"

fun resolveCoreText(context: Context, text: Text?, locale: String = DEFAULT_CORE_LOCALE): String =
    coreTextToMessageText(text).resolve { resolveUiMessage(context, it, locale) }

fun resolveHighlightedCoreText(
    context: Context,
    coreText: Text?,
    highlightArg: String,
    highlightStyle: SpanStyle,
    locale: String = DEFAULT_CORE_LOCALE,
): AnnotatedString {
    if (coreText == null || highlightArg.isEmpty()) {
        return AnnotatedString(resolveCoreText(context, coreText, locale))
    }
    if (coreText.key == UiMessages.homeWaitingCount(0).key) {
        val countArg = coreText.argsList.firstOrNull { it.name == highlightArg && it.hasIntValue() }
        if (countArg != null) {
            val count = countArg.intValue.toInt()
            val language = MessageLocale.resolve(locale, emptyList())
            val javaLocale = Locale.forLanguageTag(MessageLocale.appleTags.getValue(language))
            val configuration = Configuration(context.resources.configuration).apply { setLocale(javaLocale) }
            val resources = context.createConfigurationContext(configuration).resources
            val template = resources.getQuantityString(R.plurals.home_waiting_count, count)
            val match = INT_PLACEHOLDER_RE.find(template)
            if (match != null) {
                return buildAnnotatedString {
                    append(template.substring(0, match.range.first))
                    withStyle(highlightStyle) { append(String.format(javaLocale, "%d", count)) }
                    append(template.substring(match.range.last + 1))
                }
            }
        }
    }
    return AnnotatedString(resolveCoreText(context, coreText, locale))
}

fun Text?.isCoreTextNotEmpty(): Boolean =
    this != null && (key.isNotEmpty() || literal.isNotEmpty() || (hasTimestamp() && timestamp.unixMs > 0) || joinList.any { it.isCoreTextNotEmpty() })

@Composable
fun uiText(text: Text?): String = resolveCoreText(LocalContext.current, text, LocalMessageLocale.current)

@Composable
fun uiHighlightedText(text: Text?, highlightArg: String, highlightStyle: SpanStyle): AnnotatedString =
    resolveHighlightedCoreText(LocalContext.current, text, highlightArg, highlightStyle, LocalMessageLocale.current)
