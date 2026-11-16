package io.github.lewin671.remoteagent.core.transport

import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers

/** CPU lanes. Bulk work (sealing, opening and reassembling 512-768 KiB file
 * blocks) can keep every core busy for tens of milliseconds at a time. It runs
 * on at most cores-1 threads of the shared pool, so there is always a thread
 * left for interactive work (an approval's sealing and verification) instead
 * of queueing behind a block. docs/transport.md 「审批预留」: bulk cannot take
 * the interactive lane. */
internal object CpuLanes {
    val bulk: CoroutineDispatcher =
        Dispatchers.Default.limitedParallelism(maxOf(1, Runtime.getRuntime().availableProcessors() - 1))
}
