import RACore
import SwiftUI

/// The signed directory chooses roles; platform metadata never grants management.
struct AuthorityAccess {
    let state: RemoteState
    let me: String
    var isClient: Bool { state.devices[me]?.kind == .client && !state.removed }
    var isAuthority: Bool { isClient && !me.isEmpty && state.authorityDeviceId == me }
    var name: String {
        clientName(state.devices[state.authorityDeviceId], id: state.authorityDeviceId, among: state.devices)
    }
    var addHint: String { L10n.securityAddOnAuthority(name: String(describing: name)) }
    var emptyHint: String { L10n.homeAddViaManager(name: name) }
    static func exitAdvice(authority: Bool, unresolved: Bool) -> String {
        if unresolved { return L10n.securityIosASecurityOperationIsUnfinishedRetryOrVerifyIts() }
        if authority { return L10n.securityIosThisDeviceIsTheSoleManagingClientTransferManagement() }
        return L10n.securityExitingDeletesLocalKeysYouWillNeedToPair()
    }
}
