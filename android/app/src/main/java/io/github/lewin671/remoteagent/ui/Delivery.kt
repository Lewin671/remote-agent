package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.PendingInput
import io.github.lewin671.remoteagent.core.failureText
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.SendMode

internal data class SendFeedback(val text: String, val uncertain: Boolean)

internal fun deliveryFailure(reason: String, uncertain: Boolean,
    kind: io.github.lewin671.remoteagent.core.CommandFailureKind = io.github.lewin671.remoteagent.core.CommandFailureKind.RAW,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): SendFeedback {
    val short = when (kind) {
        io.github.lewin671.remoteagent.core.CommandFailureKind.CONNECTION_DROPPED -> copy(UiMessages.errorsConnectionDroppedMidway())
        io.github.lewin671.remoteagent.core.CommandFailureKind.REPLY_UNVERIFIED -> copy(UiMessages.composerAndroidReplyUnverified())
        io.github.lewin671.remoteagent.core.CommandFailureKind.UNKNOWN -> copy(UiMessages.approvalAndroidUnknownResult())
        io.github.lewin671.remoteagent.core.CommandFailureKind.COMPUTER_OFFLINE -> copy(UiMessages.projectsComputerOffline())
        io.github.lewin671.remoteagent.core.CommandFailureKind.IMAGE_MISSING -> copy(UiMessages.composerAndroidImageNotOnComputer())
        io.github.lewin671.remoteagent.core.CommandFailureKind.EXECUTED_RESULT_UNAVAILABLE -> copy(UiMessages.errorsExecutedResultUnavailable())
        io.github.lewin671.remoteagent.core.CommandFailureKind.IDENTITY_CONFLICT -> copy(UiMessages.errorsRequestIdentityConflict())
        io.github.lewin671.remoteagent.core.CommandFailureKind.RAW -> reason.trim().ifEmpty { copy(UiMessages.composerAndroidUnknownReason()) }
    }
    if (kind == io.github.lewin671.remoteagent.core.CommandFailureKind.EXECUTED_RESULT_UNAVAILABLE || kind == io.github.lewin671.remoteagent.core.CommandFailureKind.IDENTITY_CONFLICT) return SendFeedback(short, uncertain)
    return SendFeedback(copy(if (uncertain) UiMessages.composerMaybeFailure(short) else UiMessages.composerSendFailure(short)), uncertain)
}

/** The same projection is used by the session row and by restoration/retry regression tests. */
internal fun outgoingFailure(outgoing: Outgoing, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): SendFeedback =
    deliveryFailure(outgoing.failureDisplay?.resolve(copy) ?: outgoing.error,
        outgoing.state == Outgoing.State.UNKNOWN, kind = outgoing.failureKind, copy = copy)

internal fun inputFeedback(status: InputStatus, reason: String, agent: Agent, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): SendFeedback =
    if (status == InputStatus.INPUT_STATUS_UNCONFIRMED) SendFeedback(copy(UiMessages.composerMaybeFailure(copy(UiMessages.queueAndroidUnconfirmed()))), true)
    else deliveryFailure(copy(failureText(reason, agent, copy = copy)), status == InputStatus.INPUT_STATUS_NEEDS_TERMINAL, copy = copy)

internal fun pendingInputLabel(
    pending: PendingInput,
    starting: String?,
    cancel: Cancels.State?,
    resuming: Boolean,
    resumeFailed: Boolean,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText,
): String = when {
    cancel == Cancels.State.Canceling -> copy(UiMessages.queueCanceling())
    cancel is Cancels.State.Failed -> copy(UiMessages.queueCancelFailed())
    resuming -> copy(UiMessages.queueResuming())
    resumeFailed -> copy(UiMessages.queueResumeFailed())
    pending.paused -> copy(UiMessages.queuePaused())
    pending.held -> copy(UiMessages.queueInsert())
    pending.mode == SendMode.SEND_MODE_QUEUE -> copy(UiMessages.queueAfterTurn())
    starting != null -> copy(UiMessages.queueAgentReady(agent = starting))
    else -> copy(UiMessages.queueInsert())
}

internal fun pendingInputCaption(label: String, index: Int, total: Int, origin: ChatSource?, showPosition: Boolean = true, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String =
    label + (if (total > 1 && showPosition) copy(UiMessages.queueRank(rank = index)) else "") +
        (if (origin?.label != null && !origin.self) " · ${origin.label}" else "")

internal fun scheduledPendingCaption(pending: PendingInput, events: List<io.github.lewin671.remoteagent.proto.Event>, label: String, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String? {
    if (pending.origin?.scheduledTaskId.isNullOrEmpty()) return null
    val accepted = events.firstOrNull { it.hasInputState() && it.inputState.requestId == pending.id && it.inputState.status == InputStatus.INPUT_STATUS_QUEUED }
    val clock = accepted?.let { io.github.lewin671.remoteagent.core.scheduleClock(java.time.Instant.ofEpochMilli(io.github.lewin671.remoteagent.core.eventTime(it))) }
    return listOfNotNull(copy(UiMessages.queueAndroidScheduled()), clock, label).joinToString(" · ")
}
