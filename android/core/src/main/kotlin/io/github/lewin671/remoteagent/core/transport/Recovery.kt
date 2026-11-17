package io.github.lewin671.remoteagent.core.transport

import kotlin.math.ceil
import kotlin.random.Random

internal fun monotonicMillis(): Long = System.nanoTime() / 1_000_000

internal class RecoveryBudget(private val capacity: Double = 4.0, private val rate: Double = 2.0) {
    private var tokens = capacity
    private var at: Long? = null

    fun delay(now: Long): Long {
        val last = at ?: now
        tokens = minOf(capacity, tokens + maxOf(0, now - last) * rate / 1000)
        at = maxOf(now, last)
        return ceil(maxOf(0.0, (1 - tokens) * 1000 / rate)).toLong()
    }

    fun take() {
        tokens--
    }

    internal fun resetForTest() {
        tokens = capacity
        at = null
    }
}

private val processBudget = RecoveryBudget()

fun resetProcessBudgetForTest() {
    synchronized(processBudget) { processBudget.resetForTest() }
}

internal class PathChanged : Exception()

/**
 * Coordinates per-terminal reopen backoff (1/2/4..30s full jitter, target 2/1s)
 * sharing the process-wide token bucket (4/2s).
 */
class TerminalRecovery internal constructor(
    private val now: () -> Long,
    private val random: () -> Double,
    private val global: RecoveryBudget,
) {
    constructor(now: () -> Long) : this(now, { Random.nextDouble() }, processBudget)

    private val target = RecoveryBudget(2.0, 1.0)
    private var failures = 0
    private var retryAt = 0L
    private var reopenedInStreak = false

    private fun delayAt(at: Long) = maxOf(target.delay(at), global.delay(at), retryAt - at, 0)

    @Synchronized
    fun recordFailureIfUnstabilized() {
        if (reopenedInStreak) {
            synchronized(global) {
                retryAt = now() + (random() * minOf(1000L shl minOf(failures++, 5), 30_000)).toLong()
            }
        }
    }

    @Synchronized
    fun takeOrDelayMs(): Long? = synchronized(global) {
        val wait = delayAt(now())
        if (wait > 0) {
            maxOf(1L, wait)
        } else {
            target.take()
            global.take()
            reopenedInStreak = true
            null
        }
    }

    @Synchronized
    fun delayMs(): Long = synchronized(global) { delayAt(now()) }

    @Synchronized
    fun onStabilized() {
        reopenedInStreak = false
        synchronized(global) {
            failures = 0
            retryAt = 0
        }
    }

    @Synchronized
    fun reset() {
        reopenedInStreak = false
        synchronized(global) {
            failures = 0
            retryAt = 0
        }
    }

    companion object {
        fun forTest(now: () -> Long, random: () -> Double): TerminalRecovery =
            TerminalRecovery(now, random, RecoveryBudget())
    }
}
