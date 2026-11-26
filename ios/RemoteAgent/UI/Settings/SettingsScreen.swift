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
    @Environment(\.horizontalSizeClass) private var sizeClass
    @State private var leaving = false
    @State private var leavingAdvice: AppModel.RejoinAdvice?
    @State private var exitText = ""
    @State private var exitError = ""
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
            Task {
                guard await model.rejoinAdvice() == leavingAdvice else {
                    exitError = L10n.securityIosSecurityOperationStateChangedChooseHowToExitAgain()
                    return
                }
                exitError = ""
                await model.logout()
            }
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
        let clockMsg = vm.clockSkewed ? vm.clockSkewBannerText.rendered() : ""
        if !clockMsg.isEmpty {
            GroupBanner(clockMsg, tone: .warn)
        }
        if vm.recoveryPending {
            let recMsg = vm.recoveryBannerText.rendered().or(L10n.settingsRecoveryWaitingBanner())
            if !recMsg.isEmpty {
                if vm.isManager {
                    GroupBanner(recMsg, tone: .warn, action: L10n.dockHandle()) {
                        nav.open(.accountSecurity)
                    }
                } else {
                    GroupBanner(recMsg, tone: .warn)
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
        let wide = sizeClass == .regular
        let countText = vm.machineCountText.rendered()
        GroupLabel(L10n.settingsSectionComputers(), trailing: countText)
        if vm.machines.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 10) {
                    SymbolImage("laptopcomputer", size: 20, color: DS.ink2)
                    Text(vm.emptyMachinesText.rendered().or(L10n.settingsIosNoComputersYet()))
                        .font(.body.weight(.semibold))
                        .foregroundStyle(DS.ink)
                }
                let bodyText = vm.emptyMachinesBody.rendered()
                if !bodyText.isEmpty {
                    Text(bodyText)
                        .font(.footnote)
                        .foregroundStyle(DS.ink2)
                } else if !vm.canAddDevice {
                    let hint = vm.addDeviceHint.rendered()
                    if !hint.isEmpty {
                        Text(hint)
                            .font(.footnote)
                            .foregroundStyle(DS.ink2)
                    }
                }
                if !vm.machinesLoading && vm.canAddDevice {
                    Button {
                        nav.open(.addDevice())
                    } label: {
                        HStack(spacing: 6) {
                            SymbolImage("plus", size: 16, color: DS.btnFg)
                            Text(L10n.settingsAddFirstComputer())
                                .font(.subheadline.weight(.semibold))
                                .foregroundStyle(DS.btnFg)
                        }
                        .padding(.horizontal, 18)
                        .frame(minHeight: wide ? 36 : 44)
                        .background(
                            DS.btn,
                            in: RoundedRectangle(cornerRadius: wide ? 8 : DS.Radius.small, style: .continuous)
                        )
                    }
                    .buttonStyle(.plain)
                    .padding(.top, 8)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 18)
            .padding(.vertical, 20)
            .background(
                DS.gbg,
                in: RoundedRectangle(cornerRadius: wide ? 14 : DS.Radius.card, style: .continuous)
            )
            .padding(.horizontal, 16)
            .padding(.bottom, 12)
        } else {
            if wide && vm.machines.count >= 2 {
                let pairs = stride(from: 0, to: vm.machines.count, by: 2).map { idx in
                    Array(vm.machines[idx..<min(idx + 2, vm.machines.count)])
                }
                VStack(spacing: 12) {
                    ForEach(Array(pairs.enumerated()), id: \.offset) { _, pair in
                        HStack(alignment: .top, spacing: 12) {
                            MachineCard(machine: pair[0])
                                .frame(maxWidth: .infinity, maxHeight: .infinity)
                            if pair.count > 1 {
                                MachineCard(machine: pair[1])
                                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                            } else {
                                Color.clear.frame(maxWidth: .infinity)
                            }
                        }
                        .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .padding(.horizontal, 16)
                .padding(.bottom, 10)
            } else {
                VStack(spacing: 10) {
                    ForEach(vm.machines, id: \.machineID) { MachineCard(machine: $0) }
                }
                .padding(.horizontal, 16)
                .padding(.bottom, 10)
            }
            if vm.canAddDevice {
                Button {
                    nav.open(.addDevice())
                } label: {
                    HStack(spacing: 6) {
                        SymbolImage("plus", size: 17, color: DS.blue)
                        Text(L10n.settingsAddDeviceItem())
                            .font(.system(size: wide ? 15 : 17, weight: .semibold))
                            .foregroundStyle(DS.blue)
                    }
                    .frame(maxWidth: .infinity, minHeight: wide ? 44 : 50)
                    .background(
                        DS.gbg,
                        in: RoundedRectangle(cornerRadius: wide ? 14 : DS.Radius.group, style: .continuous)
                    )
                    .contentShape(RoundedRectangle(cornerRadius: wide ? 14 : DS.Radius.group, style: .continuous))
                }
                .buttonStyle(.plain)
                .padding(.horizontal, 16)
                .padding(.bottom, 12)
            } else {
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
            .valueLead { serverDot(vm) }
        }
    }

    @ViewBuilder private func serverDot(_ vm: PBSettingsViewModel) -> some View {
        if !vm.online || vm.connCheck.serverState == .unavailable {
            Dot(DS.red)
        } else if vm.connCheck.serverState == .slow || vm.connCheck.serverState == .checking {
            Dot(DS.orange)
        } else {
            StatusMark(.online)
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
        if !exitError.isEmpty {
            GroupBanner(exitError, tone: .error)
        }
        GroupCard {
            // It asks first, as every row that ends in an ellipsis does (design-system §8).
            GRow(L10n.settingsExitAndDeleteLocalKeys(), danger: true) {
                Task {
                    exitError = ""
                    let adv = await model.rejoinAdvice()
                    leavingAdvice = adv
                    if let adv {
                        exitText = adv.text
                    } else {
                        exitText = await model.exitAdviceText()
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
