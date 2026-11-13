package io.github.lewin671.remoteagent.core
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test
class MachineNotificationsTest {
 @Test fun oldServerKeepsExistingKinds() {
  val base=listOf(NotifyKind.NOTIFY_KIND_ATTENTION,NotifyKind.NOTIFY_KIND_TURN_ENDED,NotifyKind.NOTIFY_KIND_SECURITY,NotifyKind.NOTIFY_KIND_SCHEDULED)
  val old=GetPushKeyResponse.newBuilder().setPublicKey("old-server").build()
  val requested=machineNotificationKinds(base+NotifyKind.NOTIFY_KIND_MACHINE_DISK,old.machineDiskNotifications)
  assertEquals(base,requested);assertTrue(requested.all { it.number<=4 })
  assertEquals(base+NotifyKind.NOTIFY_KIND_MACHINE_DISK,machineNotificationKinds(base,true))
 }
 @Test fun localCopyAndMachineIdentity() {
  val c=NotifyContent.newBuilder().setKind(NotifyKind.NOTIFY_KIND_MACHINE_DISK).setMachineId("computer-a").setMachineName("电脑 A").setDiskStatus(DiskStatus.newBuilder().setAvailableBytes(1_500_000_000).setPressure(ResourcePressure.RESOURCE_PRESSURE_CRITICAL)).build()
  val zh=machineDiskNotice(c,{messageTextForTest(it,"zh-CN")})!!
  val en=machineDiskNotice(c,{messageTextForTest(it,"en")},"en")!!
  assertEquals("电脑 A 磁盘快满了，只剩 1.5 GB",zh.title);assertEquals("Clean up now or sessions may not start.",en.body)
  assertNotEquals(zh.tag,machineDiskNotice(c.toBuilder().setMachineId("computer-b").build(),::messageTextForTest)!!.tag)
  assertNull(machineDiskNotice(c.toBuilder().setText("daemon text").build(),::messageTextForTest))
  assertNull(machineDiskNotice(c.toBuilder().setDiskStatus(c.diskStatus.toBuilder().setAvailableBytes(50_000_000_000)).build(),::messageTextForTest))
 }
}

class MachineNotificationReceiverTest {
 @Test fun opensGoEnvelopeBeforeLocalizing()=kotlinx.coroutines.runBlocking {
  val server=TestServer();val http=Api.defaultHttp()
  try {
   val phone=newAuthorityAccount(randomBytes(32),server.url,http,server.setupToken)
   val machine=fixtureMember(phone,http,DeviceKind.DEVICE_KIND_MACHINE)
   val fixture=TestSecurity.get(server.url)
   val c=NotifyContent.newBuilder().setKind(NotifyKind.NOTIFY_KIND_MACHINE_DISK).setMachineId(machine.deviceId).setMachineName("电脑 A").setDiskStatus(DiskStatus.newBuilder().setObservedMs(1700000000000).setAvailableBytes(1_500_000_000).setPressure(ResourcePressure.RESOURCE_PRESSURE_CRITICAL)).build()
   val env=fixture.seal(machine,Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_NOTIFY).setMachineId(machine.deviceId).setSender(machine.deviceId).setRequestId("disk:1700000000000:3").build(),c)
   val key=DeviceSecurity.NotifyRecord.fromRecord(fixture.record(phone.deviceId)).bytes()
   val result=receiveMachineDiskNotification(env.toByteArray(),key,{messageTextForTest(it,"en")},"en")!!
   assertEquals(machine.deviceId,result.machineId);assertEquals("Clean up now or sessions may not start.",result.body)
   val signature=env.signature.toByteArray();signature[0]=(signature[0].toInt() xor 1).toByte()
   try { receiveMachineDiskNotification(env.toBuilder().setSignature(com.google.protobuf.ByteString.copyFrom(signature)).build().toByteArray(),key,::messageTextForTest);fail("tampered push opened") } catch(_:DeviceSecurity.Failure) {}
  } finally {server.close()}
 }
}
