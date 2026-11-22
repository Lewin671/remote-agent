package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowStatus
import io.github.lewin671.remoteagent.proto.GetRecoveryContextResponse.Phase

/**
 * Keep the completed status until adopt opens the enrolled Remote cleanly.
 */
internal suspend fun completeRecoveryAndAdopt(
    complete: suspend () -> SecurityWorkflowStatus,
    adopt: suspend (SecurityWorkflowStatus) -> Unit,
) {
    val status = complete()
    adopt(status)
}

internal enum class RecoveryNextAction {
    COMPLETE,
    RESTART,
}

internal fun recoveryNextAction(
    phase: Phase?,
): RecoveryNextAction =
    if (phase == Phase.PHASE_REJECTED) RecoveryNextAction.RESTART else RecoveryNextAction.COMPLETE

/** Clear durable material first; a failed clear must leave the resumable form intact. */
internal suspend fun restartRejectedRecovery(
    phase: Phase?,
    clearPending: suspend () -> Unit,
    resetScreen: () -> Unit,
): Boolean {
    if (recoveryNextAction(phase) != RecoveryNextAction.RESTART) return false
    clearPending()
    resetScreen()
    return true
}
