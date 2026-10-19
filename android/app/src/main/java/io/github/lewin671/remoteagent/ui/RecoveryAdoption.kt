package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.DeviceSecurity
import io.github.lewin671.remoteagent.proto.GetRecoveryContextResponse.Phase

/**
 * Keep the completed identity until adopt has durably saved both account files. RaApp.adopt owns
 * draft cleanup after that boundary.
 */
internal suspend fun completeRecoveryAndAdopt(
    complete: suspend () -> Pair<Account, DeviceSecurity.Record>,
    adopt: suspend (Account, DeviceSecurity.Record) -> Unit,
) {
    val (account, record) = complete()
    adopt(account, record)
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
