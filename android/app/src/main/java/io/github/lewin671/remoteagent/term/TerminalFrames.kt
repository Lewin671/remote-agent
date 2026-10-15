package io.github.lewin671.remoteagent.term

import io.github.lewin671.remoteagent.proto.TerminalFrame
import java.util.ArrayDeque

/** One queue per connection: stale readers cannot affect the next connection's accounting. */
internal class TerminalFrames(private val maxBytes: Long = 2L * 1024 * 1024, private val maxFrames: Int = 512) {
    enum class Offer { QUEUED, SCHEDULE, OVERFLOW, CLOSED }
    private val frames = ArrayDeque<TerminalFrame>()
    private var bytes = 0L
    private var scheduled = false
    private var closed = false
    @Synchronized fun offer(frame: TerminalFrame): Offer {
        if (closed) return Offer.CLOSED
        val n = size(frame)
        if (bytes + n > maxBytes || frames.size >= maxFrames) {
            closed = true
            frames.clear()
            bytes = 0
            return Offer.OVERFLOW
        }
        frames.addLast(frame)
        bytes += n
        if (scheduled) return Offer.QUEUED
        scheduled = true
        return Offer.SCHEDULE
    }
    @Synchronized fun drain(maxBatchBytes: Long = 64 * 1024): Pair<List<TerminalFrame>, Boolean> {
        val batch = ArrayList<TerminalFrame>()
        var n = 0L
        while (frames.isNotEmpty() && (batch.isEmpty() || n + size(frames.first) <= maxBatchBytes)) {
            val f = frames.removeFirst()
            batch += f
            n += size(f)
        }
        bytes -= n
        scheduled = frames.isNotEmpty()
        return batch to scheduled
    }
    @Synchronized fun close() { closed = true; frames.clear(); bytes = 0; scheduled = false }
    private fun size(f: TerminalFrame): Long = when (f.kindCase) {
        TerminalFrame.KindCase.OUTPUT -> f.output.size().toLong()
        TerminalFrame.KindCase.SNAPSHOT -> f.snapshot.data.size().toLong()
        TerminalFrame.KindCase.HISTORY_PAGE -> f.historyPage.serializedSize.toLong()
        else -> 0
    }
}
