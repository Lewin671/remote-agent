package io.github.lewin671.remoteagent.core

/**
 * Retrying a command whose outcome is unknown must send exactly the same
 * thing again: the same envelope under the same request ID and deadline, so
 * the daemon answers from its log instead of running it twice
 * (architecture §10). This holds the command while its outcome is unknown.
 *
 * Once such a command has expired, whether it ran stays unknown: the daemon
 * only remembers a request until shortly after its deadline. The attempt
 * then stays stuck rather than sealing a new command, which could run the
 * same thing twice; only [abandon], the user's explicit choice, frees it.
 */
class Attempt {
    var pending: PreparedCommand? = null
        private set

    /** The next attempt repeats one whose outcome is unknown. */
    var frozen = false
        private set

    /** The unknown command expired: it is not sent again. */
    var expired = false
        private set

    /** What to send: an earlier command whose outcome is unknown wins over a new one. */
    fun next(make: () -> PreparedCommand): PreparedCommand {
        if (expired) throw CommandException(CommandException.UNKNOWN, EXPIRED_UNKNOWN, failureKind = CommandFailureKind.UNKNOWN)
        return pending ?: make().also { pending = it }
    }

    /**
     * Records how the command ended and returns the error to show. Success or
     * a definite failure frees the next attempt; an unknown outcome freezes
     * it; an expiry after an unknown outcome leaves it frozen and expired.
     */
    fun settle(error: Throwable? = null): Throwable? {
        when {
            error == null -> abandon()
            uncertain(error) -> frozen = true
            frozen && expiry(error) -> {
                expired = true
                return CommandException(CommandException.UNKNOWN, EXPIRED_UNKNOWN, failureKind = CommandFailureKind.UNKNOWN)
            }
            // Once unknown, only the daemon's own answer settles it; any
            // other failure (a network error, a failed login) leaves it
            // unknown, to be retried under the same request ID.
            frozen && !(error is CommandException && error.fromDaemon) ->
                return CommandException(CommandException.UNKNOWN, localized { copy -> copy(UiMessages.errorsAndroidThePreviousOutcomeIsStillUnknownValue1RetryingSendsThe(errorText(error).resolve(copy))) }, failureKind = CommandFailureKind.UNKNOWN)
            else -> abandon()
        }
        return error
    }

    /** Gives up on an unknown command, e.g. to start over with other content. */
    fun abandon() {
        pending = null
        frozen = false
        expired = false
    }

    companion object {
        val EXPIRED_UNKNOWN = UiMessages.errorsAndroidThePreviousOutcomeIsUnknownTheRequestExpiredAndWill().messageText()

        private fun expiry(e: Throwable) =
            e is CommandException && (e.code == CommandException.EXPIRED || (e.fromDaemon && e.code == "deadline_exceeded"))
    }
}

/** The command may or may not have run. */
fun uncertain(e: Throwable): Boolean = e is CommandException && e.unknown
