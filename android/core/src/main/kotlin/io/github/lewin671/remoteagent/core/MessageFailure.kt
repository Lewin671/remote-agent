package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.proto.ImageProblemKind

/** Product errors carry keys to the UI. Throwable.message is only a diagnostic identity. */
interface MessageFailure {
    val display: MessageText
    val imageKind: ImageProblemKind? get() = null
}

open class MessageException(override val display: MessageText, cause: Throwable? = null, override val imageKind: ImageProblemKind? = null) :
    IllegalStateException("local_message", cause), MessageFailure

fun requireMessage(value: Boolean, message: () -> MessageText) {
    if (!value) throw MessageArgumentException(message())
}

class MessageArgumentException(override val display: MessageText, override val imageKind: ImageProblemKind? = null) : IllegalArgumentException("local_argument"), MessageFailure

class MessageIOException(override val display: MessageText, cause: Throwable? = null) : java.io.IOException("local_io", cause), MessageFailure
