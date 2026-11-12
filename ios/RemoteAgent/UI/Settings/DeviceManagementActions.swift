import RACore
import SwiftUI

struct DeviceManagementActions: View {
    let id: String
    let machine: Bool
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var removing = false
    @State private var deleting = false
    @State private var action = ActionRunner()
    @State private var done = ""

    var body: some View {
        let state = app.state
        let access = AuthorityAccess(state: state, me: context.me)
        let present = state.devices[id] != nil
        if access.isAuthority && id != context.me {
            GroupCard {
                if present {
                    GRow(L10n.securityRemoveDevice(), danger: true, enabled: !action.running) { removing = true }
                } else if machine {
                    GRow(L10n.securityIosRejoinFromOriginalRecords(), chevron: true, enabled: !action.running) {
                        nav.open(.addDevice(replacing: id))
                    }
                    GRow(L10n.securityDeleteThisComputerSRecords(), danger: true, enabled: !action.running) { deleting = true }
                }
            }
            if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
            if !done.isEmpty { GroupBanner(done) }
        } else if !access.isAuthority { GroupNote(access.addHint) }
        Color.clear.frame(height: 0)
            .confirm(isPresented: $removing, title: L10n.securityRemoveDeviceDeviceManagement(),
                text: L10n.securityIosAfterRemovalThisDeviceCanNoLongerOperateThe(),
                confirm: L10n.commonRemove(), danger: true) {
                    done = ""
                    action.run { try await context.remote.removeDevice(id) } onDone: { done = L10n.securityIosDeviceRemoved() }
                }
            .confirm(isPresented: $deleting, title: L10n.securityIosDeleteThisComputerSRecords(),
                text: L10n.securityThisPermanentlyDeletesSessionsEventsAndImagesOnThe(),
                confirm: L10n.securityDeleteRecords(), danger: true) {
                    done = ""
                    action.run { try await context.remote.deleteMachineRecords(id) } onDone: { done = L10n.securityIosComputerRecordsDeleted() }
                }
    }
}
