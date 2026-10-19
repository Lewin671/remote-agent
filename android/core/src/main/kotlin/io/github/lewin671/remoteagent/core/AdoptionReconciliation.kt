package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.SubmitRecoveryRequest

/** Clear a crash-left adoption intent only after its identity and exact state are durable. */
fun reconcileCompletedAdoption(
    account: Account,
    record: DeviceSecurity.Record,
    storage: DeviceSecurity.Storage,
): Boolean {
    val operation = storage.readSecurityOperation() ?: return false
    if (operation.kind != "creation" && operation.kind != "recovery") return false
    fun matches(ok: Boolean) {
        if (!ok) throw DeviceSecurity.Failure("conflict")
    }
    matches(record.memberID == account.deviceId && record.pin.account == account.accountId)
    matches(deviceId(SigningKey(account.deviceSeed).publicKey) == account.deviceId)
    val target: DeviceSecurity.State
    if (operation.kind == "creation") {
        val draft = CreatedAccount.readOperationMaterial(operation.local)
        val identity = draft.account
        matches(
            account.server == identity.server &&
                account.deviceId == identity.deviceId &&
                account.accountId == identity.accountId &&
                account.root.contentEquals(identity.root) &&
                account.deviceSeed.contentEquals(identity.deviceSeed) &&
                account.accountPublicKey.contentEquals(identity.accountPublicKey),
        )
        matches(
            record.pin.domain.contentEquals(draft.record.pin.domain) &&
                record.pin.genesis.contentEquals(draft.record.pin.genesis),
        )
        target = draft.record.head
        matches(
            operation.parent.isEmpty() &&
                operation.signed.contentEquals(DeviceSecurity.stateBytes(target)),
        )
    } else {
        val draft = RecoveryPublication.parse(operation.local)
        val identity = draft.submission
        target = DeviceSecurity.parseState(draft.state)
        val request = SubmitRecoveryRequest.parseFrom(operation.signed)
        matches(
            account.server == draft.server &&
                !account.hasRoot &&
                account.deviceId == identity.deviceId &&
                account.accountId == identity.accountId &&
                account.deviceSeed.contentEquals(identity.deviceSeed) &&
                account.accountPublicKey.contentEquals(identity.accountPublicKey),
        )
        matches(
            record.pin.genesis.copyOf(10).contentEquals(identity.genesisPrefix) &&
                record.pin.domain.contentEquals(target.core.domain) &&
                operation.parent.contentEquals(target.core.parent) &&
                request.state.toByteArray().contentEquals(draft.state) &&
                request.statement.toByteArray().contentEquals(draft.statement) &&
                record.proofs[target.core.epoch]?.contentEquals(draft.statement) == true,
        )
    }
    val installed = record.stateAt(target.core.epoch) ?: throw DeviceSecurity.Failure("conflict")
    matches(DeviceSecurity.stateHash(installed).contentEquals(DeviceSecurity.stateHash(target)))
    matches(
        target.core.authorityID == account.deviceId &&
            target.core
                .member(account.deviceId)
                ?.ed
                ?.contentEquals(SigningKey(account.deviceSeed).publicKey) == true,
    )
    storage.clearSecurityOperation()
    return true
}
