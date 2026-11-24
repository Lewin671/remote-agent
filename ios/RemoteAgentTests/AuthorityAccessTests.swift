import Foundation
import RACore
import RAProto
import Testing
import UIKit
@testable import RemoteAgent

@MainActor @Suite struct AuthorityAccessTests {
    @Test func rolesFollowTheDirectoryRatherThanPlatformMetadata() {
        for platform in ["ios", "web", "android", "", "unknown"] {
            var state = RemoteState()
            state.authorityDeviceId = "authority"
            state.devices["authority"] = DeviceView(id: "authority", kind: .client, createdMs: 1,
                meta: .with { $0.platform = platform; $0.name = "Studio" })
            state.devices["member"] = DeviceView(id: "member", kind: .client, createdMs: 1)
            #expect(AuthorityAccess(state: state, me: "authority").isAuthority)
            #expect(!AuthorityAccess(state: state, me: "member").isAuthority)
            #expect(AuthorityAccess(state: state, me: "member").addHint == "要添加电脑或客户端，在管理端（Studio）上打开「设置 › 添加设备」。")
            #expect(AuthorityAccess(state: state, me: "member").emptyHint == L10n.homeAddViaManager(name: "Studio"))
            state.devices["authority"]!.kind = .machine
            #expect(!AuthorityAccess(state: state, me: "authority").isAuthority)
            #expect(!AuthorityAccess(state: state, me: "authority").isClient)
            state.devices["authority"]!.kind = .client; state.removed = true
            #expect(!AuthorityAccess(state: state, me: "authority").isAuthority)
        }
    }
    @Test func logoutPrioritizesUnresolvedEvidenceAndWarnsTheOnlyAuthority() {
        #expect(AuthorityAccess.exitAdvice(authority: true, unresolved: true) == L10n.securityIosASecurityOperationIsUnfinishedRetryOrVerifyIts())
        #expect(AuthorityAccess.exitAdvice(authority: true, unresolved: false).contains("唯一管理端"))
        #expect(AuthorityAccess.exitAdvice(authority: true, unresolved: false).contains("没有恢复码备份将丢失管理权"))
        #expect(AuthorityAccess.exitAdvice(authority: false, unresolved: false) == L10n.securityExitingDeletesLocalKeysYouWillNeedToPair())
    }
    @Test func unrelatedPendingCannotBecomeAPairingSuccessAfterRetry() throws {
        var lease = PBSecurityPendingOpSummary()
        lease.kind = "lease"
        #expect(!AuthorityPairingModel.ownsDelivery(lease, pairingID: "pair-one"))
        #expect(!AuthorityPairingModel.ownsDelivery(nil, pairingID: "pair-one"))
        var pair = PBSecurityPendingOpSummary()
        pair.kind = "state"
        pair.hasDelivery_p = true
        pair.deliveryPairingID = "pair-one"
        #expect(AuthorityPairingModel.ownsDelivery(pair, pairingID: "pair-one"))
        #expect(!AuthorityPairingModel.ownsDelivery(pair, pairingID: "pair-two"))
        var withoutDelivery = pair
        withoutDelivery.hasDelivery_p = false
        #expect(!AuthorityPairingModel.ownsDelivery(withoutDelivery, pairingID: "pair-one"))
    }

    @Test func deviceDescriptionIncludesHardwareModelWithIdiomFallback() {
        #expect(AppModel.deviceDescription(systemVersion: "18.4", hardwareModel: "iPhone16,1", idiom: .phone) == "iOS 18.4 · iPhone16,1")
        #expect(AppModel.deviceDescription(systemVersion: "18.4", hardwareModel: "iPad14,3", idiom: .pad) == "iOS 18.4 · iPad14,3")
        #expect(AppModel.deviceDescription(systemVersion: "18.4", hardwareModel: "  ", idiom: .phone) == "iOS 18.4 · iPhone")
        #expect(AppModel.deviceDescription(systemVersion: "18.4", hardwareModel: "", idiom: .pad) == "iOS 18.4 · iPad")
    }

    @Test func pairingModelTracksReplacementCandidatesAndHonorsUserToggle() {
        let model = AuthorityPairingModel()
        var joined = PBSecurityPairingInfo()
        joined.pairingID = "pair-meta"
        joined.joined = true
        joined.sasEmoji = ["🐊", "🥯", "🐩", "🦌"]
        joined.joinerMetaReceived = false
        model.applyPolled(joined)
        #expect(model.needsMetaPoll)
        #expect(model.selectedRemoveDeviceIDs.isEmpty)

        var withMeta = joined
        withMeta.joinerMetaReceived = true
        withMeta.removeOldDefault = true
        withMeta.replacementCandidates = [
            .with {
                $0.deviceID = "old-iphone-id"
                $0.displayName = "iPhone"
                $0.description_p = "iOS 18.3 · iPhone16,1"
                $0.shortID = "…16a1"
            }
        ]
        model.applyPolled(withMeta)
        #expect(!model.needsMetaPoll)
        #expect(model.removeOld)
        #expect(model.selectedRemoveDeviceIDs == ["old-iphone-id"])

        model.setRemoveOld(false)
        #expect(!model.removeOld)
        #expect(model.selectedRemoveDeviceIDs.isEmpty)

        // Subsequent poll results must not overwrite the user's explicit toggle choice.
        model.applyPolled(withMeta)
        #expect(!model.removeOld)
        #expect(model.selectedRemoveDeviceIDs.isEmpty)
    }

}
