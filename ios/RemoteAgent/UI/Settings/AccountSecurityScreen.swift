// Account security & recovery code rotation screens, driven strictly by Go `clientcore`'s
// `AccountSecurityViewModel` (Screen 53). Pending operations branch on `PBPendingSecurityOpKind`.
import RACore
import RAProto
import SwiftUI

var iosAuthorityNotice: String { L10n.securityIosTheIosAppHasNoBackgroundSecurityAlertsOpen() }

struct RecoveryPendingEntry: View {
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    var body: some View {
        let sec = context.core.settings.security
        if sec.isManager {
            if sec.recoveryPending {
                GroupBanner(
                    L10n.securitySomeoneIsRequestingManagementTakeoverWithARecoveryCode(
                        value: String(describing: securityTime(UInt64(max(0, sec.recoveryReadyMs))))
                    ),
                    tone: .warn,
                    action: L10n.dockHandle()
                ) { nav.open(.accountSecurity) }
            } else if sec.recoveryStatusStale {
                GroupBanner(L10n.errorsIosRecoveryRequestStateMayBeOutOfDateConnect(), tone: .warn)
            }
        }
    }
}

struct AccountSecurityScreen: View {
    var initialSubpage: PBAccountSecuritySubpage = .overview
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var confirmDiscard = false
    @State private var confirmPublish = false

    var body: some View {
        let vm = context.core.accountSecurity
        let showPending = !app.isDemo && vm.subpage == .pendingOp && vm.hasPendingOp_p
        let showRotate = app.isDemo ? (initialSubpage == .rotateRecovery) : (vm.subpage == .rotateRecovery)
        let inSubpage = !app.isDemo && (showPending || showRotate) && initialSubpage == .overview
        let navTitle = showPending
            ? L10n.securityUnfinishedSecurityOperation()
            : (showRotate ? L10n.securityRecoveryCode() : L10n.securityAccountSecurity())
        SettingsPage {
            if showPending {
                pendingOpBody(vm)
            } else if showRotate {
                RecoveryCodeContent(vm: vm, onRequestPublish: { confirmPublish = true })
            } else {
                overviewBody(vm)
            }
            if !showRotate {
                SecurityBanner(banner: vm.errorBanner) {
                    Task { await context.coreHost.accountSecurityAction(.refresh) }
                }
                .padding(.horizontal, 16).padding(.top, 8)
                let success = vm.successNotice.rendered()
                if !success.isEmpty {
                    GroupBanner(success)
                }
            }
        }
        .navBar(navTitle)
        .navigationBarBackButtonHidden(inSubpage)
        .toolbar {
            if inSubpage {
                ToolbarItem(placement: .topBarLeading) {
                    Button {
                        Task { await context.coreHost.accountSecurityAction(.setSubpage, subpage: .overview) }
                    } label: {
                        SymbolImage("chevron.left", size: 22, color: DS.blue)
                    }
                    .accessibilityLabel(L10n.commonBack())
                }
            }
        }
        .task {
            await context.coreHost.openSettings()
            guard !app.isDemo else { return }
            await context.coreHost.accountSecurityOpen(subpage: initialSubpage)
        }
        .onDisappear {
            guard !app.isDemo else { return }
            let host = context.coreHost
            Task { await host.accountSecurityClose() }
        }
        .confirm(
            isPresented: confirmDiscard,
            title: L10n.securityIosAbandonThisSecurityOperation(),
            text: discardConfirmText(for: vm.pendingKind),
            confirm: L10n.newSessionIosAbandon(),
            danger: true,
            busy: vm.busy,
            onCancel: { confirmDiscard = false },
            onConfirm: {
                confirmDiscard = false
                Task { await context.coreHost.accountSecurityAction(.discardPending) }
            }
        )
        .confirm(
            isPresented: confirmPublish,
            title: L10n.securityIosPublishNewRecoveryCode(),
            text: L10n.securityIosTheOldRecoveryCodeIsImmediatelyInvalidatedAndPending(),
            confirm: L10n.securityIosPublish(),
            busy: vm.busy,
            onCancel: { confirmPublish = false },
            onConfirm: {
                confirmPublish = false
                Task { await context.coreHost.accountSecurityAction(.publishRotation) }
            }
        )
    }

