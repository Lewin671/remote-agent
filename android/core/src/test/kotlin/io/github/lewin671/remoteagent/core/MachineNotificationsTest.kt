package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

class MachineNotificationsTest {
    @Test
    fun oldServerKeepsExistingKinds() {
        val base = listOf(
            NotifyKind.NOTIFY_KIND_ATTENTION,
            NotifyKind.NOTIFY_KIND_TURN_ENDED,
            NotifyKind.NOTIFY_KIND_SECURITY,
            NotifyKind.NOTIFY_KIND_SCHEDULED,
        )
        val old = GetPushKeyResponse.newBuilder().setPublicKey("old-server").build()
        val requested = machineNotificationKinds(base + NotifyKind.NOTIFY_KIND_MACHINE_DISK, old.machineDiskNotifications)
        assertEquals(base, requested)
        assertTrue(requested.all { it.number <= 4 })
        assertEquals(base + NotifyKind.NOTIFY_KIND_MACHINE_DISK, machineNotificationKinds(base, true))
    }
}

class MachineNotificationReceiverTest {
    @get:org.junit.Rule val hangGuard = hangGuard()

    @Test
    fun opensGoEnvelopeThroughCoreDisplayAndRejectsTampered() = kotlinx.coroutines.runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        try {
            val phone = newAuthorityAccount(randomBytes(32), server.url, http, server.setupToken)
            val machine = fixtureMember(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val fixture = TestSecurity.get(server.url)
            val phoneRemote = fixtureRemote(phone, http)
            try {
                phoneRemote.syncSecurity()
                phoneRemote.renewLeaseBlocking(force = true)
                val key = fixture.stores.getValue(phone.deviceId).readNotifyBlob()!!
                val observedMs = System.currentTimeMillis()
                val c = NotifyContent.newBuilder()
                    .setKind(NotifyKind.NOTIFY_KIND_MACHINE_DISK)
                    .setMachineId(machine.deviceId)
                    .setMachineName("电脑 A")
                    .setDiskStatus(
                        DiskStatus.newBuilder()
                            .setObservedMs(observedMs)
                            .setAvailableBytes(1_500_000_000)
                            .setPressure(ResourcePressure.RESOURCE_PRESSURE_CRITICAL),
                    )
                    .build()
                val env = fixture.seal(
                    machine,
                    Header.newBuilder()
                        .setKind(PayloadKind.PAYLOAD_KIND_NOTIFY)
                        .setMachineId(machine.deviceId)
                        .setSender(machine.deviceId)
                        .setRequestId("disk:$observedMs:${ResourcePressure.RESOURCE_PRESSURE_CRITICAL_VALUE}")
                        .build(),
                    c,
                )
                val tag = "${machine.deviceId}/NOTIFY_KIND_MACHINE_DISK"
                val result = openNotifyEnvelopeViaCore(
                    notifyBlob = key,
                    envelope = env,
                    machineId = machine.deviceId,
                    tag = tag,
                    expectedKind = NotifyKind.NOTIFY_KIND_MACHINE_DISK,
                )!!
                assertTrue(result.valid)
                assertTrue(result.critical)
                assertTrue(result.showDetailedByDefault)
                assertEquals(machine.deviceId, result.machineId)
                assertEquals(tag, result.tag)
                assertEquals("电脑 A 磁盘快满了，只剩 1.5 GB", coreTextToMessageText(result.title).resolve { messageTextForTest(it, "zh-CN") })
                assertEquals("Clean up now or sessions may not start.", coreTextToMessageText(result.body).resolve { messageTextForTest(it, "en") })

                val signature = env.signature.toByteArray().also { it[0] = (it[0].toInt() xor 1).toByte() }
                assertNull(
                    openNotifyEnvelopeViaCore(
                        notifyBlob = key,
                        envelope = env.toBuilder().setSignature(com.google.protobuf.ByteString.copyFrom(signature)).build(),
                        machineId = machine.deviceId,
                        tag = tag,
                        expectedKind = NotifyKind.NOTIFY_KIND_MACHINE_DISK,
                    ),
                )
            } finally {
                phoneRemote.close()
            }
        } finally {
            server.close()
        }
    }
}
