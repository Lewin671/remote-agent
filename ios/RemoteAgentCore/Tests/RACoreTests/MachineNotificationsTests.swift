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
 @Test func mapOpErrorUsesStructuredErrorCode() {
  let lease = PBSecurityOpResultEffect.with {
   $0.error = "some localized text"
   $0.structuredErrorCode = .leaseExpired
  }
  #expect((Remote.mapOpError(lease) as? SecurityFailure) == .lease)
  let stale = PBSecurityOpResultEffect.with {
   $0.error = "lease expired" // substring must not override structured_error_code
   $0.structuredErrorCode = .stale
  }
  #expect((Remote.mapOpError(stale) as? SecurityFailure) == .stale)
  let history = PBSecurityOpResultEffect.with {
   $0.error = "old"
   $0.structuredErrorCode = .unsupportedHistory
  }
  #expect(Remote.mapOpError(history) is UnsupportedHistoryError)
 }
}
