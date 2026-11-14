package io.github.lewin671.remoteagent.core

/** Shared evidence for one immutable authorization, including preparing copies. */
internal class DispatchState(prior: Boolean) {
    private var priorUnknown = prior
    private var possible = 0
    private var active = 0
    val unresolved: Boolean get() = synchronized(this) { priorUnknown || possible > 0 || active > 0 }
    val hasPrior: Boolean get() = synchronized(this) { priorUnknown || possible > 0 }
    fun open(prior: Boolean): Call = synchronized(this) {
        priorUnknown = priorUnknown || prior
        active++
        Call()
    }
    inner class Call {
        private var closed = false
        val uncertain: Boolean get() = synchronized(this@DispatchState) { priorUnknown || possible > 0 || active > if (closed) 0 else 1 }
        val hasPrior: Boolean get() = this@DispatchState.hasPrior
        fun delivery() = Delivery()
        fun close() = synchronized(this@DispatchState) { if (!closed) { closed = true; active-- } }
        inner class Delivery {
            private var registered = false
            private var settled = false
            fun dispatch() = synchronized(this@DispatchState) {
                check(!registered && !settled && !closed)
                registered = true
                possible++
            }
            fun notForwarded(): Boolean = synchronized(this@DispatchState) {
                if (!settled) { settled = true; if (registered) possible-- }
                uncertain
            }
        }
    }
}
