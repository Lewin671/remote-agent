import RACore
import SwiftUI
import UIKit

/// 设备: computers, where the CLIs run, apart from clients, which operate
/// them (Android's DevicesScreen). A computer's online state says nothing
/// about the clients, and a paired client is not shown as watching or
/// online: what stands beside it is when it was last used.
struct DevicesScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    /// The client a held row's menu is renaming.
    @State private var renaming: DeviceView?

    /// Pulling down reads the device list again, while there is a server to ask.
    private func refresh(_ online: Bool) -> (@MainActor @Sendable () async -> Void)? {
        guard online else { return nil }
        let remote = context.remote
        return { await remote.refreshDevices() }
    }

    var body: some View {
        let state = model.state
        let remote = context.remote
        let online = state.conn == .online
        SettingsPage(refresh: refresh(online)) {
            Ticking { now in list(state, online: online, now: now) }
        }
        .navBar("设备")
        // The times on this page are as of opening it.
        .task(id: online) { if online && !model.isDemo { await remote.refreshDevices() } }
        .inputAlert(
            isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } }),
            title: "客户端名称", initial: renaming?.meta?.name ?? "",
            placeholder: renaming.map { defaultClientName($0, id: $0.id, among: state.devices) } ?? "", text: "留空就用默认名称"
        ) { [renaming] name in
            // The dialog stays until the name is stored, and says so when it is not.
            guard let id = renaming?.id else { return }
            try await remote.renameDevice(id, name: name.jvmTrim())
        }
        .onAppear {
            #if DEBUG
                let other = sortClients(
                    state.devices.values.filter { $0.kind == .client }, me: context.me
                ).first { $0.id != context.me }
                if model.isDemo, SettingsDemo.state == "renaming" { renaming = other }
            #endif
        }
    }

    @ViewBuilder private func list(_ state: RemoteState, online: Bool, now: Int64) -> some View {
        let me = context.me
        let machines = sortedMachines(state)
        let clients = sortClients(state.devices.values.filter { $0.kind == .client }, me: me)

        GroupLabel("电脑 · 运行 CLI")
        if machines.isEmpty {
            GroupNote(state.loaded ? "还没有电脑" : "读取中…")
        } else {
            GroupCard {
                for m in machines { MachineRow(machine: m, state: state, now: now) }
            }
        }
        GroupLabel("客户端 · 操作会话")
        GroupCard {
            // Before the list is read this client is still here.
            if clients.isEmpty {
                ClientRow(
                    client: nil, id: me, devices: state.devices, me: me, now: now,
                    authority: state.authorityDeviceId)
            }
            for c in clients {
                ClientRow(
                    client: c, id: c.id, devices: state.devices, me: me, now: now,
                    authority: state.authorityDeviceId,
                    onRename: online && c.id == me ? { renaming = c } : nil)
            }
        }
        GroupNote("右边是上次使用的时间。" + addDeviceHint)
    }
}
