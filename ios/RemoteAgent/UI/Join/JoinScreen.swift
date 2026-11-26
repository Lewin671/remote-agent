// First-run onboarding and pairing flow, driven strictly by Go `clientcore`'s
// `OnboardingViewModel` (Screen 51).
import RACore
import RAProto
import SwiftUI

struct LegacyRepairBanner: View {
    let show: Bool
    let dismiss: () -> Void

    var body: some View {
        if show {
            Banner(
                L10n.securityUpgradeRepairTitle() + "\n" + L10n.securityUpgradeRepairNotice(),
                tone: .warn,
                action: L10n.commonGotIt(),
                onAction: dismiss
            )
            .padding(.horizontal, 16)
            .padding(.bottom, 12)
        }
    }
}

struct SecurityBanner: View {
    let banner: PBSecurityBannerView
    var onAction: (() -> Void)? = nil

    private var tone: BannerTone {
        switch banner.tone {
        case .error: .error
        case .warn: .warn
        case .info, .unspecified, .UNRECOGNIZED: .plain
        }
    }

    private var actionLabel: String? {
        switch banner.action {
        case .retry: L10n.commonRetry()
        case .refill: L10n.securityReEnter()
        case .restart: L10n.securityStartOver()
        case .none, .UNRECOGNIZED: nil
        }
    }

    var body: some View {
        let text = banner.hasText ? banner.text.rendered() : ""
        if banner.visible && !text.isEmpty {
            if let label = actionLabel, let onAction {
                Banner(text, tone: tone, action: label, onAction: onAction)
            } else {
                Banner(text, tone: tone)
            }
        }
    }
}

/// First run: entry, create, join, or emergency recovery — routed strictly by `OnboardingViewModel.mode`.
struct JoinScreen: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        let vm = app.screens.onboarding
        Group {
            switch vm.mode {
            case .entry, .unspecified, .UNRECOGNIZED:
                OnboardingEntryView(vm: vm)
            case .create:
                CreateAccountScreen(vm: vm)
            case .recovery:
                EmergencyRecoveryScreen(vm: vm)
            case .join:
                CoreJoinFlow(vm: vm.join, showLegacyRepairBanner: vm.showLegacyRepairBanner, canBack: vm.canBack)
            }
        }
        .task {
            await LaunchDriver.driveOnboarding(app)
        }
    }
}

private struct OnboardingEntryView: View {
    @Environment(AppModel.self) private var app
    let vm: PBOnboardingViewModel

    var body: some View {
        Page {
            ScrollView {
                VStack(alignment: .leading, spacing: 28) {
                    VStack(alignment: .leading, spacing: 0) {
                        AppMark.badge(size: 56, radius: 14)
                        Text("remote-agent")
                            .font(.largeTitle.bold())
                            .foregroundStyle(DS.ink)
                            .padding(.top, 18)
                        Text(L10n.securityControlAiCodingClisOnYourComputerFromAny())
                            .font(.body)
                            .foregroundStyle(DS.ink2)
                            .fixedSize(horizontal: false, vertical: true)
                            .padding(.top, 8)
                    }
                    .padding(.init(top: 28, leading: 24, bottom: 0, trailing: 24))
                    Spacer(minLength: 0)
                    LegacyRepairBanner(show: vm.showLegacyRepairBanner) {
                        Task { await app.coreHost.appRootDismissUpgradeNotice() }
                    }
                    VStack(spacing: 12) {
                        VStack(spacing: 0) {
                            EntryRow(
                                title: L10n.securityCreateAccount(),
                                subtitle: L10n.securityThisDeviceBecomesTheManagingClient()
                            ) {
                                Task { await app.coreHost.onboardingSetMode(.create) }
                            }
                            Rectangle().fill(DS.sep).frame(height: 0.5).padding(.leading, 18)
                            EntryRow(
                                title: L10n.securityJoinAnExistingAccount(),
                                subtitle: L10n.securityJoinWithACodeFromTheManagingClient()
                            ) {
                                Task { await app.coreHost.onboardingSetMode(.join) }
                            }
                            Rectangle().fill(DS.sep).frame(height: 0.5).padding(.leading, 18)
                            EntryRow(
                                title: L10n.securityRecoverManagementWithARecoveryCode(),
                                subtitle: L10n.securityUseWhenTheManagingClientIsLost()
                            ) {
                                Task { await app.coreHost.onboardingSetMode(.recovery) }
                            }
                        }
                        .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.group, style: .continuous))
                        .padding(.horizontal, 16)
                        Text(L10n.securityEntryE2EeNote())
                            .font(.footnote)
                            .foregroundStyle(DS.ink2)
                            .multilineTextAlignment(.center)
                            .padding(.horizontal, 24)
                        PrivacyPolicyLink().frame(maxWidth: .infinity)
                    }
                }
                .padding(.vertical, 24)
                .frame(maxWidth: 480)
                .containerRelativeFrame(.vertical)
                .frame(maxWidth: .infinity)
            }
            .scrollBounceBehavior(.basedOnSize)
        }
    }
}

