package io.github.lewin671.remoteagent.core

internal suspend fun retryRecoveryPublication(work: suspend () -> Boolean) {
    repeat(5) { if (work()) return }
    throw CommandException("aborted", UiMessages.errorsTheRecoveryObjectWasUpdatedConcurrentlyRetryingOnThe().messageText())
}
