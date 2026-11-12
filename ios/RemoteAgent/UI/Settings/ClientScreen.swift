import RACore
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
        let state = model.state
        let me = context.me
        let d = state.devices[id]
        let online = state.conn == .online
        let remote = context.remote
        SettingsPage(refresh: refresh(online)) {
            if d == nil && state.conn == .online && !state.devices.isEmpty {
                // The list is read and it is not in it.
                PageNote(L10n.settingsIosThisClientIsNotInTheCurrentTrustedDirectory())
            } else {
                Ticking { now in details(d, state: state, me: me, online: online, now: now) }
            }
        }
        .navBar(clientName(d, id: id, among: state.devices), subtitle: id == me ? L10n.settingsIosCurrentClient() : L10n.settingsPairedClient())
        .inputAlert(
            isPresented: $editing, title: L10n.settingsClientName(), initial: d?.meta?.name ?? "",
            placeholder: defaultClientName(d, id: id, among: state.devices), text: L10n.settingsIosLeaveBlankForTheDefaultName()
        ) { name in
            // The dialog stays until the name is stored, and says so when it is not.
            try await remote.renameDevice(id, name: name.jvmTrim())
        }

        // What 保存 in the name dialog, 恢复默认名称 and 移除 in the confirmation do (debug builds).
        .debugSignal("client.rename") {
            save.run { try await remote.renameDevice(id, name: L10n.settingsIosNameSetInSettings()) }
        }
        .debugSignal("client.reset") { save.run { try await remote.renameDevice(id, name: "") } }
        .onAppear {
            #if DEBUG
                if model.isDemo, SettingsDemo.state == "renaming" { editing = true }

            #endif
        }
    }

    @ViewBuilder private func details(
        _ d: DeviceView?, state: RemoteState, me: String, online: Bool, now: Int64
    ) -> some View {
        let named = !(d?.meta?.name ?? "").isEmpty
        let remote = context.remote
        GroupLabel(L10n.securityClient())
        GroupCard {
            GRow(
                L10n.settingsName(), value: save.running ? L10n.commonIosSaving() : (d?.meta?.name ?? "").or(L10n.settingsUnnamed()),
                chevron: id == me, enabled: online && id == me && d != nil && !save.running
            ) { if id == me { editing = true } }
            if named && id == me {
                GRow(
                    L10n.settingsRestoreDefaultName(), value: defaultClientName(d, id: id, among: state.devices), action: true,
                    enabled: online && !save.running
                ) {
                    save.run { try await remote.renameDevice(id, name: "") }
                }
            }
            GRow(L10n.settingsIosType(), value: clientDetail(d))
            GRow(L10n.settingsLastUsed(), value: id == me ? L10n.settingsNow() : lastUsed(d, now: now).or(L10n.settingsNoRecord()))
            if let created = d?.createdMs, created > 0 {
                GRow(L10n.settingsAdded(), value: dayText(created, now: now))
            }
            GRow(L10n.settingsDeviceId(), value: "…" + shortId(id))
        }
        if !save.error.isEmpty { GroupBanner(L10n.settingsNameSaveError(reason: String(describing: save.error)), tone: .error) }
        GroupNote(L10n.settingsIosNamesSyncEncryptedToOtherClientsTheServerCannot())

        if id == me {
            GroupCard {
                GRow(L10n.settingsIosExitAndDeleteLocalKeysFromSettings(), chevron: true) { nav.open(.settings) }
            }
        } else {
            DeviceManagementActions(id: id, machine: false)
        }
    }
}
