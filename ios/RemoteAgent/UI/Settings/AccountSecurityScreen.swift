import Observation
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
                GroupBanner(L10n.securitySomeoneIsRequestingManagementTakeoverWithARecoveryCode(value: String(describing: securityTime(UInt64(max(0, sec.recoveryReadyMs))))),
                    tone: .warn, action: L10n.dockHandle()) { nav.open(.accountSecurity) }
            } else if sec.recoveryStatusStale {
                GroupBanner(L10n.errorsIosRecoveryRequestStateMayBeOutOfDateConnect(), tone: .warn)
            }
        }
    }
}

struct AccountSecurityDetails: View {
    var pending: Bool
    var onPending: () -> Void
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var action = ActionRunner()
    @State private var renewed = false
    var body: some View {
        let settings = context.core.settings
        let sec = settings.security
        if sec.recoveryPending {
            GroupBanner(L10n.securitySomeoneIsRequestingManagementTakeoverWithARecoveryCode(value: String(describing: securityTime(UInt64(max(0, sec.recoveryReadyMs))))), tone: .warn, action: sec.isManager ? L10n.securityReject() : nil) { nav.open(.recoveryCode) }
        }
        if app.recoveryRotationRequired {
            GroupBanner(L10n.securityIosRecoveryCodeBackupIsUnconfirmedReplaceTheCodeAnd(), tone: .warn, action: L10n.securityIosReplaceCode()) { nav.open(.recoveryCode) }
        }
        if sec.isManager && sec.leaseEndMs == 0 && sec.hasHandoffOffer_p {
            GroupBanner(L10n.securityIosCompletingTakeoverWaitingForRemoteControlRenewal())
        }
        if pending { GroupCard { GRow(L10n.securityUnfinishedSecurityOperation(), chevron: true) { onPending() } } }
        GroupCard {
            GRow(L10n.securityCurrentManagingClient(), value: sec.isManager ? L10n.securityThisDevice() : sec.authorityName)
            GRow(L10n.securityRemoteControlExpiry(), value: securityTime(UInt64(max(0, sec.leaseEndMs))))
            if sec.isManager {
                GRow(action.running ? L10n.securityIosRenewing() : L10n.securityRenewNow(), action: true, enabled: !action.running) {
                    renewed = false
                    action.run {
                        await context.remote.renewLeaseNow()
                        let st = try await context.remote.getSecurityStatus()
                        guard !st.hasPendingOp else {
                            throw UserError(L10nMessage.securityIosRenewalResultUnverifiedContinueTheOriginalOperation())
                        }
                    } onDone: { renewed = true }
                }
            }
        }
        GroupNote(L10n.securityIosAutomaticallyRenewsWhenYouReturnToTheManagingClient())
        GroupNote(iosAuthorityNotice)
        if !sec.isManager {
            let sync = context.core.syncState
            let advice = sync.hasLeaseRenewalAdvice
                ? sync.leaseRenewalAdvice.rendered()
                : L10n.securityOpenToRenew(value: String(describing: sec.authorityName))
            if !advice.isEmpty { GroupNote(advice) }
        }
        GroupCard {
            if sec.isManager {
                GRow(L10n.securityRecoveryCode(), chevron: true) { nav.open(.recoveryCode) }
                GRow(L10n.securityTransferManagement(), chevron: true) { nav.open(.handoff) }
            } else if settings.hasAccount_p && !sec.unlinked {
                GRow(L10n.securityTakeOverManagement(), chevron: true) { nav.open(.handoff) }
            }
        }
        if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
        if renewed { GroupBanner(L10n.securityIosRemoteControlRenewed()) }
    }
}

