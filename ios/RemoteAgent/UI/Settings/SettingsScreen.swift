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
    @State private var deletingAccount = false
    @Environment(\.openURL) private var openURL

    var body: some View {
        let state = model.state
        Page(gray: true) {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    LargeTitle(L10n.commonSettings())
                    ConnectionBar()
                    Ticking { now in
                        computers(state, now: now)
                        clients(state, now: now)
                    }
                    AccountSecurityGroup()
                    GroupLabel(L10n.settingsIosNotifications())
                    NotificationsNote()
                    GroupLabel(L10n.settingsIosLocalStorage())
                    GroupCard {
                        GRow(L10n.settingsAttachmentStorage(), value: imagesUsed, chevron: true) { nav.open(.imageStorage) }
                    }
                    GroupLabel(L10n.settingsAppearance())
                    GroupCard { themeRow }
                    GroupLabel(L10n.connectionConnectingConnCheckLabel())
                    GroupCard {
                        GRow(L10n.securityServer(), value: hostOf(context.account.server))
                        GRow(L10n.homeConnectionCheck(), value: connWord(state), chevron: true) {
                            nav.open(.connectionCheck)
                        }
                        .valueLead { StatusMark(state.conn == .online ? .online : .off) }
                    }
                    GroupLabel(L10n.settingsAbout())
                    GroupCard {
                        GRow(L10n.settingsIosVersion(), value: AppInfo.versionLine)
                        GRow(L10n.settingsIosOpenSourceLicenses(), chevron: true) { nav.open(.licenses) }
                        GRow(L10n.settingsIosPrivacyPolicy(), chevron: true) { openURL(PublicInformation.privacy) }
                        GRow(L10n.settingsIosSupportAndFeedback(), chevron: true) { openURL(PublicInformation.support) }
                    }
                    GroupCard {
                        GRow(L10n.settingsIosDeleteEntireAccount(), danger: true) { deletingAccount = true }
                        // It asks first, as every row that ends in an ellipsis does (design-system §8).
                        GRow(L10n.settingsExitAndDeleteLocalKeys(), danger: true) {
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
        .sheet(isPresented: $deletingAccount) { AccountDeletionScreen() }
        .confirm(
            isPresented: $leaving, title: L10n.securityExitAndDeleteLocalKeys(),
            text: exitText, confirm: L10n.securityExit(), danger: true
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
        GroupLabel(L10n.settingsComputer(), action: L10n.settingsAllDevices()) { nav.open(.devices) }
        ForEach(machines, id: \.id) { MachineCard(machine: $0, state: state, now: now) }
        // A blue line after the cards, or where it is to be done when this client cannot.
        AddDeviceEntry(grouped: false)
    }

    /// This client and the first few others, drawn and ordered as 设备 lists them.
    @ViewBuilder private func clients(_ state: RemoteState, now: Int64) -> some View {
        let me = context.me
        let others = sortClients(state.devices.values.filter { $0.kind == .client }, me: me).filter
        { $0.id != me }.prefix(3)
        GroupLabel(L10n.securityClient())
        GroupCard {
            ClientRow(
                client: state.devices[me], id: me, devices: state.devices, me: me, now: now,
                authority: state.authorityDeviceId)
            for c in others {
                ClientRow(
                    client: c, id: c.id, devices: state.devices, me: me, now: now,
                    authority: state.authorityDeviceId)
            }
            GRow(L10n.settingsAllClients(), chevron: true, action: true) { nav.open(.devices) }
        }
    }

    @ViewBuilder private var themeRow: some View {
        let modes: [(ThemeMode, String)] = [(.system, L10n.modesAuto()), (.light, L10n.settingsLight()), (.dark, L10n.settingsDark())]
        let selected = Binding<Int>(
            get: { modes.firstIndex { $0.0 == model.settings.settings.theme } ?? 0 },
            set: { i in model.settings.update { $0.theme = modes[i].0 } })
        let pick = SegPick(modes.map(\.1), selected: selected).accessibilityLabel(L10n.settingsTheme())
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 10) {
                Text(L10n.settingsTheme()).font(.body).ink(DS.ink).lineLimit(1).fixedSize()
                Spacer(minLength: 10)
                pick
            }
            VStack(alignment: .leading, spacing: 0) {
                Text(L10n.settingsTheme()).font(.body).ink(DS.ink).padding(.top, 8)
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
