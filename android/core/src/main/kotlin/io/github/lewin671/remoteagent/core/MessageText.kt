package io.github.lewin671.remoteagent.core

/** Deferred display text: raw user/CLI content stays raw, product copy keeps its key and args.
 * Joining is only for independent list fields, never for grammatical sentence fragments. */
typealias MessageResolver = (UiMessage) -> String

sealed interface MessageText {
    class Computed(val value: (MessageResolver) -> String) : MessageText
    data class Raw(val value: String) : MessageText
    data class Message(val value: UiMessage) : MessageText
    data class Joined(val values: List<MessageText>, val separator: String) : MessageText

    fun resolve(message: (UiMessage) -> String): String = when (this) {
        is Computed -> value(message)
        is Raw -> value
        is Message -> message(value)
        is Joined -> values.map { it.resolve(message) }.filter(String::isNotEmpty).joinToString(separator)
    }

    fun isEmpty(): Boolean = when (this) {
        is Computed -> false
        is Raw -> value.isEmpty()
        is Message -> false
        is Joined -> values.all { it.isEmpty() }
    }
    fun isNotEmpty(): Boolean = !isEmpty()
}

fun String.messageText(): MessageText = MessageText.Raw(this)
fun UiMessage.messageText(): MessageText = MessageText.Message(this)
fun List<MessageText>.joinMessages(separator: String): MessageText = MessageText.Joined(this, separator)

fun MessageText.spokenText(): MessageText = when (this) {
    is MessageText.Computed -> this
    is MessageText.Raw -> value.replace(" · ", "，").messageText()
    is MessageText.Message -> this
    is MessageText.Joined -> values.map { it.spokenText() }.joinMessages(if (separator == " · ") "，" else separator)
}

/** Resolve calculations only at the display boundary, with its current language. */
fun localized(block: (MessageResolver) -> String): MessageText = MessageText.Computed(block)
fun MessageText.messageText(): MessageText = this
fun MessageText?.orEmpty(): MessageText = this ?: "".messageText()
fun MessageText.ifEmpty(default: () -> MessageText): MessageText = if (isEmpty()) default() else this

fun MessageText?.isNullOrEmpty(): Boolean = this == null || isEmpty()
