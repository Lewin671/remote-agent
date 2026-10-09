import RACore
import SwiftUI

/// 设备: computers, where the CLIs run, apart from clients, which operate them (Android's DevicesScreen).
///
/// A placeholder with the rows that lead on: the list is here so that the
/// pages behind it can be reached; times, sorting and the rest of the page
/// are whoever writes the screen's to do, replacing this file's body.
struct DevicesScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav

    var body: some View {
        let state = model.state
        let machines = state.machines.values.sorted { machineLabel(state, id: $0.id) < machineLabel(state, id: $1.id) }
        let clients = sortClients(state.devices.values.filter { $0.kind == .client }, me: context.me)
        Page(gray: true) {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    ConnectionBar()
                    GroupLabel("电脑 · 运行 CLI")
                    if machines.isEmpty {
                        Hint("还没有电脑").padding(.horizontal, 30).padding(.bottom, 12)
                    } else {
                        GroupCard {
                            for m in machines {
                                GRow(machineLabel(state, id: m.id), value: m.online ? "在线" : "不在线", chevron: true) { nav.open(.machine(id: m.id)) }
                            }
                        }
                    }
                    GroupLabel("客户端 · 操作会话")
                    GroupCard {
                        for c in clients {
                            GRow(clientName(c, id: c.id), value: c.id == context.me ? "当前客户端" : nil, chevron: c.id != context.me) { nav.open(.client(id: c.id)) }
                        }
                    }
                    TextAction("添加电脑或客户端", style: T.body) { nav.open(.addDevice) }
                        .padding(.horizontal, Theme.Space.page).padding(.vertical, 8)
                }
            }
        }
        .navBar("设备")
    }
}
