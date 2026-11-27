package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.SecurityActionEmpty
import io.github.lewin671.remoteagent.core.proto.SecurityApprovePairingAction
import io.github.lewin671.remoteagent.core.proto.SecurityBoolAction
import io.github.lewin671.remoteagent.core.proto.SecurityPairingInfo
import io.github.lewin671.remoteagent.core.proto.SecurityPrepareCreationAction
import io.github.lewin671.remoteagent.core.proto.SecurityStartJoinPairingAction
import io.github.lewin671.remoteagent.core.proto.SecurityStartPairingAction
import io.github.lewin671.remoteagent.core.proto.SecurityStringAction
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowResultEffect
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowStatus

/** Test-only SecurityClient workflow helpers (production flows use semantic screen actions). */
internal suspend fun SecurityClient.dismissUpgradeNotice(): SecurityWorkflowStatus =
    run { setDismissUpgradeNotice(SecurityActionEmpty.getDefaultInstance()) }.status.also { notifyDismissUpgradeNotice() }

internal suspend fun SecurityClient.prepareCreation(server: String, storagePersisted: Boolean = true): String =
    run {
        setPrepareCreation(
            SecurityPrepareCreationAction.newBuilder().setServer(server).setStoragePersisted(storagePersisted).build(),
        )
    }.recoveryCode

internal suspend fun SecurityClient.confirmCreationCodeSaved() {
    run { setConfirmCreationCodeSaved(SecurityActionEmpty.getDefaultInstance()) }
}

internal suspend fun SecurityClient.prepareHandoffCode(storagePersisted: Boolean = true): String =
    run { setPrepareHandoffCode(SecurityBoolAction.newBuilder().setFlag(storagePersisted).build()) }.recoveryCode

internal suspend fun SecurityClient.publishHandoffOffer() {
    run { setPublishHandoffOffer(SecurityActionEmpty.getDefaultInstance()) }
}

internal suspend fun SecurityClient.handoffAuthority(targetDeviceId: String) {
    run { setHandoffAuthority(SecurityStringAction.newBuilder().setValue(targetDeviceId).build()) }
}

internal suspend fun SecurityClient.startPairing(webOrigin: String, replacesDeviceId: String = ""): SecurityPairingInfo =
    run {
        setStartPairing(
            SecurityStartPairingAction.newBuilder().setWebOrigin(webOrigin).setReplacesDeviceId(replacesDeviceId).build(),
        )
    }.pairing

internal suspend fun SecurityClient.pollPairing(pairingId: String): SecurityPairingInfo =
    run { setPollPairing(SecurityStringAction.newBuilder().setValue(pairingId).build()) }.pairing

internal suspend fun SecurityClient.approvePairing(
    pairingId: String,
    sasConfirmed: Boolean,
    removeDeviceIds: List<String> = emptyList(),
): SecurityPairingInfo =
    run {
        setApprovePairing(
            SecurityApprovePairingAction.newBuilder()
                .setPairingId(pairingId)
                .setSasConfirmed(sasConfirmed)
                .addAllRemoveDeviceIds(removeDeviceIds)
                .build(),
        )
    }.pairing

internal suspend fun SecurityClient.finishPairingDelivery(pairingId: String): SecurityPairingInfo =
    run { setFinishPairingDelivery(SecurityStringAction.newBuilder().setValue(pairingId).build()) }.pairing

internal suspend fun SecurityClient.startJoinPairing(
    server: String = "",
    code: String = "",
    commitHint: ByteArray? = null,
    existingRepair: Boolean = false,
    existingServer: String = "",
    rawLink: String = "",
    platform: String = "",
    description: String = "",
): SecurityPairingInfo =
    run {
        setStartJoinPairing(
            SecurityStartJoinPairingAction.newBuilder()
                .setServer(server).setCode(code)
                .apply { if (commitHint != null && commitHint.isNotEmpty()) setCommitHint(ByteString.copyFrom(commitHint)) }
                .setExistingRepair(existingRepair).setExistingServer(existingServer).setRawLink(rawLink)
                .setPlatform(platform).setDescription(description).build(),
        )
    }.pairing

internal suspend fun SecurityClient.confirmJoinSas(pairingId: String): SecurityWorkflowResultEffect =
    run { setConfirmJoinSas(SecurityStringAction.newBuilder().setValue(pairingId).build()) }

internal suspend fun Remote.prepareHandoffCode(storagePersisted: Boolean = true): String =
    securityClient.prepareHandoffCode(storagePersisted)

internal suspend fun Remote.publishHandoffOffer() {
    securityClient.publishHandoffOffer()
    refreshDevices()
}

internal suspend fun Remote.handoffAuthority(incoming: String) {
    securityClient.handoffAuthority(incoming)
}
