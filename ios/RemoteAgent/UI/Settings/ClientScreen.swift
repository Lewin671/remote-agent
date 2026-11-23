import RACore
import RAProto
import SwiftUI

/// A client's details (Android's ClientScreen): its name for the whole
/// account, what it runs on, when it was last used, and removing it.
struct ClientScreen: View {
    let id: String
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @State private var save = ActionRunner()
    @State private var editing = false

    /// Pulling down reads the device list again, while there is a server to ask.
    private func refresh(_ online: Bool) -> (@MainActor @Sendable () async -> Void)? {
        guard online else { return nil }
        let remote = context.remote
        return { await remote.refreshDevices() }
    }

    var body: some View {
        let vm = context.core.clientDetail
        let online = context.core.settings.online
        SettingsPage(refresh: refresh(online)) {
            if !vm.found && online && !context.core.devices.clients.isEmpty {
                // The list is read and it is not in it.
                PageNote(L10n.settingsIosThisClientIsNotInTheCurrentTrustedDirectory())
            } else {
                details(vm)
            }
        }
        .navBar(
            vm.displayName,
            subtitle: !vm.found ? nil : (vm.isSelf ? L10n.settingsIosCurrentClient() : L10n.settingsPairedClient())
        )
        .task(id: id) {
            await context.coreHost.openClientDetail(deviceID: id)
            if online && !model.isDemo { await context.remote.refreshDevices() }
        }
        .inputAlert(
            isPresented: $editing, title: L10n.settingsClientName(), initial: vm.customName,
            placeholder: vm.defaultName, text: L10n.settingsIosLeaveBlankForTheDefaultName()
        ) { name in
            // The dialog stays until the name is stored, and says so when it is not.
            try await context.coreHost.renameClient(deviceID: id, name: name)
        }

        // What 保存 in the name dialog, 恢复默认名称 and 移除 in the confirmation do (debug builds).
        .debugSignal("client.rename") {
            save.run { try await context.coreHost.renameClient(deviceID: id, name: L10n.settingsIosNameSetInSettings()) }
        }
        .debugSignal("client.reset") {
            save.run { try await context.coreHost.renameClient(deviceID: id, name: "", restoreDefault: true) }
        }
        .onAppear {
            #if DEBUG
                if model.isDemo, SettingsDemo.state == "renaming" { editing = true }
            #endif
        }
    }

    @ViewBuilder private func details(_ vm: PBClientDetailViewModel) -> some View {
        let busy = save.running || vm.renameSaving
        GroupLabel(L10n.securityClient())
        GroupCard {
            GRow(
                L10n.settingsName(),
                value: busy ? L10n.commonIosSaving() : vm.customName.or(L10n.settingsUnnamed()),
                chevron: vm.isSelf,
                enabled: vm.canRename && !busy
            ) { if vm.canRename { editing = true } }
            if vm.canRestoreDefault {
                GRow(
                    L10n.settingsRestoreDefaultName(), value: vm.defaultName, action: true,
                    enabled: vm.canRename && !busy
                ) {
                    save.run { try await context.coreHost.renameClient(deviceID: id, name: "", restoreDefault: true) }
                }
            }
            if !vm.description_p.isEmpty {
                GRow(L10n.settingsIosType(), value: vm.description_p)
            }
            GRow(L10n.settingsLastUsed(), value: vm.lastUsedText.rendered().or(L10n.settingsNoRecord()))
            let added = vm.addedText.rendered()
            if !added.isEmpty && added != L10n.settingsNoRecord() {
                GRow(L10n.settingsAdded(), value: added)
            }
            GRow(L10n.settingsDeviceId(), value: vm.shortID)
        }
        let renameErr = vm.renameError.rendered()
        if !renameErr.isEmpty {
            GroupBanner(renameErr, tone: .error)
        } else if !save.error.isEmpty {
            GroupBanner(L10n.settingsNameSaveError(reason: String(describing: save.error)), tone: .error)
        }
        let hint = vm.managementHint.rendered()
        if !hint.isEmpty {
            GroupNote(hint)
        }

        if vm.showExitAction {
            GroupCard {
                GRow(L10n.settingsIosExitAndDeleteLocalKeysFromSettings(), chevron: true) { nav.open(.settings) }
            }
        } else {
            DeviceManagementActions(id: id, machine: false)
        }
    }
}
