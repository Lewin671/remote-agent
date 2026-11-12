package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.DEFAULT_AGENT
import io.github.lewin671.remoteagent.core.Derived
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.Origin
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext

/** Owns the mutable projection; callers supply snapshots and receive immutable row lists. */
class ChatProjectionWorker(private val dispatcher: CoroutineDispatcher = Dispatchers.Default) {
    private val lock = Mutex()
    private val projection = ChatProjection()
    private val fold = ScheduledFold()
    private var raw: List<ChatItem> = emptyList()
    private var source: List<Event> = emptyList()
    private var updated = false

    /**
     * The rows as shown, quiet scheduled runs folded, and the events they are made of. Of two
     * results the one with the later [version] is the newer, from whichever worker.
     */
    class Rows(val items: List<ChatItem>, val events: List<Event>, val version: Long) {
        companion object {
            val EMPTY = Rows(emptyList(), emptyList(), 0)
            private val versions = java.util.concurrent.atomic.AtomicLong()
            internal fun next() = versions.incrementAndGet()
        }
    }

    suspend fun update(
        events: List<Event>,
        derived: Derived,
        machine: String,
        cwd: String = "",
        home: String = "",
        agent: Agent = DEFAULT_AGENT,
        copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText,
        textOf: (String) -> String? = { null },
        who: (Origin?) -> ChatSource? = { null },
    ): List<ChatItem> = withContext(dispatcher) {
        lock.withLock {
            projection.update(events, derived, machine, cwd, home, agent, copy, textOf, who).also {
                raw = it
                source = events
                updated = true
            }
        }
    }

    /**
     * The last [update]'s rows with quiet scheduled runs folded, except the one [reveal] names;
     * null before the first [update], so a new worker never replaces shown rows with nothing.
     */
    suspend fun rows(reveal: () -> Long = { 0 }): Rows? = withContext(dispatcher) {
        lock.withLock {
            if (!updated) null else Rows(fold.fold(raw, source, reveal()), source, Rows.next())
        }
    }
}
