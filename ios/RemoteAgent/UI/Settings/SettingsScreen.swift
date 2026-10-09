import RACore
import SwiftUI

/// 设置, the second first-level page (Android's SettingsScreen).
///
/// A placeholder that already does what the rest of the app needs of it:
/// the rows that lead to the other settings pages, and leaving the account.
/// Quotas, notifications, the theme and the version row are whoever writes
/// the screen's to add, replacing this file's body.
struct SettingsScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var leaving = false

    var body: some View {
        let state = model.state
        let machines = state.machines.values.sorted { machineLabel(state, id: $0.id) < machineLabel(state, id: $1.id) }
        Page(gray: true) {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    LargeTitle("设置")
                    ConnectionBar()
                    GroupLabel("电脑", action: "全部设备") { nav.open(.devices) }
                    GroupCard {
                        for m in machines {
                            GRow(machineLabel(state, id: m.id), value: m.online ? "在线" : "不在线", chevron: true) { nav.open(.machine(id: m.id)) }
                                .lead { IconBadge(Lucide.laptop, color: Theme.blueBg) }
                        }
                        GRow("添加设备", action: true, icon: Lucide.plus) { nav.open(.addDevice) }
                    }
                    GroupLabel("客户端")
                    GroupCard {
                        GRow(clientName(state.devices[context.me], id: context.me), value: "当前") { nav.open(.client(id: context.me)) }
                        GRow("全部客户端", chevron: true, action: true) { nav.open(.devices) }
                    }
                    GroupLabel("本客户端的通知")
                    GroupCard {
                        GRow("通知", chevron: true) { nav.open(.notifications) }
                    }
                    GroupLabel("本机存储")
                    GroupCard {
                        GRow("图片存储", chevron: true) { nav.open(.imageStorage) }
                    }
                    GroupLabel("账户")
                    GroupCard {
                        GRow("服务器", value: context.account.server.replacingOccurrences(of: "https://", with: ""))
                        GRow("连接检测", value: connWord(state), chevron: true) { nav.open(.connectionCheck) }
                        GRow("版本", value: AppInfo.versionLine)
                        GRow("开源许可", chevron: true) { nav.open(.licenses) }
                        GRow("退出并删除本机密钥", danger: true) { leaving = true }
                    }
                    Spacer(minLength: 24)
                }
            }
        }
        .confirm(
            isPresented: $leaving, title: "退出并删除本机密钥？",
            text: "这台设备会退出账户，重新使用时需要配对加入。电脑上的会话继续运行，其他设备不受影响。", confirm: "退出", danger: true
        ) {
            Task { await model.logout() }
        }
    }
}
