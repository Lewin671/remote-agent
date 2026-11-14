package io.github.lewin671.remoteagent.core.transport

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/** A network generation can cancel an obsolete HTTP round without canceling
 * its security/credential owner or any caller's admitted operation. */
internal class NetworkRound(
    private val mutex: Mutex,
    private val afterRetirement: () -> Unit = {},
) {
    private val lock = Any()
    private var generation = 0L
    private var active: Job? = null
    fun changed() {
        val old = synchronized(lock) { generation++; active.also { active = null } }
        // The scheduling hook runs outside the lock; even a new owner installed
        // here must never receive the preceding generation's cancellation.
        afterRetirement()
        old?.cancel()
    }
    suspend fun <T> run(block: suspend () -> T): T {
        val caller = currentCoroutineContext()
        while (true) {
            var before = -1L
            try {
                return coroutineScope {
                    mutex.withLock {
                        val job = currentCoroutineContext()[Job]!!
                        synchronized(lock) { before = generation; active = job }
                        try { currentCoroutineContext().ensureActive(); block() }
                        finally { synchronized(lock) { if (active === job) active = null } }
                    }
                }
            } catch (error: CancellationException) {
                caller.ensureActive()
                if (synchronized(lock) { before == generation }) throw error
            }
        }
    }
}
