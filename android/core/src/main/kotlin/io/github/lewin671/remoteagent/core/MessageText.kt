package io.github.lewin671.remoteagent.core

/** Deferred display text: raw user/CLI content stays raw, product copy keeps its key and args.
 * Joining is only for independent list fields, never for grammatical sentence fragments. */
typealias MessageResolver = (UiMessage) -> String

sealed interface MessageText {
    class Computed(val value: (MessageResolver) -> String) : MessageText
    data class Raw(val value: String) : MessageText
    data class Message(val value: UiMessage) : MessageText

    fun resolve(message: (UiMessage) -> String): String = when (this) {
        is Computed -> value(message)
        is Raw -> value
        is Message -> message(value)
    }

    fun isEmpty(): Boolean = when (this) {
        is Computed -> false
        is Raw -> value.isEmpty()
        is Message -> false
    }
    fun isNotEmpty(): Boolean = !isEmpty()
}

fun String.messageText(): MessageText = MessageText.Raw(this)
fun UiMessage.messageText(): MessageText = MessageText.Message(this)

/** Resolve calculations only at the display boundary, with its current language. */
fun localized(block: (MessageResolver) -> String): MessageText = MessageText.Computed(block)
fun MessageText.messageText(): MessageText = this
fun MessageText?.orEmpty(): MessageText = this ?: "".messageText()
fun MessageText.ifEmpty(default: () -> MessageText): MessageText = if (isEmpty()) default() else this

fun MessageText?.isNullOrEmpty(): Boolean = this == null || isEmpty()
