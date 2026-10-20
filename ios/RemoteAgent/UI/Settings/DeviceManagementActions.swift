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
                    GRow("移除设备…", danger: true, enabled: !action.running) { removing = true }
                } else if machine {
                    GRow("从原档案重新加入", chevron: true, enabled: !action.running) {
                        nav.open(.addDevice(replacing: id))
                    }
                    GRow("删除这台电脑的记录…", danger: true, enabled: !action.running) { deleting = true }
                }
            }
            if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
            if !done.isEmpty { GroupBanner(done) }
        } else if !access.isAuthority { GroupNote(access.addHint) }
        Color.clear.frame(height: 0)
            .confirm(isPresented: $removing, title: "移除设备？",
                text: "移除后，这台设备不能再操作账户。电脑本地文件保留，中继上的档案可另行删除。",
                confirm: "移除", danger: true) {
                    done = ""
                    action.run { try await context.remote.removeDevice(id) } onDone: { done = "设备已移除。" }
                }
            .confirm(isPresented: $deleting, title: "删除这台电脑的记录？",
                text: "将删除中继上的会话、事件和图片，无法撤销。不会删除电脑本地文件，也不会代替移除设备。",
                confirm: "删除记录", danger: true) {
                    done = ""
                    action.run { try await context.remote.deleteMachineRecords(id) } onDone: { done = "电脑记录已删除。" }
                }
    }
}
