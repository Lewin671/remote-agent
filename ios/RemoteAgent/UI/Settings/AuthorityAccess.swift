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

struct AddDeviceEntry: View {
    var grouped = true
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    var body: some View {
        let access = AuthorityAccess(state: app.state, me: context.me)
        if access.isAuthority {
            if grouped { GroupCard { row } } else { row.padding(.horizontal, DS.Space.l).padding(.bottom, 8) }
        } else { GroupNote(access.addHint) }
    }
    private var row: some View {
        GRow(L10n.homeAddDevice(), action: true, icon: "plus") { nav.open(.addDevice()) }
    }

}