/// Core-driven join/re-pair flow rendering `PBJoinPairingView`.
struct CoreJoinFlow: View {
    @Environment(AppModel.self) private var app
    let vm: PBJoinPairingView
    let showLegacyRepairBanner: Bool
    let canBack: Bool
    @State private var scanning = false

    var body: some View {
        if scanning {
            QRScanner(
                onResult: { raw in
                    scanning = false
                    Task { await app.coreHost.onboardingJoinSubmit(rawLink: raw) }
                },
                onCancel: { scanning = false }
            )
        } else {
            Page {
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        if vm.isExistingRepair {
                            JoinText(text: L10n.securityIosTheOriginalDeviceIdentityKeysAndHistoryArePreserved())
                        }
                        switch vm.step {
                        case .form, .unspecified, .UNRECOGNIZED:
                            JoinFormView(
                                vm: vm,
                                showLegacyRepairBanner: showLegacyRepairBanner,
                                onOpenScanner: { scanning = true }
                            )
                        case .working:
                            JoinWorkingView(vm: vm)
                        case .compareSas, .waitingApproval:
                            JoinCompareView(vm: vm)
                        }
                        if vm.showSetupReset {
                            Button(L10n.securityExitAndStartOver(), role: .destructive) {
                                Task { await app.coreHost.exitRequest(intent: .setupReset) }
                            }
                            .padding(.horizontal, 16)
                            .padding(.top, 12)
                        }
                    }
                    .frame(maxWidth: 480)
                    .frame(maxWidth: .infinity)
                }
                .scrollDismissesKeyboard(.interactively)
                .scrollBounceBehavior(.basedOnSize)
            }
            .safeAreaInset(edge: .bottom) {
                if canBack {
                    TextAction(L10n.commonBack(), alignment: .center) {
                        Task { await app.coreHost.onboardingSetMode(.entry) }
                    }
                }
            }
            .debugSignal("join.cancel") {
                Task { await app.coreHost.onboardingJoinCancel() }
            }
            .debugSignal("join.confirm") {
                Task { await app.coreHost.onboardingJoinConfirmSAS() }
            }
        }
    }
}

private struct JoinTitle: View {
    let text: String

    var body: some View {
        Text(text).font(.largeTitle.bold()).ink(DS.ink)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.init(top: 24, leading: 16, bottom: 6, trailing: 16))
            .accessibilityAddTraits(.isHeader)
    }
}

private struct JoinText: View {
    let text: String

    var body: some View {
        Text(text).font(.subheadline).ink(DS.ink2)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16).padding(.bottom, 16)
    }
}

private struct JoinLink: View {
    let text: String
    let action: () -> Void

    var body: some View {
        TextAction(text, style: .subheadline, alignment: .center, action: action).frame(maxWidth: .infinity)
    }
}

private struct JoinFormView: View {
    @Environment(AppModel.self) private var app
    let vm: PBJoinPairingView
    let showLegacyRepairBanner: Bool
    let onOpenScanner: () -> Void
    @State private var localServer = ""
    @State private var lastServerEditSeq: UInt64 = 0

    var body: some View {
        JoinTitle(text: vm.isExistingRepair ? L10n.securityOneTimeSecuritySetup() : L10n.securityJoinAccount())
        LegacyRepairBanner(show: showLegacyRepairBanner) {
            Task { await app.coreHost.appRootDismissUpgradeNotice() }
        }
        JoinText(text: L10n.securityIosOnTheManagingClientOpenSettingsAddDeviceScan())
        SecurityBanner(banner: vm.banner) {
            Task { await app.coreHost.onboardingJoinBannerAction() }
        }
        .padding(.horizontal, 16).padding(.bottom, 10)
        PairingCodeField(
            formattedCode: vm.formattedCode,
            settledEditSeq: app.screens.settledOnboardingEditSeq,
            placeholder: L10n.securityIosEnter8DigitPairingCode(),
            onEdit: { raw in
                let seq = app.coreHost.nextOnboardingEditSeq()
                Task { await app.coreHost.onboardingJoinEdit(codeInput: raw, seq: seq) }
                return seq
            },
            onSubmit: {
                if vm.canSubmit {
                    Task { await app.coreHost.onboardingJoinSubmit() }
                }
            }
        )
        .padding(.horizontal, 16).padding(.bottom, 10)
        InputField(
            L10n.securityServer(),
            text: Binding(
                get: { localServer },
                set: { next in
                    let trimmed = next.trimmingCharacters(in: .whitespacesAndNewlines)
                    localServer = trimmed
                    let seq = app.coreHost.nextOnboardingEditSeq()
                    lastServerEditSeq = seq
                    Task { await app.coreHost.onboardingJoinEdit(serverInput: trimmed, seq: seq) }
                }
            ),
            placeholder: "https://",
            keyboard: .URL,
            contentType: .URL,
            submitLabel: .join
        ) {
            if vm.canSubmit {
                Task { await app.coreHost.onboardingJoinSubmit() }
            }
        }
        .disabled(!vm.serverEditable)
        .padding(.horizontal, 16).padding(.bottom, 10)
        .onChange(of: vm.serverInput, initial: true) { _, next in
            if app.screens.settledOnboardingEditSeq >= lastServerEditSeq && localServer != next {
                localServer = next
            }
        }
        Btn(
            vm.pendingRegistration ? L10n.commonRetry() : L10n.securityJoin(),
            kind: .primary,
            enabled: vm.canSubmit && !vm.busy
        ) {
            Task { await app.coreHost.onboardingJoinSubmit() }
        }
        .padding(.horizontal, 16).padding(.top, 6)
        JoinLink(text: L10n.securityIosScanQrCode(), action: onOpenScanner).padding(.top, 8)
    }
}

