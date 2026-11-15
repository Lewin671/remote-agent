package io.github.lewin671.remoteagent.core.transport

import kotlinx.coroutines.async
import kotlinx.coroutines.supervisorScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.selects.select
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
    fun take() { tokens-- }
}
private val processBudget = RecoveryBudget()
internal class PathChanged : Exception()

/** One target owns its backoff across network generations. Global credit is
 * checked and debited atomically with local credit, never reset by hints. */
internal class Recovery(
    private val now: () -> Long = ::monotonicMillis,
    private val random: () -> Double = { Random.nextDouble() },
    private val global: RecoveryBudget = processBudget,
) {
    private val target = RecoveryBudget(2.0, 1.0)
    private var failures = 0
    private var retryAt = 0L
    private val generation = MutableStateFlow(0L)
    fun delay(): Long = synchronized(global) { delayAt(now()) }
    private fun delayAt(at: Long) = maxOf(target.delay(at), global.delay(at), retryAt - at, 0)
    fun take(): Boolean = synchronized(global) {
        if (delayAt(now()) > 0) false else { target.take(); global.take(); true }
    }
    fun failed() { retryAt = now() + (random() * minOf(1000L shl minOf(failures++, 5), 30_000)).toLong() }
    fun ready() { failures = 0; retryAt = 0; generation.value++ }
    suspend fun <T> follow(send: suspend () -> T): T = supervisorScope {
        val before = generation.value
        val delivery = async { send() }
        val changed = async { generation.first { it != before } }
        try { select { delivery.onAwait { it }; changed.onAwait { throw PathChanged() } } }
        finally { delivery.cancel(); changed.cancel() }
    }
}

internal class LegLiveness(initialNow: Long, private val clock: () -> Long = ::monotonicMillis) {
    private var lastFrameAt = initialNow
    private var heartbeats = 0
    private var lastHeartbeatAt = 0L
    private var observedIntervalMs = 0L

    @Synchronized
    fun onFrame(now: Long = clock(), isHeartbeat: Boolean = false) {
        if (now > lastFrameAt) lastFrameAt = now
        if (!isHeartbeat) return
        heartbeats++
        if (heartbeats >= 2) {
            observedIntervalMs = maxOf(0L, now - lastHeartbeatAt)
        }
        lastHeartbeatAt = now
    }

    @Synchronized
    fun thresholdMs(): Long {
        if (heartbeats < 2) return 60_000L
        return maxOf(3_000L, 3L * observedIntervalMs)
    }

    @Synchronized
    fun suspect(now: Long = clock()): Boolean = now - lastFrameAt >= thresholdMs()
}

/**
 * Coordinates per-terminal reopen backoff (1/2/4..30s full jitter, target 2/1s)
 * sharing the process-wide token bucket (4/2s).
 */
class TerminalRecovery internal constructor(
    now: () -> Long = ::monotonicMillis,
    random: () -> Double = { Random.nextDouble() },
    global: RecoveryBudget = processBudget,
) {
    constructor() : this(::monotonicMillis, { Random.nextDouble() }, processBudget)

    private val recovery = Recovery(now = now, random = random, global = global)
    private var reopenedInStreak = false

    fun recordFailureIfUnstabilized() {
        if (reopenedInStreak) {
            recovery.failed()
        }
    }

    fun takeOrDelayMs(): Long? {
        if (recovery.take()) {
            reopenedInStreak = true
            return null
        }
        return maxOf(1L, recovery.delay())
    }

    fun delayMs(): Long = recovery.delay()

    fun onStabilized() {
        reopenedInStreak = false
        recovery.ready()
    }

    fun reset() {
        reopenedInStreak = false
        recovery.ready()
    }
}
