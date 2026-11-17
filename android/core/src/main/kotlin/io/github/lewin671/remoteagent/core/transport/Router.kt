package io.github.lewin671.remoteagent.core.transport

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineExceptionHandler
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.launch

internal sealed interface DualLegResult<out T> {
    data class Trusted<T>(val value: T) : DualLegResult<T>
    data class Untrusted(val error: Throwable) : DualLegResult<Nothing>
}

/** Selects a transport path and merges dual-sent interactive legs by taking the
 * first trusted daemon outcome and ignoring late arrivals. */
internal class Router(val path: Path) {
    fun status(machineId: String) = path.status(machineId)

    /** Runs primary (direct) and fallback (relay) legs concurrently with the
     * same sealed bytes. The first trusted result wins immediately and cancels
     * the other leg; late arrivals never overwrite the winner. If neither leg
     * yields a trusted result, the fallback failure is rethrown. */
    suspend fun <T> dual(
        primary: suspend CoroutineScope.() -> DualLegResult<T>,
        fallback: suspend CoroutineScope.() -> DualLegResult<T>,
    ): T {
        val parentCtx = currentCoroutineContext()
        // Run legs on an independent SupervisorJob so a stuck non-interruptible losing
        // leg cannot delay returning a fast trusted winner, while still propagating
        // caller cancellation downward.
        val callScope = CoroutineScope(
            parentCtx.minusKey(Job) +
                SupervisorJob() +
                CoroutineExceptionHandler { _, _ -> },
        )
        val lock = Any()
        var settled = false
        var pending = 2
        var primaryErr: Throwable? = null
        var fallbackErr: Throwable? = null
        val winner = CompletableDeferred<T>()
        lateinit var primaryJob: Job
        lateinit var fallbackJob: Job

        fun finish(isPrimary: Boolean, res: DualLegResult<T>) {
            var cancelOther: Job? = null
            var failWith: Throwable? = null
            synchronized(lock) {
                if (settled) return
                when (res) {
                    is DualLegResult.Trusted -> {
                        settled = true
                        cancelOther = if (isPrimary) fallbackJob else primaryJob
                    }
                    is DualLegResult.Untrusted -> {
                        if (isPrimary) primaryErr = res.error else fallbackErr = res.error
                        pending--
                        if (pending == 0) {
                            settled = true
                            failWith = fallbackErr ?: primaryErr
                        }
                    }
                }
            }
            if (res is DualLegResult.Trusted) {
                cancelOther?.cancel()
                winner.complete(res.value)
            } else if (failWith != null) {
                winner.completeExceptionally(failWith!!)
            }
        }

        primaryJob = callScope.launch(start = CoroutineStart.LAZY) {
            val res = try {
                primary()
            } catch (e: CancellationException) {
                finish(isPrimary = true, DualLegResult.Untrusted(e))
                throw e
            } catch (t: Throwable) {
                DualLegResult.Untrusted(t)
            }
            finish(isPrimary = true, res)
        }
        fallbackJob = callScope.launch(start = CoroutineStart.LAZY) {
            val res = try {
                fallback()
            } catch (e: CancellationException) {
                finish(isPrimary = false, DualLegResult.Untrusted(e))
                throw e
            } catch (t: Throwable) {
                DualLegResult.Untrusted(t)
            }
            finish(isPrimary = false, res)
        }
        val parentHandle = parentCtx[Job]?.invokeOnCompletion { cause ->
            callScope.cancel(cause as? CancellationException ?: CancellationException("Parent cancelled", cause))
        }
        primaryJob.start()
        fallbackJob.start()
        return try {
            winner.await()
        } finally {
            parentHandle?.dispose()
            callScope.cancel()
        }
    }
}
