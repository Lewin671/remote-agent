package io.github.lewin671.remoteagent.push

import io.github.lewin671.remoteagent.ui.sessionText

import io.github.lewin671.remoteagent.core.UiMessages

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.launch
import kotlin.coroutines.coroutineContext

/** Whether push works, and what the user can do about it. */
data class PushStatus(val problem: String = "", val action: String = "")

internal data class PushAttempt(val status: PushStatus, val retry: Boolean = false)

/** One registration at a time; a new token/settings change supersedes old retries. */
internal class PushRegistration(private val scope: CoroutineScope) {
    private var job: Job? = null
    private var closed = false

    fun request(attempt: suspend () -> PushAttempt, publish: (PushStatus) -> Unit, autoRetry: Boolean = true) {
        stop()
        if (closed) return
        job = scope.launch {
            var backoff = 30_000L
            while (true) {
                val result = attempt()
                coroutineContext.ensureActive()
                publish(result.status)
                if (!result.retry || !autoRetry) break
                delay(backoff)
                backoff = (backoff * 2).coerceAtMost(300_000L)
            }
        }
    }

    fun stop() {
        job?.cancel()
        job = null
    }

    /**
     * Stops, and refuses every request until [open]: the account is being
     * removed, and whatever still holds it (a network callback, a return to
     * the foreground, a new token) must not register it again.
     */
    fun close() {
        closed = true
        stop()
    }

    /** An account is in use again. */
    fun open() {
        closed = false
    }
}

/** Inspect causes: Firebase wraps IO failures in ExecutionException. */
internal fun tokenFailure(error: Throwable): PushAttempt {
    val codes = generateSequence(error) { it.cause }.take(16).mapNotNull { it.message }.toSet()
    val permanent = codes.any { it in setOf("AUTHENTICATION_FAILED", "FIS_AUTH_ERROR", "INVALID_PARAMETERS") }
    val missing = "MISSING_INSTANCEID_SERVICE" in codes
    return when {
        missing -> PushAttempt(PushStatus(sessionText(UiMessages.notificationsAndroidGooglePlayServicesAreUnavailableInstallOrEnableThemThen()), sessionText(UiMessages.commonRetry())))
        permanent -> PushAttempt(PushStatus(sessionText(UiMessages.notificationsAndroidGooglePushRegistrationCouldNotBeVerifiedCheckGooglePlay()), sessionText(UiMessages.commonRetry())))
        else -> PushAttempt(PushStatus(sessionText(UiMessages.notificationsAndroidCannotReachGooglePushServicesRetryingAutomaticallyCheckYourNetwork()), sessionText(UiMessages.commonRetry())), retry = true)
    }
}
