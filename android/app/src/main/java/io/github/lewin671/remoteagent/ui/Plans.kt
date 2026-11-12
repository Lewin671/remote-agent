package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import io.github.lewin671.remoteagent.core.Attempt
import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.PreparedCommand
import io.github.lewin671.remoteagent.proto.PlanChoice

/**
 * Replies to plans sent from here, per session, for this run. One whose
 * outcome is unknown is only ever sent again unchanged, the same request
 * (architecture §10), and only by a button that names its choice: while it
 * is unknown no other choice is offered or sent, until the user gives it
 * up. The dock and the full page share it.
 */
object Plans {
    class Reply {
        internal val attempt = Attempt()
        /** The choice of the reply being sent, or of the one whose outcome is unknown. */
        var choice by mutableStateOf(PlanChoice.PLAN_CHOICE_UNSPECIFIED)
            internal set
        /** The outcome is unknown: only the same sealed command is sent again. */
        var frozen by mutableStateOf(false)
            internal set
        /** The unknown reply expired and is not sent again. */
        var expired by mutableStateOf(false)
            internal set
    }

    private val bySession = HashMap<String, Reply>()
    fun of(sid: String): Reply = bySession.getOrPut(sid) { Reply() }
    fun clear() = bySession.clear()

    /** Sends [choice] as a new reply. Refused while an earlier reply is unknown: that one is retried or given up first. */
    suspend fun send(
        reply: Reply, choice: PlanChoice,
        prepare: (PlanChoice) -> PreparedCommand, forward: suspend (PreparedCommand, Boolean) -> Unit) {
        // Never the earlier command under this choice's name.
        if (reply.attempt.frozen) throw CommandException("failed_precondition", io.github.lewin671.remoteagent.core.MessageText.Message(UiMessages.dockAndroidReplyPending()))
        run(reply, forward) { reply.choice = choice; prepare(choice) }
    }

    /** Sends the reply whose outcome is unknown again, as it was. */
    suspend fun retry(reply: Reply, forward: suspend (PreparedCommand, Boolean) -> Unit) {
        if (!reply.attempt.frozen) return
        run(reply, forward) { error("an unknown reply keeps its command") }
    }

    private suspend fun run(reply: Reply, forward: suspend (PreparedCommand, Boolean) -> Unit, make: () -> PreparedCommand) {
        try {
            val retry = reply.attempt.frozen
            forward(reply.attempt.next(make), retry)
            reply.attempt.settle()
        } catch (e: Exception) {
            throw reply.attempt.settle(e) ?: e
        } finally {
            reply.frozen = reply.attempt.frozen
            reply.expired = reply.attempt.expired
        }
    }

    /** Gives up on a reply whose outcome is unknown: the next one is a new request. */
    fun abandon(reply: Reply) {
        reply.attempt.abandon()
        reply.frozen = false
        reply.expired = false
    }
}

fun planChoiceName(c: PlanChoice, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String = when (c) {
    PlanChoice.PLAN_CHOICE_KEEP_PLANNING -> copy(UiMessages.dockPlanKeep())
    PlanChoice.PLAN_CHOICE_CLEAR_AND_IMPLEMENT -> copy(UiMessages.dockPlanClear())
    else -> copy(UiMessages.approvalApproveExecute())
}

/** What a plan's buttons offer: every choice, or only what can be done about a reply whose outcome is unknown. */
data class PlanOffer(
    /** 继续规划 / 批准执行 (and 清空上下文后执行) can be chosen. */
    val choices: Boolean = true,
    /** How the unknown reply stands. */
    val note: String = "",
    /** The button that sends the unknown reply again, named after its choice; null when it cannot be sent again. */
    val retry: String? = null,
    /** 放弃 is offered. */
    val abandon: Boolean = false,
)

fun planOffer(reply: Plans.Reply, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): PlanOffer {
    val what = planChoiceName(reply.choice, copy = copy)
    return when {
        reply.expired -> PlanOffer(choices = false, note = copy(UiMessages.dockAndroidPlanExpired(choice = what)), abandon = true)
        reply.frozen -> PlanOffer(choices = false, note = copy(UiMessages.dockAndroidPlanUnknown(choice = what)), retry = copy(UiMessages.dockAndroidRetryChoice(choice = what)), abandon = true)
        else -> PlanOffer()
    }
}
