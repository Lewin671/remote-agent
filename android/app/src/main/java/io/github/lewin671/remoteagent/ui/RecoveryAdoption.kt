package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.DeviceSecurity

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