    private func discardConfirmText(for kind: PBPendingSecurityOpKind) -> String {
        switch kind {
        case .handoffOffer:
            return L10n.securityIosTheSuccessorSPublicKeyMayAlreadyBePublished()
        case .pairingDelivery:
            return L10n.securityIosTheApprovedCandidateWithoutConfirmedDeliveryWillBeExplicitly()
        case .none, .stateChange, .lease, .recoveryRotation, .deleteRecords, .UNRECOGNIZED:
            return L10n.securityTheSecurityOperationSOutcomeIsUnverifiedContinueThe()
        }
    }

    private func openRotateSubpage() {
        Task { await context.coreHost.accountSecurityAction(.setSubpage, subpage: .rotateRecovery) }
    }

    @ViewBuilder private func overviewBody(_ vm: PBAccountSecurityViewModel) -> some View {
        let isManager = app.isDemo ? context.core.settings.security.isManager : vm.isManager
        let pendingRemove = vm.pendingRemoveBanner.rendered()
        if !pendingRemove.isEmpty {
            GroupBanner(pendingRemove, tone: .warn, action: L10n.commonRetry()) {
                Task { await context.coreHost.accountSecurityAction(.retryPendingRemove) }
            }
        }
        if vm.recoveryPending {
            let banner = vm.recoveryTakeoverBanner.rendered()
            if !banner.isEmpty {
                GroupBanner(banner, tone: .warn, action: vm.canVetoRecovery ? L10n.securityReject() : nil) {
                    openRotateSubpage()
                }
            }
        } else if vm.recoveryStatusStale && isManager {
            GroupBanner(L10n.errorsIosRecoveryRequestStateMayBeOutOfDateConnect(), tone: .warn)
        }
        if vm.needsRotationBanner {
            GroupBanner(
                L10n.securityIosRecoveryCodeBackupIsUnconfirmedReplaceTheCodeAnd(),
                tone: .warn,
                action: L10n.securityIosReplaceCode()
            ) { openRotateSubpage() }
        }
        if vm.takingOver {
            GroupBanner(L10n.securityIosCompletingTakeoverWaitingForRemoteControlRenewal())
        }
        let authErr = vm.authorityErrorBanner.rendered()
        if !authErr.isEmpty {
            GroupBanner(authErr, tone: .error)
        }
        if vm.hasPendingOp_p {
            GroupCard {
                GRow(L10n.securityUnfinishedSecurityOperation(), chevron: true) {
                    Task { await context.coreHost.accountSecurityAction(.setSubpage, subpage: .pendingOp) }
                }
            }
        }
        let mgrText = app.isDemo
            ? (context.core.settings.security.isManager ? L10n.securityThisDevice() : context.core.settings.security.authorityName)
            : vm.managerDisplay.rendered().or(L10n.securityClient())
        let leaseText = app.isDemo
            ? securityTime(UInt64(max(0, context.core.settings.security.leaseEndMs)))
            : vm.leaseExpiryText.rendered()
        GroupCard {
            GRow(L10n.securityCurrentManagingClient(), value: mgrText)
            GRow(L10n.securityRemoteControlExpiry(), value: leaseText)
            if isManager {
                GRow(
                    vm.busy ? L10n.securityIosRenewing() : L10n.securityRenewNow(),
                    action: true,
                    enabled: vm.canRenewNow && !vm.busy
                ) {
                    Task { await context.coreHost.accountSecurityAction(.renewLease) }
                }
            }
        }
        GroupNote(L10n.securityIosAutomaticallyRenewsWhenYouReturnToTheManagingClient())
        GroupNote(iosAuthorityNotice)
        let advice = vm.leaseRenewalAdvice.rendered()
        if !isManager && !advice.isEmpty {
            GroupNote(advice)
        }
        GroupCard {
            if isManager {
                GRow(L10n.securityRecoveryCode(), chevron: true) {
                    if app.isDemo { nav.open(.recoveryCode) } else { openRotateSubpage() }
                }
                GRow(L10n.securityTransferManagement(), chevron: true) { nav.open(.handoff) }
            } else if vm.canOpenHandoff || (app.isDemo && context.core.settings.hasAccount_p && !context.core.settings.security.unlinked) {
                GRow(L10n.securityTakeOverManagement(), chevron: true) { nav.open(.handoff) }
            }
        }
    }

