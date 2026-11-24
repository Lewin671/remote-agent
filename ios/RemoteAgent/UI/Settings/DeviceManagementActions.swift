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
        let settings = context.core.settings
        let isManager = settings.security.isManager
        let incomplete = !machine && context.core.devices.unfinishedPairings.contains(where: { $0.deviceID == id })
        let present: Bool = if machine {
            settings.machines.first(where: { $0.machineID == id })?.inDeviceDirectory ?? false
        } else {
            incomplete
                || (context.core.clientDetail.deviceID == id
                    ? context.core.clientDetail.found
                    : context.core.devices.clients.contains(where: { $0.deviceID == id }))
        }
        let addHint = settings.addDeviceHint.rendered().or(L10n.securityAddOnAuthority(name: settings.security.authorityName))
        if isManager && id != context.me {
            GroupCard {
                if present {
                    GRow(
                        incomplete
                            ? L10n.securityRemoveUnfinishedPairing()
                            : (machine ? L10n.settingsRemoveComputerEllipsis() : L10n.securityRemoveDevice()),
                        danger: true,
                        enabled: !action.running
                    ) { removing = true }
                } else if machine {
                    GRow(L10n.securityIosRejoinFromOriginalRecords(), chevron: true, enabled: !action.running) {
                        nav.open(.addDevice(replacing: id))
                    }
                    GRow(L10n.settingsDeleteComputerRecordsEllipsis(), danger: true, enabled: !action.running) { deleting = true }
                }
            }
            if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
            if !done.isEmpty { GroupBanner(done) }
        } else if !isManager { GroupNote(addHint) }
        Color.clear.frame(height: 0)
            .confirm(isPresented: $removing, title: L10n.securityRemoveDeviceDeviceManagement(),
                text: incomplete
                    ? L10n.securityAfterRemovingThisUnfinishedPairingGenerateANewCode()
                    : L10n.securityIosAfterRemovalThisDeviceCanNoLongerOperateThe(),
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
