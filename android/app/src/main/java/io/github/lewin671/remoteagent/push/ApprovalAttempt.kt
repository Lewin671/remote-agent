package io.github.lewin671.remoteagent.push

import io.github.lewin671.remoteagent.core.Attempt
import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.PreparedCommand
import io.github.lewin671.remoteagent.data.Settings
import io.github.lewin671.remoteagent.proto.NotifyKind

/** Local choices apply even before the server has received a subscription update. */
fun acceptPush(hasAccount: Boolean, kind: NotifyKind, settings: Settings): Boolean = hasAccount && when (kind) {
    NotifyKind.NOTIFY_KIND_ATTENTION -> settings.notifyAttention
    NotifyKind.NOTIFY_KIND_TURN_ENDED -> settings.notifyTurnEnded
    // A recovery request is a security action; never filtered by the chat switches.
    NotifyKind.NOTIFY_KIND_SECURITY, NotifyKind.NOTIFY_KIND_SCHEDULED -> true
    else -> false
}

/** What the phone has durably recorded about an approval reply sent from a notification. */
enum class ApprovalRecord {
    /** It may have reached the computer: only the same request is sent again, as one whose outcome is unknown. */
    SENT,
    /** The computer confirmed it. */
    DONE,
}

/**
 * Sends a reply for the worker, which may be killed at any point and run
 * again with the same input. [before] is what an earlier run recorded;
 * [record] must be durable when it returns true, and null forgets the reply.
 *
 * It is recorded as possibly sent before it leaves the phone. A confirmed
 * reply stays recorded: the worker's own success is not durable yet when
 * this returns, and a run after that must neither send it again nor, past
 * its deadline, report that it was never submitted. Only a definite failure
 * forgets it.
 */
suspend fun sendApproval(
    command: PreparedCommand, before: ApprovalRecord?,
    record: (ApprovalRecord?) -> Boolean,
    forward: suspend (PreparedCommand, Boolean) -> Unit,
) {
    if (before == ApprovalRecord.DONE) return
    val wasUnknown = before == ApprovalRecord.SENT
    val attempt = Attempt()
    attempt.next { command }
    if (wasUnknown) attempt.settle(CommandException(CommandException.UNKNOWN, "结果未知"))
    check(record(ApprovalRecord.SENT)) { "没能保存审批提交状态，没有发出" }
    try {
        forward(command, wasUnknown)
        record(ApprovalRecord.DONE)
    } catch (e: Exception) {
        val shown = attempt.settle(e) ?: e
        if (!attempt.frozen) record(null)
        throw shown
    }
}
