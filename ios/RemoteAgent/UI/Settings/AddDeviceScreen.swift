// Adding a device, driven strictly by Go `clientcore`'s `AddDeviceViewModel` (Screen 52)
// and Core's managed `TIMER_KIND_SECURITY_POLL` timers. Zero iOS polling loops live here.
import RACore
import RAProto
import SwiftUI
import UIKit

struct AddDeviceScreen: View {
    var replacing = ""
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav

    var body: some View {
        let vm = context.core.addDevice
        let isManager = app.isDemo ? context.core.settings.isManager : (vm.step != .notManager)
        let nonManagerHint = vm.nonManagerHint.rendered()
            .or(context.core.settings.addDeviceHint.rendered())
            .or(L10n.securityAddOnAuthority(name: context.core.settings.authorityName))
        SettingsPage {
            if !isManager {
                GroupNote(nonManagerHint)
            } else {
                GroupNote(L10n.securityComputerRunRemoteAgentAccountJoinAndEnterThe())
                #if DEBUG
                if app.isDemo {
                    AuthorityDemoPairCode(server: "https://relay.example.com")
                }
                #endif
                if !app.isDemo {
                    stepContent(vm)
                    SecurityBanner(banner: vm.banner) {
                        switch vm.banner.action {
                        case .retry:
                            Task { await context.coreHost.addDeviceAction(.retry) }
                        case .refill, .restart:
                            Task { await context.coreHost.addDeviceAction(.generate) }
                        case .none, .UNRECOGNIZED:
                            break
                        }
                    }
                    .padding(.horizontal, 16).padding(.top, 8)
                }
            }
        }
        .navBar(L10n.homeAddDevice())
        .debugSignal("schedule.pair.start") {
            #if DEBUG
            guard URL(string: context.account.server)?.host == "127.0.0.1" else { return }
            Task {
                await context.coreHost.addDeviceAction(.generate)
                let deadline = ContinuousClock.now + .seconds(10)
                while ContinuousClock.now < deadline {
                    let raw = context.core.addDevice.rawCode
                    if !raw.isEmpty {
                        ScheduleAcceptance.write("pair-code", raw)
                        return
                    }
                    try? await Task.sleep(nanoseconds: 50_000_000)
                }
            }
            #endif
        }
        .debugSignal("schedule.pair.approve") {
            #if DEBUG
            guard URL(string: context.account.server)?.host == "127.0.0.1" else { return }
            Task {
                let deadline = ContinuousClock.now + .seconds(15)
                while ContinuousClock.now < deadline {
                    let cur = context.core.addDevice
                    if cur.step == .compareSas && !cur.sasEmoji.isEmpty {
                        ScheduleAcceptance.write("pair-sas", cur.sasEmoji.joined(separator: " "))
                        await context.coreHost.addDeviceAction(.setSasConfirmed, flag: true)
                        await context.coreHost.addDeviceAction(.approve)
                        return
                    }
                    try? await Task.sleep(nanoseconds: 50_000_000)
                }
            }
            #endif
        }
        .task {
            await context.coreHost.openSettings()
            guard !app.isDemo else { return }
            await context.coreHost.addDeviceOpen(
                replacesDeviceID: replacing,
                webOrigin: webOrigin(server: context.account.server, preferred: ""),
                autoStart: false
            )
        }
        .onDisappear {
            guard !app.isDemo else { return }
            let host = context.coreHost
            let rep = replacing
            Task { await host.addDeviceClose(replacesDeviceID: rep) }
        }
    }

