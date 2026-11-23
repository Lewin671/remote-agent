package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*

fun machineNotificationKinds(kinds: List<NotifyKind>, advertised: Boolean): List<NotifyKind> =
    kinds.filter { it != NotifyKind.NOTIFY_KIND_MACHINE_DISK } + if (advertised) listOf(NotifyKind.NOTIFY_KIND_MACHINE_DISK) else emptyList()

suspend fun Api.setMachinePushTarget(token: String, type: PushType, target: String, kinds: List<NotifyKind>): SetPushTargetResponse {
    val advertised = target.isNotEmpty() && client.getPushKey(GetPushKeyRequest.getDefaultInstance(), Api.headers(token)).value().machineDiskNotifications
    return client.setPushTarget(SetPushTargetRequest.newBuilder().setType(type).setTarget(target).addAllKinds(if (target.isEmpty()) emptyList() else machineNotificationKinds(kinds, advertised)).build(), Api.headers(token)).value()
}