struct AccountSecurityScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @State private var pending: PBSecurityPendingOpSummary?
    @State private var pendingRemoveNotice: PBCoreText?
    @State private var action = ActionRunner()
    @State private var readError = ""
    @State private var showPending = false
    @State private var abandon = false
    var body: some View {
        SettingsPage {
            if !showPending, let notice = pendingRemoveNotice {
                let text = notice.rendered()
                if !text.isEmpty {
                    GroupBanner(text, tone: .warn, action: L10n.commonRetry()) {
                        action.run {
                            try await context.remote.resumeSecurityOperation()
                            await context.remote.refreshDevices()
                            await read()
                        }
                    }
                }
            }
            if !showPending { AccountSecurityDetails(pending: pending != nil) { showPending = true } }
            if showPending, let pending {
                GroupNote(L10n.securityTheSecurityOperationSOutcomeIsUnverifiedContinueThe())
                Btn(L10n.securityIosContinueOriginalOperation(), kind: .primary, enabled: !action.running) {
                    action.run {
                        try await context.remote.resumeSecurityOperation()
                        if pending.hasDelivery_p && !pending.deliveryPairingID.isEmpty {
                            _ = try await context.remote.finishPairingDelivery(pairingId: pending.deliveryPairingID)
                        }
                        await read()
                    }
                }.padding(16)
                if pending.kind == "offer" {
                    TextAction(L10n.securityAbandonThisSuccessionPreparationSecurity(), color: DS.red) { abandon = true }.disabled(action.running).padding(.horizontal, 16)
                } else if pending.hasDelivery_p {
                    TextAction(L10n.securityIosRemoveUndeliveredCandidate(), color: DS.red) { abandon = true }.padding(.horizontal, 16)
                }
            }
            if !readError.isEmpty { GroupBanner(readError, tone: .error, action: L10n.commonRetry()) { Task { await read() } } }
            if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
        }
        .navBar(showPending ? L10n.securityUnfinishedSecurityOperation() : L10n.securityAccountSecurity())
        .navigationBarBackButtonHidden(showPending)
        .toolbar {
            if showPending {
                ToolbarItem(placement: .topBarLeading) {
                    Button { showPending = false } label: { SymbolImage("chevron.left", size: 22, color: DS.blue) }.accessibilityLabel(L10n.commonBack())
                }
            }
        }
        .task {
            await context.coreHost.openSettings()
            await read()
        }
        .confirm(isPresented: $abandon, title: L10n.securityIosAbandonThisSecurityOperation(),
            text: pending?.kind == "offer"
                ? L10n.securityIosTheSuccessorSPublicKeyMayAlreadyBePublished()
                : L10n.securityIosTheApprovedCandidateWithoutConfirmedDeliveryWillBeExplicitly(), confirm: L10n.newSessionIosAbandon(), danger: true) {
                action.run {
                    guard let pending else { return }
                    if pending.kind == "offer" {
                        try await context.remote.discardPendingOp(abandonHandoff: true)
                    } else if !pending.deliveryCandidateID.isEmpty {
                        try await context.remote.removeDevice(pending.deliveryCandidateID)
                        if !pending.deliveryPairingID.isEmpty {
                            context.memory.value { AuthorityPairingSessions() }.discard(pairingID: pending.deliveryPairingID)
                        }
                    } else {
                        try await context.remote.discardPendingOp()
                    }
                    await read()
                }
            }
    }
    private func read() async {
        guard !app.isDemo else { return }
        do {
            let st = try await context.remote.getSecurityStatus()
            pending = st.hasPendingOp ? st.pendingOp : nil
            pendingRemoveNotice = st.hasPendingRemoveNotice ? st.pendingRemoveNotice : nil
            readError = ""
            if pending == nil { showPending = false }
        } catch { readError = errorText(error) }
    }
}

/// Displayed only in memory. A failed write keeps the same material and the confirmation.
@MainActor @Observable
final class RecoveryCodeDraft {
    var code: String?
    var saved = false
    var success = false
    var offered = false
    var canPublish: Bool { code != nil && saved }
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
        }.padding(16)
    }
}

struct RecoveryCodeScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @State private var draft = RecoveryCodeDraft()
    @State private var action = ActionRunner()
    @State private var confirm = false
    var body: some View {
        let settings = context.core.settings
        let isManager = settings.security.isManager
        let addHint = settings.addDeviceHint.rendered().or(L10n.securityAddOnAuthority(name: settings.security.authorityName))
        SettingsPage {
            if isManager {
                RecoveryPendingEntry()
                GroupLabel(L10n.securityRecoveryCode())
                GroupNote(L10n.securityReplacingTheRecoveryCodeImmediatelyInvalidatesTheOldCode())
                #if DEBUG
                if app.isDemo { AuthorityDemoCode(handoff: false) }
                #endif
                if let code = draft.code {
                    RecoveryCodeFigure(code: code, saved: $draft.saved)
                    Btn(L10n.securitySavedPublishReplacement(), kind: .primary, enabled: draft.canPublish && !action.running) { confirm = true }
                        .padding(.horizontal, 16)
                } else if !app.isDemo {
                    Btn(L10n.securityGenerateNewRecoveryCode(), kind: .primary, enabled: !action.running) {
                        action.run {
                            draft.code = try await context.remote.prepareRecoveryRotation()
                            draft.saved = false
                            draft.success = false
                        }
                    }.padding(16)
                }
                if draft.success { GroupBanner(L10n.securityIosTheNewRecoveryCodeIsActiveTheOldCode()) }
                if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
            } else { GroupNote(addHint) }
        }
        .navBar(L10n.securityRecoveryCode())
        .task {
            await context.coreHost.openSettings()
            draft = context.memory.value { RecoveryCodeDraft() }
        }
        .confirm(isPresented: $confirm, title: L10n.securityIosPublishNewRecoveryCode(), text: L10n.securityIosTheOldRecoveryCodeIsImmediatelyInvalidatedAndPending(), confirm: L10n.securityIosPublish()) {
            action.run {
                guard draft.code != nil, draft.saved else { throw UserError(L10nMessage.securityIosSaveTheRecoveryCodeOfflineFirst()) }
                try await context.remote.confirmRecoveryRotation()
                let st = try await context.remote.getSecurityStatus()
                guard !st.hasPendingOp else {
                    throw UserError(L10nMessage.securityIosPublicationResultUnverifiedContinueTheOriginalOperation())
                }
                await context.remote.refreshDevices()
                app.recoveryRotationRequired = false
                draft.success = true; draft.code = nil; draft.saved = false
            }
        }
    }
}
