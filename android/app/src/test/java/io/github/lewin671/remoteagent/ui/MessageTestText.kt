package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*

fun messageTextForTest(message: UiMessage, locale: String = "zh-CN"): String =
    resolveUiMessage(org.robolectric.RuntimeEnvironment.getApplication(), message, locale)

fun MessageText.testText(locale: String = "zh-CN"): String = resolve { messageTextForTest(it, locale) }

/** Compare the whole visible value while retaining row identity, tone and behavior assertions. */
fun assertMessageEquals(expected: Any?, actual: Any?) {
    fun rendered(value: Any?): Any? = when (value) {
        is UiMessage -> messageTextForTest(value)
        is MessageText -> value.testText()
        is List<*> -> value.map { rendered(it) }
        else -> value
    }
    org.junit.Assert.assertEquals(rendered(expected), rendered(actual))
}
