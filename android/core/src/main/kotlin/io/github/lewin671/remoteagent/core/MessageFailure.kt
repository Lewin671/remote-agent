package io.github.lewin671.remoteagent.core

/** Product errors carry keys to the UI. Throwable.message is only a diagnostic identity. */
interface MessageFailure { val display: MessageText }

open class MessageException(override val display: MessageText, cause: Throwable? = null) :
    IllegalStateException("local_message", cause), MessageFailure

fun requireMessage(value: Boolean, message: () -> MessageText) {
    if (!value) throw MessageArgumentException(message())
}

fun messageError(message: MessageText): Nothing = throw MessageException(message)

class MessageArgumentException(override val display: MessageText) : IllegalArgumentException("local_argument"), MessageFailure

class MessageIOException(override val display: MessageText, cause: Throwable? = null) : java.io.IOException("local_io", cause), MessageFailure
