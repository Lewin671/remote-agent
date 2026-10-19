package io.github.lewin671.remoteagent.core

internal suspend fun retryRecoveryPublication(work: suspend () -> Boolean) {
    repeat(5) { if (work()) return }
    throw CommandException("aborted", "恢复对象同时更新，将在下次同步重试")
}
