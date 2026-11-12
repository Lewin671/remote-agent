package io.github.lewin671.remoteagent.core

import java.io.File
import kotlinx.serialization.json.*

private val messageSource by lazy {
    File("../../i18n/messages").listFiles()!!.filter { it.extension == "json" }
        .flatMap { Json.parseToJsonElement(it.readText()).jsonArray }
        .associate { it.jsonObject.getValue("key").jsonPrimitive.content to it.jsonObject }
}

fun messageTextForTest(message: UiMessage, locale: String = "zh-CN"): String {
    val entry = messageSource.getValue(message.key).getValue(locale)
    val text = if (entry is JsonPrimitive) entry.content else {
        val obj = entry.jsonObject
        val selector = (obj["plural"] ?: obj.getValue("select")).jsonPrimitive.content
        val choice = if ("plural" in obj) {
            if (locale == "en" && message.args.getValue(selector) == 1) "one" else "other"
        } else message.args.getValue(selector).toString()
        val forms = obj.getValue("forms").jsonObject
        (forms[choice] ?: forms.getValue("other")).jsonPrimitive.content
    }
    return Regex("\\{(\\w+)\\}").replace(text) { message.args.getValue(it.groupValues[1]).toString() }
}

fun MessageText.testText(locale: String = "zh-CN"): String = resolve { messageTextForTest(it, locale) }

/** Compare the whole visible value while retaining row identity, tone and behavior assertions. */
fun assertMessageEquals(expected: Any?, actual: Any?) {
    fun rendered(value: Any?): Any? = when (value) {
        is UiMessage -> messageTextForTest(value)
        is MessageText -> value.testText()
        is SessionLookup.Failed -> listOf("failed", rendered(value.reason))
        is QuotaWindowDescriptor -> listOf(rendered(value.label), rendered(value.group))
        is List<*> -> value.map { rendered(it) }
        is HomeItem.Tail -> rendered(value.tail)
        is HomeTail.Text -> listOf("text", rendered(value.text))
        is HomeTail.Earlier -> listOf("earlier", rendered(value.text), value.retry)
        is ApprovalCardText -> listOf(rendered(value.kind), rendered(value.time), value.hot, rendered(value.title), rendered(value.place))
        is HomeHeadline -> listOf(value.kind, rendered(value.title), rendered(value.sub), value.subError, value.subPath, value.details)
        is SessionRowText -> listOf(rendered(value.title), rendered(value.time), value.status, rendered(value.preview), rendered(value.meta), value.agent)
        is PendingRowText -> listOf(rendered(value.title), rendered(value.kind), rendered(value.summary))
        else -> value
    }
    org.junit.Assert.assertEquals(rendered(expected), rendered(actual))
}

fun MessageText.contains(value: String): Boolean = testText().contains(value)
fun MessageText.startsWith(value: String): Boolean = testText().startsWith(value)
fun MessageText.endsWith(value: String): Boolean = testText().endsWith(value)

fun assertMessageEquals(message: String?, expected: Any?, actual: Any?) {
    try { assertMessageEquals(expected, actual) } catch (e: AssertionError) { throw AssertionError(message, e) }
}
fun assertMessageEquals(expected: Double, actual: Double, delta: Double) = org.junit.Assert.assertEquals(expected, actual, delta)
