package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel

fun isDiskFull(error: Throwable): Boolean = error is CommandException && error.code == "disk_full"
fun recentMachineProject(meta: MachineMeta): String? = meta.projectsList.maxByOrNull { maxOf(it.latestCreatedMs, it.latestEndedMs) }?.path
fun machineDiskReminder(online: Boolean, meta: MachineMeta): DiskStatus? = meta.diskStatus.takeIf { online && meta.capabilities.machineStatus && meta.hasDiskStatus() && it.availableBytes < 10_000_000_000L }
fun stableUsage(previous: List<String>, rows: List<ResourceUsage>, memory: Boolean = false): List<ResourceUsage> {
    val byId = rows.associateBy { it.id }.toMutableMap()
    val ordered = previous.mapNotNull { byId.remove(it) }.toMutableList()
    fun metric(r: ResourceUsage): Double = if (memory) r.memoryBytes.toDouble() else r.cpuPercent
    ordered += byId.values.sortedWith(compareByDescending<ResourceUsage> { metric(it) }.thenBy { it.id })
    val threshold = if (memory) 50_000_000.0 else 2.0
    for (i in 1 until ordered.size) for (j in i downTo 1) {
        if (metric(ordered[j]) - metric(ordered[j-1]) <= threshold) break
        val row = ordered[j]; ordered[j] = ordered[j-1]; ordered[j-1] = row
    }
    return ordered
}
const val MACHINE_STATUS_TIMEOUT_MS = 15_000L
enum class MachinePollState { IDLE, LOADING, LIVE, STALE, UNAVAILABLE }
/** Confine to the owner's dispatcher (normally Main); cancel owner scope on disposal.
 * The same coroutine owns fetch and delay, so a slow/cancelled request never overlaps. */
class MachineStatusController(
    private val scope: CoroutineScope,
    private val fetch: suspend () -> MachineStatus,
    private val intervalMs: Long = 2_000,
    private val changed: () -> Unit = {},
    private val nowMillis: () -> Long = { System.nanoTime() / 1_000_000 },
) {
    var state = MachinePollState.IDLE; private set
    var last: MachineStatus? = null; private set
    var failures = 0; private set
    var curveSinceMs = 0L; private set
    private var active = false
    private var generation = 0L
    private var job: Job? = null
    private val wake = Channel<Unit>(Channel.CONFLATED)
    fun setActive(visible: Boolean, online: Boolean, supported: Boolean) {
        val next = visible && online && supported
        if (visible && supported && !online) { state = if(last==null) MachinePollState.UNAVAILABLE else MachinePollState.STALE; changed() }
        if (next == active) return
        active = next; generation++
        if (!active) { changed() }
        wake.trySend(Unit)
        if (active && job?.isActive != true) start()
    }
    fun retry() { wake.trySend(Unit)
        if (active && job?.isActive != true) start() }
    fun dispose() { setActive(false, false, false); job?.cancel() }
    private fun start() {
        job = scope.launch {
            while (currentCoroutineContext().isActive) {
                if (!active) { wake.receive(); continue }
                wake.tryReceive()
                val current = generation
                val started = nowMillis()
                if (last == null && state == MachinePollState.IDLE) state = MachinePollState.LOADING
                changed()
                try {
                    val value = withTimeoutOrNull(MACHINE_STATUS_TIMEOUT_MS) { fetch() } ?: throw MachineStatusTimeout()
                    if (current == generation && active) {
                        if (state == MachinePollState.STALE || state == MachinePollState.UNAVAILABLE) curveSinceMs = value.observedMs
                        last = value; failures = 0; state = MachinePollState.LIVE
                    }
                } catch (e: CancellationException) { throw e
                } catch (_: Exception) {
                    if (current == generation && active) {
                        failures = minOf(3, failures + 1)
                        if (last == null) state = MachinePollState.UNAVAILABLE
                        if (failures == 3) state = if (last == null) MachinePollState.UNAVAILABLE else MachinePollState.STALE
                    }
                }
                changed()
                val remaining = intervalMs - (nowMillis()-started)
                if (current == generation && active && remaining > 0) withTimeoutOrNull(remaining) { wake.receive() }
            }
        }
    }
}

private class MachineStatusTimeout : Exception()
