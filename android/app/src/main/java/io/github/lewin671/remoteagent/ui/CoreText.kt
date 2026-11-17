package io.github.lewin671.remoteagent.ui

import android.content.Context
import androidx.compose.runtime.Composable
import androidx.compose.ui.platform.LocalContext
import io.github.lewin671.remoteagent.core.coreTextToMessageText
import io.github.lewin671.remoteagent.core.proto.Text

fun resolveCoreText(context: Context, text: Text?, locale: String = "zh-CN"): String =
    coreTextToMessageText(text).resolve { resolveUiMessage(context, it, locale) }

fun Text?.isCoreTextNotEmpty(): Boolean =
    this != null && (key.isNotEmpty() || literal.isNotEmpty() || (hasTimestamp() && timestamp.unixMs > 0) || joinList.any { it.isCoreTextNotEmpty() })

@Composable
fun uiText(text: Text?): String = resolveCoreText(LocalContext.current, text, LocalMessageLocale.current)