    @ViewBuilder private func stepContent(_ vm: PBAddDeviceViewModel) -> some View {
        switch vm.step {
        case .notManager:
            EmptyView()
        case .idle, .unspecified, .UNRECOGNIZED:
            Btn(L10n.securityGeneratePairingCode(), kind: .primary, enabled: vm.canGenerate && !vm.busy) {
                Task { await context.coreHost.addDeviceAction(.generate) }
            }
            .padding(16)
        case .generating:
            LoadingRow()
        case .waitingJoiner:
            VStack(spacing: 12) {
                if !vm.qrURL.isEmpty {
                    QRCodeView(text: vm.qrURL, label: L10n.securityQrCodeForAddingADevice())
                }
                Text(vm.formattedCode)
                    .font(.title2.monospaced().weight(.semibold))
                    .ink(DS.ink)
                    .textSelection(.enabled)
                let validText = vm.validUntilText.rendered()
                if !validText.isEmpty {
                    Text(validText).font(.footnote).ink(DS.ink2)
                }
                Btn(L10n.securityIosCopyPairingCode()) {
                    UIPasteboard.general.string = vm.rawCode
                }
                Btn(L10n.securityCancelPairing(), enabled: vm.canCancel && !vm.busy) {
                    Task { await context.coreHost.addDeviceAction(.cancel) }
                }
            }
            .padding(16)
        case .compareSas:
            GroupLabel(L10n.securityComparePatterns())
            Text(vm.sasEmoji.joined(separator: " "))
                .font(.system(size: 36)).minimumScaleFactor(0.5).lineLimit(1)
                .frame(maxWidth: .infinity).padding(16)
            GroupNote(L10n.securityCompareThe4PatternsOnTheNewDeviceConfirm())
            if !vm.replacementCandidates.isEmpty {
                let headerLabel = vm.replaceOldLabel.rendered().or(L10n.securityAlsoRemoveOldDevices())
                let hintText = vm.replaceOldHint.rendered()
                GroupCard {
                    Text(headerLabel)
                        .font(.body.weight(.medium))
                        .ink(DS.ink)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 14)
                        .padding(.top, 12)
                        .padding(.bottom, hintText.isEmpty ? 6 : 4)
                    if !hintText.isEmpty {
                        Text(hintText)
                            .font(.subheadline)
                            .ink(DS.ink2)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.horizontal, 14)
                            .padding(.bottom, 8)
                    }
                    ForEach(vm.replacementCandidates, id: \.deviceID) { cand in
                        let sub = cand.subtitle.rendered()
                        Toggle(
                            isOn: Binding(
                                get: { cand.selected },
                                set: { selected in
                                    Task {
                                        await context.coreHost.addDeviceAction(
                                            .toggleRemoveCandidate,
                                            flag: selected,
                                            deviceID: cand.deviceID
                                        )
                                    }
                                }
                            )
                        ) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(cand.title).font(.subheadline.weight(.semibold)).ink(DS.ink).lineLimit(1)
                                if !sub.isEmpty {
                                    Text(sub).font(.footnote).ink(DS.ink2).lineLimit(2)
                                }
                            }
                        }
                        .padding(.horizontal, 14)
                        .padding(.vertical, 8)
                    }
                }
                .padding(.bottom, 12)
            }
            Toggle(
                L10n.securityPatternsMatchTheNewDeviceConfirmedFirst(),
                isOn: Binding(
                    get: { vm.sasConfirmedChecked },
                    set: { checked in
                        Task { await context.coreHost.addDeviceAction(.setSasConfirmed, flag: checked) }
                    }
                )
            )
            .font(.body).ink(DS.ink).tint(DS.blue)
            .padding(.horizontal, 16).padding(.bottom, 12)
            Btn(L10n.securityAllowToJoin(), kind: .primary, enabled: vm.canApprove && !vm.busy) {
                Task { await context.coreHost.addDeviceAction(.approve) }
            }
            .padding(.horizontal, 16)
            Btn(L10n.approvalDeny(), kind: .negative, enabled: vm.canCancel && !vm.busy) {
                Task { await context.coreHost.addDeviceAction(.cancel) }
            }
            .padding(16)
        case .approving, .delivering:
            if !vm.sasEmoji.isEmpty {
                GroupLabel(L10n.securityComparePatterns())
                Text(vm.sasEmoji.joined(separator: " "))
                    .font(.system(size: 36)).minimumScaleFactor(0.5).lineLimit(1)
                    .frame(maxWidth: .infinity).padding(16)
            }
            GroupNote(L10n.securityIosApprovedVerifyingDeliveryToTheDeviceContinueTheOriginal())
            if vm.canRetry {
                Btn(L10n.commonRetry(), kind: .secondary, enabled: !vm.busy) {
                    Task { await context.coreHost.addDeviceAction(.retry) }
                }
                .padding(16)
            }
        case .done:
            GroupBanner(L10n.securityIosDeviceJoined(), tone: .plain)
            let warning = vm.removeOldWarning.rendered()
            if !warning.isEmpty {
                GroupBanner(warning, tone: .warn)
            }
            Btn(L10n.securityIosAddAnotherDevice(), kind: .primary, enabled: vm.canGenerate && !vm.busy) {
                if replacing.isEmpty {
                    Task { await context.coreHost.addDeviceAction(.generate) }
                } else {
                    nav.open(.addDevice())
                }
            }
            .padding(16)
        case .failed:
            if vm.canRetry {
                Btn(L10n.commonRetry(), kind: .primary, enabled: !vm.busy) {
                    Task { await context.coreHost.addDeviceAction(.retry) }
                }
                .padding(.horizontal, 16).padding(.top, 12)
            }
            if vm.canGenerate {
                TextAction(L10n.securityIosGenerateAnotherPairingCode(), enabled: !vm.busy) {
                    Task { await context.coreHost.addDeviceAction(.generate) }
                }
                .padding(.horizontal, 16).padding(.top, 8)
            }
        }
    }
}
