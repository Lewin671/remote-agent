import Foundation
import RAProto
import Testing
@testable import RACore
@Suite struct MachineNotificationsTests {
 @Test func oldServerKeepsExistingKinds() {
  let base:[PBNotifyKind]=[.attention,.turnEnded,.security,.scheduled]
  var old=PBGetPushKeyResponse();old.publicKey="old-server"
  #expect(machineNotificationKinds(base+[.machineDisk],advertised:old.machineDiskNotifications)==base)
  #expect(machineNotificationKinds(base,advertised:true)==base+[.machineDisk])
 }
 @Test func localCopyAndMachineIdentity() {
  var c=PBNotifyContent();c.kind = .machineDisk;c.machineID="computer-a";c.machineName="电脑 A";c.diskStatus.availableBytes=1_500_000_000;c.diskStatus.pressure = .critical
  let zh=machineDiskNotice(c,locale:"zh-CN"),en=machineDiskNotice(c,locale:"en")
  #expect(zh?.title=="电脑 A 磁盘快满了，只剩 1.5 GB");#expect(en?.body=="Clean up now or sessions may not start.")
  c.machineID="computer-b";#expect(machineDiskNotice(c)?.tag != zh?.tag)
  c.text="daemon text";#expect(machineDiskNotice(c)==nil)
 }
}
