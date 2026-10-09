import RACore
import SwiftUI

/// 设置, the second first-level page (Android's SettingsScreen): the
/// account's computers and clients, what this client does about
/// notifications, what it stores, how it looks, and the account itself.
///
/// What Android has and this page leaves out: the notification switches,
/// because this version sends none (`NotificationsScreen`), and updating
/// the app, which the system does for an iOS app.
struct SettingsScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var leaving = false
    @State private var images: ImageStorageModel?

    var body: some View {
        let state = model.state
        Page(gray: true) {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    LargeTitle("设置")
                    ConnectionBar()
                    Ticking { now in computers(state, now: now) }
                    clients(state)
                    GroupLabel("本客户端的通知")
                    NotificationsNote()
                    GroupLabel("本机存储")
                    GroupCard {
                        GRow("图片存储", value: imagesUsed, chevron: true) { nav.open(.imageStorage) }
                    }
                    GroupLabel("外观")
                    GroupCard { themeRow }
                    GroupLabel("账户")
                    GroupCard {
                        GRow("服务器", value: hostOf(context.account.server))
                        GRow("连接检测", value: connWord(state), chevron: true) { nav.open(.connectionCheck) }
                            .valueLead { Dot(state.conn == .online ? Theme.green : Theme.gray) }
                        GRow("版本", value: AppInfo.versionLine)
                        GRow("开源许可", chevron: true) { nav.open(.licenses) }
                        GRow("退出并删除本机密钥", danger: true) { leaving = true }
                    }
                    Color.clear.frame(height: 24)
                }
            }
            .startAtEndForScreenshots()
        }
        .confirm(
            isPresented: $leaving, title: "退出并删除本机密钥？",
            text: "这台设备会退出账户，重新使用时需要配对加入。电脑上的会话继续运行，其他设备不受影响。", confirm: "退出", danger: true
        ) {
            Task { await model.logout() }
        }
        // Read each time the page comes back: an image may have been viewed or sent meanwhile.
        .task {
            let made = images ?? ImageStorageModel(files: model.isDemo ? .demoFiles : .standard(account: context.account))
            images = made
            await made.load()
        }
    }

    // MARK: Groups

    @ViewBuilder private func computers(_ state: RemoteState, now: Int64) -> some View {
        GroupLabel("电脑", action: "全部设备") { nav.open(.devices) }
        GroupCard {
            for m in sortedMachines(state) {
                let status = machineStatus(m, state: state, now: now)
                GRow(machineLabel(state, id: m.id), sub: machineQuotaSummary(asKnown(m, state: state), now: now), value: status?.text, chevron: true) {
                    nav.open(.machine(id: m.id))
                }
                .lead { IconBadge(Lucide.laptop, color: Theme.blueBg) }
                .valueLead { if let status { Dot(status.online ? Theme.green : Theme.gray) } }
                .contextMenu {
                    Button("账户额度") { nav.open(.quota(machine: m.id)) }
                    Button("连接检测") { nav.open(.connectionCheck) }
                }
            }
            GRow("添加设备", action: true, icon: Lucide.plus) { nav.open(.addDevice) }
        }
    }

    @ViewBuilder private func clients(_ state: RemoteState) -> some View {
        let me = context.me
        // A few of the others, the oldest first as on Android; all of them are a tap away.
        let others = state.devices.values.filter { $0.kind == .client && $0.id != me }
            .sorted { $0.createdMs == $1.createdMs ? $0.id < $1.id : $0.createdMs < $1.createdMs }.prefix(3)
        GroupLabel("客户端")
        GroupCard {
            GRow(clientName(state.devices[me], id: me), value: "当前", chevron: true) { nav.open(.client(id: me)) }
            for c in others {
                GRow(clientName(c, id: c.id), value: "已配对", chevron: true) { nav.open(.client(id: c.id)) }
            }
            GRow("全部客户端", chevron: true, action: true) { nav.open(.devices) }
        }
    }

    /// 主题 and its three values. Beside each other, or the values under the name when large text leaves no room for both.
    @ViewBuilder private var themeRow: some View {
        let modes: [(ThemeMode, String)] = [(.system, "自动"), (.light, "亮色"), (.dark, "暗色")]
        let selected = Binding<Int>(
            get: { modes.firstIndex { $0.0 == model.settings.settings.theme } ?? 0 },
            set: { i in model.settings.update { $0.theme = modes[i].0 } })
        let pick = SegPick(modes.map(\.1), selected: selected).accessibilityLabel("主题")
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 10) {
                Text("主题").type(T.body, Theme.fg).lineLimit(1).fixedSize()
                Spacer(minLength: 10)
                pick
            }
            VStack(alignment: .leading, spacing: 0) {
                Text("主题").type(T.body, Theme.fg).padding(.top, 8)
                ScrollView(.horizontal, showsIndicators: false) { pick }
            }
        }
        .padding(.horizontal, 14)
        .frame(maxWidth: .infinity, minHeight: 50, alignment: .leading)
    }

    /// What the two image stores take together, once read.
    private var imagesUsed: String? {
        guard let pending = images?.pending, let cache = images?.cache else { return nil }
        return imageBytes(pending + cache)
    }
}

private extension LocalImageFiles {
    static var demoFiles: LocalImageFiles {
        #if DEBUG
        .demo(nil)
        #else
        LocalImageFiles(pendingUsed: { 0 }, cacheUsed: { 0 }, clearCache: {})
        #endif
    }
}
