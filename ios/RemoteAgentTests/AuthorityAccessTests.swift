import Foundation
import RACore
import RAProto
import Testing
import UIKit
@testable import RemoteAgent

@MainActor @Suite struct AuthorityAccessTests {
    @Test func logoutPrioritizesUnresolvedEvidenceAndWarnsTheOnlyAuthority() {
        #expect(AuthorityAccess.exitAdvice(authority: true, unresolved: true) == L10n.securityIosASecurityOperationIsUnfinishedRetryOrVerifyIts())
        #expect(AuthorityAccess.exitAdvice(authority: true, unresolved: false) == L10n.securityThisDeviceIsTheOnlyManagingClientTransferManagement())
        #expect(AuthorityAccess.exitAdvice(authority: true, unresolved: false).contains("本机是唯一管理端"))
        #expect(!AuthorityAccess.exitAdvice(authority: true, unresolved: false).contains("此浏览器"))
        #expect(AuthorityAccess.exitAdvice(authority: false, unresolved: false) == L10n.securityExitingDeletesLocalKeysYouWillNeedToPair())
        let fromCore = AppModel.RejoinAdvice.sensitive(
            authority: true,
            unresolved: false,
            messageText: .with { $0.key = "security.this_device_is_the_only_managing_client_transfer_management" }
        )
        #expect(fromCore.text.contains(L10n.securityThisDeviceIsTheOnlyManagingClientTransferManagement()))
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
        withMeta.replacementCandidates = [
            .with {
                $0.deviceID = "old-iphone-stale"
                $0.displayName = "iPhone"
                $0.description_p = "iOS 18.3 · iPhone16,1"
                $0.shortID = "…16a1"
                $0.defaultSelected = true
                $0.lastActiveText = .with { $0.key = "time.yesterday" }
            },
            .with {
                $0.deviceID = "old-iphone-active"
                $0.displayName = "Active iPhone"
                $0.description_p = "iOS 18.4 · iPhone16,1"
                $0.shortID = "…16b2"
                $0.defaultSelected = false
                $0.lastActiveText = .with { $0.key = "time.just_now" }
            },
        ]
        model.applyPolled(withMeta)
        #expect(!model.needsMetaPoll)
        #expect(model.selectedRemoveDeviceIDs == ["old-iphone-stale"])

        model.setRemoveCandidate("old-iphone-stale", selected: false)
        model.setRemoveCandidate("old-iphone-active", selected: true)
        #expect(model.selectedRemoveDeviceIDs == ["old-iphone-active"])

        // Subsequent poll results must not overwrite the user's explicit toggle choice.
        model.applyPolled(withMeta)
        #expect(model.selectedRemoveDeviceIDs == ["old-iphone-active"])
    }

    @Test func handoffCandidateLabelPrefersCoreDisplayNameTextOverRawFallback() {
        let localized = PBHandoffCandidateView.with {
            $0.deviceID = "dev-raw-id-1234"
            $0.displayName = "1234"
            $0.displayNameText = .with { $0.key = "security.browser" }
        }
        #expect(HandoffScreen.candidateLabel(localized) == L10n.securityBrowser())

        let literal = PBHandoffCandidateView.with {
            $0.deviceID = "dev-raw-id-5678"
            $0.displayName = "iPhone 16 Pro · 5678"
            $0.displayNameText = .with { $0.literal = "iPhone 16 Pro · 5678" }
        }
        #expect(HandoffScreen.candidateLabel(literal) == "iPhone 16 Pro · 5678")

        let empty = PBHandoffCandidateView.with {
            $0.deviceID = "dev-raw-id-9999"
        }
        #expect(HandoffScreen.candidateLabel(empty) == L10n.securityClient())
    }

}
