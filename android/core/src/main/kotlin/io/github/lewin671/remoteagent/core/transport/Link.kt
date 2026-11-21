package io.github.lewin671.remoteagent.core.transport

import io.github.lewin671.remoteagent.core.*
import java.util.concurrent.atomic.AtomicBoolean

internal class Link(val path: Path) {
    private val streams = mutableSetOf<Stream>()
    private var closed = false

    fun openStream(stop: (MessageText?) -> Unit, onClose: (MessageText) -> Unit): Stream {
        val stream = Stream(stop, onClose)
        val registered = synchronized(streams) {
            if (closed) false else {
                streams.add(stream)
                true
            }
        }
        if (!registered) stream.end(null)
        return stream
    }

    inner class Stream(private val stop: (MessageText?) -> Unit, private val onClose: (MessageText) -> Unit) {
        private val ended = AtomicBoolean(false)
        fun end(reason: MessageText?) {
            if (!ended.compareAndSet(false, true)) return
            synchronized(streams) { streams.remove(this) }
            cleanup({ stop(reason) }, { if (reason != null) onClose(reason) })
        }
    }

    fun closeStreams() {
        val owned = synchronized(streams) {
            closed = true
            streams.toList()
        }
        cleanup(*owned.map { stream -> { stream.end(null) } }.toTypedArray())
    }
}
