import Foundation
import RAProto
import Testing
@testable import RACore
@Suite(.timeLimit(.minutes(2))) struct MachineNotificationsTests {
 @Test func oldServerKeepsExistingKinds() {
  let base:[PBNotifyKind]=[.attention,.turnEnded,.security,.scheduled]
  var old=PBGetPushKeyResponse();old.publicKey="old-server"
  #expect(machineNotificationKinds(base+[.machineDisk],advertised:old.machineDiskNotifications)==base)
  #expect(machineNotificationKinds(base,advertised:true)==base+[.machineDisk])
 }
}
