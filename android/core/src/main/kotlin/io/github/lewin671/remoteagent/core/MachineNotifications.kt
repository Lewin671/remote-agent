package io.github.lewin671.remoteagent.core
import io.github.lewin671.remoteagent.proto.*
import java.text.NumberFormat
import java.util.Locale

fun machineNotificationKinds(kinds: List<NotifyKind>, advertised: Boolean): List<NotifyKind> =
    kinds.filter { it != NotifyKind.NOTIFY_KIND_MACHINE_DISK } + if(advertised) listOf(NotifyKind.NOTIFY_KIND_MACHINE_DISK) else emptyList()
data class MachineDiskNotice(val machineId: String, val tag: String, val title: String, val body: String)
fun machineDiskNotice(c: NotifyContent, copy: MessageResolver, locale: String = "zh-CN"): MachineDiskNotice? {
    if(c.kind != NotifyKind.NOTIFY_KIND_MACHINE_DISK || c.sessionId.isNotEmpty() || c.machineId.isEmpty() || c.machineName.isBlank() || c.title.isNotEmpty() || c.text.isNotEmpty() || c.hasApproval() || !c.hasDiskStatus()) return null
    val disk=c.diskStatus; val critical=disk.pressure == ResourcePressure.RESOURCE_PRESSURE_CRITICAL
    if(!critical && disk.pressure != ResourcePressure.RESOURCE_PRESSURE_WARNING || disk.availableBytes < 0 || disk.availableBytes >= if(critical) 2_000_000_000 else 10_000_000_000) return null
    val number=NumberFormat.getNumberInstance(Locale.forLanguageTag(locale)).apply { minimumFractionDigits=1; maximumFractionDigits=1 }.format(disk.availableBytes/1e9)
    val size=copy(UiMessages.machineStatusSizeGb(number))
    return MachineDiskNotice(c.machineId,"${c.machineId}/NOTIFY_KIND_MACHINE_DISK",
        copy(if(critical) UiMessages.machineStatusNotifyCritical(c.machineName,size) else UiMessages.machineStatusNotifyWarning(c.machineName,size)),
        copy(if(critical) UiMessages.machineStatusNotifyCriticalBody() else UiMessages.machineStatusNotifyWarningBody()))
}
/** The data-only receiver verifies ciphertext before producing local-language copy. */
fun receiveMachineDiskNotification(envelope: ByteArray, notifyRecord: ByteArray, copy: MessageResolver, locale: String = "zh-CN"): MachineDiskNotice? =
    machineDiskNotice(DeviceSecurity.NotifyRecord.parse(notifyRecord).open(Envelope.parseFrom(envelope)),copy,locale)

suspend fun Api.setMachinePushTarget(token: String, type: PushType, target: String, kinds: List<NotifyKind>): SetPushTargetResponse {
    val advertised=target.isNotEmpty() && client.getPushKey(GetPushKeyRequest.getDefaultInstance(),Api.headers(token)).value().machineDiskNotifications
    return client.setPushTarget(SetPushTargetRequest.newBuilder().setType(type).setTarget(target).addAllKinds(if(target.isEmpty()) emptyList() else machineNotificationKinds(kinds,advertised)).build(),Api.headers(token)).value()
}
