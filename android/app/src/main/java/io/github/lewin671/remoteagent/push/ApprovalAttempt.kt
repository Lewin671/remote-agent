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
    else -> false
}

/** A killed worker must leave evidence before an approval can leave the phone. */
suspend fun sendApproval(
    command: PreparedCommand, wasUnknown: Boolean,
    mark: () -> Boolean, clear: () -> Unit,
    forward: suspend (PreparedCommand, Boolean) -> Unit,
) {
    val attempt = Attempt()
    attempt.next { command }
    if (wasUnknown) attempt.settle(CommandException(CommandException.UNKNOWN, "结果未知"))
    check(mark()) { "没能保存审批提交状态，没有发出" }
    try {
        forward(command, wasUnknown)
        clear()
    } catch (e: Exception) {
        val shown = attempt.settle(e) ?: e
        if (!attempt.frozen) clear()
        throw shown
    }
}
