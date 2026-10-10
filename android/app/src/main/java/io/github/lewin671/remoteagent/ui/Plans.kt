package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Attempt
import io.github.lewin671.remoteagent.core.PreparedCommand
import io.github.lewin671.remoteagent.proto.PlanChoice

/** The dock and full page retry an unknown plan reply as the same request. */
object Plans {
    class Reply {
        val attempt = Attempt()
        var choice = PlanChoice.PLAN_CHOICE_UNSPECIFIED
    }

    private val bySession = HashMap<String, Reply>()
    fun of(sid: String): Reply = bySession.getOrPut(sid) { Reply() }
    fun clear() = bySession.clear()

    suspend fun send(
        reply: Reply, choice: PlanChoice,
        prepare: (PlanChoice) -> PreparedCommand,
        forward: suspend (PreparedCommand, Boolean) -> Unit,
    ) {
        try {
            val retry = reply.attempt.frozen
            val command = reply.attempt.next { reply.choice = choice; prepare(choice) }
            forward(command, retry)
            reply.attempt.settle()
        } catch (e: Exception) {
            throw reply.attempt.settle(e) ?: e
        }
    }
}
