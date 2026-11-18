import RACore
import RAProto
import SwiftUI
import UIKit

/// 设备: computers, where the CLIs run, apart from clients, which operate
/// them (Android's DevicesScreen). A computer's online state says nothing
/// about the clients, and a paired client is not shown as watching or
/// online: what stands beside it is when it was last used.
struct DevicesScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    /// The client a held row's menu is renaming.
    @State private var renaming: PBClientRowView?

    /// Pulling down reads the device list again, while there is a server to ask.
    private func refresh(_ online: Bool) -> (@MainActor @Sendable () async -> Void)? {
        guard online else { return nil }
        let remote = context.remote
        return { await remote.refreshDevices() }
    }

    var body: some View {
        let vm = context.core.devices
        let remote = context.remote
        SettingsPage(refresh: refresh(vm.online)) {
            list(vm)
        }
        .navBar(L10n.settingsDevices())
        .task(id: vm.online) {
            await context.coreHost.openDevices()
            if vm.online && !model.isDemo { await remote.refreshDevices() }
        }
        .inputAlert(
            isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } }),
            title: L10n.settingsClientName(), initial: renaming?.displayName ?? "",
            placeholder: renaming?.description_p ?? "", text: L10n.settingsIosLeaveBlankForTheDefaultName()
        ) { [renaming] name in
            // The dialog stays until the name is stored, and says so when it is not.
            guard let id = renaming?.deviceID else { return }
            try await context.coreHost.renameClient(deviceID: id, name: name)
        }
        .onAppear {
            #if DEBUG
                if model.isDemo, SettingsDemo.state == "renaming" {
                    renaming = vm.clients.first { !$0.isSelf }
                }
            #endif
        }
    }

    @ViewBuilder private func list(_ vm: PBDevicesViewModel) -> some View {
        let err = vm.errorText.rendered()
        if !err.isEmpty { GroupBanner(err, tone: .error) }

        GroupLabel(L10n.settingsComputersRunClis())
        if vm.computers.isEmpty {
            GroupNote(L10n.settingsIosNoComputersYet())
        } else {
            GroupCard {
                for m in vm.computers { MachineRow(machine: m) }
            }
        }
        GroupLabel(L10n.settingsClientsControlSessions())
        let allClients = vm.clients + vm.unfinishedPairings
        GroupCard {
            for c in allClients {
                ClientRow(
                    client: c,
                    onRename: vm.online && c.isSelf ? { renaming = c } : nil)
            }
        }
        GroupNote(L10n.settingsIosTheTimeOnTheRightIsTheLastUse())
        if vm.canAddDevice {
            GroupCard {
                GRow(L10n.homeAddDevice(), action: true, icon: "plus") { nav.open(.addDevice()) }
            }
        } else {
            let hint = vm.addDeviceHint.rendered()
            if !hint.isEmpty { GroupNote(hint) }
        }
    }
}