    @ViewBuilder private func pendingOpBody(_ vm: PBAccountSecurityViewModel) -> some View {
        GroupNote(L10n.securityTheSecurityOperationSOutcomeIsUnverifiedContinueThe())
        Btn(L10n.securityIosContinueOriginalOperation(), kind: .primary, enabled: vm.canContinuePending && !vm.busy) {
            Task { await context.coreHost.accountSecurityAction(.continuePending) }
        }
        .padding(16)
        if vm.canDiscardPending {
            let label: String = switch vm.pendingKind {
            case .handoffOffer:
                L10n.securityAbandonThisSuccessionPreparationSecurity()
            case .pairingDelivery:
                L10n.securityIosRemoveUndeliveredCandidate()
            case .none, .stateChange, .lease, .recoveryRotation, .deleteRecords, .UNRECOGNIZED:
                L10n.securityAbandonUnsignedOperation()
            }
            TextAction(label, color: DS.red, enabled: !vm.busy) {
                if vm.discardRequiresConfirm {
                    confirmDiscard = true
                } else {
                    Task { await context.coreHost.accountSecurityAction(.discardPending) }
                }
            }
            .padding(.horizontal, 16)
        }
    }
}

struct RecoveryCodeFigure: View {
    let code: String
    @Binding var saved: Bool
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(code).font(.footnote.monospaced()).ink(DS.ink).textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading).padding(16)
                .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.small))
            Hint(L10n.securitySaveTheRecoveryCodeOfflineAnyoneWithItCan())
            Toggle(L10n.securityISavedTheNewRecoveryCodeOffline(), isOn: $saved).font(.body).ink(DS.ink).tint(DS.blue)
        }
        .padding(16)
    }
}

struct RecoveryCodeContent: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    let vm: PBAccountSecurityViewModel
    let onRequestPublish: () -> Void

    var body: some View {
        let isManager = app.isDemo ? context.core.settings.security.isManager : vm.isManager
        let addHint = context.core.settings.addDeviceHint.rendered()
            .or(L10n.securityAddOnAuthority(name: context.core.settings.security.authorityName))
        if isManager {
            RecoveryPendingEntry()
            GroupLabel(L10n.securityRecoveryCode())
            GroupNote(L10n.securityReplacingTheRecoveryCodeImmediatelyInvalidatesTheOldCode())
            #if DEBUG
            if app.isDemo { AuthorityDemoCode(handoff: false) }
            #endif
            if !app.isDemo {
                if !vm.preparedCode.isEmpty {
                    RecoveryCodeFigure(
                        code: vm.preparedCode,
                        saved: Binding(
                            get: { vm.codeSavedChecked },
                            set: { checked in
                                Task { await context.coreHost.accountSecurityAction(.setRotationSaved, flag: checked) }
                            }
                        )
                    )
                    Btn(
                        L10n.securitySavedPublishReplacement(),
                        kind: .primary,
                        enabled: vm.canPublishRotation && !vm.busy
                    ) {
                        onRequestPublish()
                    }
                    .padding(.horizontal, 16)
                } else {
                    Btn(
                        L10n.securityGenerateNewRecoveryCode(),
                        kind: .primary,
                        enabled: vm.canPrepareRotation && !vm.busy
                    ) {
                        Task { await context.coreHost.accountSecurityAction(.prepareRotation) }
                    }
                    .padding(16)
                }
                let success = vm.successNotice.rendered()
                if !success.isEmpty { GroupBanner(success) }
                SecurityBanner(banner: vm.errorBanner)
                    .padding(.horizontal, 16).padding(.top, 8)
            }
        } else {
            GroupNote(addHint)
        }
    }
}
