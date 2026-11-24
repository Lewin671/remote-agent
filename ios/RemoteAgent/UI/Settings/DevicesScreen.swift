import RACore
import RAProto
import SwiftUI
import UIKit

/// 客户端 secondary screen: current client, paired clients, and unfinished pairings.
/// Computers have their own top-level section in Settings and do not appear here.
struct DevicesScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var renaming: PBClientRowView?
    @State private var removing: PBClientRowView?
    @State private var removeAction = ActionRunner()

    /// Pulling down reads the device list again, while there is a server to ask.
    private func refresh(_ online: Bool) -> (@MainActor @Sendable () async -> Void)? {
        guard online else { return nil }
        let remote = context.remote
        return { await remote.refreshDevices() }
    }

    var body: some View {
        let vm = context.core.devices
        let remote = context.remote
        let subtitle = vm.clientCountText.rendered()
        SettingsPage(refresh: refresh(vm.online)) {
            list(vm)
        }
        .navBar(L10n.settingsClientsTitle(), subtitle: subtitle.isEmpty ? nil : subtitle)
        .task(id: vm.online) {
            await context.coreHost.openDevices()
            if vm.online && !model.isDemo { await remote.refreshDevices() }
        }
        .inputAlert(
            isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } }),
            title: L10n.settingsClientName(), initial: renaming?.displayName ?? "",
            placeholder: renaming?.description_p ?? "", text: L10n.settingsIosLeaveBlankForTheDefaultName()
        ) { [renaming] name in
            guard let id = renaming?.deviceID else { return }
            try await context.coreHost.renameClient(deviceID: id, name: name)
        }
        .confirm(
            isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }),
            title: L10n.securityRemoveDeviceDeviceManagement(),
            text: removing?.pairingIncomplete == true
                ? L10n.securityAfterRemovingThisUnfinishedPairingGenerateANewCode()
                : L10n.securityIosAfterRemovalThisDeviceCanNoLongerOperateThe(),
            confirm: L10n.commonRemove(),
            danger: true
        ) {
            guard let target = removing else { return }
            removing = nil
            removeAction.run {
                try await context.remote.removeDevice(target.deviceID)
                await context.remote.refreshDevices()
            }
        }
        .onAppear {
            #if DEBUG
                if model.isDemo, SettingsDemo.state == "renaming" {
                    renaming = vm.clients.first { !$0.isSelf } ?? vm.clients.first
                }
            #endif
        }
    }

    @ViewBuilder private func list(_ vm: PBDevicesViewModel) -> some View {
        let err = vm.errorText.rendered()
        if !err.isEmpty { GroupBanner(err, tone: .error) }
        if !removeAction.error.isEmpty { GroupBanner(removeAction.error, tone: .error) }

        let selfClients = vm.clients.filter(\.isSelf)
        let pairedClients = vm.clients.filter { !$0.isSelf }

        if !vm.unfinishedPairings.isEmpty {
            GroupLabel(L10n.settingsUnfinishedPairing())
            GroupCard(separatorLeading: 46) {
                ForEach(vm.unfinishedPairings, id: \.deviceID) { c in
                    ClientRow(
                        client: c,
                        onRemove: vm.isManager ? { removing = c } : nil
                    )
                }
            }
            GroupNote(L10n.securityAfterRemovingThisUnfinishedPairingGenerateANewCode())
        }

        if !selfClients.isEmpty {
            GroupCard(separatorLeading: 46) {
                ForEach(selfClients, id: \.deviceID) { c in
                    ClientRow(
                        client: c,
                        onRename: vm.online ? { renaming = c } : nil
                    )
                }
            }
        }

        if !pairedClients.isEmpty {
            GroupLabel(L10n.settingsClientsGroupPaired())
            GroupCard(separatorLeading: 46) {
                ForEach(pairedClients, id: \.deviceID) { c in
                    ClientRow(
                        client: c,
                        onRename: vm.online ? { renaming = c } : nil,
                        onRemove: vm.isManager && !c.isSelf ? { removing = c } : nil
                    )
                }
                if vm.canAddDevice {
                    GRow(L10n.settingsClientsAddClient(), action: true) {
                        nav.open(.addDevice())
                    }
                    .lead {
                        SymbolImage("plus", size: 17, color: DS.blue)
                            .frame(width: 22, alignment: .center)
                    }
                }
            }
        } else if vm.canAddDevice {
            GroupCard(separatorLeading: 46) {
                GRow(L10n.settingsClientsAddClient(), action: true) {
                    nav.open(.addDevice())
                }
                .lead {
                    SymbolImage("plus", size: 17, color: DS.blue)
                        .frame(width: 22, alignment: .center)
                }
            }
        }

        if !vm.canAddDevice {
            let hint = vm.addDeviceHint.rendered()
            if !hint.isEmpty { GroupNote(hint) }
        }

        GroupNote(L10n.settingsClientsFooterRedesign())
    }
}
