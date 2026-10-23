package io.github.lewin671.remoteagent.ui

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

    suspend fun update(
        events: List<Event>,
        derived: Derived,
        machine: String,
        cwd: String = "",
        home: String = "",
        agent: Agent = Agent.AGENT_CODEX,
        textOf: (String) -> String? = { null },
        who: (Origin?) -> String? = { null },
    ): List<ChatItem> = withContext(dispatcher) {
        lock.withLock { projection.update(events, derived, machine, cwd, home, agent, textOf, who) }
    }
}