private struct JoinWorkingView: View {
    @Environment(AppModel.self) private var app
    let vm: PBJoinPairingView

    var body: some View {
        JoinTitle(text: vm.isExistingRepair ? L10n.securityOneTimeSecuritySetup() : L10n.securityJoinAccount())
        if vm.busy {
            IconText(Mark.progress, L10n.securityIosConnecting())
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 16).padding(.top, 4)
        }
        SecurityBanner(banner: vm.banner) {
            Task { await app.coreHost.onboardingJoinBannerAction() }
        }
        .padding(.horizontal, 16).padding(.top, 12)
        if vm.canCancel {
            Btn(L10n.commonCancel()) {
                Task { await app.coreHost.onboardingJoinCancel() }
            }
            .padding(.horizontal, 16).padding(.top, 24)
        }
    }
}

private struct JoinCompareView: View {
    @Environment(AppModel.self) private var app
    let vm: PBJoinPairingView
    @ScaledMetric(relativeTo: .largeTitle) private var size: CGFloat = 40

    var body: some View {
        let emoji = vm.sasEmoji
        let comparisonPending = vm.step == .compareSas
        ZStack {
            Text(L10n.securityComparePatterns()).font(.body.weight(.semibold)).ink(DS.ink).accessibilityAddTraits(.isHeader)
            HStack {
                Spacer()
                if vm.canCancel {
                    TextAction(L10n.commonCancel(), style: .body, alignment: .center) {
                        Task { await app.coreHost.onboardingJoinCancel() }
                    }
                    .padding(.trailing, 8)
                }
            }
        }
        .frame(maxWidth: .infinity, minHeight: DS.minHit)
        HStack(spacing: 12) {
            ForEach(Array(emoji.enumerated()), id: \.offset) { _, e in
                Text(e).font(.system(size: min(size, 64)))
            }
        }
        .lineLimit(1).minimumScaleFactor(0.5)
        .frame(maxWidth: .infinity)
        .padding(.top, 40).padding(.bottom, 12)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(L10n.securityIosPairPatterns(patterns: emoji.joined(separator: L10n.commonIosSpokenSeparator())))
        Text(L10n.securityIosCompareTheManagingClientS4PatternsIfThey())
            .font(.subheadline).ink(DS.ink2).multilineTextAlignment(.center)
            .frame(maxWidth: .infinity).padding(.horizontal, 24)
        if comparisonPending {
            Btn(L10n.securityPatternsMatchTrustAndContinue(), kind: .primary, enabled: vm.canConfirmSas && !vm.busy) {
                Task { await app.coreHost.onboardingJoinConfirmSAS() }
            }
            .padding(.horizontal, 16).padding(.top, 16)
        }
        IconText(
            Mark.progress,
            comparisonPending ? L10n.securityConfirmThePatternsOnThisDeviceFirst() : L10n.securityIosWaitingForManagingClientApprovalAndBinding()
        )
        .frame(maxWidth: .infinity).padding(.top, 24)
        SecurityBanner(banner: vm.banner) {
            Task { await app.coreHost.onboardingJoinBannerAction() }
        }
        .padding(.horizontal, 16).padding(.top, 16)
        VStack(spacing: 0) {
            Btn(L10n.securityPatternsDifferReturnToCodeEntry(), enabled: vm.canCancel) {
                Task { await app.coreHost.onboardingJoinCancel() }
            }
            Hint(L10n.securityReturningStopsThisWaitRejectTheOldRequestOn())
        }
        .padding(.init(top: 24, leading: 16, bottom: 16, trailing: 16))
    }
}

private struct EntryRow: View {
    let title: String
    let subtitle: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 12) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.body)
                        .foregroundStyle(DS.ink)
                        .fixedSize(horizontal: false, vertical: true)
                    Text(subtitle)
                        .font(.footnote)
                        .foregroundStyle(DS.ink2)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                Arrow(DS.ink3)
            }
            .padding(.init(top: 12, leading: 18, bottom: 12, trailing: 16))
            .frame(minHeight: 50)
            .contentShape(Rectangle())
        }
        .buttonStyle(RowPressStyle())
    }
}
