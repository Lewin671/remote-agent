import RACore
import SwiftUI

/// A client's details (Android's ClientScreen): its name for the whole
/// account, what it runs on, when it was last used, and removing it.
struct ClientScreen: View {
    let id: String
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var save = ActionRunner()
    @State private var remove = ActionRunner()
    @State private var editing = false
    @State private var confirming = false

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
                PageNote("这个客户端已经移除了")
            } else {
                Ticking { now in details(d, state: state, me: me, online: online, now: now) }
            }
        }
        .navBar(clientName(d, id: id), subtitle: id == me ? "当前客户端" : "已配对的客户端")
        .inputAlert(
            isPresented: $editing, title: "客户端名称", initial: d?.meta?.name ?? "",
            placeholder: defaultClientName(d, id: id), text: "留空就用默认名称"
        ) { name in
            // The dialog stays until the name is stored, and says so when it is not.
            try await remote.renameDevice(id, name: name.jvmTrim())
        }
        .confirm(
            isPresented: $confirming, title: "移除 \(clientName(d, id: id))？", text: RemovalText.clientConfirm,
            confirm: "移除", danger: true
        ) {
            remove.run({ try await remote.removeDevice(id) }, onDone: nav.back)
        }
        // What 保存 in the name dialog, 恢复默认名称 and 移除 in the confirmation do (debug builds).
        .debugSignal("client.rename") { save.run { try await remote.renameDevice(id, name: "设置页改的名字") } }
        .debugSignal("client.reset") { save.run { try await remote.renameDevice(id, name: "") } }
        .debugSignal("client.remove") { remove.run({ try await remote.removeDevice(id) }, onDone: nav.back) }
        .onAppear {
            #if DEBUG
            if model.isDemo, SettingsDemo.state == "renaming" { editing = true }
            if model.isDemo, SettingsDemo.state == "removing" { confirming = true }
            #endif
        }
    }

    @ViewBuilder private func details(_ d: DeviceView?, state: RemoteState, me: String, online: Bool, now: Int64) -> some View {
        let named = !(d?.meta?.name ?? "").isEmpty
        let remote = context.remote
        GroupLabel("客户端")
        GroupCard {
            GRow(
                "名称", value: save.running ? "保存中…" : (d?.meta?.name ?? "").or("未命名"), chevron: true,
                enabled: online && d != nil && !save.running
            ) { editing = true }
            if named {
                GRow("恢复默认名称", value: defaultClientName(d, id: id), action: true, enabled: online && !save.running) {
                    save.run { try await remote.renameDevice(id, name: "") }
                }
            }
            GRow("种类", value: clientDetail(d))
            GRow("上次使用", value: id == me ? "现在" : lastUsed(d, now: now).or("没有记录"))
            if let created = d?.createdMs, created > 0 { GRow("添加于", value: dayText(created, now: now)) }
            GRow("设备 ID", value: "…" + shortId(id))
        }
        if !save.error.isEmpty { GroupBanner("没能保存名称：\(save.error)", tone: .error) }
        GroupNote("名称加密后同步到其他客户端，服务器看不到。")

        if id == me {
            GroupCard {
                GRow("退出并删除本机密钥在设置页", chevron: true) { nav.open(.settings) }
            }
        } else {
            GroupCard {
                GRow(remove.running ? "正在移除…" : "移除这个客户端…", danger: true, enabled: online && !remove.running) { confirming = true }
            }
            GroupNote(RemovalText.clientNote)
            if !remove.error.isEmpty { GroupBanner(remove.error, tone: .error) }
        }
    }
}
