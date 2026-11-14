package io.github.lewin671.remoteagent.core.transport

import java.util.concurrent.atomic.AtomicLong
import java.util.concurrent.atomic.AtomicReference
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/** A network generation can cancel an obsolete HTTP round without canceling
 * its security/credential owner or any caller's admitted operation. */
internal class NetworkRound(private val mutex: Mutex) {
    private val generation = AtomicLong()
    private val active = AtomicReference<Job?>()
    fun changed() { generation.incrementAndGet(); active.get()?.cancel() }
    suspend fun <T> run(block: suspend () -> T): T {
        val caller = currentCoroutineContext()
        while (true) {
            val before = generation.get()
            try {
                return coroutineScope {
                    mutex.withLock {
                        val job = currentCoroutineContext()[Job]!!
                        active.set(job)
                        try { currentCoroutineContext().ensureActive(); block() }
                        finally { active.compareAndSet(job, null) }
                    }
                }
            } catch (error: CancellationException) {
                caller.ensureActive()
                if (before == generation.get()) throw error
            }
        }
    }
}
