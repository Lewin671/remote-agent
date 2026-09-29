package io.github.lewin671.remoteagent.core

/**
 * Retrying a command whose outcome is unknown must send exactly the same
 * thing again: the same envelope under the same request ID and deadline, so
 * the daemon answers from its log instead of running it twice
 * (architecture §10). This holds the command while its outcome is unknown,
 * as web/src/attempt.ts does.
 */
class Attempt {
    var pending: PreparedCommand? = null
        private set

    /** What to send: an earlier command whose outcome is unknown wins over a new one. */
    fun next(make: () -> PreparedCommand): PreparedCommand = pending ?: make().also { pending = it }

    /**
     * Records how the command ended. Success or a definite failure frees the
     * next attempt; an unknown outcome freezes it. A frozen command that has
     * expired is not renewed: whether it ran stays unknown, and the returned
     * exception says so.
     */
    fun settle(error: Throwable? = null): Throwable? {
        val wasFrozen = pending != null && frozen
        if (error == null || !uncertain(error)) {
            pending = null
            frozen = false
        } else {
            frozen = true
        }
        if (wasFrozen && error is CommandException && error.code == CommandException.EXPIRED) {
            return CommandException(CommandException.UNKNOWN, "上一次的结果不确定，已经过期，不会再重发；请先确认它有没有执行")
        }
        return error
    }

    /** The next attempt repeats one whose outcome is unknown. */
    var frozen = false
        private set
}

/** The command may or may not have run. */
fun uncertain(e: Throwable): Boolean = e is CommandException && e.unknown
