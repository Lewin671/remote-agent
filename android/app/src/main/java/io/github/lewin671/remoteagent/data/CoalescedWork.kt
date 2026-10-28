package io.github.lewin671.remoteagent.data

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.coroutines.launch

/** Requests from one save/remove batch share a deferred pass; requests during a pass get another. */
internal class CoalescedWork(scope: CoroutineScope, quietMs: Long = 100, work: suspend () -> Unit) {
    private val requests = Channel<Unit>(Channel.CONFLATED)
    init {
        scope.launch {
            for (request in requests) {
                while (withTimeoutOrNull(quietMs) { requests.receive() } != null) { /* wait for the batch to settle */ }
                try { work() }
                catch (e: CancellationException) { throw e }
                catch (_: Exception) { /* Cleanup is best effort; a later request retries it. */ }
            }
        }
    }
    fun request() { requests.trySend(Unit) }
}
