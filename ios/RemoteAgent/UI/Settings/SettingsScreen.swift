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
    @State private var exitText = ""
    @State private var images: ImageStorageModel?

    var body: some View {
        let state = model.state
        Page(gray: true) {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    LargeTitle("设置")
                    ConnectionBar()
                    Ticking { now in
                        computers(state, now: now)
                        clients(state, now: now)
                    }
                    AccountSecurityGroup()
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
                        GRow("连接检测", value: connWord(state), chevron: true) {
                            nav.open(.connectionCheck)
                        }
                        .valueLead { Dot(state.conn == .online ? Theme.green : Theme.gray) }
                        GRow("版本", value: AppInfo.versionLine)
                        GRow("开源许可", chevron: true) { nav.open(.licenses) }
                        // It asks first, as every row that ends in an ellipsis does (design-system §8).
                        GRow("退出并删除本机密钥…", danger: true) {
                            Task {
                                do {
                                    let c = try await context.remote.authorityCoordinator()
                                    let pending = try await c.pendingOperation()
                                    exitText = AuthorityAccess.exitAdvice(authority: try await c.isAuthority(), unresolved: pending != nil)
                                } catch {
                                    exitText = AuthorityAccess.exitAdvice(authority: true, unresolved: true)
                                }
                                leaving = true
                            }
                        }
                    }
                    Color.clear.frame(height: 24)
                }
            }
            .startAtEndForScreenshots()
        }
        .confirm(
            isPresented: $leaving, title: "退出并删除本机密钥？",
            text: exitText, confirm: "退出", danger: true
        ) {
            Task { await model.logout() }
        }
        // Read each time the page comes back: an image may have been viewed or sent meanwhile.
        .task {
            let made =
                images
                ?? ImageStorageModel(
                    files: model.isDemo
                        ? .demoFiles
                        : .stores(cache: model.imageCache, pending: model.pendingImages))
            images = made
            await made.load()
        }
    }

    // MARK: Groups

    @ViewBuilder private func computers(_ state: RemoteState, now: Int64) -> some View {
        let machines = sortedMachines(state)
        GroupLabel("电脑", action: "全部设备") { nav.open(.devices) }
        if !machines.isEmpty {
            GroupCard {
                for m in machines { MachineRow(machine: m, state: state, now: now) }
            }
        }
        AddDeviceEntry()
    }

    /// This client and the first few others, drawn and ordered as 设备 lists them.
    @ViewBuilder private func clients(_ state: RemoteState, now: Int64) -> some View {
        let me = context.me
        let others = sortClients(state.devices.values.filter { $0.kind == .client }, me: me).filter
        { $0.id != me }.prefix(3)
        GroupLabel("客户端")
        GroupCard {
            ClientRow(
                client: state.devices[me], id: me, devices: state.devices, me: me, now: now,
                authority: state.authorityDeviceId)
            for c in others {
                ClientRow(
                    client: c, id: c.id, devices: state.devices, me: me, now: now,
                    authority: state.authorityDeviceId)
            }
            GRow("全部客户端", chevron: true, action: true) { nav.open(.devices) }
        }
    }

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

extension LocalImageFiles {
    fileprivate static var demoFiles: LocalImageFiles {
        #if DEBUG
            .demo(nil)
        #else
            LocalImageFiles(pendingUsed: { 0 }, cacheUsed: { 0 }, clearCache: {})
        #endif
    }
}
