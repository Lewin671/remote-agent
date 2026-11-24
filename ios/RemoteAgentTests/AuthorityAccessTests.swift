import Foundation
import RACore
import RAProto
import Testing
@testable import RemoteAgent

@MainActor @Suite struct AuthorityAccessTests {
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

}
