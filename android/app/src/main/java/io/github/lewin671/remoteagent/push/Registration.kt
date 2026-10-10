package io.github.lewin671.remoteagent.push

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
        missing -> PushAttempt(PushStatus("Google Play 服务不可用，请安装或启用后重试", "重试"))
        permanent -> PushAttempt(PushStatus("Google 推送注册未通过验证，请检查 Google Play 服务或应用的推送配置", "重试"))
        else -> PushAttempt(PushStatus("暂时连不上 Google 推送服务，正在自动重试。请检查网络和 Google Play 服务", "重试"), retry = true)
    }
}
