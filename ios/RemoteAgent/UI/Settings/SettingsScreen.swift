import RACore
import RAProto
import SwiftUI

/// 设置, 4 groups (`电脑` / `本机` / `账户与设备` / `关于`) + separated bottom danger card.
/// Keeps iOS Store-compliant Privacy Policy, Support Feedback, and Account Deletion links.
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
        let vm = context.core.settings
        Page(gray: true) {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    LargeTitle(L10n.commonSettings())
                    ConnectionBar()
                    banners(vm)
                    computers(vm)
                    thisDevice(vm)
                    accountAndDevices(vm)
                    aboutSection()
                    dangerSection()
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
            await context.coreHost.openSettings()
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

    // MARK: Banners

    @ViewBuilder private func banners(_ vm: PBSettingsViewModel) -> some View {
        if vm.clockSkewed {
            GroupBanner(vm.clockSkewBannerText.rendered(), tone: .warn)
        }
        if vm.isManager && vm.recoveryPending {
            let recMsg = vm.recoveryBannerText.rendered()
            if !recMsg.isEmpty {
                GroupBanner(recMsg, tone: .warn, action: L10n.dockHandle()) {
                    nav.open(.accountSecurity)
                }
            }
        }
        if vm.unlinked {
            let unlinkedMsg = vm.unlinkedBannerText.rendered()
            if !unlinkedMsg.isEmpty {
                GroupBanner(unlinkedMsg, tone: .plain)
            }
        }
    }

    // MARK: Groups

    @ViewBuilder private func computers(_ vm: PBSettingsViewModel) -> some View {
        let countText = vm.machineCountText.rendered()
        GroupLabel(L10n.settingsSectionComputers(), trailing: countText)
        if vm.machines.isEmpty {
            GroupCard {
                VStack(alignment: .leading, spacing: 6) {
                    Text(vm.emptyMachinesText.rendered().or(L10n.settingsIosNoComputersYet()))
                        .font(.body.weight(.semibold))
                        .foregroundStyle(DS.ink)
                    let bodyText = vm.emptyMachinesBody.rendered()
                    if !bodyText.isEmpty {
                        Text(bodyText)
                            .font(.subheadline)
                            .foregroundStyle(DS.ink2)
                    }
                    if vm.canAddDevice {
                        Btn(L10n.settingsAddFirstComputer(), kind: .primary) { nav.open(.addDevice()) }
                            .padding(.top, 8)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 14)
                .padding(.vertical, 14)
            }
            if !vm.canAddDevice {
                let hint = vm.addDeviceHint.rendered()
                if !hint.isEmpty { GroupNote(hint) }
            }
        } else {
            GroupCard(separatorLeading: 46) {
                ForEach(vm.machines, id: \.machineID) { MachineCard(machine: $0) }
                if vm.canAddDevice {
                    GRow(L10n.settingsAddDeviceItem(), action: true) { nav.open(.addDevice()) }
                        .lead {
                            SymbolImage("plus", size: 17, color: DS.blue)
                                .frame(width: 22, alignment: .center)
                        }
                }
            }
            if !vm.canAddDevice {
                let hint = vm.addDeviceHint.rendered()
                if !hint.isEmpty { GroupNote(hint) }
            }
        }
    }

    @ViewBuilder private func thisDevice(_ vm: PBSettingsViewModel) -> some View {
        let notifSummary = vm.notifications.summaryText.rendered().or(L10n.settingsNotificationsSummaryOff())
        GroupLabel(L10n.settingsSectionThisDevice())
        GroupCard {
            GRow(
                L10n.settingsNotificationsTitle(),
                value: notifSummary,
                chevron: true,
                valueColor: vm.notifications.summaryWarn ? DS.orangeInk : DS.ink2
            ) {
                nav.open(.notifications)
            }
            themeRow(vm)
            GRow(L10n.settingsLocalCache(), value: imagesUsed, chevron: true) {
                nav.open(.imageStorage)
            }
        }
    }

    @ViewBuilder private func accountAndDevices(_ vm: PBSettingsViewModel) -> some View {
        let clientsSummary = vm.clientsSummaryText.rendered()
        GroupLabel(L10n.settingsSectionAccountAndDevices())
        GroupCard {
            GRow(
                L10n.settingsClientsTitle(),
                value: clientsSummary.isEmpty ? nil : clientsSummary,
                chevron: true,
                valueColor: vm.clientsSummaryWarn ? DS.orangeInk : DS.ink2
            ) {
                nav.open(.devices)
            }
            GRow(L10n.settingsAccountSecurityTitle(), value: vm.security.summaryText.rendered(), chevron: true) {
                nav.open(.accountSecurity)
            }
            GRow(L10n.settingsServerAndConnection(), value: hostOf(context.account.server), chevron: true) {
                nav.open(.connectionCheck)
            }
            .valueLead { StatusMark(vm.online ? .online : .off) }
        }
    }

    @ViewBuilder private func aboutSection() -> some View {
        GroupLabel(L10n.settingsAbout())
        GroupCard {
            GRow(L10n.settingsVersionLabel(), value: AppInfo.versionLine)
            GRow(L10n.settingsOpenSourceLicenses(), chevron: true) { nav.open(.licenses) }
            GRow(L10n.settingsIosPrivacyPolicy(), chevron: true) { openURL(PublicInformation.privacy) }
            GRow(L10n.settingsIosSupportAndFeedback(), chevron: true) { openURL(PublicInformation.support) }
        }
    }

    @ViewBuilder private func dangerSection() -> some View {
        GroupCard {
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
            GRow(L10n.settingsIosDeleteEntireAccount(), danger: true) { deletingAccount = true }
        }
        .padding(.top, 8)
    }

    @ViewBuilder private func themeRow(_ vm: PBSettingsViewModel) -> some View {
        let modes: [(PBThemePreference, String)] = [(.auto, L10n.modesAuto()), (.light, L10n.settingsLight()), (.dark, L10n.settingsDark())]
        let selected = Binding<Int>(
            get: { modes.firstIndex { $0.0 == vm.appearance.theme } ?? 0 },
            set: { i in context.dispatch { $0.setAppearancePref.theme = modes[i].0 } })
        let pick = SegPick(modes.map(\.1), selected: selected).accessibilityLabel(L10n.settingsAppearance())
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 10) {
                Text(L10n.settingsAppearance()).font(.body).ink(DS.ink).lineLimit(1).fixedSize()
                Spacer(minLength: 10)
                pick
            }
            VStack(alignment: .leading, spacing: 0) {
                Text(L10n.settingsAppearance()).font(.body).ink(DS.ink).padding(.top, 8)
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
